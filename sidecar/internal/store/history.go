package store

import (
	"context"
	"time"
)

// HistoryPoint is the long-term history's global point: a minute, or a bucket of minutes (mean bitrates, the most of
// each count).
type HistoryPoint struct {
	T       time.Time
	InBps   float64
	OutBps  float64
	Paths   int
	Online  int
	Readers int
	Clients int
}

// PathPoint is one path's point of the long-term history.
type PathPoint struct {
	T       time.Time
	InBps   float64
	OutBps  float64
	Readers int
}

// AddHistoryMinute stores a minute's summary and its paths' (replacing a minute stored before).
func (s *Store) AddHistoryMinute(ctx context.Context, p HistoryPoint, paths map[string]PathPoint) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO history_minutes (t, in_bps, out_bps, paths, online, readers, clients)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ms(p.T), p.InBps, p.OutBps, p.Paths, p.Online, p.Readers, p.Clients); err != nil {
		return err
	}
	for name, pp := range paths {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO path_history_minutes (path, t, in_bps, out_bps, readers)
			VALUES (?, ?, ?, ?, ?)`, name, ms(p.T), pp.InBps, pp.OutBps, pp.Readers); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// History returns the points in [from, to), in buckets of the given length (a whole number of minutes), oldest first.
// Buckets without any stored minute are absent (the sidecar was down), so charts show a gap.
func (s *Store) History(ctx context.Context, from, to time.Time, bucket time.Duration) ([]HistoryPoint, error) {
	b := bucket.Milliseconds()
	rows, err := s.db.QueryContext(ctx, `SELECT t / ? * ?, avg(in_bps), avg(out_bps), max(paths), max(online), max(readers), max(clients)
		FROM history_minutes WHERE t >= ? AND t < ? GROUP BY t / ? ORDER BY 1`, b, b, ms(from), ms(to), b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryPoint{}
	for rows.Next() {
		var p HistoryPoint
		var t int64
		if err := rows.Scan(&t, &p.InBps, &p.OutBps, &p.Paths, &p.Online, &p.Readers, &p.Clients); err != nil {
			return nil, err
		}
		p.T = fromMS(t)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PathHistory returns one path's points in [from, to), bucketed like History: a minute the sidecar saw MediaMTX
// without the path having traffic counts as zero, a minute it did not see MediaMTX is absent.
func (s *Store) PathHistory(ctx context.Context, path string, from, to time.Time, bucket time.Duration) ([]PathPoint, error) {
	b := bucket.Milliseconds()
	rows, err := s.db.QueryContext(ctx, `SELECT g.t / ? * ?, avg(coalesce(p.in_bps, 0)), avg(coalesce(p.out_bps, 0)), max(coalesce(p.readers, 0))
		FROM history_minutes g LEFT JOIN path_history_minutes p ON p.path = ? AND p.t = g.t
		WHERE g.t >= ? AND g.t < ? GROUP BY g.t / ? ORDER BY 1`, b, b, path, ms(from), ms(to), b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PathPoint{}
	for rows.Next() {
		var p PathPoint
		var t int64
		if err := rows.Scan(&t, &p.InBps, &p.OutBps, &p.Readers); err != nil {
			return nil, err
		}
		p.T = fromMS(t)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneHistory deletes the minutes before cut.
func (s *Store) PruneHistory(ctx context.Context, cut time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM path_history_minutes WHERE t < ?`, ms(cut)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM history_minutes WHERE t < ?`, ms(cut))
	return err
}
