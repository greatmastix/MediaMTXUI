package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// box makes an MP4 box header claiming size bytes (the payload need not follow).
func box(size uint32, typ string) []byte {
	b := binary.BigEndian.AppendUint32(nil, size)
	return append(b, typ...)
}

func TestClipLimits(t *testing.T) {
	good := clip(t, testH264, testAAC(48000, 2))
	patched := func(typ string, at int, v uint32) []byte {
		b := bytes.Clone(good)
		i := bytes.Index(b, []byte(typ))
		if i < 0 {
			t.Fatalf("no %s box", typ)
		}
		binary.BigEndian.PutUint32(b[i+at:], v)
		return b
	}
	var free []byte
	for range maxClipBoxes {
		free = append(free, box(8, "free")...)
	}
	for _, tc := range []struct {
		name string
		file []byte
		want string // "" for none
	}{
		{"a real clip", good, ""},
		{"millions of samples in one entry", patched("stts", 12, 5_000_000), "too long"},
		{"samples larger than the file", patched("stsz", 8, 1<<30), string(notMP4)}, // the constant sample size
		{"a sample table larger than any clip's", append(bytes.Clone(good), box(maxClipTable+1, "stss")...), "too long"},
		{"thousands of empty boxes", append(bytes.Clone(good), free...), string(notMP4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := clipLimits(bytes.NewReader(tc.file), int64(len(tc.file)))
			var ue userError
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (!errors.As(err, &ue) || !bytes.Contains([]byte(ue), []byte(tc.want))):
				t.Fatalf("got %v, want a refusal with %q", err, tc.want)
			}
		})
	}
}
