package logs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// structured writes a line as MediaMTX does with logStructured (internal/logger/destination_file.go, v1.21.1).
func structured(at time.Time, level, msg string) string {
	return `{"timestamp":"` + at.Format(time.RFC3339Nano) + `","level":"` + level + `","message":` + strconv.Quote(msg) + "}"
}

func TestParseMediaMTXFormats(t *testing.T) {
	at := time.Date(2026, 10, 1, 11, 16, 19, 512_345_678, time.UTC)
	ms, sec := at.UnixMilli(), at.Truncate(time.Second).UnixMilli()
	for _, tt := range []struct {
		name string
		line string
		want Line
	}{
		{
			"structured", structured(at, "WAR", "[RTMP] [conn 1.2.3.4:5] closed: bad") + "\n",
			Line{T: ms, Source: MediaMTX, Level: "warn", Text: "[RTMP] [conn 1.2.3.4:5] closed: bad"},
		},
		{"structured, quotes", structured(at, "ERR", `path "a\b"`), Line{T: ms, Source: MediaMTX, Level: "error", Text: `path "a\b"`}},
		// A client's line break (an SRT stream id MediaMTX echoes) stays inside its line, shown escaped.
		{
			"structured, a line break", structured(at, "INF", "closed: invalid stream ID 'x\n2026/10/01 11:16:19 ERR forged'"),
			Line{T: ms, Source: MediaMTX, Level: "info", Text: `closed: invalid stream ID 'x\n2026/10/01 11:16:19 ERR forged'`},
		},
		// strconv.Quote, not JSON: "\x1b" is no JSON escape.
		{
			"structured, an escape sequence", structured(at, "INF", "name \x1b[2J\u202eä"),
			Line{T: ms, Source: MediaMTX, Level: "info", Text: `name \x1b[2J\u202eä`},
		},
		{
			"structured, cut short", `{"timestamp":"2026-10-01T11:16:19Z","level":"INF","message":"abc`,
			Line{Source: MediaMTX, Level: "info", Text: `{"timestamp":"2026-10-01T11:16:19Z","level":"INF","message":"abc`},
		},
		{
			"structured, a bad time", `{"timestamp":"yesterday","level":"INF","message":"abc"}`,
			Line{Source: MediaMTX, Level: "info", Text: `{"timestamp":"yesterday","level":"INF","message":"abc"}`},
		},
		// The plain format writes control characters raw: they are shown escaped, so they cannot change how the
		// line looks (a carriage return, a terminal escape, a right-to-left override).
		{
			"plain, control characters", "2026/10/01 11:16:19 INF closed: invalid path name (a\rb\x1b[1A\x00\u202e)\r\n",
			Line{T: sec, Source: MediaMTX, Level: "info", Text: `closed: invalid path name (a\rb\x1b[1A\x00\u202e)`},
		},
		{
			"plain, printable", "2026/10/01 11:16:19 ERR [RTSP] [conn [::1]:5] closed: Größe 'x'",
			Line{T: sec, Source: MediaMTX, Level: "error", Text: "[RTSP] [conn [::1]:5] closed: Größe 'x'"},
		},
		{"other shape, a tab", "a\tb", Line{Source: MediaMTX, Level: "info", Text: `a\tb`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseMediaMTX(tt.line); got != tt.want {
				t.Fatalf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
	// The cut comes after the escaping, so it bounds what the viewer gets.
	if l := ParseMediaMTX(structured(at, "INF", strings.Repeat("\x00", maxText))); len(l.Text) != maxText || l.T != ms {
		t.Fatalf("long structured line: %d bytes, %+v", len(l.Text), l.T)
	}
}

// A log written partly before and partly after a switch to the structured format is searched as one.
func TestSearchStructured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	at := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	content := "2026/10/01 10:59:59 INF [RTSP] plain\n" +
		structured(at, "ERR", "[SRT] [conn 1.2.3.4:5] closed: invalid stream ID 'x\n2026/10/01 11:00:00 ERR forged'") + "\n" +
		structured(at.Add(time.Second), "INF", "[RTSP] structured") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, _, err := Search(context.Background(), path, 0, Filter{}, 100)
	if err != nil || len(lines) != 3 || lines[1].Level != "error" || lines[2].Text != "[RTSP] structured" {
		t.Fatalf("%+v %v", lines, err)
	}
	errs, _, _ := Search(context.Background(), path, 0, Filter{MinLevel: "error", Query: "forged"}, 100)
	if len(errs) != 1 || errs[0].T != at.UnixMilli() {
		t.Fatalf("errors %+v", errs)
	}
}

// The sidecar's own lines quote a value that would not print as it is (a user name from an authentication
// request, chosen by whoever connects), so it stays on its line and reads as one value.
func TestTeeQuotesControlCharacters(t *testing.T) {
	hub := NewHub()
	log := slog.New(Tee(slog.NewTextHandler(io.Discard, nil), hub))
	log.Info("auth: denied", "who", "x\nforged", "tab", "a\tb", "rtl", "a\u202eb", "plain", "live/a", "word", "Größe")
	r := hub.Recent(Filter{}, 1)
	want := `auth: denied who="x\nforged" tab="a\tb" rtl="a\u202eb" plain=live/a word=Größe`
	if len(r) != 1 || r[0].Text != want {
		t.Fatalf("got %+v\nwant %s", r, want)
	}
	log.With("from", "a b\r").Warn("message\nwith a line break")
	if r := hub.Recent(Filter{}, 1); len(r) != 1 || r[0].Text != `message\nwith a line break from="a b\r"` {
		t.Fatalf("got %+v", r)
	}
}
