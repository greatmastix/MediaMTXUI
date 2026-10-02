package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B (SHA-1, the last six digits of its eight).
func TestTOTPVectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for _, tc := range []struct {
		unix int64
		code string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"},
	} {
		got, err := TOTPCode(secret, tc.unix/30)
		if err != nil || got != tc.code {
			t.Errorf("at %d: %s %v, want %s", tc.unix, got, err, tc.code)
		}
	}
}

func TestTOTPMatch(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	cur := TOTPStep(now)
	for _, d := range []int64{-1, 0, 1} {
		code, _ := TOTPCode(secret, cur+d)
		if step, ok := TOTPMatch(secret, " "+code[:3]+" "+code[3:], now); !ok || step != cur+d {
			t.Errorf("drift %d: %d %v", d, step, ok)
		}
	}
	for _, d := range []int64{-2, 2} {
		code, _ := TOTPCode(secret, cur+d)
		if _, ok := TOTPMatch(secret, code, now); ok {
			t.Errorf("drift %d accepted", d)
		}
	}
	if _, ok := TOTPMatch(secret, "12345", now); ok {
		t.Error("five digits accepted")
	}
	uri := TOTPURI("MediaMTX UI", "alice", secret)
	if !strings.HasPrefix(uri, "otpauth://totp/MediaMTX%20UI:alice?") || !strings.Contains(uri, "secret="+secret) {
		t.Errorf("uri %s", uri)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := NewRecoveryCodes(10)
	if err != nil || len(codes) != 10 {
		t.Fatal(codes, err)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || strings.ContainsAny(c, "lo01") || seen[c] {
			t.Errorf("code %q", c)
		}
		seen[c] = true
	}
	if RecoveryHash(codes[0]) != RecoveryHash(" "+strings.ToUpper(strings.ReplaceAll(codes[0], "-", ""))+" ") {
		t.Error("the hash depends on case, dashes or spaces")
	}
}
