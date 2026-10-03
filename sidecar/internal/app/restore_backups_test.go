package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/backup"
	"mtxui/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// stageRestore makes a backup on backupHarness's server, checks it (the backup's clip is missing by then, so the
// check adds it) and asks for the restore. state/ then also holds a copy of the harness's database, with the
// request's own audit entry, as the database the restore replaces.
func stageRestore(t *testing.T, h *harness, data string) {
	t.Helper()
	if rec := h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass}); rec.Code != http.StatusNoContent {
		t.Fatalf("passphrase: %d", rec.Code)
	}
	var made BackupInfo
	h.json(h.do("POST", "/api/v1/backups", nil), &made)
	if err := os.Remove(filepath.Join(data, "holding", "1-abcdef.mp4")); err != nil {
		t.Fatal(err)
	}
	if rec := h.do("POST", "/api/v1/backups/"+made.Name+"/check", map[string]any{"passphrase": backupPass}); rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	h.srv.d.Restart = func() {}
	if rec := h.do("POST", "/api/v1/backups/"+made.Name+"/restore", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	if err := h.st.Snapshot(context.Background(), filepath.Join(data, "state", "mtxui.db")); err != nil {
		t.Fatal(err)
	}
}

// A start after a restore that fails before it finishes the restore (the restored database fails a migration, or
// the start is stopped) used to make the next start delete state/pre-restore, the only copy of the previous
// database and key, and then fail at every start. Now the next start puts the previous ones back and records the
// failure, and the check's leftovers go.
func TestRestoreUndoneAfterAFailedStart(t *testing.T) {
	ctx := context.Background()
	h, data := backupHarness(t)
	stageRestore(t, h, data)
	state, cfg := filepath.Join(data, "state"), h.srv.d.Settings
	oldDB, _ := os.ReadFile(filepath.Join(state, "mtxui.db"))
	oldKey, _ := os.ReadFile(filepath.Join(state, "credential-key"))

	if sr, err := PrepareRestore(cfg, quiet); err != nil || sr == nil || sr.failed != "" {
		t.Fatalf("the first start: %+v %v", sr, err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "mtxui.db")); bytes.Equal(b, oldDB) {
		t.Fatal("the backup's database is not in place")
	}
	// That start fails before FinishRestore. The next one:
	sr, err := PrepareRestore(cfg, quiet)
	if err != nil || sr == nil || sr.failed == "" {
		t.Fatalf("the second start: %+v %v", sr, err)
	}
	for name, want := range map[string][]byte{"mtxui.db": oldDB, "credential-key": oldKey} {
		if got, _ := os.ReadFile(filepath.Join(state, name)); !bytes.Equal(got, want) {
			t.Errorf("%s is not the previous one", name)
		}
	}
	for _, gone := range []string{preRestore, restoreReady} {
		if exists(filepath.Join(state, gone)) {
			t.Errorf("state/%s stayed", gone)
		}
	}
	old, err := store.Open(ctx, filepath.Join(state, "mtxui.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	FinishRestore(ctx, sr, cfg, old, h.srv.d.Config, audit.NewRecorder(old, quiet), quiet)
	events, _ := old.ListAudit(ctx, 2)
	if len(events) != 2 || events[0].Action != "backup.restore_failed" || events[0].Details["requestedBy"] != "admin" || events[0].IP != client ||
		events[1].Action != "backup.restore" {
		t.Fatalf("audit: %+v", events)
	}
	if exists(filepath.Join(data, "holding", "1-abcdef.mp4")) {
		t.Error("the clip the check added stayed")
	}
	if left, _ := filepath.Glob(filepath.Join(state, "restore-*")); len(left) != 0 {
		t.Errorf("left in state/: %v", left)
	}
	if sr, err := PrepareRestore(cfg, quiet); sr != nil || err != nil {
		t.Fatalf("the start after: %+v %v", sr, err)
	}
}

// A stop in the middle of the swap: the next start goes on from there, and nothing of the previous database is lost
// (it used to be deleted with state/pre-restore). Should that start fail too, the one after puts it all back.
func TestRestoreSwapGoesOnAfterAStop(t *testing.T) {
	h, _ := backupHarness(t)
	old := map[string]string{"mtxui.db": "old db", "mtxui.db-wal": "old wal", "credential-key": "old key"}
	fresh := map[string]string{backup.DBFile: "backup db", backup.KeyFile: "backup key"}
	restored := map[string]string{"mtxui.db": "backup db", "credential-key": "backup key"}
	pick := func(m map[string]string, keys ...string) map[string]string {
		out := map[string]string{}
		for _, k := range keys {
			out[k] = m[k]
		}
		return out
	}
	for _, tc := range []struct {
		name              string
		phase             string
		state, aside, dir map[string]string // as the stop left them; aside nil: no state/pre-restore
	}{
		{name: "before the start", phase: "", state: old, aside: map[string]string{"mtxui.db": "an earlier restore's"}, dir: fresh},
		{name: "nothing moved", phase: phaseSwapping, state: old, aside: map[string]string{}, dir: fresh},
		{name: "the database aside", phase: phaseSwapping, state: pick(old, "mtxui.db-wal", "credential-key"), aside: pick(old, "mtxui.db"), dir: fresh},
		{name: "all aside", phase: phaseSwapping, state: map[string]string{}, aside: old, dir: fresh},
		{name: "the backup's database in", phase: phaseSwapping, state: pick(restored, "mtxui.db"), aside: old, dir: pick(fresh, backup.KeyFile)},
		{name: "all in, not recorded", phase: phaseSwapping, state: restored, aside: old, dir: map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := *h.srv.d.Settings
			cfg.DataDir = t.TempDir()
			state := cfg.StateDir()
			write := func(dir string, files map[string]string) {
				t.Helper()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for name, content := range files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			write(state, tc.state)
			if tc.aside != nil {
				write(filepath.Join(state, preRestore), tc.aside)
			}
			dir := filepath.Join(state, restoreReady)
			write(dir, tc.dir)
			b, _ := json.Marshal(backupExtract{Name: "the backup", Phase: tc.phase})
			write(dir, map[string]string{readyFile: string(b)})

			sr, err := PrepareRestore(&cfg, quiet)
			if err != nil || sr == nil || sr.failed != "" {
				t.Fatalf("prepare: %+v %v", sr, err)
			}
			if got := files(t, state); !maps.Equal(got, restored) {
				t.Errorf("state/ after the swap: %v", got)
			}
			if got := files(t, filepath.Join(state, preRestore)); !maps.Equal(got, old) {
				t.Errorf("state/pre-restore after the swap: %v", got)
			}
			if !slices.Equal(sr.ex.Moved, []string{"mtxui.db", "mtxui.db-wal", "credential-key"}) {
				t.Errorf("moved %v", sr.ex.Moved)
			}

			// This start fails too, after opening the restored database (which leaves a WAL of its own).
			write(state, map[string]string{"mtxui.db-wal": "the restored database's wal"})
			sr, err = PrepareRestore(&cfg, quiet)
			if err != nil || sr == nil || sr.failed == "" {
				t.Fatalf("the start after: %+v %v", sr, err)
			}
			if got := files(t, state); !maps.Equal(got, old) {
				t.Errorf("state/ after the undo: %v", got)
			}
		})
	}
}

// files reads the regular files of a directory.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	out := map[string]string{}
	for _, e := range entries {
		if e.Type().IsRegular() {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name()] = string(b)
		}
	}
	return out
}

// A stop after the restored database opened: the next start finishes the restore (again) instead of undoing it. The
// restored database keeps the replaced one's audit log, the restore is recorded with who asked for it, and the
// backup's mediamtx.yml goes through the same guard as an edit in the UI.
func TestRestoreFinishesAfterAStop(t *testing.T) {
	ctx := context.Background()
	h, data := backupHarness(t)
	stageRestore(t, h, data)
	state, cfg := filepath.Join(data, "state"), h.srv.d.Settings
	// Someone changed the staged config: it now adds a hook.
	ready := filepath.Join(state, restoreReady, backup.ConfigFile)
	conf, _ := os.ReadFile(ready)
	if err := os.WriteFile(ready, append(conf, "runOnConnect: /mediamtx\n"...), 0o600); err != nil {
		t.Fatal(err)
	}

	sr, err := PrepareRestore(cfg, quiet)
	if err != nil || sr == nil {
		t.Fatalf("prepare: %v", err)
	}
	restoredDB, _ := os.ReadFile(filepath.Join(state, "mtxui.db"))
	if err := sr.mark(phaseOpened); err != nil { // how far FinishRestore got before the stop
		t.Fatal(err)
	}
	sr, err = PrepareRestore(cfg, quiet)
	if err != nil || sr == nil || sr.failed != "" {
		t.Fatalf("the start after the stop: %+v %v", sr, err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "mtxui.db")); !bytes.Equal(b, restoredDB) {
		t.Fatal("the restored database is not in place")
	}
	restored, err := store.Open(ctx, filepath.Join(state, "mtxui.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	before, _ := os.ReadFile(h.srv.d.Config.Path)
	FinishRestore(ctx, sr, cfg, restored, h.srv.d.Config, audit.NewRecorder(restored, quiet), quiet)
	if after, _ := os.ReadFile(h.srv.d.Config.Path); !bytes.Equal(before, after) {
		t.Errorf("a config with a new hook was written:\n%s", after)
	}
	events, _ := restored.ListAudit(ctx, 100)
	var restore, finished []store.AuditEvent
	for _, e := range events {
		switch e.Action {
		case "backup.restore":
			restore = append(restore, e)
		case "backup.restored":
			finished = append(finished, e)
		}
	}
	if len(restore) != 1 || restore[0].Actor != "admin" {
		t.Errorf("the request's own entry, from the replaced database: %+v", restore)
	}
	if len(finished) != 1 || finished[0].Details["requestedBy"] != "admin" || finished[0].IP != client {
		t.Fatalf("the restore's entry: %+v", finished)
	}
	if msg, _ := finished[0].Details["config"].(string); !strings.Contains(msg, "Hooks") {
		t.Errorf("the refused config, in the restore's entry: %q", msg)
	}
	if n, _ := finished[0].Details["auditCarried"].(float64); n < 1 || events[1].Action != "backup.restore" { // newest first
		t.Errorf("carried %v entries, right before the restore's entry: %+v", finished[0].Details["auditCarried"], events[:2])
	}
	if exists(filepath.Join(state, restoreReady)) || !exists(filepath.Join(state, preRestore, "mtxui.db")) {
		t.Error("the ready restore stayed, or the replaced database went")
	}
	if !exists(filepath.Join(data, "holding", "1-abcdef.mp4")) {
		t.Error("the restored clip went")
	}
}

// The check vets the backup's mediamtx.yml like a config edit: a hook the file did not have, or a source pointing at a
// metadata address, is refused (each was written by hand on the server the backup comes from).
func TestBackupCheckVetsTheConfig(t *testing.T) {
	for _, tc := range []struct{ name, add, want string }{
		{"a hook", "runOnConnect: /mediamtx\n", "Hooks"},
		{"a source to a metadata address", "  evil:\n    source: rtsp://169.254.169.254/x\n", "link-local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, data := backupHarness(t)
			h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass})
			path := h.srv.d.Config.Path
			conf, _ := os.ReadFile(path)
			if err := os.WriteFile(path, append(slices.Clone(conf), tc.add...), 0o600); err != nil {
				t.Fatal(err)
			}
			var made BackupInfo
			h.json(h.do("POST", "/api/v1/backups", nil), &made)
			if err := os.WriteFile(path, conf, 0o600); err != nil {
				t.Fatal(err)
			}
			rec := h.do("POST", "/api/v1/backups/"+made.Name+"/check", map[string]any{"passphrase": backupPass})
			if msg, _ := decode(rec)["message"].(string); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(msg, tc.want) {
				t.Fatalf("check: %d %s", rec.Code, rec.Body)
			}
			if h.backupsView().Staged != nil {
				t.Error("a refused backup is staged")
			}
			if left, _ := filepath.Glob(filepath.Join(data, "state", "restore-*")); len(left) != 0 {
				t.Errorf("left in state/: %v", left)
			}
		})
	}
}

// The clips a check adds to the holding directory go when it is abandoned, after a restart too, but not the ones a
// stream has started to use since. A start also removes database copies that a stop left behind.
func TestCheckedClipsGoUnlessUsed(t *testing.T) {
	ctx := context.Background()
	h, data := backupHarness(t)
	h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass})
	var made BackupInfo
	h.json(h.do("POST", "/api/v1/backups", nil), &made)
	clip := filepath.Join(data, "holding", "1-abcdef.mp4")
	check := func() {
		t.Helper()
		_ = os.Remove(clip)
		if rec := h.do("POST", "/api/v1/backups/"+made.Name+"/check", map[string]any{"passphrase": backupPass}); rec.Code != http.StatusOK {
			t.Fatalf("check: %d %s", rec.Code, rec.Body)
		}
		if !exists(clip) {
			t.Fatal("the check did not add the clip")
		}
	}

	check()
	copyLeft := filepath.Join(data, "state", ".backup-0123456789abcdef.db")
	if err := os.WriteFile(copyLeft, []byte("a plaintext copy of the database"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The process ends; the next start:
	FinishRestore(ctx, nil, h.srv.d.Settings, h.st, h.srv.d.Config, h.srv.d.Audit, quiet)
	if exists(clip) {
		t.Error("the clip a check added outlived the restart")
	}
	if got := slices.Sorted(maps.Keys(files(t, filepath.Join(data, "state")))); !slices.Equal(got, []string{"credential-key"}) {
		t.Errorf("left in state/: %v", got)
	}
	if left, _ := filepath.Glob(filepath.Join(data, "state", "restore-staging-*")); len(left) != 0 {
		t.Errorf("left in state/: %v", left)
	}

	check()
	if rec := h.do("POST", "/api/v1/streams", map[string]any{"name": "live/cam"}); rec.Code != http.StatusCreated {
		t.Fatal(rec.Code)
	}
	streams, _ := h.st.Streams(ctx)
	streams[0].ClipAAC = "1-abcdef.mp4" // an upload of the same clip, for a stream with the same id
	if err := h.st.UpdateStream(ctx, streams[0]); err != nil {
		t.Fatal(err)
	}
	if rec := h.do("DELETE", "/api/v1/backups/restore", nil); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	if !exists(clip) {
		t.Error("cancelling the check removed a clip a stream uses")
	}
}

// A manual backup does not count as the day's scheduled one, so it no longer makes another scheduled backup the same
// day (which pushed older days out of the keep).
func TestManualBackupsLeaveTheScheduleAlone(t *testing.T) {
	ctx := context.Background()
	h, _ := backupHarness(t)
	h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass})
	day := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	h.srv.scheduledBackup(ctx, day)
	if rec := h.do("POST", "/api/v1/backups", nil); rec.Code != http.StatusCreated {
		t.Fatalf("manual: %d", rec.Code)
	}
	h.srv.scheduledBackup(ctx, day.Add(time.Hour))
	v := h.backupsView()
	if v.Last == nil || v.Last.Kind != "manual" || len(v.Backups) != 2 {
		t.Fatalf("a second scheduled backup the same day: %+v %v", v.Last, kindsOf(v.Backups))
	}

	// A scheduled backup recorded before the day had its own key (an update on the day of one) counts too.
	h2, _ := backupHarness(t)
	h2.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass})
	h2.srv.setLastBackup(ctx, BackupLast{At: day, OK: true, Kind: "scheduled", Name: "mtxui-20261001-040000-scheduled.mtxbackup"})
	h2.srv.scheduledBackup(ctx, day.Add(time.Hour))
	if n := len(h2.backupsView().Backups); n != 0 {
		t.Fatalf("%d backups after one recorded the old way", n)
	}
}

// Uploads go a day after they arrived, whatever date their header gives, and only the newest three stay.
func TestUploadsArePruned(t *testing.T) {
	h, data := backupHarness(t)
	h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass})
	var made BackupInfo
	h.json(h.do("POST", "/api/v1/backups", nil), &made)
	file := h.do("GET", "/api/v1/backups/"+made.Name+"/download", nil).Body.Bytes()
	dir := filepath.Join(data, "backups")
	upload := func(age time.Duration) string {
		t.Helper()
		var up BackupInfo
		rec := h.do("POST", "/api/v1/backups/upload", rawBody(file), contentType("application/octet-stream"))
		h.json(rec, &up)
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload: %d", rec.Code)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(filepath.Join(dir, up.Name), at, at); err != nil {
			t.Fatal(err)
		}
		return up.Name
	}

	stale := upload(25 * time.Hour) // its header says it was made a moment ago
	h.srv.pruneBackups()
	if exists(filepath.Join(dir, stale)) {
		t.Error("an upload older than a day stayed")
	}
	var names []string
	for i := range 4 {
		names = append(names, upload(time.Duration(4-i)*time.Hour))
	}
	for i, name := range names {
		if kept := exists(filepath.Join(dir, name)); kept != (i > 0) {
			t.Errorf("upload %d kept: %v", i, kept)
		}
	}
	if !exists(filepath.Join(dir, made.Name)) {
		t.Error("the manual backup went")
	}
}
