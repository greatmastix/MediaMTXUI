// Package mtxlog follows MediaMTX's log file for what the stream pages should explain to whoever streams: an encoder
// MediaMTX refused because its tracks do not match the holding clip, and an encoder whose B-frames browsers cannot
// play over WebRTC. MediaMTX reports both only in its log. The notes are kept in memory for a while, per path.
//
// MediaMTX writes client-chosen text into its log, before any authentication: a rejected path name or SRT stream id
// in a close reason. So a message counts only whole, by how it begins, which MediaMTX writes itself. That text can
// hold a line break, though, which MediaMTX's plain format writes raw: what follows then reads as a line of its own
// and can pass for any line here, a refusal included, which can switch a holding version. Only the structured format
// (logStructured) quotes it; see logs.ParseMediaMTX.
package mtxlog

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"mtxui/internal/logs"
	"mtxui/internal/pathname"
)

// Note is something about a stream's encoder, for people.
type Note struct {
	Kind    string    `json:"kind"` // "tracks" (refused) or "bframes"
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Timing.
const (
	keepNotes     = 15 * time.Minute // how long a note stays on the page
	publishWindow = 30 * time.Second // a refusal this soon after a publish from the same address belongs to it
	maxNotes      = 5                // per path
	maxPaths      = 1000             // paths with notes; beyond, the one noted longest ago is forgotten
	maxSessions   = 2000             // WebRTC sessions remembered (path and address)
)

// The messages read (logs.ParseMediaMTX took off time and level), matched whole: MediaMTX begins each with the
// server and the connection, and client-chosen text comes only later.
var (
	reReading = regexp.MustCompile(`^\[WebRTC\] \[session (\w+)\] is (?:reading from|publishing to) path '([^']+)'(?:, .*)?$`)
	reCreated = regexp.MustCompile(`^\[WebRTC\] \[session (\w+)\] created by (\S+)$`)
	reBFrames = regexp.MustCompile(`^\[WebRTC\] \[session (\w+)\] closed: WebRTC doesn't support H264 streams with B-frames$`)
	reRefused = regexp.MustCompile(`^\[(?:RTMP|RTMPS|SRT|RTSP|RTSPS|WebRTC)\] \[(?:conn|session) ([^\]]+)\] closed: ` +
		`(wants to publish \[([^\]]*)\], but stream expects \[([^\]]*)\]|(?:MPEG-4 audio|G711|LPCM) configuration does not match, .*)$`)
)

// Notes collects the notes.
type Notes struct {
	mu        sync.Mutex
	notes     map[string][]Note        // path -> newest last
	published map[netip.Addr]published // address -> its latest publish
	sessions  map[string]session       // WebRTC session -> what is known of it
	order     []string                 // sessions, oldest first, to forget them
	now       func() time.Time
	// OnRefused, when set, hears of an encoder MediaMTX refused for its tracks: the path and the tracks it sends and
	// the path expects, as MediaMTX lists them ("H264 MPEG-4 Audio"). It returns a note to show instead of the
	// refusal (it acted on it), or nil. It is called without the lock held; set it once, before Follow.
	OnRefused func(path, sends, expects string) *Note
	// OnLine, when set, hears every line Follow reads (the log viewer's live tail). Set it once, before Follow.
	OnLine func(line string)
}

type published struct {
	path  string
	at    time.Time
	other time.Time // when the address last published to another path
}

type session struct {
	path string
	ip   netip.Addr
}

// New returns an empty collection.
func New() *Notes {
	return &Notes{
		notes: map[string][]Note{}, published: map[netip.Addr]published{}, sessions: map[string]session{}, now: time.Now,
	}
}

// Publish records that ip was let in to publish to path (from the authentication hook).
func (n *Notes) Publish(path string, ip netip.Addr) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.published[ip]
	if p.path != path {
		p.other = p.at
	}
	p.path, p.at = path, n.now()
	n.published[ip] = p
	if len(n.published) > 1000 {
		for k, v := range n.published {
			if n.now().Sub(v.at) > publishWindow {
				delete(n.published, k)
			}
		}
	}
}

// Add records a note for a path.
func (n *Notes) Add(path string, note Note) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if note.At.IsZero() {
		note.At = n.now()
	}
	n.add(path, note)
}

// For returns the recent notes of a path, newest first.
func (n *Notes) For(path string) []Note {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := []Note{}
	for _, note := range n.notes[path] {
		if n.now().Sub(note.At) < keepNotes {
			out = append([]Note{note}, out...)
		}
	}
	return out
}

func (n *Notes) add(path string, note Note) {
	list := n.notes[path]
	// The same note again (every viewer's session closes alike) only moves to the end with the new time.
	for i, old := range list {
		if old.Kind == note.Kind && old.Message == note.Message {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	list = append(list, note)
	if len(list) > maxNotes {
		list = list[len(list)-maxNotes:]
	}
	n.notes[path] = list
	if len(n.notes) > maxPaths {
		n.forget()
	}
}

// forget drops the paths whose notes are all too old to show, then, while there are too many, the one noted
// longest ago.
func (n *Notes) forget() {
	now := n.now()
	newest := func(path string) time.Time {
		list := n.notes[path]
		return list[len(list)-1].At
	}
	for path := range n.notes {
		if now.Sub(newest(path)) >= keepNotes {
			delete(n.notes, path)
		}
	}
	for len(n.notes) > maxPaths {
		oldest := ""
		for path := range n.notes {
			if oldest == "" || newest(path).Before(newest(oldest)) {
				oldest = path
			}
		}
		delete(n.notes, oldest)
	}
}

func (n *Notes) remember(id string, update func(*session)) {
	s, known := n.sessions[id]
	update(&s)
	n.sessions[id] = s
	if !known {
		n.order = append(n.order, id)
		if len(n.order) > maxSessions {
			delete(n.sessions, n.order[0])
			n.order = n.order[1:]
		}
	}
}

func addrOf(hostport string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	ip, err := netip.ParseAddr(host)
	return ip.Unmap(), err == nil
}

// Line reads one line of MediaMTX's log, plain or structured.
func (n *Notes) Line(line string) {
	l := logs.ParseMediaMTX(line)
	if l.T == 0 {
		return // not in MediaMTX's shape
	}
	if path, sends, expects, ok := n.message(l.Text); ok && n.OnRefused != nil {
		if note := n.OnRefused(path, sends, expects); note != nil {
			n.Add(path, *note)
			return
		}
		n.Add(path, refusedNote(sends, expects))
	} else if ok {
		n.Add(path, refusedNote(sends, expects))
	}
}

func refusedNote(sends, expects string) Note {
	return Note{Kind: "tracks", Message: "MediaMTX refused your encoder: it sends " + tracks(sends) +
		", but the holding screen expects " + tracks(expects) +
		". Set what your encoder sends under Holding screen, or switch the holding screen off."}
}

// message reads one message; a refusal for tracks that do not match is returned for Line to note (or hand on). What
// it keeps is copied out of the message, which would otherwise stay in memory whole.
func (n *Notes) message(msg string) (path, sends, expects string, refused bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if m := reCreated.FindStringSubmatch(msg); m != nil {
		if ip, ok := addrOf(m[2]); ok {
			n.remember(strings.Clone(m[1]), func(s *session) { s.ip = ip })
		}
		return "", "", "", false
	}
	if m := reReading.FindStringSubmatch(msg); m != nil {
		if pathname.Valid(m[2]) == nil { // the only names a stream page asks for
			n.remember(strings.Clone(m[1]), func(s *session) { s.path = strings.Clone(m[2]) })
		}
		return "", "", "", false
	}
	if m := reBFrames.FindStringSubmatch(msg); m != nil {
		if s := n.sessions[m[1]]; s.path != "" {
			n.add(s.path, Note{Kind: "bframes", At: now, Message: "Your encoder sends B-frames, which browsers cannot play " +
				"over WebRTC, so the page and the watch link fall back to HLS, a few seconds behind. In OBS, set B-frames " +
				"(\"Max B-frames\" for NVENC) to 0."})
		}
		return "", "", "", false
	}
	if m := reRefused.FindStringSubmatch(msg); m != nil {
		ip, ok := addrOf(m[1])
		if !ok {
			ip = n.sessions[m[1]].ip
		}
		// The log names the address only: the refusal belongs to the path published from it just before, unless it
		// published to another path as well (one encoder sending two streams, or a NAT). Acting on the wrong one
		// would switch its holding version, so neither hears of it.
		p, found := n.published[ip]
		if !found || now.Sub(p.at) > publishWindow || now.Sub(p.other) <= publishWindow {
			return "", "", "", false
		}
		if m[3] != "" {
			return p.path, m[3], m[4], true
		}
		n.add(p.path, Note{Kind: "tracks", At: now, Message: "MediaMTX refused your encoder: " + m[2] + ". Your " +
			"encoder's audio has to be what the holding clip has (AAC-LC 48 kHz stereo, as OBS sends by default)."})
	}
	return "", "", "", false
}

// tracks turns MediaMTX's track list ("H264 MPEG-4 Audio") into words.
func tracks(s string) string {
	s = strings.ReplaceAll(s, "MPEG-4 Audio", "AAC")
	return strings.Join(strings.Fields(s), " + ")
}

// Follow reads the log file as MediaMTX appends to it, from its current end, until ctx ends. A file that shrinks
// (rotated or truncated) is read again from the start; a missing file is waited for.
func (n *Notes) Follow(ctx context.Context, path string, every time.Duration) {
	var f *os.File
	var r *bufio.Reader
	var offset int64
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	t := time.NewTicker(every)
	defer t.Stop()
	first := true
	for {
		if f == nil {
			if g, err := os.Open(path); err == nil {
				f = g
				if first {
					offset, _ = f.Seek(0, io.SeekEnd)
				}
				r = bufio.NewReader(f)
			}
			first = false
		}
		if f != nil {
			if st, err := os.Stat(path); err != nil || st.Size() < offset {
				_ = f.Close()
				f, offset = nil, 0
				continue
			}
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					if errors.Is(err, io.EOF) && line != "" {
						// A partial line: read it again whole next time.
						_, _ = f.Seek(offset, io.SeekStart)
						r.Reset(f)
					}
					break
				}
				offset += int64(len(line))
				if n.OnLine != nil {
					n.OnLine(line)
				}
				n.Line(line)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
