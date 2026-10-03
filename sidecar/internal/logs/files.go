package logs

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"time"
)

// rotated is the name of the n-th rotated copy of the log at path (1 is the newest).
func rotated(path string, n int) string { return path + "." + strconv.Itoa(n) + ".gz" }

// Files returns the log at path and its rotated copies that exist, oldest first.
func Files(path string, keep int) []string {
	var out []string
	for n := keep; n >= 1; n-- {
		if _, err := os.Stat(rotated(path, n)); err == nil {
			out = append(out, rotated(path, n))
		}
	}
	return append(out, path)
}

// Search reads MediaMTX's log and its rotated copies and returns the newest lines that pass f, at most limit,
// oldest first, and whether older matches were left out. Memory stays bounded by limit, however large the files.
func Search(ctx context.Context, path string, keep int, f Filter, limit int) ([]Line, bool, error) {
	ring := make([]Line, 0, limit)
	start, more := 0, false
	for _, name := range Files(path, keep) {
		err := eachLine(name, func(s string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			l := ParseMediaMTX(s)
			if !f.Match(l) {
				return nil
			}
			if len(ring) < limit {
				ring = append(ring, l)
			} else {
				ring[start] = l
				start = (start + 1) % limit
				more = true
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, false, err
		}
	}
	return append(ring[start:], ring[:start]...), more, nil
}

// eachLine calls fn for every line of a log file, gzipped or not; overlong lines are cut, not buffered whole.
func eachLine(name string, fn func(string) error) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if len(name) > 3 && name[len(name)-3:] == ".gz" {
		z, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		defer z.Close()
		r = z
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(line) < maxText {
			line = append(line, chunk...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) > 0 {
					return fn(string(line))
				}
				return nil
			}
			return err
		}
		if isPrefix {
			continue
		}
		if err := fn(string(line)); err != nil {
			return err
		}
		line = line[:0]
	}
}

// Rotate rotates the log at path by copy-truncate when it is larger than maxBytes: its content goes to path.1.gz
// (older copies move up one, and the one past keep is deleted), then the file is truncated. MediaMTX keeps its
// file open and appends, so it simply continues at the start; lines written between the copy and the
// truncate are lost, a window of microseconds. With keep 0 the content is dropped.
//
// The new copy is written before any older one moves, so a rotation that cannot write it (a full disk) leaves the
// copies as they are: retried every minute, it does not delete them one by one.
func Rotate(path string, maxBytes int64, keep int) (bool, error) {
	st, err := os.Stat(path)
	if err != nil || st.Size() <= maxBytes {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return false, err
	}
	if keep > 0 {
		tmp := rotated(path, 1) + ".tmp"
		if err := compress(path, tmp); err != nil {
			return false, err
		}
		// Each copy moves over the next one up; the one at keep is replaced, so dropped.
		for n := keep - 1; n >= 1; n-- {
			if err := os.Rename(rotated(path, n), rotated(path, n+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				_ = os.Remove(tmp)
				return false, err
			}
		}
		if err := os.Rename(tmp, rotated(path, 1)); err != nil {
			_ = os.Remove(tmp)
			return false, err
		}
	}
	return true, os.Truncate(path, 0)
}

// compress writes src gzipped to dst, which it removes again when that fails.
func compress(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(out)
	_, err = io.Copy(z, in)
	if cerr := z.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}

// RunRotation checks the log every interval until ctx ends; report hears of every rotation and failure.
func RunRotation(ctx context.Context, path string, maxBytes int64, keep int, every time.Duration, report func(error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if ok, err := Rotate(path, maxBytes, keep); ok || err != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Copy writes a log file's content to w, decompressing a rotated copy.
func Copy(ctx context.Context, w io.Writer, name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if len(name) > 3 && name[len(name)-3:] == ".gz" {
		z, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		defer z.Close()
		r = z
	}
	_, err = io.Copy(w, ctxReader{ctx, r})
	return err
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
