package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"
	"go.yaml.in/yaml/v3"
)

// HoldingClips keeps long holding clips (up to an hour) from slowing the sidecar's config checks, and gives MediaMTX's
// reloads the time they take. MediaMTX reads a clip's every sample, about 5 s an hour of video, to validate a config
// that names it (every reload, and every --validate-conf), and again to play it:
//
//   - The config check (mtxconf.Validator.StandIn) checks the small offline clip in place of an uploaded clip the
//     sidecar has read itself, with the reader MediaMTX uses, and that has not changed since (size and time).
//   - The read-back after a write (mtxconf.Verifier.Allowance) waits longer by the clips' length.
type HoldingClips struct {
	dir string
	log *slog.Logger

	mu      sync.Mutex
	checked map[string]checkedClip // by file name
}

type checkedClip struct {
	size    int64
	mod     time.Time
	seconds float64
}

// uploadedClip matches the names uploads are stored under (storeClip): the stream's id and a content hash.
var uploadedClip = regexp.MustCompile(`^[0-9]+-[0-9a-f]{12}\.mp4$`)

// reloadPerClip is the reload time MediaMTX needs beyond the usual for each hour of holding clips a config names (measured on 1.21.3:
// a setting that restarts the paths took 10.3 s with one hour-long clip; the clip is read to validate, then to play).
const reloadPerClip = 15 * time.Second

// NewHoldingClips returns the registry for the holding directory dir.
func NewHoldingClips(dir string, log *slog.Logger) *HoldingClips {
	return &HoldingClips{dir: filepath.Clean(dir), log: log, checked: map[string]checkedClip{}}
}

// mark records an uploaded clip as read and fine, with its length.
func (h *HoldingClips) mark(name string, seconds float64) {
	fi, err := os.Stat(filepath.Join(h.dir, name))
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checked[name] = checkedClip{size: fi.Size(), mod: fi.ModTime(), seconds: seconds}
}

// known returns an uploaded clip's record when the file is as it was when it was read.
func (h *HoldingClips) known(file string) (checkedClip, bool) {
	if filepath.Dir(file) != h.dir || !uploadedClip.MatchString(filepath.Base(file)) {
		return checkedClip{}, false
	}
	fi, err := os.Stat(file)
	if err != nil || !fi.Mode().IsRegular() {
		return checkedClip{}, false
	}
	h.mu.Lock()
	c, ok := h.checked[filepath.Base(file)]
	h.mu.Unlock()
	return c, ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime())
}

// StandIn is the config check's stand-in for a holding clip (mtxconf.Validator.StandIn).
func (h *HoldingClips) StandIn(file string) string {
	if _, ok := h.known(file); !ok {
		return ""
	}
	return filepath.Join(h.dir, offlineClip(defaultFormat, audioAAC))
}

// Allowance is how much longer MediaMTX may take to apply config than usual (mtxconf.Verifier.Allowance), for the
// uploaded clips it names; a clip not read yet counts as an hour.
func (h *HoldingClips) Allowance(config []byte) time.Duration {
	var doc struct {
		Paths map[string]struct {
			AlwaysAvailableFile string `yaml:"alwaysAvailableFile"`
		} `yaml:"paths"`
	}
	if yaml.Unmarshal(config, &doc) != nil {
		return 0
	}
	hours := 0.0
	for _, p := range doc.Paths {
		f := filepath.Clean(p.AlwaysAvailableFile)
		if filepath.Dir(f) != h.dir || !uploadedClip.MatchString(filepath.Base(f)) {
			continue
		}
		if c, ok := h.known(f); ok {
			hours += c.seconds / 3600
		} else {
			hours++
		}
	}
	return time.Duration(hours * float64(reloadPerClip))
}

// CheckAll reads every uploaded clip in the holding directory that has not been read yet, as an upload is
// (clipLimits, then MediaMTX's MP4 reader), and records the ones that are fine. It runs at start, in the background:
// until a clip is read, the config check reads it itself.
func (h *HoldingClips) CheckAll(ctx context.Context) {
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if !e.Type().IsRegular() || !uploadedClip.MatchString(e.Name()) {
			continue
		}
		if _, ok := h.known(filepath.Join(h.dir, e.Name())); ok {
			continue
		}
		if seconds, err := readClip(filepath.Join(h.dir, e.Name())); err == nil {
			h.mark(e.Name(), seconds)
		} else if h.log != nil {
			h.log.Warn("a holding clip does not read as an MP4 MediaMTX can play", "file", e.Name(), "err", err)
		}
	}
}

// readClip reads a clip as an upload is read, and returns its length in seconds (its longest track's).
func readClip(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	r, err := newBufferedReadSeeker(f)
	if err != nil {
		return 0, err
	}
	if err := clipLimits(r, fi.Size()); err != nil {
		return 0, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	var p pmp4.Presentation
	if err := p.Unmarshal(r); err != nil {
		return 0, notMP4
	}
	return presentationSeconds(&p), nil
}

// presentationSeconds is a presentation's length: its longest track's.
func presentationSeconds(p *pmp4.Presentation) float64 {
	longest := 0.0
	for _, t := range p.Tracks {
		var d uint64
		for _, s := range t.Samples {
			d += uint64(s.Duration)
		}
		if t.TimeScale > 0 {
			longest = max(longest, float64(d)/float64(t.TimeScale))
		}
	}
	return longest
}
