// Package logs is the sidecar's side of the log viewer: MediaMTX's log file, searched together with
// its rotated copies and rotated by copy-truncate, and the sidecar's own recent log lines, kept in memory.
// A hub hands new lines of both to the viewer's live tails.
package logs

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// Sources.
const (
	MediaMTX = "mediamtx"
	Sidecar  = "sidecar"
)

// Line is one log line, as the viewer shows it.
type Line struct {
	T      int64  `json:"t"` // Unix milliseconds; 0 when the line carries no time
	Source string `json:"source"`
	Level  string `json:"level"` // debug, info, warn or error
	Text   string `json:"text"`
}

// maxText caps a line's text; MediaMTX's lines are short, so only garbage is cut.
const maxText = 4096

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// Level reports whether s is a known level name.
func Level(s string) bool {
	_, ok := levelRank[s]
	return ok
}

// Filter selects lines: one source, a minimum level (empty: all) and a case-insensitive substring (empty: all).
type Filter struct {
	Source   string
	MinLevel string
	Query    string
}

// Match reports whether l passes the filter.
func (f Filter) Match(l Line) bool {
	if f.Source != "" && l.Source != f.Source {
		return false
	}
	if f.MinLevel != "" && levelRank[l.Level] < levelRank[f.MinLevel] {
		return false
	}
	return f.Query == "" || strings.Contains(strings.ToLower(l.Text), strings.ToLower(f.Query))
}

var reMediaMTX = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) (DEB|INF|WAR|ERR) (.*)$`)

var mtxLevels = map[string]string{"DEB": "debug", "INF": "info", "WAR": "warn", "ERR": "error"}

// ParseMediaMTX reads one line of MediaMTX's log ("2026/10/01 11:16:19 INF [RTMP] ..."). MediaMTX writes the
// container's local time, which is UTC. A line in another shape is kept whole, as info.
func ParseMediaMTX(s string) Line {
	s = strings.TrimRight(s, "\r\n")
	if len(s) > maxText {
		s = s[:maxText]
	}
	l := Line{Source: MediaMTX, Level: "info", Text: s}
	if m := reMediaMTX.FindStringSubmatch(s); m != nil {
		if t, err := time.ParseInLocation("2006/01/02 15:04:05", m[1], time.UTC); err == nil {
			l.T = t.UnixMilli()
		}
		l.Level, l.Text = mtxLevels[m[2]], m[3]
	}
	return l
}

// Hub fans new lines out to live tails and keeps the sidecar's own recent lines (they exist nowhere else: the
// sidecar logs to stdout, which only Docker keeps).
type Hub struct {
	mu     sync.Mutex
	recent []Line // the sidecar's own lines, oldest first
	subs   map[*Tail]struct{}
}

// Limits.
const (
	keepRecent = 5000 // the sidecar's own lines kept
	tailBuffer = 512  // lines a tail may fall behind before it is dropped (its browser reconnects)
	maxTails   = 32
)

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[*Tail]struct{}{}} }

// Tail is one live tail: the lines its filter passes, as they come.
type Tail struct {
	C      <-chan Line
	c      chan Line
	filter Filter
}

// Publish hands a new line to the tails that want it; a tail too far behind is closed.
func (h *Hub) Publish(l Line) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l.Source == Sidecar {
		h.recent = append(h.recent, l)
		if len(h.recent) > keepRecent+keepRecent/10 {
			h.recent = append([]Line(nil), h.recent[len(h.recent)-keepRecent:]...)
		}
	}
	for t := range h.subs {
		if !t.filter.Match(l) {
			continue
		}
		select {
		case t.c <- l:
		default:
			close(t.c)
			delete(h.subs, t)
		}
	}
}

// Subscribe starts a tail, or returns nil when there are too many.
func (h *Hub) Subscribe(f Filter) *Tail {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= maxTails {
		return nil
	}
	c := make(chan Line, tailBuffer)
	t := &Tail{C: c, c: c, filter: f}
	h.subs[t] = struct{}{}
	return t
}

// Unsubscribe ends a tail.
func (h *Hub) Unsubscribe(t *Tail) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[t]; ok {
		close(t.c)
		delete(h.subs, t)
	}
}

// Recent returns the newest of the sidecar's own lines that pass f, at most limit, oldest first.
func (h *Hub) Recent(f Filter, limit int) []Line {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []Line{}
	for i := len(h.recent) - 1; i >= 0 && len(out) < limit; i-- {
		if f.Match(h.recent[i]) {
			out = append(out, h.recent[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
