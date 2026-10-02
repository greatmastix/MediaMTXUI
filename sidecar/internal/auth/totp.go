package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238's default, which every authenticator app speaks; HMAC-SHA1 is not broken
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238) as authenticator apps speak it: HMAC-SHA1, 6 digits, 30-second steps. A code is accepted for the
// current step and one either side (clock drift); the caller records the step it matched, so a code works once.

const (
	totpStep   = 30
	totpDigits = 6
)

var totpB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh secret, base32 as apps take it.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return totpB32.EncodeToString(b), nil
}

// TOTPCode is the code for a secret at a time step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := totpB32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // steps are positive
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1_000_000), nil
}

// TOTPStep is the time step of a moment.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpStep }

// TOTPMatch checks a code against the steps around now and returns the one it matches.
func TOTPMatch(secret, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	matched, found := int64(0), false
	for _, step := range []int64{cur - 1, cur, cur + 1} {
		want, err := TOTPCode(secret, step)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && !found {
			matched, found = step, true
		}
	}
	return matched, found
}

// TOTPURI is the otpauth:// URI an app scans from a QR code.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{
		"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"},
		"digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpStep)},
	}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// NewRecoveryCodes returns n single-use codes like "k7qmz-x2b4r" (50 random bits each).
func NewRecoveryCodes(n int) ([]string, error) {
	enc := base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding) // no l, o, 0, 1
	out := make([]string, 0, n)
	for range n {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		s := enc.EncodeToString(b)[:10]
		out = append(out, s[:5]+"-"+s[5:])
	}
	return out, nil
}

// RecoveryHash is what is stored of a recovery code: high-entropy and single-use, so a plain hash is enough.
func RecoveryHash(code string) string {
	norm := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}
