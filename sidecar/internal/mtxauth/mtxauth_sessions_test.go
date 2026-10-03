package mtxauth

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A success does not clear an address's failures: holding one valid key (a guest's, say), a client could otherwise
// spend it every 19 guesses and never be throttled.
func TestSuccessKeepsFailures(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bad := Request{IP: "198.51.100.1", User: "cam1", Password: "wrong", Action: "read", Path: "cam1", Protocol: "rtsp"}
	good := Request{IP: "198.51.100.1", User: "cam1", Password: f.pub, Action: "read", Path: "cam1", Protocol: "rtsp"}
	for round := range 3 {
		for range throttleFailures - 1 {
			f.h.Decide(ctx, bad)
		}
		if d := f.h.Decide(ctx, good); !d.Allow {
			if round == 0 {
				t.Fatalf("the right key before the threshold: %+v", d)
			}
			return // throttled: the earlier failures still counted
		}
	}
	t.Error("a valid key between batches of guesses kept the address from being throttled")
}

// Tickets stay bounded however fast they are issued; expired ones are swept now and then, not on every issue.
func TestTicketsBounded(t *testing.T) {
	v := NewViewers()
	client := netip.MustParseAddr("203.0.113.7")
	for range maxTickets + 10 {
		v.Issue("live/open", "public", client)
	}
	if len(v.m) > maxTickets {
		t.Errorf("%d live tickets kept", len(v.m))
	}
	last, _ := v.Issue("live/open", "public", client)
	if _, ok := v.m[last]; !ok {
		t.Error("the newest ticket was not kept")
	}
}

type kicks struct {
	mu    sync.Mutex
	paths []string
}

func (k *kicks) Post(_ context.Context, path string) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.paths = append(k.paths, path)
	if strings.Contains(path, "/rtsp/") && strings.HasSuffix(path, "conn-1") {
		return http.StatusNotFound, nil // a connection's id, not a session's
	}
	return http.StatusOK, nil
}

func (k *kicks) take() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := k.paths
	k.paths = nil
	slices.Sort(out)
	return out
}

// Anonymous reads of a public stream are remembered, and disconnected once the stream is no longer public; reads
// with a credential, and the anonymous readers of streams that are still public, are left alone.
func TestPublicReadsWithdrawn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	public := map[string]bool{"live/open": true, "live/other": true}
	f.h.Public = func(path string) bool {
		mu.Lock()
		defer mu.Unlock()
		return public[path]
	}
	id := func(s string) *string { return &s }
	for _, req := range []Request{
		{IP: "203.0.113.7", Action: "read", Path: "live/open", Protocol: "rtsp", ID: id("conn-1")}, // DESCRIBE
		{IP: "203.0.113.7", Action: "read", Path: "live/open", Protocol: "rtsp", ID: id("sess-1")}, // SETUP
		{IP: "203.0.113.8", Action: "read", Path: "live/open", Protocol: "webrtc", ID: id("whep-1")},
		{IP: "203.0.113.9", Action: "read", Path: "live/other", Protocol: "srt", ID: id("srt-1")},
		{IP: "203.0.113.10", Token: f.token, Action: "read", Path: "live/open", Protocol: "webrtc", ID: id("token-1")},
	} {
		if d := f.h.Decide(ctx, req); !d.Allow {
			t.Fatalf("%+v refused: %s", req, d.Reason)
		}
	}
	k := &kicks{}
	if n := f.h.withdraw(ctx, k); n != 0 || len(k.take()) != 0 {
		t.Fatalf("withdrew %d while every stream is public", n)
	}
	mu.Lock()
	public["live/open"] = false
	mu.Unlock()
	n := f.h.withdraw(ctx, k)
	want := []string{"/v3/rtsp/sessions/kick/conn-1", "/v3/rtsp/sessions/kick/sess-1", "/v3/webrtc/sessions/kick/whep-1"}
	if got := k.take(); n != 2 || !slices.Equal(got, want) {
		t.Errorf("made private: %d closed, kicks %v", n, got)
	}
	if n := f.h.withdraw(ctx, k); n != 0 || len(k.take()) != 0 {
		t.Errorf("the withdrawn reads were not forgotten: %d", n)
	}
	creds, _ := f.creds.List(ctx)
	for _, c := range creds {
		if c.Name == "viewer-1" {
			if got := f.h.SessionsOf(c.ID); len(got) != 1 || got[0].ID != "token-1" {
				t.Errorf("the token's read: %+v", got)
			}
		}
	}
	// The stream is private now: anonymous readers are refused, and nothing new is remembered.
	if d := f.h.Decide(ctx, Request{IP: "203.0.113.7", Action: "read", Path: "live/open", Protocol: "rtsp", ID: id("sess-2")}); d.Allow {
		t.Error("an anonymous read of a private stream")
	}
	if paths := f.h.opened.publicPaths(); !slices.Equal(paths, []string{"live/other"}) {
		t.Errorf("public reads on record: %v", paths)
	}
}

// Anyone can add anonymous reads of a public stream to the record: a flood of them stays within maxPublic and never
// pushes out what credentials opened.
func TestPublicReadsBounded(t *testing.T) {
	var o opened
	t0 := time.Unix(1_800_000_000, 0)
	o.add(subject{cred: 3}, SessionRef{"webrtc", "publisher"}, t0)
	for i := range maxPublic + 100 {
		o.add(subject{path: "live/open"}, SessionRef{"rtsp", fmt.Sprint("conn-", i)}, t0.Add(time.Duration(i+1)*time.Millisecond))
	}
	if o.public != maxPublic || len(o.by[subject{path: "live/open"}]) != maxPublic || o.n != 1 {
		t.Errorf("%d anonymous entries (%d recorded), %d credential entries", o.public, len(o.by[subject{path: "live/open"}]), o.n)
	}
	if got := o.take(subject{cred: 3}); len(got) != 1 {
		t.Errorf("the credential's session was pushed out: %v", got)
	}
}

type listing map[string]bool // protocol/id -> listed; absent: not listed

func (l listing) Listed(protocol, id string) (bool, bool) {
	if l == nil {
		return false, false // MediaMTX did not answer
	}
	return l[protocol+"/"+id], true
}

// The record forgets sessions MediaMTX no longer lists once they are past the grace period, rather than dropping the
// oldest entries (the longest-running sessions) when it is full; while MediaMTX does not answer, nothing goes.
func TestPruneForgetsEndedSessions(t *testing.T) {
	var o opened
	t0 := time.Unix(1_800_000_000, 0)
	cred := subject{cred: 7}
	o.add(cred, SessionRef{"webrtc", "alive"}, t0)
	o.add(cred, SessionRef{"webrtc", "ended"}, t0)
	o.add(cred, SessionRef{"rtsp", "fresh"}, t0.Add(sessionGrace))
	o.add(subject{path: "live/open"}, SessionRef{"rtmp", "gone"}, t0)
	cutoff := t0.Add(sessionGrace / 2)
	l := listing{"webrtc/alive": true}
	gone := func(l Listing) func(SessionRef) bool {
		return func(ref SessionRef) bool {
			listed, fresh := l.Listed(ref.Protocol, ref.ID)
			return fresh && !listed
		}
	}
	if n := o.prune(cutoff, gone(listing(nil))); n != 0 {
		t.Errorf("pruned %d without an answer from MediaMTX", n)
	}
	if n := o.prune(cutoff, gone(l)); n != 2 || o.n != 2 {
		t.Errorf("pruned %d, %d left", n, o.n)
	}
	got := o.take(cred)
	slices.SortFunc(got, func(a, b SessionRef) int { return strings.Compare(a.ID, b.ID) })
	if !slices.Equal(got, []SessionRef{{"webrtc", "alive"}, {"rtsp", "fresh"}}) {
		t.Errorf("kept %v", got)
	}
	if len(o.publicPaths()) != 0 {
		t.Error("an emptied public stream is still on record")
	}
}
