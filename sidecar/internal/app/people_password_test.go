package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// An admin sets anyone's password outright, without the usual rules: the person is signed out everywhere, an
// outstanding join code stops working, and the audit log says the rules were not met (never the password).
func TestAdminSetsPassword(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	var inv Invitation
	h.json(h.do("POST", "/api/v1/users", map[string]any{"username": "bob", "role": "streamer"}), &inv)
	bob := "/api/v1/users/" + itoa(inv.User.ID) + "/password"

	// Bob has not joined yet: a three-character password makes him an account he can sign in to.
	if rec := h.do("PUT", bob, map[string]any{"password": "q7z"}); rec.Code != http.StatusNoContent {
		t.Fatalf("a short password: %d %s", rec.Code, rec.Body)
	}
	if action, target, details := lastAudit(t, h); action != "user.password.set" || target != "bob" ||
		details["meetsRules"] != false || strings.Contains(allAudit(t, h), "q7z") {
		t.Errorf("audit %s %s %v", action, target, details)
	}
	admin := [2]string{h.cookie, h.csrf}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": "bob's own password"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("the join code after a set password: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "bob", "password": "q7z"}); rec.Code != http.StatusOK {
		t.Fatalf("bob signs in with it: %d %s", rec.Code, rec.Body)
	}
	bobSession := [2]string{h.cookie, h.csrf}
	if rec := h.do("PUT", bob, map[string]any{"password": "x"}); rec.Code != http.StatusForbidden {
		t.Errorf("a streamer sets a password: %d", rec.Code)
	}

	// The username as the password: accepted too; bob's session ends.
	h.cookie, h.csrf = admin[0], admin[1]
	if rec := h.do("PUT", bob, map[string]any{"password": "bob"}); rec.Code != http.StatusNoContent {
		t.Fatalf("the username as password: %d %s", rec.Code, rec.Body)
	}
	if _, _, details := lastAudit(t, h); details["endedSessions"] != float64(1) {
		t.Errorf("ended sessions: %v", details)
	}
	h.cookie, h.csrf = bobSession[0], bobSession[1]
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("bob's session after the change: %d", rec.Code)
	}

	// Not empty, not past the hashing cap.
	h.cookie, h.csrf = admin[0], admin[1]
	for _, bad := range []string{"", strings.Repeat("x", maxPasswordBytes+1)} {
		if rec := h.do("PUT", bob, map[string]any{"password": bad}); rec.Code != http.StatusBadRequest {
			t.Errorf("%d bytes: %d", len(bad), rec.Code)
		}
	}

	// An admin setting their own: their own session stays.
	if rec := h.do("PUT", "/api/v1/users/1/password", map[string]any{"password": "1"}); rec.Code != http.StatusNoContent {
		t.Fatalf("own password: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != http.StatusOK {
		t.Errorf("the admin's own session: %d", rec.Code)
	}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "1"}); rec.Code != http.StatusOK {
		t.Errorf("the admin signs in with the new one: %d", rec.Code)
	}
}

// allAudit is the whole audit log as text, to look for what must never be in it.
func allAudit(t *testing.T, h *harness) string {
	t.Helper()
	events, err := h.st.ListAudit(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%+v", events)
}
