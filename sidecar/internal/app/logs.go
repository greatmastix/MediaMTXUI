package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"mtxui/internal/auth"
	"mtxui/internal/logs"
)

// The log viewer: MediaMTX's log with its rotated copies, and the sidecar's own recent
// lines; searched, tailed live, and MediaMTX's downloaded as text.

const (
	logLimitDefault = 500
	logLimitMax     = 2000
	logBacklog      = 300 // lines a live tail starts with
)

// LogLines is a search's answer: the newest matches, oldest first, and whether older ones were left out.
type LogLines struct {
	Lines []logs.Line `json:"lines"`
	More  bool        `json:"more"`
}

func logFilter(w http.ResponseWriter, r *http.Request) (logs.Filter, bool) {
	q := r.URL.Query()
	f := logs.Filter{Source: q.Get("source"), MinLevel: q.Get("level"), Query: q.Get("q")}
	if f.Source == "" {
		f.Source = logs.MediaMTX
	}
	if (f.Source != logs.MediaMTX && f.Source != logs.Sidecar) || (f.MinLevel != "" && !logs.Level(f.MinLevel)) ||
		len(f.Query) > 200 {
		writeError(w, http.StatusBadRequest, "invalid", "Unknown source or level, or a search longer than 200 characters.")
		return f, false
	}
	return f, true
}

// search returns the newest lines that pass f.
func (s *Server) searchLogs(r *http.Request, f logs.Filter, limit int) (LogLines, error) {
	if f.Source == logs.Sidecar {
		lines := s.d.Logs.Recent(f, limit+1)
		if len(lines) > limit {
			return LogLines{Lines: lines[1:], More: true}, nil
		}
		return LogLines{Lines: lines}, nil
	}
	lines, more, err := logs.Search(r.Context(), s.d.Settings.MediaMTXLog(), s.d.Settings.MediaMTXLogKeep, f, limit)
	return LogLines{Lines: lines, More: more}, err
}

func (s *Server) logsSearch(w http.ResponseWriter, r *http.Request) {
	f, ok := logFilter(w, r)
	if !ok {
		return
	}
	limit := logLimitDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > logLimitMax {
			writeError(w, http.StatusBadRequest, "invalid", "The limit is 1 to 2000 lines.")
			return
		}
		limit = n
	}
	out, err := s.searchLogs(r, f, limit)
	if err != nil {
		s.d.Log.Warn("searching the logs", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "The log could not be read.")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// logsStream is a live tail (SSE): the newest matching lines first, then new ones as they are written. It ends
// when the session does, like the event stream; a tail too slow for the hub is dropped and the browser reconnects.
func (s *Server) logsStream(w http.ResponseWriter, r *http.Request) {
	f, ok := logFilter(w, r)
	if !ok {
		return
	}
	cur, _ := current(r.Context())
	tail := s.d.Logs.Subscribe(f) // before the backlog, so nothing written meanwhile is missed
	if tail == nil {
		writeError(w, http.StatusTooManyRequests, "too_many_streams", "Too many open log views. Close some tabs.")
		return
	}
	defer s.d.Logs.Unsubscribe(tail)
	backlog, err := s.searchLogs(r, f, logBacklog)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The log could not be read.")
		return
	}
	// A line read into the backlog may also have reached the tail: skip those (same time and text) at the seam.
	seam := map[logs.Line]bool{}
	if n := len(backlog.Lines); n > 0 {
		last := backlog.Lines[n-1].T
		for i := n - 1; i >= 0 && backlog.Lines[i].T >= last-2000; i-- {
			seam[backlog.Lines[i]] = true
		}
	}

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(chunks ...string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		for _, c := range chunks {
			if _, err := io.WriteString(w, c); err != nil {
				return false
			}
		}
		return rc.Flush() == nil
	}
	sendLine := func(l logs.Line) bool {
		b, _ := json.Marshal(l)
		return send("event: line\ndata: ", string(b), "\n\n")
	}
	if !send("retry: ", strconv.Itoa(retryAfterDrops), "\n\n") {
		return
	}
	b, _ := json.Marshal(backlog)
	if !send("event: backlog\ndata: ", string(b), "\n\n") {
		return
	}
	heartbeat := time.NewTicker(heartbeatEvery)
	defer heartbeat.Stop()
	recheck := time.NewTicker(recheckEvery)
	defer recheck.Stop()
	for {
		var check bool
		select {
		case <-r.Context().Done():
			return
		case l, ok := <-tail.C:
			if !ok {
				return
			}
			if seam[l] {
				delete(seam, l)
				continue
			}
			if !sendLine(l) {
				return
			}
		case <-heartbeat.C:
			if !send(": keep-alive\n\n") {
				return
			}
		case <-recheck.C:
			check = true
		case <-s.sessionNudge.wait():
			check = true
		}
		if check {
			_, user, err := s.d.Sessions.Lookup(r.Context(), cur.token)
			if errors.Is(err, auth.ErrNoSession) || (err == nil && user.Role != cur.user.Role) {
				send("event: session\ndata: {\"state\":\"ended\"}\n\n")
				return
			}
		}
	}
}

// logsDownload sends MediaMTX's log with its rotated copies as one text file, oldest first.
func (s *Server) logsDownload(w http.ResponseWriter, r *http.Request) {
	path := s.d.Settings.MediaMTXLog()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mediamtx-`+time.Now().UTC().Format("20060102-150405")+`.log"`)
	w.Header().Set("Cache-Control", "no-store")
	for _, name := range logs.Files(path, s.d.Settings.MediaMTXLogKeep) {
		if err := logs.Copy(r.Context(), w, name); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.d.Log.Warn("sending the log", "err", err)
			return
		}
	}
}
