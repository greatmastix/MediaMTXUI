package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The append-only audit log takes nothing from anonymous requests but the named actions (sign-in, setup, joining),
// keeps every field small, and never stores what was typed as an unknown username (often a password).
func TestAuditAnonymous(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	count := func() int {
		events, _ := h.st.ListAudit(context.Background(), 1000)
		return len(events)
	}
	h.signOutLocally()
	before := count()
	for _, path := range []string{"/api/v1/auth/passkey/begin", "/api/v1/streams", "/api/v1/nope", "/api/v1/auth/login/code"} {
		h.do("POST", path, rawBody(strings.Repeat("x", 10000)), contentType("application/json"))
	}
	if n := count(); n != before {
		t.Fatalf("anonymous requests wrote %d audit entries", n-before)
	}
	h.do("POST", "/api/v1/auth/login", map[string]any{"username": "Tr0ub4dor-3", "password": "x"})
	if action, target, _ := lastAudit(t, h); action != "auth.login" || target != "(unknown username)" {
		t.Fatalf("a failed sign-in for an unknown name: %s %q", action, target)
	}
	h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "wrong"})
	if _, target, d := lastAudit(t, h); target != "admin" || d["result"] != "failed" {
		t.Fatalf("a failed sign-in for a real user: %q %v", target, d)
	}
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": adminPassword}); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
}
