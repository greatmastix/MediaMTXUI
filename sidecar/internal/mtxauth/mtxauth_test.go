package mtxauth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtxui/internal/credentials"
	"mtxui/internal/mtxtest"
	"mtxui/internal/store"
)

type fixture struct {
	h       *Handler
	creds   *credentials.Service
	logs    *bytes.Buffer
	pub     string // secret of "cam1": publish and read on path cam1
	token   string // secret of "viewer-1": read on any path, bearer
	revoked string // secret of "old": revoked
}

func newFixture(t *testing.T, own ...netip.Addr) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key, err := credentials.LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{creds: credentials.New(st, key), logs: &bytes.Buffer{}}
	f.pub, _, err = f.creds.Add(ctx, credentials.Spec{Name: "cam1", Actions: []string{"publish", "read"}, Paths: []string{"cam1"}})
	if err != nil {
		t.Fatal(err)
	}
	f.token, _, err = f.creds.Add(ctx, credentials.Spec{Name: "viewer-1", Kind: credentials.KindToken, Actions: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	f.revoked, _, err = f.creds.Add(ctx, credentials.Spec{Name: "old", Actions: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.creds.Revoke(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	p := &Principal{secret: "sidecar-secret-for-tests", addrs: own}
	f.h = NewHandler(p, f.creds, slog.New(slog.NewTextHandler(f.logs, nil)))
	return f
}

func TestDecide(t *testing.T) {
	sidecarIP := netip.MustParseAddr("172.29.42.2")
	f := newFixture(t, sidecarIP)
	f.h.Public = func(path string) bool { return path == "live/open" }
	ctx := context.Background()
	tests := []struct {
		name string
		req  Request
		want bool
	}{
		{"anonymous probe", Request{IP: "203.0.113.7", Action: "read", Path: "cam1", Protocol: "rtsp"}, false},
		{"anonymous read of a public stream", Request{IP: "203.0.113.7", Action: "read", Path: "live/open", Protocol: "rtsp"}, true},
		{"anonymous publish to a public stream", Request{IP: "203.0.113.7", Action: "publish", Path: "live/open", Protocol: "rtmp"}, false},
		{"anonymous playback of a public stream", Request{IP: "203.0.113.7", Action: "playback", Path: "live/open"}, false},
		{"a wrong key on a public stream", Request{IP: "203.0.113.7", User: "cam1", Password: "wrong-secret-value", Action: "read", Path: "live/open"}, false},
		{"garbage address", Request{IP: "nope", User: "cam1", Password: f.pub, Action: "read", Path: "cam1"}, false},
		{"unknown action", Request{IP: "203.0.113.7", User: "cam1", Password: f.pub, Action: "admin", Path: "cam1"}, false},
		{"publisher", Request{IP: "203.0.113.7", User: "cam1", Password: f.pub, Token: f.pub, Action: "publish", Path: "cam1", Protocol: "rtsp"}, true},
		{"publisher reads its path", Request{IP: "203.0.113.7", User: "cam1", Password: f.pub, Action: "read", Path: "cam1"}, true},
		{"publisher on another path", Request{IP: "203.0.113.7", User: "cam1", Password: f.pub, Action: "publish", Path: "cam2"}, false},
		{"publisher without playback", Request{IP: "203.0.113.7", User: "cam1", Password: f.pub, Action: "playback", Path: "cam1"}, false},
		{"stream credentials never get the API", Request{IP: "203.0.113.7", User: "cam1", Password: f.pub, Action: "api"}, false},
		{"wrong secret", Request{IP: "203.0.113.7", User: "cam1", Password: "x" + f.pub, Action: "publish", Path: "cam1"}, false},
		{"bearer token", Request{IP: "203.0.113.7", Token: f.token, Action: "read", Path: "anything", Protocol: "hls"}, true},
		{"name and secret as a WHIP bearer token", Request{IP: "203.0.113.7", Token: "cam1:" + f.pub, Action: "publish", Path: "cam1", Protocol: "webrtc"}, true},
		{"name and wrong secret as a bearer token", Request{IP: "203.0.113.7", Token: "cam1:x" + f.pub, Action: "publish", Path: "cam1", Protocol: "webrtc"}, false},
		{"token field ignored when a user is given", Request{IP: "203.0.113.7", User: "nobody", Password: "x", Token: f.token, Action: "read", Path: "cam1"}, false},
		{"revoked", Request{IP: "203.0.113.7", User: "old", Password: f.revoked, Action: "read", Path: "cam1"}, false},
		{"sidecar principal for the API", Request{IP: "172.29.42.2", User: SidecarUser, Password: "sidecar-secret-for-tests", Action: "api"}, true},
		{"sidecar principal for metrics", Request{IP: "172.29.42.2", User: SidecarUser, Password: "sidecar-secret-for-tests", Action: "metrics"}, true},
		{"sidecar principal for playback", Request{IP: "172.29.42.2", User: SidecarUser, Password: "sidecar-secret-for-tests", Action: "playback", Path: "cam1"}, true},
		{"sidecar principal cannot publish", Request{IP: "172.29.42.2", User: SidecarUser, Password: "sidecar-secret-for-tests", Action: "publish", Path: "cam1"}, false},
		{"sidecar principal cannot pprof", Request{IP: "172.29.42.2", User: SidecarUser, Password: "sidecar-secret-for-tests", Action: "pprof"}, false},
		{"sidecar secret from a foreign address", Request{IP: "172.29.42.99", User: SidecarUser, Password: "sidecar-secret-for-tests", Action: "api"}, false},
		{"wrong sidecar secret", Request{IP: "172.29.42.2", User: SidecarUser, Password: "guess", Action: "api"}, false},
		{"IPv4-mapped sidecar address", Request{IP: "::ffff:172.29.42.2", User: SidecarUser, Password: "sidecar-secret-for-tests", Action: "api"}, true},
	}
	for _, tt := range tests {
		if got := f.h.Decide(ctx, tt.req); got.Allow != tt.want {
			t.Errorf("%s: allow=%v (%s), want %v", tt.name, got.Allow, got.Reason, tt.want)
		}
	}
	for _, secret := range []string{f.pub, f.token, f.revoked, "sidecar-secret-for-tests"} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatalf("a secret reached the log:\n%s", f.logs)
		}
	}
}

func TestThrottle(t *testing.T) {
	own := netip.MustParseAddr("172.29.42.2")
	f := newFixture(t, own)
	ctx := context.Background()
	bad := func(ip string) Request {
		return Request{IP: ip, User: "cam1", Password: "wrong", Action: "publish", Path: "cam1", Query: "user=cam1&pass=wrong"}
	}
	good := func(ip string) Request {
		return Request{IP: ip, User: "cam1", Password: f.pub, Action: "publish", Path: "cam1"}
	}
	for range throttleFailures {
		f.h.Decide(ctx, bad("198.51.100.1"))
	}
	if d := f.h.Decide(ctx, good("198.51.100.1")); d.Allow || !strings.Contains(d.Reason, "too many") {
		t.Fatalf("after %d failures the right secret got %+v, want throttling", throttleFailures, d)
	}
	if d := f.h.Decide(ctx, good("198.51.100.2")); !d.Allow {
		t.Errorf("another address is throttled too: %+v", d)
	}
	// Anonymous probes are neither counted nor throttled.
	for range 3 * throttleFailures {
		f.h.Decide(ctx, Request{IP: "198.51.100.3", Action: "read", Path: "cam1"})
	}
	if d := f.h.Decide(ctx, good("198.51.100.3")); !d.Allow {
		t.Errorf("anonymous probes throttled an address: %+v", d)
	}
	// One IPv6 host holds a whole /64: failures from its addresses add up.
	for i := range throttleFailures {
		f.h.Decide(ctx, bad(fmt.Sprintf("2001:db8:1:2::%x", i+1)))
	}
	if d := f.h.Decide(ctx, good("2001:db8:1:2:ffff::1")); d.Allow {
		t.Error("IPv6 failures spread over a /64 escaped throttling")
	}
	// The sidecar's own addresses are never throttled.
	for range 2 * throttleFailures {
		f.h.Decide(ctx, bad(own.String()))
	}
	if d := f.h.Decide(ctx, good(own.String())); !d.Allow {
		t.Errorf("the sidecar's own address was throttled: %+v", d)
	}
	if strings.Contains(f.logs.String(), "pass=wrong") {
		t.Error("the query (with RTMP credentials) reached the log")
	}
}

func TestServeHTTP(t *testing.T) {
	f := newFixture(t)
	post := func(body string) int {
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/auth", strings.NewReader(body)))
		return rec.Code
	}
	if code := post(fmt.Sprintf(`{"ip":"203.0.113.7","user":"cam1","password":%q,"action":"publish","path":"cam1","id":null,"future":"field"}`, f.pub)); code != 200 {
		t.Errorf("allowed request: %d (unknown fields must be tolerated)", code)
	}
	if code := post(`{"ip":"203.0.113.7","user":"cam1","password":"x","action":"publish","path":"cam1"}`); code != 401 {
		t.Errorf("denied request: %d", code)
	}
	if code := post(`{not json`); code != 400 {
		t.Errorf("bad JSON: %d", code)
	}
	if code := post(`{"user":"` + strings.Repeat("a", 70<<10) + `"}`); code != 400 {
		t.Errorf("oversized body: %d", code)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/auth", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
}

// TestWithMediaMTX runs the pinned MediaMTX with authMethod http against the handler and checks each protocol's
// outcome end to end.
func TestWithMediaMTX(t *testing.T) {
	mtxtest.Bin(t)
	f := newFixture(t)
	p, err := NewPrincipal() // the test's requests reach MediaMTX from 127.0.0.1, one of this host's addresses
	if err != nil {
		t.Fatal(err)
	}
	f.h.principal = p
	mux := http.NewServeMux()
	mux.Handle("/internal/auth", f.h)
	authSrv := httptest.NewServer(mux)
	defer authSrv.Close()

	api, metrics, playback, rtsp, hls := mtxtest.FreePort(t), mtxtest.FreePort(t), mtxtest.FreePort(t), mtxtest.FreePort(t), mtxtest.FreePort(t)
	dir := t.TempDir()
	mtxtest.Start(t, fmt.Sprintf(`
logLevel: debug
authMethod: http
authHTTPAddress: %s/internal/auth
authHTTPExclude: []
api: yes
apiAddress: 127.0.0.1:%d
metrics: yes
metricsAddress: 127.0.0.1:%d
playback: yes
playbackAddress: 127.0.0.1:%d
pprof: no
rtsp: yes
rtspTransports: [tcp]
rtspAddress: 127.0.0.1:%d
rtmp: no
hls: yes
hlsAddress: 127.0.0.1:%d
webrtc: no
srt: no
moq: no
pathDefaults:
  recordPath: %s/%%path/%%Y-%%m-%%d_%%H-%%M-%%S-%%f
paths:
  all_others:
`, authSrv.URL, api, metrics, playback, rtsp, hls, dir), api)

	get := func(t *testing.T, url, user, pass string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		return status(t, req)
	}
	sidecarGet := func(t *testing.T, url string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		p.Authorize(req)
		return status(t, req)
	}
	bearer := func(t *testing.T, url, token string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return status(t, req)
	}
	base := func(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

	// MediaMTX itself pauses 1 to 3 s before answering a failed authentication that carried credentials (anonymous
	// probes are answered at once), so the checks run in parallel.
	checks := []struct {
		name string
		do   func(t *testing.T) int
		want int
	}{
		{"API, anonymous", func(t *testing.T) int { return get(t, base(api)+"/v3/paths/list", "", "") }, 401},
		{"API, sidecar principal", func(t *testing.T) int { return sidecarGet(t, base(api)+"/v3/paths/list") }, 200},
		{"API, stream credential", func(t *testing.T) int { return get(t, base(api)+"/v3/paths/list", "cam1", f.pub) }, 401},
		{"metrics, sidecar principal", func(t *testing.T) int { return sidecarGet(t, base(metrics)+"/metrics") }, 200},
		{"metrics, anonymous", func(t *testing.T) int { return get(t, base(metrics)+"/metrics", "", "") }, 401},
		{"playback, reader without playback", func(t *testing.T) int { return get(t, base(playback)+"/list?path=cam1", "cam1", f.pub) }, 401},
		{"HLS, reader, no stream yet", func(t *testing.T) int { return get(t, base(hls)+"/cam1/index.m3u8", "cam1", f.pub) }, 404},
		{"HLS, wrong secret", func(t *testing.T) int { return get(t, base(hls)+"/cam1/index.m3u8", "cam1", "wrong") }, 401},
		{"HLS, bearer token", func(t *testing.T) int { return bearer(t, base(hls)+"/any/index.m3u8", f.token) }, 404},
		{"RTSP DESCRIBE, reader, no stream yet", func(t *testing.T) int { return rtspDescribe(t, rtsp, "cam1", "cam1", f.pub) }, 404},
		{"RTSP DESCRIBE, wrong secret", func(t *testing.T) int { return rtspDescribe(t, rtsp, "cam1", "cam1", "wrong") }, 401},
		{"RTSP DESCRIBE, out of scope", func(t *testing.T) int { return rtspDescribe(t, rtsp, "cam2", "cam1", f.pub) }, 401},
		{"RTSP DESCRIBE, revoked", func(t *testing.T) int { return rtspDescribe(t, rtsp, "cam1", "old", f.revoked) }, 401},
		{"RTSP DESCRIBE, anonymous", func(t *testing.T) int { return rtspDescribe(t, rtsp, "cam1", "", "") }, 401},
	}
	t.Run("protocols", func(t *testing.T) {
		for _, c := range checks {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				if got := c.do(t); got != c.want {
					t.Errorf("%d, want %d", got, c.want)
				}
			})
		}
	})

	// Revoking takes effect on the next connection.
	if err := f.creds.Revoke(context.Background(), "cam1"); err != nil {
		t.Fatal(err)
	}
	if got := rtspDescribe(t, rtsp, "cam1", "cam1", f.pub); got != 401 {
		t.Errorf("RTSP DESCRIBE after revocation: %d, want 401", got)
	}
}

func status(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// rtspDescribe sends one DESCRIBE, with preemptive Basic credentials when user is set, and returns the status code.
func rtspDescribe(t *testing.T, port int, path, user, pass string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := fmt.Sprintf("DESCRIBE rtsp://127.0.0.1:%d/%s RTSP/1.0\r\nCSeq: 1\r\nAccept: application/sdp\r\n", port, path)
	if user != "" {
		req += "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)) + "\r\n"
	}
	if _, err := conn.Write([]byte(req + "\r\n")); err != nil {
		t.Fatal(err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("RTSP response: %v", err)
	}
	var code int
	if _, err := fmt.Sscanf(status, "RTSP/1.0 %d", &code); err != nil {
		t.Fatalf("RTSP status line %q", status)
	}
	return code
}

func TestSessionsOf(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := func(s string) *string { return &s }
	for _, req := range []Request{
		{IP: "203.0.113.7", User: "cam1", Password: f.pub, Action: "publish", Path: "cam1", Protocol: "rtsp", ID: id("a")},
		{IP: "203.0.113.8", User: "cam1", Password: f.pub, Action: "read", Path: "cam1", Protocol: "hls", ID: id("b")},
		{IP: "203.0.113.8", User: "cam1", Password: f.pub, Action: "read", Path: "cam1", Protocol: "hls", ID: id("b")},
		{IP: "203.0.113.9", Token: f.token, Action: "read", Path: "x", Protocol: "webrtc", ID: id("c")},
		{IP: "203.0.113.9", User: "cam1", Password: "wrong", Action: "read", Path: "cam1", Protocol: "rtsp", ID: id("d")},
	} {
		f.h.Decide(ctx, req)
	}
	cam1, _ := f.creds.List(ctx)
	var camID int64
	for _, c := range cam1 {
		if c.Name == "cam1" {
			camID = c.ID
		}
	}
	got := f.h.SessionsOf(camID)
	if len(got) != 2 {
		t.Fatalf("sessions of cam1: %+v", got)
	}
	paths := []string{KickPath(got[0]), KickPath(got[1])}
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "/v3/rtsp/sessions/kick/a") || !strings.Contains(joined, "/v3/hls/sessions/kick/b") {
		t.Errorf("kick paths %v", paths)
	}
	if again := f.h.SessionsOf(camID); len(again) != 0 {
		t.Errorf("sessions were not forgotten: %v", again)
	}
	if KickPath(SessionRef{Protocol: "api", ID: "x"}) != "" || KickPath(SessionRef{Protocol: "rtsp"}) != "" {
		t.Error("kick path for an unknown protocol or a missing id")
	}
	var o opened
	for i := range maxOpened + 5 {
		o.add(subject{cred: 1}, SessionRef{Protocol: "rtsp", ID: fmt.Sprint(i)}, time.Unix(int64(i), 0))
	}
	if o.n != maxOpened || len(o.by[subject{cred: 1}]) != maxOpened {
		t.Errorf("bound: n=%d", o.n)
	}
	if _, ok := o.by[subject{cred: 1}][SessionRef{Protocol: "rtsp", ID: "0"}]; ok {
		t.Error("the oldest entry survived")
	}
}

func TestViewerTickets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	client := netip.MustParseAddr("203.0.113.7")
	user, secret := f.h.Viewers.Issue("cam1", "alice", client)
	ok := Request{IP: "203.0.113.7", User: user, Password: secret, Action: "read", Path: "cam1", Protocol: "hls"}
	if d := f.h.Decide(ctx, ok); !d.Allow || d.Who != "viewer alice" {
		t.Fatalf("ticket refused: %+v", d)
	}
	for name, req := range map[string]Request{
		"another path":    {IP: "203.0.113.7", User: user, Password: secret, Action: "read", Path: "cam2"},
		"publishing":      {IP: "203.0.113.7", User: user, Password: secret, Action: "publish", Path: "cam1"},
		"another address": {IP: "203.0.113.8", User: user, Password: secret, Action: "read", Path: "cam1"},
		"wrong secret":    {IP: "203.0.113.7", User: user, Password: "x" + secret[1:], Action: "read", Path: "cam1"},
		"made up":         {IP: "203.0.113.7", User: ViewerPrefix + "0000", Password: secret, Action: "read", Path: "cam1"},
	} {
		if d := f.h.Decide(ctx, req); d.Allow {
			t.Errorf("%s: allowed", name)
		}
	}
	now := time.Now()
	f.h.Viewers.now = func() time.Time { return now.Add(2 * ticketTTL) }
	if d := f.h.Decide(ctx, ok); d.Allow {
		t.Error("an expired ticket was accepted")
	}
	// Issuing prunes expired tickets, now and then.
	for range ticketSweep {
		f.h.Viewers.Issue("cam1", "bob", client)
	}
	if _, ok := f.h.Viewers.m[user]; ok || len(f.h.Viewers.m) > ticketSweep {
		t.Errorf("%d tickets kept, the expired one among them: %v", len(f.h.Viewers.m), ok)
	}
	if strings.Contains(f.logs.String(), secret) {
		t.Error("a ticket secret reached the log")
	}
}
