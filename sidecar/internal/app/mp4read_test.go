package app

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"
)

// countingReader counts the Read calls that reach the file.
type countingReader struct {
	io.ReadSeeker
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.ReadSeeker.Read(p)
}

// longClip makes an MP4 with samples video and audio frames each, whose sample tables are what go-mp4 reads byte by byte.
func longClip(t *testing.T, samples int) []byte {
	t.Helper()
	var p pmp4.Presentation
	for i, c := range []codecs.Codec{testH264, testAAC(48000, 2)} {
		tr := &pmp4.Track{ID: i + 1, TimeScale: 90000, Codec: c}
		for n := range samples {
			payload := []byte{byte(n), byte(n >> 8), byte(i)}
			tr.Samples = append(tr.Samples, &pmp4.Sample{
				Duration: 1500, PayloadSize: uint32(len(payload)), IsNonSyncSample: n%50 != 0,
				GetPayload: func() ([]byte, error) { return payload, nil },
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

// A clip's box structure and samples are read in a few dozen reads, not one a byte, and come out the same.
func TestBufferedClipReads(t *testing.T) {
	b := longClip(t, 20_000)
	written := func(r io.ReadSeeker) []byte {
		t.Helper()
		if err := clipLimits(r, int64(len(b))); err != nil {
			t.Fatal(err)
		}
		var p pmp4.Presentation
		if err := p.Unmarshal(r); err != nil {
			t.Fatal(err)
		}
		var out seekBuffer
		if err := p.Marshal(&out); err != nil { // the samples, read back through r
			t.Fatal(err)
		}
		return out.b
	}
	plain := &countingReader{ReadSeeker: bytes.NewReader(b)}
	direct := written(plain)
	counted := &countingReader{ReadSeeker: bytes.NewReader(b)}
	br, err := newBufferedReadSeeker(counted)
	if err != nil {
		t.Fatal(err)
	}
	if got := written(br); !bytes.Equal(got, direct) {
		t.Fatal("the clip read through the buffer comes out different")
	}
	if counted.reads*100 > plain.reads {
		t.Errorf("%d reads through the buffer, %d without", counted.reads, plain.reads)
	}
	t.Logf("reads: %d without the buffer, %d with it", plain.reads, counted.reads)
}

// The buffered reader reads, seeks and ends exactly as what it wraps does, for any sequence of reads and seeks.
func TestBufferedReadSeekerMatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	data := make([]byte, 3*readBuffer+123)
	for i := range data {
		data[i] = byte(rng.Uint32())
	}
	ops := make([]byte, 4000)
	for i := range ops {
		ops[i] = byte(rng.Uint32())
	}
	compareReaders(t, data, ops)
}

func FuzzBufferedReadSeeker(f *testing.F) {
	f.Add([]byte("hello, world"), []byte{0, 5, 1, 200, 2, 3, 0, 255})
	f.Fuzz(func(t *testing.T, data, ops []byte) {
		compareReaders(t, data, ops)
	})
}

// compareReaders runs ops (pairs of a kind and a size) against a plain and a buffered reader of data.
func compareReaders(t *testing.T, data, ops []byte) {
	t.Helper()
	plain := bytes.NewReader(data)
	br, err := newBufferedReadSeeker(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	size := func(b byte) int { return int(b) * int(b) * 2 } // up to 130 KiB: past the buffer's size too
	for i := 0; i+1 < len(ops); i += 2 {
		arg := ops[i+1]
		switch ops[i] % 4 {
		case 0, 1: // read: as much as one Read gives, then the rest with ReadFull
			n := size(arg)
			pa, pb := make([]byte, n), make([]byte, n)
			na, ea := io.ReadFull(plain, pa)
			nb, eb := io.ReadFull(br, pb)
			if na != nb || !bytes.Equal(pa[:na], pb[:nb]) || (ea == nil) != (eb == nil) || ea != nil && !errors.Is(eb, ea) {
				t.Fatalf("op %d: read %d: plain %d %v, buffered %d %v", i, n, na, ea, nb, eb)
			}
		case 2: // seek from the start or the current position
			whence, off := io.SeekStart, int64(size(arg))
			if arg%2 == 1 {
				whence, off = io.SeekCurrent, int64(size(arg))-8000
			}
			pa, ea := plain.Seek(off, whence)
			pb, eb := br.Seek(off, whence)
			if (ea == nil) != (eb == nil) || ea == nil && pa != pb {
				t.Fatalf("op %d: seek %d/%d: plain %d %v, buffered %d %v", i, off, whence, pa, ea, pb, eb)
			}
			if ea != nil { // both refused a negative position: put them back in step
				_, _ = plain.Seek(0, io.SeekStart)
				_, _ = br.Seek(0, io.SeekStart)
			}
		case 3: // seek from the end
			off := -int64(size(arg))
			pa, ea := plain.Seek(off, io.SeekEnd)
			pb, eb := br.Seek(off, io.SeekEnd)
			if (ea == nil) != (eb == nil) || ea == nil && pa != pb {
				t.Fatalf("op %d: seek end%d: plain %d %v, buffered %d %v", i, off, pa, ea, pb, eb)
			}
			if ea != nil {
				_, _ = plain.Seek(0, io.SeekStart)
				_, _ = br.Seek(0, io.SeekStart)
			}
		}
	}
}
