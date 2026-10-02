package store

import (
	"context"
	"testing"
	"time"
)

func TestHistory(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := range 20 {
		if i == 7 {
			continue // the sidecar was down
		}
		m := t0.Add(time.Duration(i) * time.Minute)
		paths := map[string]PathPoint{}
		if i < 10 {
			paths["live/a"] = PathPoint{InBps: float64(1000 * i), Readers: i}
		}
		if err := s.AddHistoryMinute(ctx, HistoryPoint{T: m, InBps: float64(i), OutBps: 2, Readers: i, Paths: 1}, paths); err != nil {
			t.Fatal(err)
		}
	}
	// Replacing a minute.
	if err := s.AddHistoryMinute(ctx, HistoryPoint{T: t0, InBps: 100, Readers: 0, Paths: 1}, nil); err != nil {
		t.Fatal(err)
	}
	mins, err := s.History(ctx, t0, t0.Add(time.Hour), time.Minute)
	if err != nil || len(mins) != 19 || mins[0].InBps != 100 || !mins[1].T.Equal(t0.Add(time.Minute)) {
		t.Fatalf("minutes: %d %+v %v", len(mins), mins, err)
	}
	tens, _ := s.History(ctx, t0, t0.Add(time.Hour), 10*time.Minute)
	// Minutes 0–9 without 7: mean of 100,1..6,8,9 / 9; most readers 9.
	if len(tens) != 2 || !tens[0].T.Equal(t0) || tens[0].Readers != 9 || tens[0].InBps != (100.0+1+2+3+4+5+6+8+9)/9 ||
		!tens[1].T.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("buckets %+v", tens)
	}
	pa, _ := s.PathHistory(ctx, "live/a", t0, t0.Add(time.Hour), time.Minute)
	if len(pa) != 19 || pa[0].InBps != 0 || pa[3].InBps != 3000 || pa[15].InBps != 0 || pa[15].Readers != 0 {
		t.Fatalf("path minutes %+v", pa)
	}
	pt, _ := s.PathHistory(ctx, "live/a", t0, t0.Add(time.Hour), 10*time.Minute)
	if len(pt) != 2 || pt[0].Readers != 9 || pt[1].InBps != 0 {
		t.Fatalf("path buckets %+v", pt)
	}
	if none, _ := s.PathHistory(ctx, "nope", t0, t0.Add(time.Hour), time.Hour); len(none) != 1 || none[0].InBps != 0 {
		t.Fatalf("unknown path %+v", none)
	}
	if err := s.PruneHistory(ctx, t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.History(ctx, t0, t0.Add(time.Hour), time.Minute); len(left) != 10 {
		t.Fatalf("after pruning: %d", len(left))
	}
	if left, _ := s.PathHistory(ctx, "live/a", t0, t0.Add(time.Hour), time.Minute); len(left) != 10 || left[0].InBps != 0 {
		t.Fatalf("path rows after pruning: %+v", left)
	}
}
