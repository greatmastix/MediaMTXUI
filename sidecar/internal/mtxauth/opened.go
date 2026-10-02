package mtxauth

import (
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

// opened remembers which sessions each credential authenticated, so that revoking a credential can close them:
// MediaMTX asks only when a session starts (HLS once per session), so revocation alone would leave them running.
type opened struct {
	mu     sync.Mutex
	byCred map[int64]map[SessionRef]time.Time
	n      int
}

// maxOpened bounds the memory: beyond it, the oldest entries go (a kick of a session that has ended anyway is harmless;
// a forgotten live one is still found by the user name in MediaMTX's lists).
const maxOpened = 20000

func (o *opened) add(cred int64, ref SessionRef, at time.Time) {
	if ref.ID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.byCred == nil {
		o.byCred = map[int64]map[SessionRef]time.Time{}
	}
	m := o.byCred[cred]
	if m == nil {
		m = map[SessionRef]time.Time{}
		o.byCred[cred] = m
	}
	if _, seen := m[ref]; !seen {
		o.n++
	}
	m[ref] = at
	if o.n > maxOpened {
		o.evictOldest()
	}
}

func (o *opened) evictOldest() {
	var oldCred int64
	var oldRef SessionRef
	var oldAt time.Time
	for c, m := range o.byCred {
		for r, at := range m {
			if oldAt.IsZero() || at.Before(oldAt) {
				oldCred, oldRef, oldAt = c, r, at
			}
		}
	}
	delete(o.byCred[oldCred], oldRef)
	o.n--
}

// take returns and forgets the sessions of a credential.
func (o *opened) take(cred int64) []SessionRef {
	o.mu.Lock()
	defer o.mu.Unlock()
	m := o.byCred[cred]
	delete(o.byCred, cred)
	o.n -= len(m)
	out := make([]SessionRef, 0, len(m))
	for r := range m {
		out = append(out, r)
	}
	return out
}
