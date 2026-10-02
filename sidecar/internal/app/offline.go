package app

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"
)

// The offline screen's clips (offline/README.md): MediaMTX's offline frame in every format and audio, embedded and
// installed into the holding directory at startup, so "Offline screen" always matches the stream's encoder.

//go:embed offline/*.mp4
var offlineClips embed.FS

// offlineClip is the installed file name of the offline clip for a format and audio.
func offlineClip(format, audio string) string { return fmt.Sprintf("offline-%s-%s.mp4", format, audio) }

// InstallOfflineClips writes the offline clips into the holding directory dir, each checked and put in its encoder's
// track order the way an upload is. A clip already there unchanged is left alone. It runs before the startup check of
// mediamtx.yml, which MediaMTX's validator fails if a holding file the config names is missing.
func InstallOfflineClips(dir string) error {
	for format := range videoFormats {
		for _, audio := range []string{audioAAC, audioOpus} {
			name := offlineClip(format, audio)
			src, err := offlineClips.ReadFile("offline/" + name)
			if err != nil {
				return err
			}
			var p pmp4.Presentation
			if err := p.Unmarshal(bytes.NewReader(src)); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := fitClip(&p, audio, format); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			var out seekBuffer
			if err := p.Marshal(&out); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			path := filepath.Join(dir, name)
			if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, out.b) {
				continue
			}
			tmp := path + ".tmp"
			if err := os.WriteFile(tmp, out.b, 0o600); err != nil {
				return err
			}
			if err := os.Rename(tmp, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// seekBuffer is an in-memory io.WriteSeeker.
type seekBuffer struct {
	b   []byte
	pos int
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	if need := s.pos + len(p); need > len(s.b) {
		s.b = append(s.b, make([]byte, need-len(s.b))...)
	}
	copy(s.b[s.pos:], p)
	s.pos += len(p)
	return len(p), nil
}

func (s *seekBuffer) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case 0:
		s.pos = int(off)
	case 1:
		s.pos += int(off)
	case 2:
		s.pos = len(s.b) + int(off)
	}
	return int64(s.pos), nil
}
