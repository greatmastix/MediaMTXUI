package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/live"
	"mtxui/internal/portgate"
	"mtxui/internal/store"
)

// Automatic exposure: ports open because they are being used, on top of the Exposure page's manual
// openings. Rules, all on until an admin switches them off:
//   - publish: while a stream's manager has its page open (a lease the page renews), its publishing ports are open to
//     that browser's address (three addresses per stream); while a stream is live from a client, that protocol's port
//     is open to the client; while a guest publish key is valid, the publishing ports are open to anyone.
//   - remember: an address that published to a stream stays allowed for 30 days after its last use (three per stream).
//   - viewers: while any path is live or any stream has a holding screen, the output protocols (RTSP, RTMP, SRT, WebRTC
//     media) are open to anyone, so outside players (VRChat, VLC) and browsers reach them; keys still decide who may
//     watch.
// So any stream manager opens the publishing ports to anyone by setting a holding screen or making a guest publish key.
// Only protocols switched on in MediaMTX are opened, within the helper's policy. Openings carry short expiries that
// the loop renews, so they close by themselves when the sidecar stops.

// AutoRules are the automatic exposure rules.
type AutoRules struct {
	Publish  bool `json:"publish"`
	Remember bool `json:"remember"`
	Viewers  bool `json:"viewers"`
}

const autoRulesKey = "exposure.auto"

// Timing of automatic openings.
const (
	autoEvery       = 15 * time.Second
	leaseFor        = 5 * time.Minute  // a stream page renews its lease every minute
	liveFor         = 30 * time.Minute // renewed while live; quantized so desired.json changes rarely
	rememberFor     = 30 * 24 * time.Hour
	touchEncoderGap = time.Hour // how often a live encoder's last_seen is written
	// perStream is how many addresses one stream keeps open for each reason (its page's leases, its live encoders),
	// as for remembered encoders: a stream moving between addresses cannot crowd out the others' sources.
	perStream = 3
)

// publishPorts are the portgate port ids an encoder may use, with the MediaMTX switch that must be on.
var publishPorts = []struct{ port, setting string }{
	{"rtmp", "rtmp"}, {"srt", "srt"}, {"rtsp", "rtsp"}, {"webrtc", "webrtc"},
}

// sourceList maps a path source type to the list its connection is in, and the port its client came through.
var sourceList = map[string]struct {
	kind live.Kind
	port string
}{
	"rtmpConn": {"rtmpConns", "rtmp"}, "srtConn": {"srtConns", "srt"},
	"rtspSession": {"rtspSessions", "rtsp"}, "webRTCSession": {"webrtcSessions", "webrtc"},
}

type autoExpose struct {
	mu      sync.Mutex
	leases  map[int64]map[netip.Addr]time.Time // stream id -> browser address -> until
	touched map[string]time.Time               // stream/port/ip -> last write of last_seen
	kept    map[string]portgate.AutoOpening    // what is open, kept until its own expiry
	wake    chan struct{}
	clock   func() time.Time // tests
	closing sync.Mutex       // taking in a close-all on the host, once
}

func (a *autoExpose) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

// ruleOf names the rule an opening comes from, by its reason.
func ruleOf(reason string) string {
	switch {
	case strings.HasPrefix(reason, "known encoder"):
		return "remember"
	case strings.HasPrefix(reason, "viewers"):
		return "viewers"
	}
	return "publish"
}

// sticky keeps every opening until its own expiry, even after its reason has gone (the stream went offline, the
// page closed): a stream that drops and reconnects, or goes live again soon, changes no firewall rule, and each
// change costs calls to the cloud firewall's API. Openings of a rule that is switched off go at once, and so do a
// stream's addresses for one reason beyond perStream (those still in use first, then the latest). Openings that stand
// for a record (a remembered encoder, a guest key) do not come here: they end with their record.
func (a *autoExpose) sticky(fresh []portgate.AutoOpening, rules AutoRules, now time.Time) []portgate.AutoOpening {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kept == nil {
		a.kept = map[string]portgate.AutoOpening{}
	}
	inUse := map[string]bool{}
	for _, o := range fresh {
		key := o.Port + "|" + o.Source + "|" + o.Reason
		inUse[key] = true
		if old, ok := a.kept[key]; !ok || o.Until.After(old.Until) {
			a.kept[key] = o
		}
	}
	on := map[string]bool{"publish": rules.Publish, "remember": rules.Remember, "viewers": rules.Viewers}
	named := map[string][]string{} // port|reason -> the keys of its openings to an address
	for key, o := range a.kept {
		if !o.Until.After(now) || !on[ruleOf(o.Reason)] {
			delete(a.kept, key)
			continue
		}
		if o.Source != "" {
			named[o.Port+"|"+o.Reason] = append(named[o.Port+"|"+o.Reason], key)
		}
	}
	for _, keys := range named {
		if len(keys) <= perStream {
			continue
		}
		sort.Slice(keys, func(i, j int) bool {
			if inUse[keys[i]] != inUse[keys[j]] {
				return inUse[keys[i]]
			}
			ui, uj := a.kept[keys[i]].Until, a.kept[keys[j]].Until
			return ui.After(uj) || (ui.Equal(uj) && keys[i] < keys[j])
		})
		for _, key := range keys[perStream:] {
			delete(a.kept, key)
		}
	}
	out := make([]portgate.AutoOpening, 0, len(a.kept))
	for _, o := range a.kept {
		out = append(out, o)
	}
	return out
}

// byRank orders openings by what counts most when a port's sources run short: encoders streaming now, then stream
// pages setting up, then remembered encoders (an opening to anyone takes no source).
func byRank(out []portgate.AutoOpening) {
	rank := func(reason string) int {
		switch {
		case strings.HasPrefix(reason, "live: "):
			return 0
		case strings.HasPrefix(reason, "setting up "):
			return 1
		case ruleOf(reason) == "remember":
			return 3
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i].Reason), rank(out[j].Reason); ri != rj {
			return ri < rj
		}
		return out[i].Port+out[i].Source+out[i].Reason < out[j].Port+out[j].Source+out[j].Reason
	})
}

func (a *autoExpose) init() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.wake == nil {
		a.wake = make(chan struct{}, 1)
		a.leases = map[int64]map[netip.Addr]time.Time{}
		a.touched = map[string]time.Time{}
	}
}

func (a *autoExpose) poke() {
	a.init()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (s *Server) autoRules(ctx context.Context) AutoRules {
	rules := AutoRules{Publish: true, Remember: true, Viewers: true}
	if v, ok, err := s.d.Store.Meta(ctx, autoRulesKey); err == nil && ok {
		_ = json.Unmarshal([]byte(v), &rules)
	}
	return rules
}

func (s *Server) setAutoRules(ctx context.Context, rules AutoRules) error {
	b, _ := json.Marshal(rules)
	if err := s.d.Store.SetMeta(ctx, autoRulesKey, string(b)); err != nil {
		return err
	}
	s.auto.poke()
	return nil
}

// RunAutoExpose recomputes the automatic openings every 15 s and when poked, until ctx ends.
func (s *Server) RunAutoExpose(ctx context.Context) {
	s.auto.init()
	t := time.NewTicker(autoEvery)
	defer t.Stop()
	for {
		s.autoOnce(ctx) // expired guest keys' sessions end in RunAccess, with exposure control or without
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.auto.wake:
		}
	}
}

// publicAddr reports whether an address is worth opening a firewall to.
func publicAddr(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback()
}

// roundUp quantizes an expiry, so that renewals rewrite desired.json only when it moves to the next step.
func roundUp(t time.Time, step time.Duration) time.Time {
	if r := t.Truncate(step); !r.Equal(t) {
		return r.Add(step)
	}
	return t
}

func (s *Server) autoOnce(ctx context.Context) {
	st, err := s.d.Exposure.Status()
	if err != nil {
		return // no helper: nothing to open
	}
	if s.followHostCloseAll(ctx) {
		return
	}
	rules := s.autoRules(ctx)
	cfg := s.readMTXConfig()
	usable := func(port, setting string) bool {
		_, known := st.Ports[port]
		return known && cfg.on(setting)
	}
	now := s.auto.now()
	streams, err := s.d.Store.Streams(ctx)
	if err != nil {
		s.d.Log.Warn("automatic exposure", "err", err)
		return // a failed read must not close what is open
	}
	byID := map[int64]store.Stream{}
	for _, str := range streams {
		byID[str.ID] = str
	}
	// out is kept until its own expiry (sticky); records are what a remembered encoder or a guest key stands for, and
	// end with it.
	var out, records []portgate.AutoOpening

	if rules.Publish {
		s.auto.mu.Lock()
		for id, ips := range s.auto.leases {
			str, ok := byID[id]
			for ip, until := range ips {
				if !ok || !until.After(now) {
					delete(ips, ip)
					continue
				}
				for _, p := range publishPorts {
					if usable(p.port, p.setting) {
						out = append(out, portgate.AutoOpening{
							Port: p.port, Source: ip.String(),
							Until: roundUp(until, time.Minute), Reason: "setting up " + str.Name,
						})
					}
				}
			}
			if len(ips) == 0 {
				delete(s.auto.leases, id)
			}
		}
		s.auto.mu.Unlock()
		// A guest's address is unknown: while a guest publish key is valid, its stream's publishing ports are open to
		// anyone (the key still decides who gets in). A revoked key's opening ends with it.
		guests, err := s.d.Store.ActiveGuestKeys(ctx, now)
		if err != nil {
			s.d.Log.Warn("automatic exposure", "err", err)
			return
		}
		for _, g := range guests {
			str, ok := byID[g.StreamID]
			if !ok || g.Kind != store.GuestPublish {
				continue
			}
			until := roundUp(now.Add(liveFor), 15*time.Minute)
			if exp := g.Credential.ExpiresAt; exp != nil && exp.Before(until) {
				until = roundUp(*exp, time.Minute)
			}
			for _, p := range publishPorts {
				if usable(p.port, p.setting) {
					records = append(records, portgate.AutoOpening{Port: p.port, Until: until, Reason: "guest key for " + str.Name})
				}
			}
		}
		for _, str := range streams {
			port, ip, ok := s.livePublisher(str.Name)
			if !ok || !usable(port, port) {
				continue
			}
			out = append(out, portgate.AutoOpening{
				Port: port, Source: ip.String(),
				Until: roundUp(now.Add(liveFor), 15*time.Minute), Reason: "live: " + str.Name,
			})
			if rules.Remember {
				s.touchEncoder(ctx, str.ID, port, ip, now)
			}
		}
	}
	if rules.Remember {
		// An address the store no longer has (a fourth replaced it, its stream was deleted) closes at once.
		addrs, err := s.d.Store.EncoderAddresses(ctx, now.Add(-rememberFor))
		if err != nil {
			s.d.Log.Warn("automatic exposure", "err", err)
			return
		}
		for _, a := range addrs {
			str, ok := byID[a.StreamID]
			if !ok || !usable(a.Port, a.Port) {
				continue
			}
			records = append(records, portgate.AutoOpening{
				Port: a.Port, Source: a.IP,
				Until: a.LastSeen.Truncate(24 * time.Hour).Add(rememberFor), Reason: "known encoder of " + str.Name,
			})
		}
	}
	// While a stream is live, or shows a holding screen (always available), outside players must reach it.
	if rules.Viewers && (s.d.Live.AnyOnline() || anyHolding(streams)) {
		for _, p := range publishPorts {
			if usable(p.port, p.setting) {
				out = append(out, portgate.AutoOpening{
					Port: p.port, Until: roundUp(now.Add(liveFor), 15*time.Minute), Reason: "viewers of live streams",
				})
			}
		}
	}
	out = append(s.auto.sticky(out, rules, now), records...)
	byRank(out)
	if err := s.d.Exposure.SetAuto(out); err != nil {
		s.d.Log.Warn("automatic exposure", "err", err)
		return
	}
	s.exposurePublish()
}

// followHostCloseAll takes in a close-all on the host (mtx-portgate close-all) the way Close all on the Exposure page
// works: the automatic rules go off and the manual openings close. Until the sidecar has written that, the helper
// opens nothing, so its routine renewals cannot undo the host's close-all; afterwards admins open again as they see
// fit. It reports whether there was one to take in.
func (s *Server) followHostCloseAll(ctx context.Context) bool {
	s.auto.closing.Lock()
	defer s.auto.closing.Unlock()
	st, err := s.d.Exposure.Status()
	if err != nil || !s.d.Exposure.ClosedOnHost(st) {
		return false
	}
	if err := s.setAutoRules(ctx, AutoRules{}); err != nil {
		s.d.Log.Warn("taking in the close-all on the host", "err", err)
		return true
	}
	d, err := s.d.Exposure.CloseAll()
	if err != nil {
		s.d.Log.Warn("taking in the close-all on the host", "err", err)
		return true
	}
	at := st.ClosedAll.UTC().Format(time.RFC3339)
	s.d.Log.Warn("everything was closed on the host (mtx-portgate close-all): the automatic rules are off and the manual openings closed", "at", at)
	s.d.Audit.Record(ctx, store.AuditEvent{
		Actor: "system", Action: "exposure.close-all", Target: "host", Details: map[string]any{"closedAll": at, "rev": d.Rev},
	})
	s.exposurePublish()
	return true
}

// livePublisher returns the port and public address of the client publishing to path, if any.
func (s *Server) livePublisher(path string) (string, netip.Addr, bool) {
	var p struct {
		Online bool `json:"online"`
		Source *struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"source"`
	}
	raw := s.d.Live.Path(path)
	if raw == nil || json.Unmarshal(raw, &p) != nil || !p.Online || p.Source == nil {
		return "", netip.Addr{}, false
	}
	src, ok := sourceList[p.Source.Type]
	if !ok {
		return "", netip.Addr{}, false
	}
	var c struct {
		RemoteAddr string `json:"remoteAddr"`
	}
	item := s.d.Live.Item(src.kind, p.Source.ID)
	if item == nil || json.Unmarshal(item, &c) != nil {
		return "", netip.Addr{}, false
	}
	host, _, err := net.SplitHostPort(c.RemoteAddr)
	if err != nil {
		host = c.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip.Unmap()) {
		return "", netip.Addr{}, false
	}
	return src.port, ip.Unmap(), true
}

func (s *Server) touchEncoder(ctx context.Context, streamID int64, port string, ip netip.Addr, now time.Time) {
	key := itoa64(streamID) + "/" + port + "/" + ip.String()
	s.auto.mu.Lock()
	last := s.auto.touched[key]
	if now.Sub(last) < touchEncoderGap {
		s.auto.mu.Unlock()
		return
	}
	s.auto.touched[key] = now
	s.auto.mu.Unlock()
	if err := s.d.Store.TouchEncoderAddress(ctx, store.EncoderAddress{StreamID: streamID, Port: port, IP: ip.String(), LastSeen: now}); err != nil {
		s.d.Log.Warn("remembering an encoder address", "err", err)
	}
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// LeaseInfo answers a stream page's lease.
type LeaseInfo struct {
	Leased  bool       `json:"leased"`
	Address string     `json:"address"`
	Until   *time.Time `json:"until,omitempty"`
	Reason  string     `json:"reason,omitempty"` // why not
	Off     bool       `json:"off,omitempty"`    // exposure control is off: the ports are as published
}

// streamLease keeps a stream's publishing ports open to the caller's address while its page is open.
func (s *Server) streamLease(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	ip := clientip.From(r.Context()).IP.Unmap()
	info := LeaseInfo{Address: ip.String()}
	switch {
	case !s.d.Settings.ExposureControl:
		info.Off = true
	case !s.autoRules(r.Context()).Publish:
		info.Reason = "Automatic opening for encoders is off."
	case !publicAddr(ip):
		info.Reason = "Your address as the sidecar sees it is not a public one."
	default:
		s.auto.init()
		until := time.Now().Add(leaseFor)
		s.auto.mu.Lock()
		ips := s.auto.leases[st.ID]
		if ips == nil {
			ips = map[netip.Addr]time.Time{}
			s.auto.leases[st.ID] = ips
		}
		fresh := ips[ip].IsZero()
		if fresh && len(ips) >= perStream { // a new address replaces the one whose lease ends first
			var first netip.Addr
			for a, u := range ips {
				if !first.IsValid() || u.Before(ips[first]) {
					first = a
				}
			}
			delete(ips, first)
		}
		ips[ip] = until
		s.auto.mu.Unlock()
		if fresh {
			s.auto.poke()
		}
		info.Leased, info.Until = true, &until
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) exposureAutoPut(w http.ResponseWriter, r *http.Request) {
	var rules AutoRules
	if !decodeJSON(w, r, &rules) {
		return
	}
	s.followHostCloseAll(r.Context()) // first, so it cannot switch these rules off again
	if err := s.setAutoRules(r.Context(), rules); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot save the rules.")
		return
	}
	audit.Set(r.Context(), "exposure.auto", "", map[string]any{"publish": rules.Publish, "remember": rules.Remember, "viewers": rules.Viewers})
	s.exposurePublish()
	writeJSON(w, http.StatusOK, rules)
}
