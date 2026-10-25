package app

import (
	"errors"
	"io"
)

// bufferedReadSeeker buffers reads of an MP4 file for its box structure. go-mp4 (under clipLimits and pmp4) decodes
// boxes bit by bit and calls Read for every byte, and an *os.File makes each of those a system call: three million
// for an hour-long clip, about 5 s. With this, it is a few dozen. Reads as large as the buffer (sample payloads, when
// a clip is written back) go to the file directly instead of through the buffer.
type bufferedReadSeeker struct {
	r   io.ReadSeeker
	buf []byte
	at  int64 // the file position of buf[0]
	n   int   // bytes of buf that hold data
	pos int64 // the position the next Read reads from
	// rpos is where r is positioned, or -1 when unknown.
	rpos int64
}

// readBuffer is the buffer's size: a few dozen reads for the sample tables of an hour-long clip.
const readBuffer = 64 << 10

func newBufferedReadSeeker(r io.ReadSeeker) (*bufferedReadSeeker, error) {
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	return &bufferedReadSeeker{r: r, buf: make([]byte, readBuffer), pos: pos, rpos: pos}, nil
}

func (b *bufferedReadSeeker) seekTo(pos int64) error {
	if b.rpos == pos {
		return nil
	}
	if _, err := b.r.Seek(pos, io.SeekStart); err != nil {
		b.rpos = -1
		return err
	}
	b.rpos = pos
	return nil
}

func (b *bufferedReadSeeker) Read(p []byte) (int, error) {
	if b.pos >= b.at && b.pos < b.at+int64(b.n) {
		n := copy(p, b.buf[b.pos-b.at:b.n])
		b.pos += int64(n)
		return n, nil
	}
	if err := b.seekTo(b.pos); err != nil {
		return 0, err
	}
	if len(p) >= len(b.buf) {
		n, err := b.r.Read(p)
		b.pos += int64(n)
		b.rpos = b.pos
		return n, err
	}
	n, err := io.ReadFull(b.r, b.buf)
	b.rpos += int64(n)
	b.at, b.n = b.pos, n
	if n == 0 {
		return 0, err // io.ReadFull says io.EOF when it read nothing
	}
	c := copy(p, b.buf[:n])
	b.pos += int64(c)
	return c, nil
}

func (b *bufferedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += b.pos
	case io.SeekEnd:
		end, err := b.r.Seek(0, io.SeekEnd)
		b.rpos = end
		if err != nil {
			b.rpos = -1
			return 0, err
		}
		offset += end
	default:
		return 0, errors.New("invalid whence")
	}
	if offset < 0 {
		return 0, errors.New("negative position")
	}
	b.pos = offset
	return offset, nil
}
