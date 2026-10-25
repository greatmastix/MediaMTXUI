package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/backup"
	"mtxui/internal/store"
	"mtxui/internal/yamledit"
)

// Holding clips a mediamtx.yml names that are not on this server: backups leave out clips larger than
// backup.MaxClip, so a restore can bring back a stream whose clip is gone (on a new server, or after the clip was
// replaced). MediaMTX refuses a config naming a missing file, so the restore's check validates the backup's config
// with those clips replaced, and the restore itself switches their streams to the offline screen (missingClipsFallback).

// ownClipRefs returns the sidecar's own holding clips a config names (an alwaysAvailableFile in the holding directory
// with a clip's name), by path ("" for pathDefaults).
func (s *Server) ownClipRefs(conf []byte) map[string]string {
	var doc struct {
		PathDefaults struct {
			AlwaysAvailableFile string `yaml:"alwaysAvailableFile"`
		} `yaml:"pathDefaults"`
		Paths map[string]struct {
			AlwaysAvailableFile string `yaml:"alwaysAvailableFile"`
		} `yaml:"paths"`
	}
	out := map[string]string{}
	if yaml.Unmarshal(conf, &doc) != nil {
		return out
	}
	own := func(path, file string) {
		if filepath.Dir(file) == filepath.Clean(s.d.Settings.HoldingDir) && backup.ClipName.MatchString(filepath.Base(file)) {
			out[path] = filepath.Base(file)
		}
	}
	own("", doc.PathDefaults.AlwaysAvailableFile)
	for name, p := range doc.Paths {
		own(name, p.AlwaysAvailableFile)
	}
	return out
}

// clipPresent reports whether the holding directory has a clip of that name.
func (s *Server) clipPresent(name string) bool {
	fi, err := os.Stat(filepath.Join(s.d.Settings.HoldingDir, name))
	return err == nil && fi.Mode().IsRegular()
}

// replaceMissingClips puts with(path) in place of every own holding clip conf names that is not on this server, and
// returns the config and those paths, sorted ("" for pathDefaults).
func (s *Server) replaceMissingClips(conf []byte, with func(path string) string) ([]byte, []string, error) {
	var missing []string
	for path, name := range s.ownClipRefs(conf) {
		if !s.clipPresent(name) {
			missing = append(missing, path)
		}
	}
	if len(missing) == 0 {
		return conf, nil, nil
	}
	slices.Sort(missing)
	d, err := yamledit.New(conf)
	if err != nil {
		return nil, nil, err
	}
	for _, path := range missing {
		at := []string{"paths", path, "alwaysAvailableFile"}
		if path == "" {
			at = []string{"pathDefaults", "alwaysAvailableFile"}
		}
		if err := d.Set(at, with(path)); err != nil {
			return nil, nil, err
		}
	}
	return d.Bytes(), missing, nil
}

// missingClipsFallback gets a restored database and the backup's config ready for clips that are not on this
// server: a stream forgets a missing clip, and one that showed it shows the offline screen in its format and audio
// instead (or its clip for the other audio, when that one is here); conf names the offline screen where it named
// the missing clip. It returns conf and the streams (or config paths) that lost their clip.
func (s *Server) missingClipsFallback(ctx context.Context, conf []byte) ([]byte, []string, error) {
	streams, err := s.d.Store.Streams(ctx)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]store.Stream{}
	var lost []string
	for _, st := range streams {
		before := st
		for _, audio := range []string{audioAAC, audioOpus} {
			if name := clipOf(st, audio); name != "" && !s.clipPresent(name) {
				setClip(&st, audio, "")
			}
		}
		if st.Holding == "file" && clipOf(st, st.Audio) == "" {
			if other := map[string]string{audioAAC: audioOpus, audioOpus: audioAAC}[st.Audio]; clipOf(st, other) != "" {
				st.Audio = other
			} else {
				st.Holding = "builtin"
			}
		}
		byName[st.Name] = st
		if st == before {
			continue
		}
		lost = append(lost, st.Name)
		if err := s.d.Store.PatchStream(ctx, st.ID, store.StreamPatch{
			Audio: &st.Audio, Holding: &st.Holding, ClipAAC: &st.ClipAAC, ClipOpus: &st.ClipOpus,
		}); err != nil {
			return nil, nil, err
		}
	}
	out, paths, err := s.replaceMissingClips(conf, func(path string) string {
		if st, ok := byName[path]; ok {
			switch st.Holding {
			case "file":
				return filepath.Join(s.d.Settings.HoldingDir, clipOf(st, st.Audio))
			case "builtin":
				return filepath.Join(s.d.Settings.HoldingDir, offlineClip(st.Format, st.Audio))
			}
		}
		return filepath.Join(s.d.Settings.HoldingDir, offlineClip(defaultFormat, audioAAC))
	})
	if err != nil {
		return nil, nil, err
	}
	for _, path := range paths {
		if _, ok := byName[path]; !ok && !slices.Contains(lost, path) {
			lost = append(lost, path)
		}
	}
	slices.Sort(lost)
	return out, lost, nil
}
