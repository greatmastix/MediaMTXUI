package mtxconf

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/mtxtest"
	"mtxui/internal/store"
)

const authURL = "http://sidecar:9081/internal/auth"

var params = SeedParams{
	MediaMTXVersion: "1.21.1", AuthURL: authURL, PublicHost: "mtx.example.com",
	StackSubnet: netip.MustParsePrefix("172.29.42.0/24"), RTSP: true,
}

func seed(t *testing.T, p SeedParams) []byte {
	t.Helper()
	b, err := Seed(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSeed(t *testing.T) {
	b := seed(t, params)
	var conf map[string]any
	if err := yaml.Unmarshal(b, &conf); err != nil {
		t.Fatalf("seed is not YAML: %v\n%s", err, b)
	}
	for key, want := range map[string]any{
		"authMethod": "http", "authHTTPAddress": authURL, "rtsp": "yes", "rtmp": "no", "srt": "no", "moq": "no",
		"pprof": "no", "webrtcIPsFromInterfaces": "no",
	} {
		if conf[key] != want {
			t.Errorf("%s = %v, want %v", key, conf[key], want)
		}
	}
	if got := conf["hlsTrustedProxies"].([]any); len(got) != 1 || got[0] != "172.29.42.0/24" {
		t.Errorf("hlsTrustedProxies = %v", got)
	}
	if got := conf["webrtcAdditionalHosts"].([]any); len(got) != 1 || got[0] != "mtx.example.com" {
		t.Errorf("webrtcAdditionalHosts = %v", got)
	}
	if err := (Rules{AuthURL: authURL}).Check(b); err != nil {
		t.Errorf("the seed breaks the sidecar's own rules: %v", err)
	}

	noSubnet := params
	noSubnet.StackSubnet = netip.Prefix{}
	if b := seed(t, noSubnet); strings.Contains(string(b), "TrustedProxies") {
		t.Errorf("trusted proxies rendered without a subnet:\n%s", b)
	}
	evil := params
	evil.PublicHost = `x"]` + "\nauthMethod: internal\n#"
	var conf2 map[string]any
	if err := yaml.Unmarshal(seed(t, evil), &conf2); err != nil || conf2["authMethod"] != "http" {
		t.Errorf("a hostile value escaped its quoting: %v %v", err, conf2["authMethod"])
	}
}

func TestRules(t *testing.T) {
	r := Rules{AuthURL: authURL}
	good := string(seed(t, params))
	with := func(extra string) []byte { return []byte(good + extra) }
	replace := func(from, to string) []byte { return []byte(strings.Replace(good, from, to, 1)) }
	bad := []struct {
		name    string
		content []byte
		want    string
	}{
		{"empty", nil, "empty"},
		{"whitespace", []byte("  \n\n"), "empty"},
		{"null document", []byte("~\n"), "no settings"},
		{"not YAML", []byte("a: [\n"), "not valid YAML"},
		{"duplicate key", with("rtmp: yes\n"), "not valid YAML"},
		{"internal auth", replace("authMethod: http", "authMethod: internal"), `authMethod must be "http"`},
		{"auth elsewhere", replace(authURL, "http://evil:80/auth"), "authHTTPAddress must be"},
		{"auth exclusions", replace("authHTTPExclude: []", "authHTTPExclude: [{action: publish}]"), "authHTTPExclude must be empty"},
		{"API off", replace("api: yes", "api: no"), "api must be on"},
		{"pprof on", replace("pprof: no", "pprof: yes"), "pprof must be off"},
		{"RTMP on the RTSP port", replace("rtmp: no", "rtmp: yes\nrtmpAddress: :8554"), "both listen on tcp port 8554"},
		{"API on the HLS port", replace("apiAddress: :9997", "apiAddress: :8888"), "both listen on tcp port 8888"},
		{"SRT on WebRTC's UDP port", replace("srt: no", "srt: yes\nsrtAddress: :8189"), "both listen on udp port 8189"},
		{"RTSP over UDP on the SRT port", []byte(strings.NewReplacer("rtspTransports: [tcp]", "rtspTransports: [udp, tcp]\nrtpAddress: :8890",
			"srt: no", "srt: yes").Replace(good)), "both listen on udp port 8890"},
		{"record outside /recordings", replace("/recordings/%path/", "/etc/%path/"), "under /recordings/"},
		{"record with ..", replace("/recordings/%path/", "/recordings/../%path/"), `must not contain ".."`},
		{"record without %path", replace("/recordings/%path/%Y", "/recordings/all/%Y"), "must contain %path"},
		{"path traversal name", with("  a/../b:\n"), `path "a/../b"`},
		{"bad regex path", with("  ~(:\n"), "invalid regular expression"},
		{"per-path recordPath", with("  cam1:\n    recordPath: /tmp/x\n"), "path cam1: recordPath"},
	}
	for _, tt := range bad {
		err := r.Check(tt.content)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want an error containing %q", tt.name, err, tt.want)
		}
	}
	for name, content := range map[string][]byte{
		"TCP and UDP on one port": replace("srt: no", "srt: yes\nsrtAddress: :8888"),
		"one port on two hosts": []byte(strings.NewReplacer("apiAddress: :9997", "apiAddress: 127.0.0.1:8000",
			"metricsAddress: :9998", "metricsAddress: 10.0.0.1:8000").Replace(good)),
		"disabled server's default": replace("rtmp: no", "rtmp: no\nhlsAddress: :1935"),
		"regex path":                with("  ~^cam[0-9]+$:\n"),
		"booleans as true/false":    replace("api: yes", "api: true"),
	} {
		if err := r.Check(content); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The defaults above are MediaMTX v1.21.1's. This fails when a MediaMTX bump changes one.
func TestDefaultsMatchReference(t *testing.T) {
	b, err := os.ReadFile(mtxtest.Reference(t))
	if err != nil {
		t.Fatal(err)
	}
	var ref map[string]any
	if err := yaml.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	for key, want := range defaultAddress {
		got, _ := ref[key].(string)
		if got != want {
			t.Errorf("%s: reference %q, table %q", key, got, want)
		}
	}
	for _, l := range listeners {
		if got, ok := asBool(ref[l.enable]); !ok || got != l.dflt {
			t.Errorf("%s: reference %v, table %v", l.enable, ref[l.enable], l.dflt)
		}
	}
	if !transportsInclude(ref["rtspTransports"], "udp") {
		t.Error("the reference no longer enables RTSP over UDP by default")
	}
}

type fakeChecker struct{ err error }

func (f fakeChecker) Validate(context.Context, []byte) error { return f.err }

func newWriter(t *testing.T, c Checker) (*Writer, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := os.Mkdir(filepath.Join(dir, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	return &Writer{
		Path: filepath.Join(dir, "config", "mediamtx.yml"), LockPath: filepath.Join(dir, "config.lock"),
		Rules: Rules{AuthURL: authURL}, Checker: c, Store: st,
	}, st
}

func TestWrite(t *testing.T) {
	ctx := context.Background()
	w, st := newWriter(t, fakeChecker{})
	content := seed(t, params)
	snap, err := w.Write(ctx, content, "admin", "setup")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(w.Path)
	if string(got) != string(content) || snap.Author != "admin" || snap.Reason != "setup" {
		t.Fatalf("written %q, snapshot %+v", got, snap)
	}
	fi, _ := os.Stat(w.Path)
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(w.Path))
	if len(entries) != 1 {
		t.Errorf("leftover files in the config directory: %v", entries)
	}

	// Refused content leaves the file and the history alone.
	for _, bad := range [][]byte{nil, []byte("authMethod: internal\n")} {
		if _, err := w.Write(ctx, bad, "admin", "bad"); err == nil {
			t.Fatalf("%q was written", bad)
		}
	}
	w.Checker = fakeChecker{err: errors.New("MediaMTX says no")}
	if _, err := w.Write(ctx, content, "admin", "bad"); err == nil || !strings.Contains(err.Error(), "MediaMTX says no") {
		t.Fatalf("a checker failure was ignored: %v", err)
	}
	got, _ = os.ReadFile(w.Path)
	latest, _ := st.LatestSnapshot(ctx)
	if string(got) != string(content) || latest.ID != snap.ID {
		t.Fatal("a refused write changed the file or the history")
	}
}

func TestConcurrentWritesAreSerialised(t *testing.T) {
	ctx := context.Background()
	w, st := newWriter(t, fakeChecker{})
	var wg sync.WaitGroup
	for i := range 8 {
		p := params
		p.RTMP = i%2 == 0
		content := seed(t, p)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.Write(ctx, content, "admin", "race"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := os.ReadFile(w.Path)
	latest, _ := st.LatestSnapshot(ctx)
	if string(got) != string(latest.Content) {
		t.Fatal("the file and the newest snapshot disagree after concurrent writes")
	}
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	w, st := newWriter(t, fakeChecker{})
	good := seed(t, params)

	if out, err := w.Reconcile(ctx, good); err != nil || out.Message != "wrote the initial config" || out.Notable {
		t.Fatalf("no file: %+v %v", out, err)
	}
	// With history, a missing file comes back as the newest snapshot rather than the seed.
	custom := append(append([]byte{}, good...), "# custom\n"...)
	if _, err := w.Write(ctx, custom, "admin", "edit"); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(w.Path)
	if out, err := w.Reconcile(ctx, good); err != nil || !strings.Contains(out.Message, "restored snapshot") || !out.Notable {
		t.Fatalf("missing file with history: %+v %v", out, err)
	}
	if got, _ := os.ReadFile(w.Path); string(got) != string(custom) {
		t.Fatal("a missing file was not restored from the newest snapshot")
	}
	_ = os.WriteFile(w.Path, good, 0o640) // back to the seed for the checks below
	if _, err := w.Reconcile(ctx, good); err != nil {
		t.Fatal(err)
	}
	if out, err := w.Reconcile(ctx, good); err != nil || out.Message != "" {
		t.Fatalf("unchanged file: %+v %v", out, err)
	}

	// An outside edit that is still valid is recorded, not reverted.
	edited := append(append([]byte{}, good...), "# a comment\n"...)
	_ = os.WriteFile(w.Path, edited, 0o640)
	if out, err := w.Reconcile(ctx, good); err != nil || !strings.Contains(out.Message, "outside the sidecar") || !out.Notable {
		t.Fatalf("valid outside edit: %+v %v", out, err)
	}
	if latest, _ := st.LatestSnapshot(ctx); latest.Author != "external" {
		t.Errorf("outside edit snapshot by %q", latest.Author)
	}

	// An invalid file is replaced by the last good snapshot.
	_ = os.WriteFile(w.Path, []byte("rtmp: [unclosed\n"), 0o640)
	out, err := w.Reconcile(ctx, good)
	if err != nil || !strings.Contains(out.Message, "restored snapshot") || !out.Notable {
		t.Fatalf("invalid file: %+v %v", out, err)
	}
	if got, _ := os.ReadFile(w.Path); string(got) != string(edited) {
		t.Error("the restored file is not the last good snapshot")
	}

	// A file the database has never seen (e.g. a fresh database) is adopted when valid.
	w2, st2 := newWriter(t, fakeChecker{})
	_ = os.WriteFile(w2.Path, good, 0o640)
	if out, err := w2.Reconcile(ctx, good); err != nil || !strings.Contains(out.Message, "adopted") || out.Notable {
		t.Fatalf("unknown valid file: %+v %v", out, err)
	}
	if latest, _ := st2.LatestSnapshot(ctx); latest.Author != "system" {
		t.Errorf("adoption snapshot by %q", latest.Author)
	}
}

// The S4 cases, through the real validation pipeline: the sidecar's rules plus MediaMTX's --validate-conf.
func TestPipelineRejectsS4Cases(t *testing.T) {
	v := Validator{Bin: mtxtest.Bin(t)}
	w, _ := newWriter(t, v)
	ctx := context.Background()
	good := string(seed(t, params))
	if err := w.Validate(ctx, []byte(good)); err != nil {
		t.Fatalf("the seed fails MediaMTX's own validation: %v", err)
	}
	for name, content := range map[string]string{
		"syntax error":      "logLevel: info\nrtmp: [unclosed\n",
		"unknown key":       good + "notAKey: 1\n",
		"invalid value":     strings.Replace(good, "rtspTransports: [tcp]", "rtspTransports: [bogus]", 1),
		"invalid path name": good + "  \"bad name!\": {}\n",
		"listener conflict": strings.Replace(good, "rtmp: no", "rtmp: yes\nrtmpAddress: :8554", 1),
		"empty file":        "",
	} {
		if _, err := w.Write(ctx, []byte(content), "admin", name); err == nil {
			t.Errorf("%s: written", name)
		}
	}
	if _, err := os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) {
		t.Error("a rejected config reached the file")
	}
	if err := v.Validate(ctx, []byte(strings.Replace(good, "rtspTransports: [tcp]", "rtspTransports: [bogus]", 1))); err == nil ||
		!strings.Contains(err.Error(), "invalid transport: bogus") || strings.Contains(err.Error(), "mtxui-validate") {
		t.Errorf("MediaMTX's message should come through without the temp path: %v", err)
	}
	version, err := v.Version(ctx)
	if err != nil || !strings.HasPrefix(version, "v1.") {
		t.Errorf("Version = %q, %v", version, err)
	}
}
