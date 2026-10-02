package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mtxui/internal/backup"
	"mtxui/internal/store"
)

const backupPass = "the backup passphrase"

func init() { backup.WorkFactor = 10 } // fast; the format does not depend on it

// backupHarness is a signed-in harness with a data directory: state/ (with the harness's credential key),
// backups/ and holding/ (with a clip).
func backupHarness(t *testing.T) (*harness, string) {
	t.Helper()
	data := t.TempDir()
	for _, d := range []string{"state", "backups", "holding", "logs"} {
		if err := os.Mkdir(filepath.Join(data, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, map[string]string{"MTXUI_DATA_DIR": data, "MTXUI_HOLDING_DIR": filepath.Join(data, "holding")}, fast)
	h.completeSetup()
	key, err := os.ReadFile(filepath.Join(h.dir, "credential-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "state", "credential-key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "holding", "1-abcdef.mp4"), []byte("a clip"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, data
}

func (h *harness) backupsView() BackupsView {
	h.t.Helper()
	var v BackupsView
	h.json(h.do("GET", "/api/v1/backups", nil), &v)
	return v
}

func TestBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	h, data := backupHarness(t)
	if v := h.backupsView(); v.Configured || len(v.Backups) != 0 || !v.Schedule.Enabled || v.Schedule.Keep != 7 {
		t.Fatalf("fresh: %+v", v)
	}
	if rec := h.do("POST", "/api/v1/backups", nil); rec.Code != http.StatusConflict {
		t.Fatalf("a backup without a passphrase: %d", rec.Code)
	}
	if rec := h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": "short"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a short passphrase: %d", rec.Code)
	}
	if rec := h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass}); rec.Code != http.StatusNoContent {
		t.Fatalf("passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("POST", "/api/v1/streams", map[string]any{"name": "live/kept"}); rec.Code != http.StatusCreated {
		t.Fatalf("stream: %d %s", rec.Code, rec.Body)
	}
	var made BackupInfo
	rec := h.do("POST", "/api/v1/backups", nil)
	h.json(rec, &made)
	if rec.Code != http.StatusCreated || made.Kind != "manual" || !backupName.MatchString(made.Name) || made.Size == 0 {
		t.Fatalf("backup: %d %s", rec.Code, rec.Body)
	}
	if v := h.backupsView(); !v.Configured || len(v.Backups) != 1 || v.Last == nil || !v.Last.OK || v.Backups[0].Host != "mtx.example.com" {
		t.Fatalf("after a backup: %+v", v)
	}

	// Download, and the download is audited.
	rec = h.do("GET", "/api/v1/backups/"+made.Name+"/download", nil)
	file := rec.Body.Bytes()
	if rec.Code != http.StatusOK || !bytes.HasPrefix(file, []byte(backup.Magic+"\n")) || !strings.Contains(rec.Header().Get("Content-Disposition"), made.Name) {
		t.Fatalf("download: %d", rec.Code)
	}
	if action, target, _ := lastAudit(t, h); action != "backup.download" || target != made.Name {
		t.Errorf("audit %s %s", action, target)
	}
	for _, bad := range []string{"../state/mtxui.db", "x.mtxbackup", "mtxui-20260101-000000-evil.mtxbackup"} {
		if rec := h.do("GET", "/api/v1/backups/"+bad+"/download", nil); rec.Code != http.StatusNotFound {
			t.Errorf("download %q: %d", bad, rec.Code)
		}
	}

	// Things change after the backup.
	if rec := h.do("POST", "/api/v1/streams", map[string]any{"name": "live/later"}); rec.Code != http.StatusCreated {
		t.Fatal(rec.Code)
	}

	// Upload: only backups, as octet-stream.
	if rec := h.do("POST", "/api/v1/backups/upload", rawBody("not a backup"), contentType("application/octet-stream")); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("junk upload: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/backups/upload", rawBody(file), contentType("text/plain")); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("upload as text: %d", rec.Code)
	}
	var up BackupInfo
	rec = h.do("POST", "/api/v1/backups/upload", rawBody(file), contentType("application/octet-stream"))
	h.json(rec, &up)
	if rec.Code != http.StatusCreated || up.Kind != "upload" || up.Size != int64(len(file)) {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}

	// The check: a wrong passphrase, then the preview. The clip missing here is added for the validation, and
	// removed again when the restore is cancelled.
	if err := os.Remove(filepath.Join(data, "holding", "1-abcdef.mp4")); err != nil {
		t.Fatal(err)
	}
	if rec := h.do("POST", "/api/v1/backups/"+up.Name+"/check", map[string]any{"passphrase": "wrong passphrase"}); rec.Code != http.StatusUnauthorized ||
		decode(rec)["error"] != "passphrase" {
		t.Fatalf("wrong passphrase: %d %s", rec.Code, rec.Body)
	}
	var pv RestorePreview
	rec = h.do("POST", "/api/v1/backups/"+up.Name+"/check", map[string]any{"passphrase": backupPass})
	h.json(rec, &pv)
	if rec.Code != http.StatusOK || !slices.Contains(pv.Database.Users, "admin") || !slices.Equal(pv.Database.Streams, []string{"live/kept"}) ||
		!slices.Equal(pv.CurrentStreams, []string{"live/kept", "live/later"}) || pv.Clips != 1 || pv.ConfigChanges != true {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(data, "holding", "1-abcdef.mp4")); err != nil {
		t.Fatal("the check did not add the backup's clip")
	}
	if v := h.backupsView(); v.Staged == nil || v.Staged.Name != up.Name {
		t.Fatalf("staged: %+v", v.Staged)
	}
	if rec := h.do("DELETE", "/api/v1/backups/restore", nil); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	if _, err := os.Stat(filepath.Join(data, "holding", "1-abcdef.mp4")); err == nil {
		t.Fatal("cancelling left the added clip")
	}
	if entries, _ := os.ReadDir(filepath.Join(data, "state")); len(entries) != 1 {
		t.Fatalf("cancelling left files in state/: %v", entries)
	}
	if rec := h.do("POST", "/api/v1/backups/"+up.Name+"/restore", nil); rec.Code != http.StatusConflict {
		t.Fatalf("a restore without a check: %d", rec.Code)
	}

	// Check again and restore: a backup of the current state first, then the restart.
	restarted := make(chan struct{})
	h.srv.d.Restart = func() { close(restarted) }
	if rec := h.do("POST", "/api/v1/backups/"+up.Name+"/check", map[string]any{"passphrase": backupPass}); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	rec = h.do("POST", "/api/v1/backups/"+up.Name+"/restore", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	select {
	case <-restarted:
	case <-time.After(3 * time.Second):
		t.Fatal("no restart")
	}
	if kinds := kindsOf(h.backupsView().Backups); !slices.Contains(kinds, "before-restore") {
		t.Fatalf("no backup before the restore: %v", kinds)
	}

	// The next start: the database and key are swapped in, then finished once it is open.
	cfg := h.srv.d.Settings
	_ = os.WriteFile(filepath.Join(data, "state", "mtxui.db"), []byte("the old database"), 0o600)
	_ = os.WriteFile(filepath.Join(data, "state", "mtxui.db-wal"), []byte("its WAL, which must not meet the new one"), 0o600)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sr, err := PrepareRestore(cfg, log)
	if err != nil || sr == nil {
		t.Fatalf("prepare: %v", err)
	}
	if old, _ := os.ReadFile(filepath.Join(data, "state", "pre-restore", "mtxui.db-wal")); string(old) != "its WAL, which must not meet the new one" {
		t.Fatal("the old WAL was not moved aside")
	}
	if _, err := os.Stat(filepath.Join(data, "state", "mtxui.db-wal")); err == nil {
		t.Fatal("a WAL next to the restored database")
	}
	restored, err := store.Open(ctx, filepath.Join(data, "state", "mtxui.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	streams, _ := restored.Streams(ctx)
	if len(streams) != 1 || streams[0].Name != "live/kept" {
		t.Fatalf("restored streams: %+v", streams)
	}
	before, _ := os.ReadFile(h.srv.d.Config.Path)
	FinishRestore(ctx, sr, cfg, restored, h.srv.d.Config, h.srv.d.Audit, log)
	after, _ := os.ReadFile(h.srv.d.Config.Path)
	if bytes.Equal(before, after) || strings.Contains(string(after), "live/later") || !strings.Contains(string(after), "live/kept") {
		t.Fatalf("the config after the restore:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(data, "holding", "1-abcdef.mp4")); err != nil {
		t.Fatal("the clip is not back")
	}
	if _, err := os.Stat(filepath.Join(data, "state", restoreReady)); err == nil {
		t.Fatal("the ready restore stayed")
	}
	// Nothing to do at the start after.
	if sr, err := PrepareRestore(cfg, log); sr != nil || err != nil {
		t.Fatalf("a second prepare: %v %v", sr, err)
	}
}

func kindsOf(list []BackupInfo) []string {
	var out []string
	for _, b := range list {
		out = append(out, b.Kind)
	}
	return out
}

func TestBackupSchedule(t *testing.T) {
	ctx := context.Background()
	h, _ := backupHarness(t)
	if rec := h.do("PUT", "/api/v1/backups/schedule", map[string]any{"enabled": true, "time": "25:00", "keep": 3}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad time: %d", rec.Code)
	}
	if rec := h.do("PUT", "/api/v1/backups/schedule", map[string]any{"enabled": true, "time": "03:30", "keep": 1}); rec.Code != http.StatusOK {
		t.Fatalf("schedule: %d", rec.Code)
	}
	day := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	h.srv.scheduledBackup(ctx, day.Add(time.Hour)) // no passphrase yet: nothing
	if n := len(h.backupsView().Backups); n != 0 {
		t.Fatalf("%d backups without a passphrase", n)
	}
	h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass})
	h.srv.scheduledBackup(ctx, day) // before the time
	h.srv.scheduledBackup(ctx, day.Add(time.Hour))
	h.srv.scheduledBackup(ctx, day.Add(2*time.Hour)) // done today
	if v := h.backupsView(); len(v.Backups) != 1 || v.Backups[0].Kind != "scheduled" || v.Last == nil || v.Last.Kind != "scheduled" {
		t.Fatalf("after a day: %+v", v)
	}
	if action, _, _ := lastAudit(t, h); action != "backup.create" {
		t.Errorf("audit %s", action)
	}
	time.Sleep(1100 * time.Millisecond) // the next one needs another name (second resolution)
	h.srv.scheduledBackup(ctx, day.Add(25*time.Hour))
	if v := h.backupsView(); len(v.Backups) != 1 { // keep 1
		t.Fatalf("after the next day: %+v", v.Backups)
	}
	name := h.backupsView().Backups[0].Name
	if rec := h.do("DELETE", "/api/v1/backups/"+name, nil); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	if rec := h.do("DELETE", "/api/v1/backups/"+name, nil); rec.Code != http.StatusNotFound {
		t.Fatal(rec.Code)
	}
}

// A backup someone tampered with, or one that is not a backup, is refused at the check and nothing of it stays.
func TestBackupRefused(t *testing.T) {
	h, data := backupHarness(t)
	h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass})
	var made BackupInfo
	h.json(h.do("POST", "/api/v1/backups", nil), &made)
	file := h.do("GET", "/api/v1/backups/"+made.Name+"/download", nil).Body.Bytes()
	file[len(file)-30] ^= 1
	var up BackupInfo
	h.json(h.do("POST", "/api/v1/backups/upload", rawBody(file), contentType("application/octet-stream")), &up)
	rec := h.do("POST", "/api/v1/backups/"+up.Name+"/check", map[string]any{"passphrase": backupPass})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(decode(rec)["message"].(string), "damaged") {
		t.Fatalf("tampered: %d %s", rec.Code, rec.Body)
	}
	if entries, _ := os.ReadDir(filepath.Join(data, "state")); len(entries) != 1 {
		t.Fatalf("left in state/: %v", entries)
	}
	if v := h.backupsView(); v.Staged != nil {
		t.Fatal("a refused backup is staged")
	}
}
