package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mtxui/internal/portgate"
	"mtxui/internal/store"
)

// exposureHarness is a set-up harness with RTMP, RTSP and WebRTC on, the dry-run helper installed and one stream.
func exposureHarness(t *testing.T) (*harness, *portgate.Helper, streamJSON) {
	t.Helper()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	if rec := h.do("PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"rtmp": true, "webrtc": true, "rtsp": true, "srt": false}}); rec.Code != http.StatusOK {
		t.Fatalf("protocols on: %d %s", rec.Code, rec.Body)
	}
	helper := h.startPortgate()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/alice"}), &st)
	return h, helper, st
}

func (h *harness) desiredWant() map[string]portgate.Want {
	h.t.Helper()
	h.srv.autoOnce(context.Background())
	d, err := h.exposure.Desired()
	if err != nil {
		h.t.Fatal(err)
	}
	return d.Want
}

func (h *harness) ourRules() string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, "ufw.rules"))
	if err != nil && !os.IsNotExist(err) {
		h.t.Fatal(err)
	}
	var ours []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "'"+portgate.CommentPrefix) {
			ours = append(ours, l)
		}
	}
	return strings.Join(ours, "\n")
}

// A close-all on the host holds while the sidecar runs: the sidecar takes it in by itself (automatic rules off, manual
// openings closed, audited), so its routine renewals reopen nothing; an admin opens again on top of it.
func TestHostCloseAllIsTakenIn(t *testing.T) {
	ctx := context.Background()
	h, helper, st := exposureHarness(t)
	if rec := h.do("POST", "/api/v1/streams/"+itoa(st.ID)+"/lease", nil); decode(rec)["leased"] != true {
		t.Fatalf("lease: %s", rec.Body)
	}
	if rec := h.do("PUT", "/api/v1/exposure/rtsp", map[string]any{"sources": []string{"198.51.100.0/24"}, "hours": 2}); rec.Code != http.StatusOK {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	h.desiredWant()
	if _, err := helper.Apply(ctx); err != nil || h.ourRules() == "" {
		t.Fatalf("nothing open: %v", err)
	}

	closed, err := helper.CloseAll(ctx)
	if err != nil || h.ourRules() != "" {
		t.Fatalf("close-all: %v %s", err, h.ourRules())
	}
	// The next round (the 15 s loop) takes it in instead of renewing the lease.
	if w := h.desiredWant(); len(w) != 0 {
		t.Fatalf("renewed after the close-all: %+v", w)
	}
	if r := h.srv.autoRules(ctx); r.Publish || r.Remember || r.Viewers {
		t.Errorf("rules %+v", r)
	}
	events, _ := h.st.ListAudit(ctx, 1)
	if len(events) != 1 || events[0].Action != "exposure.close-all" || events[0].Actor != "system" || events[0].Target != "host" {
		t.Errorf("audit %+v", events)
	}
	if d, _ := h.exposure.Desired(); d.ClosedAll == nil || !d.ClosedAll.Equal(*closed.ClosedAll) {
		t.Fatalf("not taken in: %+v", d)
	}
	if s, err := helper.Apply(ctx); err != nil || s.ClosedAll != nil || s.Error != "" || h.ourRules() != "" {
		t.Fatalf("after taking it in: %+v %v %s", s, err, h.ourRules())
	}
	// The stream page keeps renewing its lease: with the rule off, nothing opens.
	h.do("POST", "/api/v1/streams/"+itoa(st.ID)+"/lease", nil)
	if w := h.desiredWant(); len(w) != 0 {
		t.Fatalf("reopened: %+v", w)
	}

	// An admin's opening right after another close-all on the host, before the loop came round, applies on top of it.
	if _, err := helper.CloseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := h.do("PUT", "/api/v1/exposure/rtsp", map[string]any{"sources": []string{"198.51.100.0/24"}, "hours": 2}); rec.Code != http.StatusOK {
		t.Fatalf("open after the close-all: %d %s", rec.Code, rec.Body)
	}
	if s, err := helper.Apply(ctx); err != nil || s.ClosedAll != nil || s.Error != "" || s.Ports["rtsp"].State != portgate.StateOpen {
		t.Fatalf("status %+v %v", s, err)
	}
}

// A stream page holds at most three addresses open, however many it renews its lease from: one streamer cannot fill
// a port's sources and crowd out the other streams' encoders, and leases do not pile up.
func TestLeasesPerStreamAreCapped(t *testing.T) {
	h, _, st := exposureHarness(t)
	addrs := []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5", "198.51.100.6"}
	for _, ip := range addrs {
		if rec := h.do("POST", "/api/v1/streams/"+itoa(st.ID)+"/lease", nil, header("X-Forwarded-For", ip)); decode(rec)["leased"] != true {
			t.Fatalf("lease from %s: %s", ip, rec.Body)
		}
		h.desiredWant() // every lease is seen by the loop, so each was open for a while
	}
	h.srv.auto.mu.Lock()
	n := len(h.srv.auto.leases[st.ID])
	h.srv.auto.mu.Unlock()
	if n != perStream {
		t.Errorf("%d leases", n)
	}
	w := h.desiredWant()
	for _, port := range []string{"rtmp", "rtsp", "webrtc"} {
		if got := w[port].Sources; len(got) != perStream || !slices.Contains(got, "198.51.100.6/32") {
			t.Errorf("%s: %v", port, got)
		}
	}
}

// Openings that stand for a record end with it: a deleted stream's remembered encoders, a revoked guest key.
func TestRecordOpeningsEndWithTheirRecords(t *testing.T) {
	ctx := context.Background()
	h, _, st := exposureHarness(t)
	_ = h.srv.setAutoRules(ctx, AutoRules{Publish: true, Remember: true})
	if err := h.st.TouchEncoderAddress(ctx, store.EncoderAddress{StreamID: st.ID, Port: "rtmp", IP: "198.51.100.30", LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if w := h.desiredWant(); !slices.Contains(w["rtmp"].Sources, "198.51.100.30/32") {
		t.Fatalf("known encoder: %+v", w)
	}
	if rec := h.do("DELETE", "/api/v1/streams/"+itoa(st.ID), nil); rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if w := h.desiredWant(); len(w) != 0 {
		t.Fatalf("the deleted stream's encoder is still let in: %+v", w)
	}

	var other streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/guest"}), &other)
	base := "/api/v1/streams/" + itoa(other.ID) + "/guest-keys"
	var made NewGuestKey
	h.json(h.do("POST", base, map[string]any{"kind": "publish", "label": "Bob", "hours": 6}), &made)
	if w := h.desiredWant(); len(w["rtmp"].Sources) != 0 || w["rtmp"].Until == nil {
		t.Fatalf("RTMP for the guest: %+v", w)
	}
	if rec := h.do("DELETE", base+"/"+itoa(made.Guest.ID), nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if w := h.desiredWant(); len(w) != 0 {
		t.Fatalf("still open to anyone after the guest key was revoked: %+v", w)
	}
}
