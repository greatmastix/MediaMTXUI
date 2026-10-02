package app

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mtxui/internal/store"
)

type sse struct {
	id, typ, data string
}

// stream is an open event stream read by a background goroutine.
type stream struct {
	resp   *http.Response
	events chan sse
}

// openStream connects to the event stream over a real listener (a recorder cannot stream), as the signed-in user.
// It returns nil and the status code when the stream is refused; an open stream is closed when the test ends.
func (h *harness) openStream(srv *httptest.Server, lastID string) (*stream, int) {
	h.t.Helper()
	return h.openStreamAt(srv, "/api/v1/events", lastID)
}

// openStreamAt opens another event stream (the log tail) the same way.
func (h *harness) openStreamAt(srv *httptest.Server, path, lastID string) (*stream, int) {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: "__Host-mtxui_session", Value: h.cookie})
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := srv.Client().Do(req) //nolint:bodyclose // closed below: at once when refused, else by t.Cleanup
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, resp.StatusCode
	}
	s := &stream{resp: resp, events: make(chan sse, 64)}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var ev sse
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.typ != "" {
					s.events <- ev
				}
				ev = sse{}
			case strings.HasPrefix(line, "id: "):
				ev.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				ev.typ = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[6:]
			case strings.HasPrefix(line, ": "):
				s.events <- sse{typ: "comment"}
			}
		}
	}()
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	return s, resp.StatusCode
}

func (s *stream) next(t *testing.T) sse {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			t.Fatal("the stream ended")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event within 3 s")
	}
	return sse{}
}

func (s *stream) ends(t *testing.T) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-s.events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the stream stayed open")
		}
	}
}

func signedInServer(t *testing.T) (*harness, *httptest.Server) {
	t.Helper()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	h.hub.Poll(context.Background())
	srv := httptest.NewServer(h.public)
	t.Cleanup(srv.Close)
	return h, srv
}

func TestEventStream(t *testing.T) {
	h, srv := signedInServer(t)
	h.mu.Lock()
	h.paths = `[{"name":"cam1","online":true,"inboundBytes":1}]`
	h.mu.Unlock()
	h.hub.Poll(context.Background())

	s, code := h.openStream(srv, "")
	if s == nil {
		t.Fatalf("status %d", code)
	}
	hdr := s.resp.Header
	if hdr.Get("Content-Type") != "text/event-stream" || hdr.Get("Cache-Control") != "no-store" ||
		hdr.Get("X-Accel-Buffering") != "no" || !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("headers %v", hdr)
	}
	snap := s.next(t)
	var body struct {
		Status struct{ Reachable bool }
		Lists  map[string]struct{ Items []map[string]any }
	}
	if err := json.Unmarshal([]byte(snap.data), &body); err != nil || snap.typ != "snapshot" || snap.id == "" {
		t.Fatalf("first event %+v: %v", snap, err)
	}
	if !body.Status.Reachable || len(body.Lists["paths"].Items) != 1 || len(body.Lists) != 13 { // an admin sees every list
		t.Errorf("snapshot %s", snap.data)
	}

	h.mu.Lock()
	h.paths = `[]`
	h.mu.Unlock()
	h.hub.Poll(context.Background())
	ev := s.next(t)
	if ev.typ != "update" || !strings.Contains(ev.data, `"remove":["cam1"]`) {
		t.Fatalf("update %+v", ev)
	}

	// Reconnecting with the last id resumes: only what was missed, no snapshot.
	_ = s.resp.Body.Close()
	s.ends(t)
	h.mu.Lock()
	h.paths = `[{"name":"cam2"}]`
	h.mu.Unlock()
	h.hub.Poll(context.Background())
	s, _ = h.openStream(srv, ev.id)
	if ev := s.next(t); ev.typ != "update" || !strings.Contains(ev.data, "cam2") {
		t.Fatalf("resume %+v", ev)
	}
}

// Signing out ends the session's open streams at once (the nudge), not at the next recheck (10 s).
func TestEventStreamEndsWithTheSession(t *testing.T) {
	h, srv := signedInServer(t)
	s, _ := h.openStream(srv, "")
	s.next(t) // snapshot
	if rec := h.do("POST", "/api/v1/auth/logout", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout %d", rec.Code)
	}
	if ev := s.next(t); ev.typ != "session" || ev.data != `{"state":"ended"}` {
		t.Fatalf("event %+v", ev)
	}
	s.ends(t)
}

func TestEventStreamHeartbeat(t *testing.T) {
	defer func(d time.Duration) { heartbeatEvery = d }(heartbeatEvery)
	heartbeatEvery = 20 * time.Millisecond
	h, srv := signedInServer(t)
	s, _ := h.openStream(srv, "")
	s.next(t)
	if ev := s.next(t); ev.typ != "comment" {
		t.Fatalf("event %+v", ev)
	}
}

func TestEventStreamsPerUser(t *testing.T) {
	defer func(n int) { streamsPerUser = n }(streamsPerUser)
	streamsPerUser = 2
	h, srv := signedInServer(t)
	a, _ := h.openStream(srv, "")
	b, _ := h.openStream(srv, "")
	if a == nil || b == nil {
		t.Fatal("the first two streams were refused")
	}
	a.next(t)
	b.next(t)
	if s, code := h.openStream(srv, ""); s != nil || code != http.StatusTooManyRequests {
		t.Fatalf("third stream: %d", code)
	}
	_ = a.resp.Body.Close()
	a.ends(t)
	deadline := time.Now().Add(3 * time.Second)
	for { // the handler releases its slot once it notices the disconnect
		if s, _ := h.openStream(srv, ""); s != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a closed stream's slot was not released")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEventStreamsCloseOnShutdown(t *testing.T) {
	h, srv := signedInServer(t)
	s, _ := h.openStream(srv, "")
	s.next(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.hub.Run(ctx); close(done) }()
	cancel()
	<-done
	s.ends(t)
	if s, code := h.openStream(srv, ""); s != nil || code != http.StatusServiceUnavailable {
		t.Fatalf("after shutdown: %d", code)
	}
}

func TestHistoryEndpoint(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	rec := h.do("GET", "/api/v1/metrics/history", nil)
	var got History
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.IntervalSeconds != 5 || got.Samples == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	// The longer ranges come from the stored minutes, in buckets.
	ctx := context.Background()
	hour := time.Now().Truncate(time.Hour)
	for i := range 90 {
		m := hour.Add(-time.Duration(i) * time.Minute)
		paths := map[string]store.PathPoint{"live/a": {InBps: 1000, Readers: 2}}
		if err := h.st.AddHistoryMinute(ctx, store.HistoryPoint{T: m, InBps: float64(i), Readers: i % 7}, paths); err != nil {
			t.Fatal(err)
		}
	}
	old := hour.Add(-40 * 24 * time.Hour)
	if err := h.st.AddHistoryMinute(ctx, store.HistoryPoint{T: old}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		rng      string
		interval int
		points   int
	}{{"24h", 60, 90}, {"7d", 600, 10}, {"30d", 3600, 3}} {
		rec := h.do("GET", "/api/v1/metrics/history?range="+tt.rng, nil)
		var got History
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.IntervalSeconds != tt.interval || len(got.Samples) != tt.points ||
			got.Samples[0].InBps == nil {
			t.Errorf("%s: %d %+v", tt.rng, rec.Code, got)
		}
	}
	if rec := h.do("GET", "/api/v1/metrics/history?range=1y", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown range: %d", rec.Code)
	}
	// Recording a minute without samples stores nothing; the minute after the hour prunes what is too old.
	h.srv.recordMinute(ctx, hour)
	if all, _ := h.st.History(ctx, old, time.Now(), time.Minute); len(all) != 90 {
		t.Errorf("after the hourly prune: %d minutes", len(all))
	}
}
