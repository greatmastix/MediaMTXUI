package portgate

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Whatever the (less trusted) sidecar writes into desired.json, the root helper changes only rules of its own, for
// ports in its policy, from sources within the policy's limits, with nothing but plain addresses on ufw's command
// line, and leaves the operator's rules alone.
func FuzzApply(f *testing.F) {
	f.Add([]byte(`{"rev":1,"want":{"rtsp":{"sources":["192.0.2.0/24"],"until":"2026-09-29T13:00:00Z"}}}`))
	f.Add([]byte(`{"rev":2,"want":{"rtmp":{"sources":[],"until":"2026-09-29T12:30:00Z"},"srt":{"sources":["2001:db8::/48"],"until":"2026-09-30T12:00:00Z"}}}`))
	f.Add([]byte(`{"rev":3,"want":{"webrtc":{"sources":["198.51.100.7","198.51.100.7/32"],"until":"2026-09-29T12:01:00Z"}}}`))
	f.Add([]byte(`{"rev":9,"want":{"rtsp; rm -rf /":{"sources":["--dry-run"]}}}`))
	safeArg := regexp.MustCompile(`^[0-9a-f.:/]+$|^any$`)
	f.Fuzz(func(t *testing.T, data []byte) {
		fx := newFixture(t)
		if err := os.WriteFile(fx.h.Policy.Desired, data, 0o600); err != nil {
			t.Fatal(err)
		}
		st, _ := fx.h.Apply(context.Background()) // a refused request is fine; what matters is what was applied
		for _, line := range strings.Split(fx.ours(), "\n") {
			if line == "" {
				continue
			}
			r, err := parseUFWRule(line)
			if err != nil {
				t.Fatalf("a rule portgate cannot read back: %q: %v", line, err)
			}
			spec, ok := fx.h.Policy.Ports[r.ID]
			if !ok || spec.Port != r.Port || spec.Proto != r.Proto {
				t.Fatalf("a rule outside the policy: %+v", r)
			}
			src := r.Source
			if src == "" {
				src = "any"
			}
			if !safeArg.MatchString(src) {
				t.Fatalf("a source that is not a plain address on ufw's command line: %q", src)
			}
			if src == "any" && !fx.h.Policy.AllowAnySource {
				t.Fatalf("opened to anywhere against the policy: %+v", r)
			}
			if src != "any" {
				p, err := ParseSource(src)
				if err != nil {
					t.Fatalf("an unparsable source applied: %q", src)
				}
				minBits := fx.h.Policy.MinPrefixV4
				if p.Addr().Is6() {
					minBits = fx.h.Policy.MinPrefixV6
				}
				if p.Bits() < minBits {
					t.Fatalf("a source wider than the policy allows: %s", src)
				}
			}
		}
		for id, ps := range st.Ports {
			if _, ok := fx.h.Policy.Ports[id]; !ok {
				t.Fatalf("status for a port outside the policy: %s", id)
			}
			if ps.Until != nil && ps.Until.Sub(fx.now) > time.Duration(fx.h.Policy.MaxTTL) {
				t.Fatalf("%s open longer than the policy allows: until %s", id, ps.Until)
			}
		}
	})
}
