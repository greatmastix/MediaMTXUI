package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testSPA(t *testing.T) http.Handler {
	t.Helper()
	h, err := New(fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"favicon.svg":          {Data: []byte("<svg/>")},
		"assets/index-3f2a.js": {Data: []byte("console.log(1)")},
		".gitkeep":             {Data: nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestServe(t *testing.T) {
	h := testSPA(t)
	tests := []struct {
		name, method, path string
		wantCode           int
		wantType, wantBody string
		wantCache          string
	}{
		{"root", "GET", "/", 200, "text/html; charset=utf-8", "<!doctype html><title>app</title>", "no-cache"},
		{"index by name", "GET", "/index.html", 200, "text/html; charset=utf-8", "<!doctype html><title>app</title>", "no-cache"},
		{"client route falls back to index", "GET", "/paths/cam1/detail", 200, "text/html; charset=utf-8", "<!doctype html><title>app</title>", "no-cache"},
		{"hashed asset is immutable", "GET", "/assets/index-3f2a.js", 200, "text/javascript; charset=utf-8", "console.log(1)", "public, max-age=31536000, immutable"},
		{"unhashed file revalidates", "GET", "/favicon.svg", 200, "image/svg+xml", "<svg/>", "no-cache"},
		{"missing asset is 404, not html", "GET", "/assets/index-old.js", 404, "", "", ""},
		{"missing file with extension is 404", "GET", "/robots.txt", 404, "", "", ""},
		{"dotfiles are not served", "GET", "/.gitkeep", 404, "", "", ""},
		{"traversal stays inside the app", "GET", "/../../etc/passwd", 200, "text/html; charset=utf-8", "<!doctype html><title>app</title>", "no-cache"},
		{"head has no body", "HEAD", "/", 200, "text/html; charset=utf-8", "", "no-cache"},
		{"post is rejected", "POST", "/", 405, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode != 200 {
				return
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestConditionalGet(t *testing.T) {
	h := testSPA(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	etag := rec.Header().Get("ETag")
	if len(etag) < 10 {
		t.Fatalf("ETag = %q, want a strong content hash", etag)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("code = %d, want 304", rec.Code)
	}
}

func TestNotBuilt(t *testing.T) {
	h, err := New(fstest.MapFS{".gitkeep": {Data: nil}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

func TestEmbeddedHandler(t *testing.T) {
	// A source checkout embeds only .gitkeep (503); an image build embeds the real app (200).
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 200 or 503", rec.Code)
	}
}
