package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/credentials"
	"mtxui/internal/live"
	"mtxui/internal/liveproxy"
	"mtxui/internal/logs"
	"mtxui/internal/mtxapi"
	"mtxui/internal/mtxauth"
	"mtxui/internal/mtxconf"
	"mtxui/internal/mtxlog"
	"mtxui/internal/netguard"
	"mtxui/internal/portgate"
	"mtxui/internal/probe"
	"mtxui/internal/settings"
	"mtxui/internal/setup"
	"mtxui/internal/store"
	"mtxui/internal/webui"
)

const (
	origin = "https://mtx.example.com"
	caddy  = "172.29.42.1" // the trusted reverse proxy
	client = "203.0.113.7" // the real client behind it
)

type okChecker struct{}

func (okChecker) Validate(context.Context, []byte) error { return nil }

type principal struct{}

func (principal) Authorize(r *http.Request) { r.SetBasicAuth("mtxui-sidecar", "s") }

// harness is a server wired from real components, with a fake MediaMTX behind the probe and stubs for the proxy and
// the MediaMTX auth callback.
type harness struct {
	t        *testing.T
	srv      *Server
	public   http.Handler
	st       *store.Store
	setup    *setup.Service
	token    string
	mtx      *httptest.Server
	anonCode int                         // what the fake MediaMTX answers anonymous API requests with
	paths    string                      // the fake MediaMTX's paths list items, a JSON array
	lists    map[string]string           // other lists' items by API path (e.g. /v3/rtmpconns/list), JSON arrays
	routes   map[string]http.HandlerFunc // the fake MediaMTX's own answers by path (the playback server, deletions)
	hub      *live.Hub
	dir      string
	exposure *portgate.Client
	creds    *credentials.Service
	opened   map[int64][]mtxauth.SessionRef // what the fake authentication endpoint saw
	kicks    []string                       // kick operations called
	proxied  []*http.Request
	mu       sync.Mutex
	cookie   string
	csrf     string
}

func newHarness(t *testing.T, env map[string]string, params auth.Params) *harness {
	t.Helper()
	h := &harness{t: t, anonCode: http.StatusUnauthorized, paths: "[]", opened: map[int64][]mtxauth.SessionRef{}, lists: map[string]string{}, routes: map[string]http.HandlerFunc{}}
	vars := map[string]string{
		"MTXUI_PUBLIC_URL": origin, "MTXUI_PUBLIC_HOST": "mtx.example.com", "MTXUI_TRUSTED_PROXIES": caddy,
		"MTXUI_LOCKOUT_THRESHOLD": "3", "MTXUI_LOGIN_RATE_PER_MINUTE": "600", "MTXUI_EXPOSURE_CONTROL": "on",
	}
	for k, v := range env {
		vars[k] = v
	}
	cfg, err := settings.Load(func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	h.dir = dir
	h.st, err = store.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.st.Close() })
	_ = os.Mkdir(filepath.Join(dir, "config"), 0o750)
	writer := &mtxconf.Writer{
		Path: filepath.Join(dir, "config", "mediamtx.yml"), LockPath: filepath.Join(dir, "lock"),
		Rules: mtxconf.Rules{AuthURL: cfg.AuthURL()}, Checker: okChecker{}, Store: h.st,
	}
	hasher := auth.NewHasher(params, 2)
	h.setup = &setup.Service{
		Store: h.st, Hasher: hasher, Writer: writer, TokenPath: filepath.Join(dir, "setup-token"),
		Seed: func(in setup.Ingest) ([]byte, error) {
			return mtxconf.Seed(mtxconf.SeedParams{MediaMTXVersion: "1.21.1", AuthURL: cfg.AuthURL(), PublicHost: cfg.PublicHost, RTSP: in.RTSP})
		},
	}
	if h.token, err = h.setup.EnsureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.mtx = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			w.WriteHeader(h.anonCode)
			return
		}
		h.mu.Lock()
		route := h.routes[r.URL.Path]
		h.mu.Unlock()
		if route != nil {
			route(w, r)
			return
		}
		switch r.URL.Path {
		case "/v3/info":
			_, _ = io.WriteString(w, `{"version":"v1.21.1"}`)
		case "/v3/paths/list":
			h.mu.Lock()
			_, _ = io.WriteString(w, `{"pageCount":1,"items":`+h.paths+`}`)
			h.mu.Unlock()
		default:
			h.mu.Lock()
			items, ok := h.lists[r.URL.Path]
			h.mu.Unlock()
			if !ok {
				items = "[]"
			}
			_, _ = io.WriteString(w, `{"pageCount":1,"items":`+items+`}`)
		}
	}))
	t.Cleanup(h.mtx.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	key, err := credentials.LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	h.creds = credentials.New(h.st, key)
	ops, _ := mtxapi.Operations()
	if h.hub, err = live.New(h.mtx.URL, principal{}, ops, log); err != nil {
		t.Fatal(err)
	}
	_ = os.Mkdir(filepath.Join(dir, "portgate"), 0o700)
	h.exposure = &portgate.Client{Dir: filepath.Join(dir, "portgate"), StatusPath: filepath.Join(dir, "portgate-status", portgate.StatusFile)}
	ui, _ := webui.New(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>ui</title>")}})
	h.srv = New(Deps{
		Settings: cfg, Store: h.st, Sessions: auth.NewSessions(h.st, cfg.Secure(), cfg.SessionIdleTimeout, cfg.SessionMaxAge),
		Hasher: hasher, Setup: h.setup, Probe: probe.New(h.mtx.URL, principal{}, "1.21.1", "mtx.example.com", log),
		Audit: audit.NewRecorder(h.st, log), Live: h.hub, Config: writer, NetGuard: netguard.New(cfg.StackSubnet), UI: ui, Log: log,
		Creds: h.creds, Opened: fakeOpened{h}, Kicker: fakeKicker{h},
		MTX: &mtxconf.APIClient{Base: h.mtx.URL, Principal: principal{}}, Notes: mtxlog.New(), Logs: logs.NewHub(),
		Playback: &mtxconf.APIClient{Base: h.mtx.URL, Principal: principal{}},
		Watch:    liveproxy.New(h.mtx.URL, h.mtx.URL, mtxauth.NewViewers()),
		Exposure: h.exposure,
		Proxy: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.mu.Lock()
			h.proxied = append(h.proxied, r)
			h.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"path": r.URL.Path})
		}),
		MTXAuth: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	})
	h.public = h.srv.Public()
	return h
}

var fast = auth.Params{Memory: 8 * 1024, Time: 1, Threads: 1}

type reqOpt func(*http.Request)

func noCSRF(r *http.Request)        { r.Header.Del(auth.CSRFHeader) }
func fromOrigin(o string) reqOpt    { return func(r *http.Request) { r.Header.Set("Origin", o) } }
func contentType(ct string) reqOpt  { return func(r *http.Request) { r.Header.Set("Content-Type", ct) } }
func peer(addr string) reqOpt       { return func(r *http.Request) { r.RemoteAddr = addr } }
func header(k, v string) reqOpt     { return func(r *http.Request) { r.Header.Set(k, v) } }
func withoutHeader(k string) reqOpt { return func(r *http.Request) { r.Header.Del(k) } }
func (h *harness) signOutLocally()  { h.cookie, h.csrf = "", "" }
func decode(rec *httptest.ResponseRecorder) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m
}

// rawBody is a request body sent as it is (set its Content-Type with an option).
type rawBody []byte

// do sends a request as the browser behind Caddy would: from the trusted proxy with X-Forwarded-*, same-origin,
// with the session cookie and CSRF token once signed in.
func (h *harness) do(method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if raw, ok := body.(rawBody); ok {
		rd = bytes.NewReader(raw)
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "http://mtx.example.com"+path, rd)
	req.RemoteAddr = caddy + ":40000"
	req.Header.Set("X-Forwarded-For", client)
	req.Header.Set("X-Forwarded-Proto", "https")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !auth.SafeMethod(method) {
		req.Header.Set("Origin", origin)
	}
	if h.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "__Host-mtxui_session", Value: h.cookie})
		req.Header.Set(auth.CSRFHeader, h.csrf)
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.public.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-mtxui_session" {
			h.cookie = c.Value
		}
	}
	if m := decode(rec); m["csrfToken"] != nil {
		h.csrf, _ = m["csrfToken"].(string)
	}
	return rec
}

func (h *harness) completeSetup() {
	h.t.Helper()
	rec := h.do("POST", "/api/v1/setup", map[string]any{"token": h.token, "username": "admin", "password": "correct horse battery"})
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t, nil, fast)
	// Downloads and the event stream are GETs that a cookie may reach from another site (SameSite=Lax); their
	// answers must stay unreadable there: no CORS, and Cross-Origin-Resource-Policy same-origin.
	for _, path := range []string{
		"/", "/some/route", "/api/v1/health", "/api/v1/nope", "/api/v1/session", "/api/v1/backups/x.mtxbak/download",
		"/api/v1/audit/export", "/api/v1/logs/download", "/api/v1/recordings/export?path=test",
	} {
		rec := h.do("GET", path, nil, header("Origin", "https://evil.example"))
		hdr := rec.Header()
		if hdr.Get("Cross-Origin-Resource-Policy") != "same-origin" || hdr.Get("Access-Control-Allow-Origin") != "" ||
			hdr.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s: readable from another origin: %v", path, hdr)
		}
		if !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'self'") ||
			!strings.Contains(hdr.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
			hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Referrer-Policy") != "same-origin" ||
			!strings.Contains(hdr.Get("Permissions-Policy"), "publickey-credentials-get=(self)") ||
			hdr.Get("X-Frame-Options") != "DENY" || hdr.Get("Cross-Origin-Opener-Policy") != "same-origin" {
			t.Errorf("%s: headers %v", path, hdr)
		}
		if strings.Contains(hdr.Get("Content-Security-Policy"), "unsafe-inline") {
			t.Errorf("%s: CSP allows inline code", path)
		}
	}
}

// Every route on the public listener is registered with an access level, and every non-public one refuses
// anonymous requests.
func TestRouteRegistry(t *testing.T) {
	h := newHarness(t, nil, fast)
	registered := map[string]Access{}
	for _, r := range h.srv.Routes() {
		registered[r.Method+" "+r.Pattern] = r.Access
	}
	err := chi.Walk(h.public.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if _, ok := registered[method+" "+route]; !ok {
			if _, ok := registered["* "+route]; !ok {
				t.Errorf("%s %s is not registered with an access level", method, route)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range h.srv.Routes() {
		if r.Access == Public {
			continue
		}
		method, path := r.Method, strings.ReplaceAll(r.Pattern, "/*", "/v3/paths/list")
		if method == "*" {
			method = "GET"
		}
		if rec := h.do(method, path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s: %d, want 401", method, path, rec.Code)
		}
	}
}

func TestSetupFlow(t *testing.T) {
	h := newHarness(t, nil, fast)
	if m := decode(h.do("GET", "/api/v1/setup", nil)); m["required"] != true {
		t.Fatalf("setup not required on a fresh install: %v", m)
	}
	body := func(token, pw string) map[string]any {
		return map[string]any{"token": token, "username": "admin", "password": pw, "ingest": map[string]bool{"rtsp": true}}
	}
	if rec := h.do("POST", "/api/v1/setup", body("", "correct horse battery")); rec.Code != 401 {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/setup", body("AAAA-BBBB", "correct horse battery")); rec.Code != 401 {
		t.Errorf("wrong token: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/setup", body(h.token, "short")); rec.Code != 400 {
		t.Errorf("weak password: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("POST", "/api/v1/setup", body(h.token, "correct horse battery"), contentType("application/x-www-form-urlencoded")); rec.Code != 415 {
		t.Errorf("form-encoded: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/setup", body(h.token, "correct horse battery"), fromOrigin("https://evil.example")); rec.Code != 403 {
		t.Errorf("cross-origin: %d", rec.Code)
	}

	rec := h.do("POST", "/api/v1/setup", body(h.token, "correct horse battery"))
	if rec.Code != http.StatusCreated || h.cookie == "" || h.csrf == "" {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	if m := decode(h.do("GET", "/api/v1/session", nil)); m["user"].(map[string]any)["role"] != "admin" {
		t.Errorf("not signed in as admin after setup: %v", m)
	}
	if m := decode(h.do("GET", "/api/v1/setup", nil)); m["required"] != false {
		t.Errorf("setup still required: %v", m)
	}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/setup", body(h.token, "correct horse battery")); rec.Code != 404 {
		t.Errorf("setup again with the same token: %d, want 404", rec.Code)
	}
	if _, err := os.Stat(h.setup.TokenPath); !os.IsNotExist(err) {
		t.Error("the token file survived setup")
	}
}

func TestLoginLogout(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong password!"}); rec.Code != 401 {
		t.Errorf("wrong password: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/auth/login", map[string]string{"username": "nobody", "password": "whatever12345"}); rec.Code != 401 {
		t.Errorf("unknown user: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/auth/login", map[string]string{"username": "ADMIN", "password": "correct horse battery"}); rec.Code != 200 {
		t.Fatalf("right password: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != 200 {
		t.Fatalf("session: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/auth/logout", nil, noCSRF); rec.Code != 403 {
		t.Errorf("logout without the CSRF token: %d, want 403", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/auth/logout", nil, header(auth.CSRFHeader, "forged")); rec.Code != 403 {
		t.Errorf("logout with a wrong CSRF token: %d, want 403", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/auth/logout", nil); rec.Code != 204 {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != 401 {
		t.Errorf("session after logout: %d", rec.Code)
	}
}

func TestLockout(t *testing.T) {
	h := newHarness(t, nil, fast) // threshold 3
	h.completeSetup()
	h.signOutLocally()
	for range 3 {
		h.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong password!"})
	}
	rec := h.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "correct horse battery"})
	if rec.Code != 429 || decode(rec)["error"] != "locked" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("right password while locked: %d %s", rec.Code, rec.Body)
	}
	// Unknown names lock the same way, so a lock reveals nothing about which names exist.
	for range 3 {
		h.do("POST", "/api/v1/auth/login", map[string]string{"username": "mallory", "password": "wrong password!"})
	}
	if rec := h.do("POST", "/api/v1/auth/login", map[string]string{"username": "mallory", "password": "x"}); decode(rec)["error"] != "locked" {
		t.Errorf("an unknown name did not lock: %s", rec.Body)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newHarness(t, map[string]string{"MTXUI_LOGIN_RATE_PER_MINUTE": "1"}, fast) // bursts of 5
	var last *httptest.ResponseRecorder
	for i := range 6 {
		last = h.do("POST", "/api/v1/auth/login", map[string]string{"username": "u" + string(rune('a'+i)), "password": "whatever12345"})
	}
	if last.Code != 429 || decode(last)["error"] != "rate_limited" {
		t.Fatalf("6th attempt: %d %s", last.Code, last.Body)
	}
	// The limit is per client address (behind the trusted proxy, the X-Forwarded-For address).
	if rec := h.do("POST", "/api/v1/auth/login", map[string]string{"username": "x", "password": "whatever12345"}, header("X-Forwarded-For", "198.51.100.1")); rec.Code != 401 {
		t.Errorf("another client: %d", rec.Code)
	}
}

// Unknown usernames and wrong passwords must take the same time: both paths run exactly one argon2id verification.
func TestLoginTimingIsEqualised(t *testing.T) {
	h := newHarness(t, map[string]string{"MTXUI_LOCKOUT_THRESHOLD": "100"}, auth.Params{Memory: 32 * 1024, Time: 1, Threads: 1})
	h.completeSetup()
	h.signOutLocally()
	median := func(user string) time.Duration {
		var ds []time.Duration
		for i := range 15 {
			start := time.Now()
			h.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": "wrong password " + string(rune('a'+i))},
				header("X-Forwarded-For", "198.51.100."+string(rune('1'+i%9))))
			ds = append(ds, time.Since(start))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2]
	}
	unknown, wrong := median("nobody-here"), median("admin")
	diff := unknown - wrong
	if diff < 0 {
		diff = -diff
	}
	if diff > wrong/4 {
		t.Fatalf("unknown user %v, wrong password %v: more than 25%% apart", unknown, wrong)
	}
	t.Logf("median: unknown user %v, wrong password %v", unknown, wrong)
}

func TestCSRFOnProxiedMutations(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	kick := "/api/mtx/v3/rtsp/sessions/kick/0b3c5b3e-1111-4222-8333-944455556666"
	if rec := h.do("POST", kick, nil, noCSRF); rec.Code != 403 {
		t.Errorf("kick without CSRF: %d", rec.Code)
	}
	if rec := h.do("POST", kick, nil, withoutHeader("Origin")); rec.Code != 403 {
		t.Errorf("kick without Origin: %d", rec.Code)
	}
	if len(h.proxied) != 0 {
		t.Fatal("a refused request reached the proxy")
	}
	if rec := h.do("POST", kick, nil); rec.Code != 200 {
		t.Fatalf("kick: %d %s", rec.Code, rec.Body)
	}
	if got := h.proxied[0].URL.Path; got != "/v3/rtsp/sessions/kick/0b3c5b3e-1111-4222-8333-944455556666" {
		t.Errorf("the proxy saw %q", got)
	}
	if rec := h.do("GET", "/api/mtx/v3/paths/list", nil, noCSRF); rec.Code != 200 {
		t.Errorf("reads need no CSRF token: %d", rec.Code)
	}
}

// Audit entries carry the real client address: X-Forwarded-For from the trusted proxy, the peer otherwise.
func TestAuditRecordsRealClients(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	h.signOutLocally()
	h.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong password!"})
	h.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong password!"},
		peer("198.51.100.20:5555"), header("X-Forwarded-For", "6.6.6.6"))
	h.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "correct horse battery"})
	events, err := h.st.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Action+" "+e.Actor+" "+e.IP+" "+toString(e.Details["result"]))
	}
	want := []string{
		"auth.login admin 203.0.113.7 ok",
		"auth.login admin 198.51.100.20 failed", // an untrusted peer's X-Forwarded-For is ignored
		"auth.login admin 203.0.113.7 failed",
		"setup.complete admin 203.0.113.7 ok",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("audit log:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, e := range events {
		if strings.Contains(toString(e.Details), "horse") || strings.Contains(toString(e.Details), "wrong password") {
			t.Fatal("a password reached the audit log")
		}
	}
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

func TestUnsafeGate(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	h.anonCode = http.StatusOK // MediaMTX now answers anonymous API requests
	h.srv.d.Probe.Check(context.Background())
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != 503 || decode(rec)["error"] != "unsafe" {
		t.Errorf("API while unsafe: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("GET", "/", nil); rec.Code != 503 || !strings.Contains(rec.Body.String(), "without credentials") {
		t.Errorf("UI while unsafe: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("GET", "/api/v1/health", nil); rec.Code != 200 {
		t.Errorf("health while unsafe: %d", rec.Code)
	}
	h.anonCode = http.StatusUnauthorized
	h.srv.d.Probe.Check(context.Background())
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != 200 {
		t.Errorf("after recovery: %d", rec.Code)
	}
}

func TestProxyMismatchWarnings(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	h.srv.d.Probe.Check(context.Background())
	if w := decode(h.do("GET", "/api/v1/status", nil))["warnings"].([]any); len(w) != 0 {
		t.Fatalf("warnings on a correct setup: %v", w)
	}
	// Signed-out requests do not count: anyone can send any Host header.
	h.do("GET", "/", nil, header("X-Forwarded-Host", "evil.example"), withoutHeader("Cookie"))
	if w := decode(h.do("GET", "/api/v1/status", nil))["warnings"].([]any); len(w) != 0 {
		t.Fatalf("a signed-out request raised %v", w)
	}
	// The default port written out is the same host, and so is another case.
	h.do("GET", "/", nil, header("X-Forwarded-Host", "mtx.example.com:443"))
	h.do("GET", "/", nil, header("X-Forwarded-Host", "MTX.example.com"))
	if w := decode(h.do("GET", "/api/v1/status", nil))["warnings"].([]any); len(w) != 0 {
		t.Fatalf("warnings for the same host: %v", w)
	}
	h.do("GET", "/", nil, header("X-Forwarded-Host", "mtx.example.com:8443"))
	if w := decode(h.do("GET", "/api/v1/status", nil))["warnings"].([]any); len(w) != 1 {
		t.Fatalf("another port is another origin: %v", w)
	}
	h.do("GET", "/", nil, header("X-Forwarded-Proto", "http"))
	h.do("GET", "/", nil, header("X-Forwarded-Host", "10.0.0.5:8080"))
	codes := []string{}
	for _, w := range decode(h.do("GET", "/api/v1/status", nil))["warnings"].([]any) {
		codes = append(codes, w.(map[string]any)["code"].(string))
	}
	if !slices.Equal(codes, []string{"proxy_host", "proxy_scheme"}) {
		t.Errorf("warnings %v", codes)
	}
	// The warning names only something that looks like a host; other text in the header stays out of it.
	h.do("GET", "/", nil, header("X-Forwarded-Host", "Your server is hacked, call 555-0100"))
	for _, w := range decode(h.do("GET", "/api/v1/status", nil))["warnings"].([]any) {
		if m := w.(map[string]any); m["code"] == "proxy_host" && !strings.HasPrefix(m["message"].(string), "Requests arrive for another host,") {
			t.Errorf("proxy_host: %s", m["message"])
		}
	}
}

func TestInternalListener(t *testing.T) {
	h := newHarness(t, nil, fast)
	internal := h.srv.Internal()
	h.srv.mediamtx.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("172.29.42.3")}, nil
	}
	call := func(method, path, remote string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		internal.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := call("GET", "/healthz", "10.9.9.9:1"); code != 200 {
		t.Errorf("healthz: %d", code)
	}
	if code := call("POST", "/internal/auth", "172.29.42.3:5000"); code != 200 {
		t.Errorf("auth from MediaMTX: %d", code)
	}
	if code := call("POST", "/internal/auth", "172.29.42.9:5000"); code != 403 {
		t.Errorf("auth from another container: %d, want 403", code)
	}
	if code := call("POST", "/internal/auth", "127.0.0.1:5000"); code != 200 {
		t.Errorf("auth from loopback: %d", code)
	}
}

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		":9081":         "http://127.0.0.1:9081/healthz",
		"0.0.0.0:9081":  "http://127.0.0.1:9081/healthz",
		"[::]:9081":     "http://127.0.0.1:9081/healthz",
		"10.0.0.5:9081": "http://10.0.0.5:9081/healthz",
	} {
		if got, err := HealthURL(in); err != nil || got != want {
			t.Errorf("HealthURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	pub, internal := freeAddr(t), freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	go func() { done <- Run(ctx, Listeners{Public: pub, Internal: internal}, ok, ok, log) }()
	for _, addr := range []string{pub, internal} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			resp, err := http.Get("http://" + addr + "/")
			if err == nil {
				_ = resp.Body.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s not ready: %v", addr, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunFailsOnBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Run(context.Background(), Listeners{Public: freeAddr(t), Internal: ln.Addr().String()}, http.NotFoundHandler(), http.NotFoundHandler(), log); err == nil {
		t.Fatal("Run with a busy port: want an error")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

type fakeOpened struct{ h *harness }

func (f fakeOpened) SessionsOf(id int64) []mtxauth.SessionRef {
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	out := f.h.opened[id]
	delete(f.h.opened, id)
	return out
}

type fakeKicker struct{ h *harness }

func (f fakeKicker) Post(_ context.Context, path string) (int, error) {
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	f.h.kicks = append(f.h.kicks, path)
	if strings.HasSuffix(path, "/gone") {
		return http.StatusNotFound, nil
	}
	return http.StatusOK, nil
}
