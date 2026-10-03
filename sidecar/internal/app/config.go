package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/audit"
	"mtxui/internal/mtxconf"
	"mtxui/internal/pathname"
	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

// The config API (admin only): mediamtx.yml as a whole (read, replace, validate), its history (list, show, restore),
// and structured edits of paths, global settings and path defaults. Every write goes through the writer (validation,
// atomic write, snapshot); structured edits are surgical, so comments and layout survive.

// maxConfig bounds a raw config upload. MediaMTX's own reference file is about 30 KiB.
const maxConfig = 1 << 20

// ConfigInfo is mediamtx.yml as it is now.
type ConfigInfo struct {
	Content  string        `json:"content"`
	SHA256   string        `json:"sha256"`
	Snapshot *SnapshotInfo `json:"snapshot"` // the latest snapshot; its sha256 differs from the file's after an external edit
	// Settings is the file as data, the way MediaMTX reads it (yes and no are booleans); null when it does not parse.
	Settings map[string]any `json:"settings"`
	// Locked are the global settings the sidecar depends on, with the reason: shown read-only, refused on change.
	Locked map[string]string `json:"locked"`
	// PublicHost is where stream clients connect (MTXUI_PUBLIC_HOST), for the addresses the UI shows.
	PublicHost string `json:"publicHost"`
}

// SnapshotInfo describes one entry of the config history.
type SnapshotInfo struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	SHA256   string    `json:"sha256"`
	Author   string    `json:"author"`
	Reason   string    `json:"reason"`
	ParentID *int64    `json:"parentId"`
	Content  *string   `json:"content,omitempty"`
}

func snapshotInfo(s store.Snapshot, withContent bool) SnapshotInfo {
	info := SnapshotInfo{ID: s.ID, At: s.At, SHA256: s.SHA256, Author: s.Author, Reason: s.Reason, ParentID: s.ParentID}
	if withContent {
		c := string(s.Content)
		info.Content = &c
	}
	return info
}

// WriteResult is the answer to every config write.
type WriteResult struct {
	Changed  bool             `json:"changed"`
	SHA256   string           `json:"sha256"`
	Snapshot *SnapshotInfo    `json:"snapshot,omitempty"`
	Applied  *mtxconf.Applied `json:"applied,omitempty"` // what MediaMTX made of it
}

func (s *Server) configGet(w http.ResponseWriter, r *http.Request) {
	content, sum, err := s.d.Config.Current()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read mediamtx.yml.")
		return
	}
	info := ConfigInfo{Content: string(content), SHA256: sum, Locked: mtxconf.Locked, PublicHost: s.d.Settings.PublicHost}
	if settings, err := yamledit.Decode(content); err == nil {
		info.Settings = settings
	}
	if latest, err := s.d.Store.LatestSnapshot(r.Context()); err == nil {
		si := snapshotInfo(latest, false)
		info.Snapshot = &si
	}
	writeJSON(w, http.StatusOK, info)
}

type rawConfig struct {
	Content string `json:"content"`
	SHA256  string `json:"sha256"` // the version the edit started from; required for a replace
	Reason  string `json:"reason"`
}

func (s *Server) configValidate(w http.ResponseWriter, r *http.Request) {
	var req rawConfig
	if !decodeJSONLimit(w, r, &req, maxConfig) {
		return
	}
	if err := s.d.Config.Validate(r.Context(), []byte(req.Content)); err != nil {
		s.writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
}

func (s *Server) configReplace(w http.ResponseWriter, r *http.Request) {
	var req rawConfig
	if !decodeJSONLimit(w, r, &req, maxConfig) {
		return
	}
	if req.SHA256 == "" {
		writeError(w, http.StatusBadRequest, "invalid", "Send the sha256 of the version you edited.")
		return
	}
	reason := reasonOr(req.Reason, "edited as YAML")
	snap, err := s.d.Config.Replace(r.Context(), []byte(req.Content), req.SHA256, s.author(r), reason, s.guardEdit)
	s.finishWrite(w, r, "config.replace", "mediamtx.yml", map[string]any{"reason": reason}, snap, err)
}

// driftDismiss clears the notice about an outside edit of mediamtx.yml.
func (s *Server) driftDismiss(w http.ResponseWriter, r *http.Request) {
	s.d.Probe.Clear("config_drift")
	audit.Set(r.Context(), "config.drift.dismiss", "mediamtx.yml", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) snapshotsList(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	list, err := s.d.Store.ListSnapshots(r.Context(), before, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the history.")
		return
	}
	out := make([]SnapshotInfo, 0, len(list))
	for _, snap := range list {
		out = append(out, snapshotInfo(snap, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) snapshot(r *http.Request) (store.Snapshot, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return store.Snapshot{}, store.ErrNotFound
	}
	return s.d.Store.SnapshotByID(r.Context(), id)
}

func (s *Server) snapshotGet(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such snapshot.")
		return
	}
	writeJSON(w, http.StatusOK, snapshotInfo(snap, true))
}

// snapshotRestore writes an old snapshot as the current file (a new snapshot: history is never rewritten). The
// restored file passes today's validation like any other write.
func (s *Server) snapshotRestore(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such snapshot.")
		return
	}
	reason := fmt.Sprintf("restored snapshot %d", snap.ID)
	written, err := s.d.Config.Replace(r.Context(), snap.Content, "", s.author(r), reason, s.guardEdit)
	s.finishWrite(w, r, "config.restore", "mediamtx.yml", map[string]any{"snapshot": snap.ID}, written, err)
}

type pathConfig struct {
	Config map[string]any `json:"config"`
	Reason string         `json:"reason"`
}

// pathParam reads a path name from the route: a MediaMTX path name, a regular expression (~...) or all_others.
func pathParam(r *http.Request) (string, error) {
	name := param(r, "*")
	switch {
	case name == "all_others":
		return name, nil
	case strings.HasPrefix(name, "~"):
		if len(name) > 256 {
			return "", errors.New("the expression is too long")
		}
		if _, err := regexp.Compile(name[1:]); err != nil {
			return "", fmt.Errorf("invalid regular expression: %w", err)
		}
		return name, nil
	default:
		return name, pathname.Valid(name)
	}
}

func (s *Server) pathPut(w http.ResponseWriter, r *http.Request) {
	name, err := pathParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid path name: "+err.Error())
		return
	}
	var req pathConfig
	if !decodeJSONLimit(w, r, &req, 256<<10) {
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	if err := settingNames(req.Config); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	reason := reasonOr(req.Reason, "path "+name+" saved")
	snap, err := s.d.Config.Edit(r.Context(), s.author(r), reason, s.guardEdit, func(d *yamledit.Doc) error {
		return d.Set([]string{"paths", name}, req.Config)
	})
	s.finishWrite(w, r, "config.path.save", name, map[string]any{"settings": keysOf(req.Config)}, snap, err)
}

func (s *Server) pathDelete(w http.ResponseWriter, r *http.Request) {
	name, err := pathParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid path name: "+err.Error())
		return
	}
	snap, err := s.d.Config.Edit(r.Context(), s.author(r), "path "+name+" deleted", s.guardEdit, func(d *yamledit.Doc) error {
		return d.Delete([]string{"paths", name})
	})
	s.finishWrite(w, r, "config.path.delete", name, nil, snap, err)
}

// settingsPatch changes some settings of one section: set replaces values, remove deletes keys (MediaMTX's defaults
// apply again).
type settingsPatch struct {
	Set    map[string]any `json:"set"`
	Remove []string       `json:"remove"`
	Reason string         `json:"reason"`
}

func (s *Server) globalPatch(w http.ResponseWriter, r *http.Request) {
	s.patchSection(w, r, nil, "config.global", "global settings")
}

func (s *Server) pathDefaultsPatch(w http.ResponseWriter, r *http.Request) {
	s.patchSection(w, r, []string{"pathDefaults"}, "config.pathDefaults", "path defaults")
}

func (s *Server) patchSection(w http.ResponseWriter, r *http.Request, section []string, action, what string) {
	var req settingsPatch
	if !decodeJSONLimit(w, r, &req, 256<<10) {
		return
	}
	keys := append(keysOf(req.Set), req.Remove...)
	if len(keys) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "Nothing to change.")
		return
	}
	for _, k := range keys {
		if !settingName.MatchString(k) {
			writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("%q is not a setting name.", k))
			return
		}
		if section == nil && (k == "paths" || k == "pathDefaults") {
			writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("Edit %s through its own endpoint.", k))
			return
		}
		if why, locked := mtxconf.Locked[k]; section == nil && locked {
			writeError(w, http.StatusBadRequest, "locked", fmt.Sprintf("%s cannot be changed: %s", k, why))
			return
		}
	}
	reason := reasonOr(req.Reason, what+" changed")
	snap, err := s.d.Config.Edit(r.Context(), s.author(r), reason, s.guardEdit, func(d *yamledit.Doc) error {
		for _, k := range keysOf(req.Set) {
			if err := d.Set(append(append([]string(nil), section...), k), req.Set[k]); err != nil {
				return err
			}
		}
		for _, k := range req.Remove {
			if err := d.Delete(append(append([]string(nil), section...), k)); err != nil && !errors.Is(err, yamledit.ErrNotFound) {
				return err
			}
		}
		return nil
	})
	s.finishWrite(w, r, action, what, map[string]any{"set": keysOf(req.Set), "remove": req.Remove}, snap, err)
}

var settingName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)

// settingNames checks the keys of a path config: MediaMTX's setting names, nothing that could address anything else.
func settingNames(m map[string]any) error {
	for k := range m {
		if !settingName.MatchString(k) {
			return fmt.Errorf("%q is not a setting name", k)
		}
	}
	return nil
}

// guardEdit vets what an edit through the UI changes, before validation: no hook may be added, changed or removed
// (mtxconf.HookChanges), and every new or changed source and forward destination must pass the SSRF guard.
func (s *Server) guardEdit(before, after []byte) error {
	changed, err := mtxconf.HookChanges(before, after)
	if err != nil {
		return &mtxconf.InvalidError{Err: err}
	}
	if len(changed) > 0 {
		return &hookError{changed}
	}
	targets, err := mtxconf.OutboundChanges(before, after)
	if err != nil {
		return &mtxconf.InvalidError{Err: err}
	}
	ctx := context.Background()
	for _, o := range targets {
		check := s.d.NetGuard.CheckSource
		if o.Kind == "forward" {
			check = s.d.NetGuard.CheckForward
		}
		if err := check(ctx, o.URL); err != nil {
			return &mtxconf.InvalidError{Err: fmt.Errorf("%s: %w", o.Where, err)}
		}
	}
	return nil
}

type hookError struct{ keys []string }

func (e *hookError) Error() string {
	return "Hooks run commands inside the MediaMTX container, so they are changed only in the hooks editor, which is not " +
		"available yet. This change touches: " + strings.Join(e.keys, ", ") + "."
}

// finishWrite answers a config write and records it in the audit log.
func (s *Server) finishWrite(w http.ResponseWriter, r *http.Request, action, target string, details map[string]any, res mtxconf.Result, err error) {
	if details == nil {
		details = map[string]any{}
	}
	if err == nil {
		details["snapshot"] = res.ID
		if res.Applied.State != "" {
			details["applied"] = res.Applied.State
		}
		audit.Set(r.Context(), action, target, details)
		si := snapshotInfo(res.Snapshot, false)
		out := WriteResult{Changed: true, SHA256: res.SHA256, Snapshot: &si}
		if res.Applied.State != "" {
			out.Applied = &res.Applied
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var notApplied *mtxconf.NotAppliedError
	if errors.As(err, &notApplied) {
		details["applied"] = mtxconf.AppliedRestored
		details["snapshot"] = notApplied.Result.ID
	}
	details["refused"] = err.Error()
	audit.Set(r.Context(), action, target, details)
	if errors.Is(err, mtxconf.ErrUnchanged) {
		_, sum, _ := s.d.Config.Current()
		writeJSON(w, http.StatusOK, WriteResult{Changed: false, SHA256: sum})
		return
	}
	s.writeConfigError(w, err)
}

func (s *Server) writeConfigError(w http.ResponseWriter, err error) {
	var invalid *mtxconf.InvalidError
	var hooks *hookError
	var notApplied *mtxconf.NotAppliedError
	switch {
	case errors.As(err, &notApplied):
		writeError(w, http.StatusBadGateway, "not_applied", notApplied.Error())
	case errors.As(err, &invalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_config", invalid.Error())
	case errors.As(err, &hooks):
		writeError(w, http.StatusForbidden, "hooks", hooks.Error())
	case errors.Is(err, mtxconf.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "mediamtx.yml has changed since you loaded it. Reload and apply your change again.")
	case errors.Is(err, yamledit.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "There is no such entry in mediamtx.yml.")
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusServiceUnavailable, "canceled", "The request was cancelled.")
	default:
		s.d.Log.Error("config write failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "The change could not be made; mediamtx.yml is unchanged.")
	}
}

func (s *Server) author(r *http.Request) string {
	name, _ := actor(r)
	return name
}

func reasonOr(reason, fallback string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fallback
	}
	if len(reason) > 200 {
		reason = strings.ToValidUTF8(reason[:200], "")
	}
	return reason
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
