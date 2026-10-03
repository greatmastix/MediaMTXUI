package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/liveproxy"
	"mtxui/internal/pathname"
	"mtxui/internal/store"
)

// The live view's proxies (see internal/liveproxy): any signed-in user may watch.

func (s *Server) viewer(r *http.Request) liveproxy.Viewer {
	cur, _ := current(r.Context())
	return liveproxy.Viewer{Session: cur.token, User: cur.user.Username, Client: clientip.From(r.Context()).IP}
}

func (s *Server) watchHLS(w http.ResponseWriter, r *http.Request) {
	path, file, err := liveproxy.SplitHLS(param(r, "*"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid path or file.")
		return
	}
	if !s.mayWatch(r, path) {
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return
	}
	s.d.Watch.HLS(w, r, s.viewer(r), path, file)
}

func (s *Server) watchWHEP(w http.ResponseWriter, r *http.Request) {
	path := param(r, "*")
	if err := pathname.Valid(path); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid path name.")
		return
	}
	if !s.mayWatch(r, path) {
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return
	}
	s.d.Watch.WHEPOffer(w, r, s.viewer(r), path, "/api/v1/live/whep-session/")
}

func (s *Server) watchWHEPSession(w http.ResponseWriter, r *http.Request) {
	s.d.Watch.WHEPSession(w, r, s.viewer(r), param(r, "id"))
}

// mayWatchAs reports whether user u may watch path: viewers and up watch anything, streamers their own.
func (s *Server) mayWatchAs(ctx context.Context, u store.User, path string) bool {
	if auth.Role(u.Role).AtLeast(auth.RoleViewer) {
		return true
	}
	return s.loadOwners(ctx) == nil && s.ownsPath(u.ID, path)
}

// The access loop. MediaMTX checks access only when a session starts, so what the sidecar takes away later, it must
// close itself. Every accessEvery, and at once when sessions or users change (sign-out, signing out everywhere, a
// password change, disabling, deleting, a new role), it ends the live view's WebRTC sessions whose UI session has
// ended or whose user may no longer watch the path (an owner change is noticed on the next round); HLS needs nothing,
// as the proxy checks every request. It disconnects what expired guest keys opened, and lets the WHEP proxy forget
// sessions MediaMTX has ended. (mtxauth.Handler.RunSessions does the same for public streams made private.)
const (
	accessEvery = 10 * time.Second
	// whepGrace is how long a new WHEP session may go unlisted by MediaMTX before the proxy forgets it: the hub polls
	// every 1 to 5 s, and MediaMTX drops a session whose peer never connects after its handshake timeout (10 s).
	whepGrace = time.Minute
)

// RunAccess runs the access loop until ctx ends.
func (s *Server) RunAccess(ctx context.Context) {
	t := time.NewTicker(accessEvery)
	defer t.Stop()
	for {
		nudged := s.sessionNudge.wait() // before the round: a change during it brings the next one forward
		s.checkAccess(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-nudged:
		}
	}
}

func (s *Server) checkAccess(ctx context.Context, now time.Time) {
	s.endExpiredGuests(ctx, now)
	type viewer struct {
		user  store.User
		alive bool
		err   error
	}
	var ended, gone []string
	viewers := map[string]viewer{} // by UI session key
	for _, ws := range s.d.Watch.Sessions() {
		if ws.MediaMTX != "" && now.Sub(ws.Started) > whepGrace {
			if listed, fresh := s.d.Live.Listed("webrtc", ws.MediaMTX); fresh && !listed {
				gone = append(gone, ws.ID)
				continue
			}
		}
		if ws.Viewer == "" {
			continue // an external client's: revoking its credential closes it (credentialRevoke, retireKey)
		}
		v, seen := viewers[ws.Viewer]
		if !seen {
			v.user, v.alive, v.err = s.viewerOf(ctx, ws.Viewer)
			viewers[ws.Viewer] = v
		}
		if v.err == nil && (!v.alive || !s.mayWatchAs(ctx, v.user, ws.Path)) {
			ended = append(ended, ws.ID)
		}
	}
	s.d.Watch.Forget(gone)
	closed := 0
	for _, id := range ended {
		if err := s.d.Watch.End(ctx, id); err != nil {
			s.d.Log.Warn("closing a live view", "err", err)
			continue
		}
		closed++
	}
	if closed > 0 {
		s.d.Log.Info("live views closed: their session ended or they may no longer watch", "sessions", closed)
	}
}

// viewerOf returns the user of the UI session with key (its IDHash), and whether that session is still alive by the
// rules of auth.Sessions.Lookup: not deleted, expired or idle, and its user neither deleted nor disabled.
func (s *Server) viewerOf(ctx context.Context, key string) (store.User, bool, error) {
	sess, err := s.d.Store.SessionByHash(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, false, nil
	}
	if err != nil {
		return store.User{}, false, err
	}
	now := s.d.Store.Now()
	if !now.Before(sess.ExpiresAt) || !now.Before(sess.LastSeenAt.Add(s.d.Settings.SessionIdleTimeout)) {
		return store.User{}, false, nil
	}
	u, err := s.d.Store.UserByID(ctx, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, false, nil
	}
	if err != nil {
		return store.User{}, false, err
	}
	return u, !u.Disabled, nil
}

// External WHIP and WHEP for clients outside the UI (OBS, players, other servers): /whip/<path> publishes,
// /whep/<path> reads, /rtc-session/<id> trickles and ends. They authenticate with a stream credential in their own
// Authorization header, which MediaMTX checks through the sidecar like every other protocol; no cookie counts.

func (s *Server) externalOffer(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := param(r, "*")
		if err := pathname.Valid(path); err != nil {
			http.Error(w, "invalid path name", http.StatusBadRequest)
			return
		}
		s.d.Watch.ExternalOffer(w, r, kind, path, clientip.From(r.Context()).IP, "/rtc-session/")
	}
}

func (s *Server) externalSession(w http.ResponseWriter, r *http.Request) {
	s.d.Watch.ExternalSession(w, r, clientip.From(r.Context()).IP, param(r, "id"))
}

// externalRTC reports whether a request is for the external WHIP/WHEP endpoints, which the CSRF check leaves alone:
// they carry no cookie authority, and a cross-site page cannot send them application/sdp without a CORS preflight,
// which the sidecar never grants.
func externalRTC(path string) bool {
	return strings.HasPrefix(path, "/whip/") || strings.HasPrefix(path, "/whep/") || strings.HasPrefix(path, "/rtc-session/")
}

// Saved multi-view layouts, per user. The shape is the UI's, versioned by it; the sidecar checks the size, the
// version, and that every tile names a valid path (or is empty).

// LayoutData is a layout as the UI stores it.
type LayoutData struct {
	Version int       `json:"version"`
	Columns int       `json:"columns"`
	Tiles   []*string `json:"tiles"` // path names; null for an empty tile
}

// LayoutInfo is one saved layout.
type LayoutInfo struct {
	Name      string     `json:"name"`
	Layout    LayoutData `json:"layout"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// maxTiles is the most players a layout holds: every tile is a live stream in the browser.
const maxTiles = 9

var layoutName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)

func (s *Server) layoutsList(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	list, err := s.d.Store.Layouts(r.Context(), cur.user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the layouts.")
		return
	}
	out := make([]LayoutInfo, 0, len(list))
	for _, l := range list {
		if l.Name == currentLayout {
			continue
		}
		var d LayoutData
		if json.Unmarshal([]byte(l.Data), &d) != nil {
			continue
		}
		out = append(out, LayoutInfo{Name: l.Name, Layout: d, UpdatedAt: l.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) layoutSave(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	name := param(r, "name")
	if !layoutName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid", "Layout names use letters, digits, spaces and _ . - (up to 64).")
		return
	}
	s.saveLayout(w, r, cur.user.ID, name)
}

// currentLayout is the stored name of the layout on screen, which the Watch page saves as it changes. Named layouts
// cannot start with a dot, so it never collides with one, and the list leaves it out.
const currentLayout = ".current"

func (s *Server) watchCurrentGet(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	list, err := s.d.Store.Layouts(r.Context(), cur.user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the layout.")
		return
	}
	for _, l := range list {
		var d LayoutData
		if l.Name == currentLayout && json.Unmarshal([]byte(l.Data), &d) == nil {
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	writeJSON(w, http.StatusOK, nil)
}

func (s *Server) watchCurrentPut(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	s.saveLayout(w, r, cur.user.ID, currentLayout)
}

func (s *Server) saveLayout(w http.ResponseWriter, r *http.Request, uid int64, name string) {
	var d LayoutData
	if !decodeJSON(w, r, &d) {
		return
	}
	if d.Version != 1 || d.Columns < 1 || d.Columns > 3 || len(d.Tiles) > maxTiles {
		writeError(w, http.StatusBadRequest, "invalid", "A layout has version 1, 1 to 3 columns and at most 9 tiles.")
		return
	}
	for _, t := range d.Tiles {
		if t != nil && pathname.Valid(*t) != nil {
			writeError(w, http.StatusBadRequest, "invalid", "A tile names an invalid path.")
			return
		}
	}
	data, _ := json.Marshal(d)
	switch err := s.d.Store.SaveLayout(r.Context(), uid, name, string(data)); {
	case errors.Is(err, store.ErrTooMany):
		writeError(w, http.StatusConflict, "too_many", "You have the most layouts allowed; delete one first.")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", "The layout could not be saved.")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) layoutDelete(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	switch err := s.d.Store.DeleteLayout(r.Context(), cur.user.ID, param(r, "name")); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "No such layout.")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", "The layout could not be deleted.")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
