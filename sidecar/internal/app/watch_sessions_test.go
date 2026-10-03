package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mtxui/internal/liveproxy"
	"mtxui/internal/mtxauth"
)

// fakeWebRTC is MediaMTX's WebRTC signalling: each offer gets a session URL and an ID header, and DELETEs are noted.
type fakeWebRTC struct {
	mu      sync.Mutex
	n       int
	deleted []string // session URL paths
}

func (f *fakeWebRTC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPost:
		f.n++
		w.Header().Set("Location", fmt.Sprintf("%s/0a1b2c3d-0000-4000-8000-%012d", r.URL.Path, f.n))
		w.Header().Set("ID", fmt.Sprintf("9f8e7d6c-0000-4000-8000-%012d", f.n))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "v=0 answer")
	case http.MethodDelete:
		f.deleted = append(f.deleted, r.URL.Path)
	}
}

func (f *fakeWebRTC) wasDeleted(sessionURL string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.deleted, func(p string) bool { return strings.HasSuffix(sessionURL, p) })
}

// watchHarness is a signed-in admin's harness whose live view talks to a fake WebRTC server.
func watchHarness(t *testing.T, env map[string]string) (*harness, *fakeWebRTC) {
	h := newHarness(t, env, fast)
	h.completeSetup()
	rtc := &fakeWebRTC{}
	srv := httptest.NewServer(rtc)
	t.Cleanup(srv.Close)
	h.srv.d.Watch = liveproxy.New(h.mtx.URL, srv.URL, mtxauth.NewViewers())
	return h, rtc
}

// join invites a user with role and signs them in; it returns their id and session, and leaves the admin signed in.
// Each joins from an address of their own (joining is rate-limited per address).
func (h *harness) join(name, role string) (int64, [2]string) {
	h.t.Helper()
	admin := [2]string{h.cookie, h.csrf}
	var inv Invitation
	h.json(h.do("POST", "/api/v1/users", map[string]any{"username": name, "role": role}), &inv)
	h.signOutLocally()
	from := header("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", inv.User.ID%250))
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": name + " watches a lot"}, from); rec.Code != http.StatusCreated {
		h.t.Fatalf("join %s: %d %s", name, rec.Code, rec.Body)
	}
	session := [2]string{h.cookie, h.csrf}
	h.cookie, h.csrf = admin[0], admin[1]
	return inv.User.ID, session
}

// watch opens a live view of path over WebRTC as session, and returns its session URL.
func (h *harness) watch(session [2]string, path string) string {
	h.t.Helper()
	keep := [2]string{h.cookie, h.csrf}
	h.cookie, h.csrf = session[0], session[1]
	rec := h.do("POST", "/api/v1/live/whep/"+path, rawBody("v=0 offer"), contentType("application/sdp"))
	h.cookie, h.csrf = keep[0], keep[1]
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("WHEP offer for %s: %d %s", path, rec.Code, rec.Body)
	}
	id := rec.Header().Get("Location")
	id = id[strings.LastIndexByte(id, '/')+1:]
	return "/" + path + "/whep/" + id
}

// Taking a user's access away closes their open WebRTC live views (MediaMTX checked them only at the offer): signing
// them out everywhere, disabling, deleting or demoting them, and the end of the UI session itself. Everyone else's
// views stay open, and the loop acts as soon as the change is made, not at its next round.
func TestLiveViewsEndWithAccess(t *testing.T) {
	h, rtc := watchHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.srv.RunAccess(ctx)
	}()

	adminSession := [2]string{h.cookie, h.csrf}
	adminView := h.watch(adminSession, "cam1")
	for i, c := range []struct {
		name, method, path string
		body               any
	}{
		{"signed out everywhere", "DELETE", "/sessions", nil},
		{"disabled", "PATCH", "", map[string]any{"disabled": true}},
		{"demoted to streamer", "PATCH", "", map[string]any{"role": "streamer"}},
		{"deleted", "DELETE", "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, session := h.join(fmt.Sprintf("viewer%d", i), "viewer")
			view := h.watch(session, "cam1")
			_, bystander := h.join(fmt.Sprintf("bystander%d", i), "viewer")
			otherView := h.watch(bystander, "cam1")
			if rec := h.do(c.method, "/api/v1/users/"+itoa(id)+c.path, c.body); rec.Code != http.StatusNoContent {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			deadline := time.Now().Add(accessEvery / 2) // the change itself wakes the loop
			for !rtc.wasDeleted(view) {
				if time.Now().After(deadline) {
					t.Fatal("the view stayed open")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if rtc.wasDeleted(otherView) || rtc.wasDeleted(adminView) {
				t.Error("someone else's view was closed")
			}
		})
	}
	cancel()
	<-done

	// A UI session that ends by itself (idle here) takes its views along on the next round.
	_, session := h.join("idler", "viewer")
	view := h.watch(session, "cam2")
	later := time.Now().Add(h.srv.d.Settings.SessionIdleTimeout + time.Hour)
	h.st.SetClock(func() time.Time { return later })
	if err := h.st.TouchSession(context.Background(), liveproxy.Viewer{Session: adminSession[0]}.Key(), later); err != nil {
		t.Fatal(err)
	}
	h.srv.checkAccess(context.Background(), time.Now())
	if !rtc.wasDeleted(view) || rtc.wasDeleted(adminView) {
		t.Errorf("the idle session's view closed: %v, the admin's: %v", rtc.wasDeleted(view), rtc.wasDeleted(adminView))
	}
}

// The WHEP proxy forgets sessions MediaMTX no longer lists, once they are past the grace period; listed ones, new
// ones, and all of them while MediaMTX does not answer, are kept.
func TestAccessForgetsEndedWHEPSessions(t *testing.T) {
	h, _ := watchHarness(t, nil)
	admin := [2]string{h.cookie, h.csrf}
	h.watch(admin, "cam1") // MediaMTX id ...0001
	h.watch(admin, "cam2") // ...0002
	ctx := context.Background()
	later := time.Now().Add(2 * whepGrace)
	h.srv.checkAccess(ctx, later) // the hub has not polled yet: nothing is known
	if n := len(h.srv.d.Watch.Sessions()); n != 2 {
		t.Fatalf("%d sessions kept before the hub knows anything", n)
	}
	h.mu.Lock()
	h.lists["/v3/webrtc/sessions/list"] = `[{"id":"9f8e7d6c-0000-4000-8000-000000000001","path":"cam1","state":"read"}]`
	h.mu.Unlock()
	h.hub.Poll(ctx)
	h.srv.checkAccess(ctx, time.Now()) // within the grace period
	if n := len(h.srv.d.Watch.Sessions()); n != 2 {
		t.Fatalf("%d sessions kept within the grace period", n)
	}
	h.srv.checkAccess(ctx, later)
	left := h.srv.d.Watch.Sessions()
	if len(left) != 1 || left[0].Path != "cam1" {
		t.Errorf("left %+v", left)
	}
}

// What an expired guest key opened is disconnected by the access loop, with exposure control off (the default): it
// used to happen only in the automatic exposure loop, which runs only with exposure control on.
func TestAccessEndsExpiredGuests(t *testing.T) {
	h, _ := watchHarness(t, map[string]string{"MTXUI_EXPOSURE_CONTROL": "off"})
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/guest", "public": false}), &st)
	var made NewGuestKey
	h.json(h.do("POST", "/api/v1/streams/"+itoa(st.ID)+"/guest-keys", map[string]any{"kind": "read", "label": "Ann", "hours": 1}), &made)
	h.mu.Lock()
	h.opened[made.Guest.ID] = []mtxauth.SessionRef{{Protocol: "rtsp", ID: "guest-session"}}
	h.mu.Unlock()
	kicked := func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return slices.Contains(h.kicks, "/v3/rtsp/sessions/kick/guest-session")
	}
	h.srv.checkAccess(context.Background(), time.Now())
	if kicked() {
		t.Fatal("a valid guest key's session was disconnected")
	}
	h.srv.checkAccess(context.Background(), time.Now().Add(2*time.Hour))
	if !kicked() {
		t.Errorf("kicks %v", h.kicks)
	}
}
