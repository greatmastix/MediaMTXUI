package mtxauth

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// SessionRef names a MediaMTX session or connection by protocol and id, as the authentication request carried them.
type SessionRef struct {
	Protocol string
	ID       string
}

// KickPath is the Control API operation that closes ref, or "" for a protocol without one.
func KickPath(ref SessionRef) string {
	base, ok := map[string]string{
		"rtsp": "/v3/rtsp/sessions/kick/", "rtsps": "/v3/rtsps/sessions/kick/",
		"rtmp": "/v3/rtmp/conns/kick/", "rtmps": "/v3/rtmps/conns/kick/",
		"srt": "/v3/srt/conns/kick/", "webrtc": "/v3/webrtc/sessions/kick/",
		"hls": "/v3/hls/sessions/kick/", "moq": "/v3/moq/sessions/kick/",
	}[ref.Protocol]
	if !ok || ref.ID == "" {
		return ""
	}
	return base + ref.ID
}

// subject is what let a session in: a credential, or a public stream that anyone may read.
type subject struct {
	cred int64  // the credential's id, or 0
	path string // the public stream's path, for an anonymous read
}

// opened remembers which sessions each credential, and each public stream's anonymous readers, opened, so that
// revoking the credential or making the stream private can close them: MediaMTX asks only when a session starts (HLS
// once per session), so taking access away alone would leave them running.
//
// It lives in memory. Sessions that started before the sidecar did are not in it: those MediaMTX lists under the
// credential's name are found that way (Live.ClientsOf), but MediaMTX lists no name for a session that authenticated
// with a bearer token (token credentials, and keys sent as WHIP's "name:secret" bearer), nor for an anonymous reader.
// After a sidecar restart (an upgrade, a restore), revoking a key or making a stream private therefore leaves those
// older sessions running until they disconnect; the stream page's Disconnect closes its publisher in any case.
type opened struct {
	mu     sync.Mutex
	by     map[subject]map[SessionRef]time.Time
	n      int // the credentials' entries
	public int // the anonymous readers' entries
}

// The memory is bounded. Sessions MediaMTX no longer lists are forgotten (prune), so a bound is reached only by that
// many live sessions, or by a flood of anonymous connections within the grace period. Beyond maxOpened a credential's
// oldest entry goes; beyond maxPublic an arbitrary anonymous reader's, at once: anyone can add those, and they must
// never push out what credentials opened.
const (
	maxOpened = 20000
	maxPublic = 20000
)

func (o *opened) add(s subject, ref SessionRef, at time.Time) {
	if ref.ID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.by == nil {
		o.by = map[subject]map[SessionRef]time.Time{}
	}
	m := o.by[s]
	if m == nil {
		m = map[SessionRef]time.Time{}
		o.by[s] = m
	}
	if _, seen := m[ref]; !seen {
		o.count(s, 1)
	}
	m[ref] = at
	switch {
	case s.path != "" && o.public > maxPublic:
		o.evictPublic()
	case s.path == "" && o.n > maxOpened:
		o.evictOldest()
	}
}

func (o *opened) count(s subject, d int) {
	if s.path != "" {
		o.public += d
	} else {
		o.n += d
	}
}

func (o *opened) evictOldest() {
	var oldSub subject
	var oldRef SessionRef
	var oldAt time.Time
	for s, m := range o.by {
		if s.path != "" {
			continue
		}
		for r, at := range m {
			if oldAt.IsZero() || at.Before(oldAt) {
				oldSub, oldRef, oldAt = s, r, at
			}
		}
	}
	o.removeLocked(oldSub, oldRef)
}

func (o *opened) evictPublic() {
	for s, m := range o.by { // map order is random
		if s.path == "" {
			continue
		}
		for r := range m {
			o.removeLocked(s, r)
			return
		}
	}
}

func (o *opened) removeLocked(s subject, ref SessionRef) {
	m := o.by[s]
	if _, ok := m[ref]; !ok {
		return
	}
	delete(m, ref)
	o.count(s, -1)
	if len(m) == 0 {
		delete(o.by, s)
	}
}

// take returns and forgets the sessions of a subject.
func (o *opened) take(s subject) []SessionRef {
	o.mu.Lock()
	defer o.mu.Unlock()
	m := o.by[s]
	delete(o.by, s)
	o.count(s, -len(m))
	out := make([]SessionRef, 0, len(m))
	for r := range m {
		out = append(out, r)
	}
	return out
}

// publicPaths lists the public streams with anonymous readers on record.
func (o *opened) publicPaths() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for s := range o.by {
		if s.path != "" {
			out = append(out, s.path)
		}
	}
	return out
}

// prune forgets the sessions recorded before cutoff that gone reports ended. gone is called without the lock held:
// it may be slow, and authentication must not wait for it.
func (o *opened) prune(cutoff time.Time, gone func(SessionRef) bool) int {
	type entry struct {
		s   subject
		ref SessionRef
		at  time.Time
	}
	var old []entry
	o.mu.Lock()
	for s, m := range o.by {
		for r, at := range m {
			if at.Before(cutoff) {
				old = append(old, entry{s, r, at})
			}
		}
	}
	o.mu.Unlock()
	var ended []entry
	for _, e := range old {
		if gone(e.ref) {
			ended = append(ended, e)
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, e := range ended {
		if at, ok := o.by[e.s][e.ref]; ok && at.Equal(e.at) { // not authenticated again meanwhile
			o.removeLocked(e.s, e.ref)
			n++
		}
	}
	return n
}

// Kicker closes MediaMTX sessions through its Control API, with the sidecar's credentials.
type Kicker interface {
	Post(ctx context.Context, path string) (int, error)
}

// Listing says whether MediaMTX listed a session or connection at the live hub's last poll (live.Hub). fresh is false
// when that answer means nothing (MediaMTX did not answer the poll).
type Listing interface {
	Listed(protocol, id string) (listed, fresh bool)
}

// How the record is kept honest.
const (
	withdrawEvery = 2 * time.Second // how soon the anonymous readers of a stream made private are disconnected
	pruneEvery    = time.Minute
	// sessionGrace is how long after authenticating a session may go unlisted: the hub polls every 1 to 5 s. An RTSP
	// reader's first authentication names its connection, which is never in the sessions list; that entry goes too.
	sessionGrace = time.Minute
)

// RunSessions keeps the record of opened sessions honest until ctx ends. Every few seconds it disconnects the
// anonymous readers of streams that are no longer public, as revoking a credential disconnects what it opened; and it
// forgets the sessions MediaMTX no longer lists, so that the record holds live sessions, not the oldest ones.
func (h *Handler) RunSessions(ctx context.Context, k Kicker, l Listing) {
	t := time.NewTicker(withdrawEvery)
	defer t.Stop()
	pruned := h.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.withdraw(ctx, k)
		if now := h.now(); now.Sub(pruned) >= pruneEvery {
			h.opened.prune(now.Add(-sessionGrace), func(ref SessionRef) bool {
				listed, fresh := l.Listed(ref.Protocol, ref.ID)
				return fresh && !listed
			})
			pruned = now
		}
	}
}

// withdraw disconnects the anonymous readers of streams that are no longer public, and returns how many it closed.
func (h *Handler) withdraw(ctx context.Context, k Kicker) int {
	if h.Public == nil {
		return 0
	}
	total := 0
	for _, path := range h.opened.publicPaths() {
		if h.Public(path) {
			continue
		}
		n := 0
		for _, ref := range h.opened.take(subject{path: path}) {
			kick := KickPath(ref)
			if kick == "" {
				continue
			}
			switch code, err := k.Post(ctx, kick); {
			case err != nil:
				h.log.Warn("kick failed", "protocol", ref.Protocol, "id", ref.ID, "err", err)
			case code == http.StatusOK:
				n++
			}
		}
		if n > 0 {
			h.log.Info("stream no longer public: anonymous readers disconnected", "path", path, "sessions", n)
		}
		total += n
	}
	return total
}
