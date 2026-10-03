package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "mtxui.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenCreatesPrivateFileAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mtxui.db")
	for range 2 { // the second open re-runs migrations as a no-op
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("database mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestSetupIsAtomicAndOnce(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if done, _ := s.SetupCompleted(ctx); done {
		t.Fatal("fresh database reports setup done")
	}

	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CompleteSetup(ctx, "admin"+string(rune('a'+i)), "hash")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var ok, done int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrSetupDone):
			done++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || done != 7 {
		t.Fatalf("%d setups succeeded and %d were refused, want 1 and 7", ok, done)
	}
	if done, _ := s.SetupCompleted(ctx); !done {
		t.Fatal("setup not marked complete")
	}
}

func TestUsers(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, err := s.CompleteSetup(ctx, "Admin", "h1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.UserByName(ctx, "ADMIN") // usernames are case-insensitive
	if err != nil || got.ID != u.ID || got.Role != "admin" || got.Username != "Admin" {
		t.Fatalf("UserByName = %+v, %v", got, err)
	}
	if _, err := s.UserByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if err := s.UpdatePasswordHash(ctx, u.ID, "h2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserByID(ctx, u.ID); got.PasswordHash != "h2" {
		t.Errorf("hash not updated: %q", got.PasswordHash)
	}
}

func TestSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, _ := s.CompleteSetup(ctx, "admin", "h")
	now := time.UnixMilli(1_700_000_000_000)
	sess := Session{
		IDHash: "abc", UserID: u.ID, CSRFToken: "csrf", AuthMethod: "password", CreatedAt: now,
		LastSeenAt: now, ExpiresAt: now.Add(time.Hour), IP: "203.0.113.7", UserAgent: "test",
	}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionByHash(ctx, "abc")
	if err != nil || got != sess {
		t.Fatalf("SessionByHash = %+v, %v; want %+v", got, err, sess)
	}
	if err := s.TouchSession(ctx, "abc", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SessionByHash(ctx, "abc"); !got.LastSeenAt.Equal(now.Add(time.Minute)) {
		t.Errorf("last seen = %v", got.LastSeenAt)
	}
	n, err := s.DeleteExpiredSessions(ctx, now.Add(2*time.Hour), now)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d, %v", n, err)
	}
	if _, err := s.SessionByHash(ctx, "abc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session still there: %v", err)
	}
}

func TestCredentials(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	exp := time.UnixMilli(1_800_000_000_000)
	c := Credential{
		Name: "cam1", Kind: "password", SecretHash: "h1", Actions: []string{"publish"}, Paths: []string{"cam1"},
		SourceCIDRs: []string{"10.0.0.0/8"}, ExpiresAt: &exp, CreatedAt: time.UnixMilli(1_700_000_000_000), CreatedBy: "cli",
	}
	id, err := s.CreateCredential(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCredential(ctx, c); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate name: %v", err)
	}
	dup := c
	dup.Name = "other"
	if _, err := s.CreateCredential(ctx, dup); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate secret hash: %v", err)
	}
	got, err := s.CredentialByName(ctx, "cam1")
	if err != nil || got.ID != id || got.Paths[0] != "cam1" || !got.ExpiresAt.Equal(exp) || got.RevokedAt != nil {
		t.Fatalf("CredentialByName = %+v, %v", got, err)
	}
	if got, err := s.CredentialBySecretHash(ctx, "h1"); err != nil || got.ID != id {
		t.Fatalf("CredentialBySecretHash = %+v, %v", got, err)
	}
	if err := s.RevokeCredential(ctx, "cam1", time.UnixMilli(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeCredential(ctx, "cam1", time.UnixMilli(2)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.CredentialByName(ctx, "cam1"); got.RevokedAt == nil || got.RevokedAt.UnixMilli() != 1 {
		t.Errorf("revoked at = %v, want the first revocation", got.RevokedAt)
	}
	if err := s.RevokeCredential(ctx, "nope", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking unknown: %v", err)
	}
	all, err := s.ListCredentials(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListCredentials = %v, %v", all, err)
	}
}

func TestAuditIsAppendOnly(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	uid := int64(7)
	if err := s.InsertAudit(ctx, AuditEvent{
		Actor: "admin", ActorUserID: &uid, IP: "203.0.113.7", Action: "auth.login",
		Target: "admin", Details: map[string]any{"method": "password"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAudit(ctx, AuditEvent{Actor: "cli", IP: "", Action: "credential.add", Target: "cam1"}); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListAudit(ctx, 10)
	if err != nil || len(events) != 2 || events[0].Action != "credential.add" || events[1].Details["method"] != "password" {
		t.Fatalf("ListAudit = %+v, %v", events, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_log SET actor = 'mallory'`); err == nil {
		t.Error("audit_log accepted an UPDATE")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM audit_log`); err == nil {
		t.Error("audit_log accepted a DELETE")
	}
}

func TestSnapshots(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.LatestSnapshot(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty: %v", err)
	}
	a, err := s.InsertSnapshot(ctx, []byte("a: 1\n"), "system", "seed")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.InsertSnapshot(ctx, []byte("a: 2\n"), "admin", "setup")
	if err != nil {
		t.Fatal(err)
	}
	if b.ParentID == nil || *b.ParentID != a.ID {
		t.Errorf("parent = %v, want %d", b.ParentID, a.ID)
	}
	latest, err := s.LatestSnapshot(ctx)
	if err != nil || latest.ID != b.ID || string(latest.Content) != "a: 2\n" || len(latest.SHA256) != 64 {
		t.Fatalf("LatestSnapshot = %+v, %v", latest, err)
	}
	c, _ := s.InsertSnapshot(ctx, []byte("a: 3\n"), "admin", "edit")
	list, err := s.ListSnapshots(ctx, 0, 2)
	if err != nil || len(list) != 2 || list[0].ID != c.ID || list[1].ID != b.ID || list[0].Content != nil {
		t.Fatalf("ListSnapshots = %+v, %v", list, err)
	}
	if older, _ := s.ListSnapshots(ctx, b.ID, 10); len(older) != 1 || older[0].ID != a.ID || older[0].Author != "system" {
		t.Errorf("before %d: %+v", b.ID, older)
	}
	if got, err := s.SnapshotByID(ctx, a.ID); err != nil || string(got.Content) != "a: 1\n" || got.ParentID != nil {
		t.Errorf("SnapshotByID = %+v, %v", got, err)
	}
	if _, err := s.SnapshotByID(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing snapshot: %v", err)
	}
}

func TestMeta(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, ok, err := s.Meta(ctx, "k"); ok || err != nil {
		t.Fatalf("missing key: %v %v", ok, err)
	}
	_ = s.SetMeta(ctx, "k", "1")
	_ = s.SetMeta(ctx, "k", "2")
	if v, ok, _ := s.Meta(ctx, "k"); !ok || v != "2" {
		t.Errorf("Meta = %q %v", v, ok)
	}
}

func TestLayouts(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, err := s.CompleteSetup(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLayout(ctx, u.ID, "wall", `{"version":1}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLayout(ctx, u.ID, "wall", `{"version":1,"columns":3}`); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Layouts(ctx, u.ID)
	if len(list) != 1 || list[0].Data != `{"version":1,"columns":3}` {
		t.Fatalf("layouts %+v", list)
	}
	for i := range maxLayouts - 1 {
		_ = s.SaveLayout(ctx, u.ID, fmt.Sprint("l", i), "{}")
	}
	if err := s.SaveLayout(ctx, u.ID, "one too many", "{}"); !errors.Is(err, ErrTooMany) {
		t.Errorf("over the cap: %v", err)
	}
	if err := s.SaveLayout(ctx, u.ID, "wall", "{}"); err != nil {
		t.Errorf("replacing at the cap: %v", err)
	}
	if err := s.DeleteLayout(ctx, u.ID, "wall"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLayout(ctx, u.ID, "wall"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

// A database from the previous release (schema version 1, with data) migrates forward and keeps its data.
func TestMigrationFromVersion1(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := fs.Sub(migrations, "migrations")
	p, err := goose.NewProvider(goose.DialectSQLite3, db, sub, goose.WithGoMigrations(goMigrations()...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES ('old', 'h', 'admin', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.UserByName(ctx, "old")
	if err != nil {
		t.Fatalf("the version-1 user is gone: %v", err)
	}
	if err := s.SaveLayout(ctx, u.ID, "wall", "{}"); err != nil {
		t.Errorf("layouts after the migration: %v", err)
	}
}

// Version 3 rebuilds users (for the streamer role): every user, session, layout and credential survives, and the
// foreign keys still cascade afterwards.
func TestMigrationFromVersion2(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := fs.Sub(migrations, "migrations")
	p, err := goose.NewProvider(goose.DialectSQLite3, db, sub, goose.WithGoMigrations(goMigrations()...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (7, 'old', 'h', 'operator', 1, 2)`,
		`INSERT INTO sessions (id_hash, user_id, csrf_token, auth_method, created_at, last_seen_at, expires_at, ip, user_agent)
		 VALUES ('s1', 7, 'c', 'password', 1, 1, 9999999999999, '203.0.113.1', 'ua')`,
		`INSERT INTO user_layouts (user_id, name, data, updated_at) VALUES (7, 'wall', '{}', 1)`,
		`INSERT INTO stream_credentials (name, kind, secret_hash, actions, paths, source_cidrs, created_at, created_by)
		 VALUES ('cam', 'password', 'x', '[]', '[]', '[]', 1, 'cli')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.UserByID(ctx, 7)
	if err != nil || u.Username != "old" || u.Role != "operator" || u.UpdatedAt.UnixMilli() != 2 {
		t.Fatalf("user after the migration: %+v %v", u, err)
	}
	if _, err := s.SessionByHash(ctx, "s1"); err != nil {
		t.Errorf("session: %v", err)
	}
	if l, err := s.Layouts(ctx, 7); err != nil || len(l) != 1 {
		t.Errorf("layouts: %v %v", l, err)
	}
	if _, err := s.CredentialByName(ctx, "cam"); err != nil {
		t.Errorf("credential: %v", err)
	}
	if _, err := s.CreateUser(ctx, "streamer1", "h", "streamer"); err != nil {
		t.Errorf("the streamer role: %v", err)
	}
	if _, err := s.CreateUser(ctx, "root1", "h", "root"); err == nil {
		t.Error("the CHECK constraint is gone")
	}
	if err := s.DeleteUser(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByHash(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting the user no longer cascades to sessions: %v", err)
	}
	if l, _ := s.Layouts(ctx, 7); len(l) != 0 {
		t.Errorf("deleting the user no longer cascades to layouts: %v", l)
	}
}

func TestStreamsAndJoinCodes(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	u, err := s.CreateUser(ctx, "alice", "", "streamer")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.CreateStream(ctx, Stream{Name: "live/alice", Title: "Alice", OwnerID: &u.ID, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateStream(ctx, Stream{Name: "live/alice", Title: "Again", CreatedBy: "admin"}); !errors.Is(err, ErrExists) {
		t.Errorf("a duplicate name: %v", err)
	}
	cred, err := s.CreateCredential(ctx, Credential{Name: "key-a", Kind: "password", SecretHash: "h1", CreatedAt: now, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStreamKey(ctx, st.ID, KeyPublish, cred, "enc"); err != nil {
		t.Fatal(err)
	}
	got, err := s.StreamByID(ctx, st.ID)
	if err != nil || got.PublishKeyID == nil || *got.PublishKeyID != cred || got.PublishKeyEnc != "enc" || got.PlaybackKeyID != nil ||
		got.OwnerID == nil || *got.OwnerID != u.ID {
		t.Fatalf("stream %+v %v", got, err)
	}
	got.Title = "Alice live"
	if err := s.UpdateStream(ctx, got); err != nil {
		t.Fatal(err)
	}

	if err := s.CreateJoinCode(ctx, u.ID, "code1", now.Add(time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateJoinCode(ctx, u.ID, "code2", now.Add(time.Hour), "admin"); err != nil { // replaces code1
		t.Fatal(err)
	}
	if _, err := s.RedeemJoinCode(ctx, "code1", "pw"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a replaced code: %v", err)
	}
	if who, err := s.JoinCodeUser(ctx, "code2"); err != nil || who.ID != u.ID {
		t.Errorf("code lookup: %+v %v", who, err)
	}
	if _, err := s.JoinCodeUser(ctx, "code1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("lookup of a replaced code: %v", err)
	}
	if exp, err := s.JoinCodeExpiry(ctx, u.ID); err != nil || exp == nil {
		t.Errorf("pending code: %v %v", exp, err)
	}
	joined, err := s.RedeemJoinCode(ctx, "code2", "pw-hash")
	if err != nil || joined.ID != u.ID || joined.PasswordHash != "pw-hash" {
		t.Fatalf("redeem: %+v %v", joined, err)
	}
	if _, err := s.RedeemJoinCode(ctx, "code2", "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a used code: %v", err)
	}
	if err := s.CreateJoinCode(ctx, u.ID, "code3", now.Add(time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := s.RedeemJoinCode(ctx, "code3", "late"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired code: %v", err)
	}

	// Deleting the owner keeps the stream, without an owner.
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StreamByID(ctx, st.ID); got.OwnerID != nil || got.Title != "Alice live" {
		t.Errorf("after deleting the owner: %+v", got)
	}
	if list, _ := s.Streams(ctx); len(list) != 1 {
		t.Errorf("streams %v", list)
	}
	if err := s.DeleteStream(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamByID(ctx, st.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted stream: %v", err)
	}
}

func TestEncoderAddresses(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	st, err := s.CreateStream(ctx, Stream{Name: "live/a", Title: "A", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4"} {
		if err := s.TouchEncoderAddress(ctx, EncoderAddress{StreamID: st.ID, Port: "rtmp", IP: ip, LastSeen: t0.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	// .1 is seen again: it becomes the newest, and .2 is the one dropped instead.
	if err := s.TouchEncoderAddress(ctx, EncoderAddress{StreamID: st.ID, Port: "rtmp", IP: "198.51.100.1", LastSeen: t0.Add(5 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.EncoderAddresses(ctx, t0)
	if err != nil || len(got) != 3 || got[0].IP != "198.51.100.1" || got[1].IP != "198.51.100.4" || got[2].IP != "198.51.100.3" {
		t.Fatalf("addresses %+v %v", got, err)
	}
	if recent, _ := s.EncoderAddresses(ctx, t0.Add(4*time.Hour)); len(recent) != 1 {
		t.Errorf("since: %+v", recent)
	}
	if err := s.DeleteStream(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.EncoderAddresses(ctx, t0); len(left) != 0 {
		t.Errorf("addresses outlived their stream: %+v", left)
	}
}

// A database a newer release migrated is refused, not run on a schema this release was not built for.
func TestNewerDatabaseIsRefused(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1)`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if _, err := Open(ctx, path); !errors.Is(err, ErrNewerDatabase) || !strings.Contains(err.Error(), "9999") {
		t.Fatalf("a newer database: %v", err)
	}
}
