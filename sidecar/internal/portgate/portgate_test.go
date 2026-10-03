package portgate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The helper with the dry-run driver: a fake ufw whose rule list lives in a file that also holds the operator's own
// rules, so every test can check that portgate touched only its own.

const foreign = "ufw allow 22/tcp\nufw allow 443/tcp\nufw allow 3478/udp comment 'UniFi OS Server'\n"

type fixture struct {
	t      *testing.T
	h      *Helper
	rules  string // the fake ufw's file
	now    time.Time
	verify []string // outputs of the verify command, in order; the last repeats
	calls  []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, rules: filepath.Join(dir, "ufw.rules"), now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	if err := os.WriteFile(f.rules, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "portgate"), 0o700); err != nil {
		t.Fatal(err)
	}
	pol := &Policy{
		Driver: "dry-run", DryRunFile: f.rules,
		Desired:  filepath.Join(dir, "portgate", "desired.json"),
		StateDir: filepath.Join(dir, "state"),
		Ports: map[string]PortSpec{
			"rtsp": {Proto: "tcp", Port: 8554}, "rtmp": {Proto: "tcp", Port: 1935},
			"srt": {Proto: "udp", Port: 8890}, "webrtc": {Proto: "udp", Port: 8189},
		},
		Limits: Limits{
			AllowAnySource: true, MaxTTLAnySource: Duration(12 * time.Hour), MaxTTL: Duration(30 * 24 * time.Hour),
			MaxSources: 4, MinPrefixV4: 8, MinPrefixV6: 32,
		},
	}
	if err := pol.check(); err != nil {
		t.Fatal(err)
	}
	fake := &FakeUFW{File: f.rules}
	f.h = &Helper{
		Policy: pol,
		Driver: &UFW{Run: fake.Run},
		Now:    func() time.Time { return f.now },
		Sleep: func(context.Context, time.Duration) error {
			f.now = f.now.Add(2 * time.Second)
			return nil
		},
		Run: func(_ context.Context, argv ...string) (string, error) {
			f.calls = append(f.calls, strings.Join(argv, " "))
			if argv[0] == "nudge" || len(f.verify) == 0 {
				return "", nil
			}
			out := f.verify[0]
			if len(f.verify) > 1 {
				f.verify = f.verify[1:]
			}
			return out, nil
		},
	}
	return f
}

func (f *fixture) want(rev int64, w map[string]Want) {
	f.t.Helper()
	if err := WriteFileAtomic(f.h.Policy.Desired, Desired{Rev: rev, Want: w}, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) apply() Status {
	f.t.Helper()
	st, err := f.h.Apply(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// ours is the fake ufw's rule list without the operator's rules, which must be exactly as before.
func (f *fixture) ours() string {
	f.t.Helper()
	b, err := os.ReadFile(f.rules)
	if err != nil {
		f.t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, foreign) {
		f.t.Fatalf("the operator's rules changed:\n%s", s)
	}
	return strings.TrimSpace(strings.TrimPrefix(s, foreign))
}

func (f *fixture) in(d time.Duration) *time.Time {
	t := f.now.Add(d)
	return &t
}

func TestDesiredStateConvergesAndExpires(t *testing.T) {
	f := newFixture(t)
	f.want(1, map[string]Want{
		"rtsp":   {Sources: []string{"203.0.113.7", "198.51.100.0/24"}, Until: f.in(10 * time.Minute)},
		"webrtc": {Until: f.in(time.Hour)},
	})
	st := f.apply()
	want := "ufw allow from 198.51.100.0/24 to any port 8554 proto tcp comment 'mtx-portgate:rtsp'\n" +
		"ufw allow from 203.0.113.7 to any port 8554 proto tcp comment 'mtx-portgate:rtsp'\n" +
		"ufw allow 8189/udp comment 'mtx-portgate:webrtc'"
	if got := f.ours(); got != want {
		t.Fatalf("rules:\n%s\nwant:\n%s", got, want)
	}
	if st.Rev != 1 || st.Error != "" || st.Ports["rtsp"].State != StateOpen || st.Ports["webrtc"].State != StateOpen ||
		st.Ports["srt"].State != StateClosed || len(st.Ports) != 4 {
		t.Fatalf("status %+v", st)
	}
	if got := st.Ports["rtsp"].Sources; strings.Join(got, ",") != "198.51.100.0/24,203.0.113.7/32" {
		t.Errorf("sources %v", got)
	}

	// Applying again changes nothing.
	f.apply()
	if got := f.ours(); got != want {
		t.Fatalf("second apply changed the rules:\n%s", got)
	}

	// Expiry needs nothing from the sidecar: the timer's run closes what is due.
	if err := os.Remove(f.h.Policy.Desired); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(11 * time.Minute)
	st = f.apply()
	if got := f.ours(); got != "ufw allow 8189/udp comment 'mtx-portgate:webrtc'" {
		t.Fatalf("after rtsp's expiry:\n%s", got)
	}
	if st.Ports["rtsp"].State != StateClosed {
		t.Errorf("rtsp %+v", st.Ports["rtsp"])
	}

	// Narrowing a port replaces its rules; dropping it closes it.
	f.want(2, map[string]Want{"webrtc": {Sources: []string{"2001:db8::/48"}, Until: f.in(time.Hour)}})
	f.apply()
	if got := f.ours(); got != "ufw allow from 2001:db8::/48 to any port 8189 proto udp comment 'mtx-portgate:webrtc'" {
		t.Fatalf("narrowed:\n%s", got)
	}
	f.want(3, map[string]Want{})
	f.apply()
	if got := f.ours(); got != "" {
		t.Fatalf("closed:\n%s", got)
	}
}

func TestPolicyViolationsAreRefused(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		sources []string
		ttl     time.Duration // 0: no expiry
		err     string
	}{
		{"unknown id", "ssh", nil, time.Hour, "ssh: unknown port id"},
		{"ttl too long", "rtsp", []string{"203.0.113.7"}, 31 * 24 * time.Hour, "at most 720h"},
		{"anywhere too long", "rtsp", nil, 13 * time.Hour, "at most 12h"},
		{"slash zero", "rtsp", []string{"0.0.0.0/0"}, time.Hour, "wider than /8"},
		{"v6 too wide", "rtsp", []string{"2001::/16"}, time.Hour, "wider than /32"},
		{"no expiry", "rtsp", []string{"203.0.113.7"}, 0, "an expiry is required"},
		{"host bits", "rtsp", []string{"203.0.113.7/24"}, time.Hour, "host bits"},
		{"garbage", "rtsp", []string{"; rm -rf /"}, time.Hour, "not an address"},
		{"too many", "rtsp", []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"}, time.Hour, "at most 4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			w := Want{Sources: c.sources}
			if c.ttl > 0 {
				w.Until = f.in(c.ttl)
			}
			f.want(1, map[string]Want{c.id: w})
			st := f.apply()
			if !strings.Contains(st.Error, c.err) || st.Rev != 0 {
				t.Fatalf("status %+v, want error %q", st, c.err)
			}
			if got := f.ours(); got != "" {
				t.Fatalf("opened anyway:\n%s", got)
			}
		})
	}

	t.Run("anywhere disallowed", func(t *testing.T) {
		f := newFixture(t)
		f.h.Policy.AllowAnySource = false
		f.want(1, map[string]Want{"srt": {Until: f.in(time.Hour)}})
		if st := f.apply(); !strings.Contains(st.Error, "not allowed") {
			t.Fatalf("status %+v", st)
		}
	})
	t.Run("permanent when allowed", func(t *testing.T) {
		f := newFixture(t)
		f.h.Policy.PermanentOK = true
		f.want(1, map[string]Want{"srt": {Sources: []string{"203.0.113.0/24"}}})
		if st := f.apply(); st.Error != "" || st.Ports["srt"].State != StateOpen {
			t.Fatalf("status %+v", st)
		}
	})
}

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }

func TestRequestFileIsReadSafely(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		f := newFixture(t)
		big := `{"rev": 1, "want": {}}` + strings.Repeat(" ", MaxRequest)
		if err := os.WriteFile(f.h.Policy.Desired, []byte(big), 0o600); err != nil {
			t.Fatal(err)
		}
		if st := f.apply(); !strings.Contains(st.Error, "at most 8192") {
			t.Fatalf("status %+v", st)
		}
	})
	t.Run("symlinked file", func(t *testing.T) {
		f := newFixture(t)
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		if err := os.WriteFile(target, []byte(`{"rev": 1, "want": {}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, f.h.Policy.Desired); err != nil {
			t.Fatal(err)
		}
		if st := f.apply(); !strings.Contains(st.Error, "symlink is refused") || st.Rev != 0 {
			t.Fatalf("status %+v", st)
		}
	})
	t.Run("symlinked directory", func(t *testing.T) {
		f := newFixture(t)
		target := filepath.Join(t.TempDir(), "real")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(f.h.Policy.Desired)
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		f.want(1, map[string]Want{})
		if st := f.apply(); !strings.Contains(st.Error, "request directory") {
			t.Fatalf("status %+v", st)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		f := newFixture(t)
		if err := mkfifo(f.h.Policy.Desired); err != nil {
			t.Skip(err)
		}
		if st := f.apply(); !strings.Contains(st.Error, "not a regular file") {
			t.Fatalf("status %+v", st)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		f := newFixture(t)
		if err := os.WriteFile(f.h.Policy.Desired, []byte(`{"rev": 1, "want": {}, "driver": "none"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if st := f.apply(); !strings.Contains(st.Error, "unknown field") {
			t.Fatalf("status %+v", st)
		}
	})
}

func TestOldRevisionsAreNotReplayed(t *testing.T) {
	f := newFixture(t)
	f.want(5, map[string]Want{"rtsp": {Sources: []string{"203.0.113.7"}, Until: f.in(time.Hour)}})
	f.apply()
	f.want(4, map[string]Want{"rtsp": {Until: f.in(time.Hour)}}) // an older file put back
	st := f.apply()
	if st.Rev != 5 || f.ours() != "ufw allow from 203.0.113.7 to any port 8554 proto tcp comment 'mtx-portgate:rtsp'" {
		t.Fatalf("status %+v rules %s", st, f.ours())
	}
}

func TestCloseAllWorksWithoutTheSidecar(t *testing.T) {
	f := newFixture(t)
	f.want(1, map[string]Want{"rtsp": {Until: f.in(time.Hour)}, "srt": {Until: f.in(time.Hour)}})
	f.apply()
	// A rule portgate once wrote for a port the policy no longer has is its own too, and goes.
	if _, err := (&FakeUFW{File: f.rules}).Run(context.Background(), "ufw", "allow", "proto", "tcp", "from", "any", "to", "any",
		"port", "9000", "comment", CommentPrefix+"old"); err != nil {
		t.Fatal(err)
	}
	st, err := f.h.CloseAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.ours() != "" || st.ClosedAll == nil || st.Ports["rtsp"].State != StateClosed {
		t.Fatalf("status %+v rules %s", st, f.ours())
	}
	// The desired state that was open stays closed on the next runs, and so does a newer one written without taking
	// the close-all in (the running sidecar's routine renewals)...
	if st := f.apply(); f.ours() != "" || st.ClosedAll == nil {
		t.Fatalf("reopened: %s", f.ours())
	}
	f.want(2, map[string]Want{"rtsp": {Until: f.in(time.Hour)}})
	if st := f.apply(); f.ours() != "" || st.ClosedAll == nil || !strings.Contains(st.Error, "closed on the host") {
		t.Fatalf("reopened by a renewal: %+v %s", st, f.ours())
	}
	// ...until the sidecar has taken it in.
	if err := WriteFileAtomic(f.h.Policy.Desired, Desired{Rev: 3, Want: map[string]Want{"rtsp": {Until: f.in(time.Hour)}}, ClosedAll: st.ClosedAll}, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := f.apply(); f.ours() == "" || st.ClosedAll != nil || st.Error != "" {
		t.Fatalf("status %+v", st)
	}
}

func TestVerifyWaitsForTheFirewallAndNudgesOnce(t *testing.T) {
	f := newFixture(t)
	f.h.Policy.Verify = &Verify{
		Command: []string{"check"}, Expect: "in sync: yes", Timeout: Duration(20 * time.Second), Nudge: []string{"nudge"},
	}
	f.verify = []string{"in sync: no", "in sync: no", "in sync: no", "in sync: no", "in sync: no", "in sync: no", "in sync: yes"}
	f.want(1, map[string]Want{"rtsp": {Until: f.in(time.Hour)}})
	st := f.apply()
	if st.Ports["rtsp"].State != StateOpen || st.Error != "" {
		t.Fatalf("status %+v", st)
	}
	if n := strings.Count(strings.Join(f.calls, "|"), "nudge"); n != 1 {
		t.Errorf("nudged %d times: %v", n, f.calls)
	}

	// A firewall that never catches up is an error on the port, retried on the next run.
	f.calls, f.verify = nil, []string{"in sync: no"}
	f.want(2, map[string]Want{"rtsp": {Until: f.in(time.Hour)}, "rtmp": {Until: f.in(time.Hour)}})
	st = f.apply()
	if st.Ports["rtmp"].State != StateError || !strings.Contains(st.Error, "not confirmed in sync") {
		t.Fatalf("status %+v", st)
	}
	f.verify = []string{"in sync: yes"}
	if st = f.apply(); st.Ports["rtmp"].State != StateOpen || st.Error != "" {
		t.Fatalf("retry: %+v", st)
	}
	// With nothing changed and nothing in error, there is nothing to verify.
	f.calls = nil
	f.apply()
	if len(f.calls) != 0 {
		t.Errorf("verified with nothing changed: %v", f.calls)
	}
}

func TestParsesWhatUFWPrints(t *testing.T) {
	// Lines as ufw 0.36 printed them (Ubuntu, 2026-09-29).
	for line, want := range map[string]Rule{
		"ufw allow from 192.0.2.1 to any port 65000 proto udp comment 'mtx-portgate:probe'": {ID: "probe", Proto: "udp", Port: 65000, Source: "192.0.2.1/32"},
		"ufw allow 65001/udp comment 'mtx-portgate:probe'":                                  {ID: "probe", Proto: "udp", Port: 65001},
		"ufw allow from 2001:db8::/48 to any port 8189 proto udp comment 'mtx-portgate:w'":  {ID: "w", Proto: "udp", Port: 8189, Source: "2001:db8::/48"},
	} {
		got, err := parseUFWRule(line)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", line, got, err)
		}
	}
	for _, bad := range []string{
		"ufw allow from 192.0.2.1 to 10.0.0.1 port 1 proto tcp comment 'mtx-portgate:x'",
		"ufw deny 22/tcp comment 'mtx-portgate:x'",
		"ufw allow 22 comment 'mtx-portgate:x'",
		"ufw allow 22/tcp comment 'mtx-portgate:X Y'",
	} {
		if _, err := parseUFWRule(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// A marked rule portgate cannot read stops the run instead of being ignored.
	f := newFixture(t)
	if err := os.WriteFile(f.rules, []byte(foreign+"ufw deny 22/tcp comment 'mtx-portgate:x'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.want(1, map[string]Want{"rtsp": {Until: f.in(time.Hour)}})
	if st := f.apply(); st.Ports["rtsp"].State != StateError || !strings.Contains(st.Error, "cannot read") {
		t.Fatalf("status %+v", st)
	}
}

func TestStatusJSON(t *testing.T) {
	f := newFixture(t)
	f.want(1, map[string]Want{"rtsp": {Until: f.in(time.Hour)}})
	f.apply()
	b, err := os.ReadFile(filepath.Join(f.h.Policy.StateDir, StatusFile))
	if err != nil {
		t.Fatal(err)
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil || st.Limits.MaxTTLAnySource != Duration(12*time.Hour) {
		t.Fatalf("%s %v", b, err)
	}
	if !strings.Contains(string(b), `"maxTTLAnySource": "12h0m0s"`) {
		t.Errorf("durations are not strings: %s", b)
	}
	info, err := os.Stat(filepath.Join(f.h.Policy.StateDir, StatusFile))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("status mode %v %v", info.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(f.h.Policy.StateDir, acceptedFile)); err != nil {
		t.Error(err)
	}
}

func TestClientAndHelperTogether(t *testing.T) {
	f := newFixture(t)
	c := &Client{
		Dir:        filepath.Dir(f.h.Policy.Desired),
		StatusPath: filepath.Join(f.h.Policy.StateDir, StatusFile),
		Now:        func() time.Time { return f.now },
	}
	if _, err := c.Set("rtsp", &Want{Until: f.in(time.Hour)}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("before the helper ran: %v", err)
	}
	if v := c.View(); v.Installed {
		t.Fatalf("view %+v", v)
	}
	f.apply() // the helper's first run (the timer) writes a status with the ports and limits

	if _, err := c.Set("ssh", &Want{Until: f.in(time.Hour)}); err == nil {
		t.Error("an unknown port was accepted")
	}
	if _, err := c.Set("rtsp", &Want{Until: f.in(13 * time.Hour)}); err == nil || !strings.Contains(err.Error(), "at most 12h") {
		t.Errorf("the limit was not checked: %v", err)
	}
	if _, err := c.Set("rtsp", &Want{Sources: []string{"10.0.0.0/7"}, Until: f.in(time.Hour)}); err == nil {
		t.Error("a range wider than the policy allows was accepted")
	}
	d, err := c.Set("rtsp", &Want{Sources: []string{"203.0.113.7"}, Until: f.in(time.Hour)})
	if err != nil || d.Rev != 1 {
		t.Fatalf("set: %+v %v", d, err)
	}
	if v := c.View(); !v.Pending {
		t.Fatalf("not pending before the helper ran: %+v", v)
	}
	f.apply()
	v := c.View()
	if v.Pending || v.Status.Ports["rtsp"].State != StateOpen || v.Status.Ports["rtsp"].Sources[0] != "203.0.113.7/32" {
		t.Fatalf("view %+v", v)
	}

	// The host's close-all wins over the sidecar's file: nothing opens until the sidecar has taken it in (CloseAll),
	// and then its changes apply again.
	if _, err := f.h.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Set("srt", &Want{Sources: []string{"203.0.113.7"}, Until: f.in(time.Hour)}); !errors.Is(err, ErrClosedOnHost) {
		t.Fatalf("opened before the close-all was taken in: %v", err)
	}
	if d, err := c.CloseAll(); err != nil || d.Rev != 2 || len(d.Want) != 0 || d.ClosedAll == nil {
		t.Fatalf("taking it in: %+v %v", d, err)
	}
	d, err = c.Set("srt", &Want{Sources: []string{"203.0.113.7"}, Until: f.in(time.Hour)})
	if err != nil || d.Rev != 3 || len(d.Want) != 1 {
		t.Fatalf("after close-all: %+v %v", d, err)
	}
	if st := f.apply(); f.ours() == "" || st.ClosedAll != nil {
		t.Fatalf("the sidecar could not reopen after taking close-all in: %+v", st)
	}
	if _, err := c.CloseAll(); err != nil {
		t.Fatal(err)
	}
	f.apply()
	if f.ours() != "" || c.View().Pending {
		t.Fatalf("close all from the sidecar: %s", f.ours())
	}
}

func TestARequestWrittenDuringARunIsApplied(t *testing.T) {
	f := newFixture(t)
	f.h.Policy.Verify = &Verify{Command: []string{"check"}, Expect: "in sync: yes", Timeout: Duration(time.Minute)}
	f.want(1, map[string]Want{"rtsp": {Until: f.in(time.Hour)}})
	written := false
	run := f.h.Run
	f.h.Run = func(ctx context.Context, argv ...string) (string, error) {
		if !written { // the sidecar writes again while the first run waits for the firewall
			written = true
			f.want(2, map[string]Want{"rtsp": {Until: f.in(time.Hour)}, "srt": {Until: f.in(time.Hour)}})
		}
		return run(ctx, argv...)
	}
	f.verify = []string{"in sync: yes"}
	st := f.apply()
	if st.Rev != 2 || st.Ports["srt"].State != StateOpen {
		t.Fatalf("status %+v", st)
	}
	// A refused request does not make it loop.
	f.want(3, map[string]Want{"ssh": {Until: f.in(time.Hour)}})
	if st := f.apply(); st.Rev != 2 || !strings.Contains(st.Error, "unknown port id") {
		t.Fatalf("status %+v", st)
	}
}

func TestAutomaticOpeningsMerge(t *testing.T) {
	f := newFixture(t)
	c := &Client{
		Dir:        filepath.Dir(f.h.Policy.Desired),
		StatusPath: filepath.Join(f.h.Policy.StateDir, StatusFile),
		ManualPath: filepath.Join(t.TempDir(), "manual.json"),
		Now:        func() time.Time { return f.now },
	}
	f.apply()
	if _, err := c.Set("rtsp", &Want{Sources: []string{"203.0.113.7"}, Until: f.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	month := f.now.Add(30 * 24 * time.Hour)
	soon := f.now.Add(30 * time.Minute)
	auto := []AutoOpening{
		{Port: "rtmp", Source: "198.51.100.4", Until: soon, Reason: "lease"},
		{Port: "rtmp", Source: "198.51.100.9", Until: month, Reason: "known encoder"},
		{Port: "rtsp", Source: "198.51.100.4", Until: soon, Reason: "lease"},
		{Port: "webrtc", Source: "198.51.100.9", Until: month, Reason: "known encoder"},
		{Port: "webrtc", Until: soon, Reason: "viewers"},                      // anyone: subsumes the named source, keeps its own expiry
		{Port: "ssh", Until: soon, Reason: "nonsense"},                        // not in the policy: ignored
		{Port: "srt", Source: "10.0.0.0/7", Until: soon},                      // wider than the policy allows: ignored
		{Port: "srt", Source: "198.51.100.1", Until: f.now.Add(-time.Minute)}, // expired
	}
	if err := c.SetAuto(auto); err != nil {
		t.Fatal(err)
	}
	d, _ := c.Desired()
	if got := d.Want["rtmp"]; strings.Join(got.Sources, ",") != "198.51.100.4/32,198.51.100.9/32" || !got.Until.Equal(month) {
		t.Errorf("rtmp %+v", got)
	}
	if got := d.Want["rtsp"]; strings.Join(got.Sources, ",") != "198.51.100.4/32,203.0.113.7/32" || !got.Until.Equal(f.now.Add(time.Hour)) {
		t.Errorf("rtsp (manual plus lease) %+v", got)
	}
	if got := d.Want["webrtc"]; len(got.Sources) != 0 || !got.Until.Equal(soon) {
		t.Errorf("webrtc (anyone, within the 12 h limit) %+v", got)
	}
	if _, ok := d.Want["srt"]; ok || len(d.Want) != 3 {
		t.Errorf("wants %+v", d.Want)
	}
	f.apply()
	if st, _ := c.Status(); st.Error != "" || st.Ports["webrtc"].State != StateOpen {
		t.Fatalf("the helper refused the merge: %+v", st)
	}

	// The same openings again: nothing is rewritten, so the helper is not woken for nothing.
	rev := d.Rev
	if err := c.SetAuto(auto); err != nil {
		t.Fatal(err)
	}
	if d, _ := c.Desired(); d.Rev != rev {
		t.Errorf("rewritten without a change: rev %d -> %d", rev, d.Rev)
	}
	// Automatic openings going away leave the manual one.
	if err := c.SetAuto(nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := c.Desired(); len(d.Want) != 1 || strings.Join(d.Want["rtsp"].Sources, ",") != "203.0.113.7/32" {
		t.Errorf("after the automatic openings ended: %+v", d.Want)
	}
	if v := c.View(); v.Manual == nil || len(v.Manual.Want) != 1 || len(v.Auto) != 0 {
		t.Errorf("view %+v", v)
	}
	// Close all ends both kinds.
	_ = c.SetAuto(auto)
	if _, err := c.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if d, _ := c.Desired(); len(d.Want) != 0 {
		t.Errorf("after close all: %+v", d.Want)
	}
}
