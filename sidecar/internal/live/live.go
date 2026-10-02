// Package live keeps the UI current. The hub polls MediaMTX's Control API (every second while anyone watches, every
// five seconds otherwise), diffs each list item by item against the previous poll, and fans the changes out to the
// browsers' event streams: a snapshot on connect, then updates, each with an id so a reconnecting browser resumes
// where it left off. It also keeps the last hour of traffic and client counts in memory for the dashboard's charts.
//
// Which lists a subscriber receives follows the operations table: the same minimum role the API proxy demands for
// the list operation. Deprecated fields, which only duplicate their replacements, are dropped before diffing.
package live

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mtxui/internal/auth"
	"mtxui/internal/mtxapi"
)

// Authorizer adds the sidecar's credentials to a request.
type Authorizer interface {
	Authorize(*http.Request)
}

// Kind names a mirrored list (or, for info, a single object).
type Kind string

// source describes one mirrored API list.
type source struct {
	kind   Kind
	opID   string
	key    string   // the item field that identifies an item; "" for a single object
	strip  []string // deprecated fields, dropped (a test checks them against the spec)
	client bool     // its items are clients, counted in the history
}

var (
	bytesDeprecated   = []string{"bytesReceived", "bytesSent"}
	packetsDeprecated = []string{
		"bytesReceived", "bytesSent", "rtcpPacketsReceived", "rtcpPacketsSent", "rtpPacketsJitter", "rtpPacketsLost",
		"rtpPacketsReceived", "rtpPacketsSent",
	}
	rtspSessionDeprecated = append([]string{"rtcpPacketsInError", "rtpPacketsInError"}, packetsDeprecated...)
)

var sources = []source{
	{kind: "info", opID: "info"},
	{kind: "paths", opID: "pathsList", key: "name", strip: []string{"bytesReceived", "bytesSent", "ready", "readyTime", "tracks"}},
	{kind: "rtspConns", opID: "rtspConnsList", key: "id", strip: bytesDeprecated},
	{kind: "rtspSessions", opID: "rtspSessionsList", key: "id", strip: rtspSessionDeprecated, client: true},
	{kind: "rtspsConns", opID: "rtspsConnsList", key: "id", strip: bytesDeprecated},
	{kind: "rtspsSessions", opID: "rtspsSessionsList", key: "id", strip: rtspSessionDeprecated, client: true},
	{kind: "rtmpConns", opID: "rtmpConnsList", key: "id", strip: bytesDeprecated, client: true},
	{kind: "rtmpsConns", opID: "rtmpsConnsList", key: "id", strip: bytesDeprecated, client: true},
	{kind: "srtConns", opID: "srtConnsList", key: "id", client: true},
	{kind: "webrtcSessions", opID: "webrtcSessionsList", key: "id", strip: packetsDeprecated, client: true},
	{kind: "hlsMuxers", opID: "hlsMuxersList", key: "path", strip: []string{"bytesSent"}},
	{kind: "hlsSessions", opID: "hlssessionsList", key: "id", client: true},
	{kind: "moqSessions", opID: "moqSessionsList", key: "id", client: true},
}

const (
	activeInterval = time.Second     // poll interval while anyone watches
	idleInterval   = 5 * time.Second // otherwise; still often enough for the history
	// SampleInterval is the spacing of the history samples.
	SampleInterval = 5 * time.Second

	historySize  = 720 // an hour of 5 s samples
	ringSize     = 1024
	subBuffer    = 256
	itemsPerPage = 1000
	maxPages     = 20 // 20,000 items per list; beyond that the list is marked truncated
	maxBody      = 32 << 20
)

// Event is one server-sent event.
type Event struct {
	ID      string
	Type    string // snapshot, update, status, sample, rates
	Data    []byte
	minRole auth.Role
	seq     uint64
}

// Status says whether MediaMTX answers the hub's polls.
type Status struct {
	Reachable bool      `json:"reachable"`
	Since     time.Time `json:"since"`             // when Reachable last changed (or the hub started)
	Error     string    `json:"error,omitempty"`   // why it is unreachable
	PolledAt  time.Time `json:"polledAt,omitzero"` // the last successful poll
}

type list struct {
	available bool // the endpoint answered; false while that protocol's server is off
	truncated bool
	items     map[string]json.RawMessage
	value     json.RawMessage // single objects (info)
}

type compiled struct {
	source
	path    string
	minRole auth.Role
}

// Hub polls MediaMTX and serves subscribers.
type Hub struct {
	api       string
	principal Authorizer
	client    *http.Client
	sources   []compiled
	log       *slog.Logger
	epoch     string
	wake      chan struct{}
	active    time.Duration
	idle      time.Duration

	mu      sync.Mutex
	seq     uint64
	status  Status
	lists   map[Kind]*list
	ring    []Event // the last ringSize events, oldest first
	subs    map[*Subscription]struct{}
	closed  bool
	history *history
	rates   *Rates // the latest per-path bitrates
	extras  map[string]extra
}

// extra is a value the sidecar itself produces (exposure status, for one), streamed next to MediaMTX's lists.
type extra struct {
	minRole auth.Role
	value   json.RawMessage
}

// New builds a hub for the MediaMTX API at api.
func New(api string, principal Authorizer, ops []mtxapi.Operation, log *slog.Logger) (*Hub, error) {
	byID := map[string]mtxapi.Operation{}
	for _, op := range ops {
		byID[op.ID] = op
	}
	epoch := make([]byte, 4)
	_, _ = rand.Read(epoch)
	h := &Hub{
		api: strings.TrimSuffix(api, "/"), principal: principal, log: log, epoch: hex.EncodeToString(epoch),
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		wake: make(chan struct{}, 1), active: activeInterval, idle: idleInterval, lists: map[Kind]*list{}, subs: map[*Subscription]struct{}{},
		status: Status{Since: time.Now()}, history: newHistory(historySize), extras: map[string]extra{},
	}
	for _, s := range sources {
		op, ok := byID[s.opID]
		if !ok || op.Method != http.MethodGet {
			return nil, fmt.Errorf("live: operation %q is not a GET in the operations table", s.opID)
		}
		switch op.Access {
		case mtxapi.AccessViewer, mtxapi.AccessOperator, mtxapi.AccessAdmin:
		default:
			return nil, fmt.Errorf("live: operation %q is not open to UI roles (%s)", s.opID, op.Access)
		}
		s.strip = slices.Concat(s.strip, op.Redact) // what the proxy redacts, the stream drops too
		h.sources = append(h.sources, compiled{source: s, path: op.Path, minRole: auth.Role(op.Access)})
	}
	return h, nil
}

// Run polls until ctx ends, then closes every subscription.
func (h *Hub) Run(ctx context.Context) {
	defer h.closeAll()
	var lastSample time.Time
	for {
		h.Poll(ctx)
		if time.Since(lastSample) >= SampleInterval-h.active/2 {
			h.sample(time.Now())
			lastSample = time.Now()
		}
		wait := h.idle
		if h.Subscribers() > 0 {
			wait = h.active
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-h.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// Subscribers is the number of open subscriptions.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

type fetched struct {
	src   *compiled
	list  *list
	err   error // transport failure or an unexpected answer
	fatal bool  // MediaMTX itself is unreachable or refuses the sidecar
}

// Poll fetches every list once and publishes the differences.
func (h *Hub) Poll(ctx context.Context) {
	results := make([]fetched, len(h.sources))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range h.sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			src := &h.sources[i]
			l, fatal, err := h.fetch(ctx, src)
			results[i] = fetched{src: src, list: l, err: err, fatal: fatal}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}

	var fatal error
	for _, r := range results {
		if r.fatal {
			fatal = r.err
			break
		}
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if fatal != nil {
		if h.status.Reachable || h.status.Error == "" {
			h.status = Status{Reachable: false, Since: now, Error: fatal.Error(), PolledAt: h.status.PolledAt}
			h.log.Warn("MediaMTX API unreachable", "err", fatal)
			h.publishLocked("status", auth.RoleViewer, h.status)
		}
		return
	}
	if !h.status.Reachable {
		if h.status.Error != "" {
			h.log.Info("MediaMTX API reachable again")
		}
		h.status = Status{Reachable: true, Since: now}
		h.status.PolledAt = now
		h.publishLocked("status", auth.RoleViewer, h.status)
	}
	h.status.PolledAt = now
	for _, r := range results {
		if r.err != nil {
			h.log.Debug("list poll failed", "kind", r.src.kind, "err", r.err)
			continue
		}
		h.applyLocked(r.src, r.list)
	}
}

// update is the payload of an update event.
type update struct {
	Kind      Kind              `json:"kind"`
	Available bool              `json:"available"`
	Truncated bool              `json:"truncated,omitempty"`
	Reset     bool              `json:"reset,omitempty"` // replace the whole list with Upsert
	Upsert    []json.RawMessage `json:"upsert,omitempty"`
	Remove    []string          `json:"remove,omitempty"`
	Value     json.RawMessage   `json:"value,omitempty"`
}

func (h *Hub) applyLocked(src *compiled, next *list) {
	prev, seen := h.lists[src.kind]
	h.lists[src.kind] = next
	u := update{Kind: src.kind, Available: next.available, Truncated: next.truncated}
	if src.key == "" {
		if seen && prev.available == next.available && string(prev.value) == string(next.value) {
			return
		}
		u.Value = next.value
		h.publishLocked("update", src.minRole, u)
		return
	}
	if !seen || prev.available != next.available || prev.truncated != next.truncated {
		u.Reset = true
		u.Upsert = sortedItems(next.items)
		h.publishLocked("update", src.minRole, u)
		return
	}
	for _, k := range sortedKeys(next.items) {
		if old, ok := prev.items[k]; !ok || string(old) != string(next.items[k]) {
			u.Upsert = append(u.Upsert, next.items[k])
		}
	}
	for _, k := range sortedKeys(prev.items) {
		if _, ok := next.items[k]; !ok {
			u.Remove = append(u.Remove, k)
		}
	}
	if len(u.Upsert) > 0 || len(u.Remove) > 0 {
		h.publishLocked("update", src.minRole, u)
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedItems(m map[string]json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

var errUnauthorized = errors.New("MediaMTX refused the sidecar's credentials")

// fetch reads one list, every page. fatal reports a failure of MediaMTX as a whole (unreachable, refusing the
// sidecar, answering garbage) rather than of one protocol's server being off.
func (h *Hub) fetch(ctx context.Context, src *compiled) (*list, bool, error) {
	l := &list{available: true, items: map[string]json.RawMessage{}}
	for page := 0; ; page++ {
		url := h.api + src.path
		if src.key != "" {
			url += "?itemsPerPage=" + strconv.Itoa(itemsPerPage) + "&page=" + strconv.Itoa(page)
		}
		code, body, err := h.get(ctx, url)
		switch {
		case err != nil:
			return nil, true, err
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return nil, true, errUnauthorized
		case code == http.StatusNotFound || code == http.StatusBadRequest:
			// The protocol's server is off: MediaMTX answers its endpoints with an error.
			return &list{items: map[string]json.RawMessage{}}, false, nil
		case code != http.StatusOK:
			return nil, src.kind == "paths", fmt.Errorf("MediaMTX answered %d", code)
		}
		if src.key == "" {
			obj, err := clean(body, src.strip)
			if err != nil {
				return nil, true, err
			}
			l.value = obj
			return l, false, nil
		}
		var doc struct {
			PageCount int               `json:"pageCount"`
			Items     []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, true, fmt.Errorf("MediaMTX sent an unreadable %s list: %w", src.kind, err)
		}
		for _, raw := range doc.Items {
			key, item, err := keyed(raw, src.key, src.strip)
			if err != nil {
				return nil, true, fmt.Errorf("MediaMTX sent an unreadable %s item: %w", src.kind, err)
			}
			l.items[key] = item
		}
		if page+1 >= doc.PageCount {
			return l, false, nil
		}
		if page+1 >= maxPages {
			l.truncated = true
			return l, false, nil
		}
	}
}

func (h *Hub) get(ctx context.Context, url string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	h.principal.Authorize(req)
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, nil, errors.New("MediaMTX's API does not answer")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, errors.New("MediaMTX's API answer broke off")
	}
	if len(body) > maxBody {
		return 0, nil, errors.New("MediaMTX's API answer is oversized")
	}
	return resp.StatusCode, body, nil
}

// clean drops fields from a JSON object and re-encodes it with sorted keys, so equal objects are equal bytes.
func clean(raw []byte, strip []string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for _, f := range strip {
		delete(m, f)
	}
	return json.Marshal(m)
}

func keyed(raw []byte, keyField string, strip []string) (string, json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", nil, err
	}
	var key string
	if err := json.Unmarshal(m[keyField], &key); err != nil || key == "" {
		return "", nil, fmt.Errorf("no %s", keyField)
	}
	for _, f := range strip {
		delete(m, f)
	}
	b, err := json.Marshal(m)
	return key, b, err
}

// publishLocked appends an event to the ring and hands it to every subscriber allowed to see it. A subscriber that
// cannot keep up is closed; its browser reconnects and resumes from the ring or a fresh snapshot.
func (h *Hub) publishLocked(typ string, minRole auth.Role, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		h.log.Error("live: cannot encode event", "type", typ, "err", err)
		return
	}
	h.seq++
	ev := Event{ID: h.idLocked(), Type: typ, Data: data, minRole: minRole, seq: h.seq}
	h.ring = append(h.ring, ev)
	if len(h.ring) > ringSize {
		h.ring = append(h.ring[:0], h.ring[len(h.ring)-ringSize:]...)
	}
	for sub := range h.subs {
		e := ev
		if sub.scope != nil {
			var ok bool
			if e, ok = scopedEvent(ev, payload, sub.scope); !ok {
				continue
			}
		} else if !sub.role.AtLeast(minRole) {
			continue
		}
		select {
		case sub.c <- e:
		default:
			h.dropLocked(sub)
		}
	}
}

func (h *Hub) idLocked() string { return h.epoch + "-" + strconv.FormatUint(h.seq, 10) }

// Subscription is one browser's event stream.
type Subscription struct {
	C       <-chan Event
	Initial []Event // what to send first: a snapshot, or the events missed since Last-Event-ID
	c       chan Event
	role    auth.Role
	scope   Scope
}

// Scope decides which paths a scoped subscription sees. It is called for every event, so it must be cheap, and it
// runs with the hub locked, so it must not call back into the hub.
type Scope func(path string) bool

// SubscribeScoped opens a subscription limited to some paths, for streamers: MediaMTX's status, the
// paths list and the per-path rates, each filtered by scope, and nothing else (no other list, no totals, no extras).
// It always starts with a snapshot: replaying the ring would need every past event re-filtered.
func (h *Hub) SubscribeScoped(scope Scope) (*Subscription, error) {
	c := make(chan Event, subBuffer)
	sub := &Subscription{C: c, c: c, role: auth.RoleStreamer, scope: scope}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("live: shutting down")
	}
	sub.Initial = []Event{h.scopedSnapshotLocked(scope)}
	h.subs[sub] = struct{}{}
	if len(h.subs) == 1 {
		select {
		case h.wake <- struct{}{}:
		default:
		}
	}
	return sub, nil
}

// scopedEvent is ev as a scoped subscription sees it, if it sees it at all.
func scopedEvent(ev Event, payload any, scope Scope) (Event, bool) {
	var out any
	switch p := payload.(type) {
	case Status:
		return ev, true
	case update:
		if p.Kind != "paths" {
			return Event{}, false
		}
		u := update{
			Kind: p.Kind, Available: p.Available, Truncated: p.Truncated, Reset: p.Reset,
			Upsert: scopedItems(p.Upsert, scope),
		}
		for _, k := range p.Remove {
			if scope(k) {
				u.Remove = append(u.Remove, k)
			}
		}
		if !u.Reset && len(u.Upsert) == 0 && len(u.Remove) == 0 {
			return Event{}, false
		}
		out = u
	case *Rates:
		out = scopedRates(p, scope)
	default:
		return Event{}, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return Event{}, false
	}
	return Event{ID: ev.ID, Type: ev.Type, Data: b, minRole: ev.minRole, seq: ev.seq}, true
}

// scopedItems keeps the paths list items whose name is in scope.
func scopedItems(items []json.RawMessage, scope Scope) []json.RawMessage {
	var out []json.RawMessage
	for _, raw := range items {
		var it struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &it) == nil && scope(it.Name) {
			out = append(out, raw)
		}
	}
	return out
}

func scopedRates(r *Rates, scope Scope) *Rates {
	if r == nil {
		return nil
	}
	out := &Rates{T: r.T, Paths: map[string]PathRate{}}
	for name, pr := range r.Paths {
		if scope(name) {
			out.Paths[name] = pr
		}
	}
	return out
}

func (h *Hub) scopedSnapshotLocked(scope Scope) Event {
	snap := snapshot{Status: h.status, Lists: map[Kind]snapshotList{}, Rates: scopedRates(h.rates, scope)}
	if l, ok := h.lists["paths"]; ok {
		snap.Lists["paths"] = snapshotList{
			Available: l.available, Truncated: l.truncated,
			Items: scopedItems(sortedItems(l.items), scope),
		}
	}
	data, _ := json.Marshal(snap)
	return Event{ID: h.idLocked(), Type: "snapshot", Data: data, seq: h.seq}
}

// Subscribe opens a subscription for role. lastID is the browser's Last-Event-ID: when the ring still holds every
// event after it, those are replayed; otherwise the subscription starts with a snapshot.
func (h *Hub) Subscribe(role auth.Role, lastID string) (*Subscription, error) {
	c := make(chan Event, subBuffer)
	sub := &Subscription{C: c, c: c, role: role}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("live: shutting down")
	}
	if missed, ok := h.sinceLocked(lastID, role); ok {
		sub.Initial = missed
	} else {
		sub.Initial = []Event{h.snapshotLocked(role)}
	}
	h.subs[sub] = struct{}{}
	if len(h.subs) == 1 {
		select { // the first watcher: poll now rather than at the next idle tick
		case h.wake <- struct{}{}:
		default:
		}
	}
	return sub, nil
}

// Unsubscribe ends a subscription.
func (h *Hub) Unsubscribe(sub *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropLocked(sub)
}

func (h *Hub) dropLocked(sub *Subscription) {
	if _, ok := h.subs[sub]; ok {
		delete(h.subs, sub)
		close(sub.c)
	}
}

func (h *Hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for sub := range h.subs {
		h.dropLocked(sub)
	}
}

func (h *Hub) sinceLocked(lastID string, role auth.Role) ([]Event, bool) {
	epoch, n, ok := strings.Cut(lastID, "-")
	if !ok || epoch != h.epoch {
		return nil, false
	}
	seq, err := strconv.ParseUint(n, 10, 64)
	if err != nil || seq > h.seq {
		return nil, false
	}
	if seq == h.seq {
		return nil, true
	}
	if len(h.ring) == 0 || h.ring[0].seq > seq+1 {
		return nil, false // events after lastID have left the ring
	}
	var out []Event
	for _, ev := range h.ring {
		if ev.seq > seq && role.AtLeast(ev.minRole) {
			out = append(out, ev)
		}
	}
	return out, true
}

type snapshotList struct {
	Available bool              `json:"available"`
	Truncated bool              `json:"truncated,omitempty"`
	Items     []json.RawMessage `json:"items,omitempty"`
	Value     json.RawMessage   `json:"value,omitempty"`
}

type snapshot struct {
	Status Status                     `json:"status"`
	Lists  map[Kind]snapshotList      `json:"lists"`
	Rates  *Rates                     `json:"rates"`
	Extras map[string]json.RawMessage `json:"extras,omitempty"`
}

// ExtraEvent is the "extra" event: a new value of one of the sidecar's own streamed values.
type ExtraEvent struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

// SetExtra streams a value of the sidecar's own under kind, to subscribers with at least minRole; unchanged values
// send nothing.
func (h *Hub) SetExtra(kind string, minRole auth.Role, value any) {
	b, err := json.Marshal(value)
	if err != nil {
		h.log.Error("live: cannot encode extra", "kind", kind, "err", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.extras[kind]; ok && old.minRole == minRole && bytes.Equal(old.value, b) {
		return
	}
	h.extras[kind] = extra{minRole: minRole, value: b}
	h.publishLocked("extra", minRole, ExtraEvent{Kind: kind, Value: b})
}

func (h *Hub) snapshotLocked(role auth.Role) Event {
	snap := snapshot{Status: h.status, Lists: map[Kind]snapshotList{}, Rates: h.rates}
	for k, e := range h.extras {
		if role.AtLeast(e.minRole) {
			if snap.Extras == nil {
				snap.Extras = map[string]json.RawMessage{}
			}
			snap.Extras[k] = e.value
		}
	}
	for _, src := range h.sources {
		l, ok := h.lists[src.kind]
		if !ok || !role.AtLeast(src.minRole) {
			continue
		}
		sl := snapshotList{Available: l.available, Truncated: l.truncated, Value: l.value}
		if src.key != "" {
			sl.Items = sortedItems(l.items)
		}
		snap.Lists[src.kind] = sl
	}
	data, _ := json.Marshal(snap)
	return Event{ID: h.idLocked(), Type: "snapshot", Data: data, seq: h.seq}
}

// Item returns an item of a list (a connection or session by id) as MediaMTX last listed it, or nil.
func (h *Hub) Item(kind Kind, key string) json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l, ok := h.lists[kind]; ok {
		return l.items[key]
	}
	return nil
}

// AnyOnline reports whether any path has a publisher or source right now.
func (h *Hub) AnyOnline() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.lists["paths"]
	if !ok {
		return false
	}
	for _, raw := range l.items {
		var p struct {
			Online bool `json:"online"`
		}
		if json.Unmarshal(raw, &p) == nil && p.Online {
			return true
		}
	}
	return false
}

// Path returns a path as MediaMTX last listed it, or nil.
func (h *Hub) Path(name string) json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l, ok := h.lists["paths"]; ok {
		return l.items[name]
	}
	return nil
}

// Status returns the hub's view of MediaMTX.
func (h *Hub) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

// Client is a session or connection by protocol and id, as MediaMTX's kick operations name it.
type Client struct {
	Protocol string
	ID       string
}

var clientProtocol = map[Kind]string{
	"rtspSessions": "rtsp", "rtspsSessions": "rtsps", "rtmpConns": "rtmp", "rtmpsConns": "rtmps",
	"srtConns": "srt", "webrtcSessions": "webrtc", "hlsSessions": "hls", "moqSessions": "moq",
}

// ClientsOf lists the sessions and connections that MediaMTX last reported for user (the name a client
// authenticated with), from the latest poll.
func (h *Hub) ClientsOf(user string) []Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Client
	for kind, proto := range clientProtocol {
		l, ok := h.lists[kind]
		if !ok {
			continue
		}
		for id, raw := range l.items {
			var it struct {
				User string `json:"user"`
			}
			if json.Unmarshal(raw, &it) == nil && it.User == user {
				out = append(out, Client{Protocol: proto, ID: id})
			}
		}
	}
	return out
}
