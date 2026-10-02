// Package mtxtest runs the pinned MediaMTX binary for integration tests. Tests skip unless MTXUI_TEST_MEDIAMTX_BIN
// points at it; `./dev test` extracts the binary from the pinned image and sets it.
package mtxtest

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Bin returns the MediaMTX binary, or skips the test.
func Bin(t testing.TB) string {
	t.Helper()
	bin := os.Getenv("MTXUI_TEST_MEDIAMTX_BIN")
	if bin == "" {
		t.Skip("MTXUI_TEST_MEDIAMTX_BIN is not set (./dev test sets it)")
	}
	return bin
}

// Reference returns the pinned release's reference mediamtx.yml, or skips the test.
func Reference(t testing.TB) string {
	t.Helper()
	p := os.Getenv("MTXUI_TEST_MEDIAMTX_REFERENCE")
	if p == "" {
		t.Skip("MTXUI_TEST_MEDIAMTX_REFERENCE is not set (./dev test sets it)")
	}
	return p
}

// FreePort returns a TCP port on 127.0.0.1 that was free a moment ago.
func FreePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// Instance is a running MediaMTX.
type Instance struct {
	Dir  string
	logs *syncBuffer
}

// Logs returns what MediaMTX has written so far.
func (i *Instance) Logs() string { return i.logs.String() }

// Start writes config to a temporary mediamtx.yml and runs MediaMTX until the test ends. It waits until readyPort
// on 127.0.0.1 accepts connections.
func Start(t *testing.T, config string, readyPort int) *Instance {
	t.Helper()
	bin := Bin(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mediamtx.yml")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inst := &Instance{Dir: dir, logs: &syncBuffer{}}
	cmd := exec.CommandContext(ctx, bin, cfg)
	cmd.Dir = dir
	cmd.Env = []string{} // MediaMTX reads MTX_* overrides from the environment
	cmd.Stdout, cmd.Stderr = inst.logs, inst.logs
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("MediaMTX output:\n%s", inst.Logs())
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", readyPort), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return inst
		}
		select {
		case <-done:
			t.Fatalf("MediaMTX exited during startup:\n%s", inst.Logs())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("MediaMTX not listening on %d after 10 s:\n%s", readyPort, inst.Logs())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
