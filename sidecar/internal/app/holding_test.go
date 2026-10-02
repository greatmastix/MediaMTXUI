package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"

	"mtxui/internal/mtxconf"
	"mtxui/internal/mtxtest"
)

var (
	testH264 = &codecs.H264{
		SPS: []byte{ // 1920x1080 baseline (mediacommon's test SPS)
			0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02, 0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04,
			0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9, 0x20,
		},
		PPS: []byte{0x08, 0x06, 0x07, 0x08},
	}
	testAAC = func(rate, channels int) *codecs.MPEG4Audio {
		return &codecs.MPEG4Audio{Config: mpeg4audio.AudioSpecificConfig{
			Type: mpeg4audio.ObjectTypeAACLC, SampleRate: rate, ChannelConfig: uint8(channels),
		}}
	}
	testOpus = &codecs.Opus{ChannelCount: 2}
)

// clip makes an MP4 with one short track per codec, in that order.
func clip(t *testing.T, cs ...codecs.Codec) []byte {
	t.Helper()
	var p pmp4.Presentation
	for i, c := range cs {
		scale := uint32(90000)
		if !c.IsVideo() {
			scale = 48000
		}
		tr := &pmp4.Track{ID: i + 1, TimeScale: scale, Codec: c}
		for range 3 {
			tr.Samples = append(tr.Samples, &pmp4.Sample{ // video at 60 fps, audio in 20 ms frames
				Duration: scale / 60, PayloadSize: 2, GetPayload: func() ([]byte, error) { return []byte{1, 2}, nil },
			})
		}
		p.Tracks = append(p.Tracks, tr)
	}
	var buf seekBuffer
	if err := p.Marshal(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.b
}

func tracksOf(t *testing.T, b []byte) []string {
	t.Helper()
	var p pmp4.Presentation
	if err := p.Unmarshal(bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tr := range p.Tracks {
		out = append(out, codecName(tr.Codec))
	}
	return out
}

func TestFitClip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		audio   string
		format  string
		codecs  []codecs.Codec
		want    string // tracks after fitting, or "error: <part of the message>"
		bframes bool   // a reordered frame, as B-frames make
	}{
		{"AAC for RTMP", audioAAC, "1080p60", []codecs.Codec{testH264, testAAC(48000, 2)}, "H264 MPEG4Audio", false},
		{"AAC, audio first, reordered", audioAAC, "1080p60", []codecs.Codec{testAAC(48000, 2), testH264}, "H264 MPEG4Audio", false},
		{"Opus for WHIP, reordered", audioOpus, "1080p60", []codecs.Codec{testH264, testOpus}, "Opus H264", false},
		{"AAC for an Opus stream", audioOpus, "1080p60", []codecs.Codec{testH264, testAAC(48000, 2)}, "error: sends Opus", false},
		{"Opus for an AAC stream", audioAAC, "1080p60", []codecs.Codec{testH264, testOpus}, "error: sends AAC", false},
		{"44.1 kHz", audioAAC, "1080p60", []codecs.Codec{testH264, testAAC(44100, 2)}, "error: 44100 Hz", false},
		{"mono", audioAAC, "1080p60", []codecs.Codec{testH264, testAAC(48000, 1)}, "error: 1 channels", false},
		{"no audio", audioAAC, "1080p60", []codecs.Codec{testH264}, "error: no audio", false},
		{"no video", audioOpus, "1080p60", []codecs.Codec{testOpus}, "error: no H.264", false},
		{"two videos", audioAAC, "1080p60", []codecs.Codec{testH264, testH264, testAAC(48000, 2)}, "error: more than one video", false},
		{"B-frames", audioAAC, "1080p60", []codecs.Codec{testH264, testAAC(48000, 2)}, "error: B-frames", true},
		{"another format", audioAAC, "720p50", []codecs.Codec{testH264, testAAC(48000, 2)}, "error: 1920×1080 at 60 fps; this stream's format is 1280×720 at 50 fps", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p pmp4.Presentation
			if err := p.Unmarshal(bytes.NewReader(clip(t, tc.codecs...))); err != nil {
				t.Fatal(err)
			}
			if tc.bframes {
				p.Tracks[0].Samples[1].PTSOffset = 1500
			}
			err := fitClip(&p, tc.audio, tc.format)
			if want, ok := strings.CutPrefix(tc.want, "error: "); ok {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("got %v, want an error with %q", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, tr := range p.Tracks {
				got = append(got, codecName(tr.Codec))
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("tracks %v, want %s", got, tc.want)
			}
		})
	}
}

// The holding screen through the API: MediaMTX's built-in screen in the layout of the encoder's audio, an uploaded
// clip (checked, reordered, written under its own name), the rules around them, viewers let in while it shows, and
// the clip gone with the stream.
func TestHoldingScreen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir}, fast)
	h.completeSetup()
	h.startPortgate()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/hold"}), &st)
	stream := "/api/v1/streams/" + itoa(st.ID)
	// The stream's path is the last entry in mediamtx.yml: its section runs to the end.
	section := func() string {
		file := h.mtxFile()
		i := strings.Index(file, "  live/hold:")
		if i < 0 {
			t.Fatalf("no path section:\n%s", file)
		}
		return file[i:]
	}

	if rec := h.do("PATCH", stream, map[string]any{"holding": "file"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a clip before uploading one: %d", rec.Code)
	}
	// The offline screen: the sidecar's clip for the stream's format and audio, which follow it.
	if err := InstallOfflineClips(dir); err != nil {
		t.Fatal(err)
	}
	if offline, _ := filepath.Glob(filepath.Join(dir, "offline-*.mp4")); len(offline) != 8 {
		t.Fatalf("offline clips installed: %v", offline)
	}
	if rec := h.do("PATCH", stream, map[string]any{"holding": "builtin"}); rec.Code != http.StatusOK || decode(rec)["format"] != "1080p50" {
		t.Fatalf("offline screen: %d %s", rec.Code, rec.Body)
	}
	if s := section(); !strings.Contains(s, "alwaysAvailable: yes") || !strings.Contains(s, "alwaysAvailableFile: "+dir+"/offline-1080p50-aac.mp4") ||
		!strings.Contains(s, "alwaysAvailableRecorded: no") {
		t.Errorf("offline screen, AAC:\n%s", s)
	}
	if rec := h.do("PATCH", stream, map[string]any{"audio": "opus", "format": "720p60"}); rec.Code != http.StatusOK ||
		!strings.Contains(section(), "offline-720p60-opus.mp4") {
		t.Errorf("offline screen, Opus at 720p60: %d\n%s", rec.Code, section())
	}
	if rec := h.do("PATCH", stream, map[string]any{"format": "1080p60"}); rec.Code != http.StatusOK {
		t.Errorf("back to 1080p60: %d", rec.Code)
	}

	// Viewers get in while it shows, though nobody streams.
	h.srv.autoOnce(ctx)
	if d, _ := h.exposure.Desired(); len(d.Want) == 0 {
		t.Error("nothing opened for viewers of the holding screen")
	}

	upload := func(b []byte) *httptest.ResponseRecorder {
		return h.do("PUT", stream+"/holding/clip", rawBody(b), contentType("video/mp4"))
	}
	if rec := upload(clip(t, testH264, testAAC(48000, 2))); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "sends Opus") {
		t.Errorf("an AAC clip for an Opus stream: %d %s", rec.Code, rec.Body)
	}
	if rec := upload([]byte("not an mp4")); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("garbage: %d", rec.Code)
	}
	rec := upload(clip(t, testH264, testOpus))
	if rec.Code != http.StatusOK || decode(rec)["holding"] != "file" {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "[0-9]*.mp4"))
	if len(files) != 1 || !strings.Contains(section(), "alwaysAvailableFile: "+files[0]) ||
		strings.Contains(section(), "alwaysAvailableTracks") {
		t.Fatalf("files %v\n%s", files, section())
	}
	stored, _ := os.ReadFile(files[0])
	if got := strings.Join(tracksOf(t, stored), " "); got != "Opus H264" {
		t.Errorf("stored tracks %s", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	if rec := h.do("PATCH", stream, map[string]any{"audio": "aac"}); rec.Code != http.StatusBadRequest {
		t.Errorf("other audio with an Opus clip: %d", rec.Code)
	}
	if rec := h.do("PATCH", stream, map[string]any{"format": "720p50"}); rec.Code != http.StatusBadRequest {
		t.Errorf("another format with a clip: %d", rec.Code)
	}
	if rec := h.do("PUT", stream+"/holding/clip?format=720p60", rawBody(clip(t, testH264, testOpus)), contentType("video/mp4")); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "1280×720 at 60 fps") {
		t.Errorf("a 1080p clip as 720p: %d %s", rec.Code, rec.Body)
	}
	// The clip's other version (the page sends both): the stream keeps its audio, and switching it needs no upload.
	rec = h.do("PUT", stream+"/holding/clip?audio=aac", rawBody(clip(t, testH264, testAAC(48000, 2))), contentType("video/mp4"))
	var both struct {
		Audio string          `json:"audio"`
		Clips map[string]bool `json:"clips"`
	}
	h.json(rec, &both)
	if rec.Code != http.StatusOK || both.Audio != "opus" || !both.Clips["aac"] || !both.Clips["opus"] {
		t.Fatalf("the AAC version: %d %s", rec.Code, rec.Body)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "[0-9]*.mp4")); len(files) != 2 {
		t.Errorf("versions stored: %v", files)
	}
	opusClip := section()
	if rec := h.do("PATCH", stream, map[string]any{"audio": "aac"}); rec.Code != http.StatusOK || section() == opusClip ||
		!strings.Contains(section(), "alwaysAvailableFile: "+dir+"/1-") {
		t.Errorf("switching to the AAC version: %d\n%s", rec.Code, section())
	}
	if rec := h.do("PATCH", stream, map[string]any{"holding": ""}); rec.Code != http.StatusOK ||
		strings.Contains(section(), "alwaysAvailable") {
		t.Errorf("off: %d\n%s", rec.Code, section())
	}
	if rec := h.do("PATCH", stream, map[string]any{"format": "720p50"}); rec.Code != http.StatusOK || decode(rec)["format"] != "720p50" {
		t.Errorf("the format while off: %d %s", rec.Code, rec.Body)
	}
	if action, _, _ := lastAudit(t, h); action != "stream.update" {
		t.Errorf("audit %s", action)
	}
	// What MediaMTX's log says about the stream's encoder.
	h.srv.d.Notes.Publish("live/hold", netip.MustParseAddr("192.0.2.1"))
	h.srv.d.Notes.Line("2026/10/01 11:16:22 INF [RTMP] [conn 192.0.2.1:5000] closed: wants to publish [H264 MPEG-4 Audio], but stream expects [Opus H264]")
	if rec := h.do("GET", stream+"/notes", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "refused your encoder") {
		t.Errorf("notes: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("DELETE", stream, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "[0-9]*.mp4")); len(files) != 0 {
		t.Errorf("clip left after the stream went: %v", files)
	}
}

// MediaMTX itself accepts every offline clip as the sidecar installs it.
func TestOfflineClipsWithMediaMTX(t *testing.T) {
	bin := mtxtest.Bin(t)
	dir := t.TempDir()
	if err := InstallOfflineClips(dir); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, offlineClip("720p50", audioOpus)))
	if err := InstallOfflineClips(dir); err != nil { // again: nothing to do
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, offlineClip("720p50", audioOpus))); !bytes.Equal(before, after) {
		t.Error("a second install changed a clip")
	}
	var conf strings.Builder
	conf.WriteString("paths:\n")
	for format := range videoFormats {
		for _, audio := range []string{audioAAC, audioOpus} {
			stored, _ := os.ReadFile(filepath.Join(dir, offlineClip(format, audio)))
			want := "H264 MPEG4Audio"
			if audio == audioOpus {
				want = "Opus H264"
			}
			if got := strings.Join(tracksOf(t, stored), " "); got != want {
				t.Errorf("%s %s: tracks %s", format, audio, got)
			}
			fmt.Fprintf(&conf, "  cam-%s-%s:\n    alwaysAvailable: yes\n    alwaysAvailableFile: %s\n",
				format, audio, filepath.Join(dir, offlineClip(format, audio)))
		}
	}
	if err := (mtxconf.Validator{Bin: bin}).Validate(context.Background(), []byte(conf.String())); err != nil {
		t.Fatal(err)
	}
}

// The holding version follows the encoder: MediaMTX refuses an encoder with the other audio, and the stream switches
// to the version with its audio (when there is one), so the encoder's reconnect gets in.
func TestHoldingFollowsEncoder(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir}, fast)
	h.completeSetup()
	if err := InstallOfflineClips(dir); err != nil {
		t.Fatal(err)
	}
	notes := h.srv.d.Notes
	notes.OnRefused = h.srv.FollowEncoder
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/follow"}), &st)
	stream := "/api/v1/streams/" + itoa(st.ID)
	if rec := h.do("PATCH", stream, map[string]any{"holding": "builtin", "audio": "opus"}); rec.Code != http.StatusOK {
		t.Fatalf("offline screen with Opus: %d %s", rec.Code, rec.Body)
	}
	refuse := func(sends, expects string) {
		notes.Publish("live/follow", netip.MustParseAddr("192.0.2.1"))
		notes.Line("2026/10/01 12:00:00 INF [RTMP] [conn 192.0.2.1:5000] closed: wants to publish [" + sends +
			"], but stream expects [" + expects + "]")
	}
	refuse("H264 MPEG-4 Audio", "Opus H264")
	if m := decode(h.do("GET", stream, nil)); m["audio"] != "aac" || !strings.Contains(h.mtxFile(), "offline-1080p50-aac.mp4") {
		t.Fatalf("after an AAC encoder: %v\n%s", m["audio"], h.mtxFile())
	}
	if rec := h.do("GET", stream+"/notes", nil); !strings.Contains(rec.Body.String(), `"kind":"switched"`) ||
		strings.Contains(rec.Body.String(), `"kind":"tracks"`) {
		t.Errorf("notes: %s", rec.Body)
	}
	if action, target, _ := lastAudit(t, h); action != "stream.holding.follow" || target != "live/follow" {
		t.Errorf("audit %s %s", action, target)
	}

	// An own clip with only the Opus version: nothing to switch to, and the note says so.
	if rec := h.do("PATCH", stream, map[string]any{"audio": "opus"}); rec.Code != http.StatusOK {
		t.Fatalf("back to Opus: %d", rec.Code)
	}
	if rec := h.do("PUT", stream+"/holding/clip?format=1080p60", rawBody(clip(t, testH264, testOpus)), contentType("video/mp4")); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	h.srv.followed.Clear() // the gap between switches
	refuse("H264 MPEG-4 Audio", "Opus H264")
	if m := decode(h.do("GET", stream, nil)); m["audio"] != "opus" {
		t.Errorf("switched to a version that does not exist: %v", m["audio"])
	}
	if rec := h.do("GET", stream+"/notes", nil); !strings.Contains(rec.Body.String(), "has no RTMP/SRT (AAC) version") {
		t.Errorf("notes: %s", rec.Body)
	}
	// An encoder without audio cannot be matched: the refusal itself is the note.
	refuse("H264", "Opus H264")
	if rec := h.do("GET", stream+"/notes", nil); !strings.Contains(rec.Body.String(), "it sends H264, but the holding screen expects") {
		t.Errorf("notes: %s", rec.Body)
	}
}
