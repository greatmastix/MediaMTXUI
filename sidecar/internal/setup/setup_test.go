package setup

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mtxui/internal/auth"
	"mtxui/internal/mtxconf"
	"mtxui/internal/store"
)

type okChecker struct{}

func (okChecker) Validate(context.Context, []byte) error { return nil }

const authURL = "http://sidecar:9081/internal/auth"

func newService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_ = os.Mkdir(filepath.Join(dir, "config"), 0o750)
	w := &mtxconf.Writer{
		Path: filepath.Join(dir, "config", "mediamtx.yml"), LockPath: filepath.Join(dir, "lock"),
		Rules: mtxconf.Rules{AuthURL: authURL}, Checker: okChecker{}, Store: st,
	}
	return &Service{
		Store: st, Hasher: auth.NewHasher(auth.Params{Memory: 8 * 1024, Time: 1, Threads: 1}, 2), Writer: w,
		TokenPath: filepath.Join(dir, "setup-token"),
		Seed: func(in Ingest) ([]byte, error) {
			return mtxconf.Seed(mtxconf.SeedParams{
				MediaMTXVersion: "1.21.1", AuthURL: authURL, PublicHost: "h",
				StackSubnet: netip.MustParsePrefix("172.29.42.0/24"), RTSP: in.RTSP, RTMP: in.RTMP, SRT: in.SRT,
			})
		},
	}
}

func TestToken(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	tok, err := s.EnsureToken(ctx)
	if err != nil || len(tok) != 39 || strings.Count(tok, "-") != 7 {
		t.Fatalf("token %q, %v", tok, err)
	}
	fi, _ := os.Stat(s.TokenPath)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %v, want 0600", fi.Mode().Perm())
	}
	again, _ := s.EnsureToken(ctx)
	if again != tok {
		t.Error("the token changed between starts")
	}
	for _, typed := range []string{tok, strings.ToLower(tok), strings.ReplaceAll(tok, "-", ""), " " + strings.ReplaceAll(tok, "-", " ") + "\n"} {
		if !s.CheckToken(typed) {
			t.Errorf("%q rejected", typed)
		}
	}
	for _, wrong := range []string{"", "AAAA", tok[:38] + "Z", tok + "A"} {
		if wrong != tok && s.CheckToken(wrong) {
			t.Errorf("%q accepted", wrong)
		}
	}
}

func TestComplete(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	if _, err := s.EnsureToken(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Complete(ctx, Request{Username: "a", Password: "long enough pw"}); !errors.As(err, new(ValidationError)) {
		t.Errorf("bad username: %v", err)
	}
	if _, _, err := s.Complete(ctx, Request{Username: "admin", Password: "short"}); !errors.As(err, new(ValidationError)) {
		t.Errorf("short password: %v", err)
	}
	if required, _ := s.Required(ctx); !required {
		t.Fatal("a rejected request completed setup")
	}

	user, warning, err := s.Complete(ctx, Request{Username: "admin", Password: "correct horse battery", Ingest: Ingest{RTSP: true, SRT: true}})
	if err != nil || warning != "" || user.Role != "admin" {
		t.Fatalf("Complete = %+v, %q, %v", user, warning, err)
	}
	conf, _ := os.ReadFile(s.Writer.Path)
	for _, want := range []string{"rtsp: yes", "rtmp: no", "srt: yes"} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("mediamtx.yml lacks %q", want)
		}
	}
	if _, err := os.Stat(s.TokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("the setup token survived setup")
	}
	if s.CheckToken("anything") {
		t.Error("a token check passed after setup")
	}
	if tok, err := s.EnsureToken(ctx); tok != "" || err != nil {
		t.Errorf("EnsureToken after setup = %q, %v", tok, err)
	}
	if _, _, err := s.Complete(ctx, Request{Username: "second", Password: "correct horse battery"}); !errors.Is(err, store.ErrSetupDone) {
		t.Errorf("second setup: %v", err)
	}
}

func TestCompleteRace(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.Complete(ctx, Request{Username: "admin" + string(rune('a'+i)), Password: "correct horse battery"})
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, store.ErrSetupDone) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d setups succeeded, want exactly 1", wins)
	}
}
