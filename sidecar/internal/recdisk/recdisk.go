// Package recdisk measures the recordings filesystem for the storage budget: the size of the segments each path
// directory holds (recordPath puts a path's segments in a directory named after it) and the filesystem's free space.
// It only reads, from a root the sidecar mounts read-only; it never takes a name from a request.
package recdisk

import (
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
)

// Usage is one measurement.
type Usage struct {
	ByDir map[string]int64 // segment bytes per directory, relative to the root with forward slashes ("live/woo")
	Bytes int64            // all segments
	Total int64            // the filesystem's size
	Free  int64            // space available to unprivileged writers
}

// segment reports whether a file is a recording segment (MediaMTX writes fMP4 as .mp4, MPEG-TS as .ts).
func segment(name string) bool {
	return strings.HasSuffix(name, ".mp4") || strings.HasSuffix(name, ".ts")
}

// Measure walks root and asks the filesystem for its free space. Other files (a filler, a stray note) count for the
// filesystem, not for recordings.
func Measure(root string) (Usage, error) {
	u := Usage{ByDir: map[string]int64{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // a segment deleted while walking
		}
		if d.IsDir() || !d.Type().IsRegular() || !segment(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // a segment deleted while walking
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil || rel == "." {
			return nil //nolint:nilerr // segments sit in a path's directory, never in the root
		}
		u.ByDir[filepath.ToSlash(rel)] += info.Size()
		u.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return u, err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return u, err
	}
	bsize := int64(st.Bsize)           //nolint:unconvert // int32 on 32-bit ARM
	u.Total = int64(st.Blocks) * bsize //nolint:gosec // block counts fit
	u.Free = int64(st.Bavail) * bsize  //nolint:gosec // as above
	return u, nil
}
