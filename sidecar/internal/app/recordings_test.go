package app

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
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

// fakeRecordings stands in for MediaMTX's recordings: segment files under dir/recordings/<path>/, listed by the
// Control API, deleted through it (the file goes), spans and exports from the playback server.
type fakeRecordings struct {
	t       *testing.T
	root    string
	mu      sync.Mutex
	deleted []string // path@start
}

func segName(t time.Time) string { return t.UTC().Format("2006-01-02_15-04-05-000000") + ".mp4" }

func (f *fakeRecordings) add(path string, start time.Time, size int) {
	f.t.Helper()
	dir := filepath.Join(f.root, path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, segName(start)), make([]byte, size), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// listing returns MediaMTX's recordings list: every path directory and its segments' starts.
func (f *fakeRecordings) listing() string {
	var items []string
	dirs, _ := filepath.Glob(filepath.Join(f.root, "*"))
	sub, _ := filepath.Glob(filepath.Join(f.root, "*", "*"))
	for _, d := range append(dirs, sub...) {
		files, _ := filepath.Glob(filepath.Join(d, "*.mp4"))
		if len(files) == 0 {
			continue
		}
		sort.Strings(files)
		var segs []string
		for _, file := range files {
			start, _ := time.Parse("2006-01-02_15-04-05-000000", strings.TrimSuffix(filepath.Base(file), ".mp4"))
			segs = append(segs, fmt.Sprintf(`{"start":%q}`, start.Format(time.RFC3339Nano)))
		}
		rel, _ := filepath.Rel(f.root, d)
		items = append(items, fmt.Sprintf(`{"name":%q,"segments":[%s]}`, rel, strings.Join(segs, ",")))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func (f *fakeRecordings) install(h *harness) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routes["/v3/recordings/list"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"pageCount":1,"items":`+f.listing()+`}`)
	}
	h.routes["/v3/recordings/segments/delete"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		path, raw := r.URL.Query().Get("path"), r.URL.Query().Get("start")
		start, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if os.Remove(filepath.Join(f.root, path, segName(start))) != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.deleted = append(f.deleted, path+"@"+start.UTC().Format(time.RFC3339))
		f.mu.Unlock()
	}
	h.routes["/v3/recordings/get/live/cam"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"name":"live/cam","segments":[{"start":"2026-10-01T10:00:00Z"},{"start":"2026-10-01T10:01:00Z"}]}`)
	}
	h.routes["/list"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "live/cam" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `[{"start":"2026-10-01T10:00:00Z","duration":120.5,"url":"http://mediamtx:9996/get?x"}]`)
	}
	h.routes["/get"] = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("path") != "live/cam" || q.Get("duration") != "30" || q.Get("start") != "2026-10-01T10:00:10Z" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, "ftyp-and-the-rest-of-an-mp4")
	}
}

func recordingsHarness(t *testing.T, env map[string]string) (*harness, *fakeRecordings) {
	t.Helper()
	data := t.TempDir()
	root := filepath.Join(data, "recordings")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"MTXUI_DATA_DIR": data, "MTXUI_RECORDINGS_MIN_FREE_GB": "0", "MTXUI_RECORDINGS_CRITICAL_FREE_GB": "0",
	}
	for k, v := range env {
		vars[k] = v
	}
	h := newHarness(t, vars, fast)
	h.completeSetup()
	f := &fakeRecordings{t: t, root: root}
	f.install(h)
	return h, f
}

func TestRecordingsAPI(t *testing.T) {
	h, f := recordingsHarness(t, nil)
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	f.add("live/cam", base, 1000)
	f.add("live/cam", base.Add(time.Minute), 2000)
	f.add("other", base, 500)
	h.srv.recordingsOnce(context.Background())

	var list struct {
		Disk             RecordingsDisk  `json:"disk"`
		Paths            []RecordingPath `json:"paths"`
		ExportMaxSeconds float64         `json:"exportMaxSeconds"`
	}
	h.json(h.do("GET", "/api/v1/recordings", nil), &list)
	if list.ExportMaxSeconds != h.srv.d.Settings.ExportMaxDuration.Seconds() || list.ExportMaxSeconds == 0 {
		t.Errorf("exportMaxSeconds %v", list.ExportMaxSeconds)
	}
	if len(list.Paths) != 2 || list.Paths[0].Name != "live/cam" || list.Paths[0].Bytes != 3000 || list.Paths[0].Segments != 2 ||
		!list.Paths[0].Last.Equal(base.Add(time.Minute)) || list.Disk.Recordings != 3500 || list.Disk.Free <= 0 || list.Disk.Guard != nil {
		t.Fatalf("list %+v", list)
	}

	rec := h.do("GET", "/api/v1/recordings/spans?path=live/cam", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "url") || !strings.Contains(rec.Body.String(), `"duration":120.5`) {
		t.Errorf("spans: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("GET", "/api/v1/recordings/spans?path=other", nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("no spans: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("GET", "/api/v1/recordings/segments?path=live/cam", nil); rec.Code != http.StatusOK || strings.Count(rec.Body.String(), "start") != 2 {
		t.Errorf("segments: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"", "../../etc", "~^.*$", "a//b", "/abs"} {
		if rec := h.do("GET", "/api/v1/recordings/spans?path="+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("path %q: %d", bad, rec.Code)
		}
	}

	exp := "/api/v1/recordings/export?path=live/cam&start=2026-10-01T10:00:10Z&duration=30&format=mp4"
	rec = h.do("GET", exp+"&download=1", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "ftyp-and-the-rest-of-an-mp4" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="live_cam_2026-10-01T10-00-10Z.mp4"` {
		t.Errorf("export: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	for _, q := range []string{
		"path=live/cam&start=2026-10-01T10:00:10Z&duration=0&format=mp4",
		"path=live/cam&start=2026-10-01T10:00:10Z&duration=7201&format=mp4", // over the 2 h cap
		"path=live/cam&start=2026-10-01T10:00:10Z&duration=30&format=mkv",
		"path=live/cam&start=yesterday&duration=30&format=mp4",
		"path=../cam&start=2026-10-01T10:00:10Z&duration=30&format=mp4",
	} {
		if rec := h.do("GET", "/api/v1/recordings/export?"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("export %s: %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec := h.do("GET", strings.Replace(exp, "duration=30", "duration=31", 1), nil); rec.Code != http.StatusNotFound {
		t.Errorf("an empty range: %d", rec.Code)
	}
	slots := h.srv.exportSlots()
	for range cap(slots) {
		slots <- struct{}{}
	}
	if rec := h.do("GET", exp, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("exports at the cap: %d", rec.Code)
	}
	for range cap(slots) {
		<-slots
	}

	rec = h.do("POST", "/api/v1/recordings/delete", map[string]any{
		"path": "live/cam", "starts": []string{base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339)},
	})
	if m := decode(rec); rec.Code != http.StatusOK || m["deleted"] != 1.0 || m["failed"] != 1.0 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "live/cam@2026-10-01T10:00:00Z" {
		t.Errorf("deleted %v", f.deleted)
	}
	if action, target, details := lastAudit(t, h); action != "recordings.delete" || target != "live/cam" || details["deleted"] != 1.0 {
		t.Errorf("audit %s %s %v", action, target, details)
	}
	if rec := h.do("POST", "/api/v1/recordings/delete", map[string]any{"path": "../x", "starts": []string{base.Format(time.RFC3339)}}); rec.Code != http.StatusBadRequest {
		t.Errorf("delete outside: %d", rec.Code)
	}
}

// An export stops at the size cap with a broken response, never a whole-looking file.
func TestRecordingExportCap(t *testing.T) {
	h, _ := recordingsHarness(t, map[string]string{"MTXUI_EXPORT_MAX_GB": "0.00000001"}) // about 10 bytes
	srv := httptest.NewServer(h.public)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/recordings/export?path=live/cam&start=2026-10-01T10:00:10Z&duration=30&format=mp4", nil)
	req.Header.Set("X-Forwarded-For", client)
	req.AddCookie(&http.Cookie{Name: "__Host-mtxui_session", Value: h.cookie})
	resp, err := srv.Client().Do(req)
	if err != nil {
		return // broken before the headers: fine, nothing looks whole
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil || len(body) >= len("ftyp-and-the-rest-of-an-mp4") {
		t.Errorf("a capped export read whole: %q %v", body, err)
	}
}

// The budget: over it, the oldest segments go (never a path's newest); then the guard at critical free space.
func TestRecordingsBudgetAndGuard(t *testing.T) {
	ctx := context.Background()
	budget := fmt.Sprintf("%.12f", 2500.0/(1<<30))
	h, f := recordingsHarness(t, map[string]string{"MTXUI_RECORDINGS_MAX_GB": budget})
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	for i := range 4 {
		f.add("live/cam", base.Add(time.Duration(i)*time.Minute), 1000)
	}
	f.add("other", base.Add(30*time.Second), 1000) // the only segment of its path: never pruned
	h.srv.recordingsOnce(ctx)
	if len(f.deleted) != 3 || f.deleted[0] != "live/cam@2026-10-01T10:00:00Z" || f.deleted[1] != "other@2026-10-01T10:00:30Z" &&
		f.deleted[1] != "live/cam@2026-10-01T10:01:00Z" {
		t.Errorf("pruned %v", f.deleted)
	}
	for _, d := range f.deleted {
		if strings.HasPrefix(d, "other@") || d == "live/cam@2026-10-01T10:03:00Z" {
			t.Errorf("pruned a path's newest segment: %v", f.deleted)
		}
	}
	if u := h.srv.recordingsDisk(ctx); u.Recordings > 2500 || u.Recordings < 1500 {
		t.Errorf("after pruning %d bytes, for a budget of 2500 with 1000-byte segments", u.Recordings)
	}
	if action, _, details := lastAudit(t, h); action != "recordings.prune" || details["reason"] != "budget" {
		t.Errorf("audit %s %v", action, details)
	}

	// Critical free space (any free space is "critical" here): recording goes off where it was on.
	if rec := h.do("PUT", "/api/v1/config/paths/live/cam", map[string]any{"config": map[string]any{"record": true}}); rec.Code != http.StatusOK {
		t.Fatalf("record on: %d %s", rec.Code, rec.Body)
	}
	h.srv.d.Settings.RecordingsCriticalBytes = 1 << 62
	h.srv.recordingsOnce(ctx)
	g := h.srv.guard(ctx)
	if g == nil || len(g.Paths) != 1 || g.Paths[0] != "live/cam" || !strings.Contains(h.mtxFile(), "record: no") {
		t.Fatalf("guard %+v\n%s", g, h.mtxFile())
	}
	if action, _, _ := lastAudit(t, h); action != "recordings.guard" {
		t.Errorf("audit %s", action)
	}
	rec := h.do("GET", "/api/v1/status", nil)
	if !strings.Contains(rec.Body.String(), "recordings_guard") {
		t.Errorf("no banner: %s", rec.Body)
	}
	h.srv.d.Settings.RecordingsMinFreeBytes = 1 << 62
	if rec := h.do("POST", "/api/v1/recordings/guard/release", nil); rec.Code != http.StatusConflict {
		t.Errorf("release without room: %d", rec.Code)
	}
	h.srv.d.Settings.RecordingsMinFreeBytes, h.srv.d.Settings.RecordingsCriticalBytes = 0, 0
	if rec := h.do("POST", "/api/v1/recordings/guard/release", nil); rec.Code != http.StatusOK ||
		!strings.Contains(h.mtxFile(), "record: yes") || h.srv.guard(ctx) != nil {
		t.Errorf("release: %d %s\n%s", rec.Code, rec.Body, h.mtxFile())
	}
}

// No request input reaches a filesystem call: every os, filepath and path call in the package is checked for the
// request (r) or its URL in its arguments.
func TestNoRequestInputInFilesystemCalls(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fsCalls := map[string]bool{"os": true, "filepath": true, "path": true}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		{
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkgID, ok := sel.X.(*ast.Ident); !ok || !fsCalls[pkgID.Name] {
					return true
				}
				for _, arg := range call.Args {
					ast.Inspect(arg, func(m ast.Node) bool {
						switch x := m.(type) {
						case *ast.Ident:
							if x.Name == "r" || x.Name == "req" {
								t.Errorf("%s: %s.%s takes request input", fset.Position(call.Pos()), sel.X, sel.Sel.Name)
							}
						case *ast.SelectorExpr:
							if x.Sel.Name == "URL" || x.Sel.Name == "URLParam" {
								t.Errorf("%s: %s.%s takes request input", fset.Position(call.Pos()), sel.X, sel.Sel.Name)
							}
						}
						return true
					})
				}
				return true
			})
		}
	}
}

// Whatever a request names as a recording's path, what gets through is a plain relative MediaMTX path name.
func FuzzRecordingPath(f *testing.F) {
	for _, seed := range []string{"live/cam", "../etc/passwd", "/abs", "a/../b", "~^.*$", "a\\b", "a//b", "%2e%2e", "cam\x00", "ünï"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.URL.RawQuery = "path=" + strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2B").Replace(name)
		got, ok := recordingPath(httptest.NewRecorder(), req)
		if !ok {
			return
		}
		if !filepath.IsLocal(got) || strings.ContainsAny(got, "\\\x00") || filepath.Clean(got) != got {
			t.Errorf("accepted %q", got)
		}
		for _, seg := range strings.Split(got, "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Errorf("accepted %q", got)
			}
		}
	})
}

// A stream's manager switches recording on and off from the stream page.
func TestStreamRecord(t *testing.T) {
	h, _ := recordingsHarness(t, nil)
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/rec"}), &st)
	stream := "/api/v1/streams/" + itoa(st.ID)
	if m := decode(h.do("GET", stream, nil)); m["record"] != false {
		t.Errorf("a new stream records: %v", m["record"])
	}
	if rec := h.do("PATCH", stream, map[string]any{"record": true}); rec.Code != http.StatusOK || decode(rec)["record"] != true ||
		!strings.Contains(h.mtxFile(), "  live/rec:\n    record: yes\n") {
		t.Fatalf("record on: %d %s\n%s", rec.Code, rec.Body, h.mtxFile())
	}
	if action, _, details := lastAudit(t, h); action != "stream.update" || details["record"] != true {
		t.Errorf("audit %s %v", action, details)
	}
	if rec := h.do("PATCH", stream, map[string]any{"record": false}); rec.Code != http.StatusOK || decode(rec)["record"] != false {
		t.Errorf("record off: %d %s", rec.Code, rec.Body)
	}
}
