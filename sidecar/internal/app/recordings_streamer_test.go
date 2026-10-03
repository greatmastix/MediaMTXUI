package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A streamer sees the recordings of its own streams, and only those; deleting stays with operators.
func TestStreamerSeesOwnRecordings(t *testing.T) {
	h, f := recordingsHarness(t, nil)
	var inv Invitation
	h.json(h.do("POST", "/api/v1/users", map[string]any{"username": "alice", "role": "streamer"}), &inv)
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/alice", "ownerId": inv.User.ID}), &st)
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	f.add("live/alice", base, 1000)
	f.add("live/bob", base, 500)
	h.srv.recordingsOnce(context.Background())

	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": "alice streams a lot"}); rec.Code != http.StatusCreated {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Paths []RecordingPath `json:"paths"`
	}
	h.json(h.do("GET", "/api/v1/recordings", nil), &list)
	if len(list.Paths) != 1 || list.Paths[0].Name != "live/alice" {
		t.Fatalf("a streamer's list: %+v", list.Paths)
	}
	if rec := h.do("GET", "/api/v1/recordings/spans?path=live/alice", nil); rec.Code != http.StatusOK {
		t.Errorf("own spans: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{
		"/api/v1/recordings/spans?path=live/bob", "/api/v1/recordings/segments?path=live/bob",
		"/api/v1/recordings/export?path=live/bob&start=2026-10-01T10:00:00Z&duration=10",
	} {
		if rec := h.do("GET", path, nil); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "live/bob") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := h.do("POST", "/api/v1/recordings/delete", map[string]any{"path": "live/alice"}); rec.Code != http.StatusForbidden {
		t.Errorf("a streamer deletes: %d", rec.Code)
	}
}
