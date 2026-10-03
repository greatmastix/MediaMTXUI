package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/portgate"
)

// exposureOnly answers 404 while exposure control is off (MTXUI_EXPOSURE_CONTROL).
func (s *Server) exposureOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.d.Settings.ExposureControl {
			writeError(w, http.StatusNotFound, "exposure_off", "Exposure control is off (MTXUI_EXPOSURE_CONTROL): the stream ports are as published.")
			return
		}
		h(w, r)
	}
}

// Exposure control (admin only): which stream ports the internet may reach. The sidecar only writes its
// wish into desired.json; the host helper mtx-portgate checks it against the operator's policy, changes the firewall
// and reports back in its status, which the UI shows as the truth. The status also streams as the "exposure" extra.

// ExposureView is GET /v1/exposure: the helper's status, the pending wish, and the caller's address as the sidecar
// sees it, for "open to my address".
type ExposureView struct {
	portgate.View
	ClientIP string    `json:"clientIP"`
	Rules    AutoRules `json:"rules"`
}

func (s *Server) exposureGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ExposureView{
		View: s.d.Exposure.View(), ClientIP: clientip.From(r.Context()).IP.String(),
		Rules: s.autoRules(r.Context()),
	})
}

type exposureOpen struct {
	Sources   []string `json:"sources"`
	MyAddress bool     `json:"myAddress"` // add the caller's own address
	Hours     float64  `json:"hours"`     // how long; 0 with permanent
	Permanent bool     `json:"permanent"`
}

func (s *Server) exposureOpen(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req exposureOpen
	if !decodeJSON(w, r, &req) {
		return
	}
	want := portgate.Want{Sources: req.Sources}
	if want.Sources == nil {
		want.Sources = []string{}
	}
	if req.MyAddress {
		ip := clientip.From(r.Context()).IP
		if !ip.IsValid() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			writeError(w, http.StatusBadRequest, "invalid",
				"Your address as the sidecar sees it ("+ip.String()+") is not a public one: check the trusted proxies setting.")
			return
		}
		want.Sources = append(want.Sources, ip.String())
	}
	switch {
	case req.Permanent && req.Hours == 0:
	case !req.Permanent && req.Hours > 0 && req.Hours <= 24*366:
		until := time.Now().Add(time.Duration(req.Hours * float64(time.Hour))).Truncate(time.Second).UTC()
		want.Until = &until
	default:
		writeError(w, http.StatusBadRequest, "invalid", "Give a duration in hours, or permanent.")
		return
	}
	s.followHostCloseAll(r.Context()) // an admin opening after a close-all on the host opens on top of it
	d, err := s.d.Exposure.Set(id, &want)
	if !s.exposureWritten(w, err) {
		return
	}
	audit.Set(r.Context(), "exposure.open", id, map[string]any{"rev": d.Rev, "sources": d.Want[id].Sources, "until": want.Until})
	s.exposurePublish()
	writeJSON(w, http.StatusOK, s.d.Exposure.View())
}

func (s *Server) exposureClose(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.followHostCloseAll(r.Context())
	d, err := s.d.Exposure.Set(id, nil)
	if !s.exposureWritten(w, err) {
		return
	}
	audit.Set(r.Context(), "exposure.close", id, map[string]any{"rev": d.Rev})
	s.exposurePublish()
	writeJSON(w, http.StatusOK, s.d.Exposure.View())
}

// exposureCloseAll closes everything: the manual openings, and the automatic ones with their rules switched off (they
// would reopen within seconds otherwise). It also takes in a close-all on the host.
func (s *Server) exposureCloseAll(w http.ResponseWriter, r *http.Request) {
	if err := s.setAutoRules(r.Context(), AutoRules{}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot switch the automatic rules off.")
		return
	}
	d, err := s.d.Exposure.CloseAll()
	if !s.exposureWritten(w, err) {
		return
	}
	audit.Set(r.Context(), "exposure.close-all", "", map[string]any{"rev": d.Rev})
	s.exposurePublish()
	writeJSON(w, http.StatusOK, s.d.Exposure.View())
}

func (s *Server) exposureWritten(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, portgate.ErrNotInstalled):
		writeError(w, http.StatusServiceUnavailable, "not_installed",
			"Exposure control is not installed on this host: open the stream ports in your firewall by hand.")
	case errors.Is(err, portgate.ErrClosedOnHost):
		writeError(w, http.StatusConflict, "closed_on_host",
			"Everything was just closed on the host (mtx-portgate close-all): try again in a few seconds.")
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	default:
		return true
	}
	return false
}

// exposurePublish streams the view at once, ahead of the watcher's next look.
func (s *Server) exposurePublish() {
	s.d.Live.SetExtra("exposure", auth.RoleAdmin, s.ExposureLive(context.Background()))
}

// ExposureLive is the view the event stream carries: the helper's status, the openings and the rules.
func (s *Server) ExposureLive(ctx context.Context) ExposureView {
	return ExposureView{View: s.d.Exposure.View(), Rules: s.autoRules(ctx)}
}
