package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"mtxui/internal/buildinfo"
)

// fakeGitHub answers the latest-release request with release (a JSON body), counting the requests.
func fakeGitHub(t *testing.T, release *atomic.Value) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		body, _ := release.Load().(string)
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func release(tag string, extra string) string {
	return fmt.Sprintf(`{"tag_name":%q,"html_url":"https://github.com/greatmastix/MediaMTXUI/releases/tag/%s","published_at":"2026-11-01T10:00:00Z","draft":false,"prerelease":false%s}`, tag, tag, extra)
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = old })
}

// A newer release shows for admins until each skips it or asks for later; one newer still shows again; drafts,
// pre-releases and pages elsewhere are not taken; the switch turns it all off, and so does MTXUI_UPDATE_CHECK.
func TestUpdateNotice(t *testing.T) {
	withVersion(t, "v1.4.0")
	ctx := context.Background()
	var rel atomic.Value
	gh, asked := fakeGitHub(t, &rel)
	h := newHarness(t, nil, fast)
	h.completeSetup()
	h.srv.updates.url = gh.URL
	info := func() UpdatesInfo {
		t.Helper()
		var u UpdatesInfo
		rec := h.do("GET", "/api/v1/updates", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("updates: %d %s", rec.Code, rec.Body)
		}
		h.json(rec, &u)
		return u
	}
	check := func(body string) UpdatesInfo {
		t.Helper()
		rel.Store(body)
		h.srv.checkUpdate(ctx)
		return info()
	}

	if u := check(release("v1.4.0", "")); !u.Checking || u.Latest != "1.4.0" || u.Available || u.Show || u.Current != "v1.4.0" {
		t.Errorf("up to date: %+v", u)
	}
	for _, c := range []struct{ name, body string }{
		{"a draft", release("v1.5.0", `,"draft":true`)},
		{"a pre-release", release("v1.5.0", `,"prerelease":true`)},
		{"a pre-release tag", release("v1.5.0-rc.1", "")},
		{"a page elsewhere", `{"tag_name":"v1.5.0","html_url":"https://example.com/v1.5.0"}`},
		{"no release yet", ""},
	} {
		if u := check(c.body); u.Show || u.Latest == "1.5.0" {
			t.Errorf("%s was taken: %+v", c.name, u)
		}
	}
	u := check(release("v1.5.0", ""))
	if !u.Available || !u.Show || u.Latest != "1.5.0" || u.Page != "https://github.com/greatmastix/MediaMTXUI/releases/tag/v1.5.0" {
		t.Fatalf("a newer release: %+v", u)
	}

	// Skipped by this admin; another admin still sees it.
	if rec := h.do("PUT", "/api/v1/updates/dismissal", map[string]any{"action": "skip"}); rec.Code != http.StatusOK {
		t.Fatalf("skip: %d %s", rec.Code, rec.Body)
	}
	if action, target, _ := lastAudit(t, h); action != "updates.skip" || target != "1.5.0" {
		t.Errorf("audit of the skip: %s %s", action, target)
	}
	if u := info(); u.Show || !u.Available || u.Skipped != "1.5.0" {
		t.Errorf("after skipping: %+v", u)
	}
	_, other := h.join("second", "admin")
	admin := [2]string{h.cookie, h.csrf}
	h.cookie, h.csrf = other[0], other[1]
	if u := info(); !u.Show {
		t.Errorf("another admin's notice went too: %+v", u)
	}
	// Later: a week.
	if rec := h.do("PUT", "/api/v1/updates/dismissal", map[string]any{"action": "later"}); rec.Code != http.StatusOK {
		t.Fatalf("later: %d %s", rec.Code, rec.Body)
	}
	if u := info(); u.Show || u.SnoozedUntil == nil || u.SnoozedUntil.Sub(*u.CheckedAt).Hours() < 24*7-1 {
		t.Errorf("after later: %+v", u)
	}
	h.cookie, h.csrf = admin[0], admin[1]

	// A version newer than the one skipped shows again.
	if u := check(release("v1.5.1", "")); !u.Show || u.Latest != "1.5.1" {
		t.Errorf("newer than the skipped one: %+v", u)
	}
	if rec := h.do("PUT", "/api/v1/updates/dismissal", map[string]any{"action": "now"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown answer: %d", rec.Code)
	}

	// Switched off: nothing shows, and nothing is asked.
	if rec := h.do("PUT", "/api/v1/updates/settings", map[string]any{"check": false}); rec.Code != http.StatusOK {
		t.Fatalf("switch off: %d %s", rec.Code, rec.Body)
	}
	if action, _, d := lastAudit(t, h); action != "updates.check" || d["check"] != false {
		t.Errorf("audit of the switch: %s %v", action, d)
	}
	if u := info(); u.Checking || u.Show || u.Latest != "" {
		t.Errorf("switched off: %+v", u)
	}
	if on, _ := h.srv.updateCheckOn(ctx); on {
		t.Error("the check still runs")
	}
	if rec := h.do("PUT", "/api/v1/updates/settings", map[string]any{"check": true}); rec.Code != http.StatusOK || !decodeBool(rec, "checking") {
		t.Errorf("switch on: %d %s", rec.Code, rec.Body)
	}

	// MTXUI_UPDATE_CHECK=off: the switch stays off.
	h.srv.d.Settings.UpdateCheck = false
	if u := info(); u.Checking || u.Locked == "" {
		t.Errorf("locked: %+v", u)
	}
	if rec := h.do("PUT", "/api/v1/updates/settings", map[string]any{"check": true}); rec.Code != http.StatusConflict {
		t.Errorf("switching on against MTXUI_UPDATE_CHECK: %d", rec.Code)
	}
	h.srv.d.Settings.UpdateCheck = true

	// Only admins see any of it.
	_, viewer := h.join("viewer", "viewer")
	h.cookie, h.csrf = viewer[0], viewer[1]
	if rec := h.do("GET", "/api/v1/updates", nil); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer: %d", rec.Code)
	}
	h.cookie, h.csrf = admin[0], admin[1]
	if n := asked.Load(); n != 8 {
		t.Errorf("GitHub was asked %d times, once per check", n)
	}
}

// A build that is no release never asks.
func TestNoUpdateCheckForDevBuilds(t *testing.T) {
	for _, v := range []string{"dev", "edge-0123abc"} {
		withVersion(t, v)
		var rel atomic.Value
		rel.Store(release("v9.0.0", ""))
		gh, asked := fakeGitHub(t, &rel)
		h := newHarness(t, nil, fast)
		h.completeSetup()
		h.srv.updates.url = gh.URL
		if on, why := h.srv.updateCheckOn(context.Background()); on || why == "" {
			t.Errorf("%s: check on (%q)", v, why)
		}
		var u UpdatesInfo
		h.json(h.do("GET", "/api/v1/updates", nil), &u)
		if u.Checking || u.Show || asked.Load() != 0 {
			t.Errorf("%s: %+v, asked %d", v, u, asked.Load())
		}
	}
}

func TestVersionOrder(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"1.5.0", "v1.4.0", true},
		{"1.4.1", "1.4.0", true},
		{"2.0.0", "1.99.99", true},
		{"1.10.0", "1.9.0", true},
		{"1.4.0", "1.4.0", false},
		{"1.3.9", "1.4.0", false},
	} {
		a, _ := parseVersion(c.a)
		b, _ := parseVersion(c.b)
		if a.newer(b) != c.newer {
			t.Errorf("%s newer than %s: %v", c.a, c.b, !c.newer)
		}
	}
	for _, bad := range []string{"dev", "edge-abc", "1.4", "v1.4.0-rc.1", "1.4.0.1", ""} {
		if _, ok := parseVersion(bad); ok {
			t.Errorf("%q parsed as a release", bad)
		}
	}
}

func decodeBool(rec *httptest.ResponseRecorder, key string) bool {
	b, _ := decode(rec)[key].(bool)
	return b
}
