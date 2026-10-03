package app

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mtxui/internal/auth"
	"mtxui/internal/live"
)

// Event stream limits. Variables, so tests can shorten them.
var (
	heartbeatEvery  = 15 * time.Second
	recheckEvery    = 10 * time.Second // how soon a stream notices that its session ended
	writeTimeout    = 10 * time.Second
	streamsPerUser  = 8
	retryAfterDrops = 1000 // ms, the browser's reconnection delay
)

// nudge wakes every open event stream to recheck its session now, rather than at its next recheck tick: after a
// sign-out or a deleted user, their streams end at once.
type nudge struct {
	mu sync.Mutex
	ch chan struct{}
}

func (n *nudge) wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch == nil {
		n.ch = make(chan struct{})
	}
	return n.ch
}

func (n *nudge) fire() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch != nil {
		close(n.ch)
	}
	n.ch = make(chan struct{})
}

// streamCount limits concurrent event streams per user.
type streamCount struct {
	mu sync.Mutex
	n  map[int64]int
}

func (c *streamCount) acquire(user int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[int64]int{}
	}
	if c.n[user] >= streamsPerUser {
		return false
	}
	c.n[user]++
	return true
}

func (c *streamCount) release(user int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[user]--; c.n[user] <= 0 {
		delete(c.n, user)
	}
}

// events is the live event stream (SSE): a snapshot or the missed events first, then updates as the hub publishes
// them, a comment every 15 s so proxies keep the connection open, and a "session" event when the session ends
// (sign-out elsewhere, expiry, a disabled user or a changed role), after which the stream closes.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	// Taken before anything is sent, held across passes, and taken anew only once it fired: a sign-out right after
	// the snapshot, or while this stream is busy sending, still counts rather than waiting for the next recheck.
	nudged := s.sessionNudge.wait()
	uid := cur.user.ID
	if !s.streams.acquire(uid) {
		writeError(w, http.StatusTooManyRequests, "too_many_streams", "Too many open live views for this user. Close some tabs.")
		return
	}
	defer s.streams.release(uid)

	role := auth.Role(cur.user.Role)
	var sub *live.Subscription
	var err error
	if role.AtLeast(auth.RoleViewer) {
		sub, err = s.d.Live.Subscribe(role, lastEventID(r))
	} else { // a streamer: its own streams only, decided per event, so ownership changes apply at once
		if err := s.loadOwners(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "Cannot read the streams.")
			return
		}
		sub, err = s.d.Live.SubscribeScoped(func(path string) bool { return s.ownsPath(uid, path) })
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "The server is shutting down.")
		return
	}
	defer s.d.Live.Unsubscribe(sub)

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(chunks ...string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		for _, c := range chunks {
			if _, err := w.Write([]byte(c)); err != nil {
				return false
			}
		}
		return rc.Flush() == nil
	}
	sendEvent := func(ev live.Event) bool {
		return send("id: ", ev.ID, "\nevent: ", ev.Type, "\ndata: ", string(ev.Data), "\n\n")
	}

	if !send("retry: ", strconv.Itoa(retryAfterDrops), "\n\n") {
		return
	}
	for _, ev := range sub.Initial {
		if !sendEvent(ev) {
			return
		}
	}
	heartbeat := time.NewTicker(heartbeatEvery)
	defer heartbeat.Stop()
	recheck := time.NewTicker(recheckEvery)
	defer recheck.Stop()
	ended := func() bool {
		_, user, err := s.d.Sessions.Lookup(r.Context(), cur.token)
		return errors.Is(err, auth.ErrNoSession) || (err == nil && (user.Role != cur.user.Role || user.ID != uid))
	}
	for {
		var check bool
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return // the hub dropped a slow stream or is shutting down: the browser reconnects and resumes
			}
			if !sendEvent(ev) {
				return
			}
		case <-heartbeat.C:
			if !send(": keep-alive\n\n") {
				return
			}
		case <-recheck.C:
			check = true
		case <-nudged:
			nudged, check = s.sessionNudge.wait(), true
		}
		if check && ended() {
			send("event: session\ndata: {\"state\":\"ended\"}\n\n")
			return
		}
	}
}

// lastEventID reads the browser's resume point; EventSource sends it as a header when it reconnects.
func lastEventID(r *http.Request) string {
	id := r.Header.Get("Last-Event-ID")
	if len(id) > 64 || strings.ContainsAny(id, "\r\n") {
		return ""
	}
	return id
}

// History is the payload of the history endpoint.
type History struct {
	IntervalSeconds int           `json:"intervalSeconds"`
	Samples         []live.Sample `json:"samples"`
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	span, bucket, ok := historyRange(w, r)
	if !ok {
		return
	}
	if bucket == 0 {
		writeJSON(w, http.StatusOK, History{IntervalSeconds: int(live.SampleInterval / time.Second), Samples: s.d.Live.History()})
		return
	}
	now := time.Now()
	points, err := s.d.Store.History(r.Context(), now.Add(-span), now, bucket)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the history.")
		return
	}
	out := History{IntervalSeconds: int(bucket / time.Second), Samples: make([]live.Sample, 0, len(points))}
	for _, p := range points {
		out.Samples = append(out.Samples, live.Sample{
			T: p.T.UnixMilli(), InBps: &p.InBps, OutBps: &p.OutBps, Paths: p.Paths, Online: p.Online, Readers: p.Readers, Clients: p.Clients,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
