package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"

	"mtxui/internal/audit"
	"mtxui/internal/mtxconf"
	"mtxui/internal/mtxlog"
	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

// Holding screens: what a stream shows while nobody streams to it, through MediaMTX's alwaysAvailable. The stream stays
// available with a clip on repeat (the offline screen, one of the sidecar's own clips in the stream's format and audio,
// or an MP4 the manager uploads), viewers and players keep their connection, and the encoder takes over when it
// connects. MediaMTX accepts an encoder only if its tracks
// match the clip's (codecs, order, and for AAC its configuration), so each stream says which audio its encoder sends:
// AAC (RTMP and SRT: video, then audio) or Opus (WHIP from OBS: audio, then video). Uploads are checked with the MP4
// reader MediaMTX itself uses and written back in that order; the browser transcodes what does not fit. Forwards run
// while the stream is available, so platforms show the holding clip too (the user's choice, 2026-10-01). Recording
// skips the holding clip (alwaysAvailableRecorded: no).

// maxHoldingBytes caps an uploaded clip.
const maxHoldingBytes = 64 << 20

// Audio codecs a stream's encoder may send.
const (
	audioAAC  = "aac"
	audioOpus = "opus"
)

// videoFormat is a stream's video format: holding clips are made in it, so the hand-over to the encoder changes
// neither size nor rate (players such as VRChat's stutter on a change).
type videoFormat struct {
	width, height int
	fps           float64
}

var videoFormats = map[string]videoFormat{
	"720p50": {1280, 720, 50}, "720p60": {1280, 720, 60}, "1080p50": {1920, 1080, 50}, "1080p60": {1920, 1080, 60},
}

func (f videoFormat) String() string {
	return fmt.Sprintf("%d×%d at %g fps", f.width, f.height, f.fps)
}

// clipOf returns the stream's own clip for encoders that send audio, or "".
func clipOf(st store.Stream, audio string) string {
	if audio == audioOpus {
		return st.ClipOpus
	}
	return st.ClipAAC
}

func setClip(st *store.Stream, audio, name string) {
	if audio == audioOpus {
		st.ClipOpus = name
	} else {
		st.ClipAAC = name
	}
}

// audioName names an audio choice by the encoders that send it.
func audioName(audio string) string {
	if audio == audioOpus {
		return "WHIP (Opus)"
	}
	return "RTMP/SRT (AAC)"
}

// holdingNeeds says in words what a clip for that audio must contain.
func holdingNeeds(audio string) string {
	if audio == audioOpus {
		return "H.264 video and Opus stereo audio"
	}
	return "H.264 video and AAC-LC audio at 48 kHz, stereo"
}

// fitClip checks a clip for a stream whose encoder sends audio in format, and puts its tracks in the order that
// encoder sends them. The error says what does not fit, for the person who uploaded it.
func fitClip(p *pmp4.Presentation, audio, format string) error {
	var video, sound *pmp4.Track
	for _, t := range p.Tracks {
		switch c := t.Codec.(type) {
		case *codecs.H264:
			if video != nil {
				return userError("The clip has more than one video track.")
			}
			video = t
		case *codecs.MPEG4Audio:
			if sound != nil {
				return userError("The clip has more than one audio track.")
			}
			if audio != audioAAC {
				return userErrorf("The clip has AAC audio, but this stream's encoder sends Opus: it needs %s.", holdingNeeds(audio))
			}
			if c.Config.Type != mpeg4audio.ObjectTypeAACLC || c.Config.SampleRate != 48000 || c.Config.ChannelConfig != 2 {
				return userErrorf("The clip's AAC audio is not AAC-LC at 48 kHz, stereo (it is %d Hz, %d channels), which is what encoders such as OBS send.",
					c.Config.SampleRate, c.Config.ChannelConfig)
			}
			sound = t
		case *codecs.Opus:
			if sound != nil {
				return userError("The clip has more than one audio track.")
			}
			if audio != audioOpus {
				return userErrorf("The clip has Opus audio, but this stream's encoder sends AAC: it needs %s.", holdingNeeds(audio))
			}
			if c.ChannelCount != 2 {
				return userError("The clip's Opus audio is not stereo.")
			}
			sound = t
		default:
			return userErrorf("The clip has a %s track; it needs %s.", codecName(t.Codec), holdingNeeds(audio))
		}
	}
	switch {
	case video == nil:
		return userErrorf("The clip has no H.264 video; it needs %s.", holdingNeeds(audio))
	case sound == nil:
		return userErrorf("The clip has no audio (silence counts); it needs %s.", holdingNeeds(audio))
	case len(video.Samples) == 0 || len(sound.Samples) == 0:
		return userError("The clip is empty.")
	}
	for _, s := range video.Samples {
		if s.PTSOffset != 0 {
			return userError("The clip's video has B-frames, which WebRTC players (browsers, the watch link) cannot play. Transcode it here.")
		}
	}
	if want, ok := videoFormats[format]; ok {
		got, err := clipFormat(video)
		if err != nil {
			return err
		}
		if got.width != want.width || got.height != want.height || got.fps < want.fps-0.5 || got.fps > want.fps+0.5 {
			return userErrorf("The clip is %s; this stream's format is %s.", got, want)
		}
	}
	p.Tracks = []*pmp4.Track{video, sound}
	if audio == audioOpus {
		p.Tracks = []*pmp4.Track{sound, video}
	}
	return nil
}

// clipFormat reads a video track's size from its SPS and its rate from its sample durations.
func clipFormat(t *pmp4.Track) (videoFormat, error) {
	var sps h264.SPS
	if err := sps.Unmarshal(t.Codec.(*codecs.H264).SPS); err != nil {
		return videoFormat{}, userError("The clip's H.264 parameters cannot be read.")
	}
	var total uint64
	for _, s := range t.Samples {
		total += uint64(s.Duration)
	}
	if total == 0 || t.TimeScale == 0 {
		return videoFormat{}, userError("The clip's frames have no duration.")
	}
	fps := float64(len(t.Samples)) * float64(t.TimeScale) / float64(total)
	return videoFormat{sps.Width(), sps.Height(), float64(int(fps*100+0.5)) / 100}, nil
}

func codecName(c codecs.Codec) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", c), "*codecs.")
}

// storeClip reads an uploaded clip, fits it to the stream's audio and writes it into the holding directory under a
// name of its own (so a new clip is a new file, and MediaMTX reloads it). It returns the file name.
func (s *Server) storeClip(streamID int64, audio, format string, body io.Reader) (string, error) {
	dir := s.d.Settings.HoldingDir
	up, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(up.Name())
	defer up.Close()
	n, err := io.Copy(up, io.LimitReader(body, maxHoldingBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxHoldingBytes {
		return "", userErrorf("The clip is larger than %d MB.", maxHoldingBytes>>20)
	}
	if _, err := up.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	var p pmp4.Presentation
	if err := p.Unmarshal(up); err != nil {
		return "", userError("The file is not an MP4 MediaMTX can read (fragmented MP4s are not). Transcode it.")
	}
	if err := fitClip(&p, audio, format); err != nil {
		return "", err
	}
	out, err := os.CreateTemp(dir, ".clip-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(out.Name())
	h := sha256.New()
	if err := p.Marshal(io.MultiWriter(out, h)); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%d-%s.mp4", streamID, hex.EncodeToString(h.Sum(nil))[:12])
	return name, os.Rename(out.Name(), filepath.Join(dir, name))
}

// removeClip deletes a stream's old holding clip; a file name never leaves the holding directory.
func (s *Server) removeClip(name string) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return
	}
	root, err := os.OpenRoot(s.d.Settings.HoldingDir) // confines the name to the directory
	if err == nil {
		err = root.Remove(name)
		_ = root.Close()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.d.Log.Warn("removing a holding clip", "file", name, "err", err)
	}
}

// syncHolding writes a stream's holding screen into its path config.
func (s *Server) syncHolding(ctx context.Context, st store.Stream, author, reason string) error {
	keys := []string{"alwaysAvailable", "alwaysAvailableTracks", "alwaysAvailableFile", "alwaysAvailableRecorded"}
	set := map[string]any{}
	switch st.Holding {
	case "builtin":
		set = map[string]any{
			"alwaysAvailable": true, "alwaysAvailableFile": filepath.Join(s.d.Settings.HoldingDir, offlineClip(st.Format, st.Audio)),
			"alwaysAvailableRecorded": false,
		}
	case "file":
		set = map[string]any{
			"alwaysAvailable": true, "alwaysAvailableFile": filepath.Join(s.d.Settings.HoldingDir, clipOf(st, st.Audio)),
			"alwaysAvailableRecorded": false,
		}
	}
	_, configured := s.readMTXConfig().paths[st.Name]
	_, err := s.d.Config.Edit(ctx, author, reason, s.guardEdit, func(d *yamledit.Doc) error {
		if !configured {
			if len(set) == 0 {
				return nil
			}
			return d.Set([]string{"paths", st.Name}, set)
		}
		for _, k := range keys {
			if v, ok := set[k]; ok {
				if err := d.Set([]string{"paths", st.Name, k}, v); err != nil {
					return err
				}
			} else if err := d.Delete([]string{"paths", st.Name, k}); err != nil && !errors.Is(err, yamledit.ErrNotFound) {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, mtxconf.ErrUnchanged) {
		return nil
	}
	return err
}

// holdingUpload takes one version of a stream's own holding clip (the body is the MP4): the query names the audio it is
// for and its format (the stream's, by default). The page sends both versions, AAC and Opus, one after the other. A
// clip in another format replaces both versions (the old ones no longer match) and becomes the stream's format; the
// stream keeps its audio when it has a version for it. The holding screen switches to the clip.
func (s *Server) holdingUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "video/mp4" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid", "Send the clip as video/mp4.")
		return
	}
	q := r.URL.Query()
	audio, format := orDefault(q.Get("audio"), st.Audio), orDefault(q.Get("format"), st.Format)
	if _, known := videoFormats[format]; (audio != audioAAC && audio != audioOpus) || !known {
		writeError(w, http.StatusBadRequest, "invalid", "Unknown audio or format.")
		return
	}
	s.holdingMu.Lock()
	defer s.holdingMu.Unlock()
	name, err := s.storeClip(st.ID, audio, format, http.MaxBytesReader(w, r.Body, maxHoldingBytes+1))
	var ue userError
	switch {
	case errors.As(err, &ue):
		writeError(w, http.StatusUnprocessableEntity, "clip", ue.Error())
		return
	case err != nil:
		s.d.Log.Error("storing a holding clip", "stream", st.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "The clip could not be stored.")
		return
	}
	var drop []string
	if format != st.Format {
		drop = append(drop, st.ClipAAC, st.ClipOpus)
		st.ClipAAC, st.ClipOpus = "", ""
	} else {
		drop = append(drop, clipOf(st, audio))
	}
	setClip(&st, audio, name)
	st.Format, st.Holding = format, "file"
	if clipOf(st, st.Audio) == "" {
		st.Audio = audio
	}
	if err := s.syncHolding(ctx, st, s.author(r), "stream "+st.Name+" holding clip "+name); err != nil {
		s.removeClip(name)
		s.writeConfigError(w, err)
		return
	}
	if err := s.d.Store.UpdateStream(ctx, st); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot save the stream.")
		return
	}
	for _, old := range drop {
		if old != name {
			s.removeClip(old)
		}
	}
	s.auto.poke()
	audit.Set(ctx, "stream.holding.upload", st.Name, map[string]any{"file": name, "audio": audio, "format": format})
	cur, _ := current(ctx)
	writeJSON(w, http.StatusOK, s.streamInfo(ctx, st, cur, s.readMTXConfig()))
}

// holdingPatch applies the holding, audio and format fields of a stream patch: it returns the stream as changed,
// whether anything changed, and a refusal for the person. While the own clip shows, the audio changes to the clip's
// other version (when there is one), and the format only with a new clip.
func holdingPatch(st store.Stream, holding, audio, format *string) (store.Stream, bool, error) {
	changed := false
	keepsClip := st.Holding == "file" && (holding == nil || *holding == "file")
	if audio != nil && *audio != st.Audio {
		if *audio != audioAAC && *audio != audioOpus {
			return st, false, userError("The audio is aac (RTMP, SRT) or opus (WHIP).")
		}
		if keepsClip && clipOf(st, *audio) == "" {
			return st, false, userErrorf("Your holding clip has no %s version. Choose it again, or switch the holding screen off.", audioName(*audio))
		}
		st.Audio, changed = *audio, true
	}
	if format != nil && *format != st.Format {
		f, ok := videoFormats[*format]
		if !ok {
			return st, false, userError("The format is 720p50, 720p60, 1080p50 or 1080p60.")
		}
		if keepsClip {
			return st, false, userErrorf("The holding clip is in another format. Make or choose it again in %s, or switch the holding screen off.", f)
		}
		st.Format, changed = *format, true
	}
	if holding != nil && *holding != st.Holding {
		switch *holding {
		case "", "builtin":
		case "file":
			if clipOf(st, st.Audio) == "" {
				return st, false, userErrorf("Your holding clip has no %s version. Choose it again.", audioName(st.Audio))
			}
		default:
			return st, false, userError("The holding screen is off, builtin or file.")
		}
		st.Holding, changed = *holding, true
	}
	return st, changed, nil
}

// anyHolding reports whether some stream has a holding screen, so it is available to viewers while offline.
func anyHolding(streams []store.Stream) bool {
	for _, st := range streams {
		if st.Holding != "" {
			return true
		}
	}
	return false
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// streamNotes answers what MediaMTX's log said about the stream's encoder lately (refused, B-frames), newest first.
func (s *Server) streamNotes(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.streamFor(w, r, true)
	if !ok {
		return
	}
	notes := []mtxlog.Note{}
	if s.d.Notes != nil {
		notes = s.d.Notes.For(st.Name)
	}
	writeJSON(w, http.StatusOK, notes)
}

// followGap is the least time between two automatic switches of one stream's holding version.
const followGap = 10 * time.Second

// FollowEncoder is called (by the log follower) when MediaMTX refused an encoder of path for its tracks: sends is
// what it sends, as MediaMTX lists it. When its audio is the other one and the holding screen has a version with it,
// the stream switches to that version, so the encoder's automatic reconnect gets in (OBS retries every few seconds).
// It returns the note to show instead of the refusal, or nil to show the refusal.
func (s *Server) FollowEncoder(path, sends, _ string) *mtxlog.Note {
	audio := ""
	switch {
	case strings.Contains(sends, "MPEG-4 Audio"):
		audio = audioAAC
	case strings.Contains(sends, "Opus"):
		audio = audioOpus
	default:
		return nil // no audio we can match: the refusal says what to do
	}
	ctx := context.Background()
	s.holdingMu.Lock()
	defer s.holdingMu.Unlock()
	streams, err := s.d.Store.Streams(ctx)
	if err != nil {
		return nil
	}
	for _, st := range streams {
		if st.Name != path || st.Holding == "" || st.Audio == audio {
			continue
		}
		if st.Holding == "file" && clipOf(st, audio) == "" {
			return &mtxlog.Note{Kind: "tracks", Message: "MediaMTX refused your encoder: it sends " + audioName(audio) +
				" audio, and your holding clip has no " + audioName(audio) + " version. Choose the clip again, so both " +
				"versions are made, or switch the holding screen off."}
		}
		if last, ok := s.followed.Load(st.ID); ok && time.Since(last.(time.Time)) < followGap {
			return nil
		}
		s.followed.Store(st.ID, time.Now())
		st.Audio = audio
		reason := fmt.Sprintf("stream %s holding screen follows its encoder: %s", st.Name, audioName(audio))
		if err := s.syncHolding(ctx, st, "system", reason); err != nil {
			s.d.Log.Warn("switching the holding version", "stream", st.Name, "err", err)
			return nil
		}
		if err := s.d.Store.UpdateStream(ctx, st); err != nil {
			return nil
		}
		s.d.Audit.Record(ctx, store.AuditEvent{
			Actor: "system", Action: "stream.holding.follow", Target: st.Name, Details: map[string]any{"audio": audio},
		})
		return &mtxlog.Note{Kind: "switched", Message: "Your encoder sends " + audioName(audio) + " audio, so the " +
			"holding screen switched to its " + audioName(audio) + " version. MediaMTX had to refuse the first attempt; " +
			"OBS reconnects by itself within a few seconds (otherwise, start streaming again)."}
	}
	return nil
}
