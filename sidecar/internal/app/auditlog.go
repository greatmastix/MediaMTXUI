package app

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"mtxui/internal/store"
)

// The audit log viewer: filter and page through what everyone (and the system) changed, and export
// it. The log itself is append-only (triggers refuse updates and deletes); the API only reads it.

// AuditEntry is one event as the viewer shows it.
type AuditEntry struct {
	ID      int64          `json:"id"`
	At      time.Time      `json:"at"`
	Actor   string         `json:"actor"`
	IP      string         `json:"ip"`
	Action  string         `json:"action"`
	Target  string         `json:"target"`
	Details map[string]any `json:"details"`
}

func auditQuery(w http.ResponseWriter, r *http.Request, limit int) (store.AuditQuery, bool) {
	q := r.URL.Query()
	aq := store.AuditQuery{Actor: q.Get("actor"), Action: q.Get("action"), Target: q.Get("target"), Limit: limit}
	for _, f := range []struct {
		key string
		dst *time.Time
	}{{"from", &aq.From}, {"to", &aq.To}} {
		if v := q.Get(f.key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid", f.key+" is not a time like 2026-10-01T12:00:00Z.")
				return aq, false
			}
			*f.dst = t
		}
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid", "before is an entry id.")
			return aq, false
		}
		aq.Before = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, "invalid", "limit is 1 to 500.")
			return aq, false
		}
		aq.Limit = n
	}
	return aq, true
}

func entries(events []store.AuditEvent) []AuditEntry {
	out := make([]AuditEntry, 0, len(events))
	for _, e := range events {
		if e.Details == nil {
			e.Details = map[string]any{}
		}
		out = append(out, AuditEntry{ID: e.ID, At: e.At, Actor: e.Actor, IP: e.IP, Action: e.Action, Target: e.Target, Details: e.Details})
	}
	return out
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	q, ok := auditQuery(w, r, 100)
	if !ok {
		return
	}
	events, err := s.d.Store.QueryAudit(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the audit log.")
		return
	}
	writeJSON(w, http.StatusOK, entries(events))
}

// auditExport downloads what the filter matches (at most 10,000 entries) as CSV or JSON.
func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) {
	q, ok := auditQuery(w, r, 10000)
	if !ok {
		return
	}
	q.Limit = 10000
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "json" {
		writeError(w, http.StatusBadRequest, "invalid", "The format is csv or json.")
		return
	}
	events, err := s.d.Store.QueryAudit(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the audit log.")
		return
	}
	name := "audit-" + time.Now().UTC().Format("2006-01-02T15-04-05Z") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if format == "json" {
		writeJSON(w, http.StatusOK, entries(events))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "at", "actor", "ip", "action", "target", "details"})
	for _, e := range entries(events) {
		d, _ := json.Marshal(e.Details)
		_ = cw.Write([]string{
			strconv.FormatInt(e.ID, 10), e.At.UTC().Format(time.RFC3339), csvSafe(e.Actor), csvSafe(e.IP),
			csvSafe(e.Action), csvSafe(e.Target), csvSafe(string(d)),
		})
	}
	cw.Flush()
}

// csvSafe keeps a spreadsheet from taking a cell for a formula (a username or target could start with "=").
func csvSafe(v string) string {
	if v != "" && (v[0] == '=' || v[0] == '+' || v[0] == '-' || v[0] == '@' || v[0] == '\t' || v[0] == '\r') {
		return "'" + v
	}
	return v
}
