// Package credentials manages stream credentials: the secrets publishers and readers present to MediaMTX. The
// sidecar generates the secrets and stores only an HMAC under a key kept in the state directory, so a copy of the
// database alone does not allow testing guesses offline.
package credentials

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"mtxui/internal/pathname"
	"mtxui/internal/store"
)

// Actions a credential may carry. api and pprof are deliberately absent: the Control API can change MediaMTX's
// runtime config, so only the sidecar itself gets it.
var Actions = []string{"publish", "read", "playback", "metrics"}

// Kinds of credential: a name/secret pair (user and password in the client's URL), or a bearer token.
const (
	KindPassword = "password"
	KindToken    = "token"
)

// ReservedPrefix is refused for credential names; the sidecar's own principal uses it.
const ReservedPrefix = "mtxui-"

// Service creates, looks up and checks credentials.
type Service struct {
	store *store.Store
	key   []byte

	mu      sync.Mutex
	regexps map[string]*regexp.Regexp
	touched map[int64]time.Time
	touches chan int64
}

// New returns a service using the HMAC key.
func New(s *store.Store, key []byte) *Service {
	return &Service{store: s, key: key, regexps: map[string]*regexp.Regexp{}, touched: map[int64]time.Time{}}
}

// LoadKey reads the HMAC key from dir/credential-key, creating it (mode 0600) on first use.
func LoadKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, "credential-key")
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("%s: want 32 bytes, found %d", path, len(key))
		}
		return key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return LoadKey(dir) // another process created it first
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return key, f.Close()
}

func (s *Service) hash(secret string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("mtxui-credential-v1\x00"))
	m.Write([]byte(secret))
	return hex.EncodeToString(m.Sum(nil))
}

// Spec describes a credential to create.
type Spec struct {
	Name        string
	Kind        string
	Actions     []string
	Paths       []string // names or ~regex; empty means any path
	SourceCIDRs []string // IPs or CIDRs; empty means any source
	TTL         time.Duration
	Secret      string // optional; generated when empty
	CreatedBy   string
}

var (
	namePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	secretPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{16,128}$`)
	b32           = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// Add validates spec, stores the credential and returns its secret, which is not retrievable later.
func (s *Service) Add(ctx context.Context, spec Spec) (string, store.Credential, error) {
	c, err := normalize(spec)
	if err != nil {
		return "", store.Credential{}, err
	}
	secret := spec.Secret
	if secret == "" {
		b := make([]byte, 20)
		if _, err := rand.Read(b); err != nil {
			return "", store.Credential{}, err
		}
		secret = strings.ToLower(b32.EncodeToString(b)) // 32 characters, safe inside any URL
	} else if !secretPattern.MatchString(secret) {
		return "", store.Credential{}, errors.New("secret must be 16 to 128 letters, digits, dots, underscores, tildes or hyphens")
	}
	c.SecretHash = s.hash(secret)
	c.CreatedAt = s.store.Now()
	if spec.TTL > 0 {
		exp := c.CreatedAt.Add(spec.TTL)
		c.ExpiresAt = &exp
	}
	id, err := s.store.CreateCredential(ctx, c)
	if errors.Is(err, store.ErrExists) {
		return "", store.Credential{}, fmt.Errorf("a credential named %q (or with this secret) %w", c.Name, store.ErrExists)
	}
	if err != nil {
		return "", store.Credential{}, err
	}
	c.ID = id
	return secret, c, nil
}

func normalize(spec Spec) (store.Credential, error) {
	c := store.Credential{Name: spec.Name, Kind: spec.Kind, CreatedBy: spec.CreatedBy}
	if c.Kind == "" {
		c.Kind = KindPassword
	}
	var errs []error
	if !namePattern.MatchString(c.Name) {
		errs = append(errs, errors.New("name must be 1 to 64 letters, digits, dots, underscores or hyphens, starting with a letter or digit"))
	}
	if strings.HasPrefix(strings.ToLower(c.Name), ReservedPrefix) {
		errs = append(errs, fmt.Errorf("names starting with %q are reserved", ReservedPrefix))
	}
	if c.Kind != KindPassword && c.Kind != KindToken {
		errs = append(errs, fmt.Errorf("kind must be %q or %q", KindPassword, KindToken))
	}
	if len(spec.Actions) == 0 {
		errs = append(errs, errors.New("at least one action is required"))
	}
	for _, a := range spec.Actions {
		if !slices.Contains(Actions, a) {
			errs = append(errs, fmt.Errorf("action %q is not one of %v", a, Actions))
		} else if !slices.Contains(c.Actions, a) {
			c.Actions = append(c.Actions, a)
		}
	}
	for _, p := range spec.Paths {
		if err := pathname.ValidPattern(p); err != nil {
			errs = append(errs, fmt.Errorf("path %q: %w", p, err))
		} else if !slices.Contains(c.Paths, p) {
			c.Paths = append(c.Paths, p)
		}
	}
	for _, cidr := range spec.SourceCIDRs {
		pfx, err := parsePrefix(cidr)
		if err != nil {
			errs = append(errs, fmt.Errorf("source %q is not an IP or CIDR", cidr))
			continue
		}
		c.SourceCIDRs = append(c.SourceCIDRs, pfx.String())
	}
	if spec.TTL < 0 {
		errs = append(errs, errors.New("expiry must be in the future"))
	}
	return c, errors.Join(errs...)
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// List returns every credential.
func (s *Service) List(ctx context.Context) ([]store.Credential, error) {
	return s.store.ListCredentials(ctx)
}

// Revoke revokes a credential by name.
func (s *Service) Revoke(ctx context.Context, name string) error {
	return s.store.RevokeCredential(ctx, name, s.store.Now())
}

// ByPassword returns the password credential called name if secret matches it.
func (s *Service) ByPassword(ctx context.Context, name, secret string) (store.Credential, bool, error) {
	c, err := s.store.CredentialByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Credential{}, false, nil
	}
	if err != nil || c.Kind != KindPassword {
		return store.Credential{}, false, err
	}
	if !hmac.Equal([]byte(s.hash(secret)), []byte(c.SecretHash)) {
		return store.Credential{}, false, nil
	}
	return c, true, nil
}

// ByToken returns the token credential whose secret is token.
func (s *Service) ByToken(ctx context.Context, token string) (store.Credential, bool, error) {
	c, err := s.store.CredentialBySecretHash(ctx, s.hash(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.Credential{}, false, nil
	}
	if err != nil || c.Kind != KindToken {
		return store.Credential{}, false, err
	}
	return c, true, nil
}

// Check reports why c does not allow action on path from ip at now, or nil if it does.
func (s *Service) Check(c store.Credential, action, path string, ip netip.Addr, now time.Time) error {
	switch {
	case c.RevokedAt != nil:
		return errors.New("credential revoked")
	case c.ExpiresAt != nil && !now.Before(*c.ExpiresAt):
		return errors.New("credential expired")
	case !slices.Contains(c.Actions, action):
		return fmt.Errorf("credential does not allow %s", action)
	}
	if action != "metrics" && len(c.Paths) > 0 && !s.pathAllowed(c.Paths, path) {
		return fmt.Errorf("credential does not cover path %q", path)
	}
	if len(c.SourceCIDRs) > 0 {
		for _, cidr := range c.SourceCIDRs {
			if p, err := netip.ParsePrefix(cidr); err == nil && p.Contains(ip) {
				return nil
			}
		}
		return fmt.Errorf("credential does not allow source %s", ip)
	}
	return nil
}

func (s *Service) pathAllowed(patterns []string, path string) bool {
	for _, p := range patterns {
		if expr, ok := strings.CutPrefix(p, "~"); ok {
			if re := s.regexp(expr); re != nil && re.MatchString(path) {
				return true
			}
		} else if p == path {
			return true
		}
	}
	return false
}

// regexp compiles and caches a pattern the way MediaMTX's permissions do: the expression must match the whole path.
func (s *Service) regexp(expr string) *regexp.Regexp {
	s.mu.Lock()
	defer s.mu.Unlock()
	if re, ok := s.regexps[expr]; ok {
		return re
	}
	re, err := regexp.Compile(`^(?:` + expr + `)$`)
	if err != nil {
		re = nil
	}
	if len(s.regexps) < 10_000 {
		s.regexps[expr] = re
	}
	return re
}

// StartTouches records last-used times in the background, at most once a minute per credential, so that a busy
// database never delays an authentication answer. It returns when ctx ends.
func (s *Service) StartTouches(ctx context.Context) {
	s.mu.Lock()
	s.touches = make(chan int64, 256)
	ch := s.touches
	s.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-ch:
			_ = s.store.MarkCredentialUsed(ctx, id, s.store.Now())
		}
	}
}

// Touch queues a last-used update; it never blocks.
func (s *Service) Touch(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.store.Now()
	if s.touches == nil || now.Sub(s.touched[id]) < time.Minute {
		return
	}
	select {
	case s.touches <- id:
		s.touched[id] = now
	default:
	}
}
