package app

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"mtxui/internal/store"
)

// Guest keys end to end: a manager makes a publish and a read key; the secret is shown once and never logged; the
// key is scoped to the stream and expires; while a publish key is valid the publishing ports are open to anyone;
// revoking ends it; at most ten are valid at once; deleting the stream revokes them.
func TestGuestKeys(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	if rec := h.do("PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"rtmp": true, "srt": false}}); rec.Code != http.StatusOK {
		t.Fatalf("RTMP on: %d %s", rec.Code, rec.Body)
	}
	h.startPortgate()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/guest"}), &st)
	base := "/api/v1/streams/" + itoa(st.ID) + "/guest-keys"

	for _, bad := range []map[string]any{
		{"kind": "admin", "label": "x", "hours": 6},
		{"kind": "publish", "label": "", "hours": 6},
		{"kind": "publish", "label": "x", "hours": 5},
		{"kind": "publish", "label": strings.Repeat("x", 61), "hours": 6},
	} {
		if rec := h.do("POST", base, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", bad, rec.Code)
		}
	}

	rec := h.do("POST", base, map[string]any{"kind": "publish", "label": "Bob (co-host)", "hours": 6})
	var made NewGuestKey
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	h.json(rec, &made)
	g := made.Guest
	if made.Secret == "" || g.State != "active" || g.Kind != "publish" || !strings.HasPrefix(g.Name, "guest-") ||
		g.ExpiresAt == nil || time.Until(*g.ExpiresAt) < 5*time.Hour || time.Until(*g.ExpiresAt) > 6*time.Hour {
		t.Fatalf("made %+v", made)
	}
	if action, target, details := lastAudit(t, h); action != "stream.guest.create" || target != "live/guest" ||
		strings.Contains(string(mustJSON(details)), made.Secret) {
		t.Errorf("audit %s %s %v", action, target, details)
	}
	c, err := h.st.CredentialByName(ctx, g.Name)
	if err != nil || !slices.Equal(c.Paths, []string{"live/guest"}) || !slices.Equal(c.Actions, []string{"publish"}) {
		t.Errorf("credential %+v %v", c, err)
	}
	rec = h.do("GET", base, nil)
	if strings.Contains(rec.Body.String(), made.Secret) || !strings.Contains(rec.Body.String(), "Bob (co-host)") {
		t.Errorf("list %s", rec.Body)
	}

	// While it is valid, RTMP is open to anyone for the guest's encoder.
	h.srv.autoOnce(ctx)
	d, _ := h.exposure.Desired()
	if w, ok := d.Want["rtmp"]; !ok || len(w.Sources) != 0 {
		t.Errorf("RTMP for the guest: %+v", d.Want)
	}

	rec = h.do("DELETE", base+"/"+itoa(g.ID), nil)
	var revoked GuestKeyInfo
	h.json(rec, &revoked)
	if rec.Code != http.StatusOK || revoked.State != "revoked" {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("DELETE", base+"/"+itoa(g.ID), nil); rec.Code != http.StatusOK {
		t.Errorf("revoking twice: %d", rec.Code)
	}
	if guests, _ := h.st.ActiveGuestKeys(ctx, time.Now()); len(guests) != 0 {
		t.Errorf("still active: %+v", guests)
	}

	// At most ten valid at once (revoked ones do not count).
	for i := range maxGuestKeys {
		if rec := h.do("POST", base, map[string]any{"kind": "read", "label": "viewer " + itoa(int64(i)), "hours": 1}); rec.Code != http.StatusCreated {
			t.Fatalf("read key %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := h.do("POST", base, map[string]any{"kind": "read", "label": "one too many", "hours": 1}); rec.Code != http.StatusConflict {
		t.Errorf("an eleventh key: %d", rec.Code)
	}

	// Deleting the stream revokes what is still valid.
	if rec := h.do("DELETE", "/api/v1/streams/"+itoa(st.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("stream delete: %d", rec.Code)
	}
	list, _ := h.st.ListCredentials(ctx)
	for _, c := range list {
		if strings.HasPrefix(c.Name, "guest-") && c.RevokedAt == nil {
			t.Errorf("%s still valid after the stream went", c.Name)
		}
	}
}

func TestGuestInfoState(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	for _, tc := range []struct {
		c    store.Credential
		want string
	}{
		{store.Credential{ExpiresAt: &future}, "active"},
		{store.Credential{ExpiresAt: &past}, "expired"},
		{store.Credential{ExpiresAt: &future, RevokedAt: &past}, "revoked"},
	} {
		if got := guestInfo(store.GuestKey{Credential: tc.c}, now).State; got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.c, got, tc.want)
		}
	}
}
