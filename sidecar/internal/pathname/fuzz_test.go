package pathname

import (
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// An accepted path name is safe wherever it is used: as a file path under the recordings directory (recordPath's
// %path), in URLs and in mediamtx.yml.
func FuzzValid(f *testing.F) {
	for _, s := range []string{"live/a", "a..b", "../x", "a//b", "/a", "a/", "a/./b", "x\x00y", "ä", "~^live/.*$"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if Valid(name) != nil {
			_ = ValidPattern(name)
			return
		}
		if path.Clean(name) != name || strings.Contains(name, "..") && hasDotDotSegment(name) {
			t.Fatalf("accepted %q, which is not clean", name)
		}
		joined := filepath.Join("/recordings", name)
		if !strings.HasPrefix(joined, "/recordings/") {
			t.Fatalf("accepted %q, which leaves the recordings directory: %s", name, joined)
		}
		for _, r := range name {
			if r < 0x21 || r > 0x7e || strings.ContainsRune(`"'#:?%& \`, r) {
				t.Fatalf("accepted %q with %q, which needs quoting in YAML or escaping in URLs", name, r)
			}
		}
		if ValidPattern(name) != nil {
			t.Fatalf("a valid name %q is not a valid pattern", name)
		}
	})
}

func hasDotDotSegment(name string) bool {
	for _, s := range strings.Split(name, "/") {
		if s == ".." || s == "." {
			return true
		}
	}
	return false
}
