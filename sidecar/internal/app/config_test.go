package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"mtxui/internal/auth"
)

func configInfo(t *testing.T, h *harness) ConfigInfo {
	t.Helper()
	rec := h.do("GET", "/api/v1/config", nil)
	var info ConfigInfo
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &info) != nil {
		t.Fatalf("GET config: %d %s", rec.Code, rec.Body)
	}
	return info
}

func lastAudit(t *testing.T, h *harness) (string, string, map[string]any) {
	t.Helper()
	events, _ := h.st.ListAudit(context.Background(), 1)
	if len(events) == 0 {
		t.Fatal("no audit entry")
	}
	return events[0].Action, events[0].Target, events[0].Details
}

func TestConfigAPI(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	start := configInfo(t, h)
	if start.Settings["authMethod"] != "http" || start.Settings["api"] != true || start.Locked["authMethod"] == "" ||
		start.PublicHost != "mtx.example.com" {
		t.Errorf("settings %v, locked %v", start.Settings, start.Locked)
	}
	if !strings.Contains(start.Content, "authMethod: http") || start.Snapshot == nil || start.Snapshot.SHA256 != start.SHA256 {
		t.Fatalf("config %+v", start.Snapshot)
	}

	// A path, saved surgically: the file keeps everything else.
	rec := h.do("PUT", "/api/v1/config/paths/live/cam1", map[string]any{"config": map[string]any{"source": "publisher", "record": true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT path: %d %s", rec.Code, rec.Body)
	}
	after := configInfo(t, h)
	if strings.Replace(after.Content, "  live/cam1:\n    record: yes\n    source: publisher\n", "", 1) != start.Content {
		t.Fatalf("the path edit disturbed the file:\n%s", after.Content)
	}
	if action, target, details := lastAudit(t, h); action != "config.path.save" || target != "live/cam1" || details["snapshot"] == nil {
		t.Errorf("audit: %s %s %v", action, target, details)
	}

	// Global settings and path defaults.
	if rec := h.do("PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"readTimeout": "20s"}}); rec.Code != http.StatusOK {
		t.Fatalf("PATCH global: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("PATCH", "/api/v1/config/path-defaults", map[string]any{"set": map[string]any{"maxReaders": 5}}); rec.Code != http.StatusOK {
		t.Fatalf("PATCH path defaults: %d %s", rec.Code, rec.Body)
	}
	cur := configInfo(t, h)
	if !strings.Contains(cur.Content, "readTimeout: 20s") || !strings.Contains(cur.Content, "maxReaders: 5") {
		t.Errorf("settings not written:\n%s", cur.Content)
	}
	var res WriteResult
	rec = h.do("PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"readTimeout": "20s"}})
	if _ = json.Unmarshal(rec.Body.Bytes(), &res); rec.Code != http.StatusOK || res.Changed {
		t.Errorf("an unchanged value: %d %s", rec.Code, rec.Body)
	}

	// Refusals: the sidecar's rules, MediaMTX's validation (a fake here: see mtxconf for the real one), hooks, names.
	for _, tt := range []struct {
		method, path string
		body         any
		code         int
		kind         string
	}{
		{"PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"authMethod": "internal"}}, 400, "locked"},
		{"PATCH", "/api/v1/config/global", map[string]any{"remove": []string{"apiAddress"}}, 400, "locked"},
		{"PATCH", "/api/v1/config/path-defaults", map[string]any{"set": map[string]any{"recordPath": "/tmp/x"}}, 422, "invalid_config"},
		{"PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"paths": map[string]any{}}}, 400, "invalid"},
		{"PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"a.b": 1}}, 400, "invalid"},
		{"PATCH", "/api/v1/config/global", map[string]any{}, 400, "invalid"},
		{"PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"runOnConnect": "curl evil"}}, 403, "hooks"},
		{"PUT", "/api/v1/config/paths/cam2", map[string]any{"config": map[string]any{"runOnReady": "sh -c id"}}, 403, "hooks"},
		{"PUT", "/api/v1/config/paths/cam2", map[string]any{"config": map[string]any{"recordPath": "/etc/%path"}}, 422, "invalid_config"},
		{"PUT", "/api/v1/config/paths/cam2", map[string]any{"config": map[string]any{"source": "http://169.254.169.254/latest"}}, 422, "invalid_config"},
		{"PUT", "/api/v1/config/paths/cam2", map[string]any{"config": map[string]any{"source": "rtsp://127.0.0.1:9997/"}}, 422, "invalid_config"},
		{"PUT", "/api/v1/config/paths/cam2", map[string]any{"config": map[string]any{"forward": []any{map[string]any{"dest": "rtmp://localhost/x"}}}}, 422, "invalid_config"},
		{"PUT", "/api/v1/config/paths/bad%20name!", map[string]any{"config": map[string]any{}}, 400, "invalid"},
		{"PUT", "/api/v1/config/paths/~[", map[string]any{"config": map[string]any{}}, 400, "invalid"},
		{"PUT", "/api/v1/config/paths/cam2", map[string]any{"config": map[string]any{"bad key": 1}}, 400, "invalid"},
		{"DELETE", "/api/v1/config/paths/nope", nil, 404, "not_found"},
		{"PUT", "/api/v1/config", map[string]any{"content": "a: 1\n"}, 400, "invalid"},
		{"PUT", "/api/v1/config", map[string]any{"content": "logLevel: debug\n", "sha256": "stale"}, 409, "conflict"},
		{"POST", "/api/v1/config/validate", map[string]any{"content": ""}, 422, "invalid_config"},
		{"GET", "/api/v1/config/snapshots/999", nil, 404, "not_found"},
		{"POST", "/api/v1/config/snapshots/x/restore", nil, 404, "not_found"},
	} {
		rec := h.do(tt.method, tt.path, tt.body)
		if rec.Code != tt.code || decode(rec)["error"] != tt.kind {
			t.Errorf("%s %s %v: %d %s, want %d %s", tt.method, tt.path, tt.body, rec.Code, rec.Body, tt.code, tt.kind)
		}
	}
	if now := configInfo(t, h); now.Content != cur.Content {
		t.Fatal("a refused change reached the file")
	}
	events, _ := h.st.ListAudit(context.Background(), 30)
	refusedHook := false
	for _, e := range events {
		if r, _ := e.Details["refused"].(string); e.Action == "config.path.save" && strings.Contains(r, "hooks editor") {
			refusedHook = true
		}
	}
	if !refusedHook {
		t.Error("a refused hook change is not in the audit log")
	}

	// The raw editor: validate, then replace the version it read.
	edited := strings.Replace(cur.Content, "readTimeout: 20s", "readTimeout: 30s", 1)
	if rec := h.do("POST", "/api/v1/config/validate", map[string]any{"content": edited}); rec.Code != http.StatusOK {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("PUT", "/api/v1/config", map[string]any{"content": edited, "sha256": cur.SHA256, "reason": "longer timeout"}); rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}

	// History and restore: restoring the first snapshot brings the setup-time file back, as a new snapshot.
	rec = h.do("GET", "/api/v1/config/snapshots", nil)
	var list []SnapshotInfo
	if _ = json.Unmarshal(rec.Body.Bytes(), &list); len(list) < 5 || list[0].Reason != "longer timeout" || list[0].Author != "admin" || list[0].Content != nil {
		t.Fatalf("history: %s", rec.Body)
	}
	first := list[len(list)-1]
	rec = h.do("GET", "/api/v1/config/snapshots/"+itoa(first.ID), nil)
	var snap SnapshotInfo
	if _ = json.Unmarshal(rec.Body.Bytes(), &snap); snap.Content == nil || *snap.Content != start.Content {
		t.Fatalf("snapshot %d: %s", first.ID, rec.Body)
	}
	if rec := h.do("POST", "/api/v1/config/snapshots/"+itoa(first.ID)+"/restore", nil); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	if now := configInfo(t, h); now.Content != start.Content || now.Snapshot.Reason != "restored snapshot "+itoa(first.ID) {
		t.Errorf("after the restore: %+v", now.Snapshot)
	}
	if rec := h.do("DELETE", "/api/v1/config/paths/live/cam1", nil); rec.Code != http.StatusNotFound {
		t.Errorf("the restore should have removed live/cam1: %d", rec.Code)
	}
}

func TestDriftDismiss(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	h.srv.d.Probe.Warn("config_drift", "changed outside")
	if rec := h.do("POST", "/api/v1/config/drift/dismiss", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss: %d", rec.Code)
	}
	for _, w := range h.srv.d.Probe.Status().Warnings {
		if w.Code == "config_drift" {
			t.Error("the drift warning survived")
		}
	}
	if action, _, _ := lastAudit(t, h); action != "config.drift.dismiss" {
		t.Errorf("audit: %s", action)
	}
}

func TestConfigNeedsAdmin(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	hash, _ := auth.NewHasher(fast, 1).Hash(context.Background(), "operator password")
	if _, err := h.st.CreateUser(context.Background(), "op", hash, "operator"); err != nil {
		t.Fatal(err)
	}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "op", "password": "operator password"}); rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	for _, r := range [][2]string{
		{"GET", "/api/v1/config"},
		{"PUT", "/api/v1/config"},
		{"GET", "/api/v1/config/snapshots"},
		{"PATCH", "/api/v1/config/global"},
		{"PUT", "/api/v1/config/paths/cam1"},
		{"DELETE", "/api/v1/config/paths/cam1"},
		{"POST", "/api/v1/config/snapshots/1/restore"},
	} {
		if rec := h.do(r[0], r[1], map[string]any{}); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as operator: %d", r[0], r[1], rec.Code)
		}
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
