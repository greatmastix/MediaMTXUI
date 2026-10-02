package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/auth"
	"mtxui/internal/mtxapi"
	"mtxui/internal/mtxtest"
)

type principal struct{}

func (principal) Authorize(r *http.Request) { r.SetBasicAuth("mtxui-sidecar", "s") }

// fake is a MediaMTX API with settable lists. A list that is absent answers 400, like a server that is off.
type fake struct {
	mu      sync.Mutex
	down    bool
	lists   map[string][]map[string]any // by API path
	perPage int
}

func newFake() *fake {
	f := &fake{lists: map[string][]map[string]any{}}
	for _, s := range sources {
		if s.key != "" && s.kind != "rtmpConns" && s.kind != "rtmpsConns" {
			f.lists[pathOf(s.opID)] = []map[string]any{}
		}
	}
	return f
}

func pathOf(opID string) string {
	ops, _ := mtxapi.Operations()
	for _, op := range ops {
		if op.ID == opID {
			return op.Path
		}
	}
	panic(opID)
}

func (f *fake) set(opID string, items ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if items == nil {
		items = []map[string]any{}
	}
	f.lists[pathOf(opID)] = items
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, _, _ := r.BasicAuth(); u != "mtxui-sidecar" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.down {
		panic(http.ErrAbortHandler) // the connection drops, as with a MediaMTX that is gone
	}
	if r.URL.Path == "/v3/info" {
		_, _ = io.WriteString(w, `{"version":"v1.21.1","started":"2026-09-29T10:00:00Z"}`)
		return
	}
	items, ok := f.lists[r.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"server is disabled"}`)
		return
	}
	per := f.perPage
	if per == 0 {
		per = len(items) + 1
	}
	page := 0
	_, _ = fmt.Sscan(r.URL.Query().Get("page"), &page)
	start, end := min(page*per, len(items)), min((page+1)*per, len(items))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"pageCount": (len(items) + per - 1) / per, "itemCount": len(items), "items": items[start:end],
	})
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newHub(t *testing.T, f *fake) *Hub {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	ops, err := mtxapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(srv.URL, principal{}, ops, quiet())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func path(name string, in, out int, readers int) map[string]any {
	rs := make([]map[string]any, readers)
	for i := range rs {
		rs[i] = map[string]any{"type": "rtspSession", "id": fmt.Sprint(i)}
	}
	return map[string]any{
		"name": name, "online": true, "inboundBytes": in, "outboundBytes": out, "readers": rs,
		"bytesReceived": in, "bytesSent": out, "ready": true, // deprecated duplicates
	}
}

func next(t *testing.T, sub *Subscription) Event {
	t.Helper()
	select {
	case ev, ok := <-sub.C:
		if !ok {
			t.Fatal("subscription closed")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	return Event{}
}

func decode[T any](t *testing.T, ev Event) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(ev.Data, &v); err != nil {
		t.Fatalf("%s event %s: %v", ev.Type, ev.Data, err)
	}
	return v
}

func noEvent(t *testing.T, sub *Subscription) {
	t.Helper()
	select {
	case ev := <-sub.C:
		t.Fatalf("unexpected %s event: %s", ev.Type, ev.Data)
	default:
	}
}

func TestSnapshotAndDiffs(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.set("pathsList", path("a", 10, 0, 0), path("b", 0, 0, 0))
	h := newHub(t, f)
	h.Poll(ctx)

	sub, err := h.Subscribe(auth.RoleOperator, "")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Unsubscribe(sub)
	if len(sub.Initial) != 1 || sub.Initial[0].Type != "snapshot" {
		t.Fatalf("initial %+v", sub.Initial)
	}
	snap := decode[snapshot](t, sub.Initial[0])
	if !snap.Status.Reachable || len(snap.Lists["paths"].Items) != 2 || string(snap.Lists["info"].Value) == "" {
		t.Fatalf("snapshot %s", sub.Initial[0].Data)
	}
	if strings.Contains(string(sub.Initial[0].Data), "bytesReceived") || strings.Contains(string(sub.Initial[0].Data), `"ready"`) {
		t.Errorf("deprecated fields survived: %s", sub.Initial[0].Data)
	}
	if rtmp := snap.Lists["rtmpConns"]; rtmp.Available || !snap.Lists["rtspConns"].Available {
		t.Errorf("availability: rtmp %+v, rtsp %+v", rtmp, snap.Lists["rtspConns"])
	}

	h.Poll(ctx) // nothing changed: nothing to send
	noEvent(t, sub)

	f.set("pathsList", path("a", 20, 0, 0), path("c", 0, 0, 0))
	h.Poll(ctx)
	u := decode[update](t, next(t, sub))
	if u.Kind != "paths" || u.Reset || len(u.Upsert) != 2 || !slices.Equal(u.Remove, []string{"b"}) {
		t.Fatalf("update %+v", u)
	}
	if !strings.Contains(string(u.Upsert[0]), `"inboundBytes":20`) || !strings.Contains(string(u.Upsert[1]), `"name":"c"`) {
		t.Errorf("upsert %s", u.Upsert)
	}

	f.set("rtmpConnsList", map[string]any{"id": "1", "path": "a"}) // RTMP switched on
	h.Poll(ctx)
	u = decode[update](t, next(t, sub))
	if u.Kind != "rtmpConns" || !u.Available || !u.Reset || len(u.Upsert) != 1 {
		t.Fatalf("switch-on update %+v", u)
	}
}

func TestRoles(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	h := newHub(t, f)
	h.Poll(ctx)
	sub, _ := h.Subscribe(auth.RoleViewer, "")
	defer h.Unsubscribe(sub)
	snap := decode[snapshot](t, sub.Initial[0])
	var kinds []string
	for k := range snap.Lists {
		kinds = append(kinds, string(k))
	}
	slices.Sort(kinds)
	if !slices.Equal(kinds, []string{"info", "paths"}) {
		t.Fatalf("a viewer sees %v", kinds)
	}
	f.set("rtspSessionsList", map[string]any{"id": "x", "remoteAddr": "203.0.113.9:5000"})
	f.set("pathsList", path("a", 0, 0, 1))
	h.Poll(ctx)
	if u := decode[update](t, next(t, sub)); u.Kind != "paths" {
		t.Fatalf("a viewer got %+v", u)
	}
	noEvent(t, sub)
}

func TestResume(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	h := newHub(t, f)
	h.Poll(ctx)
	sub, _ := h.Subscribe(auth.RoleAdmin, "")
	last := sub.Initial[0].ID
	h.Unsubscribe(sub)
	if _, ok := <-sub.C; ok {
		t.Fatal("an unsubscribed channel stays open")
	}

	f.set("pathsList", path("a", 0, 0, 0))
	h.Poll(ctx)
	f.set("pathsList", path("a", 5, 0, 0))
	h.Poll(ctx)

	sub, _ = h.Subscribe(auth.RoleAdmin, last)
	if len(sub.Initial) != 2 || sub.Initial[0].Type != "update" || sub.Initial[1].Type != "update" {
		t.Fatalf("resume replayed %+v", sub.Initial)
	}
	last = sub.Initial[1].ID
	h.Unsubscribe(sub)

	sub, _ = h.Subscribe(auth.RoleAdmin, last) // up to date: nothing to replay
	if len(sub.Initial) != 0 {
		t.Errorf("up-to-date resume replayed %d events", len(sub.Initial))
	}
	h.Unsubscribe(sub)

	for _, stale := range []string{"00000000-1", "garbage", h.epoch + "-999999", h.epoch + "-x"} {
		sub, _ = h.Subscribe(auth.RoleAdmin, stale)
		if len(sub.Initial) != 1 || sub.Initial[0].Type != "snapshot" {
			t.Errorf("Last-Event-ID %q: %+v", stale, sub.Initial)
		}
		h.Unsubscribe(sub)
	}

	// Events that have left the ring cannot be replayed.
	h.mu.Lock()
	for range ringSize + 5 {
		h.publishLocked("status", auth.RoleViewer, h.status)
	}
	h.mu.Unlock()
	sub, _ = h.Subscribe(auth.RoleAdmin, last)
	if len(sub.Initial) != 1 || sub.Initial[0].Type != "snapshot" {
		t.Errorf("resume past the ring: %d events, first %q", len(sub.Initial), sub.Initial[0].Type)
	}
	h.Unsubscribe(sub)
}

func TestUnreachableAndBack(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.set("pathsList", path("a", 0, 0, 0))
	h := newHub(t, f)
	h.Poll(ctx)
	sub, _ := h.Subscribe(auth.RoleViewer, "")
	defer h.Unsubscribe(sub)

	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	h.Poll(ctx)
	st := decode[Status](t, next(t, sub))
	if st.Reachable || st.Error == "" {
		t.Fatalf("status %+v", st)
	}
	h.Poll(ctx) // still down: no repeat
	noEvent(t, sub)

	f.mu.Lock()
	f.down = false
	f.lists[pathOf("pathsList")] = []map[string]any{} // MediaMTX restarted: the publisher is gone
	f.mu.Unlock()
	h.Poll(ctx)
	if st := decode[Status](t, next(t, sub)); !st.Reachable {
		t.Fatalf("status %+v", st)
	}
	if u := decode[update](t, next(t, sub)); u.Kind != "paths" || !slices.Equal(u.Remove, []string{"a"}) {
		t.Fatalf("update %+v", u)
	}
}

func TestUnauthorizedIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	ops, _ := mtxapi.Operations()
	h, _ := New(srv.URL, principal{}, ops, quiet())
	h.Poll(context.Background())
	if st := h.Status(); st.Reachable || st.Error != errUnauthorized.Error() {
		t.Fatalf("status %+v", st)
	}
}

func TestPages(t *testing.T) {
	f := newFake()
	f.perPage = 2
	f.set("pathsList", path("a", 0, 0, 0), path("b", 0, 0, 0), path("c", 0, 0, 0), path("d", 0, 0, 0), path("e", 0, 0, 0))
	h := newHub(t, f)
	h.Poll(context.Background())
	if n := len(h.lists["paths"].items); n != 5 || h.lists["paths"].truncated {
		t.Fatalf("%d paths, truncated %v", n, h.lists["paths"].truncated)
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	f := newFake()
	h := newHub(t, f)
	h.Poll(context.Background())
	sub, _ := h.Subscribe(auth.RoleViewer, "")
	h.mu.Lock()
	for range subBuffer + 1 {
		h.publishLocked("status", auth.RoleViewer, h.status)
	}
	h.mu.Unlock()
	n := 0
	for range sub.C {
		n++
	}
	if n != subBuffer || h.Subscribers() != 0 {
		t.Fatalf("got %d events, %d subscribers", n, h.Subscribers())
	}
}

func TestHistory(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.set("pathsList", path("a", 1000, 0, 2))
	f.set("rtspSessionsList", map[string]any{"id": "1"}, map[string]any{"id": "2"})
	f.set("rtspConnsList", map[string]any{"id": "3"})
	h := newHub(t, f)
	t0 := time.Unix(1_800_000_000, 0)
	h.Poll(ctx)
	h.sample(t0)
	f.set("pathsList", path("a", 11_000, 5000, 2), path("new", 999_999, 0, 0))
	h.Poll(ctx)
	h.sample(t0.Add(5 * time.Second))
	f.set("pathsList", path("a", 500, 0, 2)) // recreated: the counter went down
	h.Poll(ctx)
	h.sample(t0.Add(10 * time.Second))

	got := h.History()
	if len(got) != 3 || got[0].InBps != nil || got[0].Readers != 2 || got[0].Clients != 2 || got[0].Online != 1 {
		t.Fatalf("first sample %+v", got[0])
	}
	if *got[1].InBps != 10_000*8/5 || *got[1].OutBps != 5000*8/5 || got[1].Paths != 2 {
		t.Errorf("second sample in %v out %v paths %d", *got[1].InBps, *got[1].OutBps, got[1].Paths)
	}
	if *got[2].InBps != 500*8/5 {
		t.Errorf("after a reset: %v", *got[2].InBps)
	}
	if r := h.rates; r == nil || len(r.Paths) != 1 || r.Paths["a"].InBps != 500*8/5 || r.T != got[2].T {
		t.Errorf("rates %+v", r)
	}
	if ph := h.PathHistory("a"); len(ph) != 2 || ph[0].InBps != 10_000*8/5 || ph[0].Readers != 2 || ph[1].T != got[2].T {
		t.Errorf("path history of a: %+v", ph)
	}
	if ph := h.PathHistory("nobody"); len(ph) != 0 {
		t.Errorf("history of an unknown path: %+v", ph)
	}

	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	h.Poll(ctx)
	h.sample(t0.Add(15 * time.Second))
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	h.Poll(ctx)
	h.sample(t0.Add(20 * time.Second))
	if h.rates != nil {
		t.Error("rates survived an outage")
	}
	got = h.History()
	if len(got) != 4 || got[3].InBps != nil {
		t.Errorf("after an outage: %d samples, last %+v", len(got), got[len(got)-1])
	}

	hi := newHistory(3)
	for i := range 5 {
		hi.add(Sample{T: int64(i)})
		hi.addPath("p", PathSample{T: int64(i)})
	}
	if all := hi.all(); len(all) != 3 || all[0].T != 2 || all[2].T != 4 {
		t.Errorf("ring %+v", all)
	}
	if ps := hi.paths["p"]; len(ps) != 3 || ps[0].T != 2 {
		t.Errorf("path ring %+v", ps)
	}
	hi.prunePaths(5)
	if len(hi.paths) != 0 {
		t.Errorf("a path without recent points survived: %v", hi.paths)
	}
}

func TestRunClosesSubscriptions(t *testing.T) {
	f := newFake()
	h := newHub(t, f)
	h.active, h.idle = 10*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	sub, _ := h.Subscribe(auth.RoleViewer, "")
	f.set("pathsList", path("a", 0, 0, 0))
	for {
		ev := next(t, sub)
		if ev.Type == "update" && decode[update](t, ev).Kind == "paths" && len(decode[update](t, ev).Upsert) == 1 {
			break
		}
	}
	cancel()
	<-done
	for range sub.C {
		// drain until closed
	}
	if _, err := h.Subscribe(auth.RoleViewer, ""); err == nil {
		t.Error("a stopped hub accepted a subscription")
	}
}

// The fields the hub drops must be exactly the ones the vendored spec marks deprecated, per list item schema.
func TestStripListMatchesSpec(t *testing.T) {
	b, err := os.ReadFile("../../../spec/mediamtx/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Deprecated bool `yaml:"deprecated"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	schema := map[Kind]string{
		"info": "Info", "paths": "Path", "rtspConns": "RTSPConn", "rtspSessions": "RTSPSession", "rtspsConns": "RTSPConn",
		"rtspsSessions": "RTSPSession", "rtmpConns": "RTMPConn", "rtmpsConns": "RTMPConn", "srtConns": "SRTConn",
		"webrtcSessions": "WebRTCSession", "hlsMuxers": "HLSMuxer", "hlsSessions": "HLSSession", "moqSessions": "MoQSession",
	}
	for _, s := range sources {
		sc, ok := spec.Components.Schemas[schema[s.kind]]
		if !ok {
			t.Errorf("%s: no schema %q", s.kind, schema[s.kind])
			continue
		}
		var want []string
		for name, p := range sc.Properties {
			if p.Deprecated {
				want = append(want, name)
			}
		}
		got := slices.Clone(s.strip)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s drops %v, the spec deprecates %v", s.kind, got, want)
		}
		if _, ok := sc.Properties[s.key]; s.key != "" && !ok {
			t.Errorf("%s: key field %q is not in %s", s.kind, s.key, schema[s.kind])
		}
	}
	if len(sources) != len(schema) {
		t.Errorf("%d sources, %d schemas", len(sources), len(schema))
	}
}

// Against the real MediaMTX: every list answers, a protocol that is off is reported unavailable rather than as an
// outage, and a published stream appears in the paths list.
func TestWithMediaMTX(t *testing.T) {
	mtxtest.Bin(t)
	allow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer allow.Close()
	api, rtsp := mtxtest.FreePort(t), mtxtest.FreePort(t)
	mtxtest.Start(t, fmt.Sprintf("authMethod: http\nauthHTTPAddress: %s/internal/auth\nauthHTTPExclude: []\n"+
		"api: yes\napiAddress: 127.0.0.1:%d\nrtsp: yes\nrtspAddress: 127.0.0.1:%d\nrtspTransports: [tcp]\n"+
		"rtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\npaths:\n  all_others:\n", allow.URL, api, rtsp), api)
	ops, _ := mtxapi.Operations()
	h, err := New(fmt.Sprintf("http://127.0.0.1:%d", api), principal{}, ops, quiet())
	if err != nil {
		t.Fatal(err)
	}
	h.Poll(context.Background())
	if st := h.Status(); !st.Reachable {
		t.Fatalf("status %+v", st)
	}
	for _, s := range sources {
		l, ok := h.lists[s.kind]
		if !ok {
			t.Errorf("%s: not polled", s.kind)
			continue
		}
		wantAvailable := s.kind == "info" || s.kind == "paths" || strings.HasPrefix(string(s.kind), "rtsp")
		if strings.HasPrefix(string(s.kind), "rtsps") {
			wantAvailable = false
		}
		if l.available != wantAvailable {
			t.Errorf("%s: available %v, want %v", s.kind, l.available, wantAvailable)
		}
	}
	if !strings.Contains(string(h.lists["info"].value), `"version":"v1.21.1"`) {
		t.Errorf("info %s", h.lists["info"].value)
	}
}

func TestExtrasFollowTheirRole(t *testing.T) {
	h := newHub(t, newFake())
	admin, _ := h.Subscribe(auth.RoleAdmin, "")
	defer h.Unsubscribe(admin)
	viewer, _ := h.Subscribe(auth.RoleViewer, "")
	defer h.Unsubscribe(viewer)

	h.SetExtra("exposure", auth.RoleAdmin, map[string]int{"rev": 1})
	if e := decode[ExtraEvent](t, next(t, admin)); e.Kind != "exposure" || string(e.Value) != `{"rev":1}` {
		t.Fatalf("admin got %+v", e)
	}
	noEvent(t, viewer)
	h.SetExtra("exposure", auth.RoleAdmin, map[string]int{"rev": 1}) // unchanged: nothing sent
	noEvent(t, admin)

	later, _ := h.Subscribe(auth.RoleAdmin, "")
	defer h.Unsubscribe(later)
	if snap := decode[snapshot](t, later.Initial[0]); string(snap.Extras["exposure"]) != `{"rev":1}` {
		t.Fatalf("snapshot extras %v", snap.Extras)
	}
	v2, _ := h.Subscribe(auth.RoleViewer, "")
	defer h.Unsubscribe(v2)
	if snap := decode[snapshot](t, v2.Initial[0]); len(snap.Extras) != 0 {
		t.Fatalf("a viewer's snapshot has %v", snap.Extras)
	}
}

// A streamer's subscription sees its own paths and their rates, MediaMTX's status, and nothing else.
func TestScopedSubscription(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.set("pathsList", path("live/alice", 0, 0, 1), path("live/bob", 0, 0, 3))
	f.set("rtspSessionsList", map[string]any{"id": "1", "path": "live/bob"})
	h := newHub(t, f)
	h.Poll(ctx)
	t0 := time.Unix(1_800_000_000, 0)
	h.sample(t0)
	mine := func(p string) bool { return p == "live/alice" }
	sub, err := h.SubscribeScoped(mine)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Unsubscribe(sub)
	snap := decode[snapshot](t, sub.Initial[0])
	if len(snap.Lists) != 1 || len(snap.Lists["paths"].Items) != 1 || !strings.Contains(string(snap.Lists["paths"].Items[0]), "live/alice") {
		t.Fatalf("snapshot %+v", snap)
	}
	if len(snap.Extras) != 0 {
		t.Errorf("extras in a scoped snapshot: %v", snap.Extras)
	}

	f.set("pathsList", path("live/alice", 1000, 0, 2), path("live/bob", 5000, 0, 4))
	f.set("rtspSessionsList", map[string]any{"id": "2", "path": "live/bob"})
	h.Poll(ctx)
	u := decode[update](t, next(t, sub))
	if u.Kind != "paths" || len(u.Upsert) != 1 || !strings.Contains(string(u.Upsert[0]), "live/alice") {
		t.Fatalf("update %+v", u)
	}
	noEvent(t, sub) // not bob's change, not the RTSP sessions

	h.sample(t0.Add(5 * time.Second))
	r := decode[Rates](t, next(t, sub))
	if len(r.Paths) != 1 || r.Paths["live/alice"].InBps != 1000*8/5 {
		t.Fatalf("rates %+v", r)
	}
	noEvent(t, sub) // no totals sample
	h.SetExtra("exposure", auth.RoleViewer, map[string]int{"rev": 1})
	noEvent(t, sub)

	f.set("pathsList", path("live/bob", 5000, 0, 4)) // alice's path goes away
	h.Poll(ctx)
	if u := decode[update](t, next(t, sub)); len(u.Remove) != 1 || u.Remove[0] != "live/alice" {
		t.Fatalf("removal %+v", u)
	}
	f.set("pathsList", path("live/bob", 5000, 0, 5)) // only bob changes
	h.Poll(ctx)
	noEvent(t, sub)
}

func TestSummary(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.set("pathsList", path("a", 0, 0, 1), path("idle", 0, 0, 0))
	h := newHub(t, f)
	t0 := time.Unix(1_800_000_000, 0)
	if _, _, ok := h.Summary(t0, t0.Add(time.Minute)); ok {
		t.Fatal("a summary without samples")
	}
	h.Poll(ctx)
	h.sample(t0) // no rates yet: left out
	f.set("pathsList", path("a", 10_000, 0, 3), path("idle", 0, 0, 0))
	h.Poll(ctx)
	h.sample(t0.Add(5 * time.Second)) // a: 16 kbit/s in, 3 readers
	f.set("pathsList", path("a", 10_000, 0, 1), path("idle", 0, 0, 0), path("b", 0, 2500, 0))
	h.Poll(ctx)
	h.sample(t0.Add(10 * time.Second)) // a: nothing; b is new (no rate yet)
	f.set("pathsList", path("a", 10_000, 0, 1), path("idle", 0, 0, 0), path("b", 0, 5000, 0))
	h.Poll(ctx)
	h.sample(t0.Add(time.Minute)) // the next minute

	s, paths, ok := h.Summary(t0, t0.Add(time.Minute))
	if !ok || s.T != t0.UnixMilli() || *s.InBps != 16_000/2 || *s.OutBps != 0 || s.Readers != 3 || s.Paths != 3 || s.Online != 3 {
		t.Fatalf("summary %+v in %v", s, *s.InBps)
	}
	if len(paths) != 1 || paths["a"].InBps != 16_000/2 || paths["a"].Readers != 3 {
		t.Fatalf("paths %+v", paths)
	}
	if _, next, _ := h.Summary(t0.Add(time.Minute), t0.Add(2*time.Minute)); next["b"].OutBps != 2500*8/50 {
		t.Fatalf("next minute: %+v", next)
	}
}
