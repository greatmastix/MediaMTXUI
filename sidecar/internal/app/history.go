package app

import (
	"context"
	"net/http"
	"time"

	"mtxui/internal/store"
)

// The long-term history: every minute, the live hub's samples of the minute before are summed
// up into SQLite, kept MTXUI_HISTORY_DAYS days. The charts' longer ranges read it in buckets of a size that gives
// them about a thousand points; the last hour stays the hub's 5-second samples.

// historyRanges maps a chart range to its span and bucket (0: the hub's own samples).
var historyRanges = map[string]struct{ span, bucket time.Duration }{
	"1h":  {time.Hour, 0},
	"24h": {24 * time.Hour, time.Minute},
	"7d":  {7 * 24 * time.Hour, 10 * time.Minute},
	"30d": {30 * 24 * time.Hour, time.Hour},
}

func historyRange(w http.ResponseWriter, r *http.Request) (span, bucket time.Duration, ok bool) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "1h"
	}
	rg, known := historyRanges[name]
	if !known {
		writeError(w, http.StatusBadRequest, "invalid", "The range is 1h, 24h, 7d or 30d.")
		return 0, 0, false
	}
	return rg.span, rg.bucket, true
}

// RunHistory records each minute shortly after it ends, until ctx ends, and prunes the old ones hourly.
func (s *Server) RunHistory(ctx context.Context) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute + 2*time.Second) // the minute's last sample is in by then
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		s.recordMinute(ctx, next.Truncate(time.Minute).Add(-time.Minute))
	}
}

// recordMinute stores the summary of the minute starting at start, if the hub has one.
func (s *Server) recordMinute(ctx context.Context, start time.Time) {
	sum, paths, ok := s.d.Live.Summary(start, start.Add(time.Minute))
	if ok {
		pp := make(map[string]store.PathPoint, len(paths))
		for name, p := range paths {
			pp[name] = store.PathPoint{InBps: p.InBps, OutBps: p.OutBps, Readers: p.Readers}
		}
		p := store.HistoryPoint{
			T: start, InBps: *sum.InBps, OutBps: *sum.OutBps, Paths: sum.Paths, Online: sum.Online, Readers: sum.Readers, Clients: sum.Clients,
		}
		if err := s.d.Store.AddHistoryMinute(ctx, p, pp); err != nil {
			s.d.Log.Warn("recording the history", "err", err)
		}
	}
	if start.Minute() == 0 {
		cut := start.Add(-time.Duration(s.d.Settings.HistoryDays) * 24 * time.Hour)
		if err := s.d.Store.PruneHistory(ctx, cut); err != nil {
			s.d.Log.Warn("pruning the history", "err", err)
		}
	}
}
