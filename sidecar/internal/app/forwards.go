package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/audit"
	"mtxui/internal/mtxconf"
	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

// Forwarding: a stream re-streamed to another platform. The person picks a provider and pastes the
// platform's stream key; the sidecar keeps the destination sealed and writes it into the path's forward list in
// mediamtx.yml only while it is switched on (MediaMTX reads it from there). Entries in that list that the sidecar did
// not write (an admin's, in Configuration) are left alone.
//
// The key never appears in the audit log or the sidecar's logs. An RTMP key travels in the URL fragment, which the
// RTMP client sends as the stream key and MediaMTX leaves out of its "forwarding to" log line; a WHIP key is the
// bearer token. While a forward is on, the key is in mediamtx.yml and so in the configuration history, which only
// admins can read.

// forwardProvider is a platform preset: its ingest (fixed, or pasted by the person) and the schemes a pasted one may
// use.
type forwardProvider struct {
	label   string
	server  string
	schemes []string
}

var forwardProviders = map[string]forwardProvider{
	"twitch":  {label: "Twitch", server: "rtmp://live.twitch.tv/app"},
	"youtube": {label: "YouTube", server: "rtmps://a.rtmps.youtube.com/live2"},
	"kick":    {label: "Kick", schemes: []string{"rtmps", "rtmp"}},
	"custom":  {label: "Custom", schemes: []string{"rtmp", "rtmps", "srt", "rtsp", "rtsps", "whip", "whips"}},
}

// maxForwards is how many forwards a stream may have.
const maxForwards = 5

// forwardDest is what a forward sends MediaMTX: the destination URL and, for WHIP, a bearer token. Sealed as JSON.
type forwardDest struct {
	Dest  string `json:"dest"`
	Token string `json:"token,omitempty"`
}

// userError is a message for the person who filled in the form.
type userError string

func (e userError) Error() string { return string(e) }

func userErrorf(format string, args ...any) error { return userError(fmt.Sprintf(format, args...)) }

// buildForward makes the destination for a provider from a pasted server (ignored for fixed ingests) and key, and
// the label that names it without the key.
func buildForward(provider, server, key string) (forwardDest, string, error) {
	p, ok := forwardProviders[provider]
	if !ok {
		return forwardDest{}, "", userError("Unknown provider.")
	}
	key = strings.TrimSpace(key)
	if len(key) > 512 || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return forwardDest{}, "", userError("The key is too long or contains spaces.")
	}
	raw := p.server
	if raw == "" {
		raw = strings.TrimSpace(server)
		if raw == "" {
			return forwardDest{}, "", userError("Paste the server address from the platform.")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return forwardDest{}, "", userError("The server address is not a URL like rtmp://host/app.")
	}
	scheme := strings.ToLower(u.Scheme)
	if p.server == "" && !slices.Contains(p.schemes, scheme) {
		return forwardDest{}, "", userErrorf("%s takes %s addresses.", p.label, strings.Join(p.schemes, ", "))
	}
	u.Scheme = scheme
	d := forwardDest{}
	switch scheme {
	case "rtmp", "rtmps":
		if provider == "kick" && strings.Trim(u.Path, "/") == "" {
			u.Path = "/app" // Kick shows its ingest without the application
		}
		if u.Fragment != "" && key != "" {
			return forwardDest{}, "", userError("Put the key only in the key field.")
		}
		if key != "" {
			u.Fragment = key // the RTMP client sends the fragment as the stream key
		}
		if u.Fragment == "" && provider != "custom" {
			return forwardDest{}, "", userError("Paste your stream key from the platform.")
		}
		d.Dest = u.String()
	case "whip", "whips":
		d.Dest, d.Token = u.String(), key
	default:
		if key != "" {
			return forwardDest{}, "", userErrorf("%s addresses carry their credentials in the URL; leave the key empty.", strings.ToUpper(scheme))
		}
		d.Dest = u.String()
	}
	return d, scheme + "://" + u.Host, nil
}

func forwardSealContext(streamID int64) string { return fmt.Sprintf("stream %d forward", streamID) }

func (s *Server) sealForward(streamID int64, d forwardDest) (string, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return s.d.Creds.Seal(string(b), forwardSealContext(streamID))
}

func (s *Server) openForward(f store.Forward) (forwardDest, error) {
	plain, err := s.d.Creds.Unseal(f.DestEnc, forwardSealContext(f.StreamID))
	if err != nil {
		return forwardDest{}, err
	}
	var d forwardDest
	return d, json.Unmarshal([]byte(plain), &d)
}

// syncForwards writes the stream's switched-on forwards into its path's forward list, keeping entries the sidecar did
// not write. drop names destinations to take out although no record lists them any more.
func (s *Server) syncForwards(ctx context.Context, st store.Stream, author, reason string) error {
	list, err := s.d.Store.Forwards(ctx, st.ID)
	if err != nil {
		return err
	}
	ours := map[string]bool{}
	var on []any
	for _, f := range list {
		d, err := s.openForward(f)
		if err != nil {
			return fmt.Errorf("forward %d cannot be opened: %w", f.ID, err)
		}
		ours[d.Dest] = true
		if f.Enabled {
			entry := map[string]any{"dest": d.Dest}
			if d.Token != "" {
				entry["whipBearerToken"] = d.Token
			}
			on = append(on, entry)
		}
	}
	pathConf, configured := s.readMTXConfig().paths[st.Name]
	var next []any
	if cur, ok := pathConf["forward"].([]any); ok {
		for _, e := range cur {
			if m, ok := e.(map[string]any); ok {
				if dest, _ := m["dest"].(string); ours[dest] {
					continue
				}
			}
			next = append(next, e)
		}
	}
	next = append(next, on...)
	_, err = s.d.Config.Edit(ctx, author, reason, s.guardEdit, func(d *yamledit.Doc) error {
		switch {
		case len(next) == 0:
			if err := d.Delete([]string{"paths", st.Name, "forward"}); err != nil && !errors.Is(err, yamledit.ErrNotFound) {
				return err
			}
			return nil
		case !configured: // the path left mediamtx.yml (edited by hand): put it back with its forwards
			return d.Set([]string{"paths", st.Name}, map[string]any{"forward": next})
		}
		return d.Set([]string{"paths", st.Name, "forward"}, next)
	})
	if errors.Is(err, mtxconf.ErrUnchanged) {
		return nil
	}
	return err
}

// ForwardInfo is a forward as the stream page shows it: never the key.
type ForwardInfo struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Enabled  bool   `json:"enabled"`
	// State: off, idle (on, waiting for the stream), forwarding, error, missing (on, but MediaMTX does not have it:
	// mediamtx.yml was edited elsewhere) or unknown (MediaMTX did not answer).
	State         string    `json:"state"`
	LastError     string    `json:"lastError,omitempty"`
	OutboundBytes uint64    `json:"outboundBytes"`
	CreatedAt     time.Time `json:"createdAt"`
	CreatedBy     string    `json:"createdBy"`
}

// mtxForward is an item of MediaMTX's forward destination list.
type mtxForward struct {
	Conf struct {
		Dest string `json:"dest"`
	} `json:"conf"`
	State         string `json:"state"`
	LastError     string `json:"lastError"`
	OutboundBytes uint64 `json:"outboundBytes"`
}

// forwardStates reads MediaMTX's forward destinations of a path by destination; ok is false when MediaMTX did not
// answer. A path MediaMTX does not run has none.
func (s *Server) forwardStates(ctx context.Context, name string) (map[string]mtxForward, bool) {
	if s.d.MTX == nil {
		return nil, false
	}
	code, body, err := s.d.MTX.Get(ctx, "/v3/paths/forward-dests/list?itemsPerPage=100&path="+url.QueryEscape(name))
	switch {
	case err != nil:
		return nil, false
	case code == http.StatusNotFound:
		return map[string]mtxForward{}, true
	case code != http.StatusOK:
		return nil, false
	}
	var doc struct {
		Items []mtxForward `json:"items"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	out := map[string]mtxForward{}
	for _, it := range doc.Items {
		out[it.Conf.Dest] = it
	}
	return out, true
}

func (s *Server) forwardInfos(ctx context.Context, st store.Stream) ([]ForwardInfo, error) {
	list, err := s.d.Store.Forwards(ctx, st.ID)
	if err != nil {
		return nil, err
	}
	states, answered := s.forwardStates(ctx, st.Name)
	out := make([]ForwardInfo, 0, len(list))
	for _, f := range list {
		info := ForwardInfo{
			ID: f.ID, Provider: f.Provider, Label: f.Label, Enabled: f.Enabled, State: "off",
			CreatedAt: f.CreatedAt, CreatedBy: f.CreatedBy,
		}
		if f.Enabled {
			d, err := s.openForward(f)
			switch {
			case err != nil:
				info.State = "unknown"
			case !answered:
				info.State = "unknown"
			default:
				m, ok := states[d.Dest]
				switch {
				case ok:
					info.State, info.OutboundBytes = m.State, m.OutboundBytes
					info.LastError = scrub(m.LastError, d)
				case len(states) == 0:
					info.State = "idle" // MediaMTX does not run the path (nobody streams to it)
				default:
					info.State = "missing"
				}
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// scrub takes a destination's secrets out of a message.
func scrub(msg string, d forwardDest) string {
	secrets := []string{d.Dest, d.Token}
	if u, err := url.Parse(d.Dest); err == nil {
		secrets = append(secrets, u.Fragment, u.EscapedFragment(), u.RawQuery)
		if p, ok := u.User.Password(); ok {
			secrets = append(secrets, p)
		}
	}
	for _, sec := range secrets {
		if len(sec) >= 4 {
			msg = strings.ReplaceAll(msg, sec, "…")
		}
	}
	return msg
}

func (s *Server) forwardsList(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	infos, err := s.forwardInfos(r.Context(), st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot list the forwards.")
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

type newForward struct {
	Provider string `json:"provider"`
	Server   string `json:"server"`
	Key      string `json:"key"`
	Enabled  bool   `json:"enabled"`
}

func (s *Server) forwardCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	var req newForward
	if !decodeJSON(w, r, &req) {
		return
	}
	dest, label, err := buildForward(req.Provider, req.Server, req.Key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	u, _ := url.Parse(dest.Dest)
	if err := s.d.NetGuard.CheckHost(ctx, u.Hostname()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "The server cannot be used: "+err.Error()+".")
		return
	}
	s.forwardMu.Lock()
	defer s.forwardMu.Unlock()
	existing, err := s.d.Store.Forwards(ctx, st.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot add the forward.")
		return
	}
	if len(existing) >= maxForwards {
		writeError(w, http.StatusConflict, "limit", fmt.Sprintf("A stream can forward to at most %d places.", maxForwards))
		return
	}
	for _, f := range existing {
		if d, err := s.openForward(f); err == nil && d.Dest == dest.Dest {
			writeError(w, http.StatusConflict, "exists", "This stream already forwards there with that key.")
			return
		}
	}
	sealed, err := s.sealForward(st.ID, dest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot add the forward.")
		return
	}
	f, err := s.d.Store.CreateForward(ctx, store.Forward{
		StreamID: st.ID, Provider: req.Provider, Label: label, DestEnc: sealed, Enabled: req.Enabled, CreatedBy: s.author(r),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot add the forward.")
		return
	}
	if req.Enabled {
		if err := s.syncForwards(ctx, st, s.author(r), fmt.Sprintf("stream %s forwarding to %s on", st.Name, label)); err != nil {
			_ = s.d.Store.DeleteForward(context.WithoutCancel(ctx), f.ID)
			s.writeConfigError(w, err)
			return
		}
	}
	audit.Set(ctx, "stream.forward.create", st.Name, map[string]any{"provider": req.Provider, "label": label, "enabled": req.Enabled})
	s.writeForward(w, r, st, f.ID, http.StatusCreated)
}

// forwardFor reads the forward named by the route, of a stream the caller manages.
func (s *Server) forwardFor(w http.ResponseWriter, r *http.Request) (store.Stream, store.Forward, bool) {
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return st, store.Forward{}, false
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "fid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such forward.")
		return st, store.Forward{}, false
	}
	f, err := s.d.Store.ForwardByID(r.Context(), st.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such forward.")
		return st, store.Forward{}, false
	}
	return st, f, true
}

func (s *Server) writeForward(w http.ResponseWriter, r *http.Request, st store.Stream, id int64, code int) {
	infos, err := s.forwardInfos(r.Context(), st)
	if err == nil {
		for _, info := range infos {
			if info.ID == id {
				writeJSON(w, code, info)
				return
			}
		}
	}
	writeError(w, http.StatusInternalServerError, "internal", "Cannot read the forward back.")
}

func (s *Server) forwardPatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	st, f, ok := s.forwardFor(w, r)
	if !ok || !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid", "Nothing to change.")
		return
	}
	s.forwardMu.Lock()
	defer s.forwardMu.Unlock()
	if err := s.d.Store.SetForwardEnabled(ctx, f.ID, *req.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot change the forward.")
		return
	}
	state := map[bool]string{true: "on", false: "off"}[*req.Enabled]
	if err := s.syncForwards(ctx, st, s.author(r), fmt.Sprintf("stream %s forwarding to %s %s", st.Name, f.Label, state)); err != nil {
		_ = s.d.Store.SetForwardEnabled(context.WithoutCancel(ctx), f.ID, f.Enabled)
		s.writeConfigError(w, err)
		return
	}
	audit.Set(ctx, "stream.forward.update", st.Name, map[string]any{"provider": f.Provider, "label": f.Label, "enabled": *req.Enabled})
	s.writeForward(w, r, st, f.ID, http.StatusOK)
}

func (s *Server) forwardDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, f, ok := s.forwardFor(w, r)
	if !ok {
		return
	}
	s.forwardMu.Lock()
	defer s.forwardMu.Unlock()
	// Off first, so the sync still knows the destination to take out of mediamtx.yml; then gone.
	if f.Enabled {
		if err := s.d.Store.SetForwardEnabled(ctx, f.ID, false); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "Cannot delete the forward.")
			return
		}
		if err := s.syncForwards(ctx, st, s.author(r), fmt.Sprintf("stream %s forwarding to %s removed", st.Name, f.Label)); err != nil {
			_ = s.d.Store.SetForwardEnabled(context.WithoutCancel(ctx), f.ID, true)
			s.writeConfigError(w, err)
			return
		}
	}
	if err := s.d.Store.DeleteForward(ctx, f.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot delete the forward.")
		return
	}
	audit.Set(ctx, "stream.forward.delete", st.Name, map[string]any{"provider": f.Provider, "label": f.Label})
	w.WriteHeader(http.StatusNoContent)
}
