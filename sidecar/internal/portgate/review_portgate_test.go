package portgate

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fake ufw replaces a rule that differs from a new one only in action or comment, as ufw does: portgate must never
// add a rule over one of the operator's, and must say so for that port instead.
func TestOperatorRulesAreLeftAlone(t *testing.T) {
	const ip = "203.0.113.7"
	cases := []struct {
		name     string
		operator string // the operator's own rule, in `ufw show added` form
		port     string
		want     Want
		blocked  bool // the operator's rule has the same port, protocol and source
	}{
		{"deny for the address", "ufw deny from 203.0.113.7 to any port 1935 proto tcp", "rtmp", Want{Sources: []string{ip}}, true},
		{"permanent allow for anyone", "ufw allow 1935/tcp", "rtmp", Want{}, true},
		{"rate limit", "ufw limit 8554/tcp", "rtsp", Want{}, true},
		{
			"logged, with a comment", "ufw allow log from 198.51.100.0/24 to any port 8890 proto udp comment 'office'", "srt",
			Want{Sources: []string{"198.51.100.0/24"}},
			true,
		},
		{"reject, IPv6", "ufw reject from 2001:db8::/48 to any port 8189 proto udp", "webrtc", Want{Sources: []string{"2001:db8::/48"}}, true},
		{"another source", "ufw deny from 203.0.113.8 to any port 1935 proto tcp", "rtmp", Want{Sources: []string{ip}}, false},
		{"any protocol", "ufw deny 1935", "rtmp", Want{}, false},
		{"every port", "ufw deny from 203.0.113.7", "rtmp", Want{Sources: []string{ip}}, false},
		{"routed", "ufw route allow proto tcp from 203.0.113.7 to any port 1935", "rtmp", Want{Sources: []string{ip}}, false},
		{"on an interface", "ufw allow in on eth0 to any port 1935 proto tcp", "rtmp", Want{}, false},
		{"outgoing", "ufw deny out 1935/tcp", "rtmp", Want{}, false},
		{"another address", "ufw allow from 203.0.113.7 to 192.0.2.1 port 1935 proto tcp", "rtmp", Want{Sources: []string{ip}}, false},
		{"from a source port", "ufw allow from 203.0.113.7 port 5000 to any port 1935 proto tcp", "rtmp", Want{Sources: []string{ip}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			before := foreign + c.operator + "\n"
			if err := os.WriteFile(f.rules, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			w := c.want
			w.Until = f.in(time.Hour)
			f.want(1, map[string]Want{c.port: w})
			for range 2 {
				st := f.apply()
				b, err := os.ReadFile(f.rules)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(string(b), before) {
					t.Fatalf("the operator's rule changed:\n%s", b)
				}
				ours := strings.TrimSpace(strings.TrimPrefix(string(b), before))
				ps := st.Ports[c.port]
				if c.blocked {
					if ours != "" || ps.State != StateError || !strings.Contains(ps.Error, c.operator) || !strings.Contains(st.Error, c.operator) {
						t.Fatalf("status %+v rules %q", st, ours)
					}
				} else if ours == "" || ps.State != StateOpen || st.Error != "" {
					t.Fatalf("status %+v rules %q", st, ours)
				}
			}
		})
	}
}

// IPv4-mapped IPv6 sources mean IPv4 addresses: the IPv4 limits apply, and the rule is written and read back as IPv4.
func TestMappedSourcesAreIPv4(t *testing.T) {
	for in, want := range map[string]string{
		"::ffff:203.0.113.7":      "203.0.113.7/32",
		"::ffff:203.0.113.7/128":  "203.0.113.7/32",
		"::ffff:198.51.100.0/120": "198.51.100.0/24",
		"::ffff:0.0.0.0/96":       "0.0.0.0/0",
		"2001:db8::/48":           "2001:db8::/48",
		"203.0.113.7":             "203.0.113.7/32",
	} {
		if p, err := ParseSource(in); err != nil || p.String() != want {
			t.Errorf("%s: %v %v, want %s", in, p, err, want)
		}
	}
	if _, err := ParseSource("::ffff:0:0/95"); err == nil {
		t.Error("a mapped prefix shorter than /96 was accepted")
	}

	t.Run("wider than the IPv4 limit", func(t *testing.T) {
		f := newFixture(t)
		f.want(1, map[string]Want{"srt": {Sources: []string{"::ffff:0.0.0.0/96"}, Until: f.in(time.Hour)}})
		if st := f.apply(); !strings.Contains(st.Error, "wider than /8") || f.ours() != "" {
			t.Fatalf("status %+v rules %s", st, f.ours())
		}
	})
	t.Run("opened and closed as IPv4", func(t *testing.T) {
		f := newFixture(t)
		f.want(1, map[string]Want{"rtsp": {Sources: []string{"::ffff:203.0.113.7/128"}, Until: f.in(10 * time.Minute)}})
		st := f.apply()
		if got := f.ours(); got != "ufw allow from 203.0.113.7 to any port 8554 proto tcp comment 'mtx-portgate:rtsp'" || st.Error != "" {
			t.Fatalf("status %+v rules %s", st, got)
		}
		f.now = f.now.Add(11 * time.Minute)
		if st := f.apply(); f.ours() != "" || st.Ports["rtsp"].State != StateClosed || st.Error != "" {
			t.Fatalf("after expiry: %+v rules %s", st, f.ours())
		}
	})
	t.Run("a mapped rule written before is removed", func(t *testing.T) {
		f := newFixture(t)
		old := "ufw allow from ::ffff:203.0.113.7 to any port 8554 proto tcp comment 'mtx-portgate:rtsp'\n"
		if err := os.WriteFile(f.rules, []byte(foreign+old), 0o600); err != nil {
			t.Fatal(err)
		}
		if st := f.apply(); f.ours() != "" || st.Error != "" {
			t.Fatalf("status %+v rules %s", st, f.ours())
		}
	})
}

// ufw answers a delete it cannot match without failing: the driver must not report that rule closed.
func TestDeleteThatFindsNothingFails(t *testing.T) {
	for out, fails := range map[string]bool{
		"Could not delete non-existent rule\n":                                          true,
		"Could not delete non-existent rule\nCould not delete non-existent rule (v6)\n": true,
		"Rule deleted\n":                                          false,
		"Rule deleted\nRule deleted (v6)\n":                       false,
		"Rule deleted\nCould not delete non-existent rule (v6)\n": false, // the IPv4 one was there
	} {
		u := &UFW{Run: func(context.Context, ...string) (string, error) { return out, nil }}
		err := u.Delete(context.Background(), Rule{ID: "rtsp", Proto: "tcp", Port: 8554})
		if (err != nil) != fails {
			t.Errorf("%q: %v", out, err)
		}
	}
}

// desired.json is rewritten only when its content changes, whatever time zone the expiries were made in.
func TestExpiriesCompareAsInstants(t *testing.T) {
	f := newFixture(t)
	zone := time.FixedZone("CEST", 2*60*60)
	f.now = f.now.In(zone)
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
	auto := []AutoOpening{
		{Port: "rtmp", Source: "198.51.100.4", Until: f.now.Add(30 * time.Minute), Reason: "setting up live/a"},
		{Port: "webrtc", Until: f.now.Add(30 * time.Minute), Reason: "viewers of live streams"},
	}
	if err := c.SetAuto(auto); err != nil {
		t.Fatal(err)
	}
	first, _ := c.Desired()
	for range 3 {
		if err := c.SetAuto(auto); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := c.Desired(); d.Rev != first.Rev {
		t.Errorf("rewritten without a change: rev %d -> %d", first.Rev, d.Rev)
	}
	checked, err := f.h.Policy.Check(Desired{Rev: 1, Want: map[string]Want{"rtsp": {Until: f.in(time.Hour)}}}, f.now)
	if err != nil || checked.Want["rtsp"].Until.Location() != time.UTC {
		t.Errorf("not normalized to UTC: %+v %v", checked, err)
	}
}

// The policy is the ceiling also for what it accepted before it was tightened: that closes, and the sidecar stops
// asking for it, so the rest of its state still applies.
func TestTightenedPolicyClosesWhatItNoLongerAllows(t *testing.T) {
	cases := []struct {
		name    string
		before  func(*Policy)
		tighten func(*Policy)
		want    func(*fixture) Want
		err     string
	}{
		{
			"permanent", func(p *Policy) { p.PermanentOK = true }, func(p *Policy) { p.PermanentOK = false },
			func(*fixture) Want { return Want{Sources: []string{"198.51.100.0/24"}} }, "an expiry is required",
		},
		{
			"to anyone", func(*Policy) {}, func(p *Policy) { p.AllowAnySource = false },
			func(f *fixture) Want { return Want{Until: f.in(time.Hour)} }, "not allowed",
		},
		{
			"too long", func(*Policy) {}, func(p *Policy) { p.MaxTTL = Duration(7 * 24 * time.Hour) },
			func(f *fixture) Want {
				return Want{Sources: []string{"198.51.100.0/24"}, Until: f.in(20 * 24 * time.Hour)}
			}, "at most 168h",
		},
		{
			"too wide", func(*Policy) {}, func(p *Policy) { p.MinPrefixV4 = 28 },
			func(f *fixture) Want { return Want{Sources: []string{"198.51.100.0/24"}, Until: f.in(time.Hour)} }, "wider than /28",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			cl := &Client{
				Dir:        filepath.Dir(f.h.Policy.Desired),
				StatusPath: filepath.Join(f.h.Policy.StateDir, StatusFile),
				ManualPath: filepath.Join(t.TempDir(), "manual.json"),
				Now:        func() time.Time { return f.now },
			}
			c.before(f.h.Policy)
			f.apply()
			w := c.want(f)
			if _, err := cl.Set("srt", &w); err != nil {
				t.Fatal(err)
			}
			if st := f.apply(); st.Ports["srt"].State != StateOpen {
				t.Fatalf("before: %+v", st)
			}

			// The operator edits the policy; the helper's next run (the timer) closes what it no longer allows.
			c.tighten(f.h.Policy)
			st := f.apply()
			if f.ours() != "" || st.Ports["srt"].State != StateClosed || !strings.Contains(st.Error, "no longer within the policy") ||
				!strings.Contains(st.Error, c.err) {
				t.Fatalf("after tightening: %+v rules %s", st, f.ours())
			}
			// The sidecar drops the manual opening the new limits refuse, and the helper takes the rest.
			if err := cl.SetAuto([]AutoOpening{{Port: "rtmp", Source: "203.0.113.7", Until: f.now.Add(5 * time.Minute), Reason: "setting up live/a"}}); err != nil {
				t.Fatal(err)
			}
			if d, _ := cl.Desired(); len(d.Want) != 1 || len(d.Want["rtmp"].Sources) != 1 {
				t.Fatalf("desired %+v", d.Want)
			}
			if v := cl.View(); v.Manual == nil || len(v.Manual.Want) != 0 {
				t.Errorf("the refused manual opening is still listed: %+v", v.Manual)
			}
			if st := f.apply(); st.Error != "" || st.Ports["rtmp"].State != StateOpen || st.Ports["srt"].State != StateClosed {
				t.Fatalf("the sidecar's next state: %+v", st)
			}
		})
	}
}

// A rev the sidecar's counting cannot have produced is refused, so none near the int64 maximum ever locks the helper;
// an older one is reported, not silently ignored; and the sidecar counts on from the helper's rev past a refused jump.
func TestRevisionJumpsAreRefused(t *testing.T) {
	f := newFixture(t)
	c := &Client{
		Dir:        filepath.Dir(f.h.Policy.Desired),
		StatusPath: filepath.Join(f.h.Policy.StateDir, StatusFile),
		ManualPath: filepath.Join(t.TempDir(), "manual.json"),
		Now:        func() time.Time { return f.now },
	}
	f.want(1, map[string]Want{"rtsp": {Until: f.in(time.Hour)}})
	f.apply()
	for _, rev := range []int64{2 + MaxRevStep, math.MaxInt64} {
		f.want(rev, map[string]Want{"srt": {Until: f.in(time.Hour)}})
		if st := f.apply(); st.Rev != 1 || !strings.Contains(st.Error, "jumps") || st.Ports["srt"].State != StateClosed {
			t.Fatalf("rev %d: %+v", rev, st)
		}
	}
	d, err := c.Set("srt", &Want{Until: f.in(time.Hour)})
	if err != nil || d.Rev != 2 {
		t.Fatalf("the sidecar's next write: %+v %v", d, err)
	}
	if st := f.apply(); st.Rev != 2 || st.Error != "" || st.Ports["srt"].State != StateOpen {
		t.Fatalf("status %+v", st)
	}
	f.want(1, map[string]Want{})
	if st := f.apply(); st.Rev != 2 || !strings.Contains(st.Error, "rev 1 ignored") || st.Ports["srt"].State != StateOpen {
		t.Fatalf("an older rev: %+v", st)
	}
}

// After close-all on the host, the running sidecar's renewals reopen nothing; once the sidecar has taken it in
// (CloseAll), what it asks for applies again.
func TestHostCloseAllHolds(t *testing.T) {
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
	lease := AutoOpening{Port: "rtmp", Source: "198.51.100.4", Until: f.now.Add(5 * time.Minute), Reason: "setting up live/a"}
	if err := c.SetAuto([]AutoOpening{lease}); err != nil {
		t.Fatal(err)
	}
	f.apply()
	if f.ours() == "" {
		t.Fatal("nothing open")
	}
	closed, err := f.h.CloseAll(context.Background())
	if err != nil || f.ours() != "" || closed.ClosedAll == nil {
		t.Fatalf("close-all: %+v %v", closed, err)
	}
	st, _ := c.Status()
	if !c.ClosedOnHost(st) {
		t.Fatal("the sidecar does not see the close-all")
	}

	// The sidecar's renewals change the merge, but nothing is written, and an older sidecar's write (no ClosedAll) is
	// refused.
	rev := closed.Rev
	lease.Until = lease.Until.Add(time.Minute)
	if err := c.SetAuto([]AutoOpening{lease}); err != nil {
		t.Fatal(err)
	}
	if d, _ := c.Desired(); d.Rev > rev {
		t.Fatalf("written before the close-all was taken in: %+v", d)
	}
	if _, err := c.Set("srt", &Want{Until: f.in(time.Hour)}); !errors.Is(err, ErrClosedOnHost) {
		t.Fatalf("set: %v", err)
	}
	f.want(rev+1, map[string]Want{"rtsp": {Until: f.in(time.Hour)}})
	if st := f.apply(); f.ours() != "" || st.ClosedAll == nil || !strings.Contains(st.Error, "closed on the host") {
		t.Fatalf("reopened: %+v %s", st, f.ours())
	}
	stale := Desired{Rev: rev + 2, Want: map[string]Want{"rtsp": {Until: f.in(time.Hour)}}, ClosedAll: f.in(-time.Hour)}
	if err := WriteFileAtomic(f.h.Policy.Desired, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := f.apply(); f.ours() != "" || st.ClosedAll == nil {
		t.Fatalf("an earlier close-all's acknowledgement counted: %+v %s", st, f.ours())
	}

	// Taking it in (what Close all on the Exposure page does, and what the sidecar does by itself) ...
	d, err := c.CloseAll()
	if err != nil || len(d.Want) != 0 || d.ClosedAll == nil || !d.ClosedAll.Equal(*closed.ClosedAll) {
		t.Fatalf("taking it in: %+v %v", d, err)
	}
	if c.ClosedOnHost(st) {
		t.Error("still not taken in")
	}
	// ... lets what is asked for afterwards apply, even before the helper has seen the empty state.
	if _, err := c.Set("srt", &Want{Sources: []string{"203.0.113.7"}, Until: f.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if st := f.apply(); st.ClosedAll != nil || st.Error != "" || st.Ports["srt"].State != StateOpen || st.Ports["rtsp"].State != StateClosed {
		t.Fatalf("after taking it in: %+v", st)
	}
	if d, _ := c.Desired(); d.ClosedAll == nil {
		t.Fatalf("desired %+v", d)
	}
	// The next write no longer carries it.
	if _, err := c.Set("rtsp", &Want{Sources: []string{"203.0.113.7"}, Until: f.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if d, _ := c.Desired(); d.ClosedAll != nil {
		t.Errorf("desired %+v", d)
	}
}

// Each automatic opening is fitted to the policy on its own: one it refuses costs no other opening on the port.
func TestAutomaticOpeningsFitThePolicyOneByOne(t *testing.T) {
	cases := []struct {
		name    string
		policy  func(*Policy)
		auto    func(*fixture) []AutoOpening
		sources string // rtmp's sources in desired.json
		within  time.Duration
	}{
		{
			"anyone not allowed", func(p *Policy) { p.AllowAnySource = false },
			func(f *fixture) []AutoOpening {
				return []AutoOpening{
					{Port: "rtmp", Until: f.now.Add(30 * time.Minute), Reason: "viewers of live streams"},
					{Port: "rtmp", Source: "198.51.100.4", Until: f.now.Add(5 * time.Minute), Reason: "setting up live/a"},
				}
			},
			"198.51.100.4/32", 5 * time.Minute,
		},
		{
			"remembered encoder beyond maxTTL", func(p *Policy) { p.MaxTTL = Duration(7 * 24 * time.Hour) },
			func(f *fixture) []AutoOpening {
				return []AutoOpening{
					{Port: "rtmp", Source: "198.51.100.4", Until: f.now.Add(5 * time.Minute), Reason: "setting up live/a"},
					{Port: "rtmp", Source: "198.51.100.9", Until: f.now.Add(30 * 24 * time.Hour), Reason: "known encoder of live/a"},
				}
			},
			"198.51.100.4/32,198.51.100.9/32", 7 * 24 * time.Hour,
		},
		{
			"one source too wide", func(*Policy) {},
			func(f *fixture) []AutoOpening {
				return []AutoOpening{
					{Port: "rtmp", Source: "10.0.0.0/7", Until: f.now.Add(5 * time.Minute)},
					{Port: "rtmp", Source: "198.51.100.4", Until: f.now.Add(5 * time.Minute), Reason: "setting up live/a"},
				}
			},
			"198.51.100.4/32", 5 * time.Minute,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			cl := &Client{
				Dir:        filepath.Dir(f.h.Policy.Desired),
				StatusPath: filepath.Join(f.h.Policy.StateDir, StatusFile),
				ManualPath: filepath.Join(t.TempDir(), "manual.json"),
				Now:        func() time.Time { return f.now },
			}
			c.policy(f.h.Policy)
			f.apply()
			if err := cl.SetAuto(c.auto(f)); err != nil {
				t.Fatal(err)
			}
			d, _ := cl.Desired()
			got := d.Want["rtmp"]
			if strings.Join(got.Sources, ",") != c.sources || got.Until == nil || got.Until.Sub(f.now) > c.within {
				t.Fatalf("rtmp %+v", got)
			}
			if st := f.apply(); st.Error != "" || st.Ports["rtmp"].State != StateOpen {
				t.Fatalf("the helper refused it: %+v", st)
			}
		})
	}
}
