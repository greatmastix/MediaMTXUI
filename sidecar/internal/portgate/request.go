package portgate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ErrNoRequest means there is no desired.json yet: nothing is wanted.
var ErrNoRequest = errors.New("no desired state")

// ReadRequest reads the sidecar's desired.json the way a root process must read a file someone else controls: neither
// the file nor its directory may be a symlink, it must be a regular file (no FIFO to hang on), at most MaxRequest
// bytes, and strict JSON with no unknown fields.
func ReadRequest(path string) (Desired, error) {
	dir, err := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Desired{}, fmt.Errorf("request directory: %w", err)
	}
	defer syscall.Close(dir)
	fd, err := syscall.Openat(dir, filepath.Base(path), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return Desired{}, ErrNoRequest
	}
	if err != nil {
		return Desired{}, fmt.Errorf("request file (a symlink is refused): %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Desired{}, err
	}
	if !st.Mode().IsRegular() {
		return Desired{}, errors.New("request is not a regular file")
	}
	if st.Size() > MaxRequest {
		return Desired{}, fmt.Errorf("request is %d bytes, at most %d", st.Size(), MaxRequest)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxRequest+1))
	if err != nil {
		return Desired{}, err
	}
	if len(b) > MaxRequest {
		return Desired{}, fmt.Errorf("request is over %d bytes", MaxRequest)
	}
	var d Desired
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Desired{}, fmt.Errorf("request: %w", err)
	}
	if dec.More() {
		return Desired{}, errors.New("request: trailing data")
	}
	return d, nil
}
