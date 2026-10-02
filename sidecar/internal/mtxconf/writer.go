package mtxconf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

// Checker validates a candidate config; Validator is the production one.
type Checker interface {
	Validate(ctx context.Context, content []byte) error
}

// Writer is the only code that writes mediamtx.yml.
type Writer struct {
	Path     string // mediamtx.yml; its directory is what MediaMTX mounts
	LockPath string // a file outside that directory, so locking never looks like a config change to MediaMTX
	Rules    Rules
	Checker  Checker
	Store    *store.Store
	// Verifier, when set, reads UI edits back from MediaMTX after writing them (Edit, Replace).
	Verifier *Verifier
	// MinGap spaces writes while MediaMTX watches the file: it ignores changes within a second of its last reload
	// (verified with v1.21.1), so a second write in quick succession would never be applied. ReloadGap is the value
	// for a running stack; zero (tests without MediaMTX) writes at once.
	MinGap time.Duration

	mu        sync.Mutex
	last      string    // SHA-256 of the file as last written or reconciled
	lastWrite time.Time // when the file was last written (under the lock)
}

// ReloadGap is Writer.MinGap for a running stack: MediaMTX's one second, with a margin.
const ReloadGap = 1500 * time.Millisecond

// InvalidError is a config that fails validation: the sidecar's rules or MediaMTX's own. Its message is meant for
// the person who wrote the config.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

// Errors of the editing methods.
var (
	ErrConflict  = errors.New("mediamtx.yml has changed since it was read")
	ErrUnchanged = errors.New("the change leaves mediamtx.yml as it is")
)

// Validate runs every check without writing: the sidecar's rules first (cheap, and they explain security
// problems in their own words), then MediaMTX's own validation. Failures are *InvalidError.
func (w *Writer) Validate(ctx context.Context, content []byte) error {
	if err := w.Rules.Check(content); err != nil {
		return &InvalidError{Err: err}
	}
	return w.Checker.Validate(ctx, content)
}

// Write validates content, replaces the file atomically and records a snapshot. On any validation error nothing is
// written.
func (w *Writer) Write(ctx context.Context, content []byte, author, reason string) (store.Snapshot, error) {
	unlock, err := w.lock()
	if err != nil {
		return store.Snapshot{}, err
	}
	defer unlock()
	return w.writeLocked(ctx, content, author, reason)
}

func (w *Writer) writeLocked(ctx context.Context, content []byte, author, reason string) (store.Snapshot, error) {
	if err := w.Validate(ctx, content); err != nil {
		return store.Snapshot{}, err
	}
	w.mu.Lock()
	wait := w.MinGap - time.Since(w.lastWrite)
	w.mu.Unlock()
	if wait > 0 {
		select {
		case <-ctx.Done():
			return store.Snapshot{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	if err := writeAtomic(w.Path, content); err != nil {
		return store.Snapshot{}, err
	}
	w.mu.Lock()
	w.lastWrite = time.Now()
	w.mu.Unlock()
	snap, err := w.Store.InsertSnapshot(ctx, content, author, reason)
	if err == nil {
		w.remember(snap.SHA256)
	}
	return snap, err
}

// Current returns the file as it is now, with its SHA-256 (hex).
func (w *Writer) Current() ([]byte, string, error) {
	b, err := os.ReadFile(w.Path)
	if err != nil {
		return nil, "", err
	}
	return b, sha(b), nil
}

// Guard vets a change before it is validated and written: it sees the file before and after.
type Guard func(before, after []byte) error

// Edit applies edits to the current file with surgical patches (internal/yamledit: every byte outside the edited
// entries stays), then validates and writes the result. Nothing is written when edit or guard fails.
func (w *Writer) Edit(ctx context.Context, author, reason string, guard Guard, edit func(*yamledit.Doc) error) (Result, error) {
	unlock, err := w.lock()
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	before, err := os.ReadFile(w.Path)
	if err != nil {
		return Result{}, err
	}
	doc, err := yamledit.New(before)
	if err != nil {
		return Result{}, &InvalidError{Err: fmt.Errorf("the current mediamtx.yml cannot be edited: %w", err)}
	}
	if err := edit(doc); err != nil {
		return Result{}, err
	}
	return w.finish(ctx, before, doc.Bytes(), author, reason, guard)
}

// Replace writes content as the whole file, if the file still has the SHA-256 expectSHA (the version the author
// edited; empty skips the check).
func (w *Writer) Replace(ctx context.Context, content []byte, expectSHA, author, reason string, guard Guard) (Result, error) {
	unlock, err := w.lock()
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	before, err := os.ReadFile(w.Path)
	if err != nil {
		return Result{}, err
	}
	if expectSHA != "" && sha(before) != expectSHA {
		return Result{}, ErrConflict
	}
	return w.finish(ctx, before, content, author, reason, guard)
}

// Result is a write through Edit or Replace: the snapshot, and what MediaMTX made of it.
type Result struct {
	store.Snapshot
	Applied Applied
}

// NotAppliedError is returned when MediaMTX stopped answering after a write and the previous file was restored.
// Result describes the restore.
type NotAppliedError struct {
	Result Result
	Err    error
}

func (e *NotAppliedError) Error() string { return e.Result.Applied.Message }
func (e *NotAppliedError) Unwrap() error { return e.Err }

func (w *Writer) finish(ctx context.Context, before, after []byte, author, reason string, guard Guard) (Result, error) {
	if bytes.Equal(before, after) {
		return Result{}, ErrUnchanged
	}
	if guard != nil {
		if err := guard(before, after); err != nil {
			return Result{}, err
		}
	}
	// The read-back must not stop because the browser went away: a restore it may lead to is not optional.
	vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	answering := w.Verifier != nil && w.Verifier.Answering(vctx)
	from := before
	if answering {
		// A path leaving the file for a pattern config is closed only if MediaMTX sees a step in between.
		if step, err := closingStep(before, after); err == nil && step != nil {
			if _, err := w.apply(ctx, vctx, before, step, before, author, reason+" (closing its path first)", true); err != nil {
				return Result{}, err
			}
			from = step
		}
	}
	return w.apply(ctx, vctx, from, after, before, author, reason, answering)
}

// apply writes after over from and, while MediaMTX answers, reads it back; when MediaMTX stops answering, restore
// goes back in.
func (w *Writer) apply(ctx, vctx context.Context, from, after, restore []byte, author, reason string, answering bool) (Result, error) {
	snap, err := w.writeLocked(ctx, after, author, reason)
	if err != nil {
		return Result{}, err
	}
	res := Result{Snapshot: snap}
	switch {
	case w.Verifier == nil:
	case !answering:
		res.Applied = Applied{
			State:   AppliedSkipped,
			Message: "MediaMTX was not answering before the change; it reads the file when it is back.",
		}
	default:
		applied, verr := w.Verifier.Verify(vctx, from, after)
		if verr == nil {
			res.Applied = applied
			break
		}
		restored, rerr := w.writeLocked(vctx, restore, "system", fmt.Sprintf("restored: MediaMTX stopped answering after version %d", snap.ID))
		if rerr != nil {
			return res, fmt.Errorf("MediaMTX stopped answering after version %d, and restoring the previous file failed: %w", snap.ID, rerr)
		}
		return Result{Snapshot: restored}, &NotAppliedError{
			Err: verr,
			Result: Result{Snapshot: restored, Applied: Applied{
				State: AppliedRestored,
				Message: fmt.Sprintf("MediaMTX stopped answering after version %d, so the previous file is back as version %d. "+
					"Check MediaMTX's log for why (a port already in use, for example).", snap.ID, restored.ID),
			}},
		}
	}
	return res, nil
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Outcome is what Reconcile did. Notable outcomes deserve a warning and an audit entry; the others are routine.
type Outcome struct {
	Message  string // empty when nothing needed doing
	Notable  bool
	Rejected []byte // an invalid file that was replaced, for the audit log
}

// Reconcile makes sure a valid file is in place and the history knows it: it writes seed when there is no file and no
// history (the last snapshot when there is history), adopts a valid file the database has not seen, records a valid
// outside edit as a snapshot, and replaces an invalid one with the last good snapshot (MediaMTX exits on an invalid
// reload and would crash-loop on the file at start). It runs before MediaMTX starts and then every few
// seconds (Watch). It holds the writer's lock, so the sidecar's own writes never look like outside edits.
func (w *Writer) Reconcile(ctx context.Context, seed []byte) (Outcome, error) {
	unlock, err := w.lock()
	if err != nil {
		return Outcome{}, err
	}
	defer unlock()
	w.removeStaleTemps()
	latest, snapErr := w.Store.LatestSnapshot(ctx)
	if snapErr != nil && !errors.Is(snapErr, store.ErrNotFound) {
		return Outcome{}, snapErr
	}
	current, err := os.ReadFile(w.Path)
	if errors.Is(err, fs.ErrNotExist) {
		if snapErr != nil { // a fresh install
			if _, err := w.writeLocked(ctx, seed, "system", "initial config"); err != nil {
				return Outcome{}, fmt.Errorf("writing the initial config: %w", err)
			}
			return Outcome{Message: "wrote the initial config"}, nil
		}
		if _, err := w.writeLocked(ctx, latest.Content, "system", "file was missing"); err != nil {
			return Outcome{}, fmt.Errorf("restoring snapshot %d: %w", latest.ID, err)
		}
		return Outcome{Message: fmt.Sprintf("mediamtx.yml was missing; restored snapshot %d", latest.ID), Notable: true}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	sum := sha(current)
	if snapErr == nil && latest.SHA256 == sum {
		w.remember(sum)
		return Outcome{}, nil
	}
	if verr := w.Validate(ctx, current); verr != nil {
		if errors.Is(verr, context.Canceled) || errors.Is(verr, context.DeadlineExceeded) {
			return Outcome{}, verr
		}
		restore := seed
		what := "the initial config"
		if snapErr == nil {
			restore, what = latest.Content, fmt.Sprintf("snapshot %d", latest.ID)
		}
		if _, err := w.writeLocked(ctx, restore, "system", "restored after an invalid edit: "+verr.Error()); err != nil {
			return Outcome{}, fmt.Errorf("mediamtx.yml is invalid (%w) and restoring %s failed: %w", verr, what, err)
		}
		return Outcome{
			Message: fmt.Sprintf("mediamtx.yml was changed outside the sidecar into an invalid config (%v); restored %s", verr, what),
			Notable: true, Rejected: current,
		}, nil
	}
	if snapErr != nil {
		if _, err := w.Store.InsertSnapshot(ctx, current, "system", "adopted existing file"); err != nil {
			return Outcome{}, err
		}
		w.remember(sum)
		return Outcome{Message: "adopted the existing config"}, nil
	}
	snap, err := w.Store.InsertSnapshot(ctx, current, "external", "changed outside the sidecar")
	if err != nil {
		return Outcome{}, err
	}
	w.remember(sum)
	return Outcome{
		Message: fmt.Sprintf("mediamtx.yml was changed outside the sidecar; the change is valid and was recorded as snapshot %d", snap.ID),
		Notable: true,
	}, nil
}

// Watch runs Reconcile every interval, skipping the work while the file's hash is the one last seen, and hands each
// outcome that did something to report. It returns when ctx ends.
func (w *Writer) Watch(ctx context.Context, interval time.Duration, seed []byte, report func(Outcome, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if b, err := os.ReadFile(w.Path); err == nil && sha(b) == w.known() {
			continue
		}
		out, err := w.Reconcile(ctx, seed)
		if ctx.Err() != nil {
			return
		}
		if err != nil || out.Message != "" {
			report(out, err)
		}
	}
}

func (w *Writer) remember(sum string) {
	w.mu.Lock()
	w.last = sum
	w.mu.Unlock()
}

func (w *Writer) known() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

func (w *Writer) lock() (func(), error) {
	f, err := os.OpenFile(w.LockPath, os.O_RDWR|os.O_CREATE, 0o600)
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

// removeStaleTemps deletes temp files a write killed before its rename left next to the file (see writeAtomic). It
// runs under the lock, so no write is in progress.
func (w *Writer) removeStaleTemps() {
	stale, _ := filepath.Glob(filepath.Join(filepath.Dir(w.Path), tempPattern))
	for _, f := range stale {
		_ = os.Remove(f)
	}
}

const tempPattern = ".mediamtx.yml.tmp-*"

// writeAtomic writes content next to path, syncs it, renames it over path and syncs the directory, so a crash
// leaves either the old or the new file, never a partial one. The mode is 0640: MediaMTX shares the sidecar's group.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
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
	if err := os.Rename(tmp.Name(), path); err != nil { //nolint:gosec // path is the writer's own config file, never request input
		return err
	}
	d, err := os.Open(dir) //nolint:gosec // the directory of that file
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
