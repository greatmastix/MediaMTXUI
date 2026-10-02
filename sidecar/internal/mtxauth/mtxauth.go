// Package mtxauth answers MediaMTX's authentication requests (authMethod: http). Every decision is local:
// memory, the SQLite database and constant-time comparisons. The handler never waits on the network, because MediaMTX
// stalls each new connection for up to 10 s per call while it waits.
package mtxauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/credentials"
	"mtxui/internal/store"
)

// Request is the body MediaMTX POSTs for every authentication. password and query arrive in the clear
// (RTMP clients put credentials in the query), so neither is ever logged.
type Request struct {
	IP        string  `json:"ip"`
	User      string  `json:"user"`
	Password  string  `json:"password"`
	Token     string  `json:"token"`
	Action    string  `json:"action"`
	Path      string  `json:"path"`
	Protocol  string  `json:"protocol"`
	ID        *string `json:"id"`
	Query     string  `json:"query"`
	UserAgent string  `json:"userAgent"`
}

var knownActions = []string{"publish", "read", "playback", "api", "metrics", "pprof"}

// SidecarUser is the username of the sidecar's own principal.
const SidecarUser = credentials.ReservedPrefix + "sidecar"

// Principal is the sidecar's own credential for MediaMTX's API, metrics and playback servers. Its secret exists only
// in this process's memory, and it is accepted only from the sidecar's own addresses.
type Principal struct {
	secret string
	addrs  []netip.Addr
}

// NewPrincipal creates the principal with a fresh secret and the host's current interface addresses.
func NewPrincipal() (*Principal, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &Principal{secret: base64.RawURLEncoding.EncodeToString(b), addrs: localAddrs()}, nil
}

func localAddrs() []netip.Addr {
	var out []netip.Addr
	ifaddrs, _ := net.InterfaceAddrs()
	for _, a := range ifaddrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

// Authorize adds the principal's credentials to a request for MediaMTX.
func (p *Principal) Authorize(req *http.Request) { req.SetBasicAuth(SidecarUser, p.secret) }

// Own reports whether ip is one of the sidecar's addresses.
func (p *Principal) Own(ip netip.Addr) bool { return slices.Contains(p.addrs, ip) }

func (p *Principal) matches(password string) bool {
	return subtle.ConstantTimeCompare([]byte(password), []byte(p.secret)) == 1
}

// Handler serves POST /internal/auth.
type Handler struct {
	principal *Principal
	creds     *credentials.Service
	throttle  *auth.Lockout
	log       *slog.Logger
	now       func() time.Time
	opened    opened
	// Viewers are the sidecar's viewer tickets for its HLS and WHEP proxies.
	Viewers *Viewers
	// Public reports whether a path is a public stream: anyone may read it without credentials (never publish).
	// Set once, before serving.
	Public func(path string) bool
	// OnPublish, when set, hears of every publish MediaMTX lets through (the log notes name refused encoders only by
	// address; this ties an address to a path). Set once, before serving.
	OnPublish func(path string, ip netip.Addr)
}

// Throttling: failed attempts that carry credentials, per client address (IPv6 per /64). Anonymous probes are not
// counted: every RTSP client makes one before sending credentials.
const (
	throttleFailures = 20
	throttleWindow   = 5 * time.Minute
)

// NewHandler returns the handler.
func NewHandler(p *Principal, creds *credentials.Service, log *slog.Logger) *Handler {
	return &Handler{
		principal: p, creds: creds, throttle: auth.NewLockout(throttleFailures, throttleWindow), log: log, now: time.Now,
		Viewers: NewViewers(),
	}
}

// Decision is the outcome of one authentication.
type Decision struct {
	Allow  bool
	Reason string
	Who    string // the principal or credential name, for logs
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Decisions come from memory and the local database; the timeout only guards against a wedged database.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	d := h.Decide(ctx, req)
	if d.Allow {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
}

// Decide authenticates one request from MediaMTX.
func (h *Handler) Decide(ctx context.Context, req Request) Decision {
	ip, err := netip.ParseAddr(req.IP)
	if err != nil {
		return h.deny(req, "", "invalid client address", false)
	}
	ip = ip.Unmap()
	if !slices.Contains(knownActions, req.Action) {
		return h.deny(req, "", "unknown action", false)
	}
	if req.User == "" && req.Token == "" {
		// A public stream may be watched by anyone: reading only, and only that path.
		if req.Action == "read" && h.Public != nil && h.Public(req.Path) {
			return Decision{Allow: true, Who: "public"}
		}
		return Decision{Reason: "anonymous"} // expected before every credentialed RTSP attempt; not logged, not counted
	}
	own := h.principal.Own(ip)
	key := clientip.RateKey(ip)
	if !own {
		if locked, _ := h.throttle.Locked(key); locked {
			return h.deny(req, req.User, "too many failed attempts from this address", false)
		}
	}

	if req.User == SidecarUser {
		switch {
		case !h.principal.matches(req.Password):
			return h.fail(req, key, own, SidecarUser, "wrong sidecar secret")
		case !own:
			return h.deny(req, SidecarUser, "sidecar principal used from a foreign address", true)
		case req.Action != "api" && req.Action != "metrics" && req.Action != "playback":
			return h.deny(req, SidecarUser, "sidecar principal is not for "+req.Action, true)
		}
		return Decision{Allow: true, Who: SidecarUser}
	}

	if isViewer(req.User) {
		reason, uiUser := h.Viewers.check(req.User, req.Password, req.Action, req.Path, ip)
		if reason != "" {
			return h.fail(req, key, own, req.User, reason)
		}
		return Decision{Allow: true, Who: "viewer " + uiUser}
	}

	c, found, err := h.lookup(ctx, req)
	if err != nil {
		h.log.Error("auth: credential lookup failed", "err", err)
		return Decision{Reason: "internal error"}
	}
	if !found {
		return h.fail(req, key, own, req.User, "unknown credential or wrong secret")
	}
	if err := h.creds.Check(c, req.Action, req.Path, ip, h.now()); err != nil {
		return h.deny(req, c.Name, err.Error(), true)
	}
	h.creds.Touch(c.ID)
	if req.ID != nil {
		h.opened.add(c.ID, SessionRef{Protocol: req.Protocol, ID: *req.ID}, h.now())
	}
	if !own {
		h.throttle.Succeed(key)
	}
	if req.Action == "publish" && h.OnPublish != nil {
		h.OnPublish(req.Path, ip)
	}
	return Decision{Allow: true, Who: c.Name}
}

// lookup follows S1's rules: a non-empty user means user and password (token then only repeats the password);
// otherwise token is a bearer token.
func (h *Handler) lookup(ctx context.Context, req Request) (store.Credential, bool, error) {
	if req.User != "" {
		return h.creds.ByPassword(ctx, req.User, req.Password)
	}
	// WHIP clients such as OBS send only a bearer token, so a name/secret credential travels as "name:secret" (stream
	// pages show it that way). Token secrets never contain a colon.
	if name, secret, ok := strings.Cut(req.Token, ":"); ok {
		return h.creds.ByPassword(ctx, name, secret)
	}
	return h.creds.ByToken(ctx, req.Token)
}

// fail records a failed attempt with credentials against the client's address, then denies.
func (h *Handler) fail(req Request, key string, own bool, who, reason string) Decision {
	if !own && h.throttle.Fail(key) {
		h.log.Warn("auth: throttling address after repeated failures", "ip", req.IP, "for", throttleWindow.String())
	}
	return h.deny(req, who, reason, true)
}

func (h *Handler) deny(req Request, who, reason string, log bool) Decision {
	if log {
		// Never log password, token or query: they carry secrets.
		h.log.Info("auth: denied", "ip", req.IP, "action", req.Action, "path", req.Path, "protocol", req.Protocol,
			"who", who, "reason", reason)
	}
	return Decision{Reason: reason, Who: who}
}

// SessionsOf returns, and forgets, the sessions credential id has authenticated since the sidecar started.
func (h *Handler) SessionsOf(id int64) []SessionRef { return h.opened.take(id) }
