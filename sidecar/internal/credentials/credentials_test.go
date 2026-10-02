package credentials

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtxui/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, key), st
}

func TestLoadKeyCreatesOncePrivately(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadKey(dir)
	if err != nil || string(a) != string(b) || len(a) != 32 {
		t.Fatalf("second load: %v, same=%v", err, string(a) == string(b))
	}
	fi, _ := os.Stat(filepath.Join(dir, "credential-key"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v, want 0600", fi.Mode().Perm())
	}
	_ = os.WriteFile(filepath.Join(dir, "credential-key"), []byte("short"), 0o600)
	if _, err := LoadKey(dir); err == nil {
		t.Error("a truncated key was accepted")
	}
}

func TestAddAndLookup(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	secret, c, err := s.Add(ctx, Spec{Name: "cam1", Actions: []string{"publish", "publish"}, Paths: []string{"cam1"}, CreatedBy: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 || strings.ToLower(secret) != secret || c.Kind != KindPassword || len(c.Actions) != 1 {
		t.Errorf("secret %q credential %+v", secret, c)
	}
	stored, _ := st.CredentialByName(ctx, "cam1")
	if stored.SecretHash == secret || strings.Contains(stored.SecretHash, secret) {
		t.Fatal("the secret is stored in the clear")
	}
	if got, ok, err := s.ByPassword(ctx, "cam1", secret); !ok || err != nil || got.ID != c.ID {
		t.Errorf("ByPassword right secret: %v %v", ok, err)
	}
	if _, ok, _ := s.ByPassword(ctx, "cam1", secret+"x"); ok {
		t.Error("ByPassword accepted a wrong secret")
	}
	if _, ok, _ := s.ByPassword(ctx, "nobody", secret); ok {
		t.Error("ByPassword accepted an unknown name")
	}
	if _, ok, _ := s.ByToken(ctx, secret); ok {
		t.Error("a password credential worked as a bearer token")
	}

	tok, tc, err := s.Add(ctx, Spec{Name: "viewer-1", Kind: KindToken, Actions: []string{"read"}, TTL: time.Hour, CreatedBy: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.ByToken(ctx, tok); !ok || got.ID != tc.ID || got.ExpiresAt == nil {
		t.Errorf("ByToken: %v %+v", ok, got)
	}
	if _, ok, _ := s.ByPassword(ctx, "viewer-1", tok); ok {
		t.Error("a token credential worked as a password")
	}

	if _, _, err := s.Add(ctx, Spec{Name: "dev", Actions: []string{"publish"}, Secret: "dev-publisher-secret"}); err != nil {
		t.Errorf("a valid supplied secret was refused: %v", err)
	}
	if _, _, err := s.Add(ctx, Spec{Name: "cam1", Actions: []string{"read"}}); err == nil {
		t.Error("a duplicate name was accepted")
	}
}

func TestAddRejects(t *testing.T) {
	s, _ := newService(t)
	tests := map[string]Spec{
		"no actions":         {Name: "a"},
		"api action":         {Name: "a", Actions: []string{"api"}},
		"pprof action":       {Name: "a", Actions: []string{"pprof"}},
		"reserved name":      {Name: "MTXUI-sidecar", Actions: []string{"read"}},
		"bad name":           {Name: "a b", Actions: []string{"read"}},
		"bad kind":           {Name: "a", Kind: "cert", Actions: []string{"read"}},
		"path traversal":     {Name: "a", Actions: []string{"read"}, Paths: []string{"../etc"}},
		"bad regex":          {Name: "a", Actions: []string{"read"}, Paths: []string{"~("}},
		"bad CIDR":           {Name: "a", Actions: []string{"read"}, SourceCIDRs: []string{"10.0.0.0/33"}},
		"short secret":       {Name: "a", Actions: []string{"read"}, Secret: "short"},
		"secret with @":      {Name: "a", Actions: []string{"read"}, Secret: "user@host:password1"},
		"expiry in the past": {Name: "a", Actions: []string{"read"}, TTL: -time.Hour},
	}
	for name, spec := range tests {
		if _, _, err := s.Add(context.Background(), spec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheck(t *testing.T) {
	s, _ := newService(t)
	now := time.Unix(1_700_000_000, 0)
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	ip := netip.MustParseAddr("203.0.113.7")
	base := store.Credential{Actions: []string{"publish", "metrics"}, Paths: []string{"cam1", "~^live/[a-z]+$"}}
	tests := []struct {
		name   string
		mod    func(*store.Credential)
		action string
		path   string
		ip     netip.Addr
		ok     bool
	}{
		{"exact path", nil, "publish", "cam1", ip, true},
		{"regex path", nil, "publish", "live/front", ip, true},
		{"regex is anchored", nil, "publish", "live/front/x", ip, false},
		{"regex is anchored at the start", nil, "publish", "xlive/front", ip, false},
		{"other path", nil, "publish", "cam2", ip, false},
		{"action not granted", nil, "read", "cam1", ip, false},
		{"metrics ignore the path scope", nil, "metrics", "", ip, true},
		{"revoked", func(c *store.Credential) { c.RevokedAt = &past }, "publish", "cam1", ip, false},
		{"expired", func(c *store.Credential) { c.ExpiresAt = &past }, "publish", "cam1", ip, false},
		{"not yet expired", func(c *store.Credential) { c.ExpiresAt = &future }, "publish", "cam1", ip, true},
		{"any path when unscoped", func(c *store.Credential) { c.Paths = nil }, "publish", "anything", ip, true},
		{"source allowed", func(c *store.Credential) { c.SourceCIDRs = []string{"203.0.113.0/24"} }, "publish", "cam1", ip, true},
		{"source refused", func(c *store.Credential) { c.SourceCIDRs = []string{"10.0.0.0/8"} }, "publish", "cam1", ip, false},
	}
	for _, tt := range tests {
		c := base
		if tt.mod != nil {
			tt.mod(&c)
		}
		if err := s.Check(c, tt.action, tt.path, tt.ip, now); (err == nil) != tt.ok {
			t.Errorf("%s: Check = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestTouchIsAsyncAndDebounced(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, st := newService(t)
	_, c, err := s.Add(ctx, Spec{Name: "cam1", Actions: []string{"publish"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Touch(c.ID) // before StartTouches: dropped, never blocks
	go s.StartTouches(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.Touch(c.ID)
		if got, _ := st.CredentialByName(ctx, "cam1"); got.LastUsedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("last used was never recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSeal(t *testing.T) {
	s := New(nil, make([]byte, 32))
	sealed, err := s.Seal("s3cr3t-value-1234", "stream 1 publish")
	if err != nil || strings.Contains(sealed, "s3cr3t") {
		t.Fatalf("seal: %q %v", sealed, err)
	}
	if got, err := s.Unseal(sealed, "stream 1 publish"); err != nil || got != "s3cr3t-value-1234" {
		t.Fatalf("unseal: %q %v", got, err)
	}
	if _, err := s.Unseal(sealed, "stream 2 publish"); err == nil {
		t.Error("a sealed secret opened for another stream")
	}
	other := New(nil, append(make([]byte, 31), 1))
	if _, err := other.Unseal(sealed, "stream 1 publish"); err == nil {
		t.Error("a sealed secret opened under another key")
	}
	if again, _ := s.Seal("s3cr3t-value-1234", "stream 1 publish"); again == sealed {
		t.Error("sealing is deterministic")
	}
}
