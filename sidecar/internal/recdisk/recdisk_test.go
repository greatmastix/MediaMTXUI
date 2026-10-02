package recdisk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeasure(t *testing.T) {
	root := t.TempDir()
	write := func(rel string, n int) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("live/woo/2026-10-01_12-00-00-000000.mp4", 1000)
	write("live/woo/2026-10-01_12-01-00-000000.mp4", 500)
	write("cam/2026-10-01_12-00-00-000000.ts", 300)
	write("filler", 5000)     // not a segment
	write("stray.mp4", 7000)  // not in a path's directory
	write("cam/notes.txt", 9) // not a segment
	u, err := Measure(root)
	if err != nil {
		t.Fatal(err)
	}
	if u.ByDir["live/woo"] != 1500 || u.ByDir["cam"] != 300 || u.Bytes != 1800 || len(u.ByDir) != 2 {
		t.Errorf("usage %+v", u)
	}
	if u.Total <= 0 || u.Free <= 0 || u.Free > u.Total {
		t.Errorf("filesystem %d free of %d", u.Free, u.Total)
	}
	if _, err := Measure(filepath.Join(root, "missing")); err == nil {
		t.Error("a missing root measured")
	}
}
