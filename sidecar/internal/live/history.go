package live

import (
	"encoding/json"
	"time"

	"mtxui/internal/auth"
)

// Sample is one point of the dashboard's charts.
type Sample struct {
	T       int64    `json:"t"`      // Unix milliseconds
	InBps   *float64 `json:"inBps"`  // bits per second into MediaMTX (publishers, sources); null right after (re)start
	OutBps  *float64 `json:"outBps"` // bits per second out to readers
	Paths   int      `json:"paths"`
	Online  int      `json:"online"`
	Readers int      `json:"readers"`
	Clients int      `json:"clients"` // sessions and connections of every protocol, except RTSP connections and HLS muxers
}

// Rates are each path's bitrates over the last sample interval.
type Rates struct {
	T     int64               `json:"t"`
	Paths map[string]PathRate `json:"paths"`
}

// PathRate is one path's bitrates in bits per second.
type PathRate struct {
	InBps  float64 `json:"inBps"`
	OutBps float64 `json:"outBps"`
}

type counters struct{ in, out uint64 }

// PathSample is one point of a single path's charts (stream pages).
type PathSample struct {
	T       int64   `json:"t"`
	InBps   float64 `json:"inBps"`
	OutBps  float64 `json:"outBps"`
	Readers int     `json:"readers"`
}

// maxPathHistories bounds the per-path histories kept: beyond it, paths without one get none.
const maxPathHistories = 1000

type history struct {
	buf    []Sample
	next   int
	full   bool
	prev   map[string]counters // per path, at prevAt
	prevAt time.Time
	paths  map[string][]PathSample // per path, oldest first, at most len(buf) each
}

func newHistory(size int) *history {
	return &history{buf: make([]Sample, size), paths: map[string][]PathSample{}}
}

// addPath appends a path's point, keeping at most as many as the global history.
func (hi *history) addPath(name string, p PathSample) {
	ps, ok := hi.paths[name]
	if !ok && len(hi.paths) >= maxPathHistories {
		return
	}
	ps = append(ps, p)
	if len(ps) > len(hi.buf) {
		ps = ps[len(ps)-len(hi.buf):]
	}
	hi.paths[name] = ps
}

// prunePaths drops the histories of paths without a point since cut.
func (hi *history) prunePaths(cut int64) {
	for n, ps := range hi.paths {
		if ps[len(ps)-1].T < cut {
			delete(hi.paths, n)
		}
	}
}

func (hi *history) add(s Sample) {
	hi.buf[hi.next] = s
	hi.next = (hi.next + 1) % len(hi.buf)
	if hi.next == 0 {
		hi.full = true
	}
}

func (hi *history) all() []Sample {
	out := make([]Sample, 0, len(hi.buf))
	if hi.full {
		out = append(out, hi.buf[hi.next:]...)
	}
	return append(out, hi.buf[:hi.next]...)
}

// History returns the samples of the last hour, oldest first.
func (h *Hub) History() []Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.history.all()
}

// PathHistory returns one path's samples of the last hour, oldest first.
func (h *Hub) PathHistory(name string) []PathSample {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]PathSample(nil), h.history.paths[name]...)
}

// sample records one history point from the latest poll and publishes it. Rates are the counter increase since
// the previous sample, over paths present in both; a counter that went down (a path that was recreated) counts
// from zero. While MediaMTX is unreachable nothing is recorded, which shows as a gap.
func (h *Hub) sample(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hi := h.history
	if !h.status.Reachable {
		hi.prev = nil
		h.rates = nil
		return
	}
	s := Sample{T: now.UnixMilli()}
	cur := map[string]counters{}
	readers := map[string]int{}
	if paths, ok := h.lists["paths"]; ok {
		for name, raw := range paths.items {
			var p struct {
				InboundBytes  uint64            `json:"inboundBytes"`
				OutboundBytes uint64            `json:"outboundBytes"`
				Online        bool              `json:"online"`
				Readers       []json.RawMessage `json:"readers"`
			}
			if json.Unmarshal(raw, &p) != nil {
				continue
			}
			cur[name] = counters{p.InboundBytes, p.OutboundBytes}
			readers[name] = len(p.Readers)
			s.Paths++
			s.Readers += len(p.Readers)
			if p.Online {
				s.Online++
			}
		}
	}
	for _, src := range h.sources {
		if l, ok := h.lists[src.kind]; ok && src.client {
			s.Clients += len(l.items)
		}
	}
	var rates *Rates
	if dt := now.Sub(hi.prevAt).Seconds(); hi.prev != nil && dt > 0 {
		rates = &Rates{T: s.T, Paths: map[string]PathRate{}}
		var in, out uint64
		for name, c := range cur {
			p, ok := hi.prev[name]
			if !ok {
				continue
			}
			pin, pout := increase(p.in, c.in), increase(p.out, c.out)
			in += pin
			out += pout
			rates.Paths[name] = PathRate{float64(pin) * 8 / dt, float64(pout) * 8 / dt}
			hi.addPath(name, PathSample{T: s.T, InBps: float64(pin) * 8 / dt, OutBps: float64(pout) * 8 / dt, Readers: readers[name]})
		}
		hi.prunePaths(s.T - (time.Duration(len(hi.buf)) * SampleInterval).Milliseconds())
		inBps, outBps := float64(in)*8/dt, float64(out)*8/dt
		s.InBps, s.OutBps = &inBps, &outBps
	}
	hi.prev, hi.prevAt = cur, now
	hi.add(s)
	h.publishLocked("sample", auth.RoleViewer, s)
	if rates != nil {
		h.rates = rates
		h.publishLocked("rates", auth.RoleViewer, rates)
	}
}

func increase(prev, cur uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// PathMinute is one path's summary of a stretch of samples: mean bitrates, most readers at once.
type PathMinute struct {
	InBps   float64
	OutBps  float64
	Readers int
}

// Summary sums up the samples taken in [from, to) for the long-term history: mean bitrates, and
// the most paths, online paths, readers and clients seen at once; per path the same, for paths that had traffic or
// readers. ok is false when there was no sample with rates (MediaMTX unreachable, or the sidecar just started).
func (h *Hub) Summary(from, to time.Time) (s Sample, paths map[string]PathMinute, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lo, hi := from.UnixMilli(), to.UnixMilli()
	var in, out float64
	n := 0
	for _, x := range h.history.all() {
		if x.T < lo || x.T >= hi || x.InBps == nil || x.OutBps == nil {
			continue
		}
		n++
		in += *x.InBps
		out += *x.OutBps
		s.Paths, s.Online = max(s.Paths, x.Paths), max(s.Online, x.Online)
		s.Readers, s.Clients = max(s.Readers, x.Readers), max(s.Clients, x.Clients)
	}
	if n == 0 {
		return Sample{}, nil, false
	}
	in, out = in/float64(n), out/float64(n)
	s.T, s.InBps, s.OutBps = lo, &in, &out
	paths = map[string]PathMinute{}
	for name, ps := range h.history.paths {
		var pm PathMinute
		k := 0
		for _, p := range ps {
			if p.T < lo || p.T >= hi {
				continue
			}
			k++
			pm.InBps += p.InBps
			pm.OutBps += p.OutBps
			pm.Readers = max(pm.Readers, p.Readers)
		}
		if k == 0 {
			continue
		}
		pm.InBps, pm.OutBps = pm.InBps/float64(n), pm.OutBps/float64(n) // a path absent from some samples had nothing then
		if pm.InBps > 0 || pm.OutBps > 0 || pm.Readers > 0 {
			paths[name] = pm
		}
	}
	return s, paths, true
}
