package portgate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Helper is the host side: it runs as root on every change of desired.json and on a timer, and converges the firewall
// rules it owns to what the last accepted desired state still wants (expired entries close even when the sidecar is
// gone, since the timer needs nothing from it).
type Helper struct {
	Policy *Policy
	Driver Driver
	Run    Runner // for the verify and nudge commands
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration) error
	Log    *slog.Logger
}

// MaxRevStep is how far past the accepted rev a desired state may jump. The sidecar counts up by one per write, so a
// bigger jump is not its counting, and refusing it keeps a rev near the int64 maximum (which no later rev could
// pass) from ever being accepted.
const MaxRevStep = 1 << 32

// Files in Policy.StateDir.
const (
	StatusFile   = "status.json"
	acceptedFile = "accepted.json"
	lockFile     = "lock"
)

func (h *Helper) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Helper) sleep(ctx context.Context, d time.Duration) error {
	if h.Sleep != nil {
		return h.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (h *Helper) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Apply reads desired.json, accepts it if it is newer and within policy, and converges the rules. It returns the
// status it wrote. A request written while it ran (the verify step can take a minute) is applied before it returns:
// systemd does not start a path unit's service again for changes made while it is running.
func (h *Helper) Apply(ctx context.Context) (Status, error) {
	for i := 0; ; i++ {
		st, again, err := h.applyOnce(ctx)
		if err != nil || !again || i == 4 {
			return st, err
		}
	}
}

func (h *Helper) applyOnce(ctx context.Context) (Status, bool, error) {
	unlock, err := h.lock()
	if err != nil {
		return Status{}, false, err
	}
	defer unlock()
	prev := h.readStatus()
	accepted := h.readAccepted()
	now := h.now()

	var refusal string
	var refused int64
	refuse := func(rev int64, why string) {
		refusal = fmt.Sprintf("desired state rev %d refused: %s", rev, strings.ReplaceAll(why, "\n", "; "))
		refused = rev
	}
	req, err := ReadRequest(h.Policy.Desired)
	switch {
	case errors.Is(err, ErrNoRequest):
	case err != nil:
		refusal = "desired state refused: " + err.Error()
	case req.Rev > accepted.Rev && req.Rev-accepted.Rev > MaxRevStep:
		refuse(req.Rev, fmt.Sprintf("it jumps more than %d past the accepted rev %d", int64(MaxRevStep), accepted.Rev))
	case req.Rev > accepted.Rev && prev.ClosedAll != nil && (req.ClosedAll == nil || !req.ClosedAll.Equal(*prev.ClosedAll)):
		// The sidecar's routine renewals must not undo the host's close-all: only a state written after the sidecar
		// has taken it in (closed its manual openings, switched its automatic rules off) opens anything again.
		refuse(req.Rev, fmt.Sprintf("everything was closed on the host (mtx-portgate close-all) at %s, and nothing "+
			"opens until the sidecar has taken that in", prev.ClosedAll.UTC().Format(time.RFC3339)))
	case req.Rev > accepted.Rev:
		checked, err := h.Policy.Check(req, now)
		if err != nil {
			refuse(req.Rev, err.Error())
			break
		}
		accepted = checked
		prev.ClosedAll = nil
		if err := h.writeJSON(acceptedFile, accepted, 0o600); err != nil {
			return Status{}, false, err
		}
		h.log().Info("accepted desired state", "rev", accepted.Rev, "ports", len(accepted.Want))
	case req.Rev < accepted.Rev:
		refusal = fmt.Sprintf("desired state rev %d ignored: rev %d was accepted before, and only a newer one applies", req.Rev, accepted.Rev)
	}
	// The policy is read on every run: what it no longer allows (the operator tightened it) closes.
	allowed, err := h.Policy.Allowed(accepted, now)
	if err != nil {
		refusal = joinErr(refusal, "no longer within the policy, closed: "+strings.ReplaceAll(err.Error(), "\n", "; "))
	}
	if refusal != "" {
		h.log().Warn(refusal)
	}
	st := h.converge(ctx, allowed, prev, now)
	st.ClosedAll = prev.ClosedAll
	st.Error = joinErr(refusal, st.Error)
	if err := h.writeJSON(StatusFile, st, 0o644); err != nil {
		return st, false, err
	}
	next, err := ReadRequest(h.Policy.Desired)
	return st, err == nil && next.Rev > accepted.Rev && next.Rev != refused, nil
}

// CloseAll deletes every rule portgate owns. It needs nothing from the sidecar, and it holds: nothing opens again
// until the sidecar has taken it in, which it does by itself as for Close all on the Exposure page (Desired.ClosedAll).
func (h *Helper) CloseAll(ctx context.Context) (Status, error) {
	unlock, err := h.lock()
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	prev := h.readStatus()
	accepted := h.readAccepted()
	if req, err := ReadRequest(h.Policy.Desired); err == nil && req.Rev > accepted.Rev && req.Rev-accepted.Rev <= MaxRevStep {
		accepted.Rev = req.Rev // the pending request must not reopen anything either
	}
	accepted.Want = map[string]Want{}
	if err := h.writeJSON(acceptedFile, accepted, 0o600); err != nil {
		return Status{}, err
	}
	now := h.now()
	h.log().Warn("closing all exposure", "rev", accepted.Rev)
	st := h.converge(ctx, accepted, prev, now)
	st.ClosedAll = &now
	return st, h.writeJSON(StatusFile, st, 0o644)
}

// notInSync starts the error of a port the verify command did not confirm: the next run asks again.
const notInSync = "firewall not confirmed in sync: "

// converge makes the owned rules match what accepted still wants at now, then waits for the firewall that matters.
func (h *Helper) converge(ctx context.Context, accepted Desired, prev Status, now time.Time) Status {
	st := Status{Rev: accepted.Rev, Updated: now, Driver: h.Driver.Name(), Ports: map[string]PortStatus{}, Limits: h.Policy.Limits}
	want := map[Rule]bool{}
	for id, spec := range h.Policy.Ports {
		ps := PortStatus{Proto: spec.Proto, Port: spec.Port, State: StateClosed}
		if w, ok := accepted.Want[id]; ok && (w.Until == nil || w.Until.After(now)) {
			ps.State, ps.Sources, ps.Until = StateOpen, w.Sources, w.Until
			srcs := w.Sources
			if len(srcs) == 0 {
				srcs = []string{""}
			}
			for _, s := range srcs {
				want[Rule{ID: id, Proto: spec.Proto, Port: spec.Port, Source: s}] = true
			}
		}
		st.Ports[id] = ps
	}
	fail := func(id, msg string) {
		st.Error = joinErr(st.Error, msg)
		if ps, ok := st.Ports[id]; ok {
			ps.State, ps.Error = StateError, msg
			st.Ports[id] = ps
		}
	}

	have, err := h.Driver.Rules(ctx)
	if err != nil {
		for id := range st.Ports {
			if st.Ports[id].State == StateOpen {
				fail(id, "listing firewall rules: "+err.Error())
			}
		}
		return st
	}
	changed := false
	// Close first: a failure half-way leaves less open, not more.
	for _, r := range have {
		if want[r] {
			delete(want, r)
			continue
		}
		if err := h.Driver.Delete(ctx, r); err != nil {
			fail(r.ID, fmt.Sprintf("removing %s: %v", r, err))
			continue
		}
		changed = true
		h.log().Info("closed", "rule", r.String())
	}
	adds := make([]Rule, 0, len(want))
	for r := range want {
		adds = append(adds, r)
	}
	slices.SortFunc(adds, func(a, b Rule) int { return strings.Compare(a.String(), b.String()) })
	for _, r := range adds {
		if err := h.Driver.Add(ctx, r); err != nil {
			fail(r.ID, fmt.Sprintf("adding %s: %v", r, err))
			continue
		}
		changed = true
		h.log().Info("opened", "rule", r.String())
	}

	retry := false // a port the last run could not confirm; other errors (an operator's rule in the way) recur anyway
	for _, ps := range prev.Ports {
		retry = retry || (ps.State == StateError && strings.HasPrefix(ps.Error, notInSync))
	}
	if (changed || retry) && h.Policy.Verify != nil {
		if err := h.verify(ctx); err != nil {
			msg := notInSync + err.Error()
			st.Error = joinErr(st.Error, msg)
			for id, ps := range st.Ports {
				if ps.State == StateOpen {
					fail(id, msg)
				}
			}
		}
	}
	return st
}

// nudgeAfter is when verify nudges a sync that has not caught up: a cloud firewall sync normally lands within 3 to
// 5 s, and it loses ufw changes made while it runs (found 2026-09-29), which only its 10-minute timer would pick up
// otherwise. An unneeded nudge costs one sync that finds nothing to do.
const nudgeAfter = 8 * time.Second

// verifyEvery is how often verify asks. An ask may cost calls to a cloud provider's rate-limited API, and a sync usually
// lands within 3 to 5 s, so the first ask waits that long.
const verifyEvery = 5 * time.Second

// verify runs the policy's check until its output says the firewall is in sync, nudging once if it takes long.
func (h *Helper) verify(ctx context.Context) error {
	v := h.Policy.Verify
	timeout := time.Duration(v.Timeout)
	start := h.now()
	nudged := len(v.Nudge) == 0
	var last string
	if err := h.sleep(ctx, verifyEvery); err != nil { // give the sync its usual few seconds before the first ask
		return err
	}
	for {
		out, err := h.Run(ctx, v.Command...)
		if err == nil && strings.Contains(out, v.Expect) {
			return nil
		}
		last = strings.TrimSpace(out)
		if err != nil {
			last = err.Error()
		}
		elapsed := h.now().Sub(start)
		if elapsed >= timeout {
			if i := strings.LastIndexByte(last, '\n'); i >= 0 {
				last = last[i+1:]
			}
			return fmt.Errorf("after %s: %s", timeout, last)
		}
		if !nudged && elapsed >= min(timeout/2, nudgeAfter) {
			nudged = true
			h.log().Warn("firewall sync is late; nudging it")
			if _, err := h.Run(ctx, v.Nudge...); err != nil {
				h.log().Warn("nudge failed", "err", err)
			}
		}
		if err := h.sleep(ctx, verifyEvery); err != nil {
			return err
		}
	}
}

func joinErr(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

func (h *Helper) lock() (func(), error) {
	if err := os.MkdirAll(h.Policy.StateDir, 0o755); err != nil { //nolint:gosec // status.json is meant to be read by the sidecar
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(h.Policy.StateDir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func (h *Helper) readStatus() Status {
	var st Status
	b, err := os.ReadFile(filepath.Join(h.Policy.StateDir, StatusFile))
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (h *Helper) readAccepted() Desired {
	var d Desired
	b, err := os.ReadFile(filepath.Join(h.Policy.StateDir, acceptedFile))
	if err == nil {
		_ = json.Unmarshal(b, &d)
	}
	if d.Want == nil {
		d.Want = map[string]Want{}
	}
	return d
}

func (h *Helper) writeJSON(name string, v any, mode os.FileMode) error {
	return WriteFileAtomic(filepath.Join(h.Policy.StateDir, name), v, mode)
}

// WriteFileAtomic writes v as JSON through a temporary file and a rename, so readers never see half a file.
func WriteFileAtomic(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // gone after the rename
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
