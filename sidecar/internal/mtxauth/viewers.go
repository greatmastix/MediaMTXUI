package mtxauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ViewerPrefix marks the sidecar's viewer tickets: short-lived credentials it mints for its own HLS and WHEP proxies,
// so a signed-in UI user can watch a path. They never leave the sidecar (the browser authenticates to the proxy with
// its session), allow only reading one path, and only for the client address the proxy forwards to MediaMTX.
const ViewerPrefix = "mtxui-viewer-"

// ticketTTL covers starting a session: MediaMTX authenticates HLS once per session and WHEP once per offer.
const ticketTTL = 60 * time.Second

type ticket struct {
	secret  string
	path    string
	client  netip.Addr
	uiUser  string
	expires time.Time
}

// Viewers issues and checks viewer tickets.
type Viewers struct {
	mu  sync.Mutex
	m   map[string]ticket
	now func() time.Time
}

// NewViewers returns an empty ticket store.
func NewViewers() *Viewers { return &Viewers{m: map[string]ticket{}, now: time.Now} }

// Issue mints a ticket for uiUser to read path from client, and returns it as MediaMTX credentials.
func (v *Viewers) Issue(path, uiUser string, client netip.Addr) (user, secret string) {
	id, sec := make([]byte, 8), make([]byte, 16)
	_, _ = rand.Read(id)
	_, _ = rand.Read(sec)
	user, secret = ViewerPrefix+hex.EncodeToString(id), hex.EncodeToString(sec)
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	for k, t := range v.m { // tickets are few and short-lived: prune on the way
		if now.After(t.expires) {
			delete(v.m, k)
		}
	}
	v.m[user] = ticket{secret: secret, path: path, client: client.Unmap(), uiUser: uiUser, expires: now.Add(ticketTTL)}
	return user, secret
}

// check reports why a ticket does not allow the request, or "" if it does, and the UI user it was issued to.
func (v *Viewers) check(user, secret, action, path string, ip netip.Addr) (reason, uiUser string) {
	v.mu.Lock()
	t, ok := v.m[user]
	v.mu.Unlock()
	switch {
	case !ok || subtle.ConstantTimeCompare([]byte(secret), []byte(t.secret)) != 1:
		return "unknown viewer ticket", ""
	case v.now().After(t.expires):
		return "expired viewer ticket", t.uiUser
	case action != "read":
		return "viewer tickets only read", t.uiUser
	case path != t.path:
		return "viewer ticket for another path", t.uiUser
	case ip.Unmap() != t.client:
		return "viewer ticket from another address", t.uiUser
	}
	return "", t.uiUser
}

func isViewer(user string) bool { return strings.HasPrefix(user, ViewerPrefix) }
