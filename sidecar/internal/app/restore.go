package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/backup"
	"mtxui/internal/mtxconf"
	"mtxui/internal/netguard"
	"mtxui/internal/settings"
	"mtxui/internal/store"
)

// A restore's second half, at the next start: PrepareRestore before the database is opened, FinishRestore right
// after. A start can stop anywhere in between (a migration that fails, a stop, a kill), so ready.json records how far
// the restore got, replaced in one step each time it moves on, and every step can run again:
//
//	(none)    the request staged the checked backup; nothing has moved yet
//	swapping  the current database and key are moving into state/pre-restore/, the backup's into place
//	swapped   the backup's are in place, and the start that put them there has not reached FinishRestore
//	opened    the restored database opened and migrated; what is left (audit, sessions, clips, config) runs again
//
// A start that finds "swapped" comes after one that did not get the restored database open, or was stopped first:
// the previous database and key go back and the restore is recorded as failed, where otherwise every later start
// would fail the same way. state/pre-restore/ is emptied only when the next restore begins, never while one is
// under way, so the previous database and key are always somewhere.

const (
	phaseSwapping = "swapping"
	phaseSwapped  = "swapped"
	phaseOpened   = "opened"

	preRestore = "pre-restore" // under state/: the database and key the last restore replaced
)

// StagedRestore is a restore swapped in at start, to finish once the database is open, or one given up at start.
type StagedRestore struct {
	dir    string
	ex     backupExtract
	failed string // why it was given up; the database and key that were there before are in place
}

// stateFiles are what a restore replaces in state/: the database (with its WAL files) and the credential key.
var stateFiles = []string{"mtxui.db", "mtxui.db-wal", "mtxui.db-shm", "credential-key"}

// PrepareRestore swaps in a ready restore: the current database and credential key move to state/pre-restore/
// (replacing what an earlier restore left there), the backup's into place. Without a ready restore it returns nil.
// A restore the last start swapped in but did not finish is undone instead (see above). An error leaves every file
// where it is, for the next start to go on from.
func PrepareRestore(cfg *settings.Settings, log *slog.Logger) (*StagedRestore, error) {
	state := cfg.StateDir()
	dir := filepath.Join(state, restoreReady)
	b, err := os.ReadFile(filepath.Join(dir, readyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sr := &StagedRestore{dir: dir}
	if err := json.Unmarshal(b, &sr.ex); err != nil {
		return nil, fmt.Errorf("the ready restore: %w", err)
	}
	aside := filepath.Join(state, preRestore)
	switch sr.ex.Phase {
	case "":
		if !exists(filepath.Join(dir, backup.DBFile)) || !exists(filepath.Join(dir, backup.KeyFile)) {
			return sr.drop(state, log, "its database or credential key is missing from state/"+restoreReady)
		}
		if err := os.RemoveAll(aside); err != nil {
			return nil, err
		}
		if err := os.Mkdir(aside, 0o700); err != nil {
			return nil, err
		}
		if err := sr.mark(phaseSwapping); err != nil {
			return nil, err
		}
		fallthrough
	case phaseSwapping:
		if err := swapIn(state, dir, aside, &sr.ex); err != nil {
			return nil, err
		}
		if err := sr.mark(phaseSwapped); err != nil {
			return nil, err
		}
		log.Warn("restoring a backup: its database and credential key are in place; the previous ones are in state/pre-restore", "backup", sr.ex.Name)
		return sr, nil
	case phaseSwapped:
		if err := swapBack(state, aside, sr.ex.Moved); err != nil {
			return nil, fmt.Errorf("putting the previous database back after a restore that did not finish: %w", err)
		}
		return sr.drop(state, log, "the start after the restore did not finish it (its log says why), so the previous database and credential key are back")
	case phaseOpened:
		return sr, nil
	default:
		return nil, fmt.Errorf("the ready restore: unknown phase %q", sr.ex.Phase)
	}
}

// drop gives up a restore before its database is opened: what is left of it goes the way of a check no restore took
// (FinishRestore clears it away and records the failure).
func (sr *StagedRestore) drop(state string, log *slog.Logger, why string) (*StagedRestore, error) {
	if err := os.Rename(sr.dir, filepath.Join(state, "restore-staging-"+randomHex())); err != nil {
		return nil, err
	}
	log.Error("the restore failed: "+why, "backup", sr.ex.Name)
	sr.failed = why
	return sr, nil
}

// mark records how far the restore got: ready.json is replaced in one step.
func (sr *StagedRestore) mark(phase string) error {
	sr.ex.Phase = phase
	b, err := json.Marshal(sr.ex)
	if err != nil {
		return err
	}
	tmp := filepath.Join(sr.dir, readyFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(sr.dir, readyFile))
}

// swapIn moves the current files aside and the backup's database and key into place (the database first), and
// records in ex.Moved what went aside. Run again after it was cut off, it goes on from where it stopped: while the
// backup's database is still in dir, the current files are moving aside; once it is not, they all are.
func swapIn(state, dir, aside string, ex *backupExtract) error {
	if exists(filepath.Join(dir, backup.DBFile)) {
		for _, name := range stateFiles {
			err := os.Rename(filepath.Join(state, name), filepath.Join(aside, name))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("moving %s aside: %w", name, err)
			}
		}
	}
	ex.Moved = nil
	for _, name := range stateFiles {
		if exists(filepath.Join(aside, name)) {
			ex.Moved = append(ex.Moved, name)
		}
	}
	for _, f := range []struct{ from, to string }{{backup.DBFile, "mtxui.db"}, {backup.KeyFile, "credential-key"}} {
		if !exists(filepath.Join(dir, f.from)) {
			continue // in place since the run that was cut off
		}
		if err := os.Rename(filepath.Join(dir, f.from), filepath.Join(state, f.to)); err != nil {
			return fmt.Errorf("putting the backup's %s in place: %w", f.to, err)
		}
	}
	return nil
}

// swapBack undoes swapIn: the files that were there before come back, and the restored ones go (with the WAL files
// opening them left). moved is fixed once the swap is done, so running it again after it was cut off does the same.
func swapBack(state, aside string, moved []string) error {
	for _, name := range stateFiles {
		var err error
		if slices.Contains(moved, name) {
			err = os.Rename(filepath.Join(aside, name), filepath.Join(state, name))
		} else {
			err = os.Remove(filepath.Join(state, name))
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	_ = os.Remove(aside)
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// FinishRestore completes a restore once the restored database is open and migrated: the audit log of the database
// it replaced is carried over (a restore takes no entry out of the log), every session ends, the backup's clips are
// in the holding directory, its mediamtx.yml is written through the writer with the same guard as an edit in the UI
// (no hook changes, new sources and forward destinations vetted; validated, and a snapshot), the restore is audited
// with who asked for it, and the staged files go. A config that fails here is reported, and the start continues
// with the file as it was (the writer's reconcile then deals with it). A restore PrepareRestore gave up is audited
// as failed.
//
// Every start calls it, with or without a restore: it also clears away what earlier runs left in state/.
func FinishRestore(ctx context.Context, sr *StagedRestore, cfg *settings.Settings, st *store.Store, w *mtxconf.Writer, rec *audit.Recorder, log *slog.Logger) {
	s := &Server{d: Deps{Settings: cfg, Store: st, Config: w, NetGuard: netguard.New(cfg.StackSubnet), Log: log}}
	defer s.cleanStaging(ctx) // last, so that the restore's own clips count as in use
	if sr == nil {
		return
	}
	details := map[string]any{"backup": sr.ex.Name}
	if sr.ex.By != "" {
		details["requestedBy"] = sr.ex.By
	}
	ev := store.AuditEvent{Actor: "system", IP: sr.ex.IP, Action: "backup.restored", Target: sr.ex.Name, Details: details}
	if sr.failed != "" {
		ev.Action, details["error"] = "backup.restore_failed", sentence(errors.New(sr.failed))
		rec.Record(ctx, ev)
		return
	}
	if err := sr.mark(phaseOpened); err != nil {
		log.Error("recording the restore's progress", "err", err)
		_ = os.Remove(filepath.Join(sr.dir, readyFile)) // a later start must not take it for a restore that never opened
	}
	// The entries carried over come right before this restore's own entry; the ones before them came with the backup.
	if n, err := st.CarryAudit(ctx, filepath.Join(cfg.StateDir(), preRestore, "mtxui.db")); err != nil {
		log.Error("carrying the replaced database's audit log over", "err", err)
		details["audit"] = err.Error()
	} else {
		details["auditCarried"] = n
	}
	if err := st.ClearSessions(ctx); err != nil {
		log.Error("ending the sessions after a restore", "err", err)
	}
	if err := s.placeClips(sr.dir, sr.ex.Clips); err != nil {
		log.Error("putting the backup's holding clips in place", "err", err)
		details["clips"] = err.Error()
	}
	conf, err := os.ReadFile(filepath.Join(sr.dir, backup.ConfigFile))
	if err == nil {
		_, err = w.Replace(ctx, conf, "", "system", "restored from "+sr.ex.Name, s.guardEdit)
		if errors.Is(err, mtxconf.ErrUnchanged) {
			err = nil
		}
	}
	if err != nil {
		log.Error("writing the backup's mediamtx.yml", "err", err)
		details["config"] = err.Error()
	}
	rec.Record(ctx, ev)
	if err := os.RemoveAll(sr.dir); err != nil {
		log.Warn("removing the restored files", "err", err)
	}
	log.Warn("restored a backup; everyone signs in again", "backup", sr.ex.Name, "at", time.Now().UTC())
}

// cleanStaging clears away what earlier runs left in state/: checked backups no restore took (a restart ended their
// 15 minutes, or the restore was given up), with the clips their checks added, and the plaintext database copies of
// backups a stop cut off. It runs at start, before anything else uses state/.
func (s *Server) cleanStaging(ctx context.Context) {
	state := s.d.Settings.StateDir()
	entries, _ := os.ReadDir(state)
	for _, e := range entries {
		switch name := e.Name(); {
		case e.IsDir() && strings.HasPrefix(name, "restore-staging-"):
			s.dropStaging(ctx, filepath.Join(state, name))
		case e.Type().IsRegular() && strings.HasPrefix(name, ".backup-"):
			_ = os.Remove(filepath.Join(state, name))
		}
	}
}
