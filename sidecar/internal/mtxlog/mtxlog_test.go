package mtxlog

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Lines as MediaMTX v1.21.1 writes them (from the deployment's log, 2026-10-01).
const sample = `2026/10/01 11:16:19 INF [RTMP] [conn 78.43.7.216:24989] opened
2026/10/01 11:16:22 INF [RTMP] [conn 78.43.7.216:24989] closed: wants to publish [H264 MPEG-4 Audio], but stream expects [Opus H264]
2026/10/01 11:22:24 INF [WebRTC] [session 4927e5a7] created by 78.43.7.216:32714
2026/10/01 11:22:25 INF [WebRTC] [session 4927e5a7] is reading from path 'live/woo', 1 track (H264)
2026/10/01 11:22:25 INF [WebRTC] [session 4927e5a7] closed: WebRTC doesn't support H264 streams with B-frames
2026/10/01 11:22:25 INF [WebRTC] [session 101ed75a] created by 78.43.7.216:16905
2026/10/01 11:22:25 INF [WebRTC] [session 101ed75a] is reading from path 'live/woo', 1 track (H264)
2026/10/01 11:22:25 INF [WebRTC] [session 101ed75a] closed: WebRTC doesn't support H264 streams with B-frames
2026/10/01 11:23:40 INF [RTMP] [conn 198.51.100.9:4000] closed: wants to publish [H264], but stream expects [H264 MPEG-4 Audio]
`

func TestLines(t *testing.T) {
	n := New()
	now := time.Date(2026, 10, 1, 11, 16, 20, 0, time.UTC)
	n.now = func() time.Time { return now }
	n.Publish("live/woo", netip.MustParseAddr("78.43.7.216"))
	for _, line := range strings.Split(sample, "\n") {
		n.Line(line)
	}
	notes := n.For("live/woo")
	if len(notes) != 2 {
		t.Fatalf("notes %+v", notes)
	}
	if notes[0].Kind != "bframes" || !strings.Contains(notes[0].Message, "B-frames") {
		t.Errorf("newest %+v", notes[0])
	}
	if notes[1].Kind != "tracks" || !strings.Contains(notes[1].Message, "it sends H264 + AAC, but the holding screen expects Opus + H264") {
		t.Errorf("refusal %+v", notes[1])
	}
	// A refusal from an address that did not just publish belongs to nobody.
	if len(n.notes) != 1 {
		t.Errorf("paths with notes: %v", n.notes)
	}
	now = now.Add(keepNotes)
	if notes := n.For("live/woo"); len(notes) != 0 {
		t.Errorf("old notes kept: %+v", notes)
	}
}

func TestFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil { // already there: not read again
		t.Fatal(err)
	}
	n := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Follow(ctx, path, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	n.Publish("cam", netip.MustParseAddr("192.0.2.1"))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("2026/10/01 12:00:00 INF [SRT] [conn 192.0.2.1:5000] closed: wants to publish [H264 Opus], ")
	time.Sleep(50 * time.Millisecond) // half a line: not yet
	_, _ = f.WriteString("but stream expects [H264 MPEG-4 Audio]\n")
	f.Close()
	deadline := time.Now().Add(2 * time.Second)
	for len(n.For("cam")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the appended line was not read")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(n.For("live/woo")) != 0 {
		t.Error("the file's old content was read")
	}
	// Truncated (rotated): read from the start again.
	_ = os.WriteFile(path, []byte("2026/10/01 12:01:00 INF [WebRTC] [session abc] created by 192.0.2.1:1\n"+
		"2026/10/01 12:01:00 INF [WebRTC] [session abc] is reading from path 'cam', 1 track (H264)\n"+
		"2026/10/01 12:01:00 INF [WebRTC] [session abc] closed: WebRTC doesn't support H264 streams with B-frames\n"), 0o600)
	deadline = time.Now().Add(2 * time.Second)
	for len(n.For("cam")) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("after truncation: %+v", n.For("cam"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
