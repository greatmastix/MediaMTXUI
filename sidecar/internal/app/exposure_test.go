package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtxui/internal/portgate"
)

// The exposure API against a real helper with the dry-run driver, in the harness's temporary directories.
func TestExposureAPI(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()

	rec := h.do("GET", "/api/v1/exposure", nil)
	if rec.Code != http.StatusOK || decode(rec)["installed"] != false || decode(rec)["clientIP"] != client {
		t.Fatalf("before the helper: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("PUT", "/api/v1/exposure/rtsp", map[string]any{"myAddress": true, "hours": 1}); rec.Code != http.StatusServiceUnavailable ||
		decode(rec)["error"] != "not_installed" {
		t.Fatalf("open without the helper: %d %s", rec.Code, rec.Body)
	}

	rules := filepath.Join(h.dir, "ufw.rules")
	helper := &portgate.Helper{
		Policy: &portgate.Policy{
			Driver: "dry-run", DryRunFile: rules, Desired: filepath.Join(h.dir, "portgate", "desired.json"),
			StateDir: filepath.Join(h.dir, "portgate-status"),
			Ports:    map[string]portgate.PortSpec{"rtsp": {Proto: "tcp", Port: 8554}, "srt": {Proto: "udp", Port: 8890}},
			Limits: portgate.Limits{
				AllowAnySource: true, MaxTTLAnySource: portgate.Duration(time.Hour), MaxTTL: portgate.Duration(24 * time.Hour),
				MaxSources: 4, MinPrefixV4: 16, MinPrefixV6: 48,
			},
		},
		Driver: &portgate.UFW{Run: (&portgate.FakeUFW{File: rules}).Run},
	}
	apply := func() {
		t.Helper()
		if _, err := helper.Apply(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	apply()

	rec = h.do("PUT", "/api/v1/exposure/rtsp", map[string]any{"myAddress": true, "sources": []string{"198.51.100.0/24"}, "hours": 2})
	if rec.Code != http.StatusOK || decode(rec)["pending"] != true {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	if action, target, details := lastAudit(t, h); action != "exposure.open" || target != "rtsp" ||
		!strings.Contains(string(mustJSON(details)), client+"/32") {
		t.Errorf("audit: %s %s %v", action, target, details)
	}
	apply()
	b, _ := os.ReadFile(rules)
	if !strings.Contains(string(b), "from 203.0.113.7 to any port 8554 proto tcp comment 'mtx-portgate:rtsp'") ||
		!strings.Contains(string(b), "from 198.51.100.0/24 to any port 8554") {
		t.Fatalf("rules after open:\n%s", b)
	}
	rec = h.do("GET", "/api/v1/exposure", nil)
	if decode(rec)["pending"] != false || !strings.Contains(rec.Body.String(), `"state":"open"`) {
		t.Fatalf("after the helper ran: %s", rec.Body)
	}

	for _, bad := range []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/exposure/ssh", map[string]any{"myAddress": true, "hours": 1}},                         // unknown port
		{"/api/v1/exposure/srt", map[string]any{"myAddress": true}},                                     // no duration
		{"/api/v1/exposure/srt", map[string]any{"myAddress": true, "permanent": true}},                  // not allowed by policy
		{"/api/v1/exposure/srt", map[string]any{"hours": 2}},                                            // anyone for longer than 1 h
		{"/api/v1/exposure/srt", map[string]any{"sources": []string{"10.0.0.0/8"}, "hours": 1}},         // wider than /16
		{"/api/v1/exposure/srt", map[string]any{"sources": []string{"; reboot"}, "hours": 1}},           // not an address
		{"/api/v1/exposure/srt", map[string]any{"sources": []string{"203.0.113.9"}, "hours": 24 * 367}}, // beyond any limit
	} {
		if rec := h.do("PUT", bad.path, bad.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %v: %d %s", bad.path, bad.body, rec.Code, rec.Body)
		}
	}
	// "My address" behind a proxy the sidecar does not trust would be the proxy's: refused.
	if rec := h.do("PUT", "/api/v1/exposure/srt", map[string]any{"myAddress": true, "hours": 1}, header("X-Forwarded-For", "10.1.2.3")); rec.Code != http.StatusBadRequest ||
		!strings.Contains(decode(rec)["message"].(string), "not a public one") {
		t.Errorf("private address: %d %s", rec.Code, rec.Body)
	}

	if rec := h.do("DELETE", "/api/v1/exposure/rtsp", nil); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body)
	}
	if action, target, _ := lastAudit(t, h); action != "exposure.close" || target != "rtsp" {
		t.Errorf("audit: %s %s", action, target)
	}
	h.do("PUT", "/api/v1/exposure/srt", map[string]any{"hours": 1})
	if rec := h.do("POST", "/api/v1/exposure/close-all", nil); rec.Code != http.StatusOK {
		t.Fatalf("close all: %d %s", rec.Code, rec.Body)
	}
	if action, _, _ := lastAudit(t, h); action != "exposure.close-all" {
		t.Errorf("audit: %s", action)
	}
	apply()
	if b, _ := os.ReadFile(rules); strings.Contains(string(b), "mtx-portgate") {
		t.Fatalf("rules after close-all:\n%s", b)
	}
}
