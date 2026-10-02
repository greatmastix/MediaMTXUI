package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/mtxconf"
	"mtxui/internal/pathname"
	"mtxui/internal/probe"
	"mtxui/internal/recdisk"
	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

// Recordings: MediaMTX records, the sidecar browses, exports, deletes and keeps the disk in check.
// Listing and deleting go through MediaMTX's Control API, spans and exports through its playback server; the sidecar
// mounts the recordings volume read-only and only measures it (recdisk). A request's path is a MediaMTX path name,
// checked and passed to MediaMTX as a query value: it is never joined to a filesystem path.
//
// The budget: every MTXUI_RECORDINGS_CHECK_EVERY the volume is measured; over MTXUI_RECORDINGS_MAX_GB, or under
// MTXUI_RECORDINGS_MIN_FREE_GB free, the oldest segments are deleted through the API (never a path's newest, which may
// be in progress). Under MTXUI_RECORDINGS_CRITICAL_FREE_GB free (pruning could not help: something else fills the
// disk), the guard switches recording off for every path that records, with a banner and an audit entry, until an admin
// switches it back on.

// recordingsState is what the budget loop last measured.
type recordingsState struct {
	mu      sync.Mutex
	usage   recdisk.Usage
	at      time.Time
	err     string
	exports chan struct{} // one token per running export
}

func (s *Server) recordingsRoot() string { return path.Join(s.d.Settings.DataDir, "recordings") }

func (s *Server) exportSlots() chan struct{} {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	if s.rec.exports == nil {
		s.rec.exports = make(chan struct{}, s.d.Settings.ExportConcurrency)
	}
	return s.rec.exports
}

// recording is one path's recordings as MediaMTX lists them.
type recording struct {
	Name     string `json:"name"`
	Segments []struct {
		Start time.Time `json:"start"`
	} `json:"segments"`
}

// listRecordings asks MediaMTX for every path's segments.
func (s *Server) listRecordings(ctx context.Context) ([]recording, error) {
	if s.d.MTX == nil {
		return nil, errors.New("MediaMTX's API is not configured")
	}
	var out []recording
	for page := 0; page < 20; page++ {
		code, body, err := s.d.MTX.Get(ctx, fmt.Sprintf("/v3/recordings/list?itemsPerPage=1000&page=%d", page))
		if err != nil {
			return nil, err
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("MediaMTX answered %d", code)
		}
		var doc struct {
			PageCount int         `json:"pageCount"`
			Items     []recording `json:"items"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, err
		}
		out = append(out, doc.Items...)
		if page+1 >= doc.PageCount {
			break
		}
	}
	return out, nil
}

// RecordingsDisk is the recordings filesystem as the page shows it.
type RecordingsDisk struct {
	Recordings int64        `json:"recordings"` // bytes of segments
	Free       int64        `json:"free"`
	Total      int64        `json:"total"`
	Budget     int64        `json:"budget"` // 0: none
	MinFree    int64        `json:"minFree"`
	Critical   int64        `json:"critical"`
	MeasuredAt *time.Time   `json:"measuredAt"`
	Error      string       `json:"error,omitempty"`
	Guard      *guardRecord `json:"guard"` // recording switched off for lack of space, or nil
}

// RecordingPath is one path's recordings.
type RecordingPath struct {
	Name     string    `json:"name"`
	Segments int       `json:"segments"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	Bytes    int64     `json:"bytes"`
}

func (s *Server) recordingsDisk(ctx context.Context) RecordingsDisk {
	s.rec.mu.Lock()
	u, at, errText := s.rec.usage, s.rec.at, s.rec.err
	s.rec.mu.Unlock()
	d := RecordingsDisk{
		Recordings: u.Bytes, Free: u.Free, Total: u.Total, Error: errText,
		Budget: s.d.Settings.RecordingsMaxBytes, MinFree: s.d.Settings.RecordingsMinFreeBytes,
		Critical: s.d.Settings.RecordingsCriticalBytes, Guard: s.guard(ctx),
	}
	if !at.IsZero() {
		d.MeasuredAt = &at
	}
	return d
}

func (s *Server) recordingsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.listRecordings(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "mediamtx", "MediaMTX does not list its recordings: "+err.Error())
		return
	}
	s.rec.mu.Lock()
	sizes := s.rec.usage.ByDir
	s.rec.mu.Unlock()
	paths := []RecordingPath{}
	for _, rec := range list {
		if len(rec.Segments) == 0 {
			continue
		}
		p := RecordingPath{Name: rec.Name, Segments: len(rec.Segments), First: rec.Segments[0].Start, Bytes: sizes[rec.Name]}
		for _, seg := range rec.Segments {
			if seg.Start.Before(p.First) {
				p.First = seg.Start
			}
			if seg.Start.After(p.Last) {
				p.Last = seg.Start
			}
		}
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].Name < paths[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"disk": s.recordingsDisk(ctx), "paths": paths})
}

// recordingPath reads and checks the path query parameter: a MediaMTX path name, never a filesystem path.
func recordingPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.URL.Query().Get("path")
	if err := pathname.Valid(name); err != nil || strings.HasPrefix(name, "~") {
		writeError(w, http.StatusBadRequest, "invalid", "Name a path.")
		return "", false
	}
	return name, true
}

func timeParam(w http.ResponseWriter, r *http.Request, key string, required bool) (time.Time, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" && !required {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("%s is not a time like 2026-10-01T12:00:00Z.", key))
		return t, false
	}
	return t, true
}

// RecordingSpan is a stretch of recording without gaps (MediaMTX joins segments that follow each other).
type RecordingSpan struct {
	Start    time.Time `json:"start"`
	Duration float64   `json:"duration"` // seconds
}

func (s *Server) recordingSpans(w http.ResponseWriter, r *http.Request) {
	name, ok := recordingPath(w, r)
	if !ok {
		return
	}
	start, ok := timeParam(w, r, "start", false)
	if !ok {
		return
	}
	end, ok := timeParam(w, r, "end", false)
	if !ok {
		return
	}
	q := url.Values{"path": {name}}
	if !start.IsZero() {
		q.Set("start", start.UTC().Format(time.RFC3339Nano))
	}
	if !end.IsZero() {
		q.Set("end", end.UTC().Format(time.RFC3339Nano))
	}
	if s.d.Playback == nil {
		writeError(w, http.StatusBadGateway, "mediamtx", "MediaMTX's playback server is not configured.")
		return
	}
	code, body, err := s.d.Playback.Get(r.Context(), "/list?"+q.Encode())
	switch {
	case err != nil:
		writeError(w, http.StatusBadGateway, "mediamtx", "MediaMTX's playback server does not answer.")
		return
	case code == http.StatusNotFound:
		writeJSON(w, http.StatusOK, []RecordingSpan{})
		return
	case code != http.StatusOK:
		writeError(w, http.StatusBadGateway, "mediamtx", fmt.Sprintf("MediaMTX's playback server answered %d.", code))
		return
	}
	spans := []RecordingSpan{}
	if err := json.Unmarshal(body, &spans); err != nil {
		writeError(w, http.StatusBadGateway, "mediamtx", "MediaMTX's playback server sent an unreadable list.")
		return
	}
	writeJSON(w, http.StatusOK, spans)
}

func (s *Server) recordingSegments(w http.ResponseWriter, r *http.Request) {
	name, ok := recordingPath(w, r)
	if !ok {
		return
	}
	segs, err := s.segmentsOf(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "mediamtx", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, segs)
}

type segmentStart struct {
	Start time.Time `json:"start"`
}

func (s *Server) segmentsOf(ctx context.Context, name string) ([]segmentStart, error) {
	if s.d.MTX == nil {
		return nil, errors.New("MediaMTX's API is not configured")
	}
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	code, body, err := s.d.MTX.Get(ctx, "/v3/recordings/get/"+strings.Join(parts, "/"))
	switch {
	case err != nil:
		return nil, errors.New("MediaMTX's API does not answer")
	case code == http.StatusNotFound:
		return []segmentStart{}, nil
	case code != http.StatusOK:
		return nil, fmt.Errorf("MediaMTX answered %d", code)
	}
	var rec struct {
		Segments []segmentStart `json:"segments"`
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, errors.New("MediaMTX sent an unreadable recording")
	}
	return rec.Segments, nil
}

// recordingExport streams a range of a path's recordings from MediaMTX's playback server: MP4 to download, or
// fragmented MP4 to play in the page. Duration, size and the number running at once are capped.
func (s *Server) recordingExport(w http.ResponseWriter, r *http.Request) {
	name, ok := recordingPath(w, r)
	if !ok {
		return
	}
	start, ok := timeParam(w, r, "start", true)
	if !ok {
		return
	}
	q := r.URL.Query()
	secs, err := strconv.ParseFloat(q.Get("duration"), 64)
	maxSecs := s.d.Settings.ExportMaxDuration.Seconds()
	if err != nil || secs <= 0 || secs > maxSecs {
		writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("The duration is 0 to %g seconds.", maxSecs))
		return
	}
	format := q.Get("format")
	if format != "mp4" && format != "fmp4" {
		writeError(w, http.StatusBadRequest, "invalid", "The format is mp4 or fmp4.")
		return
	}
	if s.d.Playback == nil {
		writeError(w, http.StatusBadGateway, "mediamtx", "MediaMTX's playback server is not configured.")
		return
	}
	slots := s.exportSlots()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		writeError(w, http.StatusTooManyRequests, "busy", fmt.Sprintf("%d exports are running; try again when one ends.", cap(slots)))
		return
	}
	get := url.Values{
		"path": {name}, "start": {start.UTC().Format(time.RFC3339Nano)},
		"duration": {strconv.FormatFloat(secs, 'f', -1, 64)}, "format": {format},
	}
	resp, err := s.d.Playback.Open(r.Context(), "/get?"+get.Encode())
	if err != nil {
		writeError(w, http.StatusBadGateway, "mediamtx", "MediaMTX's playback server does not answer.")
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "not_found", "Nothing was recorded in that range.")
		return
	case resp.StatusCode != http.StatusOK:
		writeError(w, http.StatusBadGateway, "mediamtx", fmt.Sprintf("MediaMTX's playback server answered %d.", resp.StatusCode))
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	if q.Get("download") == "1" {
		file := strings.NewReplacer("/", "_", ":", "-").Replace(name + "_" + start.UTC().Format("2006-01-02T15:04:05Z"))
		w.Header().Set("Content-Disposition", `attachment; filename="`+file+`.mp4"`)
	}
	w.WriteHeader(http.StatusOK)
	n, err := io.CopyN(w, resp.Body, s.d.Settings.ExportMaxBytes)
	if err == nil && n == s.d.Settings.ExportMaxBytes {
		// The cap is reached: end the response abruptly, so the client sees a broken download, not a whole one.
		s.d.Log.Warn("recording export cut at the size cap", "path", name, "bytes", n)
		panic(http.ErrAbortHandler)
	}
}

type deleteRequest struct {
	Path   string      `json:"path"`
	Starts []time.Time `json:"starts"`
}

// recordingsDelete deletes segments of a path through MediaMTX's API.
func (s *Server) recordingsDelete(w http.ResponseWriter, r *http.Request) {
	var req deleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := pathname.Valid(req.Path); err != nil || strings.HasPrefix(req.Path, "~") {
		writeError(w, http.StatusBadRequest, "invalid", "Name a path.")
		return
	}
	if len(req.Starts) == 0 || len(req.Starts) > 1000 {
		writeError(w, http.StatusBadRequest, "invalid", "Name 1 to 1000 segments by their start.")
		return
	}
	deleted, failed := s.deleteSegments(r.Context(), req.Path, req.Starts)
	audit.Set(r.Context(), "recordings.delete", req.Path, map[string]any{"deleted": deleted, "failed": failed})
	s.wakeRecordings()
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted, "failed": failed})
}

func (s *Server) deleteSegments(ctx context.Context, name string, starts []time.Time) (deleted, failed int) {
	for _, start := range starts {
		q := url.Values{"path": {name}, "start": {start.UTC().Format(time.RFC3339Nano)}}
		code, err := s.d.MTX.Delete(ctx, "/v3/recordings/segments/delete?"+q.Encode())
		if err != nil || code != http.StatusOK {
			failed++
			continue
		}
		deleted++
	}
	return deleted, failed
}

// The budget loop.

func (s *Server) wakeRecordings() {
	select {
	case s.recWake <- struct{}{}:
	default:
	}
}

// RunRecordings measures the recordings volume and enforces the budget every MTXUI_RECORDINGS_CHECK_EVERY (and after
// a deletion), until ctx ends.
func (s *Server) RunRecordings(ctx context.Context) {
	t := time.NewTicker(s.d.Settings.RecordingsCheckEvery)
	defer t.Stop()
	for {
		s.recordingsOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.recWake:
		}
	}
}

func (s *Server) measure() (recdisk.Usage, error) {
	u, err := recdisk.Measure(s.recordingsRoot())
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	if err != nil {
		s.rec.err = "The recordings volume cannot be measured: " + err.Error()
		return u, err
	}
	s.rec.usage, s.rec.at, s.rec.err = u, time.Now(), ""
	return u, nil
}

func (s *Server) recordingsOnce(ctx context.Context) {
	u, err := s.measure()
	if err != nil {
		return
	}
	set := s.d.Settings
	// over says why recordings must shrink, and by how many bytes.
	over := func(u recdisk.Usage) (string, int64) {
		switch {
		case set.RecordingsMaxBytes > 0 && u.Bytes > set.RecordingsMaxBytes:
			return "budget", u.Bytes - set.RecordingsMaxBytes
		case set.RecordingsMinFreeBytes > 0 && u.Free < set.RecordingsMinFreeBytes:
			return "free space", set.RecordingsMinFreeBytes - u.Free
		}
		return "", 0
	}
	if why, _ := over(u); why != "" {
		deleted := 0
		for round := 0; round < 20; round++ {
			reason, need := over(u)
			if reason == "" {
				break
			}
			n := s.pruneOldest(ctx, need, u)
			if n == 0 {
				break // nothing left to delete: only the segments being written remain
			}
			deleted += n
			if u, err = s.measure(); err != nil {
				return
			}
		}
		if deleted > 0 {
			s.d.Log.Info("recordings pruned", "reason", why, "segments", deleted, "bytes", u.Bytes, "free", u.Free)
			s.d.Audit.Record(ctx, store.AuditEvent{
				Actor: "system", Action: "recordings.prune", Target: "recordings",
				Details: map[string]any{"reason": why, "deleted": deleted, "recordings": u.Bytes, "free": u.Free},
			})
		}
	}
	if set.RecordingsCriticalBytes > 0 && u.Free < set.RecordingsCriticalBytes && s.guard(ctx) == nil {
		s.tripGuard(ctx, u)
	}
}

// pruneOldest deletes the oldest segments across all paths until about need bytes are gone, never a path's newest
// (it may be in progress). A segment's size is estimated from its path's measured total; the caller measures again.
func (s *Server) pruneOldest(ctx context.Context, need int64, u recdisk.Usage) int {
	list, err := s.listRecordings(ctx)
	if err != nil {
		s.d.Log.Warn("recordings: cannot list for pruning", "err", err)
		return 0
	}
	type seg struct {
		path  string
		start time.Time
		size  int64
	}
	var all []seg
	for _, rec := range list {
		size := int64(1)
		if len(rec.Segments) > 0 {
			size = max(1, u.ByDir[rec.Name]/int64(len(rec.Segments)))
		}
		starts := make([]time.Time, 0, len(rec.Segments))
		for _, sg := range rec.Segments {
			starts = append(starts, sg.Start)
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
		for i, st := range starts {
			if i < len(starts)-1 {
				all = append(all, seg{rec.Name, st, size})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].start.Before(all[j].start) })
	deleted, freed := 0, int64(0)
	for _, sg := range all {
		if freed >= need || deleted >= 500 {
			break
		}
		if d, _ := s.deleteSegments(ctx, sg.path, []time.Time{sg.start}); d == 1 {
			deleted++
			freed += sg.size
		}
	}
	return deleted
}

// The guard.

const guardKey = "recordings.guard"

type guardRecord struct {
	Since    time.Time `json:"since"`
	Free     int64     `json:"free"`
	Paths    []string  `json:"paths"`    // paths whose record was switched off
	Defaults bool      `json:"defaults"` // pathDefaults' record was switched off
}

func (s *Server) guard(ctx context.Context) *guardRecord {
	v, ok, err := s.d.Store.Meta(ctx, guardKey)
	if err != nil || !ok || v == "" {
		return nil
	}
	var g guardRecord
	if json.Unmarshal([]byte(v), &g) != nil {
		return nil
	}
	return &g
}

func yes(v any) bool {
	b, ok := asBool(v)
	return ok && b
}

func asBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(x) {
		case "yes", "true":
			return true, true
		case "no", "false":
			return false, true
		}
	}
	return false, false
}

// tripGuard switches recording off wherever it is on.
func (s *Server) tripGuard(ctx context.Context, u recdisk.Usage) {
	cfg := s.readMTXConfig()
	g := guardRecord{Since: time.Now(), Free: u.Free}
	if pd, ok := cfg.global["pathDefaults"].(map[string]any); ok && yes(pd["record"]) {
		g.Defaults = true
	}
	for name, p := range cfg.paths {
		if yes(p["record"]) {
			g.Paths = append(g.Paths, name)
		}
	}
	sort.Strings(g.Paths)
	if !g.Defaults && len(g.Paths) == 0 {
		return // nothing records: recordings are not what fills the disk
	}
	reason := fmt.Sprintf("recording switched off: %.1f GB free on the recordings volume", float64(u.Free)/(1<<30))
	_, err := s.d.Config.Edit(ctx, "system", reason, s.guardEdit, func(d *yamledit.Doc) error {
		if g.Defaults {
			if err := d.Set([]string{"pathDefaults", "record"}, false); err != nil {
				return err
			}
		}
		for _, name := range g.Paths {
			if err := d.Set([]string{"paths", name, "record"}, false); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, mtxconf.ErrUnchanged) {
		s.d.Log.Error("recordings guard: cannot switch recording off", "err", err)
		return
	}
	b, _ := json.Marshal(g)
	if err := s.d.Store.SetMeta(ctx, guardKey, string(b)); err != nil {
		s.d.Log.Error("recordings guard: cannot remember what was switched off", "err", err)
	}
	s.d.Log.Warn("recordings guard tripped", "free", u.Free, "paths", g.Paths, "defaults", g.Defaults)
	s.d.Audit.Record(ctx, store.AuditEvent{
		Actor: "system", Action: "recordings.guard", Target: "recordings",
		Details: map[string]any{"free": u.Free, "paths": g.Paths, "defaults": g.Defaults},
	})
}

// recordingsGuardRelease switches recording back on where the guard switched it off, once there is room again.
func (s *Server) recordingsGuardRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g := s.guard(ctx)
	if g == nil {
		writeError(w, http.StatusConflict, "not_tripped", "Recording was not switched off.")
		return
	}
	u, err := s.measure()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The recordings volume cannot be measured.")
		return
	}
	if u.Free < s.d.Settings.RecordingsMinFreeBytes {
		writeError(w, http.StatusConflict, "no_room", fmt.Sprintf("Only %.1f GB are free; make room for at least %.1f GB first.",
			float64(u.Free)/(1<<30), float64(s.d.Settings.RecordingsMinFreeBytes)/(1<<30)))
		return
	}
	cfg := s.readMTXConfig()
	_, err = s.d.Config.Edit(ctx, s.author(r), "recording switched back on", s.guardEdit, func(d *yamledit.Doc) error {
		if g.Defaults {
			if err := d.Set([]string{"pathDefaults", "record"}, true); err != nil {
				return err
			}
		}
		for _, name := range g.Paths {
			if _, still := cfg.paths[name]; still {
				if err := d.Set([]string{"paths", name, "record"}, true); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, mtxconf.ErrUnchanged) {
		s.writeConfigError(w, err)
		return
	}
	if err := s.d.Store.SetMeta(ctx, guardKey, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Recording is on again, but the guard could not be cleared.")
		return
	}
	audit.Set(ctx, "recordings.guard.release", "recordings", map[string]any{"paths": g.Paths, "defaults": g.Defaults})
	writeJSON(w, http.StatusOK, s.recordingsDisk(ctx))
}

// recordingWarnings is the guard's banner, for every page.
func (s *Server) recordingWarnings(ctx context.Context) []probe.Warning {
	g := s.guard(ctx)
	if g == nil {
		return nil
	}
	return []probe.Warning{{Code: "recordings_guard", Message: fmt.Sprintf(
		"Recording is switched off: only %.1f GB were free on the recordings volume (%s). Make room, then an admin "+
			"switches it back on under Recordings.", float64(g.Free)/(1<<30), g.Since.UTC().Format("2006-01-02 15:04 MST"))}}
}
