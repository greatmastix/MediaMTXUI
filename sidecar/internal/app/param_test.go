package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Path names arrive the way browsers write them (encodeURIComponent): escapes that Go would not have chosen still
// name the same path.
func TestEscapedNames(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()

	const escaped = "/api/v1/config/paths/~%5Ecams/(.%2B)%24"
	if rec := h.do("PUT", escaped, map[string]any{"config": map[string]any{"record": true}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if c := configInfo(t, h).Content; !strings.Contains(c, "~^cams/(.+)$:") || strings.Contains(c, "%5E") {
		t.Fatalf("saved under another name:\n%s", c)
	}
	if rec := h.do("DELETE", escaped, nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	if c := configInfo(t, h).Content; strings.Contains(c, "cams") {
		t.Fatalf("not deleted:\n%s", c)
	}
}

// `mtxui reset-password` (internal/cli) uses IssueJoinCode: the code it prints sets a new password at /join.
func TestIssueJoinCodeResetsAPassword(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	u, err := h.st.UserByName(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := IssueJoinCode(context.Background(), h.st, u, "cli")
	if err != nil {
		t.Fatal(err)
	}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": code, "password": "a brand new passphrase"}); rec.Code != http.StatusCreated {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "a brand new passphrase"}); rec.Code != http.StatusOK {
		t.Fatalf("sign-in with the new password: %d %s", rec.Code, rec.Body)
	}
}
