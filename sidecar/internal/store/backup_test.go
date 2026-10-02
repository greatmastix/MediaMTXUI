package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotAndInspect(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, err := s.CompleteSetup(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "copy.db")
	if err := s.Snapshot(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	in, err := s.Inspect(ctx, copyPath)
	if err != nil {
		t.Fatal(err)
	}
	mine, _ := s.SchemaVersion(ctx)
	if in.Schema != mine || len(in.Users) != 1 || in.Users[0] != "admin" || in.Admins != 1 {
		t.Fatalf("inspection %+v (schema %d)", in, mine)
	}

	// Not a database, a damaged one, one never set up, one with a foreign trigger: all refused.
	junk := filepath.Join(dir, "junk.db")
	_ = os.WriteFile(junk, []byte("not sqlite at all, not even close"), 0o600)
	if _, err := s.Inspect(ctx, junk); !errors.Is(err, ErrNotRestorable) {
		t.Errorf("junk: %v", err)
	}
	fresh := open(t)
	freshCopy := filepath.Join(dir, "fresh.db")
	_ = fresh.Snapshot(ctx, freshCopy)
	if _, err := s.Inspect(ctx, freshCopy); err == nil || !strings.Contains(err.Error(), "never set up") {
		t.Errorf("never set up: %v", err)
	}
	evilPath := filepath.Join(dir, "evil.db")
	_ = s.Snapshot(ctx, evilPath)
	evil, _ := sql.Open("sqlite", "file:"+evilPath)
	if _, err := evil.ExecContext(ctx, `CREATE TRIGGER x AFTER INSERT ON sessions BEGIN DELETE FROM users; END`); err != nil {
		t.Fatal(err)
	}
	_ = evil.Close()
	if _, err := s.Inspect(ctx, evilPath); err == nil || !strings.Contains(err.Error(), "trigger") {
		t.Errorf("a foreign trigger: %v", err)
	}
	newer := filepath.Join(dir, "newer.db")
	_ = s.Snapshot(ctx, newer)
	nd, _ := sql.Open("sqlite", "file:"+newer)
	_, _ = nd.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1)`)
	_ = nd.Close()
	if _, err := s.Inspect(ctx, newer); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("a newer schema: %v", err)
	}
	// A cut-off file.
	data, _ := os.ReadFile(copyPath)
	cut := filepath.Join(dir, "cut.db")
	_ = os.WriteFile(cut, data[:len(data)/2], 0o600)
	if _, err := s.Inspect(ctx, cut); !errors.Is(err, ErrNotRestorable) {
		t.Errorf("cut off: %v", err)
	}

	// Sessions go.
	if err := s.ClearSessions(ctx); err != nil {
		t.Fatal(err)
	}
}
