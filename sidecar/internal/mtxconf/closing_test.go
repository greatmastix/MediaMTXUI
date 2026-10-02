package mtxconf

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/mtxtest"
	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

func TestClosingStep(t *testing.T) {
	readers := func(t *testing.T, doc []byte, name string) any {
		t.Helper()
		var c struct {
			Paths map[string]map[string]any `yaml:"paths"`
		}
		if err := yaml.Unmarshal(doc, &c); err != nil {
			t.Fatal(err)
		}
		return c.Paths[name]["maxReaders"]
	}
	for _, tc := range []struct {
		name, before, after string
		want                map[string]any // maxReaders per path in the step; nil: no step
	}{
		{
			"caught by all_others", "paths:\n  Live/a: {}\n  keep: {}\n  all_others:\n", "paths:\n  keep: {}\n  all_others:\n",
			map[string]any{"Live/a": 1, "keep": nil},
		},
		{"no pattern left", "paths:\n  cam: {}\n", "paths: {}\n", nil},
		{"nothing removed", "paths:\n  cam: {}\n  all_others:\n", "paths:\n  cam: {}\n  all_others:\n", nil},
		{
			"the pattern's cap", "paths:\n  cam:\n    source: publisher\n  all:\n    maxReaders: 1\n", "paths:\n  all:\n    maxReaders: 1\n",
			map[string]any{"cam": 2},
		},
		{
			"the defaults' cap", "pathDefaults:\n  maxReaders: 4\npaths:\n  cam: \n  all_others:\n", "pathDefaults:\n  maxReaders: 4\npaths:\n  all_others:\n",
			map[string]any{"cam": 5},
		},
		{
			"an expression before all_others", "paths:\n  live/x: {}\n  ~^live/:\n    maxReaders: 7\n  all_others:\n",
			"paths:\n  ~^live/:\n    maxReaders: 7\n  all_others:\n",
			map[string]any{"live/x": 8},
		},
		{"a non-matching expression", "paths:\n  cam: {}\n  ~^live/:\n    maxReaders: 7\n", "paths:\n  ~^live/:\n    maxReaders: 7\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step, err := closingStep([]byte(tc.before), []byte(tc.after))
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				if step != nil {
					t.Fatalf("unexpected step:\n%s", step)
				}
				return
			}
			for name, want := range tc.want {
				if got := readers(t, step, name); got != want {
					t.Errorf("%s: maxReaders %v, want %v\n%s", name, got, want, step)
				}
			}
			if _, err := closingStep(step, []byte(tc.after)); err != nil {
				t.Error(err)
			}
		})
	}
}

// The step changes only the cap: the rest of the entry keeps its bytes (MediaMTX's yes/no stay booleans).
func TestClosingStepKeepsTheEntry(t *testing.T) {
	before := "paths:\n  cam:\n    source: rtsp://192.0.2.10:554/stream\n    sourceOnDemand: yes\n  all_others:\n"
	step, err := closingStep([]byte(before), []byte("paths:\n  all_others:\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "  cam:\n    source: rtsp://192.0.2.10:554/stream\n    sourceOnDemand: yes\n    maxReaders: 1\n"; !strings.Contains(string(step), want) {
		t.Errorf("step:\n%s", step)
	}
}

// Against the real MediaMTX: a named path deleted while all_others matches its name leaves MediaMTX's paths list
// (without the step, it stays there until a restart).
func TestRemovedPathClosesWithMediaMTX(t *testing.T) {
	mtxtest.Bin(t)
	ctx := context.Background()
	allow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer allow.Close()
	api := mtxtest.FreePort(t)
	authURL := allow.URL + "/internal/auth"
	config := fmt.Sprintf("authMethod: http\nauthHTTPAddress: %s\nauthHTTPExclude: []\napi: yes\napiAddress: 127.0.0.1:%d\n"+
		"rtsp: no\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\npaths:\n  cam1: {}\n  all_others:\n", authURL, api)
	inst := mtxtest.Start(t, config, api)
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := &APIClient{Base: fmt.Sprintf("http://127.0.0.1:%d", api), Principal: basicAuth{}}
	w := &Writer{
		Path: filepath.Join(inst.Dir, "mediamtx.yml"), LockPath: filepath.Join(dir, "lock"),
		Rules: Rules{AuthURL: authURL}, Checker: Validator{Bin: mtxtest.Bin(t)}, Store: st,
		Verifier: &Verifier{API: client}, MinGap: ReloadGap,
	}
	if _, err := w.Reconcile(ctx, []byte(config)); err != nil {
		t.Fatal(err)
	}
	listed := func() bool {
		_, body, err := client.Get(ctx, "/v3/paths/list")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(body), `"name":"cam1"`)
	}
	if !listed() {
		t.Fatalf("cam1 is not running\n%s", inst.Logs())
	}
	res, err := w.Edit(ctx, "admin", "cam1 deleted", nil, func(d *yamledit.Doc) error {
		return d.Delete([]string{"paths", "cam1"})
	})
	if err != nil || res.Applied.State != AppliedVerified {
		t.Fatalf("delete: %+v %v\n%s", res.Applied, err, inst.Logs())
	}
	for deadline := time.Now().Add(5 * time.Second); listed(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("cam1 is still in MediaMTX's paths list\n%s", inst.Logs())
		}
	}
	list, _ := st.ListSnapshots(ctx, 0, 5)
	if len(list) < 2 || !strings.Contains(list[1].Reason, "closing its path first") || list[0].Reason != "cam1 deleted" {
		t.Errorf("history: %+v", list)
	}
}
