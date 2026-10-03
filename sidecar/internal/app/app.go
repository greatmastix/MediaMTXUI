// Package app is the sidecar's HTTP layer: the public listener (the UI, the JSON API and the MediaMTX API proxy) and
// the internal listener (MediaMTX's authentication callback and the container healthcheck), with their middleware
// and handlers.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/credentials"
	"mtxui/internal/live"
	"mtxui/internal/liveproxy"
	"mtxui/internal/logs"
	"mtxui/internal/mtxconf"
	"mtxui/internal/mtxlog"
	"mtxui/internal/netguard"
	"mtxui/internal/portgate"
	"mtxui/internal/probe"
	"mtxui/internal/settings"
	"mtxui/internal/setup"
	"mtxui/internal/store"
)

// Deps are the components the HTTP layer uses.
type Deps struct {
	Settings *settings.Settings
	Store    *store.Store
	Sessions *auth.Sessions
	Hasher   *auth.Hasher
	Setup    *setup.Service
	Probe    *probe.Prober
	Audit    *audit.Recorder
	Live     *live.Hub // MediaMTX's live state, for the event stream and the charts
	Config   *mtxconf.Writer
	NetGuard *netguard.Guard // vets the addresses config edits point MediaMTX at
	Creds    *credentials.Service
	Opened   Opened           // which sessions a credential authenticated
	Kicker   Kicker           // closes MediaMTX sessions
	MTX      MTXAPI           // MediaMTX's Control API with the sidecar's credentials (forward states, recordings)
	Playback Playback         // MediaMTX's playback server with the sidecar's credentials (recording spans, exports)
	Notes    *mtxlog.Notes    // what MediaMTX's log says about encoders (refused, B-frames)
	Logs     *logs.Hub        // the log viewer's live tails and the sidecar's own recent lines
	Watch    *liveproxy.Proxy // the live view's HLS and WHEP proxies
	Exposure *portgate.Client // exposure control: desired.json out, mtx-portgate's status in
	Proxy    http.Handler     // the MediaMTX API proxy; it sees paths with /api/mtx stripped
	MTXAuth  http.Handler     // MediaMTX's authentication callback
	UI       http.Handler     // the embedded single-page app
	Restart  func()           // ends the process so it starts again (a restore); the container's restart policy brings it back
	Log      *slog.Logger
}

// MTXAPI reads MediaMTX's Control API and deletes recordings through it.
type MTXAPI interface {
	Get(ctx context.Context, path string) (int, []byte, error)
	Delete(ctx context.Context, path string) (int, error)
}

// Playback reads MediaMTX's playback server: small answers (Get) and streamed ones (Open).
type Playback interface {
	Get(ctx context.Context, path string) (int, []byte, error)
	Open(ctx context.Context, path string) (*http.Response, error)
}

// Server holds the HTTP layer's state: limiters, open event streams, the route registry and recent reverse-proxy
// mismatches.
type Server struct {
	d         Deps
	resolver  *clientip.Resolver
	loginRate *auth.RateLimiter
	setupRate *auth.RateLimiter
	lockout   *auth.Lockout
	mediamtx  *peers
	mismatch  mismatch
	streams   streamCount
	owners    owners
	// sessionNudge wakes the event streams to recheck their sessions (sign-out, deleted users).
	sessionNudge nudge
	auto         autoExpose // automatic exposure: leases and what was last opened
	forwardMu    sync.Mutex // one forwarding change at a time: each rewrites the path's whole forward list
	holdingMu    sync.Mutex // one holding clip upload at a time
	followed     sync.Map   // stream id -> when its holding version last followed its encoder
	rec          recordingsState
	flows        authFlows     // second-factor sign-ins, passkey ceremonies, TOTP setups in progress
	recWake      chan struct{} // runs the recordings budget now (after a deletion)
	bk           backupState
	routes       []Route
}

// New returns the server.
func New(d Deps) *Server {
	perMinute := d.Settings.LoginRatePerMinute
	return &Server{
		d:         d,
		resolver:  clientip.New(d.Settings.TrustedProxies),
		recWake:   make(chan struct{}, 1),
		loginRate: auth.NewRateLimiter(perMinute, max(5, perMinute/2)),
		setupRate: auth.NewRateLimiter(10, 5),
		lockout:   auth.NewLockout(d.Settings.LockoutThreshold, d.Settings.LockoutDuration),
		mediamtx:  newPeers(d.Settings.MediaMTXHost),
	}
}

// HealthURL is the loopback URL of the internal /healthz endpoint for a given internal listen address.
func HealthURL(internalListen string) (string, error) {
	host, port, err := net.SplitHostPort(internalListen)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends the API's error shape: a machine-readable kind and a sentence for people.
func writeError(w http.ResponseWriter, code int, kind, message string) {
	writeJSON(w, code, map[string]string{"error": kind, "message": message})
}

// Run serves the listeners until ctx is cancelled, then shuts them down gracefully.
func Run(ctx context.Context, l Listeners, public, internal http.Handler, log *slog.Logger) error {
	type entry struct {
		srv *http.Server
		tls bool
	}
	entries := []entry{{newServer(l.Public, public, log), l.PublicTLS != nil}, {newServer(l.Internal, internal, log), false}}
	if l.PublicTLS != nil {
		entries[0].srv.TLSConfig = l.PublicTLS
		plain := newServer(l.HTTP, l.HTTPHandler, log)
		plain.ReadTimeout, plain.WriteTimeout = 10*time.Second, 10*time.Second // challenges and redirects only
		entries = append(entries, entry{plain, false})
	}
	servers := make([]*http.Server, len(entries))
	for i, e := range entries {
		servers[i] = e.srv
	}
	errc := make(chan error, len(entries))
	var lc net.ListenConfig
	for _, e := range entries {
		ln, err := lc.Listen(ctx, "tcp", e.srv.Addr)
		if err != nil {
			shutdown(servers, log)
			return err
		}
		log.Info("listening", "addr", ln.Addr().String(), "tls", e.tls)
		go func() {
			if e.tls {
				errc <- e.srv.ServeTLS(ln, "", "")
			} else {
				errc <- e.srv.Serve(ln)
			}
		}()
	}
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			shutdown(servers, log)
			return err
		}
	}
	shutdown(servers, log)
	return nil
}

func newServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	// No WriteTimeout: SSE and streamed downloads (later phases) are long-lived responses.
	return &http.Server{
		Addr:              addr,
		Handler:           closeAfterChunked(h),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// closeAfterChunked ends the connection after every request with a chunked body. A request with both Content-Length
// and Transfer-Encoding must end its connection (RFC 9112 6.3), or a proxy in front that framed it by the length
// could slip a second request past it; net/http drops the Content-Length before handlers see the request, so every
// chunked request counts. Browsers do not send chunked bodies, and a reverse proxy re-frames what it forwards.
func closeAfterChunked(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TransferEncoding) > 0 {
			w.Header().Set("Connection", "close")
		}
		next.ServeHTTP(w, r)
	})
}

func shutdown(servers []*http.Server, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(ctx); err != nil {
			log.Warn("shutdown", "addr", s.Addr, "err", err)
		}
	}
}
