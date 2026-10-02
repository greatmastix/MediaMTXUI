package mtxconf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mtxui/internal/mtxtest"
	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

// fakeAPI answers config reads from a map of path to JSON; down makes every read fail.
type fakeAPI struct {
	mu      sync.Mutex
	objects map[string]string
	down    bool
	reads   int
}

func (f *fakeAPI) Get(_ context.Context, path string) (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.down {
		return 0, nil, errors.New("connection refused")
	}
	if path == "/v3/info" {
		return 200, []byte(`{"version":"v1.21.1"}`), nil
	}
	body, ok := f.objects[path]
	if !ok {
		return 404, []byte(`{"error":"not found"}`), nil
	}
	return 200, []byte(body), nil
}

func (f *fakeAPI) set(path, body string) {
	f.mu.Lock()
	f.objects[path] = body
	f.mu.Unlock()
}

func TestChangedSettings(t *testing.T) {
	before := []byte("readTimeout: 10s\nrtsp: yes\npathDefaults:\n  record: no\npaths:\n  a:\n    source: publisher\n  gone: {}\n")
	after := []byte("readTimeout: 1m\nrtsp: yes\nlogLevel: debug\npathDefaults:\n  record: yes\npaths:\n  a:\n    source: rtsp://x/y\n  b: {}\n")
	checks, err := changedSettings(before, after)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range checks {
		got = append(got, c.label)
	}
	want := "logLevel,readTimeout,pathDefaults.record,paths.a,paths.a.source,paths.b,paths.gone (removed)"
	if strings.Join(got, ",") != want {
		t.Errorf("checks %v", got)
	}
	for _, tt := range []struct {
		effective, written any
		same               bool
	}{
		{"1m0s", "1m", true},
		{"10s", "1m", false},
		{float64(5), 5, true},
		{"", nil, true},
		{nil, "", true},
		{[]any{"tcp"}, []any{"tcp"}, true},
		{true, true, true},
		{false, true, false},
	} {
		if sameSetting(tt.effective, tt.written) != tt.same {
			t.Errorf("sameSetting(%#v, %#v) = %v", tt.effective, tt.written, !tt.same)
		}
	}
}

func TestVerifier(t *testing.T) {
	ctx := context.Background()
	before := []byte("readTimeout: 10s\n")
	after := []byte("readTimeout: 20s\n")
	api := &fakeAPI{objects: map[string]string{"/v3/config/global/get": `{"readTimeout":"10s"}`}}
	v := &Verifier{API: api, Timeout: 2 * time.Second, Interval: 10 * time.Millisecond}

	// MediaMTX picks the change up a moment later.
	go func() {
		time.Sleep(100 * time.Millisecond)
		api.set("/v3/config/global/get", `{"readTimeout":"20s"}`)
	}()
	if got, err := v.Verify(ctx, before, after); err != nil || got.State != AppliedVerified {
		t.Fatalf("reload: %+v %v", got, err)
	}

	// It answers, but never with the new value.
	v.Timeout = 200 * time.Millisecond
	api.set("/v3/config/global/get", `{"readTimeout":"10s"}`)
	if got, err := v.Verify(ctx, before, after); err != nil || got.State != AppliedMismatch || got.Keys[0] != "readTimeout" {
		t.Fatalf("mismatch: %+v %v", got, err)
	}

	// It stops answering.
	api.mu.Lock()
	api.down = true
	api.mu.Unlock()
	if _, err := v.Verify(ctx, before, after); err == nil {
		t.Fatal("a MediaMTX that stopped answering was not reported")
	}
}

func TestWriterRestoresWhenMediaMTXStopsAnswering(t *testing.T) {
	ctx := context.Background()
	w, st := newWriter(t, fakeChecker{})
	good := seed(t, params)
	if _, err := w.Write(ctx, good, "system", "seed"); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{objects: map[string]string{"/v3/config/global/get": `{}`}}
	w.Verifier = &Verifier{API: api, Timeout: 200 * time.Millisecond, Interval: 10 * time.Millisecond}
	go func() { // MediaMTX exits on the reload
		for {
			api.mu.Lock()
			reads := api.reads
			api.mu.Unlock()
			if reads > 0 {
				api.mu.Lock()
				api.down = true
				api.mu.Unlock()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	_, err := w.Edit(ctx, "admin", "bind a busy port", nil, func(d *yamledit.Doc) error {
		return d.Set([]string{"readTimeout"}, "20s")
	})
	var notApplied *NotAppliedError
	if !errors.As(err, &notApplied) || notApplied.Result.Applied.State != AppliedRestored {
		t.Fatalf("got %v", err)
	}
	if now, _, _ := w.Current(); string(now) != string(good) {
		t.Fatal("the previous file was not restored")
	}
	list, _ := st.ListSnapshots(ctx, 0, 5)
	if len(list) != 3 || list[0].Author != "system" || !strings.Contains(list[0].Reason, "stopped answering after version 2") {
		t.Errorf("history: %+v", list)
	}

	// MediaMTX already down before a change: nothing to compare, nothing restored.
	res, err := w.Edit(ctx, "admin", "while down", nil, func(d *yamledit.Doc) error {
		return d.Set([]string{"readTimeout"}, "30s")
	})
	if err != nil || res.Applied.State != AppliedSkipped {
		t.Fatalf("while down: %+v %v", res.Applied, err)
	}
}

// Against the real MediaMTX: a change is read back once MediaMTX has reloaded; a change that passes validation but
// kills MediaMTX on reload (a port another process holds) is undone.
func TestVerifyWithMediaMTX(t *testing.T) {
	mtxtest.Bin(t)
	ctx := context.Background()
	allow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer allow.Close()
	api, rtsp := mtxtest.FreePort(t), mtxtest.FreePort(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	authURL := allow.URL + "/internal/auth"
	config := fmt.Sprintf("authMethod: http\nauthHTTPAddress: %s\nauthHTTPExclude: []\napi: yes\napiAddress: 127.0.0.1:%d\n"+
		"rtsp: yes\nrtspAddress: 127.0.0.1:%d\nrtspTransports: [tcp]\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\n"+
		"readTimeout: 10s\npaths:\n  all_others:\n", authURL, api, rtsp)
	inst := mtxtest.Start(t, config, api)

	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w := &Writer{
		Path: filepath.Join(inst.Dir, "mediamtx.yml"), LockPath: filepath.Join(dir, "lock"),
		Rules: Rules{AuthURL: authURL}, Checker: Validator{Bin: mtxtest.Bin(t)}, Store: st,
		Verifier: &Verifier{API: &APIClient{Base: fmt.Sprintf("http://127.0.0.1:%d", api), Principal: basicAuth{}}},
		MinGap:   ReloadGap,
	}
	if _, err := w.Reconcile(ctx, []byte(config)); err != nil {
		t.Fatal(err)
	}

	res, err := w.Edit(ctx, "admin", "longer timeout", nil, func(d *yamledit.Doc) error {
		if err := d.Set([]string{"readTimeout"}, "1m"); err != nil {
			return err
		}
		return d.Set([]string{"paths", "cam1"}, map[string]any{"source": "publisher", "maxReaders": 3})
	})
	if err != nil || res.Applied.State != AppliedVerified {
		t.Fatalf("a normal change: %+v %v\n%s", res.Applied, err, inst.Logs())
	}

	// Straight after the first write: MediaMTX ignores file changes within a second of its last reload, so this only
	// works because the writer spaces its writes.
	before, _, _ := w.Current()
	busyPort := busy.Addr().(*net.TCPAddr).Port
	_, err = w.Edit(ctx, "admin", "RTSP on a busy port", nil, func(d *yamledit.Doc) error {
		return d.Set([]string{"rtspAddress"}, fmt.Sprintf("127.0.0.1:%d", busyPort))
	})
	var notApplied *NotAppliedError
	if !errors.As(err, &notApplied) {
		t.Fatalf("a change that kills MediaMTX: %v\n%s", err, inst.Logs())
	}
	if now, _ := os.ReadFile(w.Path); string(now) != string(before) {
		t.Error("the previous file was not restored")
	}
}

type basicAuth struct{}

func (basicAuth) Authorize(r *http.Request) { r.SetBasicAuth("mtxui-sidecar", "s") }
