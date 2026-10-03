package liveproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// Only the request that starts a MediaMTX session (the multivariant playlist) gets a viewer ticket: MediaMTX never
// looks at credentials for media playlists and segments, so a ticket each would only fill the ticket store.
func TestHLSTicketsOnlyStartSessions(t *testing.T) {
	mtx, _ := fakeMediaMTX(t)
	tk := &tickets{}
	p := New(mtx.URL, mtx.URL, tk)
	get := func(file string) int {
		rec := httptest.NewRecorder()
		p.HLS(rec, httptest.NewRequest(http.MethodGet, "/x", nil), alice, "live/cam1", file)
		return rec.Code
	}
	if code := get("index.m3u8"); code != http.StatusOK {
		t.Fatalf("index: %d", code)
	}
	started := len(tk.issued)
	if started == 0 || started > 2 {
		t.Fatalf("%d tickets to start one session", started)
	}
	for range 50 {
		if code := get("video1_stream.m3u8"); code != http.StatusOK {
			t.Fatalf("variant: %d", code)
		}
		if code := get("seg1.mp4"); code != http.StatusOK {
			t.Fatalf("segment: %d", code)
		}
	}
	if len(tk.issued) != started {
		t.Errorf("%d tickets for 100 playlist and segment requests", len(tk.issued)-started)
	}
}

// The proxy keeps at most maxPerClient sessions per external client address, and maxSessions in all: beyond a bound
// the oldest external entry goes, so one client's flood of anonymous offers costs only its own entries. The live
// view's entries are never evicted (the access loop needs them to close a revoked user's views): a UI session past
// its bound, or any once the proxy holds nothing but live views, is refused instead.
func TestSessionBounds(t *testing.T) {
	p := New("http://mediamtx:8888", "http://mediamtx:8889", &tickets{})
	t0 := time.Unix(1_800_000_000, 0)
	n := 0
	add := func(owner, bucket string) string {
		n++
		id := fmt.Sprintf("s%06d", n)
		p.mu.Lock()
		p.addLocked(id, whep{owner: owner, bucket: bucket, created: t0.Add(time.Duration(n) * time.Millisecond)})
		p.mu.Unlock()
		return id
	}
	has := func(id string) bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		_, ok := p.sessions[id]
		return ok
	}
	viewer := add(alice.Key(), alice.Key())
	first := add(externalOwner, "198.51.100.4")
	for range maxPerClient + 5 {
		add(externalOwner, "198.51.100.4")
	}
	if got := p.buckets["198.51.100.4"]; got != maxPerClient {
		t.Errorf("one client holds %d entries", got)
	}
	if has(first) || !has(viewer) {
		t.Errorf("per client: oldest kept %v, viewer kept %v", has(first), has(viewer))
	}
	for range maxPerClient - 2 {
		add(alice.Key(), alice.Key())
	}
	if !p.roomFor(alice.Key()) {
		t.Error("a UI session refused below its bound")
	}
	add(alice.Key(), alice.Key())
	if p.roomFor(alice.Key()) || !p.roomFor("another UI session") {
		t.Error("UI sessions: the bound is per session")
	}

	for i := 0; len(p.sessions) < maxSessions; i++ {
		add(externalOwner, fmt.Sprintf("2001:db8:%x::/64", i/maxPerClient))
	}
	p.mu.Lock()
	victim := p.oldestLocked(func(o whep) bool { return o.owner == externalOwner })
	p.mu.Unlock()
	latest := add(externalOwner, "203.0.113.200")
	if len(p.sessions) != maxSessions || has(victim) || !has(viewer) || !has(latest) {
		t.Errorf("in all: %d entries, oldest external kept %v, viewer (the oldest) kept %v", len(p.sessions), has(victim), has(viewer))
	}
	if !p.roomFor("another UI session") {
		t.Error("a live view refused while external entries can make room")
	}
	p.mu.Lock()
	for id, s := range p.sessions {
		if s.owner == externalOwner {
			p.removeLocked(id)
			p.sessions[id], p.buckets["ui"] = whep{owner: "ui", bucket: "ui", created: s.created}, p.buckets["ui"]+1
		}
	}
	external := p.external
	p.mu.Unlock()
	if external != 0 || p.roomFor("another UI session") {
		t.Errorf("a live view allowed with the proxy full of live views (%d external)", external)
	}
	total := 0
	for _, c := range p.buckets {
		total += c
	}
	if total != len(p.sessions) {
		t.Errorf("bucket counts add up to %d for %d entries", total, len(p.sessions))
	}
}

// Sessions reports what the access loop needs (MediaMTX's own id from its ID header, the UI session, the path); End
// closes a session upstream as its owner's DELETE would, and Forget drops one MediaMTX has ended.
func TestSessionsEndAndForget(t *testing.T) {
	var mu sync.Mutex
	var deleted []string
	n := 0
	mtx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			n++
			w.Header().Set("Location", fmt.Sprintf("%s/0a1b2c3d-0000-4000-8000-%012d", r.URL.Path, n))
			w.Header().Set("ID", fmt.Sprintf("9f8e7d6c-0000-4000-8000-%012d", n))
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, "v=0 answer")
		case http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
		}
	}))
	defer mtx.Close()
	p := New(mtx.URL, mtx.URL, &tickets{})
	offer := func(path string, external bool) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("v=0 offer"))
		req.Header.Set("Content-Type", "application/sdp")
		rec := httptest.NewRecorder()
		if external {
			p.ExternalOffer(rec, req, "whep", path, netip.MustParseAddr("198.51.100.4"), "/rtc-session/")
		} else {
			p.WHEPOffer(rec, req, alice, path, "/api/v1/live/whep-session/")
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("offer: %d %s", rec.Code, rec.Body)
		}
	}
	offer("cam1", false)
	offer("cam2", true)
	byPath := map[string]Session{}
	for _, s := range p.Sessions() {
		byPath[s.Path] = s
	}
	ui, ext := byPath["cam1"], byPath["cam2"]
	if ui.Viewer != alice.Key() || ui.MediaMTX != "9f8e7d6c-0000-4000-8000-000000000001" || ui.Started.IsZero() ||
		ext.Viewer != "" || ext.MediaMTX != "9f8e7d6c-0000-4000-8000-000000000002" {
		t.Fatalf("sessions %+v", byPath)
	}
	if err := p.End(context.Background(), ui.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := strings.Join(deleted, " ")
	mu.Unlock()
	if got != "/cam1/whep/"+ui.ID {
		t.Errorf("upstream DELETEs: %q", got)
	}
	p.Forget([]string{ext.ID})
	if left := p.Sessions(); len(left) != 0 {
		t.Errorf("left: %+v", left)
	}
	if err := p.End(context.Background(), ui.ID); err != nil {
		t.Errorf("ending a session that is gone: %v", err)
	}
}
