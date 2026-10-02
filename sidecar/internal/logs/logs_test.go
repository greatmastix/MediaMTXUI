package logs

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMediaMTX(t *testing.T) {
	l := ParseMediaMTX("2026/10/01 11:16:19 WAR [RTMP] [conn 1.2.3.4:5] closed: bad\n")
	want := Line{T: time.Date(2026, 10, 1, 11, 16, 19, 0, time.UTC).UnixMilli(), Source: MediaMTX, Level: "warn", Text: "[RTMP] [conn 1.2.3.4:5] closed: bad"}
	if l != want {
		t.Fatalf("got %+v", l)
	}
	if l := ParseMediaMTX("something else"); l.Level != "info" || l.T != 0 || l.Text != "something else" {
		t.Fatalf("other shape: %+v", l)
	}
	if l := ParseMediaMTX(strings.Repeat("x", 10000)); len(l.Text) != maxText {
		t.Fatalf("long line kept %d bytes", len(l.Text))
	}
}

func TestFilter(t *testing.T) {
	l := Line{Source: MediaMTX, Level: "warn", Text: "[RTMP] Closed"}
	for _, tt := range []struct {
		f    Filter
		want bool
	}{
		{Filter{}, true},
		{Filter{Source: Sidecar}, false},
		{Filter{MinLevel: "info"}, true},
		{Filter{MinLevel: "warn"}, true},
		{Filter{MinLevel: "error"}, false},
		{Filter{Query: "rtmp] cl"}, true},
		{Filter{Query: "srt"}, false},
	} {
		if got := tt.f.Match(l); got != tt.want {
			t.Errorf("%+v: %v", tt.f, got)
		}
	}
}

func writeLines(t *testing.T, path string, from, to int, gz bool) {
	t.Helper()
	var b bytes.Buffer
	for i := from; i < to; i++ {
		lvl := "INF"
		if i%10 == 0 {
			lvl = "ERR"
		}
		fmt.Fprintf(&b, "2026/10/01 11:%02d:%02d %s line %d\n", i/60%60, i%60, lvl, i)
	}
	data := b.Bytes()
	if gz {
		var z bytes.Buffer
		w := gzip.NewWriter(&z)
		_, _ = w.Write(data)
		_ = w.Close()
		data = z.Bytes()
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Search reads the rotated copies oldest first, then the live file, and keeps only the newest matches.
func TestSearch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mediamtx.log")
	writeLines(t, rotated(path, 2), 0, 100, true)
	writeLines(t, rotated(path, 1), 100, 200, true)
	writeLines(t, path, 200, 250, false)

	all, more, err := Search(context.Background(), path, 5, Filter{}, 1000)
	if err != nil || more || len(all) != 250 || all[0].Text != "line 0" || all[249].Text != "line 249" {
		t.Fatalf("all: %d lines, more %v, err %v", len(all), more, err)
	}
	errs, more, _ := Search(context.Background(), path, 5, Filter{MinLevel: "error"}, 3)
	if !more || len(errs) != 3 || errs[0].Text != "line 220" || errs[2].Text != "line 240" {
		t.Fatalf("errors: %+v more %v", errs, more)
	}
	// keep 1: the second copy is not read.
	if few, _, _ := Search(context.Background(), path, 1, Filter{}, 1000); len(few) != 150 {
		t.Fatalf("keep 1: %d lines", len(few))
	}
	// A missing log is no error.
	if none, _, err := Search(context.Background(), filepath.Join(dir, "nope.log"), 5, Filter{}, 10); err != nil || len(none) != 0 {
		t.Fatalf("missing: %v %v", none, err)
	}
	// A corrupt copy is.
	if err := os.WriteFile(rotated(path, 3), []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Search(context.Background(), path, 5, Filter{}, 10); err == nil {
		t.Fatal("a corrupt copy was read")
	}
}

func TestEachLineCutsLongLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 200000)+"\nshort\nlast"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []int
	if err := eachLine(path, func(s string) error { got = append(got, len(s)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] > maxText+64<<10 || got[1] != 5 || got[2] != 4 {
		t.Fatalf("lengths %v", got)
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mediamtx.log")
	if ok, err := Rotate(path, 10, 3); ok || err != nil {
		t.Fatalf("missing file: %v %v", ok, err)
	}
	for i := range 5 {
		writeLines(t, path, i*100, i*100+100, false)
		if ok, err := Rotate(path, 100, 3); !ok || err != nil {
			t.Fatalf("rotation %d: %v %v", i, ok, err)
		}
		if st, _ := os.Stat(path); st.Size() != 0 {
			t.Fatalf("not truncated: %d", st.Size())
		}
	}
	if _, err := os.Stat(rotated(path, 4)); err == nil {
		t.Fatal("more copies than keep")
	}
	lines, _, err := Search(context.Background(), path, 3, Filter{}, 1000)
	if err != nil || len(lines) != 300 || lines[0].Text != "line 200" || lines[299].Text != "line 499" {
		t.Fatalf("after rotations: %d lines, first %v, err %v", len(lines), lines[0], err)
	}
	// Small enough: left alone.
	writeLines(t, path, 0, 1, false)
	if ok, _ := Rotate(path, 1000, 3); ok {
		t.Fatal("rotated a small file")
	}
	// keep 0 drops the content.
	writeLines(t, path, 0, 100, false)
	if ok, err := Rotate(path, 10, 0); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Fatal("not truncated with keep 0")
	}
}

// A file kept open for appending (as MediaMTX does) continues at the start after the truncate, without a hole.
func TestRotateWhileAppending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.WriteString(strings.Repeat("2026/10/01 11:00:00 INF before\n", 100))
	if ok, err := Rotate(path, 100, 2); !ok || err != nil {
		t.Fatal(ok, err)
	}
	_, _ = f.WriteString("2026/10/01 11:00:01 INF after\n")
	data, _ := os.ReadFile(path)
	if string(data) != "2026/10/01 11:00:01 INF after\n" {
		t.Fatalf("after rotation: %q", data)
	}
}

func TestHub(t *testing.T) {
	h := NewHub()
	errs := h.Subscribe(Filter{Source: MediaMTX, MinLevel: "error"})
	h.Publish(Line{Source: MediaMTX, Level: "info", Text: "a"})
	h.Publish(Line{Source: MediaMTX, Level: "error", Text: "b"})
	h.Publish(Line{Source: Sidecar, Level: "error", Text: "c"})
	if l := <-errs.C; l.Text != "b" {
		t.Fatalf("got %v", l)
	}
	select {
	case l := <-errs.C:
		t.Fatalf("unexpected %v", l)
	default:
	}
	if r := h.Recent(Filter{}, 10); len(r) != 1 || r[0].Text != "c" {
		t.Fatalf("recent %v", r)
	}
	// A tail that does not read is dropped (closed), not waited for.
	for range tailBuffer + 1 {
		h.Publish(Line{Source: MediaMTX, Level: "error"})
	}
	n := 0
	for range errs.C {
		n++
	}
	if n != tailBuffer {
		t.Fatalf("read %d before close", n)
	}
	h.Unsubscribe(errs) // after the drop: no double close
	// The limit.
	for range maxTails {
		if h.Subscribe(Filter{}) == nil {
			t.Fatal("refused under the limit")
		}
	}
	if h.Subscribe(Filter{}) != nil {
		t.Fatal("more tails than the limit")
	}
	// Recent keeps the newest, oldest first.
	h2 := NewHub()
	for i := range keepRecent * 2 {
		h2.Publish(Line{Source: Sidecar, Level: "info", Text: fmt.Sprint(i)})
	}
	r := h2.Recent(Filter{}, 3)
	if len(r) != 3 || r[2].Text != fmt.Sprint(keepRecent*2-1) {
		t.Fatalf("recent %v", r)
	}
}

func TestTee(t *testing.T) {
	h := NewHub()
	var out bytes.Buffer
	log := slog.New(Tee(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}), h))
	log.With("component", "x").WithGroup("req").Info("request", "path", "/a b", "status", 200)
	log.Debug("hidden")
	log.Error("bad", "err", "boom")
	r := h.Recent(Filter{}, 10)
	if len(r) != 2 || r[0].Text != `request component=x req.path="/a b" req.status=200` || r[1].Level != "error" ||
		r[1].Text != "bad err=boom" {
		t.Fatalf("recent %+v", r)
	}
	if !strings.Contains(out.String(), `"msg":"request"`) || strings.Contains(out.String(), "hidden") {
		t.Fatalf("inner handler: %s", out.String())
	}
}
