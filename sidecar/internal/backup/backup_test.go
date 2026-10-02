package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

const pass = "a long backup passphrase"

func TestMain(m *testing.M) {
	WorkFactor = 10 // fast; the format does not depend on it
	os.Exit(m.Run())
}

var lim = Limits{DB: 1 << 20, Total: 4 << 20}

func file(t *testing.T, dir, name string, content []byte) File {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return File{Path: p, Size: int64(len(content))}
}

func sample(t *testing.T) (wrapped, data []byte) {
	t.Helper()
	recipient, wrapped, err := NewKey(pass)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	c := Contents{
		Manifest: Manifest{Format: FormatVersion, Created: created, Version: "dev", MediaMTX: "1.21.1", Schema: 13, Holding: []string{"1-abc.mp4", "offline-720p50-aac.mp4"}},
		DB:       file(t, src, "db", []byte("SQLite format 3\x00 and more")),
		Key:      bytes.Repeat([]byte{7}, KeyLen),
		Config:   []byte("logLevel: info\n"),
		Clips:    []File{file(t, src, "a", []byte("clip a")), file(t, src, "b", []byte("clip b"))},
	}
	var b bytes.Buffer
	if err := Write(&b, Header{Created: created, Kind: "manual", Version: "dev"}, recipient, wrapped, c); err != nil {
		t.Fatal(err)
	}
	return wrapped, b.Bytes()
}

func extract(t *testing.T, data []byte, passphrase string) (Extracted, string, error) {
	t.Helper()
	rd, err := Open(bytes.NewReader(data))
	if err != nil {
		return Extracted{}, "", err
	}
	dir := t.TempDir()
	ex, err := rd.Extract(passphrase, dir, lim)
	return ex, dir, err
}

func TestRoundTrip(t *testing.T) {
	wrapped, data := sample(t)
	rd, err := Open(bytes.NewReader(data))
	if err != nil || rd.Header.Kind != "manual" {
		t.Fatalf("open: %v %+v", err, rd)
	}
	ex, dir, err := extract(t, data, pass)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Manifest.Schema != 13 || len(ex.Clips) != 2 || ex.Clips[1] != "offline-720p50-aac.mp4" {
		t.Fatalf("extracted %+v", ex)
	}
	for name, want := range map[string]string{
		DBFile: "SQLite format 3\x00 and more", KeyFile: strings.Repeat("\x07", KeyLen), ConfigFile: "logLevel: info\n",
		ClipFile(0): "clip a", ClipFile(1): "clip b",
	} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s: %q", name, got)
		}
	}
	if err := CheckPassphrase(wrapped, pass); err != nil {
		t.Error(err)
	}
	if err := CheckPassphrase(wrapped, "wrong"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
}

func TestRefused(t *testing.T) {
	_, data := sample(t)
	if _, _, err := extract(t, data, "not the passphrase"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	for _, bad := range [][]byte{nil, []byte("PK\x03\x04 a zip"), []byte(Magic + "\n{not json\n"), []byte(Magic + "\n{}\n" + strings.Repeat("x", 200))} {
		if _, err := Open(bytes.NewReader(bad)); !errors.Is(err, ErrNotBackup) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A flipped byte anywhere in the payload, or a cut-off file, fails authentication.
	start := bytes.Index(data, []byte("-----END AGE ENCRYPTED FILE-----\n")) + 34
	for _, at := range []int{start + 10, start + 200, len(data) - 5} {
		evil := bytes.Clone(data)
		evil[at] ^= 1
		if _, _, err := extract(t, evil, pass); !errors.Is(err, ErrDamaged) {
			t.Errorf("flipped byte at %d: %v", at, err)
		}
	}
	if _, _, err := extract(t, data[:len(data)-20], pass); !errors.Is(err, ErrDamaged) {
		t.Errorf("cut off: %v", err)
	}
	// The header is not trusted, so changing it changes nothing inside.
	evil := bytes.Replace(data, []byte(`"kind":"manual"`), []byte(`"kind":"scheduled"`), 1)
	if ex, _, err := extract(t, evil, pass); err != nil || ex.Manifest.Schema != 13 {
		t.Errorf("edited header: %v", err)
	}
}

type entry struct {
	name string
	typ  byte
	body string
	size int64 // when it differs from len(body)
}

// craft writes a properly encrypted backup with whatever entries: what someone with the passphrase could make.
func craft(t *testing.T, entries []entry, manifest *Manifest) []byte {
	t.Helper()
	recipient, wrapped, err := NewKey(pass)
	if err != nil {
		t.Fatal(err)
	}
	rcpt, _ := age.ParseX25519Recipient(recipient)
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n{}\n", Magic)
	b.Write(wrapped)
	enc, _ := age.Encrypt(&b, rcpt)
	z := gzip.NewWriter(enc)
	tw := tar.NewWriter(z)
	if manifest != nil {
		mj, _ := json.Marshal(manifest)
		entries = append([]entry{{name: "manifest.json", body: string(mj)}}, entries...)
	}
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		size := e.size
		if size == 0 {
			size = int64(len(e.body))
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Size: size, Mode: 0o600, Linkname: "/etc/passwd"}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(e.body))
	}
	_ = tw.Close()
	_ = z.Close()
	_ = enc.Close()
	return b.Bytes()
}

func TestMaliciousPayloads(t *testing.T) {
	m := &Manifest{Format: FormatVersion}
	ok := []entry{{name: "mtxui.db", body: "db"}, {name: "credential-key", body: strings.Repeat("k", KeyLen)}, {name: "mediamtx.yml", body: "x: 1"}}
	if _, _, err := extract(t, craft(t, ok, m), pass); err != nil {
		t.Fatalf("the crafted baseline: %v", err)
	}
	cases := map[string][]entry{
		"traversal":       append(ok[:3:3], entry{name: "../../etc/cron.d/x", body: "x"}),
		"absolute":        append(ok[:3:3], entry{name: "/etc/passwd", body: "x"}),
		"clip traversal":  append(ok[:3:3], entry{name: "holding/../../x.mp4", body: "x"}),
		"clip name":       append(ok[:3:3], entry{name: "holding/A b.mp4", body: "x"}),
		"symlink":         append(ok[:3:3], entry{name: "holding/x.mp4", typ: tar.TypeSymlink}),
		"directory":       append(ok[:3:3], entry{name: "holding/", typ: tar.TypeDir}),
		"duplicate":       append(ok[:3:3], entry{name: "mtxui.db", body: "again"}),
		"unknown":         append(ok[:3:3], entry{name: "setup-token", body: "x"}),
		"short key":       {ok[0], {name: "credential-key", body: "short"}, ok[2]},
		"missing config":  ok[:2],
		"huge database":   {{name: "mtxui.db", body: strings.Repeat("d", 2<<20)}, ok[1], ok[2]},
		"clip not listed": append(ok[:3:3], entry{name: "holding/x.mp4", body: "x"}),
		"huge config":     {ok[0], ok[1], {name: "mediamtx.yml", body: strings.Repeat("y", 2<<20)}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			_, dir, err := extract(t, craft(t, entries, m), pass)
			if !errors.Is(err, ErrDamaged) {
				t.Fatalf("accepted: %v", err)
			}
			// Nothing was written outside the directory, and nothing under a name from the archive.
			got, _ := os.ReadDir(dir)
			for _, e := range got {
				if n := e.Name(); n != DBFile && n != KeyFile && n != ConfigFile && !strings.HasPrefix(n, "clip-") {
					t.Errorf("wrote %q", n)
				}
			}
		})
	}
	if _, _, err := extract(t, craft(t, ok, nil), pass); !errors.Is(err, ErrDamaged) {
		t.Errorf("without a manifest: %v", err)
	}
	if _, _, err := extract(t, craft(t, ok, &Manifest{Format: 2}), pass); !errors.Is(err, ErrDamaged) {
		t.Errorf("a newer format: %v", err)
	}
}

// A wrapped key that asks for more scrypt memory than allowed is refused before any work.
func TestWorkFactorCap(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	sr, _ := age.NewScryptRecipient(pass)
	sr.SetWorkFactor(maxWorkFactor + 1)
	var b bytes.Buffer
	w, _ := age.Encrypt(&b, sr)
	_, _ = w.Write([]byte(id.String()))
	_ = w.Close()
	start := time.Now()
	if _, err := unwrap(append([]byte("-----BEGIN AGE ENCRYPTED FILE-----\n"), b.Bytes()...), pass); err == nil {
		t.Fatal("accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("did the work before refusing")
	}
}

func FuzzOpen(f *testing.F) {
	f.Add([]byte(Magic + "\n{}\n-----BEGIN AGE ENCRYPTED FILE-----\nYWdl\n-----END AGE ENCRYPTED FILE-----\nxyz"))
	f.Fuzz(func(t *testing.T, data []byte) {
		rd, err := Open(bytes.NewReader(data))
		if err != nil {
			return
		}
		_, _ = rd.Extract(pass, t.TempDir(), lim)
	})
}
