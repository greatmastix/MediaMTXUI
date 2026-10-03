package recdisk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"mtxui/internal/mtxtest"
)

// listed reads a start the way the sidecar reads MediaMTX's JSON: in the time zone MediaMTX wrote it in.
func listed(t *testing.T, raw string) time.Time {
	t.Helper()
	var start time.Time
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &start); err != nil {
		t.Fatal(err)
	}
	return start
}

// A segment's file is named the way MediaMTX names it, whatever the recordPath: the budget must know what deleting
// one frees, not guess it from a directory named after the path.
func TestSegmentFile(t *testing.T) {
	utc := listed(t, "2026-10-01T12:00:00.123456Z")
	berlin := listed(t, "2026-10-01T14:00:00.123456+02:00") // MediaMTX running in another time zone
	seed := DefaultRecordPath
	for _, c := range []struct {
		recordPath string
		start      time.Time
		want       string // "": not on the recordings volume
	}{
		{seed + ".mp4", utc, "live/cam/2026-10-01_12-00-00-123456.mp4"},
		{seed + ".ts", utc, "live/cam/2026-10-01_12-00-00-123456.ts"},
		{seed + ".mp4", berlin, "live/cam/2026-10-01_14-00-00-123456.mp4"},
		{"/recordings/archive/%path/%Y-%m-%d_%H-%M-%S-%f.mp4", utc, "archive/live/cam/2026-10-01_12-00-00-123456.mp4"},
		{"/recordings/%path/%Y/%m/%d/%H-%M-%S.mp4", utc, "live/cam/2026/10/01/12-00-00.mp4"},
		{"/recordings/%path_%Y-%m-%d_%H-%M-%S.mp4", utc, "live/cam_2026-10-01_12-00-00.mp4"},
		{"/recordings/%path/%s.mp4", utc, "live/cam/1790856000.mp4"},
		{"/recordings/%path/%Y-%m-%dT%H-%M-%S%z.mp4", berlin, "live/cam/2026-10-01T14-00-00+0200.mp4"},
		{"/recordings/%path/%Y-%m-%dT%H-%M-%S%z.mp4", utc, "live/cam/2026-10-01T12-00-00Z.mp4"},
		{"/recordings//%path/./%Y.mp4", utc, "live/cam/2026.mp4"},
		{"/elsewhere/%path/%Y.mp4", utc, ""},
		{"/recordings-old/%path/%Y.mp4", utc, ""},
		{"/recordings/%Y/../../%path.mp4", utc, ""}, // the rules refuse "..", and so does this
	} {
		got, ok := segmentFile(Mount, c.recordPath, "live/cam", c.start)
		if got != c.want && c.want != "" || ok != (c.want != "") {
			t.Errorf("%s at %s: %q %v, want %q", c.recordPath, c.start, got, ok, c.want)
		}
	}
}

// Each listed segment gets its own file's size, whichever recordPath its path uses; files MediaMTX does not list are
// unlisted, and a file counts for one segment only.
func TestSizes(t *testing.T) {
	at := func(minute int) time.Time { return time.Date(2026, 10, 1, 12, minute, 0, 0, time.UTC) }
	u := Usage{Files: map[string]int64{
		"archive/live/cam/2026-10-01_12-00-00-000000.mp4": 1000,
		"archive/live/cam/2026-10-01_12-01-00-000000.mp4": 2000,
		"other/2026-10-01_12-00-00-000000.ts":             300,  // a path that records MPEG-TS
		"live/cam/2026-10-01_11-00-00-000000.mp4":         5000, // under an earlier recordPath: MediaMTX does not list it
	}, Bytes: 8300}
	segs := []Segment{
		{"live/cam", at(0)},
		{"live/cam", at(1)},
		{"live/cam", at(2)}, // listed, but not (or no longer) on the volume
		{"other", at(0)},
		{"other", at(0)}, // listed twice: the file is the first one's
	}
	sizes, unlisted := u.Sizes([]string{DefaultRecordPath, "/recordings/archive/%path/%Y-%m-%d_%H-%M-%S-%f"}, segs)
	if want := []int64{1000, 2000, -1, 300, -1}; fmt.Sprint(sizes) != fmt.Sprint(want) {
		t.Errorf("sizes %v, want %v", sizes, want)
	}
	if unlisted != 5000 {
		t.Errorf("unlisted %d, want 5000", unlisted)
	}
	if _, unlisted := u.Sizes(nil, segs); unlisted != u.Bytes {
		t.Errorf("with no recordPath, unlisted %d", unlisted)
	}
}

// Against the real MediaMTX: every file it lists has a start that segmentFile turns back into that file's name, for
// recordPaths of every shape the rules allow.
func TestSegmentFileWithMediaMTX(t *testing.T) {
	mtxtest.Bin(t)
	root := t.TempDir()
	micro := listed(t, "2026-10-01T12:00:00.123456Z")
	whole := listed(t, "2026-10-01T12:00:00Z")
	paths := []struct {
		name, recordPath string
		start            time.Time
	}{
		{"live/cam", root + "/%path/%Y-%m-%d_%H-%M-%S-%f", micro}, // the seed's
		{"archived", root + "/archive/%path/%Y/%m/%d/%H-%M-%S-%f", micro},
		{"flat", root + "/%path_%s", whole},
		{"zoned", root + "/%path/%Y-%m-%dT%H-%M-%S%z", whole},
	}
	api := mtxtest.FreePort(t)
	conf := fmt.Sprintf("api: yes\napiAddress: 127.0.0.1:%d\nauthMethod: internal\nauthInternalUsers:\n  - user: any\n    permissions:\n"+
		"      - action: api\nrtsp: no\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\nplayback: no\npaths:\n", api)
	want := map[string][]string{}
	for _, p := range paths {
		conf += fmt.Sprintf("  %s:\n    recordPath: %q\n    recordDeleteAfter: 0s\n", p.name, p.recordPath) // keep the old files
		for i := range 3 {
			file, ok := segmentFile(root, p.recordPath+".mp4", p.name, p.start.Add(time.Duration(i)*time.Minute))
			if !ok {
				t.Fatalf("%s: no file for %s", p.name, p.start)
			}
			if err := os.MkdirAll(filepath.Join(root, filepath.Dir(file)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, file), []byte("segment"), 0o600); err != nil {
				t.Fatal(err)
			}
			want[p.name] = append(want[p.name], file)
		}
	}
	mtxtest.Start(t, conf, api)

	client := &http.Client{Timeout: 5 * time.Second}
	for _, p := range paths {
		parts := strings.Split(p.name, "/")
		for i, part := range parts {
			parts[i] = url.PathEscape(part)
		}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/v3/recordings/get/%s", api, strings.Join(parts, "/")))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var rec struct {
			Segments []struct {
				Start time.Time `json:"start"`
			} `json:"segments"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &rec) != nil {
			t.Fatalf("%s: %d %s", p.name, resp.StatusCode, body)
		}
		var got []string
		for _, sg := range rec.Segments {
			file, _ := segmentFile(root, p.recordPath+".mp4", p.name, sg.Start)
			got = append(got, file)
		}
		sort.Strings(got)
		sort.Strings(want[p.name])
		if fmt.Sprint(got) != fmt.Sprint(want[p.name]) {
			t.Errorf("%s: MediaMTX lists %s, which name %v; the files are %v", p.name, body, got, want[p.name])
		}
	}
}
