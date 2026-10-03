package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// layoutRecordings stands in for MediaMTX's recordings with segment files wherever their recordPath puts them: it
// lists the segments it was given whose files are still there, and deletes a segment's file.
type layoutRecordings struct {
	t       *testing.T
	root    string
	mu      sync.Mutex
	files   map[string]map[string]string // path name → start (RFC 3339) → file, relative to root
	deleted []string                     // path@start
}

func newLayoutRecordings(t *testing.T, h *harness) *layoutRecordings {
	f := &layoutRecordings{t: t, root: filepath.Join(h.srv.d.Settings.DataDir, "recordings"), files: map[string]map[string]string{}}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routes["/v3/recordings/list"] = func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var items []string
		for name, segs := range f.files {
			var starts []string
			for start, file := range segs {
				if _, err := os.Stat(filepath.Join(f.root, file)); err == nil {
					starts = append(starts, fmt.Sprintf(`{"start":%q}`, start))
				}
			}
			if len(starts) > 0 {
				items = append(items, fmt.Sprintf(`{"name":%q,"segments":[%s]}`, name, strings.Join(starts, ",")))
			}
		}
		_, _ = io.WriteString(w, `{"pageCount":1,"items":[`+strings.Join(items, ",")+`]}`)
	}
	h.routes["/v3/recordings/segments/delete"] = func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		name, raw := r.URL.Query().Get("path"), r.URL.Query().Get("start")
		start, err := time.Parse(time.RFC3339Nano, raw)
		file, ok := f.files[name][start.UTC().Format(time.RFC3339Nano)]
		if err != nil || !ok || os.Remove(filepath.Join(f.root, file)) != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.deleted = append(f.deleted, name+"@"+start.UTC().Format("15:04"))
	}
	return f
}

// add writes a segment's file; name "" writes a file MediaMTX does not list.
func (f *layoutRecordings) add(name, file string, start time.Time, size int) {
	f.t.Helper()
	p := filepath.Join(f.root, file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if name == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files[name] == nil {
		f.files[name] = map[string]string{}
	}
	f.files[name][start.UTC().Format(time.RFC3339Nano)] = file
}

func (f *layoutRecordings) pruned() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := append([]string(nil), f.deleted...)
	sort.Strings(d)
	return strings.Join(d, " ")
}

// The budget deletes what the overage needs, each segment counted at its own file's size whatever the recordPath, and
// nothing when even every segment MediaMTX lists would not be enough.
func TestRecordingsBudgetBySize(t *testing.T) {
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	at := func(minute int) time.Time { return base.Add(time.Duration(minute) * time.Minute) }
	archive := "/recordings/archive/%path/%Y-%m-%d_%H-%M-%S-%f"
	for _, c := range []struct {
		name       string
		recordPath string // live/cam's own, or "" for the seed's
		unlisted   int    // bytes in a file MediaMTX does not list
		budget     int64  // 0: none
		minFree    int64  // 0: none
		pruned     string // what goes
		listed     int64  // live/cam's bytes on the recordings page, before pruning
		banner     string // in the recordings_budget banner, or "" for none
	}{
		{name: "the seed's recordPath", budget: 5500, pruned: "live/cam@10:00", listed: 6000},
		{name: "a recordPath of its own", recordPath: archive, budget: 5500, pruned: "live/cam@10:00", listed: 6000},
		{name: "just enough", recordPath: archive, budget: 3500, pruned: "live/cam@10:00 live/cam@10:01 live/cam@10:02", listed: 6000},
		{
			name: "the newest hold the rest", budget: 500, listed: 6000,
			pruned: "live/cam@10:00 live/cam@10:01 live/cam@10:02 live/cam@10:03 live/cam@10:04",
		},
		{name: "files MediaMTX does not list count", unlisted: 2000, budget: 6000, pruned: "live/cam@10:00 live/cam@10:01", listed: 6000},
		{
			name: "files MediaMTX does not list take the budget", unlisted: 7000, budget: 4000, listed: 6000,
			banner: "of segment files on the volume are not in MediaMTX's list",
		},
		{name: "something else fills the disk", minFree: 1 << 62, listed: 6000, banner: "Something other than recordings fills the disk."},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _ := recordingsHarness(t, nil)
			f := newLayoutRecordings(t, h)
			dir := "live/cam/"
			if c.recordPath != "" {
				if rec := h.do("PUT", "/api/v1/config/paths/live/cam", map[string]any{"config": map[string]any{"recordPath": c.recordPath}}); rec.Code != http.StatusOK {
					t.Fatalf("recordPath: %d %s", rec.Code, rec.Body)
				}
				dir = "archive/live/cam/"
			}
			for i := range 6 {
				f.add("live/cam", dir+segName(at(i)), at(i), 1000)
			}
			if c.unlisted > 0 {
				f.add("", "old-layout/live/cam/"+segName(at(-60)), at(-60), c.unlisted)
			}
			h.srv.d.Settings.RecordingsMaxBytes, h.srv.d.Settings.RecordingsMinFreeBytes = c.budget, c.minFree
			h.srv.recordingsOnce(context.Background())
			if got := f.pruned(); got != c.pruned {
				t.Errorf("pruned %q, want %q", got, c.pruned)
			}

			var list struct {
				Paths []RecordingPath `json:"paths"`
			}
			h.json(h.do("GET", "/api/v1/recordings", nil), &list)
			if len(list.Paths) != 1 || list.Paths[0].Bytes != c.listed-int64(1000*len(strings.Fields(c.pruned))) {
				t.Errorf("listed %+v", list.Paths)
			}
			status := h.do("GET", "/api/v1/status", nil).Body.String()
			if c.banner == "" && strings.Contains(status, "recordings_budget") || c.banner != "" && !strings.Contains(status, c.banner) {
				t.Errorf("banner %q in %s", c.banner, status)
			}
			if c.banner == "" {
				return
			}
			// Once the budget can be met again, the banner goes.
			h.srv.d.Settings.RecordingsMaxBytes, h.srv.d.Settings.RecordingsMinFreeBytes = 0, 0
			h.srv.recordingsOnce(context.Background())
			if status := h.do("GET", "/api/v1/status", nil).Body.String(); strings.Contains(status, "recordings_budget") {
				t.Errorf("banner stays: %s", status)
			}
		})
	}
}

// Recording switched back on while the guard is on (by a stream's manager, or in the config) goes off again at the
// next check that finds the disk critically full; the release switches back on everything the guard switched off.
func TestRecordingsGuardTripsAgain(t *testing.T) {
	ctx := context.Background()
	h, _ := recordingsHarness(t, nil)
	h.srv.d.Settings.RecordingsCriticalBytes = 1 << 62 // any free space is critical
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/rec"}), &st)
	recordOn := func() {
		t.Helper()
		if rec := h.do("PATCH", "/api/v1/streams/"+itoa(st.ID), map[string]any{"record": true}); rec.Code != http.StatusOK {
			t.Fatalf("record on: %d %s", rec.Code, rec.Body)
		}
	}
	tripped := func(paths, now string) {
		t.Helper()
		h.srv.recordingsOnce(ctx)
		if g := h.srv.guard(ctx); g == nil || fmt.Sprint(g.Paths) != paths || strings.Contains(h.mtxFile(), "record: yes") {
			t.Fatalf("guard %+v, want %s\n%s", g, paths, h.mtxFile())
		}
		if action, _, details := lastAudit(t, h); action != "recordings.guard" || fmt.Sprint(details["paths"]) != now {
			t.Errorf("audit %s %v, want paths %s", action, details, now)
		}
	}

	recordOn()
	tripped("[live/rec]", "[live/rec]")
	if rec := h.do("PUT", "/api/v1/config/paths/other", map[string]any{"config": map[string]any{"record": true}}); rec.Code != http.StatusOK {
		t.Fatalf("record on: %d %s", rec.Code, rec.Body)
	}
	tripped("[live/rec other]", "[other]")
	recordOn() // the stream's manager, while the guard is on
	tripped("[live/rec other]", "[live/rec]")

	file := h.mtxFile()
	h.srv.recordingsOnce(ctx) // nothing records: nothing to do
	if h.mtxFile() != file {
		t.Errorf("a check with nothing recording wrote the config:\n%s", h.mtxFile())
	}
	h.srv.d.Settings.RecordingsCriticalBytes = 0
	if rec := h.do("POST", "/api/v1/recordings/guard/release", nil); rec.Code != http.StatusOK ||
		!strings.Contains(h.mtxFile(), "  live/rec:\n    record: yes\n") || !strings.Contains(h.mtxFile(), "  other:\n    record: yes\n") {
		t.Errorf("release: %d %s\n%s", rec.Code, rec.Body, h.mtxFile())
	}
}

// exportRequest is a raw export request with the harness's session.
func exportRequest(h *harness, host, query string) string {
	return fmt.Sprintf("GET /api/v1/recordings/export?%s HTTP/1.1\r\nHost: %s\r\nX-Forwarded-For: %s\r\nCookie: __Host-mtxui_session=%s\r\n\r\n",
		query, host, client, h.cookie)
}

// An export gives its slot back by twice its range plus exportSlack, however slowly its client reads; the connection
// it used serves later requests as before.
func TestRecordingExportDeadline(t *testing.T) {
	old := exportSlack
	exportSlack = 300 * time.Millisecond
	t.Cleanup(func() { exportSlack = old })
	h, _ := recordingsHarness(t, nil)
	endless := make(chan struct{})
	h.mu.Lock()
	h.routes["/get"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("duration") != "0.2" {
			_, _ = io.WriteString(w, "ftyp-and-the-rest-of-an-mp4")
			return
		}
		chunk := make([]byte, 64<<10) // far more than the client takes
		for {
			select {
			case <-endless:
				return
			default:
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}
	h.mu.Unlock()
	t.Cleanup(func() { close(endless) })
	srv := httptest.NewServer(h.public)
	t.Cleanup(srv.Close)
	host := srv.Listener.Addr().String()

	// A client that reads nothing.
	idle, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	_, _ = io.WriteString(idle, exportRequest(h, host, "path=live/cam&start=2026-10-01T10:00:10Z&duration=0.2&format=fmp4"))
	slots := h.srv.exportSlots()
	for start := time.Now(); len(slots) == 0; {
		if time.Since(start) > 5*time.Second {
			t.Fatal("the export never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for start := time.Now(); len(slots) > 0; {
		if time.Since(start) > 10*time.Second {
			t.Fatal("a client that reads nothing still holds its export slot")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A whole export, then another request on the same connection after the export's deadline (net/http lifts it).
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	_, _ = io.WriteString(conn, exportRequest(h, host, "path=live/cam&start=2026-10-01T10:00:10Z&duration=0.01&format=fmp4"))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ftyp-and-the-rest-of-an-mp4" {
		t.Fatalf("export: %d %q", resp.StatusCode, body)
	}
	time.Sleep(2*10*time.Millisecond + exportSlack + 100*time.Millisecond) // past the export's deadline
	_, _ = io.WriteString(conn, strings.Replace(exportRequest(h, host, ""), "/recordings/export?", "/session", 1))
	next, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("the next request on the connection: %v", err)
	}
	_ = next.Body.Close()
	if next.StatusCode != http.StatusOK {
		t.Errorf("the next request on the connection: %d", next.StatusCode)
	}
}
