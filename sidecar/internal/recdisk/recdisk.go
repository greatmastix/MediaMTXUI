// Package recdisk measures the recordings filesystem for the storage budget: every segment file's size and the
// filesystem's free space, and which measured file holds a segment MediaMTX lists, whatever the recordPath (so the
// budget knows what deleting a segment frees). It only reads, from a root the sidecar mounts read-only; it never takes
// a name from a request, and a file name it builds from MediaMTX's listing is only a key into what it measured.
package recdisk

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Mount is where MediaMTX sees the recordings volume (compose.yaml); mtxconf's rules keep every recordPath under it.
const Mount = "/recordings"

// DefaultRecordPath is MediaMTX's recordPath when mediamtx.yml sets none: ./recordings/..., run from / in its image.
const DefaultRecordPath = Mount + "/%path/%Y-%m-%d_%H-%M-%S-%f"

// Usage is one measurement.
type Usage struct {
	Files map[string]int64 // each segment file's bytes, by its name relative to the root with forward slashes
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
	u := Usage{Files: map[string]int64{}}
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
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil //nolint:nilerr // under root, where WalkDir found it
		}
		u.Files[filepath.ToSlash(rel)] = info.Size()
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

// Segment is a segment as MediaMTX lists it: its path's name and its start.
type Segment struct {
	Path  string
	Start time.Time
}

// Sizes finds the measured file of each segment MediaMTX lists, trying every recordPath in mediamtx.yml (the one
// MediaMTX uses for a path names the segment's file; another names none, or one already taken). sizes[i] is segs[i]'s
// file's bytes, or -1 when no measured file is its. unlisted is the bytes of the segment files no listed segment
// holds: files MediaMTX does not list, and so cannot delete (written under an earlier recordPath, or copied there).
func (u Usage) Sizes(recordPaths []string, segs []Segment) (sizes []int64, unlisted int64) {
	taken := map[string]bool{}
	unlisted = u.Bytes
	sizes = make([]int64, len(segs))
	for i, sg := range segs {
		sizes[i] = -1
	find:
		for _, rp := range recordPaths {
			for _, ext := range []string{".mp4", ".ts"} {
				name, ok := segmentFile(Mount, rp+ext, sg.Path, sg.Start)
				if size, measured := u.Files[name]; ok && measured && !taken[name] {
					taken[name], sizes[i] = true, size
					unlisted -= size
					break find
				}
			}
		}
	}
	return sizes, unlisted
}

// segmentFile names the file MediaMTX writes a path's segment to, relative to mount: recordPath (with the format's
// extension) with the path's name and the segment's start put in, the way MediaMTX's recordstore encodes them. MediaMTX
// lists a start in the time zone it read the file name in, so the start's own wall clock is the one in the name. false
// when the file would not be under mount.
func segmentFile(mount, recordPath, name string, start time.Time) (string, bool) {
	p := path.Clean(encode(strings.ReplaceAll(recordPath, "%path", name), start))
	rel, ok := strings.CutPrefix(p, mount+"/")
	return rel, ok && fs.ValidPath(rel)
}

// encode puts a time into a recordPath's time verbs (%z is Z or ±hhmm, as MediaMTX reads it back).
func encode(format string, t time.Time) string {
	two := func(v int) string { return fmt.Sprintf("%02d", v) }
	return strings.NewReplacer(
		"%Y", strconv.Itoa(t.Year()),
		"%m", two(int(t.Month())),
		"%d", two(t.Day()),
		"%H", two(t.Hour()),
		"%M", two(t.Minute()),
		"%S", two(t.Second()),
		"%f", fmt.Sprintf("%06d", t.Nanosecond()/1000),
		"%z", t.Format("Z0700"),
		"%s", strconv.FormatInt(t.Unix(), 10),
	).Replace(format)
}
