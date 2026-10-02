package app

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"mtxui/internal/mtxauth"
)

func TestCredentialsAPI(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()

	rec := h.do("POST", "/api/v1/credentials", map[string]any{
		"name": "obs", "actions": []string{"publish"}, "paths": []string{"studio"}, "expiresInHours": 24,
	})
	var created CredentialInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || rec.Code != http.StatusCreated ||
		len(created.Secret) < 16 || created.State != "active" || created.ExpiresAt == nil || created.CreatedBy != "admin" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if action, target, details := lastAudit(t, h); action != "credential.add" || target != "obs" ||
		strings.Contains(string(mustJSON(details)), created.Secret) {
		t.Errorf("audit: %s %s %v", action, target, details)
	}
	for _, bad := range []map[string]any{
		{"name": "obs", "actions": []string{"read"}},                            // taken
		{"name": "x", "actions": []string{"api"}},                               // the API is the sidecar's
		{"name": "mtxui-evil", "actions": []string{"read"}},                     // reserved
		{"name": "y", "actions": []string{"read"}, "sources": []string{"nope"}}, // bad CIDR
		{"name": "z", "actions": []string{"read"}, "expiresInHours": 24 * 400},  // too long
	} {
		if rec := h.do("POST", "/api/v1/credentials", bad); rec.Code < 400 {
			t.Errorf("%v accepted: %d", bad, rec.Code)
		}
	}

	rec = h.do("GET", "/api/v1/credentials", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.Secret) || !strings.Contains(rec.Body.String(), `"name":"obs"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}

	// Revoking closes what the credential opened, and what MediaMTX lists under its name.
	c, _ := h.st.CredentialByName(context.Background(), "obs")
	h.mu.Lock()
	h.opened[c.ID] = []mtxauth.SessionRef{{Protocol: "rtmp", ID: "11111111-2222-3333-4444-555555555555"}, {Protocol: "hls", ID: "gone"}}
	h.mu.Unlock()
	rec = h.do("POST", "/api/v1/credentials/obs/revoke", nil)
	if rec.Code != http.StatusOK || decode(rec)["kicked"] != float64(1) {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if !slices.Contains(h.kicks, "/v3/rtmp/conns/kick/11111111-2222-3333-4444-555555555555") || !slices.Contains(h.kicks, "/v3/hls/sessions/kick/gone") {
		t.Errorf("kicks %v", h.kicks)
	}
	if action, _, details := lastAudit(t, h); action != "credential.revoke" || details["kicked"] != float64(1) {
		t.Errorf("audit: %s %v", action, details)
	}
	rec = h.do("GET", "/api/v1/credentials", nil)
	if !strings.Contains(rec.Body.String(), `"state":"revoked"`) {
		t.Errorf("after revoke: %s", rec.Body)
	}
	if rec := h.do("POST", "/api/v1/credentials/nope/revoke", nil); rec.Code != http.StatusNotFound {
		t.Errorf("revoking an unknown credential: %d", rec.Code)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
