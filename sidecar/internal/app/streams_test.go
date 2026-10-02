package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"mtxui/internal/store"
)

type streamJSON struct {
	ID         int64                     `json:"id"`
	Name       string                    `json:"name"`
	Title      string                    `json:"title"`
	Owner      *StreamOwner              `json:"owner"`
	Keys       map[string]*StreamKeyInfo `json:"keys"`
	CanManage  bool                      `json:"canManage"`
	CanAdmin   bool                      `json:"canAdmin"`
	Ingest     Ingest                    `json:"ingest"`
	MaxReaders int                       `json:"maxReaders"`
}

func (h *harness) json(rec *httptest.ResponseRecorder, v any) {
	h.t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) mtxFile() string {
	h.t.Helper()
	b, err := os.ReadFile(h.srv.d.Config.Path)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// The stream API end to end: an admin invites a streamer and creates streams; the streamer joins with the code and
// then reaches only its own stream, on every route.
func TestStreamsAndStreamers(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	admin := [2]string{h.cookie, h.csrf}

	rec := h.do("POST", "/api/v1/users", map[string]any{"username": "alice", "role": "streamer"})
	var inv Invitation
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body)
	}
	h.json(rec, &inv)
	if len(inv.JoinCode) != 19 || !inv.User.Pending || time.Until(inv.Expires) < 71*time.Hour {
		t.Fatalf("invitation %+v", inv)
	}
	if action, target, details := lastAudit(t, h); action != "user.invite" || target != "alice" ||
		strings.Contains(string(mustJSON(details)), strings.ReplaceAll(inv.JoinCode, "-", "")[:8]) {
		t.Errorf("audit %s %s %v", action, target, details)
	}
	if rec := h.do("POST", "/api/v1/users", map[string]any{"username": "alice", "role": "streamer"}); rec.Code != http.StatusConflict {
		t.Errorf("a taken username: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/users", map[string]any{"username": "eve", "role": "root"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown role: %d", rec.Code)
	}

	aliceID := inv.User.ID
	if rec := h.do("PATCH", "/api/v1/config/global", map[string]any{"set": map[string]any{"rtsp": true}}); rec.Code != http.StatusOK {
		t.Fatalf("RTSP on: %d %s", rec.Code, rec.Body)
	}
	rec = h.do("POST", "/api/v1/streams", map[string]any{"name": "live/alice", "title": "Alice live", "ownerId": aliceID})
	var alice streamJSON
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	h.json(rec, &alice)
	if alice.Owner == nil || alice.Owner.Username != "alice" || alice.Keys["publish"] == nil || alice.Keys["playback"] == nil ||
		alice.Ingest.Host != "mtx.example.com" || alice.Ingest.RTSP != 8554 || !alice.CanAdmin {
		t.Fatalf("stream %+v", alice)
	}
	if !strings.Contains(h.mtxFile(), "live/alice") {
		t.Error("the path is not in mediamtx.yml")
	}
	rec = h.do("POST", "/api/v1/streams", map[string]any{"name": "live/bob"})
	var bob streamJSON
	h.json(rec, &bob)
	for _, bad := range []map[string]any{
		{"name": "live/alice"},        // taken
		{"name": "~^live/.*$"},        // a pattern
		{"name": "all_others"},        // MediaMTX's catch-all
		{"name": "ok", "title": ""},   // (a missing title becomes the name: valid)
		{"name": "x", "ownerId": 999}, // no such user
	} {
		rec := h.do("POST", "/api/v1/streams", bad)
		if bad["name"] == "ok" {
			if rec.Code != http.StatusCreated {
				t.Errorf("%v: %d %s", bad, rec.Code, rec.Body)
			}
			continue
		}
		if rec.Code < 400 {
			t.Errorf("%v accepted: %d", bad, rec.Code)
		}
	}

	// An invited account cannot sign in before joining, however it is tried.
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "alice", "password": ""}); rec.Code != http.StatusUnauthorized {
		t.Errorf("sign-in before joining: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": "AAAA-BBBB-CCCC-DDDD", "password": "a strong password 1"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong code: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": "short"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a weak password: %d", rec.Code)
	}
	rec = h.do("POST", "/api/v1/join", map[string]any{"code": strings.ToLower(inv.JoinCode), "password": "alice streams a lot"})
	if rec.Code != http.StatusCreated || decode(rec)["user"].(map[string]any)["role"] != "streamer" {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}
	aliceSession := [2]string{h.cookie, h.csrf}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": "another password 2"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a used code: %d", rec.Code)
	}
	h.cookie, h.csrf = aliceSession[0], aliceSession[1]

	// As alice: her stream, and nothing else.
	var list []streamJSON
	h.json(h.do("GET", "/api/v1/streams", nil), &list)
	if len(list) != 1 || list[0].Name != "live/alice" || !list[0].CanManage || list[0].CanAdmin {
		t.Fatalf("alice's list %+v", list)
	}
	path := func(id int64, rest string) string { return "/api/v1/streams/" + itoa(id) + rest }
	for _, r := range [][2]string{
		{"GET", path(bob.ID, "")},
		{"PATCH", path(bob.ID, "")},
		{"GET", path(bob.ID, "/history")},
		{"POST", path(bob.ID, "/keys/publish/reveal")},
		{"POST", path(bob.ID, "/keys/publish/regenerate")},
		{"POST", path(bob.ID, "/disconnect")},
	} {
		if rec := h.do(r[0], r[1], map[string]any{}); rec.Code != http.StatusNotFound {
			t.Errorf("alice %s %s: %d", r[0], r[1], rec.Code)
		}
	}
	for _, p := range []string{"/api/v1/live/hls/live/bob/index.m3u8"} {
		if rec := h.do("GET", p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("alice watching bob: %d", rec.Code)
		}
	}
	if rec := h.do("POST", "/api/v1/live/whep/live/bob", nil, contentType("application/sdp")); rec.Code != http.StatusNotFound {
		t.Errorf("alice opening WHEP on bob: %d", rec.Code)
	}
	if rec := h.do("GET", "/api/v1/live/hls/live/alice/index.m3u8", nil); rec.Code == http.StatusNotFound &&
		strings.Contains(rec.Body.String(), "No such stream") {
		t.Errorf("alice cannot watch her own stream")
	}

	rec = h.do("POST", path(alice.ID, "/keys/publish/reveal"), nil)
	var key StreamKey
	h.json(rec, &key)
	if rec.Code != http.StatusOK || key.Name != alice.Keys["publish"].Name || len(key.Secret) < 16 {
		t.Fatalf("reveal: %d %+v", rec.Code, key)
	}
	if c, ok, err := h.creds.ByPassword(ctx, key.Name, key.Secret); err != nil || !ok || h.creds.Check(c, "publish", "live/alice", somewhere, time.Now()) != nil {
		t.Fatalf("the revealed key does not publish: %v %v", ok, err)
	}
	if c, _, _ := h.creds.ByPassword(ctx, key.Name, key.Secret); h.creds.Check(c, "publish", "live/bob", somewhere, time.Now()) == nil {
		t.Error("alice's key publishes to bob")
	}
	if action, _, details := lastAudit(t, h); action != "stream.key.reveal" || strings.Contains(string(mustJSON(details)), key.Secret) {
		t.Errorf("audit %s %v", action, details)
	}
	rec = h.do("POST", path(alice.ID, "/keys/publish/regenerate"), nil)
	var fresh StreamKey
	h.json(rec, &fresh)
	if rec.Code != http.StatusOK || fresh.Name == key.Name || fresh.Secret == key.Secret {
		t.Fatalf("regenerate: %d %+v", rec.Code, fresh)
	}
	if c, _, _ := h.creds.ByPassword(ctx, key.Name, key.Secret); h.creds.Check(c, "publish", "live/alice", somewhere, time.Now()) == nil {
		t.Error("the old key still works")
	}
	// The stream page's form always sends the cap: 0 with none set before must save the rest.
	if rec := h.do("PATCH", path(alice.ID, ""), map[string]any{"maxReaders": 0, "title": "Alice", "public": false}); rec.Code != http.StatusOK ||
		decode(rec)["public"] != false {
		t.Fatalf("patch without a cap: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("PATCH", path(alice.ID, ""), map[string]any{"maxReaders": 5, "title": "Alice on air"}); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(h.mtxFile(), "maxReaders: 5") {
		t.Errorf("the viewer cap is not in mediamtx.yml:\n%s", h.mtxFile())
	}
	for _, n := range []int{5, 0, 0} { // the same cap again, back to none, none again: all fine
		if rec := h.do("PATCH", path(alice.ID, ""), map[string]any{"maxReaders": n}); rec.Code != http.StatusOK {
			t.Fatalf("cap %d: %d %s", n, rec.Code, rec.Body)
		}
	}
	if strings.Contains(h.mtxFile(), "maxReaders") {
		t.Errorf("the cap stayed after removing it:\n%s", h.mtxFile())
	}
	if rec := h.do("PATCH", path(alice.ID, ""), map[string]any{"ownerId": 1}); rec.Code != http.StatusForbidden {
		t.Errorf("alice changing the owner: %d", rec.Code)
	}
	if rec := h.do("POST", path(alice.ID, "/disconnect"), nil); rec.Code != http.StatusConflict {
		t.Errorf("disconnect with nothing publishing: %d", rec.Code)
	}
	if rec := h.do("GET", path(alice.ID, "/history"), nil); rec.Code != http.StatusOK {
		t.Errorf("history: %d", rec.Code)
	}
	minute := time.Now().Truncate(time.Minute).Add(-time.Minute)
	_ = h.st.AddHistoryMinute(context.Background(), store.HistoryPoint{T: minute}, map[string]store.PathPoint{"live/alice": {InBps: 8000, Readers: 3}})
	var day []map[string]any
	h.json(h.do("GET", path(alice.ID, "/history?range=24h"), nil), &day)
	if len(day) != 1 || day[0]["readers"] != 3.0 || day[0]["inBps"] != 8000.0 {
		t.Errorf("a day's history: %v", day)
	}

	// No viewer, operator or admin route lets a streamer in.
	for _, r := range h.srv.Routes() {
		if r.Access == Public || r.Access == Streamer || r.Access == PerOperation {
			continue
		}
		method, p := r.Method, strings.ReplaceAll(strings.ReplaceAll(r.Pattern, "/*", "/x"), "{id}", itoa(alice.ID))
		p = strings.NewReplacer("{name}", "x", "{kind}", "publish").Replace(p)
		if rec := h.do(method, p, map[string]any{}); rec.Code != http.StatusForbidden {
			t.Errorf("streamer %s %s: %d", method, p, rec.Code)
		}
	}

	// Back as admin: deleting alice keeps her stream without an owner; deleting the stream removes the path and its
	// keys stop working.
	h.cookie, h.csrf = admin[0], admin[1]
	if rec := h.do("DELETE", "/api/v1/users/"+itoa(1), nil); rec.Code != http.StatusConflict {
		t.Errorf("deleting yourself: %d", rec.Code)
	}
	if rec := h.do("DELETE", "/api/v1/users/"+itoa(aliceID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete alice: %d %s", rec.Code, rec.Body)
	}
	var after streamJSON
	h.json(h.do("GET", path(alice.ID, ""), nil), &after)
	if after.Owner != nil || after.Title != "Alice on air" {
		t.Errorf("after deleting the owner: %+v", after)
	}
	if rec := h.do("DELETE", path(alice.ID, ""), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete stream: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(h.mtxFile(), "live/alice") {
		t.Error("the path is still in mediamtx.yml")
	}
	if c, _, _ := h.creds.ByPassword(ctx, fresh.Name, fresh.Secret); h.creds.Check(c, "publish", "live/alice", somewhere, time.Now()) == nil {
		t.Error("a deleted stream's key still works")
	}
	if action, target, _ := lastAudit(t, h); action != "stream.delete" || target != "live/alice" {
		t.Errorf("audit %s %s", action, target)
	}
	var people []Person
	h.json(h.do("GET", "/api/v1/users", nil), &people)
	if len(people) != 1 || people[0].Username != "admin" {
		t.Errorf("people %+v", people)
	}
}

var somewhere = netip.MustParseAddr("203.0.113.9")

func TestPeopleEditing(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	var inv Invitation
	h.json(h.do("POST", "/api/v1/users", map[string]any{"username": "bob", "role": "streamer"}), &inv)
	bob := itoa(inv.User.ID)
	if rec := h.do("PATCH", "/api/v1/users/"+bob, map[string]any{"role": "operator"}); rec.Code != http.StatusNoContent {
		t.Fatalf("role: %d %s", rec.Code, rec.Body)
	}
	if action, target, details := lastAudit(t, h); action != "user.role" || target != "bob" || details["to"] != "operator" {
		t.Errorf("audit %s %s %v", action, target, details)
	}
	if rec := h.do("PATCH", "/api/v1/users/"+bob, map[string]any{"role": "root"}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown role: %d", rec.Code)
	}
	if rec := h.do("PATCH", "/api/v1/users/1", map[string]any{"role": "viewer"}); rec.Code != http.StatusConflict {
		t.Errorf("an admin demoting themselves: %d", rec.Code)
	}
	// A new join code for someone who has joined resets their password when used.
	h.signOutLocally()
	h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": "bob's first password"})
	h.signOutLocally()
	h.signInAdmin()
	var reset Invitation
	h.json(h.do("POST", "/api/v1/users/"+bob+"/join-code", nil), &reset)
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": reset.JoinCode, "password": "bob's second password"}); rec.Code != http.StatusCreated {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "bob", "password": "bob's second password"}); rec.Code != http.StatusOK {
		t.Errorf("sign-in with the new password: %d", rec.Code)
	}
}

// signInAdmin signs the setup admin back in.
func (h *harness) signInAdmin() {
	h.t.Helper()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "correct horse battery"}); rec.Code != http.StatusOK {
		h.t.Fatalf("admin sign-in: %d", rec.Code)
	}
}

// Public streams: anyone reads the watch link's info and the auth hook lets anonymous readers in; a private stream
// is invisible there.
func TestPublicStreams(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/open", "title": "Open"}), &st)
	var priv streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/closed", "public": false}), &priv)
	admin := [2]string{h.cookie, h.csrf}

	h.signOutLocally()
	rec := h.do("GET", "/api/v1/public/streams/live/open", nil)
	if rec.Code != http.StatusOK || decode(rec)["title"] != "Open" || decode(rec)["live"] != false {
		t.Fatalf("public info: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/api/v1/public/streams/live/closed", "/api/v1/public/streams/nope", "/api/v1/public/hls/live/closed/index.m3u8"} {
		if rec := h.do("GET", p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if !h.srv.IsPublicPath("live/open") || h.srv.IsPublicPath("live/closed") || h.srv.IsPublicPath("test") {
		t.Error("IsPublicPath")
	}

	// Made private, the link stops working at once.
	h.cookie, h.csrf = admin[0], admin[1]
	if rec := h.do("PATCH", "/api/v1/streams/"+itoa(st.ID), map[string]any{"public": false}); rec.Code != http.StatusOK || decode(rec)["public"] != false {
		t.Fatalf("private: %d %s", rec.Code, rec.Body)
	}
	if h.srv.IsPublicPath("live/open") {
		t.Error("still public after the change")
	}
	h.signOutLocally()
	if rec := h.do("GET", "/api/v1/public/streams/live/open", nil); rec.Code != http.StatusNotFound {
		t.Errorf("private stream's link: %d", rec.Code)
	}
}
