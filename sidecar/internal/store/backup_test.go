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

// Two admins demoting each other at the same moment: one change goes through, the other is refused, and an admin
// remains. A disabled admin can go while another can still sign in.
func TestChangeUserKeepsAnAdmin(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a, err := s.CompleteSetup(ctx, "alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateUser(ctx, "bob", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	viewer := "viewer"
	errs := make(chan error, 2)
	for _, id := range []int64{a.ID, b.ID} {
		go func() { errs <- s.ChangeUser(ctx, id, UserChange{Role: &viewer}) }()
	}
	e1, e2 := <-errs, <-errs
	if (e1 == nil) == (e2 == nil) || (!errors.Is(e1, ErrLastAdmin) && !errors.Is(e2, ErrLastAdmin)) {
		t.Fatalf("both or neither went through: %v, %v", e1, e2)
	}
	users, _ := s.Users(ctx)
	admins := 0
	for _, u := range users {
		if u.Role == "admin" {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("%d admins left", admins)
	}
	// Whoever is still an admin stays; a disabled admin can be deleted.
	c, _ := s.CreateUser(ctx, "carol", "hash", "admin")
	yes := true
	if err := s.ChangeUser(ctx, c.ID, UserChange{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeUser(ctx, c.ID, UserChange{Delete: true}); err != nil {
		t.Fatalf("deleting a disabled admin: %v", err)
	}
}
