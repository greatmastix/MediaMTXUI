// Package mtxtest runs the pinned MediaMTX binary for integration tests. Tests skip unless MTXUI_TEST_MEDIAMTX_BIN
// points at it; `./dev test` extracts the binary from the pinned image and sets it.
package mtxtest

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
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

// Ports for FreePort come from below Linux's ephemeral range (32768 and up). A port the kernel picks there (":0") can
// be taken again by an outgoing connection of a parallel test before MediaMTX listens on it, and MediaMTX then exits
// with "address already in use". Each test process starts at a random place, so packages rarely meet.
const portLow, portHigh = 20000, 32000

var (
	portMu   sync.Mutex
	portNext = portLow + rand.IntN(portHigh-portLow)
)

// FreePort returns a TCP port on 127.0.0.1 that was free a moment ago, and that this process has not handed out yet.
func FreePort(t testing.TB) int {
	t.Helper()
	portMu.Lock()
	defer portMu.Unlock()
	for range portHigh - portLow {
		p := portNext
		if portNext++; portNext >= portHigh {
			portNext = portLow
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return p
	}
	t.Fatal("no free port between 20000 and 32000")
	return 0
}

// Instance is a running MediaMTX.
type Instance struct {
	Dir  string
	logs *syncBuffer
}

// Logs returns what MediaMTX has written so far.
func (i *Instance) Logs() string { return i.logs.String() }

// Start writes config to a temporary mediamtx.yml and runs MediaMTX until the test ends. It waits until readyPort
// on 127.0.0.1 accepts connections. A MediaMTX that dies without a word during startup (killed from outside, as on a
// CI runner short of memory) gets one more try; one that says why it stopped (a config error) fails the test at once.
func Start(t *testing.T, config string, readyPort int) *Instance {
	t.Helper()
	inst, silent := start(t, config, readyPort, false)
	if inst == nil && silent {
		t.Log("MediaMTX stopped during startup without any output; trying once more")
		inst, _ = start(t, config, readyPort, true)
	}
	if inst == nil {
		t.FailNow()
	}
	return inst
}

// start runs MediaMTX once. It returns nil when MediaMTX does not come up, and whether it exited without any output;
// that case fails the test only on the last try (final), and is logged otherwise.
func start(t *testing.T, config string, readyPort int, final bool) (*Instance, bool) {
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
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
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
			return inst, false
		}
		select {
		case <-done:
			silent := inst.Logs() == ""
			if silent && !final {
				t.Logf("MediaMTX exited during startup (%v), without any output", waitErr)
			} else {
				t.Errorf("MediaMTX exited during startup (%v):\n%s", waitErr, inst.Logs())
			}
			return nil, silent
		default:
		}
		if time.Now().After(deadline) {
			t.Errorf("MediaMTX not listening on %d after 10 s:\n%s", readyPort, inst.Logs())
			return nil, false
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
