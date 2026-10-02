package liveproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

type tickets struct {
	mu     sync.Mutex
	issued []string
}

func (t *tickets) Issue(path, uiUser string, client netip.Addr) (string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.issued = append(t.issued, path+"|"+uiUser+"|"+client.String())
	return "mtxui-viewer-1", "secret"
}

// fakeMediaMTX mimics the HLS server's cookie check and session cookie, and WHEP's session URLs.
func fakeMediaMTX(t *testing.T) (*httptest.Server, *[]string) {
	var seen []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" xff="+r.Header.Get("X-Forwarded-For"))
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/index.m3u8"):
			if u, _, _ := r.BasicAuth(); u != "mtxui-viewer-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("cookieCheck") == "" {
				http.SetCookie(w, &http.Cookie{Name: "cookieCheck", Value: "1"})
				http.Redirect(w, r, r.URL.Path+"?cookieCheck=1", http.StatusFound)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "hlsSession", Value: "sess-1"})
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\nvideo1_stream.m3u8\n")
		case strings.HasSuffix(r.URL.Path, ".m3u8"), strings.HasSuffix(r.URL.Path, ".mp4"):
			if c, err := r.Cookie("hlsSession"); err != nil || c.Value != "sess-1" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, "segment of "+r.URL.Path)
		case strings.HasSuffix(r.URL.Path, "/whep") && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			if !strings.HasPrefix(string(body), "v=0") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", r.URL.Path+"/0a1b2c3d-0000-4000-8000-000000000001")
			w.Header().Add("Link", `<stun:stun.example.com:3478>; rel="ice-server"`)
			w.Header().Set("Content-Type", "application/sdp")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, "v=0 answer")
		case strings.Contains(r.URL.Path, "/whep/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

var alice = Viewer{Session: "tok-alice", User: "alice", Client: netip.MustParseAddr("203.0.113.7")}

func TestSplitHLS(t *testing.T) {
	for in, want := range map[string]string{"cam1/index.m3u8": "cam1|index.m3u8", "live/cam 1/x.mp4": "", "live/cam1/abc_video1_init.mp4": "live/cam1|abc_video1_init.mp4"} {
		p, f, err := SplitHLS(in)
		got := p + "|" + f
		if err != nil {
			got = ""
		}
		if got != want {
			t.Errorf("SplitHLS(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"index.m3u8", "cam1/../x.m3u8", "cam1/x.php", "/x.m3u8", "../../etc/passwd"} {
		if _, _, err := SplitHLS(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHLS(t *testing.T) {
	mtx, seen := fakeMediaMTX(t)
	tk := &tickets{}
	p := New(mtx.URL, mtx.URL, tk)
	get := func(v Viewer, file, query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		p.HLS(rec, httptest.NewRequest(http.MethodGet, "/x?"+query, nil), v, "live/cam1", file)
		return rec
	}
	rec := get(alice, "index.m3u8", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "video1_stream.m3u8") {
		t.Fatalf("index: %d %s", rec.Code, rec.Body)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("MediaMTX's cookies reached the browser")
	}
	// The next requests carry MediaMTX's session cookie, which the proxy kept for this viewer and path.
	if rec := get(alice, "video1_stream.m3u8", "_HLS_msn=5&_HLS_part=1"); rec.Code != http.StatusOK {
		t.Fatalf("variant: %d %s", rec.Code, rec.Body)
	}
	bob := Viewer{Session: "tok-bob", User: "bob", Client: netip.MustParseAddr("203.0.113.8")}
	if rec := get(bob, "video1_stream.m3u8", ""); rec.Code != http.StatusNotFound {
		t.Errorf("another viewer used alice's MediaMTX session: %d", rec.Code)
	}
	if rec := get(alice, "index.m3u8", "session=x"); rec.Code != http.StatusBadRequest {
		t.Errorf("a session parameter that is not a UUID: %d", rec.Code)
	}
	if rec := get(alice, "index.m3u8", "user=admin"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unexpected query parameter: %d", rec.Code)
	}
	if rec := get(alice, "video1_stream.m3u8", "session=792ce501-d003-4919-93dd-1ffb0f9c66e2"); rec.Code != http.StatusOK {
		t.Errorf("MediaMTX's session parameter: %d", rec.Code)
	}
	if !strings.Contains(strings.Join(*seen, "\n"), "GET /live/cam1/video1_stream.m3u8?_HLS_msn=5&_HLS_part=1 xff=203.0.113.7") {
		t.Errorf("upstream requests:\n%s", strings.Join(*seen, "\n"))
	}
	if tk.issued[0] != "live/cam1|alice|203.0.113.7" {
		t.Errorf("tickets %v", tk.issued)
	}
}

func TestWHEP(t *testing.T) {
	mtx, seen := fakeMediaMTX(t)
	p := New(mtx.URL, mtx.URL, &tickets{})
	offer := func(v Viewer, ct, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		p.WHEPOffer(rec, req, v, "cam1", "/api/v1/live/whep-session/")
		return rec
	}
	rec := offer(alice, "application/sdp", "v=0 offer")
	if rec.Code != http.StatusCreated || rec.Body.String() != "v=0 answer" ||
		rec.Header().Get("Location") != "/api/v1/live/whep-session/0a1b2c3d-0000-4000-8000-000000000001" ||
		!strings.Contains(rec.Header().Get("Link"), "stun.example.com") {
		t.Fatalf("offer: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec := offer(alice, "application/json", "{}"); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a non-SDP offer: %d", rec.Code)
	}
	session := func(v Viewer, method string) int {
		req := httptest.NewRequest(method, "/x", strings.NewReader("a=candidate:1"))
		req.Header.Set("Content-Type", "application/trickle-ice-sdpfrag")
		rec := httptest.NewRecorder()
		p.WHEPSession(rec, req, v, "0a1b2c3d-0000-4000-8000-000000000001")
		return rec.Code
	}
	if code := session(Viewer{Session: "other", Client: alice.Client}, http.MethodPatch); code != http.StatusNotFound {
		t.Errorf("another viewer patched alice's session: %d", code)
	}
	if code := session(alice, http.MethodPatch); code != http.StatusNoContent {
		t.Errorf("patch: %d", code)
	}
	if code := session(alice, http.MethodDelete); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code := session(alice, http.MethodDelete); code != http.StatusNotFound {
		t.Errorf("a deleted session is still known: %d", code)
	}
	if !strings.Contains(strings.Join(*seen, "\n"), "DELETE /cam1/whep/0a1b2c3d-0000-4000-8000-000000000001") {
		t.Errorf("upstream:\n%s", strings.Join(*seen, "\n"))
	}
}

func TestExternal(t *testing.T) {
	var gotAuth, gotXFF string
	mtx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/whip"):
			gotAuth, gotXFF = r.Header.Get("Authorization"), r.Header.Get("X-Forwarded-For")
			if gotAuth != "Bearer good" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mediamtx"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Location", r.URL.Path+"/0a1b2c3d-0000-4000-8000-000000000009")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, "v=0 answer")
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer mtx.Close()
	p := New(mtx.URL, mtx.URL, &tickets{})
	client := netip.MustParseAddr("198.51.100.4")
	offer := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/whip/studio", strings.NewReader("v=0 offer"))
		req.Header.Set("Content-Type", "application/sdp")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.AddCookie(&http.Cookie{Name: "mtxui_session", Value: "must-not-travel"})
		rec := httptest.NewRecorder()
		p.ExternalOffer(rec, req, "whip", "studio", client, "/rtc-session/")
		return rec
	}
	if rec := offer("Bearer bad"); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("a refused credential: %d %v", rec.Code, rec.Header())
	}
	rec := offer("Bearer good")
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/rtc-session/0a1b2c3d-0000-4000-8000-000000000009" {
		t.Fatalf("offer: %d %v", rec.Code, rec.Header())
	}
	if gotAuth != "Bearer good" || gotXFF != "198.51.100.4" {
		t.Errorf("upstream saw auth %q xff %q", gotAuth, gotXFF)
	}
	// The session is the external client's: the browser route cannot touch it, the external one can.
	del := func(f func(http.ResponseWriter, *http.Request)) int {
		rec := httptest.NewRecorder()
		f(rec, httptest.NewRequest(http.MethodDelete, "/x", nil))
		return rec.Code
	}
	if code := del(func(w http.ResponseWriter, r *http.Request) {
		p.WHEPSession(w, r, alice, "0a1b2c3d-0000-4000-8000-000000000009")
	}); code != http.StatusNotFound {
		t.Errorf("a UI viewer ended an external session: %d", code)
	}
	if code := del(func(w http.ResponseWriter, r *http.Request) {
		p.ExternalSession(w, r, client, "0a1b2c3d-0000-4000-8000-000000000009")
	}); code != http.StatusOK {
		t.Errorf("ending the external session: %d", code)
	}
}
