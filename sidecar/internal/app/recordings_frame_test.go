package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// A frame is a tenth of a second of MP4 from MediaMTX's playback server, starting at the moment (first) or ending
// there (last); it has slots of its own, apart from the exports'; a finished recording's frame may be kept.
func TestRecordingFrame(t *testing.T) {
	h, _ := recordingsHarness(t, nil)
	var asked url.Values
	h.mu.Lock()
	h.routes["/get"] = func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query()
		_, _ = io.WriteString(w, "ftyp-a-tenth-of-a-second")
	}
	h.mu.Unlock()
	frame := func(at, edge string) *httptest.ResponseRecorder {
		t.Helper()
		return h.do("GET", "/api/v1/recordings/frame?path=live/cam&at="+url.QueryEscape(at)+"&edge="+edge, nil)
	}

	resp := frame("2026-10-01T10:00:10Z", "first")
	if resp.Code != http.StatusOK || asked.Get("start") != "2026-10-01T10:00:10Z" || asked.Get("duration") != "0.1" ||
		asked.Get("format") != "mp4" || resp.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("first: %d %v %v", resp.Code, asked, resp.Header())
	}
	if cc := resp.Header().Get("Cache-Control"); cc != "private, max-age=86400" {
		t.Errorf("a finished recording's frame: Cache-Control %q", cc)
	}
	if resp := frame("2026-10-01T10:00:10Z", "last"); resp.Code != http.StatusOK || asked.Get("start") != "2026-10-01T10:00:09.9Z" {
		t.Errorf("last: %d, asked from %s", resp.Code, asked.Get("start"))
	}
	if resp := frame(time.Now().UTC().Format(time.RFC3339), "first"); resp.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("a frame of now: Cache-Control %q", resp.Header().Get("Cache-Control"))
	}
	// More, where a tenth of a second held no picture; within bounds.
	rec := h.do("GET", "/api/v1/recordings/frame?path=live/cam&at=2026-10-01T10:00:10Z&edge=last&seconds=2", nil)
	if rec.Code != http.StatusOK || asked.Get("start") != "2026-10-01T10:00:08Z" || asked.Get("duration") != "2" {
		t.Errorf("two seconds back: %d %v", rec.Code, asked)
	}
	for _, bad := range []string{"0.01", "3", "x"} {
		if rec := h.do("GET", "/api/v1/recordings/frame?path=live/cam&at=2026-10-01T10:00:10Z&edge=first&seconds="+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("seconds=%s: %d", bad, rec.Code)
		}
	}
	if resp := frame("2026-10-01T10:00:10Z", "middle"); resp.Code != http.StatusBadRequest {
		t.Errorf("edge middle: %d", resp.Code)
	}

	// Every export slot taken: frames still load. Every frame slot taken: the next frame waits its turn (429).
	exports := h.srv.exportSlots()
	for range cap(exports) {
		exports <- struct{}{}
	}
	if resp := frame("2026-10-01T10:00:10Z", "first"); resp.Code != http.StatusOK {
		t.Errorf("with the export slots taken: %d", resp.Code)
	}
	frames := h.srv.frameSlots()
	for range cap(frames) {
		frames <- struct{}{}
	}
	if resp := frame("2026-10-01T10:00:10Z", "first"); resp.Code != http.StatusTooManyRequests {
		t.Errorf("with the frame slots taken: %d", resp.Code)
	}
}
