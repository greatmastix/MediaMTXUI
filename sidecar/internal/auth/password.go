// Package auth holds UI authentication: password hashing, sessions, CSRF and origin checks, rate limits and lockout,
// and roles. Client IPs come from the clientip subpackage.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id parameters.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// DefaultParams take 263 ms on the deployment host.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
)

// Hasher hashes and verifies passwords with argon2id. It runs at most a fixed number of hashes at once, so a burst of
// sign-ins cannot exhaust memory (each hash holds Params.Memory), and lets at most maxWaiting more wait for a turn:
// beyond that a request is refused at once (ErrBusy) rather than queued behind a flood for as long as it lasts.
type Hasher struct {
	params  Params
	sem     chan struct{}
	waiting atomic.Int32
	dummy   string
}

// maxWaiting bounds the requests waiting for a hash.
const maxWaiting = 16

// ErrBusy is returned when too many requests already wait for a hash.
var ErrBusy = errors.New("the password hasher is busy")

// NewHasher returns a hasher running at most concurrency hashes at once.
func NewHasher(p Params, concurrency int) *Hasher {
	h := &Hasher{params: p, sem: make(chan struct{}, concurrency)}
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt)
	h.dummy = encode(p, salt, argon2.IDKey([]byte("dummy"), salt, p.Time, p.Memory, p.Threads, keyLen))
	return h
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	default:
	}
	if h.waiting.Add(1) > maxWaiting {
		h.waiting.Add(-1)
		return ErrBusy
	}
	defer h.waiting.Add(-1)
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

// Hash returns a PHC-formatted argon2id hash.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return encode(h.params, salt, argon2.IDKey([]byte(password), salt, h.params.Time, h.params.Memory, h.params.Threads, keyLen)), nil
}

// Verify checks password against an encoded hash. rehash reports that the hash uses other parameters than the
// hasher's, so the caller should store a fresh hash.
func (h *Hasher) Verify(ctx context.Context, encoded, password string) (ok, rehash bool, err error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want))) //nolint:gosec // len(want) is bounded by decode
	ok = subtle.ConstantTimeCompare(got, want) == 1
	return ok, ok && p != h.params, nil
}

// VerifyDummy does the work of a verification against a hash that matches nothing, so that a sign-in for an unknown
// username takes as long as one with a wrong password.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) error {
	_, _, err := h.Verify(ctx, h.dummy, password+"\x00")
	return err
}

func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

var errBadHash = errors.New("malformed password hash")

// decode parses a PHC argon2id hash. Bounds keep a tampered database row from making verification arbitrarily costly.
func decode(s string) (p Params, salt, key []byte, err error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return p, nil, nil, errBadHash
	}
	var m, t uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &threads); err != nil {
		return p, nil, nil, errBadHash
	}
	if m < 8*1024 || m > 1024*1024 || t < 1 || t > 20 || threads < 1 || threads > 16 {
		return p, nil, nil, errBadHash
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(salt) < 8 {
		return p, nil, nil, errBadHash
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(key) < 16 || len(key) > 64 {
		return p, nil, nil, errBadHash
	}
	return Params{Memory: m, Time: t, Threads: threads}, salt, key, nil
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,31}$`)

// ValidateUsername accepts 2 to 32 letters, digits, dots, underscores and hyphens, starting with a letter or digit.
func ValidateUsername(name string) error {
	if !usernamePattern.MatchString(name) {
		return errors.New("username must be 2 to 32 letters, digits, dots, underscores or hyphens, starting with a letter or digit")
	}
	return nil
}

// ValidatePassword enforces length only (NIST SP 800-63B): at least 12 characters, at most 1024 bytes.
func ValidatePassword(password, username string) error {
	switch {
	case utf8.RuneCountInString(password) < 12:
		return errors.New("password must be at least 12 characters")
	case len(password) > 1024:
		return errors.New("password must be at most 1024 bytes")
	case strings.EqualFold(password, username):
		return errors.New("password must not be the username")
	}
	return nil
}
