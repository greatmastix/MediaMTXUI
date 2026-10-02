package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/backup"
	"mtxui/internal/buildinfo"
	"mtxui/internal/store"
)

// Backups: the database, the credential key, mediamtx.yml and the holding clips, encrypted
// so that only the backup passphrase opens them (internal/backup). Made daily at a set time and on demand, kept in
// backups/ and downloadable; restored from a kept or uploaded file after a preview (the dry run: decrypted, every
// entry and the database checked, the config validated) by restarting the sidecar, which swaps the files in before
// it opens the database.

const (
	metaBackupRecipient = "backup_recipient"
	metaBackupKey       = "backup_key"
	metaBackupSchedule  = "backup_schedule"
	metaBackupLast      = "backup_last"

	stagingTTL        = 15 * time.Minute // a preview stays ready to restore this long
	uploadTTL         = 24 * time.Hour   // an uploaded backup is deleted after this
	keepBeforeRestore = 3                // backups made before restores, kept
	restoreReady      = "restore-ready"  // under state/: the staged restore the next start applies
	readyFile         = "ready.json"
)

var backupName = regexp.MustCompile(`^(?:mtxui-\d{8}-\d{6}-(scheduled|manual|before-restore)|upload-[0-9a-f]{16})\.mtxbackup$`)

// BackupSchedule is when backups are made by themselves: daily at Time (UTC), keeping the newest Keep.
type BackupSchedule struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"` // HH:MM, UTC
	Keep    int    `json:"keep"`
}

var defaultSchedule = BackupSchedule{Enabled: true, Time: "03:30", Keep: 7}

var scheduleTime = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// BackupLast is the newest backup attempt's outcome.
type BackupLast struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name,omitempty"`
	Message string    `json:"message,omitempty"`
}

// BackupInfo is one kept backup.
type BackupInfo struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // scheduled, manual, before-restore, upload
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
	Host    string    `json:"host"`
	Version string    `json:"version"`
}

// BackupsView is the backups page.
type BackupsView struct {
	Configured     bool            `json:"configured"` // a passphrase is set
	Schedule       BackupSchedule  `json:"schedule"`
	Last           *BackupLast     `json:"last"`
	Backups        []BackupInfo    `json:"backups"`
	MaxUploadBytes int64           `json:"maxUploadBytes"`
	Staged         *RestorePreview `json:"staged"` // a checked backup, ready to restore
}

// RestorePreview is what a restore would do: the backup's contents beside what is here now.
type RestorePreview struct {
	Name           string           `json:"name"`
	Created        time.Time        `json:"created"`
	Version        string           `json:"version"`
	MediaMTX       string           `json:"mediamtx"`
	Database       store.Inspection `json:"database"`
	CurrentUsers   []string         `json:"currentUsers"`
	CurrentStreams []string         `json:"currentStreams"`
	Clips          int              `json:"clips"`
	ConfigChanges  bool             `json:"configChanges"`
	ExpiresAt      time.Time        `json:"expiresAt"`
}

// backupState serialises backups, checks and restores, and holds the checked backup.
type backupState struct {
	mu     sync.Mutex
	staged *stagedRestore
}

type stagedRestore struct {
	preview RestorePreview
	dir     string        // under state/
	clips   []string      // the backup's clip names, as dir/clip-N
	added   []string      // clips the check put into the holding directory (new names only), removed if abandoned
	ex      backupExtract // what the start applies
}

// backupExtract is ready.json: what a staged restore holds.
type backupExtract struct {
	Name  string   `json:"name"`
	Clips []string `json:"clips"`
}

var errNoBackupKey = errors.New("set a backup passphrase first")

func randomHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) backupsRoot() (*os.Root, error) {
	return os.OpenRoot(s.d.Settings.BackupsDir())
}

// listBackups reads the backups directory: the backups by name pattern, newest first, with their headers.
func (s *Server) listBackups() ([]BackupInfo, error) {
	root, err := s.backupsRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	out := []BackupInfo{}
	for _, e := range entries {
		m := backupName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		info := BackupInfo{Name: e.Name(), Kind: m[1]}
		if info.Kind == "" {
			info.Kind = "upload"
		}
		if fi, err := e.Info(); err == nil {
			info.Size, info.Created = fi.Size(), fi.ModTime()
		}
		if f, err := root.Open(e.Name()); err == nil {
			if rd, err := backup.Open(f); err == nil {
				if !rd.Header.Created.IsZero() {
					info.Created = rd.Header.Created
				}
				info.Host, info.Version = rd.Header.Host, rd.Header.Version
			}
			_ = f.Close()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

func (s *Server) schedule(ctx context.Context) BackupSchedule {
	sc := defaultSchedule
	if v, ok, _ := s.d.Store.Meta(ctx, metaBackupSchedule); ok {
		_ = json.Unmarshal([]byte(v), &sc)
	}
	return sc
}

func (s *Server) lastBackup(ctx context.Context) *BackupLast {
	v, ok, _ := s.d.Store.Meta(ctx, metaBackupLast)
	if !ok {
		return nil
	}
	var l BackupLast
	if json.Unmarshal([]byte(v), &l) != nil {
		return nil
	}
	return &l
}

func (s *Server) setLastBackup(ctx context.Context, l BackupLast) {
	b, _ := json.Marshal(l)
	if err := s.d.Store.SetMeta(ctx, metaBackupLast, string(b)); err != nil {
		s.d.Log.Warn("recording the last backup", "err", err)
	}
}

// createBackup makes a backup of the given kind and prunes the old ones. Callers hold s.bk.mu.
func (s *Server) createBackup(ctx context.Context, kind string) (BackupInfo, error) {
	recipient, ok, err := s.d.Store.Meta(ctx, metaBackupRecipient)
	if err != nil {
		return BackupInfo{}, err
	}
	wrapped, ok2, err := s.d.Store.Meta(ctx, metaBackupKey)
	if err != nil {
		return BackupInfo{}, err
	}
	if !ok || !ok2 {
		return BackupInfo{}, errNoBackupKey
	}
	cfg := s.d.Settings
	dbCopy := filepath.Join(cfg.StateDir(), ".backup-"+randomHex()+".db")
	defer os.Remove(dbCopy)
	if err := s.d.Store.Snapshot(ctx, dbCopy); err != nil {
		return BackupInfo{}, fmt.Errorf("copying the database: %w", err)
	}
	dbInfo, err := os.Stat(dbCopy)
	if err != nil {
		return BackupInfo{}, err
	}
	schema, err := s.d.Store.SchemaVersion(ctx)
	if err != nil {
		return BackupInfo{}, err
	}
	key, err := os.ReadFile(filepath.Join(cfg.StateDir(), "credential-key"))
	if err != nil || len(key) != backup.KeyLen {
		return BackupInfo{}, fmt.Errorf("reading the credential key: %w", err)
	}
	conf, _, err := s.d.Config.Current()
	if err != nil {
		return BackupInfo{}, fmt.Errorf("reading mediamtx.yml: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	c := backup.Contents{
		Manifest: backup.Manifest{
			Format: backup.FormatVersion, Created: now, Version: buildinfo.Version, MediaMTX: buildinfo.MediaMTXVersion, Schema: schema,
			Holding: []string{},
		},
		DB: backup.File{Path: dbCopy, Size: dbInfo.Size()}, Key: key, Config: conf,
	}
	if entries, err := os.ReadDir(cfg.HoldingDir); err == nil {
		for _, e := range entries {
			fi, err := e.Info()
			if err != nil || !e.Type().IsRegular() || !backup.ClipName.MatchString(e.Name()) || fi.Size() > backup.MaxClip {
				continue
			}
			c.Manifest.Holding = append(c.Manifest.Holding, e.Name())
			c.Clips = append(c.Clips, backup.File{Path: filepath.Join(cfg.HoldingDir, e.Name()), Size: fi.Size()})
		}
	}
	name := fmt.Sprintf("mtxui-%s-%s.mtxbackup", now.Format("20060102-150405"), kind)
	root, err := s.backupsRoot()
	if err != nil {
		return BackupInfo{}, fmt.Errorf("the backups directory: %w", err)
	}
	defer root.Close()
	if _, err := root.Stat(name); err == nil {
		return BackupInfo{}, errors.New("a backup was made this second already")
	}
	tmp := ".tmp-" + randomHex()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return BackupInfo{}, err
	}
	defer root.Remove(tmp) //nolint:errcheck // gone after the rename
	h := backup.Header{Created: now, Kind: kind, Version: buildinfo.Version, MediaMTX: buildinfo.MediaMTXVersion, Host: cfg.PublicURL.Host}
	err = backup.Write(f, h, recipient, []byte(wrapped), c)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return BackupInfo{}, err
	}
	if err := root.Rename(tmp, name); err != nil {
		return BackupInfo{}, err
	}
	fi, _ := root.Stat(name)
	s.pruneBackups()
	return BackupInfo{Name: name, Kind: kind, Size: fi.Size(), Created: now, Host: h.Host, Version: h.Version}, nil
}

// pruneBackups deletes scheduled backups beyond the schedule's keep, before-restore ones beyond three, uploads
// older than a day, and leftovers of interrupted writes. Manual backups stay until someone deletes them.
func (s *Server) pruneBackups() {
	list, err := s.listBackups()
	if err != nil {
		return
	}
	root, err := s.backupsRoot()
	if err != nil {
		return
	}
	defer root.Close()
	keep := map[string]int{"scheduled": s.schedule(context.Background()).Keep, "before-restore": keepBeforeRestore}
	seen := map[string]int{}
	for _, b := range list { // newest first
		seen[b.Kind]++
		if limit, ok := keep[b.Kind]; (ok && seen[b.Kind] > limit) || (b.Kind == "upload" && time.Since(b.Created) > uploadTTL) {
			_ = root.Remove(b.Name)
		}
	}
	if entries, err := fs.ReadDir(root.FS(), "."); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && strings.HasPrefix(e.Name(), ".tmp-") && time.Since(fi.ModTime()) > time.Hour {
				_ = root.Remove(e.Name())
			}
		}
	}
}

// RunBackups makes the scheduled backup once a day at the schedule's time (UTC), until ctx ends.
func (s *Server) RunBackups(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.scheduledBackup(ctx, time.Now().UTC())
	}
}

// scheduledBackup makes today's scheduled backup if it is due: enabled, the time has come, and none was made today.
func (s *Server) scheduledBackup(ctx context.Context, now time.Time) {
	sc := s.schedule(ctx)
	if !sc.Enabled || now.Format("15:04") < sc.Time {
		return
	}
	if _, ok, _ := s.d.Store.Meta(ctx, metaBackupRecipient); !ok {
		return
	}
	if l := s.lastBackup(ctx); l != nil && l.Kind == "scheduled" && l.At.UTC().Format("2006-01-02") == now.Format("2006-01-02") {
		return
	}
	s.bk.mu.Lock()
	info, err := s.createBackup(ctx, "scheduled")
	s.bk.mu.Unlock()
	last := BackupLast{At: now, OK: err == nil, Kind: "scheduled", Name: info.Name}
	details := map[string]any{"size": info.Size}
	if err != nil {
		last.Message = err.Error()
		details = map[string]any{"error": err.Error()}
		s.d.Log.Error("the scheduled backup failed", "err", err)
	}
	s.setLastBackup(ctx, last)
	s.d.Audit.Record(ctx, store.AuditEvent{Actor: "system", Action: "backup.create", Target: info.Name, Details: details})
}

func (s *Server) backupsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.listBackups()
	if err != nil {
		s.d.Log.Warn("listing backups", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "The backups directory cannot be read. Is backups/ mounted and writable?")
		return
	}
	_, configured, _ := s.d.Store.Meta(ctx, metaBackupRecipient)
	v := BackupsView{
		Configured: configured, Schedule: s.schedule(ctx), Last: s.lastBackup(ctx), Backups: list,
		MaxUploadBytes: s.d.Settings.BackupMaxUploadBytes,
	}
	s.bk.mu.Lock()
	if st := s.bk.staged; st != nil && time.Now().Before(st.preview.ExpiresAt) {
		p := st.preview
		v.Staged = &p
	}
	s.bk.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) backupCreate(w http.ResponseWriter, r *http.Request) {
	s.bk.mu.Lock()
	info, err := s.createBackup(r.Context(), "manual")
	s.bk.mu.Unlock()
	s.setLastBackup(r.Context(), BackupLast{At: time.Now(), OK: err == nil, Kind: "manual", Name: info.Name, Message: errText(err)})
	if errors.Is(err, errNoBackupKey) {
		writeError(w, http.StatusConflict, "no_passphrase", "Set a backup passphrase first.")
		return
	}
	if err != nil {
		s.d.Log.Error("making a backup", "err", err)
		audit.Set(r.Context(), "backup.create", "", map[string]any{"error": err.Error()})
		writeError(w, http.StatusInternalServerError, "internal", "The backup failed: "+err.Error())
		return
	}
	audit.Set(r.Context(), "backup.create", info.Name, map[string]any{"size": info.Size})
	writeJSON(w, http.StatusCreated, info)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// backupPassphrase sets the passphrase: a new backup key, wrapped with it. Backups made before keep the passphrase
// they were made with (each carries its own wrapped key).
func (s *Server) backupPassphrase(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := auth.ValidatePassword(body.Passphrase, ""); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", strings.Replace(err.Error(), "password", "The passphrase", 1)+".")
		return
	}
	recipient, wrapped, err := backup.NewKey(body.Passphrase)
	if err == nil {
		err = s.d.Store.SetMeta(r.Context(), metaBackupKey, string(wrapped))
	}
	if err == nil {
		err = s.d.Store.SetMeta(r.Context(), metaBackupRecipient, recipient)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The passphrase could not be set.")
		return
	}
	audit.Set(r.Context(), "backup.passphrase", "", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) backupSchedule(w http.ResponseWriter, r *http.Request) {
	var sc BackupSchedule
	if !decodeJSON(w, r, &sc) {
		return
	}
	if !scheduleTime.MatchString(sc.Time) || sc.Keep < 1 || sc.Keep > 365 {
		writeError(w, http.StatusBadRequest, "invalid", "The time is HH:MM (UTC) and between 1 and 365 backups are kept.")
		return
	}
	b, _ := json.Marshal(sc)
	if err := s.d.Store.SetMeta(r.Context(), metaBackupSchedule, string(b)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The schedule could not be saved.")
		return
	}
	audit.Set(r.Context(), "backup.schedule", "", map[string]any{"enabled": sc.Enabled, "time": sc.Time, "keep": sc.Keep})
	s.pruneBackups()
	writeJSON(w, http.StatusOK, sc)
}

// backupParam reads {name}: a backup's name as the listing shows it, never a path.
func backupParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := chi.URLParam(r, "name")
	if !backupName.MatchString(name) {
		writeError(w, http.StatusNotFound, "not_found", "No such backup.")
		return "", false
	}
	return name, true
}

func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request) {
	name, ok := backupParam(w, r)
	if !ok {
		return
	}
	root, err := s.backupsRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The backups directory cannot be read.")
		return
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such backup.")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The backup cannot be read.")
		return
	}
	who, id := actor(r)
	s.d.Audit.Record(r.Context(), store.AuditEvent{
		Actor: who, ActorUserID: id, IP: clientip.From(r.Context()).IP.String(), Action: "backup.download", Target: name,
		Details: map[string]any{"size": fi.Size()},
	})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) backupDelete(w http.ResponseWriter, r *http.Request) {
	name, ok := backupParam(w, r)
	if !ok {
		return
	}
	root, err := s.backupsRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The backups directory cannot be read.")
		return
	}
	defer root.Close()
	if err := root.Remove(name); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such backup.")
		return
	}
	audit.Set(r.Context(), "backup.delete", name, nil)
	w.WriteHeader(http.StatusNoContent)
}

// backupUpload keeps an uploaded backup (for a day) so it can be checked and restored like a kept one. Only its
// first lines are read now: whether it is a backup at all.
func (s *Server) backupUpload(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid", "Send the backup as application/octet-stream.")
		return
	}
	root, err := s.backupsRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The backups directory cannot be read.")
		return
	}
	defer root.Close()
	name := "upload-" + randomHex() + ".mtxbackup"
	tmp := ".tmp-" + randomHex()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The upload could not be stored.")
		return
	}
	defer root.Remove(tmp) //nolint:errcheck // gone after the rename
	limit := s.d.Settings.BackupMaxUploadBytes
	n, err := io.Copy(f, io.LimitReader(r.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid", "The upload broke off.")
		return
	case n > limit:
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("Backups up to %d MB are accepted (MTXUI_BACKUP_MAX_UPLOAD_MB).", limit>>20))
		return
	}
	in, err := root.Open(tmp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The upload could not be read.")
		return
	}
	rd, err := backup.Open(in)
	_ = in.Close()
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "not_backup", "This is not a MediaMTX UI backup.")
		return
	}
	if err := root.Rename(tmp, name); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The upload could not be stored.")
		return
	}
	audit.Set(r.Context(), "backup.upload", name, map[string]any{"size": n, "created": rd.Header.Created, "host": rd.Header.Host})
	writeJSON(w, http.StatusCreated, BackupInfo{Name: name, Kind: "upload", Size: n, Created: rd.Header.Created, Host: rd.Header.Host, Version: rd.Header.Version})
}

// discardStaged removes a checked backup's files and the clips its check added. Callers hold s.bk.mu.
func (s *Server) discardStaged() {
	st := s.bk.staged
	if st == nil {
		return
	}
	s.bk.staged = nil
	_ = os.RemoveAll(st.dir)
	if root, err := os.OpenRoot(s.d.Settings.HoldingDir); err == nil {
		for _, c := range st.added {
			_ = root.Remove(c)
		}
		_ = root.Close()
	}
}

// backupCheck is the dry run: the backup is decrypted into a directory under state/, every entry checked, the
// database inspected and mediamtx.yml validated (with the backup's clips, which are added to the holding directory
// under their own names when missing: names are content hashes, nothing is overwritten, and they are removed again
// if the restore does not happen). Nothing else changes. The result stays ready to restore for 15 minutes.
func (s *Server) backupCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name, ok := backupParam(w, r)
	if !ok {
		return
	}
	if ok, wait := s.setupRate.Allow("backup " + clientip.RateKey(clientip.From(ctx).IP)); !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("Too many attempts. Try again in %d seconds.", retryAfter(w, wait)))
		return
	}
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	s.bk.mu.Lock()
	defer s.bk.mu.Unlock()
	s.discardStaged()
	st, status, err := s.stage(ctx, name, body.Passphrase)
	if err != nil {
		audit.Set(ctx, "backup.check", name, map[string]any{"error": err.Error()})
		kind := "invalid"
		if errors.Is(err, backup.ErrPassphrase) {
			kind = "passphrase"
		}
		writeError(w, status, kind, sentence(err))
		return
	}
	s.bk.staged = st
	audit.Set(ctx, "backup.check", name, map[string]any{"users": len(st.preview.Database.Users), "schema": st.preview.Database.Schema})
	writeJSON(w, http.StatusOK, st.preview)
}

func sentence(err error) string {
	m := err.Error()
	if m == "" {
		return m
	}
	m = strings.ToUpper(m[:1]) + m[1:]
	if !strings.HasSuffix(m, ".") {
		m += "."
	}
	return m
}

// stage decrypts and checks a backup; on failure nothing it wrote is left.
func (s *Server) stage(ctx context.Context, name, passphrase string) (*stagedRestore, int, error) {
	root, err := s.backupsRoot()
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		return nil, http.StatusNotFound, errors.New("no such backup")
	}
	defer f.Close()
	rd, err := backup.Open(f)
	if err != nil {
		return nil, http.StatusUnprocessableEntity, err
	}
	cfg := s.d.Settings
	dir := filepath.Join(cfg.StateDir(), "restore-staging-"+randomHex())
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	st := &stagedRestore{dir: dir}
	ok := false
	defer func() {
		if !ok {
			s.bk.staged = st
			s.discardStaged()
		}
	}()
	ex, err := rd.Extract(passphrase, dir, backup.Limits{DB: cfg.BackupMaxUploadBytes, Total: cfg.BackupMaxUploadBytes * 2})
	if errors.Is(err, backup.ErrPassphrase) {
		return nil, http.StatusUnauthorized, err
	}
	if err != nil {
		return nil, http.StatusUnprocessableEntity, err
	}
	in, err := s.d.Store.Inspect(ctx, filepath.Join(dir, backup.DBFile))
	if err != nil {
		return nil, http.StatusUnprocessableEntity, err
	}
	conf, err := os.ReadFile(filepath.Join(dir, backup.ConfigFile))
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if err := s.d.Config.Rules.Check(conf); err != nil {
		return nil, http.StatusUnprocessableEntity, fmt.Errorf("the backup's mediamtx.yml is refused: %w", err)
	}
	st.clips = ex.Clips
	if st.added, err = s.placeClips(dir, ex.Clips); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if err := s.d.Config.Validate(ctx, conf); err != nil {
		return nil, http.StatusUnprocessableEntity, fmt.Errorf("the backup's mediamtx.yml is refused: %w", err)
	}
	current, _, _ := s.d.Config.Current()
	users, _ := s.d.Store.Users(ctx)
	streams, _ := s.d.Store.Streams(ctx)
	st.preview = RestorePreview{
		Name: name, Created: ex.Manifest.Created, Version: ex.Manifest.Version, MediaMTX: ex.Manifest.MediaMTX, Database: in,
		CurrentUsers: []string{}, CurrentStreams: []string{}, Clips: len(ex.Clips), ConfigChanges: string(current) != string(conf),
		ExpiresAt: time.Now().Add(stagingTTL),
	}
	for _, u := range users {
		st.preview.CurrentUsers = append(st.preview.CurrentUsers, u.Username)
	}
	for _, x := range streams {
		st.preview.CurrentStreams = append(st.preview.CurrentStreams, x.Name)
	}
	slices.Sort(st.preview.CurrentUsers)
	slices.Sort(st.preview.CurrentStreams)
	st.ex = backupExtract{Name: name, Clips: ex.Clips}
	ok = true
	return st, http.StatusOK, nil
}

// placeClips copies a staged backup's clips into the holding directory where no clip of that name exists yet (the
// names are content hashes or the built-in clips, so an existing one is the same clip). It returns the names added.
func (s *Server) placeClips(dir string, clips []string) ([]string, error) {
	root, err := os.OpenRoot(s.d.Settings.HoldingDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var added []string
	for i, name := range clips {
		if !backup.ClipName.MatchString(name) { // checked by Extract already; never a path
			return added, fmt.Errorf("clip name %q", name)
		}
		if _, err := root.Stat(name); err == nil {
			continue
		}
		in, err := os.Open(filepath.Join(dir, backup.ClipFile(i)))
		if err != nil {
			return added, err
		}
		out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = in.Close()
			return added, err
		}
		added = append(added, name)
		_, err = io.Copy(out, in)
		_ = in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return added, err
		}
	}
	return added, nil
}

func (s *Server) backupCancel(w http.ResponseWriter, r *http.Request) {
	s.bk.mu.Lock()
	had := s.bk.staged != nil
	s.discardStaged()
	s.bk.mu.Unlock()
	if had {
		audit.Set(r.Context(), "backup.cancel", "", nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

// backupRestore restores the checked backup: a backup of what is here now first (when a passphrase is set), then
// the staged files are marked ready and the sidecar restarts; the next start swaps them in (PrepareRestore,
// FinishRestore). Everyone signs in again afterwards.
func (s *Server) backupRestore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name, ok := backupParam(w, r)
	if !ok {
		return
	}
	s.bk.mu.Lock()
	defer s.bk.mu.Unlock()
	st := s.bk.staged
	if st == nil || st.preview.Name != name || time.Now().After(st.preview.ExpiresAt) {
		writeError(w, http.StatusConflict, "not_checked", "Check the backup first (or again: a check is ready for 15 minutes).")
		return
	}
	var before BackupInfo
	if _, configured, _ := s.d.Store.Meta(ctx, metaBackupRecipient); configured {
		var err error
		if before, err = s.createBackup(ctx, "before-restore"); err != nil {
			s.d.Log.Error("the backup before the restore failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "The backup of the current state failed, so nothing was restored: "+err.Error())
			return
		}
	}
	b, _ := json.Marshal(st.ex)
	if err := os.WriteFile(filepath.Join(st.dir, readyFile), b, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The restore could not be prepared.")
		return
	}
	ready := filepath.Join(s.d.Settings.StateDir(), restoreReady)
	_ = os.RemoveAll(ready)
	if err := os.Rename(st.dir, ready); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The restore could not be prepared.")
		return
	}
	s.bk.staged = nil // its files are the ready restore now; its added clips stay
	audit.Set(ctx, "backup.restore", name, map[string]any{"before": before.Name, "created": st.preview.Created})
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "before": before.Name})
	if s.d.Restart != nil {
		_ = http.NewResponseController(w).Flush()
		time.AfterFunc(500*time.Millisecond, s.d.Restart)
	}
}
