// Package backup writes and reads the sidecar's backups: the database, the credential key,
// mediamtx.yml and the holding clips, in one file that only the backup passphrase opens.
//
// Keys: the server keeps an age X25519 key pair for backups. The public half encrypts every backup, so scheduled
// backups need no passphrase; the secret half is kept only wrapped with the passphrase (age's scrypt). Every backup
// carries that wrapped key, so the backup file and the passphrase are all a fresh server needs to restore it.
//
// File: a line "MTXUI-BACKUP 1", a line of JSON (the Header: when, what kind, which versions; nothing secret), the
// wrapped key (ASCII-armored age), then the payload: age-encrypted to the backup key, a gzipped tar of fixed entry
// names. Reading accepts exactly those names, regular files only, each once and within its size cap, and never uses
// a name from the archive as a path: entries land under fixed names in a directory the caller chose, holding clips
// numbered, with their names (checked against a strict pattern) returned for the caller to place.
package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// Magic is the first line of every backup.
const Magic = "MTXUI-BACKUP 1"

// FormatVersion is the payload's format.
const FormatVersion = 1

// WorkFactor is the scrypt work factor new keys are wrapped with: 2^16, 64 MiB and about a quarter second. Tests
// lower it.
var WorkFactor = 16

// maxWorkFactor is the most a backup's wrapped key may ask for before reading it is refused (more would cost the
// sidecar memory, not anyone security).
const maxWorkFactor = 18

// Size caps of what a backup may hold.
const (
	maxHeader   = 4 << 10
	maxKeyBlock = 8 << 10
	maxManifest = 64 << 10
	maxConfig   = 1 << 20
	MaxClip     = 64 << 20 // as an upload (app.maxHoldingBytes)
	maxClips    = 1000
	KeyLen      = 32 // the credential key
)

// Entry names in the payload.
const (
	entryManifest = "manifest.json"
	entryDB       = "mtxui.db"
	entryKey      = "credential-key"
	entryConfig   = "mediamtx.yml"
	clipPrefix    = "holding/"
)

// ClipName is the pattern a holding clip's name must match (the sidecar names them so: "1-e04546d7ec57.mp4",
// "offline-1080p50-aac.mp4").
var ClipName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,80}\.mp4$`)

// Header is a backup's unencrypted description, read for listings without the passphrase. Nothing in it is secret,
// and nothing in it is trusted: the manifest inside the encryption is what counts.
type Header struct {
	Created  time.Time `json:"created"`
	Kind     string    `json:"kind"` // scheduled, manual, before-restore
	Version  string    `json:"version"`
	MediaMTX string    `json:"mediamtx"`
	Host     string    `json:"host"`
}

// Manifest describes the payload, inside the encryption.
type Manifest struct {
	Format   int       `json:"format"`
	Created  time.Time `json:"created"`
	Version  string    `json:"version"`
	MediaMTX string    `json:"mediamtx"`
	Schema   int64     `json:"schema"`  // the database's migration version
	Holding  []string  `json:"holding"` // the clips, in the order they follow
}

// Errors a restore shows as they are.
var (
	ErrNotBackup  = errors.New("this is not a MediaMTX UI backup")
	ErrPassphrase = errors.New("the passphrase does not open this backup")
	ErrDamaged    = errors.New("the backup is damaged or was altered")
)

// NewKey makes a backup key: the recipient every backup is encrypted to, and the secret half wrapped with the
// passphrase (armored), which is stored and copied into every backup.
func NewKey(passphrase string) (recipient string, wrapped []byte, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", nil, err
	}
	sr, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return "", nil, err
	}
	sr.SetWorkFactor(WorkFactor)
	var b bytes.Buffer
	aw := armor.NewWriter(&b)
	w, err := age.Encrypt(aw, sr)
	if err != nil {
		return "", nil, err
	}
	if _, err := io.WriteString(w, id.String()); err != nil {
		return "", nil, err
	}
	if err := w.Close(); err != nil {
		return "", nil, err
	}
	if err := aw.Close(); err != nil {
		return "", nil, err
	}
	return id.Recipient().String(), b.Bytes(), nil
}

// unwrap opens a wrapped key with the passphrase.
func unwrap(wrapped []byte, passphrase string) (*age.X25519Identity, error) {
	si, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, ErrPassphrase
	}
	si.SetMaxWorkFactor(maxWorkFactor)
	r, err := age.Decrypt(armor.NewReader(bytes.NewReader(wrapped)), si)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrPassphrase
		}
		return nil, fmt.Errorf("%w: the key: %w", ErrDamaged, err)
	}
	s, err := io.ReadAll(io.LimitReader(r, 256))
	if err != nil {
		return nil, fmt.Errorf("%w: the key: %w", ErrDamaged, err)
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(s)))
	if err != nil {
		return nil, fmt.Errorf("%w: the key: %w", ErrDamaged, err)
	}
	return id, nil
}

// CheckPassphrase reports whether passphrase opens the wrapped key.
func CheckPassphrase(wrapped []byte, passphrase string) error {
	_, err := unwrap(wrapped, passphrase)
	return err
}

// File is one file to put into a backup.
type File struct {
	Path string // where it is read from
	Size int64
}

// Contents is what Write puts into the payload.
type Contents struct {
	Manifest Manifest
	DB       File
	Key      []byte // the credential key
	Config   []byte // mediamtx.yml
	Clips    []File // holding clips, named as Manifest.Holding
}

// Write writes a backup: the header, the wrapped key, and the payload encrypted to recipient.
func Write(dst io.Writer, h Header, recipient string, wrapped []byte, c Contents) error {
	rcpt, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return fmt.Errorf("the backup key: %w", err)
	}
	hj, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(dst, "%s\n%s\n", Magic, hj); err != nil {
		return err
	}
	if _, err := dst.Write(wrapped); err != nil {
		return err
	}
	enc, err := age.Encrypt(dst, rcpt)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(enc)
	tw := tar.NewWriter(z)
	put := func(name string, size int64, r io.Reader) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: c.Manifest.Created, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		n, err := io.Copy(tw, io.LimitReader(r, size))
		if err == nil && n != size {
			err = fmt.Errorf("%s: %d bytes instead of %d", name, n, size)
		}
		return err
	}
	putFile := func(name string, f File) error {
		in, err := os.Open(f.Path)
		if err != nil {
			return err
		}
		defer in.Close()
		return put(name, f.Size, in)
	}
	if len(c.Clips) != len(c.Manifest.Holding) {
		return errors.New("the clips and their names differ")
	}
	mj, err := json.Marshal(c.Manifest)
	if err != nil {
		return err
	}
	if err := put(entryManifest, int64(len(mj)), bytes.NewReader(mj)); err != nil {
		return err
	}
	if err := putFile(entryDB, c.DB); err != nil {
		return err
	}
	if err := put(entryKey, int64(len(c.Key)), bytes.NewReader(c.Key)); err != nil {
		return err
	}
	if err := put(entryConfig, int64(len(c.Config)), bytes.NewReader(c.Config)); err != nil {
		return err
	}
	for i, f := range c.Clips {
		if err := putFile(clipPrefix+c.Manifest.Holding[i], f); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := z.Close(); err != nil {
		return err
	}
	return enc.Close()
}

// Reader reads a backup's header and wrapped key, then (with the passphrase) its payload.
type Reader struct {
	Header  Header
	r       *bufio.Reader
	wrapped []byte
}

// Open reads a backup's header and wrapped key. It needs no passphrase.
func Open(src io.Reader) (*Reader, error) {
	br := bufio.NewReaderSize(src, 64<<10)
	line := func(limit int) (string, error) {
		var b []byte
		for {
			chunk, isPrefix, err := br.ReadLine()
			if err != nil {
				return "", ErrNotBackup
			}
			b = append(b, chunk...)
			if len(b) > limit {
				return "", ErrNotBackup
			}
			if !isPrefix {
				return string(b), nil
			}
		}
	}
	magic, err := line(64)
	if err != nil || magic != Magic {
		return nil, ErrNotBackup
	}
	hj, err := line(maxHeader)
	if err != nil {
		return nil, err
	}
	rd := &Reader{r: br}
	if err := json.Unmarshal([]byte(hj), &rd.Header); err != nil {
		return nil, ErrNotBackup
	}
	var key bytes.Buffer
	for {
		l, err := line(128)
		if err != nil {
			return nil, ErrNotBackup
		}
		key.WriteString(l + "\n")
		if key.Len() > maxKeyBlock {
			return nil, ErrNotBackup
		}
		if l == "-----END AGE ENCRYPTED FILE-----" {
			break
		}
	}
	rd.wrapped = key.Bytes()
	return rd, nil
}

// Limits bound what Extract writes.
type Limits struct {
	DB    int64 // the database's largest size
	Total int64 // everything together
}

// Extracted is a payload written out: its manifest and the holding clips' names, in the order of their files
// (Dir/clip-0, clip-1, …). The database, credential key and config are Dir/mtxui.db, credential-key, mediamtx.yml.
type Extracted struct {
	Manifest Manifest
	Clips    []string
}

// Extracted file names in the directory.
const (
	DBFile     = "mtxui.db"
	KeyFile    = "credential-key"
	ConfigFile = "mediamtx.yml"
)

// ClipFile is the name of the i-th clip in the directory.
func ClipFile(i int) string { return "clip-" + strconv.Itoa(i) }

// Extract opens the payload with the passphrase and writes it into dir (which must exist and be empty), checking
// every entry. An error leaves partial files behind for the caller to remove with dir.
func (rd *Reader) Extract(passphrase, dir string, lim Limits) (Extracted, error) {
	id, err := unwrap(rd.wrapped, passphrase)
	if err != nil {
		return Extracted{}, err
	}
	dec, err := age.Decrypt(rd.r, id)
	if err != nil {
		return Extracted{}, fmt.Errorf("%w: %w", ErrDamaged, err)
	}
	z, err := gzip.NewReader(dec)
	if err != nil {
		return Extracted{}, fmt.Errorf("%w: %w", ErrDamaged, err)
	}
	tr := tar.NewReader(z)
	var out Extracted
	seen := map[string]bool{}
	var total int64
	var manifest bool
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, fmt.Errorf("%w: %w", ErrDamaged, err)
		}
		if h.Typeflag != tar.TypeReg || seen[h.Name] || h.Size < 0 {
			return out, fmt.Errorf("%w: unexpected entry %q", ErrDamaged, h.Name)
		}
		seen[h.Name] = true
		if total += h.Size; total > lim.Total {
			return out, fmt.Errorf("%w: larger than %d MB", ErrDamaged, lim.Total>>20)
		}
		var limit int64
		var to string
		switch {
		case h.Name == entryManifest:
			if h.Size > maxManifest {
				return out, fmt.Errorf("%w: the manifest is too large", ErrDamaged)
			}
			b, err := io.ReadAll(tr)
			if err != nil || json.Unmarshal(b, &out.Manifest) != nil {
				return out, fmt.Errorf("%w: the manifest", ErrDamaged)
			}
			manifest = true
			continue
		case h.Name == entryDB:
			limit, to = lim.DB, DBFile
		case h.Name == entryKey:
			if h.Size != KeyLen {
				return out, fmt.Errorf("%w: the credential key", ErrDamaged)
			}
			limit, to = KeyLen, KeyFile
		case h.Name == entryConfig:
			limit, to = maxConfig, ConfigFile
		case strings.HasPrefix(h.Name, clipPrefix) && ClipName.MatchString(h.Name[len(clipPrefix):]):
			if len(out.Clips) >= maxClips {
				return out, fmt.Errorf("%w: too many clips", ErrDamaged)
			}
			limit, to = MaxClip, ClipFile(len(out.Clips))
			out.Clips = append(out.Clips, h.Name[len(clipPrefix):])
		default:
			return out, fmt.Errorf("%w: unexpected entry %q", ErrDamaged, h.Name)
		}
		if h.Size > limit {
			return out, fmt.Errorf("%w: %s is too large", ErrDamaged, h.Name)
		}
		if err := writeEntry(filepath.Join(dir, to), tr, h.Size); err != nil {
			return out, err
		}
	}
	// The end of the tar is inside the encryption; reading to the end of the stream checks the last chunk's tag (and
	// the gzip checksum). What follows the tar's end is padding, a few KB: more is not a backup.
	if n, err := io.Copy(io.Discard, io.LimitReader(z, 1<<20)); err != nil || n == 1<<20 {
		return out, fmt.Errorf("%w: its end (%w)", ErrDamaged, err)
	}
	if !manifest || !seen[entryDB] || !seen[entryKey] || !seen[entryConfig] {
		return out, fmt.Errorf("%w: something is missing", ErrDamaged)
	}
	if out.Manifest.Format != FormatVersion {
		return out, fmt.Errorf("%w: format %d, this server reads %d", ErrDamaged, out.Manifest.Format, FormatVersion)
	}
	if strings.Join(out.Manifest.Holding, "\n") != strings.Join(out.Clips, "\n") {
		return out, fmt.Errorf("%w: the clips do not match the manifest", ErrDamaged)
	}
	return out, nil
}

func writeEntry(path string, r io.Reader, size int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDamaged, err)
	}
	if n != size {
		return fmt.Errorf("%w: a file ends early", ErrDamaged)
	}
	return nil
}
