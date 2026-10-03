package portgate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"time"
)

// ErrNotInstalled means the host helper has never written a status: exposure control is not set up on this host.
var ErrNotInstalled = errors.New("exposure control is not installed on this host (no status from mtx-portgate)")

// ErrClosedOnHost means everything was closed on the host (mtx-portgate close-all) and the sidecar has not taken that
// in yet (CloseAll): the helper opens nothing until it has.
var ErrClosedOnHost = errors.New("everything was closed on the host (mtx-portgate close-all), and nothing opens until " +
	"the sidecar has taken that in, which it does by itself within seconds: try again then")

// Client is the sidecar's side: it writes desired.json into its own portgate directory and reads the status file the
// helper writes (mounted read-only). It knows nothing the helper does not tell it: port ids and limits come from the
// status.
//
// Two kinds of opening feed the desired state: manual ones (the Exposure page, kept in ManualPath so they survive
// restarts) and automatic ones (SetAuto: streams being set up, live publishers, known encoders, viewers). desired.json
// is their merge, rewritten only when it changes.
type Client struct {
	Dir        string // the sidecar's portgate directory (desired.json, read by the helper)
	StatusPath string // the helper's status.json
	ManualPath string // the manual openings (sidecar state; the helper never reads it)
	Now        func() time.Time

	mu   sync.Mutex
	auto []AutoOpening
}

// AutoOpening is one automatic reason to open a port: to Source (an address or CIDR; empty for anyone) until Until.
type AutoOpening struct {
	Port   string    `json:"port"`
	Source string    `json:"source"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"` // shown on the Exposure page
}

// View is what the UI shows: the helper's applied status, the sidecar's desired state (pending while the helper has
// not caught up with its rev), and where it comes from.
type View struct {
	Installed bool          `json:"installed"`
	Status    *Status       `json:"status,omitempty"`
	Desired   *Desired      `json:"desired,omitempty"`
	Pending   bool          `json:"pending"`
	Manual    *Desired      `json:"manual,omitempty"`
	Auto      []AutoOpening `json:"auto"`
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) desiredPath() string { return filepath.Join(c.Dir, "desired.json") }

func (c *Client) manualPath() string {
	if c.ManualPath != "" {
		return c.ManualPath
	}
	return filepath.Join(c.Dir, "manual.json")
}

// Status reads the helper's status, or ErrNotInstalled.
func (c *Client) Status() (*Status, error) {
	b, err := os.ReadFile(c.StatusPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	if err != nil {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("status from mtx-portgate: %w", err)
	}
	return &st, nil
}

func readDesired(path string) (Desired, bool, error) {
	d := Desired{Want: map[string]Want{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, false, err
	}
	if d.Want == nil {
		d.Want = map[string]Want{}
	}
	return d, true, nil
}

// Desired reads the desired state last written, or an empty one.
func (c *Client) Desired() (Desired, error) {
	d, _, err := readDesired(c.desiredPath())
	return d, err
}

// Manual reads the manual openings.
func (c *Client) Manual() (Desired, error) {
	d, _, err := c.manual()
	return d, err
}

// manual reads the manual openings and whether they came from the manual file. Before there was one, every opening
// in desired.json was manual: the first update writes them to the manual file, once.
func (c *Client) manual() (Desired, bool, error) {
	d, ok, err := readDesired(c.manualPath())
	if err != nil || ok {
		return d, ok, err
	}
	d, _, err = readDesired(c.desiredPath())
	d.Rev = 0
	return d, false, err
}

// View reads the files and the automatic openings.
func (c *Client) View() View {
	st, err := c.Status()
	if err != nil {
		return View{Auto: []AutoOpening{}}
	}
	c.mu.Lock()
	auto := slices.Clone(c.auto)
	c.mu.Unlock()
	if auto == nil {
		auto = []AutoOpening{}
	}
	v := View{Installed: true, Status: st, Auto: auto}
	if d, err := c.Desired(); err == nil {
		v.Desired, v.Pending = &d, d.Rev > st.Rev
	}
	if m, err := c.Manual(); err == nil {
		v.Manual = &m
	}
	return v
}

// ClosedOnHost reports a close-all on the host (mtx-portgate close-all) that the sidecar has not taken in yet. Until
// CloseAll has written a desired state that carries it, the helper opens nothing, and Set refuses.
func (c *Client) ClosedOnHost(st *Status) bool {
	if st.ClosedAll == nil {
		return false
	}
	d, err := c.Desired()
	return err != nil || !takenIn(st, d)
}

func takenIn(st *Status, d Desired) bool {
	return st.ClosedAll != nil && d.ClosedAll != nil && d.ClosedAll.Equal(*st.ClosedAll)
}

// Set opens port id manually as w, or closes the manual opening when w is nil, checked against the limits the helper
// reports (the helper checks again against its own policy). It returns the desired state written.
func (c *Client) Set(id string, w *Want) (Desired, error) {
	return c.update(false, func(st *Status, m *Desired) error {
		if _, ok := st.Ports[id]; !ok {
			return fmt.Errorf("unknown port %q", id)
		}
		if w == nil {
			delete(m.Want, id)
			return nil
		}
		pol := policyOf(st)
		checked, err := pol.Check(Desired{Rev: 1, Want: map[string]Want{id: *w}}, c.now())
		if err != nil {
			return err
		}
		m.Want[id] = checked.Want[id]
		return nil
	})
}

// CloseAll removes every manual opening and every automatic one (the caller switches the automatic rules off). It
// also takes in a close-all on the host.
func (c *Client) CloseAll() (Desired, error) {
	c.mu.Lock()
	c.auto = nil
	c.mu.Unlock()
	return c.update(true, func(_ *Status, m *Desired) error {
		m.Want = map[string]Want{}
		return nil
	})
}

// SetAuto replaces the automatic openings; desired.json changes only if the merge does (and not at all while a
// close-all on the host has not been taken in).
func (c *Client) SetAuto(auto []AutoOpening) error {
	c.mu.Lock()
	c.auto = slices.Clone(auto)
	c.mu.Unlock()
	if _, err := c.update(false, nil); !errors.Is(err, ErrClosedOnHost) {
		return err
	}
	return nil
}

func policyOf(st *Status) Policy {
	pol := Policy{Ports: map[string]PortSpec{}, Limits: st.Limits}
	for k, p := range st.Ports {
		pol.Ports[k] = PortSpec{Proto: p.Proto, Port: p.Port}
	}
	return pol
}

// update applies change (if any) to the manual openings, then writes desired.json as manual plus automatic,
// dropping what has expired or the policy no longer allows, when that differs from what is there. It returns the
// desired state. While a close-all on the host has not been taken in, only closing (CloseAll, which takes it in)
// writes anything.
func (c *Client) update(closing bool, change func(*Status, *Desired) error) (Desired, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := c.Status()
	if err != nil {
		return Desired{}, err
	}
	cur, err := c.Desired()
	if err != nil {
		return Desired{}, err
	}
	if st.ClosedAll != nil && !closing && !takenIn(st, cur) {
		return cur, ErrClosedOnHost
	}
	manual, fromFile, err := c.manual()
	if err != nil {
		return Desired{}, err
	}
	now := c.now()
	pol := policyOf(st)
	// What has expired goes, and so does what the policy no longer allows (the operator tightened it): the helper
	// would refuse the whole state over it.
	kept, _ := pol.Allowed(manual, now)
	dropped := len(kept.Want) != len(manual.Want)
	manual = kept
	if change != nil || !fromFile || dropped {
		if change != nil {
			if err := change(st, &manual); err != nil {
				return Desired{}, err
			}
		}
		if err := WriteFileAtomic(c.manualPath(), manual, 0o600); err != nil {
			return Desired{}, err
		}
	}
	merged := merge(manual, c.auto, pol, now)
	merged.ClosedAll = st.ClosedAll // the host's close-all, taken in now or before
	if reflect.DeepEqual(cur.Want, merged.Want) && change == nil {
		return cur, nil
	}
	base := max(cur.Rev, st.Rev) // above anything the helper accepted, even after close-all or a lost file
	if base-st.Rev >= MaxRevStep {
		base = st.Rev // a rev the helper refuses as a jump (not this sidecar's counting): count on from the helper's
	}
	merged.Rev = base + 1
	return merged, WriteFileAtomic(c.desiredPath(), merged, 0o600)
}

// merge combines manual and automatic openings, port by port. Opening to anyone subsumes the named sources and
// takes the latest expiry among the openings to anyone (so a 30-day named opening cannot stretch it past the policy);
// otherwise the sources are the union (manual first, automatic in the order given, at most the policy's maximum)
// until the latest of them. The helper keeps one expiry per port, so a source can outlive its own there while the
// sidecar runs; the sidecar recomputes often and drops it, and the helper's expiry is only the backstop for a dead
// sidecar. Each automatic opening is fitted to the policy on its own (an expiry beyond its longest is shortened, an
// opening it refuses is left out), so one that does not fit costs no other opening on its port; a port whose merge
// the policy would still refuse falls back to its manual opening alone, so an automatic opening can never make the
// helper refuse the whole state.
func merge(manual Desired, auto []AutoOpening, pol Policy, now time.Time) Desired {
	type acc struct {
		anyone      bool
		anyoneUntil *time.Time
		sources     []string
		until       *time.Time
	}
	later := func(a, b *time.Time) *time.Time {
		if a == nil || b == nil {
			return nil // no expiry wins
		}
		if b.After(*a) {
			return b
		}
		return a
	}
	ports := map[string]*acc{}
	get := func(id string) *acc {
		if ports[id] == nil {
			ports[id] = &acc{}
		}
		return ports[id]
	}
	for id, w := range manual.Want {
		a := get(id)
		if len(w.Sources) == 0 {
			a.anyone, a.anyoneUntil = true, w.Until
		} else {
			a.sources, a.until = slices.Clone(w.Sources), w.Until
		}
	}
	for _, o := range auto {
		anyone := o.Source == ""
		until := clip(o.Until, pol.ttl(anyone), now)
		if _, known := pol.Ports[o.Port]; !known || !until.After(now) || (anyone && !pol.AllowAnySource) {
			continue
		}
		if anyone {
			a := get(o.Port)
			if !a.anyone {
				a.anyone, a.anyoneUntil = true, &until
			} else if a.anyoneUntil != nil {
				a.anyoneUntil = later(a.anyoneUntil, &until)
			}
			continue
		}
		src, err := ParseSource(o.Source)
		if err != nil || pol.checkWidth(src.String()) != nil {
			continue
		}
		a := get(o.Port)
		if slices.Contains(a.sources, src.String()) || len(a.sources) >= pol.MaxSources {
			continue
		}
		a.sources = append(a.sources, src.String())
		if a.until == nil && len(a.sources) == 1 {
			a.until = &until
		} else if a.until != nil {
			a.until = later(a.until, &until)
		}
	}
	out := Desired{Want: map[string]Want{}}
	for id, a := range ports {
		w := Want{Sources: a.sources, Until: a.until}
		if a.anyone {
			w = Want{Until: a.anyoneUntil}
		}
		checked, err := pol.Check(Desired{Rev: 1, Want: map[string]Want{id: w}}, now)
		if err == nil {
			out.Want[id] = checked.Want[id]
		} else if m, ok := manual.Want[id]; ok {
			out.Want[id] = m
		}
	}
	return out
}

// clip shortens an automatic opening's expiry to the longest the policy allows (a remembered encoder's 30 days under
// a shorter maxTTL), in steps of up to an hour, so that the shortened expiry does not rewrite desired.json on every
// recompute.
func clip(until time.Time, ttl time.Duration, now time.Time) time.Time {
	if limit := now.Add(ttl).Truncate(min(ttl/8, time.Hour)); until.After(limit) {
		return limit
	}
	return until
}

// Watch streams the view to publish whenever it changes, checking every interval, until ctx ends.
func (c *Client) Watch(ctx context.Context, interval time.Duration, publish func(View)) {
	var last string
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		v := c.View()
		if b, _ := json.Marshal(v); string(b) != last {
			last = string(b)
			publish(v)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
