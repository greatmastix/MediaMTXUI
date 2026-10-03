package app

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"go.yaml.in/yaml/v3"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/credentials"
	"mtxui/internal/live"
	"mtxui/internal/liveproxy"
	"mtxui/internal/mtxauth"
	"mtxui/internal/mtxconf"
	"mtxui/internal/pathname"
	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

// Streams: a MediaMTX path as a page for the person who streams to it, with its keys. Admins
// create and delete streams and pick owners; the owner, operators and admins manage one (keys, viewer cap, target,
// disconnect); viewers may look. A streamer reaches only the streams it owns, here and in the event stream.

// owners caches which user owns which stream's path: the event stream asks on every event.
type owners struct {
	reload sync.Mutex // one reload at a time, from its read to its swap
	mu     sync.RWMutex
	byPath map[string]int64
	public map[string]bool // public streams' paths: anyone may watch
	loaded bool
}

func (s *Server) loadOwners(ctx context.Context) error {
	s.owners.mu.RLock()
	loaded := s.owners.loaded
	s.owners.mu.RUnlock()
	if loaded {
		return nil
	}
	return s.reloadOwners(ctx)
}

// reloadOwners rereads ownership after any change to streams or users. Reloads run one at a time: otherwise one that
// read the streams just before a change could swap its maps in after the reload that followed the change, and the
// cache would keep a stream public that was made private, or a removed owner, until some later change.
func (s *Server) reloadOwners(ctx context.Context) error {
	s.owners.reload.Lock()
	defer s.owners.reload.Unlock()
	list, err := s.d.Store.Streams(ctx)
	if err != nil {
		return err
	}
	m, pub := map[string]int64{}, map[string]bool{}
	for _, st := range list {
		if st.OwnerID != nil {
			m[st.Name] = *st.OwnerID
		}
		if st.Public {
			pub[st.Name] = true
		}
	}
	s.owners.mu.Lock()
	s.owners.byPath, s.owners.public, s.owners.loaded = m, pub, true
	s.owners.mu.Unlock()
	return nil
}

// ownsPath reports whether user uid owns the stream on path (loadOwners first).
func (s *Server) ownsPath(uid int64, path string) bool {
	s.owners.mu.RLock()
	defer s.owners.mu.RUnlock()
	owner, ok := s.owners.byPath[path]
	return ok && owner == uid
}

// IsPublicPath reports whether path is a public stream, for /internal/auth (mtxauth.Handler.Public).
func (s *Server) IsPublicPath(path string) bool {
	if s.loadOwners(context.Background()) != nil {
		return false
	}
	s.owners.mu.RLock()
	defer s.owners.mu.RUnlock()
	return s.owners.public[path]
}

// mayWatch reports whether the signed-in user may watch path: viewers and up watch anything, streamers their own.
func (s *Server) mayWatch(r *http.Request, path string) bool {
	cur, ok := current(r.Context())
	return ok && s.mayWatchAs(r.Context(), cur.user, path) // the access loop asks the same of open live views
}

// StreamOwner names a stream's owner.
type StreamOwner struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// StreamKeyInfo describes a key without its secret.
type StreamKeyInfo struct {
	Name       string  `json:"name"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt"`
}

// Ingest is what a stream page needs to build addresses: the host clients connect to and the protocols that are on.
type Ingest struct {
	Host   string `json:"host"`
	RTSP   int    `json:"rtsp,omitempty"` // port, when on
	RTMP   int    `json:"rtmp,omitempty"`
	SRT    int    `json:"srt,omitempty"`
	HLS    bool   `json:"hls"`
	WebRTC bool   `json:"webrtc"`
}

// StreamClips says which versions of its own holding clip a stream has.
type StreamClips struct {
	AAC  bool `json:"aac"`  // for RTMP and SRT encoders
	Opus bool `json:"opus"` // for WHIP encoders
}

// StreamInfo is a stream as the UI shows it.
type StreamInfo struct {
	ID         int64                     `json:"id"`
	Name       string                    `json:"name"`
	Title      string                    `json:"title"`
	Target     string                    `json:"target"`
	Public     bool                      `json:"public"`     // anyone may watch without a key
	MaxReaders int                       `json:"maxReaders"` // 0: no cap
	Record     bool                      `json:"record"`     // MediaMTX records the path
	Owner      *StreamOwner              `json:"owner"`
	Keys       map[string]*StreamKeyInfo `json:"keys"`
	CreatedAt  string                    `json:"createdAt"`
	CreatedBy  string                    `json:"createdBy"`
	CanManage  bool                      `json:"canManage"`
	CanAdmin   bool                      `json:"canAdmin"`
	Ingest     Ingest                    `json:"ingest"`
	Audio      string                    `json:"audio"`   // what the encoder sends: aac or opus
	Holding    string                    `json:"holding"` // "", builtin or file
	Clips      StreamClips               `json:"clips"`   // the own holding clip's versions
	Format     string                    `json:"format"`  // the stream's video format, which holding clips are in
}

var (
	targetPattern = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)
	keyNameB32    = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// mtxConfig is the part of mediamtx.yml the stream pages read.
type mtxConfig struct {
	global map[string]any
	paths  map[string]map[string]any
}

func (s *Server) readMTXConfig() mtxConfig {
	out := mtxConfig{global: map[string]any{}, paths: map[string]map[string]any{}}
	b, _, err := s.d.Config.Current()
	if err != nil {
		return out
	}
	var doc map[string]any
	if yaml.Unmarshal(b, &doc) != nil {
		return out
	}
	out.global = doc
	if ps, ok := doc["paths"].(map[string]any); ok {
		for k, v := range ps {
			if m, ok := v.(map[string]any); ok {
				out.paths[k] = m
			} else {
				out.paths[k] = map[string]any{}
			}
		}
	}
	return out
}

// on reports a protocol switch as MediaMTX reads it: on unless the file says no.
func (c mtxConfig) on(key string) bool {
	v, ok := c.global[key]
	if !ok || v == nil {
		return true
	}
	switch b := v.(type) {
	case bool:
		return b
	case string: // YAML 1.2 reads MediaMTX's yes and no as strings
		return b != "no" && b != "false" && b != "off"
	}
	return true
}

func (c mtxConfig) port(key string, def int) int {
	if s, ok := c.global[key].(string); ok {
		if i := strings.LastIndexByte(s, ':'); i >= 0 {
			if n, err := strconv.Atoi(s[i+1:]); err == nil {
				return n
			}
		}
	}
	return def
}

func (c mtxConfig) ingest(host string) Ingest {
	in := Ingest{Host: host, HLS: c.on("hls"), WebRTC: c.on("webrtc")}
	if c.on("rtsp") {
		in.RTSP = c.port("rtspAddress", 8554)
	}
	if c.on("rtmp") {
		in.RTMP = c.port("rtmpAddress", 1935)
	}
	if c.on("srt") {
		in.SRT = c.port("srtAddress", 8890)
	}
	return in
}

func (s *Server) streamInfo(ctx context.Context, st store.Stream, cur *currentSession, cfg mtxConfig) StreamInfo {
	info := StreamInfo{
		ID: st.ID, Name: st.Name, Title: st.Title, Target: st.Target, Public: st.Public, CreatedBy: st.CreatedBy,
		CreatedAt: st.CreatedAt.UTC().Format(timeFormat), Keys: map[string]*StreamKeyInfo{},
		CanManage: s.mayManage(cur, st), CanAdmin: auth.Role(cur.user.Role).AtLeast(auth.RoleAdmin),
		Ingest: cfg.ingest(s.d.Settings.PublicHost), Audio: st.Audio, Holding: st.Holding,
		Clips:  StreamClips{AAC: st.ClipAAC != "", Opus: st.ClipOpus != ""},
		Format: st.Format,
	}
	if info.Format == "" {
		info.Format = "1080p50"
	}
	if info.Audio == "" {
		info.Audio = audioAAC
	}
	if v, ok := cfg.paths[st.Name]["record"]; ok {
		info.Record = yes(v)
	} else if pd, ok := cfg.global["pathDefaults"].(map[string]any); ok {
		info.Record = yes(pd["record"])
	}
	if n, ok := cfg.paths[st.Name]["maxReaders"].(int); ok {
		info.MaxReaders = n
	}
	if st.OwnerID != nil {
		if u, err := s.d.Store.UserByID(ctx, *st.OwnerID); err == nil {
			info.Owner = &StreamOwner{u.ID, u.Username}
		}
	}
	creds, _ := s.d.Store.ListCredentials(ctx)
	for kind, id := range map[string]*int64{store.KeyPublish: st.PublishKeyID, store.KeyPlayback: st.PlaybackKeyID} {
		if id == nil {
			continue
		}
		for _, c := range creds {
			if c.ID == *id {
				k := &StreamKeyInfo{Name: c.Name, CreatedAt: c.CreatedAt.UTC().Format(timeFormat)}
				if c.LastUsedAt != nil {
					t := c.LastUsedAt.UTC().Format(timeFormat)
					k.LastUsedAt = &t
				}
				info.Keys[kind] = k
			}
		}
	}
	return info
}

const timeFormat = "2006-01-02T15:04:05Z07:00"

// mayManage: the owner, operators and admins.
func (s *Server) mayManage(cur *currentSession, st store.Stream) bool {
	return auth.Role(cur.user.Role).AtLeast(auth.RoleOperator) || (st.OwnerID != nil && *st.OwnerID == cur.user.ID)
}

// streamFor loads the stream in the URL and checks access: manage for changes, otherwise read (viewers and up, or
// the owner). A stream the user may not see answers 404, like one that does not exist.
func (s *Server) streamFor(w http.ResponseWriter, r *http.Request, manage bool) (store.Stream, *currentSession, bool) {
	cur, _ := current(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return store.Stream{}, nil, false
	}
	st, err := s.d.Store.StreamByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return store.Stream{}, nil, false
	}
	owner := st.OwnerID != nil && *st.OwnerID == cur.user.ID
	role := auth.Role(cur.user.Role)
	if !owner && !role.AtLeast(auth.RoleViewer) {
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return store.Stream{}, nil, false
	}
	if manage && !s.mayManage(cur, st) {
		writeError(w, http.StatusForbidden, "forbidden", "Only the stream's owner, operators and admins can change it.")
		return store.Stream{}, nil, false
	}
	return st, cur, true
}

func (s *Server) streamsList(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	list, err := s.d.Store.Streams(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the streams.")
		return
	}
	cfg := s.readMTXConfig()
	out := []StreamInfo{}
	for _, st := range list {
		mine := st.OwnerID != nil && *st.OwnerID == cur.user.ID
		if mine || auth.Role(cur.user.Role).AtLeast(auth.RoleViewer) {
			out = append(out, s.streamInfo(r.Context(), st, cur, cfg))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) streamGet(w http.ResponseWriter, r *http.Request) {
	st, cur, ok := s.streamFor(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.streamInfo(r.Context(), st, cur, s.readMTXConfig()))
}

type newStream struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	OwnerID *int64 `json:"ownerId"`
	Target  string `json:"target"`
	Public  *bool  `json:"public"` // default true
}

func validStreamName(name string) error {
	if err := pathname.Valid(name); err != nil {
		return err
	}
	if strings.HasPrefix(name, "~") || name == "all" || name == "all_others" {
		return errors.New("a stream needs a plain path name, not a pattern")
	}
	return nil
}

func validTitle(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" || utf8.RuneCountInString(t) > 80 || strings.ContainsAny(t, "\x00\n\r") {
		return "", errors.New("a title is 1 to 80 characters on one line")
	}
	return t, nil
}

func (s *Server) streamCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req newStream
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validStreamName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid stream name: "+err.Error())
		return
	}
	if req.Title == "" {
		req.Title = req.Name
	}
	title, err := validTitle(req.Title)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if !targetPattern.MatchString(req.Target) {
		writeError(w, http.StatusBadRequest, "invalid", "Unknown target.")
		return
	}
	if req.OwnerID != nil {
		if _, err := s.d.Store.UserByID(ctx, *req.OwnerID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "No such user for the owner.")
			return
		}
	}
	public := req.Public == nil || *req.Public
	st, err := s.d.Store.CreateStream(ctx, store.Stream{
		Name: req.Name, Title: title, OwnerID: req.OwnerID,
		Target: req.Target, Public: public, CreatedBy: s.author(r),
	})
	if errors.Is(err, store.ErrExists) {
		writeError(w, http.StatusConflict, "exists", "A stream for this path already exists.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot create the stream.")
		return
	}
	// The path goes into mediamtx.yml unless it is there already (an existing path keeps its settings).
	if _, exists := s.readMTXConfig().paths[st.Name]; !exists {
		_, err := s.d.Config.Edit(ctx, s.author(r), "stream "+st.Name+" created", s.guardEdit, func(d *yamledit.Doc) error {
			return d.Set([]string{"paths", st.Name}, map[string]any{})
		})
		if err != nil {
			_ = s.d.Store.DeleteStream(context.WithoutCancel(ctx), st.ID)
			s.writeConfigError(w, err)
			return
		}
	}
	for _, kind := range []string{store.KeyPublish, store.KeyPlayback} {
		if _, _, err := s.newStreamKey(ctx, st, kind, s.author(r)); err != nil {
			s.d.Log.Error("stream key", "stream", st.Name, "kind", kind, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "The stream was created, but a key could not be made. Regenerate it.")
			return
		}
	}
	_ = s.reloadOwners(ctx)
	audit.Set(ctx, "stream.create", st.Name, map[string]any{"title": title, "ownerId": req.OwnerID})
	st, _ = s.d.Store.StreamByID(ctx, st.ID)
	cur, _ := current(ctx)
	writeJSON(w, http.StatusCreated, s.streamInfo(ctx, st, cur, s.readMTXConfig()))
}

// newStreamKey makes a key of a kind for st, seals its secret and makes it the stream's key. The key it replaced (the
// one the stream held at that moment, not when st was read: of concurrent regenerations each replaces another) is
// retired; it returns the new key and how many sessions the old one had open. A key that cannot be recorded (the
// stream is gone) is revoked again, so no key stays valid without a stream to show it.
func (s *Server) newStreamKey(ctx context.Context, st store.Stream, kind, by string) (StreamKey, int, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return StreamKey{}, 0, err
	}
	prefix, actions := "key-", []string{"publish"}
	if kind == store.KeyPlayback {
		prefix, actions = "view-", []string{"read"}
	}
	name := prefix + strings.ToLower(keyNameB32.EncodeToString(b))
	secret, c, err := s.d.Creds.Add(ctx, credentials.Spec{
		Name: name, Kind: credentials.KindPassword, Actions: actions, Paths: []string{st.Name}, CreatedBy: by,
	})
	if err != nil {
		return StreamKey{}, 0, err
	}
	sealed, err := s.d.Creds.Seal(secret, sealContext(st.ID, kind))
	var old *int64
	if err == nil {
		old, err = s.d.Store.ReplaceStreamKey(ctx, st.ID, kind, c.ID, sealed)
	}
	if err != nil {
		_ = s.d.Creds.Revoke(context.WithoutCancel(ctx), name)
		return StreamKey{}, 0, err
	}
	kicked := 0
	if old != nil {
		kicked = s.retireKey(ctx, *old)
	}
	return StreamKey{Kind: kind, Name: name, Secret: secret}, kicked, nil
}

func sealContext(id int64, kind string) string { return fmt.Sprintf("stream %d %s", id, kind) }

// keyOf returns the stream's current key of a kind, or false.
func keyOf(st store.Stream, kind string) (*int64, string, bool) {
	switch kind {
	case store.KeyPublish:
		return st.PublishKeyID, st.PublishKeyEnc, true
	case store.KeyPlayback:
		return st.PlaybackKeyID, st.PlaybackKeyEnc, true
	}
	return nil, "", false
}

// StreamKey is a key with its secret, for the stream page.
type StreamKey struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

func (s *Server) streamKeyReveal(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	kind := chi.URLParam(r, "kind")
	id, sealed, known := keyOf(st, kind)
	if !known {
		writeError(w, http.StatusNotFound, "not_found", "No such key.")
		return
	}
	if id == nil || sealed == "" {
		writeError(w, http.StatusNotFound, "no_key", "This stream has no such key. Regenerate it.")
		return
	}
	secret, err := s.d.Creds.Unseal(sealed, sealContext(st.ID, kind))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The key cannot be read back. Regenerate it.")
		return
	}
	name := s.credentialName(r.Context(), *id)
	audit.Set(r.Context(), "stream.key.reveal", st.Name, map[string]any{"kind": kind, "key": name})
	writeJSON(w, http.StatusOK, StreamKey{Kind: kind, Name: name, Secret: secret})
}

func (s *Server) credentialName(ctx context.Context, id int64) string {
	list, _ := s.d.Store.ListCredentials(ctx)
	for _, c := range list {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// streamKeyRegenerate replaces a key: the new one works at once, the old one is revoked and what it opened is closed.
func (s *Server) streamKeyRegenerate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	kind := chi.URLParam(r, "kind")
	if _, _, known := keyOf(st, kind); !known {
		writeError(w, http.StatusNotFound, "not_found", "No such key.")
		return
	}
	key, kicked, err := s.newStreamKey(ctx, st, kind, s.author(r))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", "A new key could not be made.")
		return
	}
	audit.Set(ctx, "stream.key.regenerate", st.Name, map[string]any{"kind": kind, "key": key.Name, "kicked": kicked})
	writeJSON(w, http.StatusOK, key)
}

// retireKey revokes a stream key's credential and closes the sessions it opened; it returns how many were closed.
func (s *Server) retireKey(ctx context.Context, credID int64) int {
	name := s.credentialName(ctx, credID)
	if name == "" {
		return 0
	}
	if err := s.d.Creds.Revoke(ctx, name); err != nil {
		s.d.Log.Error("revoking a stream key", "key", name, "err", err)
		return 0
	}
	refs := s.d.Opened.SessionsOf(credID)
	for _, cl := range s.d.Live.ClientsOf(name) {
		refs = append(refs, mtxauth.SessionRef{Protocol: cl.Protocol, ID: cl.ID})
	}
	return s.kick(ctx, refs)
}

type streamPatch struct {
	Title      *string `json:"title"`
	Public     *bool   `json:"public"`
	Target     *string `json:"target"`
	MaxReaders *int    `json:"maxReaders"`
	Record     *bool   `json:"record"`  // MediaMTX records the path
	OwnerID    *int64  `json:"ownerId"` // admins only; 0 clears the owner
	Holding    *string `json:"holding"` // "", builtin or file
	Audio      *string `json:"audio"`   // aac or opus
	Format     *string `json:"format"`  // 720p50, 720p60, 1080p50 or 1080p60
}

func (s *Server) streamPatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, cur, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	// The entry names the action and target at once, and each part below adds its change once that has landed, so a
	// part that fails after another went through still leaves an entry saying what did.
	audit.Set(ctx, "stream.update", st.Name, nil)
	var req streamPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	// Everything is checked before anything changes.
	p, details := store.StreamPatch{Public: req.Public, Target: req.Target, OwnerID: req.OwnerID}, map[string]any{}
	if req.Title != nil {
		t, err := validTitle(*req.Title)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		p.Title, details["title"] = &t, t
	}
	if req.Public != nil {
		details["public"] = *req.Public
	}
	if req.Target != nil {
		if !targetPattern.MatchString(*req.Target) {
			writeError(w, http.StatusBadRequest, "invalid", "Unknown target.")
			return
		}
		details["target"] = *req.Target
	}
	if req.OwnerID != nil {
		if !auth.Role(cur.user.Role).AtLeast(auth.RoleAdmin) {
			writeError(w, http.StatusForbidden, "forbidden", "Only admins choose a stream's owner.")
			return
		}
		if *req.OwnerID != 0 {
			if _, err := s.d.Store.UserByID(ctx, *req.OwnerID); err != nil {
				writeError(w, http.StatusBadRequest, "invalid", "No such user for the owner.")
				return
			}
		}
		details["ownerId"] = *req.OwnerID
	}
	if req.MaxReaders != nil && (*req.MaxReaders < 0 || *req.MaxReaders > 100000) {
		writeError(w, http.StatusBadRequest, "invalid", "The viewer cap is 0 (none) to 100000.")
		return
	}
	if _, _, err := holdingPatch(st, req.Holding, req.Audio, req.Format); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}

	if req.MaxReaders != nil {
		n := *req.MaxReaders
		// The config changes only when the cap does: the form always sends it, and "no cap" is no key at all.
		pathConf, configured := s.readMTXConfig().paths[st.Name]
		cur, _ := pathConf["maxReaders"].(int)
		if n != cur {
			_, err := s.d.Config.Edit(ctx, s.author(r), fmt.Sprintf("stream %s viewer cap %d", st.Name, n), s.guardEdit,
				func(d *yamledit.Doc) error {
					switch {
					case n == 0:
						return d.Delete([]string{"paths", st.Name, "maxReaders"})
					case !configured: // the path left mediamtx.yml (edited by hand): put it back with the cap
						return d.Set([]string{"paths", st.Name}, map[string]any{"maxReaders": n})
					}
					return d.Set([]string{"paths", st.Name, "maxReaders"}, n)
				})
			if err != nil && !errors.Is(err, mtxconf.ErrUnchanged) {
				s.writeConfigError(w, err)
				return
			}
			audit.Set(ctx, "", "", map[string]any{"maxReaders": n})
		}
	}
	if req.Record != nil {
		_, configured := s.readMTXConfig().paths[st.Name]
		what := map[bool]string{true: "on", false: "off"}[*req.Record]
		_, err := s.d.Config.Edit(ctx, s.author(r), "stream "+st.Name+" recording "+what, s.guardEdit, func(d *yamledit.Doc) error {
			if !configured {
				return d.Set([]string{"paths", st.Name}, map[string]any{"record": *req.Record})
			}
			return d.Set([]string{"paths", st.Name, "record"}, *req.Record)
		})
		if err != nil && !errors.Is(err, mtxconf.ErrUnchanged) {
			s.writeConfigError(w, err)
			return
		}
		audit.Set(ctx, "", "", map[string]any{"record": *req.Record})
	}
	if (req.Holding != nil || req.Audio != nil || req.Format != nil) && !s.patchHolding(w, r, st.ID, req.Holding, req.Audio, req.Format) {
		return
	}
	// Only the columns the request names are written: the whole row from the copy read above would undo what another
	// request changed meanwhile (an admin's new owner, a clip uploaded since).
	if err := s.d.Store.PatchStream(ctx, st.ID, p); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot save the stream.")
		return
	}
	audit.Set(ctx, "", "", details)
	_ = s.reloadOwners(ctx)
	st, err := s.d.Store.StreamByID(ctx, st.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return
	}
	writeJSON(w, http.StatusOK, s.streamInfo(ctx, st, cur, s.readMTXConfig()))
}

func (s *Server) streamDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	kicked := 0
	for _, id := range []*int64{st.PublishKeyID, st.PlaybackKeyID} {
		if id != nil {
			kicked += s.retireKey(ctx, *id)
		}
	}
	kicked += s.retireGuestKeys(ctx, st.ID)
	// Under the holding lock, so no clip upload or holding change lands between the stream's last read and its
	// removal: one would put the path back into mediamtx.yml or leave a clip nobody references.
	s.holdingMu.Lock()
	defer s.holdingMu.Unlock()
	last, err := s.d.Store.StreamByID(ctx, st.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such stream.")
		return
	}
	if _, exists := s.readMTXConfig().paths[st.Name]; exists {
		_, err := s.d.Config.Edit(ctx, s.author(r), "stream "+st.Name+" deleted", s.guardEdit, func(d *yamledit.Doc) error {
			return d.Delete([]string{"paths", st.Name})
		})
		if err != nil && !errors.Is(err, mtxconf.ErrUnchanged) {
			s.writeConfigError(w, err)
			return
		}
	}
	// The record goes with every valid key it still points to, including one regenerated or made for a guest after
	// the stream was read above, so none stays valid for a path the next stream of that name gets.
	late, err := s.d.Store.DeleteStreamKeys(ctx, st.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot delete the stream.")
		return
	}
	for _, id := range late {
		kicked += s.retireKey(ctx, id)
	}
	s.removeClip(last.ClipAAC)
	s.removeClip(last.ClipOpus)
	_ = s.reloadOwners(ctx)
	audit.Set(ctx, "stream.delete", st.Name, map[string]any{"kicked": kicked})
	w.WriteHeader(http.StatusNoContent)
}

// sourceProtocol maps MediaMTX's path source types to the protocols of its kick operations.
var sourceProtocol = map[string]string{
	"rtspSession": "rtsp", "rtspsSession": "rtsps", "rtmpConn": "rtmp", "rtmpsConn": "rtmps", "srtConn": "srt",
	"webRTCSession": "webrtc", "moqSession": "moq",
}

// streamDisconnect closes the connection publishing to the stream. Its key still works: to keep it out, regenerate.
func (s *Server) streamDisconnect(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	var p struct {
		Source *struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"source"`
	}
	raw := s.d.Live.Path(st.Name)
	if raw == nil || json.Unmarshal(raw, &p) != nil || p.Source == nil {
		writeError(w, http.StatusConflict, "offline", "Nothing is publishing to this stream.")
		return
	}
	proto, ok := sourceProtocol[p.Source.Type]
	if !ok {
		writeError(w, http.StatusConflict, "not_a_client", "This stream's source is not a client connection and cannot be disconnected here.")
		return
	}
	kicked := s.kick(r.Context(), []mtxauth.SessionRef{{Protocol: proto, ID: p.Source.ID}})
	audit.Set(r.Context(), "stream.disconnect", st.Name, map[string]any{"protocol": proto, "kicked": kicked})
	writeJSON(w, http.StatusOK, map[string]int{"kicked": kicked})
}

func (s *Server) streamHistory(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.streamFor(w, r, false)
	if !ok {
		return
	}
	span, bucket, ok := historyRange(w, r)
	if !ok {
		return
	}
	if bucket == 0 {
		writeJSON(w, http.StatusOK, s.d.Live.PathHistory(st.Name))
		return
	}
	now := time.Now()
	points, err := s.d.Store.PathHistory(r.Context(), st.Name, now.Add(-span), now, bucket)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the history.")
		return
	}
	out := make([]live.PathSample, 0, len(points))
	for _, p := range points {
		out = append(out, live.PathSample{T: p.T.UnixMilli(), InBps: p.InBps, OutBps: p.OutBps, Readers: p.Readers})
	}
	writeJSON(w, http.StatusOK, out)
}

// PublicStream is what the watch link's page learns about a public stream.
type PublicStream struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Live  bool   `json:"live"`
	// Available: something plays: the stream, or its holding screen while nobody streams.
	Available bool `json:"available"`
}

// publicStreamName reads the splat and returns a public stream's record, or answers 404 (also for private ones).
func (s *Server) publicStream(w http.ResponseWriter, r *http.Request, name string) (store.Stream, bool) {
	if validStreamName(name) == nil && s.IsPublicPath(name) {
		list, _ := s.d.Store.Streams(r.Context())
		for _, st := range list {
			if st.Name == name && st.Public {
				return st, true
			}
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "No such public stream.")
	return store.Stream{}, false
}

// publicStreamGet answers the watch link's page, for anyone.
func (s *Server) publicStreamGet(w http.ResponseWriter, r *http.Request) {
	st, ok := s.publicStream(w, r, chi.URLParam(r, "*"))
	if !ok {
		return
	}
	var p struct {
		Online    bool `json:"online"`
		Available bool `json:"available"`
	}
	if raw := s.d.Live.Path(st.Name); raw != nil {
		_ = json.Unmarshal(raw, &p)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, PublicStream{Name: st.Name, Title: st.Title, Live: p.Online, Available: p.Available})
}

// publicHLS proxies a public stream's HLS for anyone (the watch link's fallback when WebRTC does not connect).
func (s *Server) publicHLS(w http.ResponseWriter, r *http.Request) {
	path, file, err := liveproxy.SplitHLS(chi.URLParam(r, "*"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid path or file.")
		return
	}
	if _, ok := s.publicStream(w, r, path); !ok {
		return
	}
	ip := clientip.From(r.Context()).IP
	s.d.Watch.HLS(w, r, liveproxy.Viewer{Session: "public:" + ip.String(), User: "public", Client: ip}, path, file)
}
