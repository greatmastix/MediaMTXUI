package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtxui/internal/logs"
)

// logHarness is a signed-in harness whose data directory holds MediaMTX's log.
func logHarness(t *testing.T) (*harness, string) {
	t.Helper()
	data := t.TempDir()
	if err := os.Mkdir(filepath.Join(data, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, map[string]string{"MTXUI_DATA_DIR": data, "MTXUI_MEDIAMTX_LOG_KEEP": "2"}, fast)
	h.completeSetup()
	path := filepath.Join(data, "logs", "mediamtx.log")
	lines := "2026/10/01 10:00:00 INF [RTMP] listener opened on :1935\n" +
		"2026/10/01 10:00:01 WAR [path test] something odd\n" +
		"2026/10/01 10:00:02 ERR [WebRTC] [session 1] closed: broken\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := logs.Rotate(path, 0, 2); err != nil { // the three lines move to the first rotated copy
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("2026/10/01 10:00:03 INF [RTMP] [conn 1.2.3.4:5] opened\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, path
}

func TestLogSearch(t *testing.T) {
	h, _ := logHarness(t)
	var out LogLines
	h.json(h.do("GET", "/api/v1/logs", nil), &out)
	if len(out.Lines) != 4 || out.More || out.Lines[0].Text != "[RTMP] listener opened on :1935" ||
		out.Lines[3].Text != "[RTMP] [conn 1.2.3.4:5] opened" || out.Lines[2].Level != "error" {
		t.Fatalf("all: %+v", out)
	}
	h.json(h.do("GET", "/api/v1/logs?level=warn&q=WEBRTC", nil), &out)
	if len(out.Lines) != 1 || out.Lines[0].Level != "error" {
		t.Fatalf("filtered: %+v", out)
	}
	h.json(h.do("GET", "/api/v1/logs?limit=2", nil), &out)
	if len(out.Lines) != 2 || !out.More || out.Lines[1].Text != "[RTMP] [conn 1.2.3.4:5] opened" {
		t.Fatalf("limited: %+v", out)
	}
	// The sidecar's own lines come from the hub.
	h.srv.d.Logs.Publish(logs.Line{T: 1, Source: logs.Sidecar, Level: "warn", Text: "request path=/x"})
	h.json(h.do("GET", "/api/v1/logs?source=sidecar", nil), &out)
	if len(out.Lines) != 1 || out.Lines[0].Text != "request path=/x" {
		t.Fatalf("sidecar: %+v", out)
	}
	for _, q := range []string{"source=../etc", "level=loud", "limit=0", "limit=5000", "q=" + strings.Repeat("x", 201)} {
		if rec := h.do("GET", "/api/v1/logs?"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	// The download: the rotated copy, decompressed, then the live file.
	rec := h.do("GET", "/api/v1/logs/download", nil)
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), "\n") != 4 ||
		!strings.HasPrefix(rec.Body.String(), "2026/10/01 10:00:00") ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download: %d %q", rec.Code, rec.Body.String())
	}
}

func TestLogStream(t *testing.T) {
	h, _ := logHarness(t)
	srv := httptest.NewServer(h.public)
	t.Cleanup(srv.Close)
	s, code := h.openStreamAt(srv, "/api/v1/logs/stream?level=warn", "")
	if s == nil {
		t.Fatalf("refused: %d", code)
	}
	ev := s.next(t)
	var backlog LogLines
	if ev.typ != "backlog" || json.Unmarshal([]byte(ev.data), &backlog) != nil || len(backlog.Lines) != 2 {
		t.Fatalf("backlog %+v", ev)
	}
	// A line the follower read before the backlog did is not sent twice; new lines come as they are published.
	h.srv.d.Logs.Publish(backlog.Lines[1])
	h.srv.d.Logs.Publish(logs.Line{Source: logs.MediaMTX, Level: "info", Text: "quiet"})
	h.srv.d.Logs.Publish(logs.Line{T: time.Now().UnixMilli(), Source: logs.MediaMTX, Level: "error", Text: "new"})
	ev = s.next(t)
	var l logs.Line
	if ev.typ != "line" || json.Unmarshal([]byte(ev.data), &l) != nil || l.Text != "new" {
		t.Fatalf("line %+v", ev)
	}
	// Signing out ends the tail.
	h.do("POST", "/api/v1/auth/logout", nil)
	s.ends(t)
}

// Every follower line reaches the tail: the wiring main sets up (OnLine) with a real file.
func TestLogFollow(t *testing.T) {
	h, path := logHarness(t)
	tail := h.srv.d.Logs.Subscribe(logs.Filter{Source: logs.MediaMTX})
	defer h.srv.d.Logs.Unsubscribe(tail)
	h.srv.d.Notes.OnLine = func(line string) { h.srv.d.Logs.Publish(logs.ParseMediaMTX(line)) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.srv.d.Notes.Follow(ctx, path, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // let Follow open the file at its end first
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("2026/10/01 10:00:04 ERR [SRT] boom\n")
	_ = f.Close()
	select {
	case l := <-tail.C:
		if l.Text != "[SRT] boom" || l.Level != "error" {
			t.Fatalf("got %+v", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the line never reached the tail")
	}
}
