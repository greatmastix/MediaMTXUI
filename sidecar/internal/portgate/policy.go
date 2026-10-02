package portgate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Policy is the operator's ceiling, root-owned in /etc/mtx-portgate/policy.json. The UI cannot exceed it.
type Policy struct {
	// Driver is "ufw", "dry-run" (rules kept in DryRunFile; for tests and CI) or "none".
	Driver     string `json:"driver"`
	DryRunFile string `json:"dryRunFile,omitempty"`
	// Desired is the sidecar's desired.json on the host; the helper opens it without following symlinks.
	Desired string `json:"desired"`
	// StateDir holds status.json (mounted read-only into the sidecar) and the helper's lock.
	StateDir string `json:"stateDir"`
	// Ports maps ids to what they open. Only these can ever be opened.
	Ports map[string]PortSpec `json:"ports"`
	Limits
	// Verify, when set, is how the helper confirms the firewall that matters has caught up before it reports a port
	// open (a cloud firewall sync tool's status, which compares the cloud firewall with ufw).
	Verify *Verify `json:"verify,omitempty"`
}

// PortSpec is what one port id opens.
type PortSpec struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
}

// Verify is a read-only check command and the text its output must contain once the firewall is in sync.
type Verify struct {
	Command []string `json:"command"`
	Expect  string   `json:"expect"`
	Timeout Duration `json:"timeout"`
	// Nudge, when set, is run once if the check has not passed after 8 s (or half of Timeout): a sync that missed a
	// change.
	Nudge []string `json:"nudge,omitempty"`
}

// LoadPolicy reads and checks a policy file.
func LoadPolicy(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	if err := p.check(); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	return &p, nil
}

func (p *Policy) check() error {
	var errs []error
	switch p.Driver {
	case "ufw", "none":
	case "dry-run":
		if p.DryRunFile == "" {
			errs = append(errs, errors.New("the dry-run driver needs dryRunFile"))
		}
	default:
		errs = append(errs, fmt.Errorf("driver must be ufw, dry-run or none, not %q", p.Driver))
	}
	if !strings.HasPrefix(p.Desired, "/") || !strings.HasPrefix(p.StateDir, "/") {
		errs = append(errs, errors.New("desired and stateDir must be absolute paths"))
	}
	if len(p.Ports) == 0 {
		errs = append(errs, errors.New("no ports"))
	}
	seen := map[PortSpec]string{}
	for id, s := range p.Ports {
		if !idPattern.MatchString(id) {
			errs = append(errs, fmt.Errorf("port id %q: lower-case letters, digits and dashes", id))
		}
		if s.Proto != "tcp" && s.Proto != "udp" {
			errs = append(errs, fmt.Errorf("port %s: proto must be tcp or udp", id))
		}
		if s.Port < 1 || s.Port > 65535 {
			errs = append(errs, fmt.Errorf("port %s: port out of range", id))
		}
		if other, dup := seen[s]; dup {
			errs = append(errs, fmt.Errorf("ports %s and %s open the same %d/%s", id, other, s.Port, s.Proto))
		}
		seen[s] = id
	}
	if p.MaxTTL <= 0 || p.MaxTTLAnySource <= 0 {
		errs = append(errs, errors.New("maxTTL and maxTTLAnySource must be positive"))
	}
	if p.MaxSources < 1 || p.MaxSources > 32 {
		errs = append(errs, errors.New("maxSources must be 1 to 32"))
	}
	if p.MinPrefixV4 < 1 || p.MinPrefixV4 > 32 || p.MinPrefixV6 < 1 || p.MinPrefixV6 > 128 {
		errs = append(errs, errors.New("minPrefixV4 must be 1 to 32 and minPrefixV6 1 to 128"))
	}
	if v := p.Verify; v != nil && (len(v.Command) == 0 || v.Expect == "" || v.Timeout <= 0) {
		errs = append(errs, errors.New("verify needs command, expect and a positive timeout"))
	}
	return errors.Join(errs...)
}

// Check validates a desired state against the policy and returns it normalized (sorted canonical sources). Every
// problem is reported, each naming its port.
func (p *Policy) Check(d Desired, now time.Time) (Desired, error) {
	var errs []error
	out := Desired{Rev: d.Rev, Want: map[string]Want{}}
	if d.Rev < 1 {
		errs = append(errs, errors.New("rev must be positive"))
	}
	for id, w := range d.Want {
		if _, ok := p.Ports[id]; !ok {
			errs = append(errs, fmt.Errorf("%s: unknown port id", id))
			continue
		}
		srcs, err := normalSources(w.Sources)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		if len(srcs) > p.MaxSources {
			errs = append(errs, fmt.Errorf("%s: %d sources, at most %d", id, len(srcs), p.MaxSources))
		}
		for _, s := range srcs {
			pf, _ := ParseSource(s)
			minBits := p.MinPrefixV4
			if pf.Addr().Is6() {
				minBits = p.MinPrefixV6
			}
			if pf.Bits() < minBits {
				errs = append(errs, fmt.Errorf("%s: %s is wider than /%d", id, s, minBits))
			}
		}
		anywhere := len(srcs) == 0
		if anywhere && !p.AllowAnySource {
			errs = append(errs, fmt.Errorf("%s: opening to anywhere is not allowed; name the sources", id))
		}
		ttl := time.Duration(p.MaxTTL)
		if anywhere {
			ttl = time.Duration(p.MaxTTLAnySource)
		}
		switch {
		case w.Until == nil && (!p.PermanentOK || anywhere):
			errs = append(errs, fmt.Errorf("%s: an expiry is required", id))
		case w.Until != nil && w.Until.Sub(now) > ttl:
			errs = append(errs, fmt.Errorf("%s: open for at most %s", id, ttl))
		}
		out.Want[id] = Want{Sources: srcs, Until: w.Until}
	}
	return out, errors.Join(errs...)
}
