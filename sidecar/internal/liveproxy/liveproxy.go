// Package liveproxy lets signed-in browsers watch paths through the sidecar: MediaMTX's HLS server and WHEP
// signalling are never published, so the sidecar forwards to them. The upstream addresses are fixed (MediaMTX's
// service name and its HLS and WebRTC ports, which the config rules lock), path and file names are validated, and
// only the query parameters low-latency HLS uses pass through.
//
// Authentication towards MediaMTX uses viewer tickets (mtxauth.Viewers): short-lived, read-only, one path, one client
// address. The browser never sees them, nor MediaMTX's own HLS session cookie, which the proxy keeps per browser
// session and path. The real client address goes to MediaMTX in X-Forwarded-For (its HLS and WebRTC servers trust the
// stack network), so its sessions and logs show viewers, not the sidecar.
package liveproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"mtxui/internal/auth/clientip"
	"mtxui/internal/pathname"
)

// Tickets mints viewer credentials (mtxauth.Viewers).
type Tickets interface {
	Issue(path, uiUser string, client netip.Addr) (user, secret string)
}

// Viewer is who is watching: the UI session (a secret: only its hash is kept), the UI user, the client address.
type Viewer struct {
	Session string
	User    string
	Client  netip.Addr
}

// Key is the hash of the UI session, as the session store keys it (store.Session.IDHash).
func (v Viewer) Key() string {
	sum := sha256.Sum256([]byte(v.Session))
	return hex.EncodeToString(sum[:])
}

// Proxy serves the HLS and WHEP proxies.
type Proxy struct {
	hls, webrtc string // e.g. http://mediamtx:8888
	tickets     Tickets
	client      *http.Client
	now         func() time.Time

	jarMu    sync.Mutex          // every HLS request takes it: never held for long
	cookies  map[string]jarEntry // viewer key + path -> MediaMTX's hlsSession cookie
	jarAdded int                 // cookies set since the jar was last swept

	mu       sync.Mutex
	sessions map[string]whep // our WHEP session id -> the upstream session
	buckets  map[string]int  // sessions per bucket (whep.bucket)
	external int             // external clients' sessions
}

type jarEntry struct {
	cookie string
	used   time.Time
}

type whep struct {
	owner    string // viewer key, or externalOwner
	upstream string // MediaMTX's session URL, absolute
	mtxID    string // MediaMTX's session id (its API lists and kicks sessions by it), or "" if it sent none
	path     string
	bucket   string // what the per-client bound counts: the UI session, or an external client's address (RateKey)
	created  time.Time
}

// Limits. The proxy keeps a session until its owner ends it or MediaMTX no longer lists it (Forget, from the
// sidecar's access loop). Anonymous offers on a public stream cost an entry each, so entries are bounded per client
// and in all (addLocked, roomFor). An external session whose entry goes keeps its media flowing, but can no longer
// trickle or end through the proxy (MediaMTX closes it when the peer goes away).
const (
	jarIdle      = 2 * time.Minute // an HLS player reloads every few seconds; this long without a request, it is gone
	jarSweep     = 1024            // the jar is swept for idle cookies after this many new ones, or as many as it holds
	maxSessions  = 10000
	maxPerClient = 64 // per UI session or external client address; a page has at most nine players
	maxOffer     = 64 << 10
	maxPatch     = 16 << 10
	upstreamWait = 20 * time.Second // low-latency HLS holds blocking playlist reloads for a few seconds
)

// New returns a proxy for MediaMTX's HLS server and WebRTC signalling at the given base URLs.
func New(hls, webrtc string, tickets Tickets) *Proxy {
	return &Proxy{
		hls: strings.TrimSuffix(hls, "/"), webrtc: strings.TrimSuffix(webrtc, "/"), tickets: tickets,
		client: &http.Client{Timeout: upstreamWait, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		cookies: map[string]jarEntry{}, sessions: map[string]whep{}, buckets: map[string]int{}, now: time.Now,
	}
}

var (
	hlsFile = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}\.(m3u8|mp4|m4s|ts|aac|vtt)$`)
	// Low-latency HLS parameters, and MediaMTX's own HLS session id: when it decides a client does not keep
	// cookies, it puts the session in the playlist URLs instead (IP-bound; the accepted exception to "no tokens in
	// URLs", CONTRIBUTING.md rule 6).
	hlsQuery   = map[string]bool{"_HLS_msn": true, "_HLS_part": true, "_HLS_skip": true, "session": true}
	uuidString = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	idChars    = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
)

// SplitHLS separates "live/cam1/index.m3u8" into the path and the file, validating both.
func SplitHLS(rest string) (path, file string, err error) {
	i := strings.LastIndexByte(rest, '/')
	if i <= 0 {
		return "", "", errors.New("no path")
	}
	path, file = rest[:i], rest[i+1:]
	if !hlsFile.MatchString(file) {
		return "", "", errors.New("not an HLS file")
	}
	return path, file, pathname.Valid(path)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// HLS forwards one HLS request (a playlist or a segment) for path and file.
func (p *Proxy) HLS(w http.ResponseWriter, r *http.Request, v Viewer, path, file string) {
	q := url.Values{}
	for k, vs := range r.URL.Query() {
		if !hlsQuery[k] || len(vs) != 1 || (k == "session" && !uuidString.MatchString(vs[0])) ||
			(k != "session" && len(vs[0]) > 16) {
			http.Error(w, "unexpected query parameter", http.StatusBadRequest)
			return
		}
		q.Set(k, vs[0])
	}
	target := p.hls + "/" + escapePath(path) + "/" + file
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	jarKey := v.Key() + "\x00" + path
	var resp *http.Response
	// MediaMTX checks for cookie support with a redirect to ?cookieCheck=1 and then sets its session cookie; the
	// proxy plays the browser's part (twice at most) and keeps the cookie.
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Only the multivariant playlist starts a MediaMTX session, which authenticates once (v1.21.1); media
		// playlists and segments ride on its cookie, and MediaMTX never looks at credentials for them. A ticket for
		// every request would mint one per segment, for anyone watching a public stream.
		if file == "index.m3u8" {
			user, secret := p.tickets.Issue(path, v.User, v.Client)
			req.SetBasicAuth(user, secret)
		}
		req.Header.Set("X-Forwarded-For", v.Client.String())
		if c := p.cookie(jarKey); c != "" {
			// Request cookies to MediaMTX: Secure, HttpOnly and SameSite are response-cookie attributes and mean nothing here.
			req.AddCookie(&http.Cookie{Name: "hlsSession", Value: c})    //nolint:gosec // see above
			req.AddCookie(&http.Cookie{Name: "cookieCheck", Value: "1"}) //nolint:gosec // see above
		}
		resp, err = p.client.Do(req)
		if err != nil {
			http.Error(w, "MediaMTX's HLS server does not answer", http.StatusBadGateway)
			return
		}
		for _, c := range resp.Cookies() {
			if c.Name == "hlsSession" {
				p.setCookie(jarKey, c.Value)
			}
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusFound || hop >= 2 || !strings.Contains(loc, "cookieCheck=1") {
			break
		}
		_ = resp.Body.Close()
		target = p.hls + "/" + escapePath(path) + "/" + file + "?" + url.Values{"cookieCheck": {"1"}}.Encode()
		if len(q) > 0 {
			target += "&" + q.Encode()
		}
	}
	defer resp.Body.Close()
	h := w.Header()
	for _, k := range []string{"Content-Type", "Content-Length"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	h.Set("Cache-Control", "no-store")
	if resp.StatusCode == http.StatusUnauthorized {
		w.WriteHeader(http.StatusForbidden) // the ticket was refused: the sidecar's problem, not the browser's session
	} else {
		w.WriteHeader(resp.StatusCode)
	}
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) cookie(key string) string {
	p.jarMu.Lock()
	defer p.jarMu.Unlock()
	e, ok := p.cookies[key]
	if !ok || p.now().Sub(e.used) > jarIdle {
		delete(p.cookies, key)
		return ""
	}
	e.used = p.now()
	p.cookies[key] = e
	return e.cookie
}

func (p *Proxy) setCookie(key, value string) {
	p.jarMu.Lock()
	defer p.jarMu.Unlock()
	now := p.now()
	// Idle cookies are swept once as many new ones have been set as the jar holds: the cookies before a sweep pay for
	// it, rather than every new HLS session scanning the whole jar.
	if p.jarAdded++; p.jarAdded >= max(jarSweep, len(p.cookies)) {
		for k, e := range p.cookies {
			if now.Sub(e.used) > jarIdle {
				delete(p.cookies, k)
			}
		}
		p.jarAdded = 0
	}
	p.cookies[key] = jarEntry{cookie: value, used: now}
}

// WHEPOffer starts a WebRTC session for a signed-in viewer: the browser's SDP offer goes to MediaMTX with a viewer
// ticket, the answer comes back with a session URL of the proxy's own (for trickle ICE and to end the session).
func (p *Proxy) WHEPOffer(w http.ResponseWriter, r *http.Request, v Viewer, path, sessionBase string) {
	p.offer(w, r, "whep", path, v.Key(), v.Client, sessionBase, func(req *http.Request) {
		user, secret := p.tickets.Issue(path, v.User, v.Client)
		req.SetBasicAuth(user, secret)
	})
}

// externalOwner marks sessions of external clients: their session URL is the only capability, as with MediaMTX itself.
const externalOwner = "external"

// ExternalOffer starts a WHIP (publish) or WHEP (read) session for a client outside the UI, such as OBS: its own
// Authorization header (a stream credential) goes to MediaMTX unchanged, which asks the sidecar's authentication
// endpoint as for any other protocol. No cookie is read or forwarded.
func (p *Proxy) ExternalOffer(w http.ResponseWriter, r *http.Request, kind, path string, client netip.Addr, sessionBase string) {
	auth := r.Header.Get("Authorization")
	p.offer(w, r, kind, path, externalOwner, client, sessionBase, func(req *http.Request) {
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
	})
}

func (p *Proxy) offer(w http.ResponseWriter, r *http.Request, kind, path, owner string, client netip.Addr, sessionBase string,
	authorize func(*http.Request),
) {
	if err := pathname.Valid(path); err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/sdp") {
		http.Error(w, "send an SDP offer", http.StatusUnsupportedMediaType)
		return
	}
	if owner != externalOwner && !p.roomFor(owner) {
		http.Error(w, "too many live views at once", http.StatusTooManyRequests)
		return
	}
	offer, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxOffer))
	if err != nil {
		http.Error(w, "offer too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	target := p.webrtc + "/" + escapePath(path) + "/" + kind
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(offer))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	authorize(req)
	req.Header.Set("Content-Type", "application/sdp")
	req.Header.Set("X-Forwarded-For", client.String())
	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "MediaMTX's WebRTC server does not answer", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxOffer))
	if resp.StatusCode != http.StatusCreated {
		code := resp.StatusCode
		switch {
		case code == http.StatusUnauthorized && owner == externalOwner:
			// The client's own credential was refused: it needs the 401 (and MediaMTX's challenge) to know.
			if c := resp.Header.Get("WWW-Authenticate"); c != "" {
				w.Header().Set("WWW-Authenticate", c)
			}
		case code == http.StatusUnauthorized:
			code = http.StatusForbidden // a refused viewer ticket is the sidecar's problem, not the browser's session
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(code)
		_, _ = w.Write(answer)
		return
	}
	loc, err := resp.Location()
	if err != nil {
		http.Error(w, "MediaMTX sent no session URL", http.StatusBadGateway)
		return
	}
	id := loc.Path[strings.LastIndexByte(loc.Path, '/')+1:]
	if !strings.HasPrefix(loc.String(), p.webrtc+"/") || !idChars.MatchString(id) {
		http.Error(w, "MediaMTX sent an unexpected session URL", http.StatusBadGateway)
		return
	}
	s := whep{owner: owner, upstream: loc.String(), path: path, bucket: owner, created: p.now()}
	if mtxID := resp.Header.Get("ID"); uuidString.MatchString(mtxID) {
		s.mtxID = mtxID
	}
	if owner == externalOwner {
		s.bucket = clientip.RateKey(client)
	}
	p.mu.Lock()
	p.addLocked(id, s)
	p.mu.Unlock()

	h := w.Header()
	h.Set("Content-Type", "application/sdp")
	h.Set("Location", sessionBase+id)
	h.Set("Cache-Control", "no-store")
	for _, k := range []string{"ETag", "Accept-Patch"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	for _, l := range resp.Header.Values("Link") { // ICE servers
		h.Add("Link", l)
	}
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(answer)
}

// WHEPSession forwards trickle ICE (PATCH) and the end of a session (DELETE) for the viewer who started it.
func (p *Proxy) WHEPSession(w http.ResponseWriter, r *http.Request, v Viewer, id string) {
	p.session(w, r, v.Key(), v.Client, id)
}

// ExternalSession does the same for an external client's session, which its id alone identifies.
func (p *Proxy) ExternalSession(w http.ResponseWriter, r *http.Request, client netip.Addr, id string) {
	p.session(w, r, externalOwner, client, id)
}

func (p *Proxy) session(w http.ResponseWriter, r *http.Request, owner string, client netip.Addr, id string) {
	p.mu.Lock()
	s, ok := p.sessions[id]
	if ok && s.owner == owner && r.Method == http.MethodDelete {
		p.removeLocked(id)
	}
	p.mu.Unlock()
	if !ok || s.owner != owner {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	var body io.Reader
	if r.Method == http.MethodPatch {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPatch))
		if err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, s.upstream, body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	headers := []string{"Content-Type", "If-Match"}
	if owner == externalOwner {
		headers = append(headers, "Authorization") // WHIP/WHEP clients repeat their credential on session requests
	}
	for _, k := range headers {
		if v := r.Header.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("X-Forwarded-For", client.String())
	resp, err := p.client.Do(req) //nolint:gosec // upstream is MediaMTX's own Location, checked in offer to be under p.webrtc
	if err != nil {
		http.Error(w, "MediaMTX's WebRTC server does not answer", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxPatch))
}

// roomFor reports whether a UI session may start another live view. The live view's entries are never evicted: the
// access loop needs them to close the views of a user whose access ends. So a UI session past its bound is refused,
// and so is everyone once the proxy is full of live views alone.
func (p *Proxy) roomFor(owner string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buckets[owner] < maxPerClient && (len(p.sessions) < maxSessions || p.external > 0)
}

// addLocked keeps a session within the bounds: an external client's oldest entry goes when it has too many, and the
// oldest external entry when the proxy is full. External entries only carry trickle ICE and the end of a session:
// revoking a credential or making a stream private finds those sessions through the authentication endpoint. Only a
// bound costs a scan.
func (p *Proxy) addLocked(id string, s whep) {
	p.removeLocked(id) // MediaMTX's ids are random; never counted twice all the same
	if s.owner == externalOwner && p.buckets[s.bucket] >= maxPerClient {
		p.removeLocked(p.oldestLocked(func(o whep) bool { return o.bucket == s.bucket }))
	}
	if len(p.sessions) >= maxSessions {
		p.removeLocked(p.oldestLocked(func(o whep) bool { return o.owner == externalOwner }))
	}
	p.sessions[id] = s
	p.buckets[s.bucket]++
	if s.owner == externalOwner {
		p.external++
	}
}

func (p *Proxy) oldestLocked(match func(whep) bool) string {
	var id string
	var at time.Time
	for k, s := range p.sessions {
		if match(s) && (id == "" || s.created.Before(at)) {
			id, at = k, s.created
		}
	}
	return id
}

func (p *Proxy) removeLocked(id string) {
	s, ok := p.sessions[id]
	if !ok {
		return
	}
	delete(p.sessions, id)
	if p.buckets[s.bucket]--; p.buckets[s.bucket] <= 0 {
		delete(p.buckets, s.bucket)
	}
	if s.owner == externalOwner {
		p.external--
	}
}

// Session is a WHEP or WHIP session the proxy keeps, as the sidecar's access loop sees it.
type Session struct {
	ID       string // the proxy's own id, in the session URL it handed out
	MediaMTX string // MediaMTX's session id, as its API lists the session; "" if MediaMTX sent none
	Path     string
	Viewer   string // the UI session's key (Viewer.Key) for the live view's sessions; "" for external clients
	Started  time.Time
}

// Sessions lists the sessions the proxy keeps.
func (p *Proxy) Sessions() []Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Session, 0, len(p.sessions))
	for id, s := range p.sessions {
		viewer := s.owner
		if viewer == externalOwner {
			viewer = ""
		}
		out = append(out, Session{ID: id, MediaMTX: s.mtxID, Path: s.path, Viewer: viewer, Started: s.created})
	}
	return out
}

// Forget drops sessions that have ended (MediaMTX no longer lists them).
func (p *Proxy) Forget(ids []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		p.removeLocked(id)
	}
}

// End closes a session as its owner's DELETE would, and forgets it: for a viewer who may no longer watch.
func (p *Proxy) End(ctx context.Context, id string) error {
	p.mu.Lock()
	s, ok := p.sessions[id]
	p.removeLocked(id)
	p.mu.Unlock()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.upstream, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound { // 404: it had ended already
		return errors.New("MediaMTX answered " + resp.Status)
	}
	return nil
}

// UpstreamLocation is what WHEPOffer keeps for a session; for tests.
func (p *Proxy) UpstreamLocation(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id].upstream
}
