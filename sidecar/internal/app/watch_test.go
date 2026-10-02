package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLayoutsAPI(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	cam := "cam1"
	wall := map[string]any{"version": 1, "columns": 2, "tiles": []any{cam, nil, "live/door"}}
	if rec := h.do("PUT", "/api/v1/layouts/Front%20wall", wall); rec.Code != http.StatusNoContent {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	rec := h.do("GET", "/api/v1/layouts", nil)
	var list []LayoutInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Name != "Front wall" ||
		list[0].Layout.Columns != 2 || len(list[0].Layout.Tiles) != 3 || list[0].Layout.Tiles[1] != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	for name, bad := range map[string]any{
		"version 2":        map[string]any{"version": 2, "columns": 1, "tiles": []any{}},
		"4 columns":        map[string]any{"version": 1, "columns": 4, "tiles": []any{}},
		"10 tiles":         map[string]any{"version": 1, "columns": 3, "tiles": make([]any, 10)},
		"a bad path":       map[string]any{"version": 1, "columns": 1, "tiles": []any{"../x"}},
		"an unknown field": map[string]any{"version": 1, "columns": 1, "tiles": []any{}, "script": "x"},
	} {
		if rec := h.do("PUT", "/api/v1/layouts/x", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := h.do("PUT", "/api/v1/layouts/..", wall); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad name: %d", rec.Code)
	}
	if rec := h.do("DELETE", "/api/v1/layouts/Front%20wall", nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := h.do("DELETE", "/api/v1/layouts/Front%20wall", nil); rec.Code != http.StatusNotFound {
		t.Errorf("delete twice: %d", rec.Code)
	}
}

// External WHIP/WHEP: no Origin, no cookie, no CSRF token (OBS sends none), and still not refused by the CSRF check;
// the path name is validated before anything reaches MediaMTX.
func TestExternalRTCRoutes(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	req := func(method, path string) int {
		r := httptest.NewRequest(method, "http://mtx.example.com"+path, strings.NewReader("v=0"))
		r.RemoteAddr = "198.51.100.4:5000"
		r.Header.Set("Content-Type", "application/sdp")
		r.Header.Set("Authorization", "Bearer x")
		rec := httptest.NewRecorder()
		h.public.ServeHTTP(rec, r)
		return rec.Code
	}
	if code := req("POST", "/whip/studio"); code == http.StatusForbidden {
		t.Error("the CSRF check refused an external WHIP offer")
	}
	if code := req("POST", "/whep/bad%20name!"); code != http.StatusBadRequest {
		t.Errorf("an invalid path name: %d", code)
	}
	if code := req("DELETE", "/rtc-session/unknown"); code != http.StatusNotFound {
		t.Errorf("an unknown session: %d", code)
	}
	// The browser's own API stays CSRF-protected.
	if code := req("POST", "/api/v1/live/whep/studio"); code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Errorf("the UI's WHEP proxy without a session or CSRF token: %d", code)
	}
}

// The layout on screen is kept separately from the named ones and never listed with them.
func TestCurrentWatchLayout(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	if rec := h.do("GET", "/api/v1/watch/current", nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("before any: %d %s", rec.Code, rec.Body)
	}
	cur := map[string]any{"version": 1, "columns": 3, "tiles": []any{"cam1", nil, "offline/cam"}}
	if rec := h.do("PUT", "/api/v1/watch/current", cur); rec.Code != http.StatusNoContent {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	var got LayoutData
	if err := json.Unmarshal(h.do("GET", "/api/v1/watch/current", nil).Body.Bytes(), &got); err != nil || got.Columns != 3 || len(got.Tiles) != 3 {
		t.Fatalf("current %+v %v", got, err)
	}
	if rec := h.do("GET", "/api/v1/layouts", nil); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("the current layout is listed: %s", rec.Body)
	}
	if rec := h.do("PUT", "/api/v1/watch/current", map[string]any{"version": 1, "columns": 4}); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid layout: %d", rec.Code)
	}
}
