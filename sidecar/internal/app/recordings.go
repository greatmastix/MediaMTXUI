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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
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
// be in progress), just enough of them: each segment's size is its own file's, found the way MediaMTX names it from
// the recordPath. When deleting every segment MediaMTX lists would not be enough (files it does not list, from an
// earlier recordPath or copied in, take the space, or something else fills the disk), none are deleted and a banner
// says why. Under MTXUI_RECORDINGS_CRITICAL_FREE_GB free, the guard switches recording off for every path that records,
// with a banner and an audit entry, until an admin switches it back on; recording switched on again meanwhile (by a
// stream's manager, or in the config) goes off again at the next check that finds the disk that full.
//
// An export holds one of MTXUI_EXPORT_CONCURRENCY slots, shared by everyone, for at most twice its range plus
// exportSlack, however slowly its client reads: a player paused in a tab, or a client that reads nothing, gives the slot
// back then (its connection breaks).

// recordingsState is what the budget loop last measured.
type recordingsState struct {
	mu        sync.Mutex
	usage     recdisk.Usage
	at        time.Time
	err       string
	shortfall string        // why the budget loop deleted nothing although recordings must shrink, or ""
	exports   chan struct{} // one token per running export
}

// exportSlack is how much longer than twice its range an export may take.
var exportSlack = time.Minute

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

// recordPaths is every recordPath in mediamtx.yml, pathDefaults' (or MediaMTX's own default) first.
func (s *Server) recordPaths() []string {
	cfg := s.readMTXConfig()
	def := recdisk.DefaultRecordPath
	if pd, ok := cfg.global["pathDefaults"].(map[string]any); ok {
		if rp, ok := pd["recordPath"].(string); ok && rp != "" {
			def = rp
		}
	}
	var others []string
	for _, p := range cfg.paths {
		if rp, ok := p["recordPath"].(string); ok && rp != def && !slices.Contains(others, rp) {
			others = append(others, rp)
		}
	}
	sort.Strings(others)
	return append([]string{def}, others...)
}

// segmentSizes flattens MediaMTX's listing and finds each segment's file in u: its size, or -1 when none is measured,
// and the bytes in segment files MediaMTX does not list.
func (s *Server) segmentSizes(list []recording, u recdisk.Usage) (segs []recdisk.Segment, sizes []int64, unlisted int64) {
	for _, rec := range list {
		for _, sg := range rec.Segments {
			segs = append(segs, recdisk.Segment{Path: rec.Name, Start: sg.Start})
		}
	}
	sizes, unlisted = u.Sizes(s.recordPaths(), segs)
	return segs, sizes, unlisted
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
	u := s.rec.usage
	s.rec.mu.Unlock()
	segs, sizes, _ := s.segmentSizes(list, u)
	bytes := map[string]int64{}
	for i, sg := range segs {
		bytes[sg.Path] += max(0, sizes[i])
	}
	may, err := s.recordingReader(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the streams.")
		return
	}
	paths := []RecordingPath{}
	for _, rec := range list {
		if len(rec.Segments) == 0 || (may != nil && !may(rec.Name)) {
			continue
		}
		p := RecordingPath{Name: rec.Name, Segments: len(rec.Segments), First: rec.Segments[0].Start, Bytes: bytes[rec.Name]}
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
	// exportMaxSeconds lets the page offer Play and Download only for ranges an export takes.
	disk := s.recordingsDisk(ctx)
	if may != nil && disk.Guard != nil { // a streamer hears of its own paths only
		g := *disk.Guard
		g.Paths = slices.DeleteFunc(slices.Clone(g.Paths), func(p string) bool { return !may(p) })
		disk.Guard = &g
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"disk": disk, "paths": paths, "exportMaxSeconds": s.d.Settings.ExportMaxDuration.Seconds(),
	})
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

// recordingReader says whose recordings the request may see: every path for viewers and up, for a streamer the paths
// of the streams it owns. A nil function means all.
func (s *Server) recordingReader(r *http.Request) (func(path string) bool, error) {
	cur, _ := current(r.Context())
	if auth.Role(cur.user.Role).AtLeast(auth.RoleViewer) {
		return nil, nil
	}
	if err := s.loadOwners(r.Context()); err != nil {
		return nil, err
	}
	uid := cur.user.ID
	return func(path string) bool { return s.ownsPath(uid, path) }, nil
}

// readablePath is recordingPath for the read routes: a streamer gets only its own streams' paths (others are answered
// as if they had no recordings at all, so their names are not confirmed).
func (s *Server) readablePath(w http.ResponseWriter, r *http.Request) (string, bool) {
	name, ok := recordingPath(w, r)
	if !ok {
		return "", false
	}
	may, err := s.recordingReader(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the streams.")
		return "", false
	}
	if may != nil && !may(name) {
		writeError(w, http.StatusNotFound, "not_found", "No recordings on that path.")
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
	name, ok := s.readablePath(w, r)
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
	name, ok := s.readablePath(w, r)
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
	name, ok := s.readablePath(w, r)
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
	// The slot comes back by end however the client reads (the server has no WriteTimeout): a player reads about as
	// fast as it plays, a download faster unless the link is slower than half the stream's bitrate. Past end, writing
	// to the client fails, and so does reading from MediaMTX. (net/http lifts the deadline when the request is done.)
	end := time.Now().Add(2*time.Duration(secs*float64(time.Second)) + exportSlack)
	_ = http.NewResponseController(w).SetWriteDeadline(end)
	ctx, cancel := context.WithDeadline(r.Context(), end)
	defer cancel()
	get := url.Values{
		"path": {name}, "start": {start.UTC().Format(time.RFC3339Nano)},
		"duration": {strconv.FormatFloat(secs, 'f', -1, 64)}, "format": {format},
	}
	resp, err := s.d.Playback.Open(ctx, "/get?"+get.Encode())
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
	// Why recordings must shrink, and by how many bytes.
	var why string
	var need int64
	if set.RecordingsMaxBytes > 0 && u.Bytes > set.RecordingsMaxBytes {
		why, need = "budget", u.Bytes-set.RecordingsMaxBytes
	}
	if set.RecordingsMinFreeBytes > 0 && set.RecordingsMinFreeBytes-u.Free > need {
		why, need = "free space", set.RecordingsMinFreeBytes-u.Free
	}
	shortfall := ""
	if why != "" {
		var deleted int
		deleted, shortfall = s.pruneOldest(ctx, why, need, u)
		if deleted > 0 {
			if u, err = s.measure(); err != nil {
				return
			}
			s.d.Log.Info("recordings pruned", "reason", why, "segments", deleted, "bytes", u.Bytes, "free", u.Free)
			s.d.Audit.Record(ctx, store.AuditEvent{
				Actor: "system", Action: "recordings.prune", Target: "recordings",
				Details: map[string]any{"reason": why, "deleted": deleted, "recordings": u.Bytes, "free": u.Free},
			})
		}
	}
	s.rec.mu.Lock()
	was := s.rec.shortfall
	s.rec.shortfall = shortfall
	s.rec.mu.Unlock()
	if shortfall != "" && was == "" {
		s.d.Log.Warn("recordings: pruning cannot help", "reason", shortfall)
	}
	if set.RecordingsCriticalBytes > 0 && u.Free < set.RecordingsCriticalBytes {
		s.tripGuard(ctx, u) // again if recording was switched back on while the guard is on
	}
}

// pruneOldest deletes the oldest segments across all paths until need bytes are gone, never a path's newest (it may
// be in progress; once done, the next check may delete it), nor one whose file it cannot find. When even all the
// segments MediaMTX lists would not make need bytes, recordings are not what takes the space: it deletes none and says
// why instead.
func (s *Server) pruneOldest(ctx context.Context, why string, need int64, u recdisk.Usage) (deleted int, shortfall string) {
	list, err := s.listRecordings(ctx)
	if err != nil {
		s.d.Log.Warn("recordings: cannot list for pruning", "err", err)
		return 0, ""
	}
	segs, sizes, unlisted := s.segmentSizes(list, u)
	newest := map[string]time.Time{}
	listed := int64(0)
	for i, sg := range segs {
		if sg.Start.After(newest[sg.Path]) {
			newest[sg.Path] = sg.Start
		}
		listed += max(0, sizes[i])
	}
	if listed < need {
		return 0, shortfallText(why, need, listed, unlisted)
	}
	type seg struct {
		recdisk.Segment
		size int64
	}
	var old []seg
	for i, sg := range segs {
		if sizes[i] >= 0 && sg.Start.Before(newest[sg.Path]) {
			old = append(old, seg{sg, sizes[i]})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].Start.Before(old[j].Start) })
	freed := int64(0)
	for _, sg := range old {
		if freed >= need {
			break
		}
		if d, _ := s.deleteSegments(ctx, sg.Path, []time.Time{sg.Start}); d == 1 {
			deleted++
			freed += sg.size
		}
	}
	return deleted, ""
}

// shortfallText is the banner for recordings that must shrink by need bytes when all MediaMTX lists is only listed.
func shortfallText(why string, need, listed, unlisted int64) string {
	text := fmt.Sprintf("Recordings are %s over the budget", bannerBytes(need))
	if why == "free space" {
		text = fmt.Sprintf("The recordings volume needs %s more free space", bannerBytes(need))
	}
	text += fmt.Sprintf(", but all the recordings MediaMTX lists come to only %s, so none were deleted.", bannerBytes(listed))
	switch {
	case unlisted > 0:
		text += fmt.Sprintf(" %s of segment files on the volume are not in MediaMTX's list (left from an earlier "+
			"recordPath, or copied there): MediaMTX cannot delete them, so remove them by hand.", bannerBytes(unlisted))
	case why == "free space":
		text += " Something other than recordings fills the disk."
	}
	return text
}

// bannerBytes is a number of bytes for a banner: MB under a GB.
func bannerBytes(n int64) string {
	if n < 1<<30 {
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
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

// tripGuard switches recording off wherever it is on. When the guard is on already (recording was switched back on
// since), it adds what it switches off now to what the admin's release switches back on.
func (s *Server) tripGuard(ctx context.Context, u recdisk.Usage) {
	cfg := s.readMTXConfig()
	var off guardRecord // what records now
	if pd, ok := cfg.global["pathDefaults"].(map[string]any); ok && yes(pd["record"]) {
		off.Defaults = true
	}
	for name, p := range cfg.paths {
		if yes(p["record"]) {
			off.Paths = append(off.Paths, name)
		}
	}
	sort.Strings(off.Paths)
	if !off.Defaults && len(off.Paths) == 0 {
		return // nothing records: recordings are not what fills the disk
	}
	reason := fmt.Sprintf("recording switched off: %.1f GB free on the recordings volume", float64(u.Free)/(1<<30))
	_, err := s.d.Config.Edit(ctx, "system", reason, s.guardEdit, func(d *yamledit.Doc) error {
		if off.Defaults {
			if err := d.Set([]string{"pathDefaults", "record"}, false); err != nil {
				return err
			}
		}
		for _, name := range off.Paths {
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
	g := guardRecord{Defaults: off.Defaults, Paths: off.Paths}
	if prev := s.guard(ctx); prev != nil {
		g.Defaults = g.Defaults || prev.Defaults
		for _, name := range prev.Paths {
			if !slices.Contains(g.Paths, name) {
				g.Paths = append(g.Paths, name)
			}
		}
		sort.Strings(g.Paths)
	}
	g.Since, g.Free = time.Now(), u.Free
	b, _ := json.Marshal(g)
	if err := s.d.Store.SetMeta(ctx, guardKey, string(b)); err != nil {
		s.d.Log.Error("recordings guard: cannot remember what was switched off", "err", err)
	}
	s.d.Log.Warn("recordings guard tripped", "free", u.Free, "paths", off.Paths, "defaults", off.Defaults)
	s.d.Audit.Record(ctx, store.AuditEvent{
		Actor: "system", Action: "recordings.guard", Target: "recordings",
		Details: map[string]any{"free": u.Free, "paths": off.Paths, "defaults": off.Defaults},
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

// recordingWarnings is the guard's banner and the budget's, for every page.
func (s *Server) recordingWarnings(ctx context.Context) []probe.Warning {
	var out []probe.Warning
	s.rec.mu.Lock()
	shortfall := s.rec.shortfall
	s.rec.mu.Unlock()
	if shortfall != "" {
		out = append(out, probe.Warning{Code: "recordings_budget", Message: shortfall})
	}
	if g := s.guard(ctx); g != nil {
		out = append(out, probe.Warning{Code: "recordings_guard", Message: fmt.Sprintf(
			"Recording is switched off: only %.1f GB were free on the recordings volume (%s). Make room, then an admin "+
				"switches it back on under Recordings.", float64(g.Free)/(1<<30), g.Since.UTC().Format("2006-01-02 15:04 MST"))})
	}
	return out
}
