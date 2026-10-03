package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/credentials"
	"mtxui/internal/mtxauth"
	"mtxui/internal/store"
)

// Stream credentials (admin only): the names and secrets publishers and readers give MediaMTX. The secret is shown
// once, at creation; the database keeps an HMAC of it. Revoking a credential refuses new connections at once and
// closes the sessions it opened.

// Opened tells which MediaMTX sessions a credential authenticated (mtxauth.Handler).
type Opened interface {
	SessionsOf(id int64) []mtxauth.SessionRef
}

// Kicker calls MediaMTX's kick operations with the sidecar's own credentials.
type Kicker interface {
	Post(ctx context.Context, path string) (int, error)
}

// CredentialInfo describes a credential to the UI; the secret appears only in the answer to its creation.
type CredentialInfo struct {
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Actions    []string   `json:"actions"`
	Paths      []string   `json:"paths"`
	Sources    []string   `json:"sources"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
	State      string     `json:"state"` // active, expired, revoked
	Secret     string     `json:"secret,omitempty"`
}

func credentialInfo(c store.Credential, now time.Time) CredentialInfo {
	state := "active"
	switch {
	case c.RevokedAt != nil:
		state = "revoked"
	case c.ExpiresAt != nil && !c.ExpiresAt.After(now):
		state = "expired"
	}
	nonNil := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	return CredentialInfo{
		Name: c.Name, Kind: c.Kind, Actions: nonNil(c.Actions), Paths: nonNil(c.Paths), Sources: nonNil(c.SourceCIDRs),
		ExpiresAt: c.ExpiresAt, RevokedAt: c.RevokedAt, LastUsedAt: c.LastUsedAt, CreatedAt: c.CreatedAt,
		CreatedBy: c.CreatedBy, State: state,
	}
}

func (s *Server) credentialsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Creds.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the credentials.")
		return
	}
	now := time.Now()
	out := make([]CredentialInfo, 0, len(list))
	for _, c := range list {
		out = append(out, credentialInfo(c, now))
	}
	writeJSON(w, http.StatusOK, out)
}

type newCredential struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Actions        []string `json:"actions"`
	Paths          []string `json:"paths"`
	Sources        []string `json:"sources"`
	ExpiresInHours int      `json:"expiresInHours"` // 0: no expiry
}

func (s *Server) credentialCreate(w http.ResponseWriter, r *http.Request) {
	var req newCredential
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ExpiresInHours < 0 || req.ExpiresInHours > 24*366 {
		writeError(w, http.StatusBadRequest, "invalid", "Expiry must be between 1 hour and a year, or none.")
		return
	}
	if req.Kind == "" {
		req.Kind = credentials.KindPassword
	}
	secret, c, err := s.d.Creds.Add(r.Context(), credentials.Spec{
		Name: req.Name, Kind: req.Kind, Actions: req.Actions, Paths: req.Paths, SourceCIDRs: req.Sources,
		TTL: time.Duration(req.ExpiresInHours) * time.Hour, CreatedBy: s.author(r),
	})
	switch {
	case errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, "exists", "A credential with this name already exists.")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	audit.Set(r.Context(), "credential.add", c.Name, map[string]any{
		"kind": c.Kind, "actions": c.Actions, "paths": c.Paths, "sources": c.SourceCIDRs, "expiresAt": c.ExpiresAt,
	})
	info := credentialInfo(c, time.Now())
	info.Secret = secret
	writeJSON(w, http.StatusCreated, info)
}

// credentialRevoke revokes a credential and closes what it opened: the sessions the authentication endpoint saw it
// authenticate, and any session MediaMTX lists under its name (opened before the sidecar started).
func (s *Server) credentialRevoke(w http.ResponseWriter, r *http.Request) {
	name := param(r, "name")
	c, err := s.d.Store.CredentialByName(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such credential.")
		return
	}
	if err := s.d.Creds.Revoke(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The credential could not be revoked.")
		return
	}
	refs := s.d.Opened.SessionsOf(c.ID)
	if c.Kind == credentials.KindPassword {
		for _, cl := range s.d.Live.ClientsOf(c.Name) {
			refs = append(refs, mtxauth.SessionRef{Protocol: cl.Protocol, ID: cl.ID})
		}
	}
	kicked := s.kick(r.Context(), refs)
	audit.Set(r.Context(), "credential.revoke", name, map[string]any{"kicked": kicked})
	writeJSON(w, http.StatusOK, map[string]int{"kicked": kicked})
}

// kick closes sessions, each once; the ones that have already ended answer 404 and do not count.
func (s *Server) kick(ctx context.Context, refs []mtxauth.SessionRef) int {
	seen := map[mtxauth.SessionRef]bool{}
	kicked := 0
	for _, ref := range refs {
		path := mtxauth.KickPath(ref)
		if path == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		code, err := s.d.Kicker.Post(context.WithoutCancel(ctx), path)
		switch {
		case err != nil:
			s.d.Log.Warn("kick failed", "protocol", ref.Protocol, "id", ref.ID, "err", err)
		case code == http.StatusOK:
			kicked++
		}
	}
	return kicked
}
