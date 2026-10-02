package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/probe"
	"mtxui/internal/store"
)

// Headers for every response. Everything the UI loads comes from the sidecar itself: no inline script or style, no
// data: URIs. Permissions-Policy leaves WebAuthn (passkeys) and fullscreen video to the site itself.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; " +
	"connect-src 'self'; media-src 'self' blob:; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

const permissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), hid=(), " +
	"bluetooth=(), display-capture=(), publickey-credentials-get=(self), publickey-credentials-create=(self), " +
	"fullscreen=(self), picture-in-picture=(self)"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin") // not no-referrer: browsers then send Origin: null on same-origin POSTs
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if r.TLS != nil { // the sidecar's own HTTPS (MTXUI_TLS=acme); behind a proxy, the proxy decides
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // the sentinel is compared by identity, as net/http does
					panic(v)
				}
				s.d.Log.Error("panic", "err", v, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "Internal error.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLog logs state-changing requests and failures at info, everything else at debug. It logs the path only:
// query strings can carry secrets (MediaMTX's HLS session ids).
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		level := slog.LevelDebug
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400 || !auth.SafeMethod(r.Method):
			level = slog.LevelInfo
		}
		s.d.Log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds(), "ip", clientip.From(r.Context()).IP.String())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// mismatch remembers recent requests that did not arrive the way PUBLIC_URL says they should.
type mismatch struct {
	mu       sync.Mutex
	scheme   time.Time
	host     time.Time
	hostSeen string
}

const mismatchMemory = 15 * time.Minute

// watchProxy notes signed-in requests that arrive over plain HTTP although PUBLIC_URL is https, or for another host:
// the reverse proxy or TRUSTED_PROXIES is misconfigured, and cookies and origin checks will misbehave. Only requests
// with a session count: anyone can send any Host header (internet scanners send "host:443" to every site), and that
// must not put a warning in front of the admins. A browser keeps sending its cookie through a misconfigured proxy.
func (s *Server) watchProxy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := current(r.Context()); !ok {
			next.ServeHTTP(w, r)
			return
		}
		info := clientip.From(r.Context())
		now := time.Now()
		s.mismatch.mu.Lock()
		if s.d.Settings.Secure() && info.Scheme != "https" {
			s.mismatch.scheme = now
		}
		if !sameHost(info.Host, s.d.Settings.PublicURL.Host, s.d.Settings.PublicURL.Scheme) {
			s.mismatch.host, s.mismatch.hostSeen = now, info.Host
		}
		s.mismatch.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// sameHost compares two host[:port] values the way a browser means them: case-insensitive, with the scheme's
// default port the same as none ("host:443" is a valid way to name an https host).
func sameHost(a, b, scheme string) bool {
	def := ":443"
	if scheme == "http" {
		def = ":80"
	}
	return strings.EqualFold(strings.TrimSuffix(a, def), strings.TrimSuffix(b, def))
}

func (s *Server) mismatchWarnings(now time.Time) []probe.Warning {
	s.mismatch.mu.Lock()
	defer s.mismatch.mu.Unlock()
	var out []probe.Warning
	if !s.mismatch.scheme.IsZero() && now.Sub(s.mismatch.scheme) < mismatchMemory {
		out = append(out, probe.Warning{Code: "proxy_scheme", Message: "PUBLIC_URL is https, but requests arrive over " +
			"plain http. Check that the reverse proxy sends X-Forwarded-Proto and that it is listed in MTXUI_TRUSTED_PROXIES."})
	}
	if !s.mismatch.host.IsZero() && now.Sub(s.mismatch.host) < mismatchMemory {
		out = append(out, probe.Warning{Code: "proxy_host", Message: fmt.Sprintf("Requests arrive for host %q, but "+
			"PUBLIC_URL is %s. Sign-in cookies and origin checks follow PUBLIC_URL.", s.mismatch.hostSeen, s.d.Settings.Origin())})
	}
	return out
}

// unsafeGate refuses to serve while the startup safety check fails (MediaMTX's API answers anonymously). Health
// stays reachable, so monitoring and the healthcheck see the sidecar alive.
func (s *Server) unsafeGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reason := s.d.Probe.Unsafe()
		if reason == "" || r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusServiceUnavailable, "unsafe", reason)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(reason + "\n"))
	})
}

type sessionKey struct{}

type currentSession struct {
	session store.Session
	user    store.User
	token   string
}

func current(ctx context.Context) (*currentSession, bool) {
	c, ok := ctx.Value(sessionKey{}).(*currentSession)
	return c, ok
}

// RoleOf reports the signed-in user's role, for the MediaMTX API proxy.
func RoleOf(r *http.Request) (auth.Role, bool) {
	c, ok := current(r.Context())
	if !ok {
		return "", false
	}
	return auth.Role(c.user.Role), true
}

// actor names the signed-in user for the audit log.
func actor(r *http.Request) (string, *int64) {
	c, ok := current(r.Context())
	if !ok {
		return "", nil
	}
	return c.user.Username, &c.user.ID
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.d.Sessions.TokenFrom(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		sess, user, err := s.d.Sessions.Lookup(r.Context(), token)
		switch {
		case err == nil:
			r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &currentSession{sess, user, token}))
		case errors.Is(err, auth.ErrNoSession):
			s.d.Sessions.ClearCookie(w) // a stale cookie: tidy it away
		default:
			s.d.Log.Error("session lookup failed", "err", err) // carry on signed out: protected routes answer 401
		}
		next.ServeHTTP(w, r)
	})
}

// csrf guards every state-changing request: it must come from the UI's own origin, and, when a session is attached,
// carry that session's CSRF token. Endpoints that work without a session also demand JSON (decodeJSON), which a
// cross-site form cannot send.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.SafeMethod(r.Method) || externalRTC(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !auth.SameOrigin(r, s.d.Settings.Origin()) {
			writeError(w, http.StatusForbidden, "csrf", "Cross-origin request refused.")
			return
		}
		if c, ok := current(r.Context()); ok && !auth.ValidCSRF(r, c.session.CSRFToken) {
			writeError(w, http.StatusForbidden, "csrf", "Missing or wrong CSRF token.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fromMediaMTX lets only MediaMTX (and loopback, for tests) call the internal authentication endpoint. Anything else
// on the stack network could otherwise use it to test guesses with made-up client addresses, past the throttle.
func (s *Server) fromMediaMTX(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		a, perr := netip.ParseAddr(host)
		if err != nil || perr != nil || !s.mediamtx.allowed(r.Context(), a.Unmap()) {
			s.d.Log.Warn("internal auth endpoint called by a stranger", "peer", r.RemoteAddr)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// peers resolves MediaMTX's hostname, caching the answer and keeping the last good one when DNS fails.
type peers struct {
	host   string
	mu     sync.Mutex
	addrs  []netip.Addr
	at     time.Time
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func newPeers(host string) *peers {
	return &peers{host: host, lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
}

func (p *peers) allowed(ctx context.Context, a netip.Addr) bool {
	if a.IsLoopback() {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	known := slices.Contains(p.addrs, a)
	// Refresh every 30 s, and at most once a second when an unknown peer turns up (MediaMTX restarted elsewhere).
	if since := time.Since(p.at); since > 30*time.Second || (!known && since > time.Second) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if addrs, err := p.lookup(ctx, p.host); err == nil {
			for i := range addrs {
				addrs[i] = addrs[i].Unmap()
			}
			p.addrs = addrs
		}
		p.at = time.Now()
		known = slices.Contains(p.addrs, a)
	}
	return known
}

// Request bodies: how long a client may take to send one. The server has no ReadTimeout, which would also cut the
// long-lived responses (the event stream, the log tail, downloads); instead a request with a body gets this long to
// send it, and the deadline is lifted once the body has been read. Uploads extend it (extendBodyDeadline).
var bodyReadTimeout = 30 * time.Second

// bodyDeadline sets the read deadline for a request's body.
func bodyDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
			rc := http.NewResponseController(w)
			if rc.SetReadDeadline(time.Now().Add(bodyReadTimeout)) == nil {
				r.Body = &deadlineBody{ReadCloser: r.Body, rc: rc}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// deadlineBody lifts the read deadline once the body is read to its end (or fails, or is closed): the connection's
// background read then waits for the next request without a deadline that would cancel this one.
type deadlineBody struct {
	io.ReadCloser
	rc   *http.ResponseController
	done bool
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.lift()
	}
	return n, err
}

func (b *deadlineBody) Close() error {
	b.lift()
	return b.ReadCloser.Close()
}

func (b *deadlineBody) lift() {
	if !b.done {
		b.done = true
		_ = b.rc.SetReadDeadline(time.Time{})
	}
}

// extendBodyDeadline gives an upload longer to arrive than bodyReadTimeout.
func extendBodyDeadline(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(d))
}
