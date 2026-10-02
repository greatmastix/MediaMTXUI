package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mtxui/internal/auth"
	"mtxui/internal/mtxapi"
)

type principal struct{}

func (principal) Authorize(r *http.Request) { r.SetBasicAuth("mtxui-sidecar", "s3cret") }

// upstream is a fake MediaMTX that records what reached it.
type upstream struct {
	mu   sync.Mutex
	seen []*http.Request
	body string
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.seen = append(u.seen, r.Clone(r.Context()))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	body := u.body
	if body == "" {
		body = `{"ok":true}`
	}
	_, _ = io.WriteString(w, body)
}

func (u *upstream) last(t *testing.T) *http.Request {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.seen) == 0 {
		t.Fatal("nothing reached MediaMTX")
	}
	return u.seen[len(u.seen)-1]
}

func setup(t *testing.T) (*Proxy, *upstream, func(role string)) {
	t.Helper()
	ops, err := mtxapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	up := &upstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	var current string
	p, err := New(ops, srv.URL, principal{}, func(*http.Request) (auth.Role, bool) {
		return auth.Role(current), current != ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, up, func(role string) { current = role }
}

func do(p *Proxy, method, target string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestRolesAndRouting(t *testing.T) {
	p, _, as := setup(t)
	tests := []struct {
		role, method, path string
		want               int
	}{
		{"", "GET", "/v3/paths/list", 401},
		{"viewer", "GET", "/v3/paths/list", 200},
		{"viewer", "GET", "/v3/info", 200},
		{"viewer", "GET", "/v3/rtsp/conns/list", 403}, // client addresses: operator
		{"operator", "GET", "/v3/rtsp/conns/list", 200},
		{"operator", "POST", "/v3/rtsp/sessions/kick/0b3c5b3e-1111-4222-8333-944455556666", 200},
		{"viewer", "POST", "/v3/rtsp/sessions/kick/0b3c5b3e-1111-4222-8333-944455556666", 403},
		{"operator", "GET", "/v3/paths/forward-dests/list", 403}, // destination secrets: admin
		{"admin", "GET", "/v3/paths/forward-dests/list", 200},
		{"admin", "GET", "/v3/config/global/get", 404},     // sidecar-only
		{"admin", "PATCH", "/v3/config/global/patch", 404}, // unused: runtime-only config
		{"admin", "POST", "/v3/config/paths/add/cam1", 404},
		{"admin", "POST", "/v3/auth/jwks/refresh", 404},
		{"admin", "GET", "/v3/nope", 404},
		{"admin", "DELETE", "/v3/paths/list", 405},
		{"viewer", "GET", "/v3/paths/get/cam1", 200},
		{"viewer", "GET", "/v3/paths/get/live/front-door", 200},         // names may contain slashes
		{"viewer", "GET", "/v3/paths/get/../../config/global/get", 400}, // traversal
		{"viewer", "GET", "/v3/paths/get/", 400},
		{"viewer", "GET", "/v3/paths/get/a//b", 400},
		{"operator", "GET", "/v3/rtsp/sessions/get/not-a-uuid", 400},
		{"operator", "GET", "/v3/rtsp/sessions/get/0b3c5b3e-1111-4222-8333-944455556666/x", 404},
	}
	for _, tt := range tests {
		as(tt.role)
		if rec := do(p, tt.method, tt.path, nil); rec.Code != tt.want {
			t.Errorf("%s %s as %q: %d %s, want %d", tt.method, tt.path, tt.role, rec.Code, rec.Body, tt.want)
		}
	}
}

func TestUpstreamRequestIsRebuilt(t *testing.T) {
	p, up, as := setup(t)
	as("viewer")
	h := http.Header{
		"Cookie": {"__Host-mtxui_session=secret"}, "Authorization": {"Bearer stolen"},
		"X-Forwarded-For": {"6.6.6.6"}, "X-Custom": {"x"},
	}
	if rec := do(p, "GET", "/v3/paths/get/live/front-door?itemsPerPage=5", h); rec.Code != 400 {
		t.Fatalf("an undeclared query parameter was forwarded: %d", rec.Code)
	}
	if rec := do(p, "GET", "/v3/paths/list?itemsPerPage=5&page=1", h); rec.Code != 200 {
		t.Fatalf("declared parameters: %d %s", rec.Code, rec.Body)
	}
	got := up.last(t)
	if got.URL.Path != "/v3/paths/list" || got.URL.Query().Get("itemsPerPage") != "5" || got.URL.Query().Get("page") != "1" {
		t.Errorf("upstream URL %s", got.URL)
	}
	if user, pass, ok := got.BasicAuth(); !ok || user != "mtxui-sidecar" || pass != "s3cret" {
		t.Errorf("upstream credentials %q %q %v, want the sidecar principal", user, pass, ok)
	}
	for _, hdr := range []string{"Cookie", "X-Forwarded-For", "X-Custom"} {
		if got.Header.Get(hdr) != "" {
			t.Errorf("the browser's %s reached MediaMTX", hdr)
		}
	}
	for _, bad := range []string{"itemsPerPage=-1", "itemsPerPage=999999", "page=abc"} {
		if rec := do(p, "GET", "/v3/paths/list?"+bad, nil); rec.Code != 400 {
			t.Errorf("?%s: %d, want 400", bad, rec.Code)
		}
	}

	if rec := do(p, "GET", "/v3/paths/get/live/front-door", nil); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if got := up.last(t); got.URL.EscapedPath() != "/v3/paths/get/live/front-door" {
		t.Errorf("path names with slashes: upstream path %q", got.URL.EscapedPath())
	}

	as("operator")
	if rec := do(p, http.MethodDelete, "/v3/recordings/segments/delete?path=cam1&start=2026-09-29T10:00:00.123456Z", nil); rec.Code != 200 {
		t.Fatalf("segment delete: %d %s", rec.Code, rec.Body)
	}
	if got := up.last(t); got.Method != http.MethodDelete || got.URL.Query().Get("path") != "cam1" {
		t.Errorf("segment delete upstream %s %s", got.Method, got.URL)
	}
	if rec := do(p, "DELETE", "/v3/recordings/segments/delete?path=../x&start=2026-09-29T10:00:00Z", nil); rec.Code != 400 {
		t.Errorf("segment delete with a traversal path: %d", rec.Code)
	}
}

func TestRedaction(t *testing.T) {
	p, up, as := setup(t)
	as("admin")
	up.body = `{"itemCount":1,"items":[{"id":"x","state":"running","conf":{"dest":"rtmp://u:secret@cdn/live#key","whipBearerToken":"tok"}}]}`
	rec := do(p, "GET", "/v3/paths/forward-dests/list?path=cam1", nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "tok") {
		t.Fatalf("forward destinations leaked secrets: %d %s", rec.Code, rec.Body)
	}
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Items[0]["state"] != "running" {
		t.Errorf("redaction lost the rest of the answer: %s", rec.Body)
	}
	up.body = `{"id":"x","conf":{"dest":"srt://h?passphrase=secret"}}`
	if rec := do(p, "GET", "/v3/paths/forward-dests/get?path=cam1&id=0b3c5b3e-1111-4222-8333-944455556666", nil); strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("a single forward destination leaked secrets: %s", rec.Body)
	}
}

func TestUpstreamDown(t *testing.T) {
	ops, _ := mtxapi.Operations()
	p, err := New(ops, "http://127.0.0.1:1", principal{}, func(*http.Request) (auth.Role, bool) { return auth.RoleAdmin, true })
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(p, "GET", "/v3/paths/list", nil); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upstream") {
		t.Errorf("unreachable MediaMTX: %d %s", rec.Code, rec.Body)
	}
}
