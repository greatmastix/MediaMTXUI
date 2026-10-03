package logs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A rotation that cannot write its copy (a full disk) leaves the older copies and the log as they are, however
// often it is retried; the next one that can write rotates as usual.
func TestRotateFailureKeepsCopies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mediamtx.log")
	const keep = 3
	writeLines(t, rotated(path, 2), 0, 100, true)
	writeLines(t, rotated(path, 1), 100, 200, true)
	writeLines(t, path, 200, 250, false)
	// The new copy is written next to the others first: a directory in its place makes writing it fail.
	blocker := rotated(path, 1) + ".tmp"
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	for range keep + 1 {
		if ok, err := Rotate(path, 100, keep); ok || err == nil {
			t.Fatalf("rotated without a copy: %v %v", ok, err)
		}
	}
	lines, _, err := Search(context.Background(), path, keep, Filter{}, 1000)
	if err != nil || len(lines) != 250 || lines[0].Text != "line 0" || lines[249].Text != "line 249" {
		t.Fatalf("after failed rotations: %d lines, err %v", len(lines), err)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if ok, err := Rotate(path, 100, keep); !ok || err != nil {
		t.Fatalf("rotation: %v %v", ok, err)
	}
	lines, _, err = Search(context.Background(), path, keep, Filter{}, 1000)
	if err != nil || len(lines) != 250 || lines[0].Text != "line 0" || lines[249].Text != "line 249" {
		t.Fatalf("after the rotation: %d lines, err %v", len(lines), err)
	}
	// The next one drops the oldest copy.
	writeLines(t, path, 250, 300, false)
	if ok, err := Rotate(path, 100, keep); !ok || err != nil {
		t.Fatalf("rotation: %v %v", ok, err)
	}
	lines, _, err = Search(context.Background(), path, keep, Filter{}, 1000)
	if err != nil || len(lines) != 200 || lines[0].Text != "line 100" || lines[199].Text != "line 299" {
		t.Fatalf("after the next rotation: %d lines, err %v", len(lines), err)
	}
	if _, err := os.Stat(rotated(path, keep+1)); err == nil {
		t.Fatal("more copies than keep")
	}
	if _, err := os.Stat(blocker); err == nil {
		t.Fatal("the temporary copy was left behind")
	}
}
