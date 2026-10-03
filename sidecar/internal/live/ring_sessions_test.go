package live

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mtxui/internal/auth"
)

// bigUpdate is an update of about n bytes, like the connection lists produce while anonymous clients flood MediaMTX's
// stream ports.
func bigUpdate(n int) update {
	item, _ := json.Marshal(map[string]string{"id": strings.Repeat("x", n)})
	return update{Kind: "rtspConns", Available: true, Upsert: []json.RawMessage{item}}
}

// The ring holds at most ringMax bytes of event data, whatever the event count: with nobody subscribed, big updates
// would otherwise pile up to ringSize of them. A browser that falls off the ring resumes with a snapshot.
func TestRingIsBoundedInBytes(t *testing.T) {
	h := newHub(t, newFake())
	h.ringMax = 64 << 10
	h.Poll(context.Background())
	sub, _ := h.Subscribe(auth.RoleAdmin, "")
	last := sub.Initial[0].ID
	h.Unsubscribe(sub)

	h.mu.Lock()
	for range 40 {
		h.publishLocked("update", auth.RoleOperator, bigUpdate(4<<10))
	}
	total := 0
	for _, ev := range h.ring {
		total += len(ev.Data)
	}
	n, counted := len(h.ring), h.ringLen
	h.mu.Unlock()
	if total > h.ringMax || counted != total || n == 0 || n >= 40 {
		t.Fatalf("ring: %d events, %d bytes (counted %d), bound %d", n, total, counted, h.ringMax)
	}
	sub, _ = h.Subscribe(auth.RoleAdmin, last)
	defer h.Unsubscribe(sub)
	if len(sub.Initial) != 1 || sub.Initial[0].Type != "snapshot" {
		t.Errorf("resume past the evicted events: %d events, first %q", len(sub.Initial), sub.Initial[0].Type)
	}

	// An event bigger than the whole bound is not kept at all.
	h.mu.Lock()
	h.publishLocked("update", auth.RoleOperator, bigUpdate(h.ringMax+1))
	n, counted = len(h.ring), h.ringLen
	h.mu.Unlock()
	if n != 0 || counted != 0 {
		t.Errorf("after an oversized event: %d events, %d bytes", n, counted)
	}
}

// A subscriber that stops reading is dropped once its waiting events exceed subMax bytes, long before its channel
// (subBuffer events) is full; one that keeps reading is not.
func TestSubscriberIsBoundedInBytes(t *testing.T) {
	h := newHub(t, newFake())
	h.subMax = 64 << 10
	h.Poll(context.Background())
	stalled, _ := h.Subscribe(auth.RoleAdmin, "")
	reading, _ := h.Subscribe(auth.RoleAdmin, "")
	defer h.Unsubscribe(reading)
	const events, size = 32, 4 << 10 // 128 KiB in all, twice the bound
	got := 0
	for range events {
		h.mu.Lock()
		h.publishLocked("update", auth.RoleOperator, bigUpdate(size))
		h.mu.Unlock()
		select {
		case <-reading.C:
			got++
		default:
		}
	}
	if got != events {
		t.Errorf("the reading subscriber got %d events of %d", got, events)
	}
	n := 0
	for range stalled.C {
		n++
	}
	if n == 0 || n*size > h.subMax {
		t.Errorf("the stalled subscriber had %d events of %d bytes waiting when it was dropped (bound %d)", n, size, h.subMax)
	}
}

// Listed answers from the last poll, and says when that answer means nothing.
func TestListed(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.set("webrtcSessionsList", map[string]any{"id": "s1", "path": "cam1", "state": "read"})
	h := newHub(t, f)
	if _, fresh := h.Listed("webrtc", "s1"); fresh {
		t.Error("fresh before the first poll")
	}
	h.Poll(ctx)
	for _, tc := range []struct {
		protocol, id  string
		listed, fresh bool
	}{
		{"webrtc", "s1", true, true},
		{"webrtc", "s2", false, true},
		{"rtsp", "s1", false, true},
		{"api", "s1", false, false},
	} {
		if listed, fresh := h.Listed(tc.protocol, tc.id); listed != tc.listed || fresh != tc.fresh {
			t.Errorf("Listed(%s, %s) = %v, %v", tc.protocol, tc.id, listed, fresh)
		}
	}
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	h.Poll(ctx)
	if _, fresh := h.Listed("webrtc", "s2"); fresh {
		t.Error("fresh while MediaMTX does not answer")
	}
}
