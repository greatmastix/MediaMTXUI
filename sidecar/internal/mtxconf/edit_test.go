package mtxconf

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"mtxui/internal/mtxtest"
	"mtxui/internal/yamledit"
)

func TestEditAndReplace(t *testing.T) {
	ctx := context.Background()
	w, st := newWriter(t, fakeChecker{})
	base := seed(t, params)
	if _, err := w.Write(ctx, base, "system", "seed"); err != nil {
		t.Fatal(err)
	}

	snap, err := w.Edit(ctx, "admin", "add cam1", nil, func(d *yamledit.Doc) error {
		return d.Set([]string{"paths", "cam1"}, map[string]any{"source": "publisher", "record": true})
	})
	if err != nil {
		t.Fatal(err)
	}
	after, sum, _ := w.Current()
	if !strings.Contains(string(after), "  cam1:\n    record: yes\n    source: publisher\n") || snap.SHA256 != sum {
		t.Fatalf("after the edit (snapshot %+v):\n%s", snap, after)
	}
	// The edit only inserted the new entry: take it out again and the file is byte for byte what it was.
	if strings.Replace(string(after), "  cam1:\n    record: yes\n    source: publisher\n", "", 1) != string(base) {
		t.Error("the edit disturbed the rest of the file")
	}

	if _, err := w.Edit(ctx, "admin", "nothing", nil, func(*yamledit.Doc) error { return nil }); !errors.Is(err, ErrUnchanged) {
		t.Errorf("an empty edit: %v", err)
	}
	var invalid *InvalidError
	if _, err := w.Edit(ctx, "admin", "weaken auth", nil, func(d *yamledit.Doc) error {
		return d.Set([]string{"authMethod"}, "internal")
	}); !errors.As(err, &invalid) {
		t.Errorf("an edit breaking the rules: %v", err)
	}
	if _, err := w.Edit(ctx, "admin", "gone", nil, func(d *yamledit.Doc) error {
		return d.Delete([]string{"paths", "nope"})
	}); !errors.Is(err, yamledit.ErrNotFound) {
		t.Errorf("deleting a missing path: %v", err)
	}
	guardErr := errors.New("no")
	if _, err := w.Edit(ctx, "admin", "guarded", func(_, _ []byte) error { return guardErr }, func(d *yamledit.Doc) error {
		return d.Set([]string{"logLevel"}, "debug")
	}); !errors.Is(err, guardErr) {
		t.Errorf("the guard was not consulted: %v", err)
	}
	if now, _, _ := w.Current(); string(now) != string(after) {
		t.Fatal("a refused edit changed the file")
	}

	if _, err := w.Replace(ctx, base, "stale", "admin", "raw", nil); !errors.Is(err, ErrConflict) {
		t.Errorf("a stale replace: %v", err)
	}
	if _, err := w.Replace(ctx, base, sum, "admin", "raw", nil); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if now, _, _ := w.Current(); string(now) != string(base) {
		t.Error("replace did not write the content")
	}
	list, _ := st.ListSnapshots(ctx, 0, 10)
	if len(list) != 3 || list[0].Reason != "raw" || list[1].Reason != "add cam1" {
		t.Errorf("history: %+v", list)
	}
}

// Real edits through the full pipeline, with MediaMTX's own validation: the result is accepted by MediaMTX.
func TestEditWithMediaMTX(t *testing.T) {
	ctx := context.Background()
	w, _ := newWriter(t, Validator{Bin: mtxtest.Bin(t)})
	if _, err := w.Write(ctx, seed(t, params), "system", "seed"); err != nil {
		t.Fatal(err)
	}
	edits := map[string]func(*yamledit.Doc) error{
		"a camera source": func(d *yamledit.Doc) error {
			return d.Set([]string{"paths", "cam1"}, map[string]any{"source": "rtsp://10.0.0.5:554/live", "sourceOnDemand": true})
		},
		"a global setting": func(d *yamledit.Doc) error { return d.Set([]string{"readTimeout"}, "20s") },
		"path defaults":    func(d *yamledit.Doc) error { return d.Set([]string{"pathDefaults", "maxReaders"}, 10) },
		"a regex path":     func(d *yamledit.Doc) error { return d.Set([]string{"paths", "~^live/.+$"}, map[string]any{}) },
	}
	for name, e := range edits {
		if _, err := w.Edit(ctx, "admin", name, nil, e); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	var invalid *InvalidError
	if _, err := w.Edit(ctx, "admin", "unknown", nil, func(d *yamledit.Doc) error {
		return d.Set([]string{"paths", "cam1", "notASetting"}, 1)
	}); !errors.As(err, &invalid) || !strings.Contains(err.Error(), "notASetting") {
		t.Errorf("an unknown path setting: %v", err)
	}
	final, _ := os.ReadFile(w.Path)
	if err := w.Validate(ctx, final); err != nil {
		t.Errorf("the final file: %v", err)
	}
}

func TestHookChanges(t *testing.T) {
	before := []byte("runOnConnect: echo hi\npaths:\n  cam1:\n    runOnReady: ffmpeg x\n  cam2: {}\n")
	for _, tt := range []struct {
		after string
		want  string
	}{
		{string(before), ""},
		{"runOnConnect: echo hi\npaths:\n  cam1:\n    runOnReady: ffmpeg x\n    record: yes\n", ""},
		{"runOnConnect: echo bye\npaths:\n  cam1:\n    runOnReady: ffmpeg x\n", "runOnConnect"},
		{"runOnConnect: echo hi\npaths: {}\n", "paths.cam1.runOnReady"},
		{"runOnConnect: echo hi\npathDefaults:\n  runOnInit: sh\npaths:\n  cam1:\n    runOnReady: ffmpeg x\n", "pathDefaults.runOnInit"},
	} {
		got, err := HookChanges(before, []byte(tt.after))
		if err != nil || strings.Join(got, ",") != tt.want {
			t.Errorf("after %q: %v, %v; want %q", tt.after, got, err, tt.want)
		}
	}
}

func TestWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, st := newWriter(t, fakeChecker{})
	good := seed(t, params)
	if _, err := w.Reconcile(ctx, good); err != nil {
		t.Fatal(err)
	}
	reports := make(chan Outcome, 10)
	go w.Watch(ctx, 20*time.Millisecond, good, func(out Outcome, err error) {
		if err != nil {
			t.Error(err)
		}
		reports <- out
	})
	next := func() Outcome {
		t.Helper()
		select {
		case out := <-reports:
			return out
		case <-time.After(3 * time.Second):
			t.Fatal("no report")
		}
		return Outcome{}
	}

	// The sidecar's own writes are not outside edits.
	if _, err := w.Edit(ctx, "admin", "own edit", nil, func(d *yamledit.Doc) error { return d.Set([]string{"logLevel"}, "debug") }); err != nil {
		t.Fatal(err)
	}
	own, _, _ := w.Current()

	// An outside edit that breaks the rules is reverted, and what it was is kept for the audit log.
	bad := strings.Replace(string(own), "authMethod: http", "authMethod: internal", 1)
	replaceFile(t, w.Path, []byte(bad))
	out := next()
	if !out.Notable || string(out.Rejected) != bad || !strings.Contains(out.Message, "restored snapshot") {
		t.Fatalf("invalid edit: %+v", out)
	}
	if now, _, _ := w.Current(); string(now) != string(own) {
		t.Fatal("the invalid edit was not reverted")
	}

	// A valid one is recorded.
	replaceFile(t, w.Path, append(append([]byte{}, own...), "# hand-edited\n"...))
	if out := next(); !out.Notable || out.Rejected != nil || !strings.Contains(out.Message, "recorded as snapshot") {
		t.Fatalf("valid edit: %+v", out)
	}
	if latest, _ := st.LatestSnapshot(ctx); latest.Author != "external" {
		t.Errorf("latest snapshot by %q", latest.Author)
	}
	select {
	case out := <-reports:
		t.Fatalf("an unexpected report: %+v", out)
	case <-time.After(100 * time.Millisecond):
	}
}

// replaceFile changes the file in one step (temp file, rename), so that a look never sees it half-written: what is
// tested here is how a change is judged; TestWatchWaitsForAWriteToFinish covers a file caught mid-write.
func replaceFile(t *testing.T, path string, b []byte) {
	t.Helper()
	tmp := path + ".test-tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// A file caught in the middle of being written (empty after the truncate, before the write) is not judged: the
// finished edit is recorded, not reverted as an empty config.
func TestWatchWaitsForAWriteToFinish(t *testing.T) {
	ctx := context.Background()
	w, _ := newWriter(t, fakeChecker{})
	good := seed(t, params)
	if _, err := w.Reconcile(ctx, good); err != nil {
		t.Fatal(err)
	}
	var pending string
	if _, judged, _ := w.look(ctx, good, &pending); judged {
		t.Fatal("an unchanged file was judged")
	}
	_ = os.WriteFile(w.Path, nil, 0o640) // truncated: the editor has not written yet
	if out, judged, _ := w.look(ctx, good, &pending); judged {
		t.Fatalf("judged mid-write: %+v", out)
	}
	edited := append(append([]byte{}, good...), "# hand-edited\n"...)
	_ = os.WriteFile(w.Path, edited, 0o640)
	if out, judged, _ := w.look(ctx, good, &pending); judged {
		t.Fatalf("judged at the first look at the new content: %+v", out)
	}
	out, judged, err := w.look(ctx, good, &pending)
	if !judged || err != nil || out.Rejected != nil || !strings.Contains(out.Message, "recorded as snapshot") {
		t.Fatalf("the finished edit: judged %v, %+v, %v", judged, out, err)
	}
	if now, _, _ := w.Current(); string(now) != string(edited) {
		t.Fatal("the edit did not stay")
	}
}
