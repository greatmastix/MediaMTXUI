package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"mtxui/internal/credentials"
	"mtxui/internal/store"
)

// Valid guest keys count, show and go with the stream however many newer keys were made and revoked: the cap, the
// list and the revocation on deletion used to see only the newest 50 keys, so ten valid keys behind fifty revoked
// ones let ten more be made, vanished from the page and outlived the stream.
func TestGuestKeysBehindRevokedOnes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/many"}), &st)
	base := "/api/v1/streams/" + itoa(st.ID) + "/guest-keys"
	for i := range maxGuestKeys {
		if rec := h.do("POST", base, map[string]any{"kind": "publish", "label": "guest " + itoa(int64(i)), "hours": 168}); rec.Code != http.StatusCreated {
			t.Fatalf("guest key %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	for i := range 50 { // newer keys, revoked: what making and revoking one key after another leaves
		_, c, err := h.creds.Add(ctx, credentials.Spec{
			Name: "guest-old" + itoa(int64(i)), Kind: credentials.KindPassword,
			Actions: []string{"publish"}, Paths: []string{"live/many"}, TTL: time.Hour, CreatedBy: "admin",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.AddGuestKey(ctx, st.ID, c.ID, store.GuestPublish, "old"); err != nil {
			t.Fatal(err)
		}
		if err := h.creds.Revoke(ctx, c.Name); err != nil {
			t.Fatal(err)
		}
	}

	if rec := h.do("POST", base, map[string]any{"kind": "publish", "label": "one too many", "hours": 1}); rec.Code != http.StatusConflict {
		t.Errorf("an eleventh valid key: %d %s", rec.Code, rec.Body)
	}
	var list []GuestKeyInfo
	h.json(h.do("GET", base, nil), &list)
	active := 0
	for _, g := range list {
		if g.State == "active" {
			active++
		}
	}
	if active != maxGuestKeys {
		t.Errorf("the page lists %d of %d valid keys", active, maxGuestKeys)
	}
	if rec := h.do("DELETE", "/api/v1/streams/"+itoa(st.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("stream delete: %d", rec.Code)
	}
	creds, _ := h.st.ListCredentials(ctx)
	for _, c := range creds {
		if strings.HasPrefix(c.Name, "guest-") && c.RevokedAt == nil {
			t.Errorf("%s still valid after the stream went", c.Name)
		}
	}
}
