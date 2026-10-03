// Package portgate is exposure control: which stream ports the internet may reach, from where, and until
// when. It has two halves that share the file formats below and nothing else:
//
//   - the sidecar's side (Client), which writes the complete desired state into its own data directory and reads the
//     status the helper reports back;
//   - the host helper (Helper, cmd/mtx-portgate), which runs as root, checks the desired state against the operator's
//     root-owned policy and converges the firewall rules it owns through a Driver.
//
// The sidecar never touches the firewall and the helper never trusts the sidecar: every request is re-validated, ports
// are named by id (the policy maps ids to protocol and port), and the helper adds and deletes only rules it created.
package portgate

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"time"
)

// Port states in Status.
const (
	StateOpen   = "open"
	StateClosed = "closed"
	StateError  = "error"
)

// MaxRequest caps the desired-state file the helper reads.
const MaxRequest = 8 << 10

// Desired is the sidecar's complete wish, written to desired.json. Rev grows by one with every write; the helper
// applies a file only when its Rev is higher than the last one it accepted (by at most MaxRevStep), so an old file
// cannot be replayed.
type Desired struct {
	Rev  int64           `json:"rev"`
	Want map[string]Want `json:"want"`
	// ClosedAll is the host's close-all (Status.ClosedAll) this state has taken in: the sidecar sets it once it has
	// closed its manual openings and switched its automatic rules off after it. Until a state carries it, the helper
	// opens nothing.
	ClosedAll *time.Time `json:"closedAll,omitempty"`
}

// Want opens one port. No sources means anywhere; no Until means no expiry (only where the policy allows either).
type Want struct {
	Sources []string   `json:"sources"`
	Until   *time.Time `json:"until"`
}

// Status is what the helper reports, in status.json.
type Status struct {
	Rev     int64                 `json:"rev"`     // the last accepted desired rev
	Updated time.Time             `json:"updated"` // when the helper last ran
	Driver  string                `json:"driver"`
	Ports   map[string]PortStatus `json:"ports"`
	Limits  Limits                `json:"limits"`
	// Error is why the latest desired state was refused, or why the helper could not converge; empty when all is well.
	Error string `json:"error,omitempty"`
	// ClosedAll is set by close-all on the host: every port stays closed until the sidecar has taken it in (a newer
	// desired state carrying the same time).
	ClosedAll *time.Time `json:"closedAll,omitempty"`
}

// PortStatus is one port's applied state.
type PortStatus struct {
	Proto   string     `json:"proto"`
	Port    int        `json:"port"`
	State   string     `json:"state"`
	Sources []string   `json:"sources,omitempty"`
	Until   *time.Time `json:"until,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// Limits is the part of the policy the UI needs, copied into every status so the sidecar need not read /etc.
type Limits struct {
	AllowAnySource  bool     `json:"allowAnySource"`
	MaxTTLAnySource Duration `json:"maxTTLAnySource"`
	MaxTTL          Duration `json:"maxTTL"`
	PermanentOK     bool     `json:"permanentOK"`
	MaxSources      int      `json:"maxSources"`
	// MinPrefixV4 and MinPrefixV6 are the widest source ranges allowed (8 means /8 or narrower). Anywhere is written
	// as no sources, never as 0.0.0.0/0, and needs AllowAnySource.
	MinPrefixV4 int `json:"minPrefixV4"`
	MinPrefixV6 int `json:"minPrefixV6"`
}

// Duration is a time.Duration written as "90m" in JSON.
type Duration time.Duration

// MarshalJSON writes the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// UnmarshalJSON reads a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration such as \"12h\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

// ParseSource checks one source and returns it as a canonical prefix: a bare address becomes /32 or /128, and an
// IPv4-mapped IPv6 one (::ffff:192.0.2.0/120) the IPv4 prefix it means (192.0.2.0/24), so the policy's IPv4 limits
// apply to it and ufw keeps the rule in the form portgate reads back.
func ParseSource(s string) (netip.Prefix, error) {
	p, err := parsePrefix(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if a := p.Addr(); a.Is4In6() {
		p = netip.PrefixFrom(a.Unmap(), p.Bits()-96) // at least 96: a shorter one has host bits in the ::ffff
	}
	return p, nil
}

// parsePrefix checks one source as written, without making it canonical: a bare address becomes /32 or /128.
func parsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		if p.Masked() != p {
			return netip.Prefix{}, fmt.Errorf("%s has host bits set (did you mean %s?)", s, p.Masked())
		}
		return p, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("%q is not an address or CIDR", s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// normalSources returns sources as sorted canonical prefixes, so two wants compare equal when they mean the same.
func normalSources(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		p, err := ParseSource(s)
		if err != nil {
			return nil, err
		}
		out = append(out, p.String())
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
