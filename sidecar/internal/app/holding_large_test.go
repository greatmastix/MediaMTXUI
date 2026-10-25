package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtxui/internal/mtxconf"
	"mtxui/internal/mtxtest"
)

// Clips up to MTXUI_HOLDING_MAX_GB: a larger one is refused before its body is read, one the disk has no room for
// too, and a clip that is stored is recorded as read, with its length.
func TestHoldingUploadLimits(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir, "MTXUI_HOLDING_MAX_GB": "0.01"}, fast)
	h.completeSetup()
	if err := InstallOfflineClips(dir); err != nil {
		t.Fatal(err)
	}
	clips := NewHoldingClips(dir, nil)
	h.srv.d.HoldingClips = clips
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/long"}), &st)
	put := "/api/v1/streams/" + itoa(st.ID) + "/holding/clip?audio=aac&format=1080p60"

	if rec := h.do("PUT", put, rawBody(make([]byte, 11<<20)), contentType("video/mp4")); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "larger than 0.01 GB") {
		t.Errorf("over the limit: %d %s", rec.Code, rec.Body)
	}
	if ups, _ := filepath.Glob(filepath.Join(dir, ".upload-*")); len(ups) != 0 {
		t.Errorf("an upload refused by its length was written: %v", ups)
	}

	old := diskFree
	diskFree = func(string) (int64, bool) { return 3 << 30, true } // under the 5 GB recording is switched off at
	rec := h.do("PUT", put, rawBody(clip(t, testH264, testAAC(48000, 2))), contentType("video/mp4"))
	diskFree = old
	if rec.Code != http.StatusInsufficientStorage || decode(rec)["error"] != "space" ||
		!strings.Contains(rec.Body.String(), "The server has 3 GB free for holding clips") {
		t.Errorf("no room: %d %s", rec.Code, rec.Body)
	}

	rec = h.do("PUT", put, rawBody(clip(t, testH264, testAAC(48000, 2))), contentType("video/mp4"))
	if rec.Code != http.StatusOK {
		t.Fatalf("a clip within the limit: %d %s", rec.Code, rec.Body)
	}
	stored, _ := filepath.Glob(filepath.Join(dir, itoa(st.ID)+"-*.mp4"))
	if len(stored) != 1 {
		t.Fatalf("stored clips: %v", stored)
	}
	if got := clips.StandIn(stored[0]); got != filepath.Join(dir, "offline-1080p50-aac.mp4") {
		t.Errorf("stand-in for the stored clip: %q", got)
	}
	if got := clips.Allowance([]byte("paths:\n  live/long:\n    alwaysAvailableFile: " + stored[0] + "\n")); got <= 0 || got > time.Second {
		t.Errorf("allowance for a clip of 0.05 s: %v", got)
	}
	// A clip changed since it was read is read again by the check, and counts as an hour until then.
	f, _ := os.OpenFile(stored[0], os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.Write([]byte{0})
	_ = f.Close()
	if got := clips.StandIn(stored[0]); got != "" {
		t.Errorf("stand-in for a changed clip: %q", got)
	}
	if got := clips.Allowance([]byte("paths:\n  a:\n    alwaysAvailableFile: " + stored[0] + "\n")); got != reloadPerClip {
		t.Errorf("allowance for a clip not read: %v", got)
	}
	if got := clips.StandIn(filepath.Join(dir, "offline-1080p50-aac.mp4")); got != "" {
		t.Errorf("stand-in for an offline clip (not an upload): %q", got)
	}
}

// What the config check gets: the offline clip in place of an uploaded clip the sidecar has read and that is
// unchanged since (here a stand-in is all that makes the config pass), the clip itself otherwise.
func TestHoldingStandInWithMediaMTX(t *testing.T) {
	bin := mtxtest.Bin(t)
	dir := t.TempDir()
	if err := InstallOfflineClips(dir); err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(dir, "5-0123456789ab.mp4")
	good, err := os.ReadFile(filepath.Join(dir, "offline-720p50-opus.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upload, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "6-0123456789ab.mp4"), []byte("not an MP4"), 0o600); err != nil {
		t.Fatal(err)
	}
	clips := NewHoldingClips(dir, nil)
	clips.CheckAll(context.Background())
	if _, ok := clips.known(filepath.Join(dir, "6-0123456789ab.mp4")); ok {
		t.Error("a file that is no MP4 was recorded as read")
	}
	conf := []byte(fmt.Sprintf("authMethod: internal\nauthInternalUsers:\n  - user: any\n    permissions:\n      - action: read\n"+
		"api: no\nrtsp: no\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\nplayback: no\n"+
		"paths:\n  hold:\n    alwaysAvailable: yes\n    alwaysAvailableFile: %s\n", upload))
	v := mtxconf.Validator{Bin: bin, StandIn: clips.StandIn}
	if err := v.Validate(context.Background(), conf); err != nil {
		t.Fatalf("a clip that was read: %v", err)
	}
	// Garbage of the same size and time: the check takes the stand-in, so it passes.
	fi, _ := os.Stat(upload)
	if err := os.WriteFile(upload, bytes.Repeat([]byte{0}, len(good)), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(upload, fi.ModTime(), fi.ModTime())
	if err := v.Validate(context.Background(), conf); err != nil {
		t.Fatalf("with the stand-in: %v", err)
	}
	// Any change MediaMTX could see in its size or time, and the check reads the clip itself again.
	_ = os.Chtimes(upload, fi.ModTime().Add(time.Second), fi.ModTime().Add(time.Second))
	if err := v.Validate(context.Background(), conf); err == nil {
		t.Fatal("a changed clip was not read again")
	}
	if err := (mtxconf.Validator{Bin: bin}).Validate(context.Background(), conf); err == nil {
		t.Fatal("without stand-ins the garbage passed")
	}
}

// An upload has as long as it keeps arriving: a body that trickles in for longer than bodyReadTimeout is taken in
// whole, one that stalls for longer than the idle time is cut off.
func TestSlidingBodyDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: trickles bodies past bodyReadTimeout")
	}
	old := bodyReadTimeout
	bodyReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })
	srv := httptest.NewServer(bodyDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, slidingBody(w, r.Body, 400*time.Millisecond))
		if err != nil {
			w.WriteHeader(http.StatusRequestTimeout)
		}
		fmt.Fprint(w, n)
	})))
	t.Cleanup(srv.Close)
	send := func(pause time.Duration) string {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintf(conn, "PUT / HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n")
		for i := range 10 {
			if i == 5 {
				time.Sleep(pause)
			}
			time.Sleep(100 * time.Millisecond) // a second in all: five times bodyReadTimeout
			_, _ = conn.Write([]byte{'x'})
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			return "cut off"
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("%d %s", resp.StatusCode, b)
	}
	if got := send(0); got != "200 10" {
		t.Errorf("a body that keeps arriving: %s", got)
	}
	if got := send(time.Second); got == "200 10" {
		t.Errorf("a body that stalled for a second: %s", got)
	}
}

// bigClip makes a valid clip larger than a backup carries: a real clip, then a free box of size bytes (sparse).
func bigClip(t *testing.T, path string, small []byte, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	head := make([]byte, 8)
	binary.BigEndian.PutUint32(head, uint32(size))
	copy(head[4:], "free")
	if _, err := f.Write(append(append([]byte{}, small...), head...)); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(len(small)) + size); err != nil {
		t.Fatal(err)
	}
}

// The browser writes long clips to disk as it makes them (mediabunny's StreamTarget), and the MP4 then has its index
// after the media (fastStart: false). Such a clip is taken, and what is stored MediaMTX reads.
func TestHoldingClipWithIndexLast(t *testing.T) {
	bin := mtxtest.Bin(t)
	clipFile, err := os.ReadFile("testdata/holding-moov-at-end-720p50-opus.mp4") // mediabunny, from offline-720p50-opus.mp4
	if err != nil {
		t.Fatal(err)
	}
	if string(clipFile[len(clipFile)-1602+4:len(clipFile)-1602+8]) != "moov" {
		t.Fatal("the fixture no longer has its index last")
	}
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"MTXUI_HOLDING_DIR": dir}, fast)
	h.completeSetup()
	if err := InstallOfflineClips(dir); err != nil {
		t.Fatal(err)
	}
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/last"}), &st)
	rec := h.do("PUT", "/api/v1/streams/"+itoa(st.ID)+"/holding/clip?audio=opus&format=720p50", rawBody(clipFile), contentType("video/mp4"))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	stored, _ := filepath.Glob(filepath.Join(dir, itoa(st.ID)+"-*.mp4"))
	if len(stored) != 1 {
		t.Fatalf("stored: %v", stored)
	}
	if seconds, err := readClip(stored[0]); err != nil || seconds < 0.9 || seconds > 1.1 {
		t.Errorf("the stored clip reads as %v s: %v", seconds, err)
	}
	conf := fmt.Sprintf("authMethod: internal\nauthInternalUsers:\n  - user: any\n    permissions:\n      - action: read\n"+
		"api: no\nrtsp: no\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\nplayback: no\n"+
		"paths:\n  hold:\n    alwaysAvailable: yes\n    alwaysAvailableFile: %s\n", stored[0])
	if err := (mtxconf.Validator{Bin: bin}).Validate(context.Background(), []byte(conf)); err != nil {
		t.Errorf("MediaMTX on the stored clip: %v", err)
	}
}
