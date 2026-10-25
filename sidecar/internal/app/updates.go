package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/buildinfo"
)

// Updates: once a day, the sidecar asks GitHub for the latest release of MediaMTX UI and tells admins when it is newer
// than this one. It only tells: updating stays with whoever runs the server (docs/upgrading.md), since the sidecar has
// no hold on Docker. Each admin can skip a version or be reminded in a week; the check can be switched off in the UI,
// or for good with MTXUI_UPDATE_CHECK=off. Builds that are no release (dev, edge-...) never check.

const (
	releasesURL   = "https://api.github.com/repos/greatmastix/MediaMTXUI/releases/latest"
	releasePages  = "https://github.com/greatmastix/MediaMTXUI/releases/tag/"
	updateEvery   = 24 * time.Hour
	updateSnooze  = 7 * 24 * time.Hour
	metaUpdateOff = "updates.check.off" // "1" when an admin switched the check off

	// Per admin, by user id.
	metaUpdateSkip   = "updates.skip."   // the version they skipped
	metaUpdateSnooze = "updates.snooze." // until when (RFC 3339) they want no reminder
)

var releaseTag = regexp.MustCompile(`^v?(\d{1,4})\.(\d{1,4})\.(\d{1,4})$`)

// version is a release's number, from its tag ("v1.4.0").
type version [3]int

func parseVersion(s string) (version, bool) {
	m := releaseTag.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	var v version
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

func (v version) newer(than version) bool {
	for i := range v {
		if v[i] != than[i] {
			return v[i] > than[i]
		}
	}
	return false
}

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

// updateState is what the last check found.
type updateState struct {
	mu        sync.Mutex
	url       string // GitHub's latest-release endpoint; tests point it elsewhere
	latest    *version
	page      string // the release's page on GitHub
	published time.Time
	checkedAt time.Time
	err       string
	wake      chan struct{} // checks now (the check was switched on)
}

func (u *updateState) init() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.wake == nil {
		u.wake = make(chan struct{}, 1)
	}
	if u.url == "" {
		u.url = releasesURL
	}
}

// running is this build's version, when it is a release.
func running() (version, bool) { return parseVersion(buildinfo.Version) }

// updateCheckOn reports whether the check runs, and why not when it does not.
func (s *Server) updateCheckOn(ctx context.Context) (bool, string) {
	if !s.d.Settings.UpdateCheck {
		return false, "MTXUI_UPDATE_CHECK is off"
	}
	if _, ok := running(); !ok {
		return false, "this build (" + buildinfo.Version + ") is no release"
	}
	if off, _, _ := s.d.Store.Meta(ctx, metaUpdateOff); off == "1" {
		return false, ""
	}
	return true, ""
}

// RunUpdates checks for a new release a minute after start, then daily, and at once when the check is switched on.
func (s *Server) RunUpdates(ctx context.Context) {
	s.updates.init()
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.updates.wake:
			t.Stop()
		}
		if on, _ := s.updateCheckOn(ctx); on {
			s.checkUpdate(ctx)
		}
		t.Reset(updateEvery)
	}
}

// checkUpdate asks GitHub for the latest release and keeps what it says. Nothing it answers is shown as it is: only a
// version number and the release's own page, both checked.
func (s *Server) checkUpdate(ctx context.Context) {
	s.updates.init()
	latest, page, published, err := s.fetchLatest(ctx)
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	s.updates.checkedAt = time.Now()
	if err != nil {
		s.updates.err = err.Error()
		s.d.Log.Debug("update check failed", "err", err)
		return
	}
	s.updates.err, s.updates.latest, s.updates.page, s.updates.published = "", latest, page, published
}

func (s *Server) fetchLatest(ctx context.Context) (*version, string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s.updates.mu.Lock()
	url := s.updates.url
	s.updates.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "MediaMTX-UI/"+buildinfo.Version)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", time.Time{}, errors.New("GitHub cannot be reached")
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, "", time.Time{}, nil // no release yet
	case res.StatusCode != http.StatusOK:
		return nil, "", time.Time{}, fmt.Errorf("GitHub answered %d", res.StatusCode)
	}
	var rel struct {
		Tag        string    `json:"tag_name"`
		Page       string    `json:"html_url"`
		Published  time.Time `json:"published_at"`
		Draft      bool      `json:"draft"`
		Prerelease bool      `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, "", time.Time{}, errors.New("GitHub's answer cannot be read")
	}
	v, ok := parseVersion(rel.Tag)
	if !ok || rel.Draft || rel.Prerelease || rel.Page != releasePages+rel.Tag {
		return nil, "", time.Time{}, nil // not a release this checks for
	}
	return &v, rel.Page, rel.Published, nil
}

// UpdatesInfo is what the UI shows about updates, for the admin asking.
type UpdatesInfo struct {
	Current  string `json:"current"`
	Checking bool   `json:"checking"`
	// Locked says why the switch cannot turn the check on ("" when it can).
	Locked    string     `json:"locked"`
	Latest    string     `json:"latest,omitempty"`
	Page      string     `json:"page,omitempty"`
	Published *time.Time `json:"published,omitempty"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
	// Available: Latest is newer than this server. Show: and this admin has neither skipped it nor asked for later.
	Available    bool       `json:"available"`
	Show         bool       `json:"show"`
	Skipped      string     `json:"skipped,omitempty"`
	SnoozedUntil *time.Time `json:"snoozedUntil,omitempty"`
}

func (s *Server) updatesInfo(ctx context.Context, uid int64) UpdatesInfo {
	s.updates.init()
	on, locked := s.updateCheckOn(ctx)
	info := UpdatesInfo{Current: buildinfo.Version, Checking: on, Locked: locked}
	if !on {
		return info
	}
	s.updates.mu.Lock()
	latest, page, published, checked, cerr := s.updates.latest, s.updates.page, s.updates.published, s.updates.checkedAt, s.updates.err
	s.updates.mu.Unlock()
	if !checked.IsZero() {
		info.CheckedAt, info.Error = &checked, cerr
	}
	cur, _ := running()
	if latest == nil {
		return info
	}
	info.Latest, info.Page = latest.String(), page
	if !published.IsZero() {
		info.Published = &published
	}
	info.Available = latest.newer(cur)
	key := strconv.FormatInt(uid, 10)
	skipped, _, _ := s.d.Store.Meta(ctx, metaUpdateSkip+key)
	info.Skipped = skipped
	if until, _, _ := s.d.Store.Meta(ctx, metaUpdateSnooze+key); until != "" {
		if t, err := time.Parse(time.RFC3339, until); err == nil && time.Now().Before(t) {
			info.SnoozedUntil = &t
		}
	}
	sv, hasSkip := parseVersion(skipped)
	info.Show = info.Available && (!hasSkip || latest.newer(sv)) && info.SnoozedUntil == nil
	return info
}

func (s *Server) updatesGet(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	writeJSON(w, http.StatusOK, s.updatesInfo(r.Context(), cur.user.ID))
}

// updatesSettings switches the daily check on or off, for the whole server.
func (s *Server) updatesSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Check bool `json:"check"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Check && !s.d.Settings.UpdateCheck {
		writeError(w, http.StatusConflict, "locked", "MTXUI_UPDATE_CHECK=off keeps the update check off on this server.")
		return
	}
	off := "1"
	if req.Check {
		off = ""
	}
	if err := s.d.Store.SetMeta(ctx, metaUpdateOff, off); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The setting could not be saved.")
		return
	}
	audit.Set(ctx, "updates.check", "", map[string]any{"check": req.Check})
	s.updates.init()
	if req.Check {
		select {
		case s.updates.wake <- struct{}{}:
		default:
		}
	}
	cur, _ := current(ctx)
	writeJSON(w, http.StatusOK, s.updatesInfo(ctx, cur.user.ID))
}

// updatesDismiss is one admin's answer to the notice: skip this version, or remind them in a week.
func (s *Server) updatesDismiss(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"` // skip or later
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	cur, _ := current(ctx)
	key := strconv.FormatInt(cur.user.ID, 10)
	info := s.updatesInfo(ctx, cur.user.ID)
	var err error
	switch req.Action {
	case "skip":
		if info.Latest == "" {
			writeError(w, http.StatusConflict, "nothing", "There is no new version to skip.")
			return
		}
		err = s.d.Store.SetMeta(ctx, metaUpdateSkip+key, info.Latest)
		audit.Set(ctx, "updates.skip", info.Latest, nil)
	case "later":
		err = s.d.Store.SetMeta(ctx, metaUpdateSnooze+key, time.Now().Add(updateSnooze).UTC().Format(time.RFC3339))
		audit.Set(ctx, "updates.later", info.Latest, nil)
	default:
		writeError(w, http.StatusBadRequest, "invalid", "The action is skip or later.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The answer could not be saved.")
		return
	}
	writeJSON(w, http.StatusOK, s.updatesInfo(ctx, cur.user.ID))
}
