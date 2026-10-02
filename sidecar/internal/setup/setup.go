// Package setup runs first-run setup: the one-time setup token, and creating the first admin together with the
// ingest protocol choices. The web wizard needs the token; `mtxui setup` inside the container does not, because
// container access already means root on the host.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"mtxui/internal/auth"
	"mtxui/internal/mtxconf"
	"mtxui/internal/store"
)

// Ingest are the protocols that carry streams in and out; all are off until setup turns some on.
type Ingest struct {
	RTSP bool `json:"rtsp"`
	RTMP bool `json:"rtmp"`
	SRT  bool `json:"srt"`
}

// Request is what setup needs.
type Request struct {
	Username string
	Password string
	Ingest   Ingest
}

// ValidationError is a problem with the request that the user can fix.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// Service performs setup.
type Service struct {
	Store     *store.Store
	Hasher    *auth.Hasher
	Writer    *mtxconf.Writer
	Seed      func(Ingest) ([]byte, error)
	TokenPath string // state/setup-token
}

// Required reports whether setup is still pending.
func (s *Service) Required(ctx context.Context) (bool, error) {
	done, err := s.Store.SetupCompleted(ctx)
	return !done, err
}

var (
	b32         = base32.StdEncoding.WithPadding(base32.NoPadding)
	tokenFormat = regexp.MustCompile(`^[A-Z2-7]{32}$`)
)

// EnsureToken returns the setup token while setup is pending, creating it (mode 0600) on first use, so the token
// printed at the first start stays valid across restarts. Once setup is done it removes the file and returns "".
func (s *Service) EnsureToken(ctx context.Context) (string, error) {
	required, err := s.Required(ctx)
	if err != nil {
		return "", err
	}
	if !required {
		if err := os.Remove(s.TokenPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	if b, err := os.ReadFile(s.TokenPath); err == nil && tokenFormat.MatchString(normalize(string(b))) {
		return Format(normalize(string(b))), nil
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := b32.EncodeToString(raw)
	_ = os.Remove(s.TokenPath) // a malformed leftover
	f, err := os.OpenFile(s.TokenPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(Format(token) + "\n"); err != nil {
		_ = f.Close()
		return "", err
	}
	return Format(token), f.Close()
}

// Format groups a token as XXXX-XXXX-... for reading aloud and typing.
func Format(token string) string {
	var parts []string
	for i := 0; i < len(token); i += 4 {
		parts = append(parts, token[i:min(i+4, len(token))])
	}
	return strings.Join(parts, "-")
}

func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\n' || r == '\r' || r == '\t':
			return -1
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		}
		return r
	}, s)
}

// CheckToken reports whether given is the current setup token. Dashes, spaces and case do not matter.
func (s *Service) CheckToken(given string) bool {
	b, err := os.ReadFile(s.TokenPath)
	if err != nil {
		return false
	}
	want := normalize(string(b))
	got := normalize(given)
	return tokenFormat.MatchString(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// Complete creates the first admin, writes mediamtx.yml with the chosen protocols and retires the token. Of
// concurrent calls exactly one succeeds; the others get store.ErrSetupDone. If the admin was created but the config
// could not be written, it returns the user with a warning: the admin can fix the config once signed in.
func (s *Service) Complete(ctx context.Context, req Request) (store.User, string, error) {
	if err := auth.ValidateUsername(req.Username); err != nil {
		return store.User{}, "", ValidationError{err.Error()}
	}
	if err := auth.ValidatePassword(req.Password, req.Username); err != nil {
		return store.User{}, "", ValidationError{err.Error()}
	}
	seed, err := s.Seed(req.Ingest)
	if err != nil {
		return store.User{}, "", err
	}
	if err := s.Writer.Validate(ctx, seed); err != nil {
		return store.User{}, "", fmt.Errorf("the config for these choices does not validate: %w", err)
	}
	hash, err := s.Hasher.Hash(ctx, req.Password)
	if err != nil {
		return store.User{}, "", err
	}
	user, err := s.Store.CompleteSetup(ctx, req.Username, hash)
	if err != nil {
		return store.User{}, "", err
	}
	var warning string
	if _, err := s.Writer.Write(ctx, seed, user.Username, "setup"); err != nil {
		warning = "Setup is complete, but mediamtx.yml could not be updated: " + err.Error()
	}
	if err := os.Remove(s.TokenPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		warning = strings.TrimSpace(warning + " The setup token file could not be removed: " + err.Error())
	}
	return user, warning, nil
}
