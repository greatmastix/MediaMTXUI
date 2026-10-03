package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

// A patch writes only the columns it names: what another request changed meanwhile stays.
func TestPatchStream(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	u, err := s.CreateUser(ctx, "alice", "", "streamer")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.CreateStream(ctx, Stream{Name: "live/a", Title: "A", OwnerID: &u.ID, Public: true, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	owned := func(g Stream) bool { return g.OwnerID != nil && *g.OwnerID == u.ID }
	for _, tc := range []struct {
		name  string
		patch StreamPatch
		check func(Stream) bool
	}{{
		name:  "the holding columns leave the owner and the public flag",
		patch: StreamPatch{Holding: ptr("file"), ClipAAC: ptr("1-a.mp4"), Audio: ptr("opus"), Format: ptr("720p60")},
		check: func(g Stream) bool {
			return owned(g) && g.Public && g.Title == "A" && g.Holding == "file" && g.ClipAAC == "1-a.mp4" &&
				g.Audio == "opus" && g.Format == "720p60"
		},
	}, {
		name:  "private, with a title",
		patch: StreamPatch{Public: ptr(false), Title: ptr("B")},
		check: func(g Stream) bool { return !g.Public && g.Title == "B" && owned(g) && g.Holding == "file" },
	}, {
		name:  "0 clears the owner",
		patch: StreamPatch{OwnerID: ptr(int64(0))},
		check: func(g Stream) bool { return g.OwnerID == nil && g.Title == "B" && g.ClipAAC == "1-a.mp4" },
	}, {
		name:  "an owner again",
		patch: StreamPatch{OwnerID: &u.ID, Target: ptr("twitch")},
		check: func(g Stream) bool { return owned(g) && g.Target == "twitch" && !g.Public },
	}, {
		name:  "nothing",
		patch: StreamPatch{},
		check: func(g Stream) bool { return owned(g) && g.Title == "B" && g.Audio == "opus" },
	}} {
		if err := s.PatchStream(ctx, st.ID, tc.patch); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got, _ := s.StreamByID(ctx, st.ID); !tc.check(got) {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
	if err := s.PatchStream(ctx, 999, StreamPatch{Title: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing stream: %v", err)
	}
}

// Replacements of a stream's key at the same time each get back a different key: every key but the last comes back
// exactly once, for its replacer to revoke.
func TestReplaceStreamKeyAtOnce(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	st, err := s.CreateStream(ctx, Stream{Name: "live/a", Title: "A", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	const n = 12
	ids := make([]int64, n)
	for i := range ids {
		name := fmt.Sprintf("key-%d", i)
		if ids[i], err = s.CreateCredential(ctx, Credential{Name: name, Kind: "password", SecretHash: name, CreatedAt: time.Now(), CreatedBy: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	replaced := make([]*int64, n)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			var err error
			if replaced[i], err = s.ReplaceStreamKey(ctx, st.ID, KeyPlayback, ids[i], "enc"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, _ := s.StreamByID(ctx, st.ID)
	if got.PlaybackKeyID == nil || got.PublishKeyID != nil {
		t.Fatalf("stream %+v", got)
	}
	back, none := []int64{*got.PlaybackKeyID}, 0
	for _, r := range replaced {
		if r == nil {
			none++
		} else {
			back = append(back, *r)
		}
	}
	slices.Sort(back)
	if none != 1 || !slices.Equal(back, ids) {
		t.Errorf("replaced %v (and %d nil), current %d; want each of %v once", back, none, *got.PlaybackKeyID, ids)
	}
	if _, err := s.ReplaceStreamKey(ctx, 999, KeyPublish, ids[0], "enc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing stream: %v", err)
	}
}

// Deleting a stream with its keys revokes, in the same transaction, every valid credential it points to: its keys
// and all its guest keys, and nothing else.
func TestDeleteStreamKeys(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	st, err := s.CreateStream(ctx, Stream{Name: "live/a", Title: "A", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateStream(ctx, Stream{Name: "live/b", Title: "B", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	cred := func(name string, expires *time.Time, revoked bool) int64 {
		t.Helper()
		id, err := s.CreateCredential(ctx, Credential{Name: name, Kind: "password", SecretHash: name, ExpiresAt: expires, CreatedAt: now, CreatedBy: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		if revoked {
			if err := s.RevokeCredential(ctx, name, now.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	publish, playback := cred("key-a", nil, false), cred("view-a", nil, true)
	if err := s.SetStreamKey(ctx, st.ID, KeyPublish, publish, "enc"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStreamKey(ctx, st.ID, KeyPlayback, playback, "enc"); err != nil {
		t.Fatal(err)
	}
	var guests []int64
	for i := range 60 { // valid ones older than the newest 50 too
		id := cred(fmt.Sprintf("guest-%d", i), &later, i >= 3)
		if err := s.AddGuestKey(ctx, st.ID, id, GuestPublish, "g"); err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			guests = append(guests, id)
		}
	}
	expired := cred("guest-expired", &earlier, false)
	if err := s.AddGuestKey(ctx, st.ID, expired, GuestRead, "g"); err != nil {
		t.Fatal(err)
	}
	theirs := cred("guest-theirs", &later, false)
	if err := s.AddGuestKey(ctx, other.ID, theirs, GuestRead, "g"); err != nil {
		t.Fatal(err)
	}

	revoked, err := s.DeleteStreamKeys(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(revoked)
	if want := append([]int64{publish}, guests...); !slices.Equal(revoked, want) {
		t.Errorf("revoked %v, want %v", revoked, want)
	}
	list, _ := s.ListCredentials(ctx)
	for _, c := range list {
		valid := c.RevokedAt == nil && (c.ExpiresAt == nil || c.ExpiresAt.After(now))
		if valid != (c.ID == theirs) {
			t.Errorf("%s valid: %v", c.Name, valid)
		}
	}
	if _, err := s.StreamByID(ctx, st.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the stream: %v", err)
	}
	if _, err := s.DeleteStreamKeys(ctx, st.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

// A stream's guest keys list every valid key, however many newer ones were made and revoked, besides the newest 50.
func TestGuestKeysKeepValidOnes(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	st, err := s.CreateStream(ctx, Stream{Name: "live/a", Title: "A", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	for i := range 70 { // ten valid, then sixty revoked
		name := fmt.Sprintf("guest-%02d", i)
		id, err := s.CreateCredential(ctx, Credential{
			Name: name, Kind: "password", SecretHash: name, ExpiresAt: &later,
			CreatedAt: now.Add(time.Duration(i) * time.Second), CreatedBy: "admin",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddGuestKey(ctx, st.ID, id, GuestPublish, "g"); err != nil {
			t.Fatal(err)
		}
		if i >= 10 {
			if err := s.RevokeCredential(ctx, name, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	list, err := s.GuestKeys(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	valid := 0
	for i, g := range list {
		if g.Credential.RevokedAt == nil {
			valid++
		}
		if i > 0 && g.Credential.CreatedAt.After(list[i-1].Credential.CreatedAt) {
			t.Errorf("not newest first at %d", i)
		}
	}
	if len(list) != 60 || valid != 10 || list[0].Credential.Name != "guest-69" {
		t.Errorf("%d keys, %d valid, first %s", len(list), valid, list[0].Credential.Name)
	}
}
