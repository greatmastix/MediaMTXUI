package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtxui/internal/store"
)

// fast keeps tests quick; production uses DefaultParams.
var fast = Params{Memory: 8 * 1024, Time: 1, Threads: 1}

func TestHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(fast, 2)
	enc, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", enc)
	}
	if ok, rehash, err := h.Verify(ctx, enc, "correct horse battery"); !ok || rehash || err != nil {
		t.Errorf("right password: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, err := h.Verify(ctx, enc, "correct horse batterY"); ok || err != nil {
		t.Errorf("wrong password: ok=%v err=%v", ok, err)
	}
	stronger := NewHasher(Params{Memory: 16 * 1024, Time: 1, Threads: 1}, 1)
	if ok, rehash, _ := stronger.Verify(ctx, enc, "correct horse battery"); !ok || !rehash {
		t.Errorf("old parameters: ok=%v rehash=%v, want a rehash", ok, rehash)
	}
	if err := h.VerifyDummy(ctx, "anything"); err != nil {
		t.Errorf("VerifyDummy: %v", err)
	}
}

func TestRejectsTamperedHashes(t *testing.T) {
	h := NewHasher(fast, 1)
	for _, enc := range []string{
		"", "plaintext", "$argon2i$v=19$m=8192,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=18$m=8192,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=4194304,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", // 4 GiB: refused, not attempted
		"$argon2id$v=19$m=8192,t=99,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=8192,t=1,p=1$!!!$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if ok, _, err := h.Verify(context.Background(), enc, "x"); ok || err == nil {
			t.Errorf("%q: ok=%v err=%v, want an error", enc, ok, err)
		}
	}
}

func TestHasherConcurrencyCapHonoursContext(t *testing.T) {
	h := NewHasher(fast, 1)
	h.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "password1234"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Hash while the slot is taken: %v, want the context's error", err)
	}
	<-h.sem
}

func TestValidation(t *testing.T) {
	for _, ok := range []string{"admin", "a1", "ops.team-1", "A_b"} {
		if err := ValidateUsername(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a", ".admin", "admin!", "user name", strings.Repeat("a", 33)} {
		if ValidateUsername(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := ValidatePassword("twelve chars", "admin"); err != nil {
		t.Errorf("12 characters: %v", err)
	}
	if ValidatePassword("elevenchars", "admin") == nil {
		t.Error("11 characters accepted")
	}
	if ValidatePassword("AdministratoR", "administrator") == nil {
		t.Error("password equal to the username accepted")
	}
	if ValidatePassword(strings.Repeat("x", 1025), "admin") == nil {
		t.Error("1025 bytes accepted")
	}
}

func TestRoles(t *testing.T) {
	if !RoleAdmin.AtLeast(RoleOperator) || !RoleOperator.AtLeast(RoleOperator) || RoleViewer.AtLeast(RoleOperator) {
		t.Error("role ordering is wrong")
	}
	if RoleStreamer.AtLeast(RoleViewer) || !RoleViewer.AtLeast(RoleStreamer) || !RoleStreamer.AtLeast(RoleStreamer) {
		t.Error("a streamer must be below viewer and every signed-in role at least a streamer")
	}
	if Role("root").AtLeast(RoleViewer) || Role("root").AtLeast(RoleStreamer) {
		t.Error("an unknown role includes viewer")
	}
	if _, err := ParseRole("root"); err == nil {
		t.Error("ParseRole accepted root")
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.UnixMilli(1_700_000_000_000)
	st.SetClock(func() time.Time { return now })
	user, err := st.CompleteSetup(ctx, "admin", "h")
	if err != nil {
		t.Fatal(err)
	}
	m := NewSessions(st, true, time.Hour, 24*time.Hour)
	if m.CookieName() != "__Host-mtxui_session" {
		t.Errorf("secure cookie name = %q", m.CookieName())
	}
	token, sess, err := m.Create(ctx, user, "password", "203.0.113.7", strings.Repeat("u", 500))
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.UserAgent) != 256 || sess.AuthMethod != "password" || !sess.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Errorf("session = %+v", sess)
	}
	if sess.IDHash == token || len(sess.IDHash) != 64 {
		t.Error("the database must hold only a hash of the token")
	}

	got, u, err := m.Lookup(ctx, token)
	if err != nil || got.IDHash != sess.IDHash || u.ID != user.ID {
		t.Fatalf("Lookup = %+v %+v %v", got, u, err)
	}
	if _, _, err := m.Lookup(ctx, token+"x"); !errors.Is(err, ErrNoSession) {
		t.Errorf("wrong token: %v", err)
	}
	if _, _, err := m.Lookup(ctx, ""); !errors.Is(err, ErrNoSession) {
		t.Errorf("empty token: %v", err)
	}

	// Activity within the idle timeout keeps the session alive...
	now = now.Add(50 * time.Minute)
	if _, _, err := m.Lookup(ctx, token); err != nil {
		t.Fatalf("after 50 idle minutes: %v", err)
	}
	now = now.Add(50 * time.Minute)
	if _, _, err := m.Lookup(ctx, token); err != nil {
		t.Fatalf("50 minutes after the last activity: %v", err)
	}
	// ...but an hour of silence ends it.
	now = now.Add(61 * time.Minute)
	if _, _, err := m.Lookup(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after 61 idle minutes: %v", err)
	}

	// The absolute lifetime ends a session however active it is.
	token, _, _ = m.Create(ctx, user, "password", "203.0.113.7", "ua")
	for range 25 {
		now = now.Add(59 * time.Minute)
		if _, _, err := m.Lookup(ctx, token); err != nil {
			if now.Before(time.UnixMilli(1_700_000_000_000).Add(24*time.Hour + 161*time.Minute)) {
				t.Fatalf("ended early at %v: %v", now, err)
			}
			break
		}
	}
	if _, _, err := m.Lookup(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("past the absolute lifetime: %v", err)
	}

	token, _, _ = m.Create(ctx, user, "password", "ip", "ua")
	if err := m.Destroy(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Lookup(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("after Destroy: %v", err)
	}
}

func TestSessionCookie(t *testing.T) {
	m := NewSessions(nil, false, time.Hour, time.Hour)
	rec := httptest.NewRecorder()
	m.SetCookie(rec, "tok", time.Now().Add(time.Hour))
	c := rec.Result().Cookies()[0]
	if c.Name != "mtxui_session" || !c.HttpOnly || c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("plain-HTTP cookie = %+v", c)
	}
	secure := NewSessions(nil, true, time.Hour, time.Hour)
	rec = httptest.NewRecorder()
	secure.SetCookie(rec, "tok", time.Now().Add(time.Hour))
	if c := rec.Result().Cookies()[0]; c.Name != "__Host-mtxui_session" || !c.Secure || c.Domain != "" {
		t.Errorf("HTTPS cookie = %+v", c)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-mtxui_session", Value: "tok"})
	if secure.TokenFrom(req) != "tok" || m.TokenFrom(req) != "" {
		t.Error("TokenFrom reads the wrong cookie")
	}
}

func TestSameOrigin(t *testing.T) {
	const origin = "https://mtx.example.com"
	tests := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"matching Origin", map[string]string{"Origin": origin}, true},
		{"other Origin", map[string]string{"Origin": "https://evil.example"}, false},
		{"null Origin", map[string]string{"Origin": "null"}, false},
		{"other port", map[string]string{"Origin": origin + ":8443"}, false},
		{"Referer fallback", map[string]string{"Referer": origin + "/setup"}, true},
		{"other Referer", map[string]string{"Referer": "https://evil.example/x"}, false},
		{"no headers", map[string]string{}, false},
		{"cross-site fetch", map[string]string{"Origin": origin, "Sec-Fetch-Site": "cross-site"}, false},
		{"same-site fetch", map[string]string{"Origin": origin, "Sec-Fetch-Site": "same-site"}, false},
		{"same-origin fetch", map[string]string{"Origin": origin, "Sec-Fetch-Site": "same-origin"}, true},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		for k, v := range tt.headers {
			req.Header.Set(k, v)
		}
		if got := SameOrigin(req, origin); got != tt.want {
			t.Errorf("%s: SameOrigin = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestValidCSRF(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if ValidCSRF(req, "tok") {
		t.Error("missing header accepted")
	}
	req.Header.Set(CSRFHeader, "tok")
	if !ValidCSRF(req, "tok") || ValidCSRF(req, "other") || ValidCSRF(req, "") {
		t.Error("CSRF comparison is wrong")
	}
}

func TestRateLimiter(t *testing.T) {
	l := NewRateLimiter(60, 3) // one per second, bursts of three
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	for i := range 3 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d of the burst refused", i+1)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("4th request: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("another key is limited too")
	}
	now = now.Add(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Error("no token after a second")
	}
}

func TestLockout(t *testing.T) {
	l := NewLockout(3, 15*time.Minute)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	key := LockKey("  Admin ")
	if key != "admin" {
		t.Fatalf("LockKey = %q", key)
	}
	for range 2 {
		if l.Fail(key) {
			t.Fatal("locked before the threshold")
		}
	}
	if !l.Fail(key) {
		t.Fatal("not locked at the threshold")
	}
	if locked, left := l.Locked(key); !locked || left != 15*time.Minute {
		t.Fatalf("Locked = %v, %v", locked, left)
	}
	now = now.Add(15 * time.Minute)
	if locked, _ := l.Locked(key); locked {
		t.Fatal("still locked after the duration")
	}
	// Failures spread wider than the window never add up to a lock.
	for range 5 {
		now = now.Add(16 * time.Minute)
		if l.Fail(key) {
			t.Fatal("locked by failures that were far apart")
		}
	}
	l.Fail(key)
	l.Succeed(key)
	if l.Fail(key) {
		t.Error("Succeed did not reset the count")
	}
}
