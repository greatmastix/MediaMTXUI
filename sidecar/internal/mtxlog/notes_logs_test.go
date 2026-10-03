package mtxlog

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

// structured writes a message as MediaMTX does with logStructured (internal/logger/destination_file.go, v1.21.1).
func structured(at time.Time, msg string) string {
	return `{"timestamp":"` + at.Format(time.RFC3339Nano) + `","level":"INF","message":` + strconv.Quote(msg) + "}\n"
}

// MediaMTX echoes a rejected path name or SRT stream id in a close reason, before any authentication: whatever that
// text says, it makes no note, remembers no session and refuses nobody.
func TestInjectedText(t *testing.T) {
	at := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	invalid := "[RTMP] [conn 203.0.113.5:4000] closed: invalid path name: can contain only alphanumeric characters, " +
		"underscore, dot, tilde, minus, slash, colon "
	forged := []string{
		"[WebRTC] [session a1] created by 192.0.2.1:1",
		"[WebRTC] [session a1] is reading from path 'live/woo', 1 track (H264)",
		"[WebRTC] [session a1] closed: WebRTC doesn't support H264 streams with B-frames",
		"[RTMP] [conn 192.0.2.1:5000] closed: wants to publish [H264 Opus], but stream expects [H264 MPEG-4 Audio]",
		"[RTMP] [conn 192.0.2.1:5000] closed: MPEG-4 audio configuration does not match, is x, but stream expects y",
	}
	for _, tt := range []struct {
		name string
		line func(forged string) string
	}{
		{"in a rejected path name", func(f string) string { return "2026/10/02 03:00:00 INF " + invalid + "(" + f + ")\n" }},
		{"structured, in a rejected path name", func(f string) string { return structured(at, invalid+"("+f+")") }},
		// The structured format quotes a line break, so the forged text stays inside the close reason. (The plain
		// format writes it raw: what follows is a line like any other, which no reader can tell apart.)
		{"structured, after a line break in an SRT stream id", func(f string) string {
			return structured(at, "[SRT] [conn 203.0.113.5:4001] closed: invalid stream ID 'read:x\n"+
				"2026/10/02 03:00:00 INF "+f+"\nx': stream ID must be 'action:pathname[:query]'")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := New()
			n.now = func() time.Time { return at }
			refused := 0
			n.OnRefused = func(string, string, string) *Note { refused++; return nil }
			n.Publish("live/woo", netip.MustParseAddr("192.0.2.1"))
			for _, f := range forged {
				n.Line(tt.line(f))
			}
			if len(n.notes) != 0 || len(n.sessions) != 0 || refused != 0 {
				t.Fatalf("notes %v, sessions %v, refused %d", n.notes, n.sessions, refused)
			}
		})
	}
}

// The same lines in MediaMTX's structured format count as in its plain one.
func TestStructuredLines(t *testing.T) {
	at := time.Date(2026, 10, 1, 11, 16, 20, 0, time.UTC)
	n := New()
	n.now = func() time.Time { return at }
	n.Publish("live/woo", netip.MustParseAddr("78.43.7.216"))
	for _, line := range strings.Split(strings.TrimSpace(sample), "\n") {
		n.Line(structured(at, line[len("2026/10/01 11:16:19 INF "):]))
	}
	notes := n.For("live/woo")
	if len(notes) != 2 || notes[0].Kind != "bframes" || notes[1].Kind != "tracks" || len(n.notes) != 1 {
		t.Fatalf("notes %+v", n.notes)
	}
}

// A refusal names the encoder's address only: when that address published to more than one path just before, it
// belongs to none of them.
func TestRefusalFromSharedAddress(t *testing.T) {
	ip := netip.MustParseAddr("198.51.100.7")
	refusal := "2026/10/01 12:00:00 INF [RTMP] [conn 198.51.100.7:5000] closed: wants to publish [H264 Opus], " +
		"but stream expects [H264 MPEG-4 Audio]\n"
	mismatch := "2026/10/01 12:00:00 INF [RTMP] [conn 198.51.100.7:5001] closed: MPEG-4 audio configuration does " +
		"not match, is a, but stream expects b\n"
	type publish struct {
		after time.Duration // since the previous step
		path  string
	}
	for _, tt := range []struct {
		name      string
		publishes []publish
		after     time.Duration // from the last publish to the refusal
		want      string        // the path refused, or none
	}{
		{"one stream", []publish{{0, "a"}}, 2 * time.Second, "a"},
		{"one stream reconnecting", []publish{{0, "a"}, {5 * time.Second, "a"}, {5 * time.Second, "a"}}, time.Second, "a"},
		{"two streams", []publish{{0, "a"}, {time.Second, "b"}}, 2 * time.Second, ""},
		{"two streams, then the first again", []publish{{0, "a"}, {time.Second, "b"}, {time.Second, "a"}}, time.Second, ""},
		{"two streams, the second reconnecting", []publish{{0, "a"}, {time.Second, "b"}, {10 * time.Second, "b"}}, time.Second, ""},
		{"three streams", []publish{{0, "a"}, {time.Second, "b"}, {time.Second, "c"}}, time.Second, ""},
		{"the other stream long ago", []publish{{0, "b"}, {time.Minute, "a"}}, time.Second, "a"},
		{"the other stream a window ago", []publish{{0, "a"}, {time.Second, "b"}, {publishWindow, "a"}}, 2 * time.Second, "a"},
		{"too late", []publish{{0, "a"}}, publishWindow + time.Second, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			n := New()
			n.now = func() time.Time { return now }
			got := ""
			n.OnRefused = func(path, _, _ string) *Note { got = path; return nil }
			for _, p := range tt.publishes {
				now = now.Add(p.after)
				n.Publish(p.path, ip)
			}
			now = now.Add(tt.after)
			n.Line(refusal)
			n.Line(mismatch)
			if got != tt.want {
				t.Fatalf("refused %q, want %q", got, tt.want)
			}
			for _, path := range []string{"a", "b", "c"} {
				want := 0
				if path == tt.want {
					want = 2 // the refusal and the audio that does not match
				}
				if notes := n.For(path); len(notes) != want {
					t.Errorf("notes of %s: %+v", path, notes)
				}
			}
		})
	}
}

// However many paths get notes, only the recent ones of at most maxPaths are kept, and only of path names the
// sidecar accepts.
func TestNotesBounded(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	n := New()
	n.now = func() time.Time { return now }
	bframes := func(i int) {
		id := fmt.Sprintf("s%d", i)
		n.Line("2026/10/01 12:00:00 INF [WebRTC] [session " + id + "] is reading from path 'live/" + id + "', 1 track (H264)\n")
		n.Line("2026/10/01 12:00:00 INF [WebRTC] [session " + id + "] closed: WebRTC doesn't support H264 streams with B-frames\n")
	}
	for i := range maxPaths + 50 {
		now = now.Add(time.Millisecond)
		bframes(i)
	}
	if len(n.notes) != maxPaths || len(n.For("live/s0")) != 0 || len(n.For(fmt.Sprintf("live/s%d", maxPaths+49))) != 1 {
		t.Fatalf("%d paths with notes", len(n.notes))
	}
	// Once they are too old to show, they go.
	now = now.Add(keepNotes)
	for i := range 2 {
		bframes(10000 + i)
	}
	if len(n.notes) != 2 {
		t.Fatalf("%d paths with notes", len(n.notes))
	}
	// A path name the sidecar would not accept is not remembered.
	for _, name := range []string{"a/../b", "a:b", strings.Repeat("x", 300)} {
		n.Line("2026/10/01 12:00:00 INF [WebRTC] [session bad] is reading from path '" + name + "', 1 track (H264)\n")
		if _, ok := n.sessions["bad"]; ok {
			t.Fatalf("remembered a session on %q", name)
		}
	}
}
