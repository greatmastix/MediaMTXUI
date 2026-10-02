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
	"strings"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/backup"
	"mtxui/internal/mtxconf"
	"mtxui/internal/settings"
	"mtxui/internal/store"
)

// A restore's second half, at the next start: PrepareRestore before the database is opened,
// FinishRestore right after.

// StagedRestore is a restore swapped in at start, to finish once the database is open.
type StagedRestore struct {
	dir string
	ex  backupExtract
}

// stateFiles are what a restore replaces in state/: the database (with its WAL files) and the credential key.
var stateFiles = []string{"mtxui.db", "mtxui.db-wal", "mtxui.db-shm", "credential-key"}

// PrepareRestore swaps in a ready restore: the current database and credential key move to state/pre-restore/
// (replacing an older one), the backup's into place. Without a ready restore it returns nil. On failure the
// current files are put back and the ready restore is left for someone to look at.
func PrepareRestore(cfg *settings.Settings, log *slog.Logger) (*StagedRestore, error) {
	state := cfg.StateDir()
	dir := filepath.Join(state, restoreReady)
	b, err := os.ReadFile(filepath.Join(dir, readyFile))
	if errors.Is(err, fs.ErrNotExist) {
		cleanStaging(state)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ex backupExtract
	if err := json.Unmarshal(b, &ex); err != nil {
		return nil, fmt.Errorf("the ready restore: %w", err)
	}
	aside := filepath.Join(state, "pre-restore")
	if err := os.RemoveAll(aside); err != nil {
		return nil, err
	}
	if err := os.Mkdir(aside, 0o700); err != nil {
		return nil, err
	}
	var moved []string
	undo := func() {
		for _, name := range moved {
			_ = os.Rename(filepath.Join(aside, name), filepath.Join(state, name))
		}
	}
	for _, name := range stateFiles {
		err := os.Rename(filepath.Join(state, name), filepath.Join(aside, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			undo()
			return nil, fmt.Errorf("moving %s aside: %w", name, err)
		}
		moved = append(moved, name)
	}
	for from, to := range map[string]string{backup.DBFile: "mtxui.db", backup.KeyFile: "credential-key"} {
		if err := os.Rename(filepath.Join(dir, from), filepath.Join(state, to)); err != nil {
			_ = os.Remove(filepath.Join(state, "mtxui.db"))
			_ = os.Remove(filepath.Join(state, "credential-key"))
			undo()
			return nil, fmt.Errorf("putting the backup's %s in place: %w", to, err)
		}
	}
	log.Warn("restoring a backup: its database and credential key are in place; the previous ones are in state/pre-restore", "backup", ex.Name)
	cleanStaging(state)
	return &StagedRestore{dir: dir, ex: ex}, nil
}

// cleanStaging removes checked backups no restore took (a restart ended their 15 minutes).
func cleanStaging(state string) {
	entries, _ := os.ReadDir(state)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "restore-staging-") {
			_ = os.RemoveAll(filepath.Join(state, e.Name()))
		}
	}
}

// FinishRestore completes a restore once the restored database is open and migrated: every session ends, the
// backup's clips are in the holding directory, its mediamtx.yml is written through the writer (validated, and a
// snapshot), the restore is audited and the staged files go. A config that fails here is reported, and the start
// continues with the file as it was (the writer's reconcile then deals with it).
func FinishRestore(ctx context.Context, sr *StagedRestore, cfg *settings.Settings, st *store.Store, w *mtxconf.Writer, rec *audit.Recorder, log *slog.Logger) {
	if sr == nil {
		return
	}
	details := map[string]any{"backup": sr.ex.Name}
	if err := st.ClearSessions(ctx); err != nil {
		log.Error("ending the sessions after a restore", "err", err)
	}
	s := &Server{d: Deps{Settings: cfg}}
	if _, err := s.placeClips(sr.dir, sr.ex.Clips); err != nil {
		log.Error("putting the backup's holding clips in place", "err", err)
		details["clips"] = err.Error()
	}
	conf, err := os.ReadFile(filepath.Join(sr.dir, backup.ConfigFile))
	if err == nil {
		cur, _, _ := w.Current()
		if string(cur) != string(conf) {
			_, err = w.Write(ctx, conf, "system", "restored from "+sr.ex.Name)
		}
	}
	if err != nil {
		log.Error("writing the backup's mediamtx.yml", "err", err)
		details["config"] = err.Error()
	}
	rec.Record(ctx, store.AuditEvent{Actor: "system", Action: "backup.restored", Target: sr.ex.Name, Details: details})
	if err := os.RemoveAll(sr.dir); err != nil {
		log.Warn("removing the restored files", "err", err)
	}
	log.Warn("restored a backup; everyone signs in again", "backup", sr.ex.Name, "at", time.Now().UTC())
}
