// Package audit records who changed what. Every request whose method can change state passes Middleware, which
// writes one append-only entry after the handler has run; handlers name the action and target and add details.
package audit

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/store"
)

type entry struct {
	mu      sync.Mutex
	action  string
	target  string
	actor   string
	actorID *int64
	details map[string]any
}

type ctxKey struct{}

func from(ctx context.Context) *entry {
	e, _ := ctx.Value(ctxKey{}).(*entry)
	return e
}

// Set names the current request's action and target and merges details into its entry. Outside Middleware it does
// nothing.
func Set(ctx context.Context, action, target string, details map[string]any) {
	e := from(ctx)
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if action != "" {
		e.action = action
	}
	if target != "" {
		e.target = target
	}
	if e.details == nil {
		e.details = map[string]any{}
	}
	maps.Copy(e.details, details)
}

// Actor sets who acted, for requests where that is not the signed-in user (a sign-in, a setup).
func Actor(ctx context.Context, name string, id *int64) {
	if e := from(ctx); e != nil {
		e.mu.Lock()
		e.actor, e.actorID = name, id
		e.mu.Unlock()
	}
}

// Recorder writes audit entries.
type Recorder struct {
	store *store.Store
	log   *slog.Logger
}

// NewRecorder returns a recorder.
func NewRecorder(s *store.Store, log *slog.Logger) *Recorder { return &Recorder{store: s, log: log} }

// Record writes one entry directly (command-line actions, background jobs). A failure is logged, loudly.
func (r *Recorder) Record(ctx context.Context, e store.AuditEvent) {
	if err := r.store.InsertAudit(context.WithoutCancel(ctx), e); err != nil {
		r.log.Error("audit: entry lost", "action", e.Action, "actor", e.Actor, "err", err)
	}
}

// Middleware records every request with a method that can change state. actor reports the signed-in user, if any.
func (r *Recorder) Middleware(actor func(*http.Request) (string, *int64)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if auth.SafeMethod(req.Method) {
				next.ServeHTTP(w, req)
				return
			}
			e := &entry{}
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, req.WithContext(context.WithValue(req.Context(), ctxKey{}, e)))

			e.mu.Lock()
			defer e.mu.Unlock()
			name, id := e.actor, e.actorID
			if name == "" {
				name, id = actor(req)
			}
			if name == "" {
				name = "anonymous"
			}
			action := e.action
			if action == "" {
				pattern := req.URL.Path
				if rc := chi.RouteContext(req.Context()); rc != nil && rc.RoutePattern() != "" {
					pattern = rc.RoutePattern()
				}
				action = req.Method + " " + pattern
			}
			details := map[string]any{"status": rec.status}
			maps.Copy(details, e.details)
			ip := ""
			if a := clientip.From(req.Context()).IP; a.IsValid() {
				ip = a.String()
			}
			r.Record(req.Context(), store.AuditEvent{Actor: name, ActorUserID: id, IP: ip, Action: action, Target: e.target, Details: details})
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
