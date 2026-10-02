package app

import (
	"context"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mtxui/internal/portgate"
)

// Automatic exposure against a dry-run helper: a stream page's lease opens its publishing ports to the browser, a
// live publisher's port opens to it and is remembered, viewers get WebRTC while something is live, and each rule can
// be switched off.
func TestAutomaticExposure(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	if rec := h.do("PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"rtmp": true, "webrtc": true, "rtsp": true, "srt": false}}); rec.Code != http.StatusOK {
		t.Fatalf("protocols on: %d %s", rec.Code, rec.Body)
	}
	helper := h.startPortgate()
	rec := h.do("POST", "/api/v1/streams", map[string]any{"name": "live/alice"})
	var st streamJSON
	h.json(rec, &st)
	want := func() map[string]portgate.Want {
		t.Helper()
		h.srv.autoOnce(ctx)
		d, err := h.exposure.Desired()
		if err != nil {
			t.Fatal(err)
		}
		return d.Want
	}
	if w := want(); len(w) != 0 {
		t.Fatalf("opened with nothing going on: %+v", w)
	}

	// The stream page's lease: publishing ports to the browser's address (the harness's client, 203.0.113.7).
	rec = h.do("POST", "/api/v1/streams/"+itoa(st.ID)+"/lease", nil)
	if rec.Code != http.StatusOK || decode(rec)["leased"] != true {
		t.Fatalf("lease: %d %s", rec.Code, rec.Body)
	}
	w := want()
	for _, port := range []string{"rtmp", "rtsp", "webrtc"} {
		if !slices.Contains(w[port].Sources, client+"/32") {
			t.Errorf("%s not open to the browser: %+v", port, w[port])
		}
	}
	if _, ok := w["srt"]; ok {
		t.Error("SRT opened though MediaMTX has it off")
	}
	// A browser behind an untrusted proxy (a private address) gets no lease.
	if rec := h.do("POST", "/api/v1/streams/"+itoa(st.ID)+"/lease", nil, header("X-Forwarded-For", "10.1.2.3")); decode(rec)["leased"] != false {
		t.Errorf("private address leased: %s", rec.Body)
	}

	// Live over RTMP from 198.51.100.20: its port opens to it, the address is remembered, viewers get WebRTC.
	h.mu.Lock()
	h.paths = `[{"name":"live/alice","online":true,"source":{"type":"rtmpConn","id":"c1"}}]`
	h.lists["/v3/rtmp/conns/list"] = `[{"id":"c1","remoteAddr":"198.51.100.20:50000","state":"publish","path":"live/alice"}]`
	h.mu.Unlock()
	h.hub.Poll(ctx)
	h.srv.auto.mu.Lock()
	h.srv.auto.leases = map[int64]map[netip.Addr]time.Time{} // the page was closed
	h.srv.auto.mu.Unlock()
	w = want()
	// With viewers let in, every output protocol is open to anyone while live; without, only the encoder gets in.
	if len(w["rtmp"].Sources) != 0 || len(w["webrtc"].Sources) != 0 || len(w["rtsp"].Sources) != 0 || w["webrtc"].Until == nil {
		t.Fatalf("while live: %+v", w)
	}
	_ = h.srv.setAutoRules(ctx, AutoRules{Publish: true, Remember: true})
	if w := want(); !slices.Contains(w["rtmp"].Sources, "198.51.100.20/32") {
		t.Fatalf("while live, viewers not let in: %+v", w)
	}
	_ = h.srv.setAutoRules(ctx, AutoRules{Publish: true, Remember: true, Viewers: true})
	want() // still live: viewers are let in again
	if _, err := helper.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.exposure.Status(); st.Error != "" || st.Ports["rtmp"].State != portgate.StateOpen {
		t.Fatalf("the helper refused the automatic state: %+v", st)
	}

	// Offline: nothing closes at once (a reconnect changes no rule and costs no cloud API call)...
	h.mu.Lock()
	h.paths = `[{"name":"live/alice","online":false}]`
	h.lists = map[string]string{}
	h.mu.Unlock()
	h.hub.Poll(ctx)
	if w := want(); len(w["webrtc"].Sources) != 0 || w["webrtc"].Until == nil || len(w["rtmp"].Sources) != 0 {
		t.Fatalf("closed at once after going offline: %+v", w)
	}
	// ...but once their expiry has passed, only the known encoder is left.
	later := time.Now().Add(time.Hour)
	h.srv.auto.mu.Lock()
	h.srv.auto.clock = func() time.Time { return later }
	h.srv.auto.mu.Unlock()
	w = want()
	if !slices.Contains(w["rtmp"].Sources, "198.51.100.20/32") || time.Until(*w["rtmp"].Until) < 29*24*time.Hour {
		t.Errorf("known encoder: %+v", w["rtmp"])
	}
	if _, ok := w["webrtc"]; ok {
		t.Errorf("WebRTC for viewers an hour after anything was live: %+v", w["webrtc"])
	}
	var view struct {
		Auto  []portgate.AutoOpening `json:"auto"`
		Rules AutoRules              `json:"rules"`
	}
	h.json(h.do("GET", "/api/v1/exposure", nil), &view)
	if len(view.Auto) != 1 || !strings.Contains(view.Auto[0].Reason, "known encoder of live/alice") || !view.Rules.Remember {
		t.Errorf("view %+v", view)
	}

	// Switching the rule off closes it; close-all switches every rule off.
	if rec := h.do("PUT", "/api/v1/exposure/auto", map[string]any{"publish": true, "remember": false, "viewers": true}); rec.Code != http.StatusOK {
		t.Fatalf("rules: %d %s", rec.Code, rec.Body)
	}
	if action, _, _ := lastAudit(t, h); action != "exposure.auto" {
		t.Errorf("audit %s", action)
	}
	if w := want(); len(w) != 0 {
		t.Errorf("after switching remember off: %+v", w)
	}
	if rec := h.do("POST", "/api/v1/exposure/close-all", nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if r := h.srv.autoRules(ctx); r.Publish || r.Remember || r.Viewers {
		t.Errorf("rules after close-all: %+v", r)
	}
}

// startPortgate runs the exposure helper once with the dry-run driver, so the sidecar sees it installed.
func (h *harness) startPortgate() *portgate.Helper {
	h.t.Helper()
	ctx := context.Background()
	t := h.t
	rules := filepath.Join(h.dir, "ufw.rules")
	helper := &portgate.Helper{
		Policy: &portgate.Policy{
			Driver: "dry-run", DryRunFile: rules, Desired: filepath.Join(h.dir, "portgate", "desired.json"),
			StateDir: filepath.Join(h.dir, "portgate-status"),
			Ports: map[string]portgate.PortSpec{
				"rtsp": {Proto: "tcp", Port: 8554}, "rtmp": {Proto: "tcp", Port: 1935},
				"srt": {Proto: "udp", Port: 8890}, "webrtc": {Proto: "udp", Port: 8189},
			},
			Limits: portgate.Limits{
				AllowAnySource: true, MaxTTLAnySource: portgate.Duration(12 * time.Hour), MaxTTL: portgate.Duration(720 * time.Hour),
				MaxSources: 8, MinPrefixV4: 16, MinPrefixV6: 48,
			},
		},
		Driver: &portgate.UFW{Run: (&portgate.FakeUFW{File: rules}).Run},
	}
	if _, err := helper.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	return helper
}
