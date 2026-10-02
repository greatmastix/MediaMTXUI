package mtxconf

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The two versions the crash child writes, over and over; both are complete configs.
var (
	crashA = bytes.Repeat([]byte("# version A\nlogLevel: info\n"), 2000)
	crashB = bytes.Repeat([]byte("# version B\nlogLevel: debug\n"), 1500)
)

// TestCrashChild is the process that gets killed: it writes crashA and crashB alternately until it dies.
func TestCrashChild(t *testing.T) {
	path := os.Getenv("MTXCONF_CRASH_FILE")
	if path == "" {
		t.Skip("run by TestCrashDuringWrite")
	}
	for i := 0; ; i++ {
		content := crashA
		if i%2 == 1 {
			content = crashB
		}
		if err := writeAtomic(path, content); err != nil {
			t.Fatal(err)
		}
	}
}

// kill -9 at random moments during writes always leaves a complete old or new file, never a torn one; the temp
// files a killed write leaves behind are cleared by the next Reconcile.
func TestCrashDuringWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mediamtx.yml")
	if err := os.WriteFile(path, crashA, 0o640); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	midWrite := 0
	for i := range 40 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$")
		cmd.Env = append(os.Environ(), "MTXCONF_CRASH_FILE="+path)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(5+r.IntN(40)) * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if !bytes.Equal(got, crashA) && !bytes.Equal(got, crashB) {
			t.Fatalf("round %d: a torn file of %d bytes", i, len(got))
		}
		if temps, _ := filepath.Glob(filepath.Join(dir, tempPattern)); len(temps) > 0 {
			midWrite++ // killed between creating the temp file and the rename
			for _, f := range temps {
				_ = os.Remove(f)
			}
		}
	}
	t.Logf("%d of 40 kills landed in the middle of a write", midWrite)
	if midWrite == 0 {
		t.Error("no kill landed in the middle of a write: the test proves nothing")
	}

	// Leftover temp files are removed when the writer next reconciles (under its lock, so no write is running).
	w, _ := newWriter(t, fakeChecker{})
	w.Path = path
	_ = os.WriteFile(filepath.Join(dir, ".mediamtx.yml.tmp-123"), []byte("partial"), 0o600)
	w.removeStaleTemps()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mediamtx.yml.tmp-") {
			t.Errorf("a stale temp file survived: %s", e.Name())
		}
	}
}
