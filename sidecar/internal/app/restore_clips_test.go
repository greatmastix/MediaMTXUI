package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mtxui/internal/audit"
	"mtxui/internal/backup"
	"mtxui/internal/store"
)

// A holding clip too large for backups is left out of them, and listed. Restored where the clip is not (a new
// server), the check passes and names the stream, and the restore shows the offline screen there instead.
func TestRestoreWithoutLargeClips(t *testing.T) {
	ctx := context.Background()
	h, data := backupHarness(t)
	holding := filepath.Join(data, "holding")
	if err := InstallOfflineClips(holding); err != nil {
		t.Fatal(err)
	}
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/big"}), &st)
	stream := "/api/v1/streams/" + itoa(st.ID)
	small := clip(t, testH264, testAAC(48000, 2))
	if rec := h.do("PUT", stream+"/holding/clip?audio=aac&format=1080p60", rawBody(small), contentType("video/mp4")); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	cur, err := h.st.StreamByID(ctx, st.ID)
	if err != nil || cur.Holding != "file" || cur.ClipAAC == "" {
		t.Fatalf("the stream after the upload: %+v %v", cur, err)
	}
	big := filepath.Join(holding, cur.ClipAAC)
	bigClip(t, big, small, backup.MaxClip+1) // the same clip, made larger than a backup carries

	if rec := h.do("PUT", "/api/v1/backups/passphrase", map[string]any{"passphrase": backupPass}); rec.Code != http.StatusNoContent {
		t.Fatalf("passphrase: %d", rec.Code)
	}
	var made BackupInfo
	h.json(h.do("POST", "/api/v1/backups", nil), &made)
	if made.LeftOutClips != 1 {
		t.Errorf("clips left out of the backup: %d", made.LeftOutClips)
	}
	if v := h.backupsView(); len(v.Backups) != 1 || v.Backups[0].LeftOutClips != 1 {
		t.Errorf("the backup list: %+v", v.Backups)
	}
	if made.Size > backup.MaxClip {
		t.Errorf("the backup holds the large clip: %d bytes", made.Size)
	}

	// On a server without the clip.
	if err := os.Remove(big); err != nil {
		t.Fatal(err)
	}
	var preview RestorePreview
	rec := h.do("POST", "/api/v1/backups/"+made.Name+"/check", map[string]any{"passphrase": backupPass})
	if rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	h.json(rec, &preview)
	if !slices.Equal(preview.MissingClips, []string{"live/big"}) {
		t.Errorf("missing clips in the preview: %v", preview.MissingClips)
	}
	h.srv.d.Restart = func() {}
	if rec := h.do("POST", "/api/v1/backups/"+made.Name+"/restore", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	state, cfg := filepath.Join(data, "state"), h.srv.d.Settings
	if err := h.st.Snapshot(ctx, filepath.Join(state, "mtxui.db")); err != nil {
		t.Fatal(err)
	}
	sr, err := PrepareRestore(cfg, quiet)
	if err != nil || sr == nil {
		t.Fatalf("prepare: %v", err)
	}
	restored, err := store.Open(ctx, filepath.Join(state, "mtxui.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	FinishRestore(ctx, sr, cfg, restored, h.srv.d.Config, audit.NewRecorder(restored, quiet), quiet)

	got, err := restored.StreamByID(ctx, st.ID)
	if err != nil || got.Holding != "builtin" || got.ClipAAC != "" || got.Format != "1080p60" {
		t.Errorf("the restored stream: %+v %v", got, err)
	}
	file := h.mtxFile()
	if i := strings.Index(file, "  live/big:"); i < 0 || !strings.Contains(file[i:], "alwaysAvailableFile: "+filepath.Join(holding, "offline-1080p60-aac.mp4")) {
		t.Errorf("the restored config does not show the offline screen:\n%s", file)
	}
	events, _ := restored.ListAudit(ctx, 100)
	for _, e := range events {
		if e.Action == "backup.restored" {
			if lost, _ := e.Details["holdingClipsMissing"].([]any); len(lost) != 1 || lost[0] != "live/big" || e.Details["config"] != nil {
				t.Errorf("the restore's entry: %+v", e.Details)
			}
			return
		}
	}
	t.Error("no backup.restored entry")
}
