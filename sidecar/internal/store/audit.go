package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// AuditEvent is one entry of the append-only audit log.
type AuditEvent struct {
	ID          int64
	At          time.Time
	Actor       string // a username, "cli", "system", or the name tried in a failed sign-in
	ActorUserID *int64
	IP          string
	Action      string // e.g. "setup.complete", "auth.login", "credential.add"
	Target      string
	Details     map[string]any
}

// InsertAudit appends an event.
func (s *Store) InsertAudit(ctx context.Context, e AuditEvent) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	details, err := json.Marshal(e.Details)
	if err != nil {
		return err
	}
	if e.Details == nil {
		details = []byte("{}")
	}
	var uid sql.NullInt64
	if e.ActorUserID != nil {
		uid = sql.NullInt64{Int64: *e.ActorUserID, Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_log (at, actor, actor_user_id, ip, action, target, details)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ms(e.At), e.Actor, uid, e.IP, e.Action, e.Target, string(details))
	return err
}

// ListAudit returns the newest events first.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, actor, actor_user_id, ip, action, target, details
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at int64
		var uid sql.NullInt64
		var details string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &uid, &e.IP, &e.Action, &e.Target, &details); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		if uid.Valid {
			e.ActorUserID = &uid.Int64
		}
		if err := json.Unmarshal([]byte(details), &e.Details); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// AuditQuery filters the audit log. Empty fields match everything; Action matches a prefix ("user." finds every user
// change), Target a substring. Before pages backwards by id.
type AuditQuery struct {
	Actor  string
	Action string
	Target string
	From   time.Time
	To     time.Time
	Before int64
	Limit  int
}

// QueryAudit returns matching events, newest first.
func (s *Store) QueryAudit(ctx context.Context, q AuditQuery) ([]AuditEvent, error) {
	where, args := []string{"1 = 1"}, []any{}
	if q.Actor != "" {
		where, args = append(where, "actor = ? COLLATE NOCASE"), append(args, q.Actor)
	}
	if q.Action != "" {
		where, args = append(where, "action LIKE ? ESCAPE '\\'"), append(args, likeEscape(q.Action)+"%")
	}
	if q.Target != "" {
		where, args = append(where, "target LIKE ? ESCAPE '\\'"), append(args, "%"+likeEscape(q.Target)+"%")
	}
	if !q.From.IsZero() {
		where, args = append(where, "at >= ?"), append(args, ms(q.From))
	}
	if !q.To.IsZero() {
		where, args = append(where, "at < ?"), append(args, ms(q.To))
	}
	if q.Before > 0 {
		where, args = append(where, "id < ?"), append(args, q.Before)
	}
	if q.Limit <= 0 || q.Limit > 10000 {
		q.Limit = 100
	}
	// The WHERE clause joins only the fixed conditions above; every value is a parameter.
	query := `SELECT id, at, actor, actor_user_id, ip, action, target, details FROM audit_log WHERE ` + //nolint:gosec // G202: fixed conditions only
		strings.Join(where, " AND ") + ` ORDER BY id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		var at int64
		var uid sql.NullInt64
		var details string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &uid, &e.IP, &e.Action, &e.Target, &details); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		if uid.Valid {
			e.ActorUserID = &uid.Int64
		}
		if err := json.Unmarshal([]byte(details), &e.Details); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
