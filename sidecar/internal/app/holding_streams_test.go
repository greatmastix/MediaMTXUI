package app

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mtxui/internal/auth"
	"mtxui/internal/mtxlog"
)

// slowUpload is a holding clip upload whose body the test sends in two halves: the first before start returns (so
// the handler has read the stream and is reading the body), the second with finish.
type slowUpload struct {
	pw     *io.PipeWriter
	rest   []byte
	answer <-chan *httptest.ResponseRecorder
}

func (h *harness) startUpload(path string, body []byte) *slowUpload {
	h.t.Helper()
	pr, pw := io.Pipe()
	u := &slowUpload{pw: pw, rest: body[len(body)/2:]}
	u.answer = h.serve(h.request("PUT", path, pr, contentType("video/mp4")))
	if _, err := pw.Write(body[:len(body)/2]); err != nil { // returns once the handler has read it
		h.t.Fatal(err)
	}
	return u
}

func (u *slowUpload) finish() *httptest.ResponseRecorder {
	_, _ = u.pw.Write(u.rest)
	_ = u.pw.Close()
	return <-u.answer
}

// heldClips lists the stream clips in the holding directory.
func heldClips(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "[0-9]*.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// An upload writes only the holding columns, from the stream as it is when the clip has arrived: an owner removed
// and a stream made private while a clip was on its way used to be put back when it landed, the removed owner's
// upload included.
func TestHoldingUploadKeepsOwnerAndPublic(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir}, fast)
	h.completeSetup()
	admin := [2]string{h.cookie, h.csrf}
	var inv Invitation
	h.json(h.do("POST", "/api/v1/users", map[string]any{"username": "alice", "role": "streamer"}), &inv)
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/alice", "ownerId": inv.User.ID}), &st)
	stream := "/api/v1/streams/" + itoa(st.ID)
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": "alice streams a lot"}); rec.Code != http.StatusCreated {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}

	up := h.startUpload(stream+"/holding/clip?format=1080p60", clip(t, testH264, testAAC(48000, 2)))
	h.cookie, h.csrf = admin[0], admin[1]
	if rec := h.do("PATCH", stream, map[string]any{"ownerId": 0, "public": false}); rec.Code != http.StatusOK {
		t.Fatalf("admin's change: %d %s", rec.Code, rec.Body)
	}
	if rec := up.finish(); rec.Code != http.StatusForbidden {
		t.Errorf("the removed owner's upload: %d %s", rec.Code, rec.Body)
	}
	m := decode(h.do("GET", stream, nil))
	if m["public"] != false || m["owner"] != nil || h.srv.IsPublicPath("live/alice") {
		t.Errorf("after the upload: public %v, owner %v", m["public"], m["owner"])
	}
	if files := heldClips(t, dir); len(files) != 0 {
		t.Errorf("clips stored: %v", files)
	}

	// The admin's own upload, while the stream is made public again, keeps that too.
	up = h.startUpload(stream+"/holding/clip?format=1080p60", clip(t, testH264, testAAC(48000, 2)))
	if rec := h.do("PATCH", stream, map[string]any{"public": true, "title": "Back"}); rec.Code != http.StatusOK {
		t.Fatalf("public again: %d %s", rec.Code, rec.Body)
	}
	if rec := up.finish(); rec.Code != http.StatusOK || decode(rec)["public"] != true || decode(rec)["title"] != "Back" {
		t.Errorf("upload: %d %s", rec.Code, rec.Body)
	}
}

// One upload per stream at a time, refused at once rather than queued; every clip a finished upload replaced is
// gone, where concurrent uploads each used to drop only the clip they had read and leave the others' behind.
func TestHoldingUploadsOnePerStream(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir}, fast)
	h.completeSetup()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/one"}), &st)
	path := "/api/v1/streams/" + itoa(st.ID) + "/holding/clip?format=1080p60"

	first := h.startUpload(path, clip(t, testH264, testAAC(48000, 2)))
	second := h.serve(h.request("PUT", path, bytes.NewReader(clip(t, testH264, testAAC(48000, 2))), contentType("video/mp4")))
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-second:
		if rec.Code != http.StatusConflict {
			t.Errorf("a second upload while one is on its way: %d %s", rec.Code, rec.Body)
		}
	case <-time.After(5 * time.Second):
		t.Error("a second upload waited for the first")
	}
	if rec := first.finish(); rec.Code != http.StatusOK {
		t.Fatalf("the first upload: %d %s", rec.Code, rec.Body)
	}
	if rec == nil {
		<-second
	}
	if rec := h.do("PUT", path, rawBody(clip(t, testH264, testAAC(48000, 2))), contentType("video/mp4")); rec.Code != http.StatusOK {
		t.Fatalf("an upload after it: %d %s", rec.Code, rec.Body)
	}
	if files := heldClips(t, dir); len(files) != 1 {
		t.Errorf("clips stored: %v, want the stream's one", files)
	}
}

// A clip on its way holds no lock other streams or the log follower need: an upload used to hold holdingMu while its
// body arrived, so a slow one stalled every holding change and, through FollowEncoder, the MediaMTX log follower.
func TestHoldingUploadLeavesTheLockFree(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir}, fast)
	h.completeSetup()
	if err := InstallOfflineClips(dir); err != nil {
		t.Fatal(err)
	}
	var slow, other streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/slow"}), &slow)
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/other"}), &other)
	if rec := h.do("PATCH", "/api/v1/streams/"+itoa(other.ID), map[string]any{"holding": "builtin", "audio": "opus"}); rec.Code != http.StatusOK {
		t.Fatalf("offline screen: %d %s", rec.Code, rec.Body)
	}

	up := h.startUpload("/api/v1/streams/"+itoa(slow.ID)+"/holding/clip?format=1080p60", clip(t, testH264, testAAC(48000, 2)))
	switched := make(chan *mtxlog.Note, 1)
	go func() { switched <- h.srv.FollowEncoder("live/other", "H264 MPEG-4 Audio", "") }()
	var note *mtxlog.Note
	select {
	case note = <-switched:
		if note == nil || note.Kind != "switched" {
			t.Errorf("note %+v", note)
		}
	case <-time.After(5 * time.Second):
		t.Error("the log follower waited for another stream's upload")
	}
	if rec := up.finish(); rec.Code != http.StatusOK {
		t.Errorf("upload: %d %s", rec.Code, rec.Body)
	}
	if note == nil {
		<-switched
	}
}

// A clip whose sample table declares millions of samples in a few bytes is refused before the MP4 reader builds a
// record for each of them (which took hundreds of MB: a streamer could make the sidecar run out of memory).
func TestHoldingClipDeclaringTooMuch(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir}, fast)
	h.completeSetup()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/bomb"}), &st)
	b := clip(t, testH264, testAAC(48000, 2))
	i := bytes.Index(b, []byte("stts"))
	if i < 0 {
		t.Fatal("no stts box")
	}
	binary.BigEndian.PutUint32(b[i+12:], 5_000_000) // type, version and flags, entry count, then the first entry's count

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rec := h.do("PUT", "/api/v1/streams/"+itoa(st.ID)+"/holding/clip?format=1080p60", rawBody(b), contentType("video/mp4"))
	runtime.ReadMemStats(&after)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "too long") {
		t.Errorf("upload: %d %s", rec.Code, rec.Body)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 32<<20 {
		t.Errorf("refusing it allocated %d MB", grew>>20)
	}
}

// slowBody sends its parts with a pause before each after the first.
type slowBody struct {
	parts [][]byte
	pause time.Duration
	sent  int
}

func (b *slowBody) Read(p []byte) (int, error) {
	if len(b.parts) == 0 {
		return 0, io.EOF
	}
	if b.sent > 0 {
		time.Sleep(b.pause)
	}
	n := copy(p, b.parts[0])
	if b.parts[0] = b.parts[0][n:]; len(b.parts[0]) == 0 {
		b.parts, b.sent = b.parts[1:], b.sent+1
	}
	return n, nil
}

// A clip may take longer to arrive than an ordinary request body (64 MB over a slow uplink).
func TestHoldingUploadTakesItsTime(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir}, fast)
	h.completeSetup()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/far"}), &st)
	srv := httptest.NewServer(h.public)
	t.Cleanup(srv.Close)

	b := clip(t, testH264, testAAC(48000, 2))
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/streams/"+itoa(st.ID)+"/holding/clip?format=1080p60",
		&slowBody{parts: [][]byte{b[:len(b)/2], b[len(b)/2:]}, pause: 3 * bodyReadTimeout})
	req.ContentLength = int64(len(b))
	req.Header.Set("Content-Type", "video/mp4")
	req.Header.Set("Origin", origin)
	req.Header.Set(auth.CSRFHeader, h.csrf)
	req.AddCookie(&http.Cookie{Name: "__Host-mtxui_session", Value: h.cookie})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK {
		t.Errorf("a slow upload: %d %s", resp.StatusCode, body)
	}
}
