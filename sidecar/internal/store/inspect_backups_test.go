package store

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// A backup's database is migrated at the check, as the start after the restore would do, and must then have exactly
// this server's triggers and views: one whose migrations fail, or that lacks the audit log's append-only triggers,
// is refused before anything is swapped in.
func TestInspectMigratesTheBackup(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, err := s.CompleteSetup(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	mine, _ := s.SchemaVersion(ctx)
	dir := t.TempDir()
	// copyWith snapshots the server's database and changes the copy with stmts (the triggers dropped first, so the
	// statements may touch the audit log).
	copyWith := func(name string, stmts ...string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := s.Snapshot(ctx, path); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, q := range stmts {
			if _, err := db.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		return path
	}
	for _, tc := range []struct {
		name  string
		stmts []string
		want  string // in the error; empty: accepted
	}{
		{"as made", nil, ""},
		{"no append-only delete trigger", []string{`DROP TRIGGER audit_log_no_delete`}, "lacks this server's trigger audit_log_no_delete"},
		{"no append-only triggers at all", []string{`DROP TRIGGER audit_log_no_delete`, `DROP TRIGGER audit_log_no_update`}, "lacks"},
		// Schema 12 by its records, but with a table migration 13 creates: the start after the restore would fail.
		{"a migration fails", []string{`DELETE FROM goose_db_version WHERE version_id = 13`, `DROP TABLE path_history_minutes`}, "migrations fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := copyWith(strings.ReplaceAll(tc.name, " ", "-")+".db", tc.stmts...)
			_, err := s.Inspect(ctx, path)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				for _, side := range []string{"-wal", "-shm", "-journal"} { // the restore moves the database file alone
					if _, err := os.Stat(path + side); err == nil {
						t.Errorf("the check left %s%s", filepath.Base(path), side)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
		})
	}

	// A backup from the first schema passes, and is this server's schema afterwards, triggers included.
	old := filepath.Join(dir, "v1.db")
	db, err := sql.Open("sqlite", "file:"+old+"?_pragma=foreign_keys(1)")
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
	for _, q := range []string{
		`INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES ('old', 'h', 'admin', 1, 1)`,
		`INSERT INTO meta (key, value) VALUES ('` + metaSetupCompleted + `', '1')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	in, err := s.Inspect(ctx, old)
	if err != nil || in.Schema != 1 || !slices.Equal(in.Users, []string{"old"}) {
		t.Fatalf("a version-1 backup: %+v %v", in, err)
	}
	db, err = sql.Open("sqlite", "file:"+old+"?mode=ro") // not Open, which would migrate it
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objects, _ := schemaObjects(ctx, db)
	if v, _ := schemaVersion(ctx, db); v != mine || len(objects) != 2 {
		t.Fatalf("after the check, the backup is at schema %d (want %d), with %v", v, mine, objects)
	}
}

// A restore keeps the audit log of the database it replaced: the entries the backup lacks follow the backup's own,
// an entry deleted from or changed in the backup comes back as it was, and nothing is added twice.
func TestCarryAudit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	replacedPath := filepath.Join(dir, "pre-restore.db")
	replaced, err := Open(ctx, replacedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer replaced.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	uid := int64(1)
	add := func(s *Store, action, target string) {
		t.Helper()
		at = at.Add(time.Minute)
		if err := s.InsertAudit(ctx, AuditEvent{At: at, Actor: "admin", ActorUserID: &uid, IP: "203.0.113.7", Action: action, Target: target}); err != nil {
			t.Fatal(err)
		}
	}
	add(replaced, "user.create", "mallory")
	add(replaced, "stream.create", "cam1")
	path := filepath.Join(dir, "backup.db")
	if err := replaced.Snapshot(ctx, path); err != nil {
		t.Fatal(err)
	}
	add(replaced, "stream.delete", "cam1") // after the backup
	add(replaced, "backup.restore", "the backup")

	// The backup someone edited: the first entry gone.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TRIGGER audit_log_no_delete`, `DELETE FROM audit_log WHERE target = 'mallory'`,
		`CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	restored, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	_ = replaced.Close() // as at the start after a restore

	want := []string{"stream.create cam1", "user.create mallory", "stream.delete cam1", "backup.restore the backup"}
	for run := range 2 { // the second run adds nothing
		n, err := restored.CarryAudit(ctx, replacedPath)
		if err != nil {
			t.Fatal(err)
		}
		if wantN := int64(3 * (1 - run)); n != wantN {
			t.Errorf("run %d carried %d entries, want %d", run, n, wantN)
		}
		events, _ := restored.ListAudit(ctx, 100)
		var got []string
		for _, e := range slices.Backward(events) { // oldest first
			got = append(got, e.Action+" "+e.Target)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("run %d: the log is\n%q\nwant\n%q", run, got, want)
		}
	}
	if n, err := restored.CarryAudit(ctx, filepath.Join(dir, "missing.db")); err == nil || n != 0 {
		t.Errorf("a missing file: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.db")); err == nil {
		t.Error("carrying from a missing file created it")
	}
}

// The backup key's two halves are stored together or not at all.
func TestSetMetasIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.SetMetas(ctx, map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER no_b BEFORE UPDATE ON meta WHEN new.key = 'b' BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMetas(ctx, map[string]string{"a": "3", "b": "4"}); err == nil {
		t.Fatal("the failing write went through")
	}
	if v, _, _ := s.Meta(ctx, "a"); v != "1" {
		t.Errorf("a = %q after a failed write of both, want the old value", v)
	}
}
