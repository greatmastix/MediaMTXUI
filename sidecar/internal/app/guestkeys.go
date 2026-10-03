package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/audit"
	"mtxui/internal/credentials"
	"mtxui/internal/mtxauth"
	"mtxui/internal/store"
)

// Guest keys: time-limited keys for one stream, made by whoever manages it. A publish key lets a guest
// stream to it (a co-host, a stand-in), a read key lets someone watch a private stream. Each is an ordinary credential
// scoped to the stream's path with an expiry; the secret is shown once. While a guest publish key is valid, automatic
// exposure opens the stream's publishing ports to anyone (decided 2026-10-01), since a guest's address is unknown; the
// key still decides who gets in. When a key expires, what it opened is disconnected; revoking does the same at once.

// guestHours are the lifetimes on offer: an hour to a week.
var guestHours = map[int]bool{1: true, 6: true, 24: true, 72: true, 168: true}

// maxGuestKeys is how many valid guest keys a stream may have at once.
const maxGuestKeys = 10

// GuestKeyInfo is a guest key as the stream page lists it.
type GuestKeyInfo struct {
	ID         int64      `json:"id"` // the credential's id
	Name       string     `json:"name"`
	Kind       string     `json:"kind"` // publish or read
	Label      string     `json:"label"`
	State      string     `json:"state"` // active, expired or revoked
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
}

func guestInfo(g store.GuestKey, now time.Time) GuestKeyInfo {
	c := g.Credential
	state := "active"
	switch {
	case c.RevokedAt != nil:
		state = "revoked"
	case c.ExpiresAt != nil && !c.ExpiresAt.After(now):
		state = "expired"
	}
	return GuestKeyInfo{
		ID: c.ID, Name: c.Name, Kind: g.Kind, Label: g.Label, State: state,
		ExpiresAt: c.ExpiresAt, LastUsedAt: c.LastUsedAt, CreatedAt: c.CreatedAt, CreatedBy: c.CreatedBy,
	}
}

func (s *Server) guestKeysList(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	list, err := s.d.Store.GuestKeys(r.Context(), st.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot list the guest keys.")
		return
	}
	now := time.Now()
	out := make([]GuestKeyInfo, 0, len(list))
	for _, g := range list {
		out = append(out, guestInfo(g, now))
	}
	writeJSON(w, http.StatusOK, out)
}

// NewGuestKey answers the creation of a guest key: the only time its secret is shown.
type NewGuestKey struct {
	Guest  GuestKeyInfo `json:"guest"`
	Secret string       `json:"secret"`
}

func (s *Server) guestKeyCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	var req struct {
		Kind  string `json:"kind"`
		Label string `json:"label"`
		Hours int    `json:"hours"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	actions, prefix := []string{"publish"}, "guest-"
	switch req.Kind {
	case store.GuestPublish:
	case store.GuestRead:
		actions, prefix = []string{"read"}, "guest-view-"
	default:
		writeError(w, http.StatusBadRequest, "invalid", "A guest key is for streaming (publish) or watching (read).")
		return
	}
	label := strings.TrimSpace(req.Label)
	if label == "" || utf8.RuneCountInString(label) > 60 || strings.ContainsAny(label, "\x00\n\r") {
		writeError(w, http.StatusBadRequest, "invalid", "Say who the key is for, in up to 60 characters.")
		return
	}
	if !guestHours[req.Hours] {
		writeError(w, http.StatusBadRequest, "invalid", "A guest key lasts 1, 6, 24, 72 or 168 hours.")
		return
	}
	existing, err := s.d.Store.GuestKeys(ctx, st.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot make the guest key.")
		return
	}
	active := 0
	for _, g := range existing {
		if guestInfo(g, time.Now()).State == "active" {
			active++
		}
	}
	if active >= maxGuestKeys {
		writeError(w, http.StatusConflict, "limit", fmt.Sprintf("A stream can have at most %d valid guest keys. Revoke one first.", maxGuestKeys))
		return
	}
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot make the guest key.")
		return
	}
	secret, c, err := s.d.Creds.Add(ctx, credentials.Spec{
		Name: prefix + strings.ToLower(keyNameB32.EncodeToString(b)), Kind: credentials.KindPassword,
		Actions: actions, Paths: []string{st.Name}, TTL: time.Duration(req.Hours) * time.Hour, CreatedBy: s.author(r),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot make the guest key.")
		return
	}
	if err := s.d.Store.AddGuestKey(ctx, st.ID, c.ID, req.Kind, label); err != nil {
		_ = s.d.Creds.Revoke(context.WithoutCancel(ctx), c.Name)
		writeError(w, http.StatusInternalServerError, "internal", "Cannot make the guest key.")
		return
	}
	g, err := s.d.Store.GuestKey(ctx, st.ID, c.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the guest key back.")
		return
	}
	if req.Kind == store.GuestPublish {
		s.auto.poke() // open the ports for the guest's encoder now, not in 15 s
	}
	audit.Set(ctx, "stream.guest.create", st.Name, map[string]any{
		"key": c.Name, "kind": req.Kind, "label": label, "expiresAt": c.ExpiresAt,
	})
	writeJSON(w, http.StatusCreated, NewGuestKey{Guest: guestInfo(g, time.Now()), Secret: secret})
}

func (s *Server) guestKeyRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "kid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such guest key.")
		return
	}
	g, err := s.d.Store.GuestKey(ctx, st.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such guest key.")
		return
	}
	kicked := 0
	if g.Credential.RevokedAt == nil {
		kicked = s.retireKey(ctx, g.Credential.ID)
		s.auto.poke()
	}
	audit.Set(ctx, "stream.guest.revoke", st.Name, map[string]any{
		"key": g.Credential.Name, "kind": g.Kind, "label": g.Label, "kicked": kicked,
	})
	if g, err = s.d.Store.GuestKey(ctx, st.ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the guest key back.")
		return
	}
	writeJSON(w, http.StatusOK, guestInfo(g, time.Now()))
}

// retireGuestKeys revokes a stream's valid guest keys and disconnects what they opened (the stream is going away).
func (s *Server) retireGuestKeys(ctx context.Context, streamID int64) int {
	list, err := s.d.Store.GuestKeys(ctx, streamID)
	if err != nil {
		return 0
	}
	kicked := 0
	for _, g := range list {
		if g.Credential.RevokedAt == nil {
			kicked += s.retireKey(ctx, g.Credential.ID)
		}
	}
	return kicked
}

// endExpiredGuests disconnects what guest keys opened once they have expired: MediaMTX checks a key only when a
// connection starts, so a guest stream would otherwise run on past its key. The access loop (RunAccess) calls it
// whether or not exposure control is on.
func (s *Server) endExpiredGuests(ctx context.Context, now time.Time) {
	list, err := s.d.Store.ExpiredGuestKeys(ctx, now.Add(-24*time.Hour), now)
	if err != nil {
		return
	}
	for _, g := range list {
		refs := s.d.Opened.SessionsOf(g.Credential.ID)
		for _, cl := range s.d.Live.ClientsOf(g.Credential.Name) {
			refs = append(refs, mtxauth.SessionRef{Protocol: cl.Protocol, ID: cl.ID})
		}
		if len(refs) > 0 {
			if n := s.kick(ctx, refs); n > 0 {
				s.d.Log.Info("guest key expired: disconnected", "key", g.Credential.Name, "sessions", n)
			}
		}
	}
}
