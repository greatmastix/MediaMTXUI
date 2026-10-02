package yamledit

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/mtxtest"
)

const sample = `###############################################
# Global settings

# Log level.
logLevel: info # debug, info, warn, error
rtsp: yes
rtspTransports: [tcp]
rtspAddress: ":8554"

# Paths
paths:
  # The test pattern.
  test:
    source: publisher
    record: no

  all_others:

# The end.
`

func edit(t *testing.T, src string, f func(*Doc) error) string {
	t.Helper()
	d, err := New([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := f(d); err != nil {
		t.Fatal(err)
	}
	return string(d.Bytes())
}

func TestGolden(t *testing.T) {
	for _, tt := range []struct {
		name string
		f    func(*Doc) error
		want string // the whole result, as a replacement of old with new in sample
		old  string
	}{
		{
			name: "an inline scalar, keeping its comment",
			f:    func(d *Doc) error { return d.Set([]string{"logLevel"}, "debug") },
			old:  "logLevel: info # debug", want: "logLevel: debug # debug",
		},
		{
			name: "a boolean, written as MediaMTX does",
			f:    func(d *Doc) error { return d.Set([]string{"rtsp"}, false) },
			old:  "rtsp: yes", want: "rtsp: no",
		},
		{
			name: "a quoted scalar",
			f:    func(d *Doc) error { return d.Set([]string{"rtspAddress"}, ":9554") },
			old:  `rtspAddress: ":8554"`, want: `rtspAddress: :9554`,
		},
		{
			name: "a flow sequence, inline when it fits",
			f:    func(d *Doc) error { return d.Set([]string{"rtspTransports"}, []string{}) },
			old:  "rtspTransports: [tcp]", want: "rtspTransports: []",
		},
		{
			name: "a flow sequence becomes a block one",
			f:    func(d *Doc) error { return d.Set([]string{"rtspTransports"}, []string{"tcp", "udp"}) },
			old:  "rtspTransports: [tcp]", want: "rtspTransports:\n  - tcp\n  - udp",
		},
		{
			name: "a nested scalar",
			f:    func(d *Doc) error { return d.Set([]string{"paths", "test", "record"}, true) },
			old:  "    record: no", want: "    record: yes",
		},
		{
			name: "a new key goes after the mapping's last entry, before the blank line and comments that follow",
			f:    func(d *Doc) error { return d.Set([]string{"paths", "test", "sourceOnDemand"}, true) },
			old:  "    record: no\n", want: "    record: no\n    sourceOnDemand: yes\n",
		},
		{
			name: "a new path with a nested config",
			f: func(d *Doc) error {
				return d.Set([]string{"paths", "cam1"}, map[string]any{"source": "rtsp://10.0.0.5/live", "sourceOnDemand": true})
			},
			old: "  all_others:\n", want: "  all_others:\n  cam1:\n    source: rtsp://10.0.0.5/live\n    sourceOnDemand: yes\n",
		},
		{
			name: "below a null value",
			f:    func(d *Doc) error { return d.Set([]string{"paths", "all_others", "record"}, true) },
			old:  "  all_others:\n", want: "  all_others:\n    record: yes\n",
		},
		{
			name: "a new top-level key goes before the closing comments",
			f:    func(d *Doc) error { return d.Set([]string{"hls"}, false) },
			old:  "  all_others:\n\n# The end.", want: "  all_others:\nhls: no\n\n# The end.",
		},
		{
			name: "a scalar replaced by a mapping",
			f:    func(d *Doc) error { return d.Set([]string{"logLevel", "x"}, 1) },
			old:  "logLevel: info # debug, info, warn, error", want: "logLevel:\n  x: 1",
		},
		{
			name: "deleting takes the comment above along",
			f:    func(d *Doc) error { return d.Delete([]string{"paths", "test"}) },
			old:  "  # The test pattern.\n  test:\n    source: publisher\n    record: no\n\n", want: "\n",
		},
		{
			name: "deleting the only key leaves an empty mapping",
			f: func(d *Doc) error {
				if err := d.Delete([]string{"paths", "test"}); err != nil {
					return err
				}
				return d.Delete([]string{"paths", "all_others"})
			},
			old:  "paths:\n  # The test pattern.\n  test:\n    source: publisher\n    record: no\n\n  all_others:\n",
			want: "paths: {}\n",
		},
		{
			name: "deleting a nested key",
			f:    func(d *Doc) error { return d.Delete([]string{"paths", "test", "record"}) },
			old:  "    record: no\n", want: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(sample, tt.old) {
				t.Fatalf("test bug: %q is not in the sample", tt.old)
			}
			want := strings.Replace(sample, tt.old, tt.want, 1)
			if got := edit(t, sample, tt.f); got != want {
				t.Errorf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestEdgeCases(t *testing.T) {
	if got := edit(t, "", func(d *Doc) error { return d.Set([]string{"a", "b"}, 1) }); got != "a:\n  b: 1\n" {
		t.Errorf("empty document: %q", got)
	}
	if got := edit(t, "# only a comment\n", func(d *Doc) error { return d.Set([]string{"a"}, "x") }); got != "# only a comment\na: x\n" {
		t.Errorf("comment-only document: %q", got)
	}
	if got := edit(t, "a: 1", func(d *Doc) error { return d.Set([]string{"b"}, 2) }); got != "a: 1\nb: 2\n" {
		t.Errorf("no final newline: %q", got)
	}
	if got := edit(t, "a: 1\nb: {x: yes, y: 2}\n", func(d *Doc) error { return d.Set([]string{"b", "y"}, 3) }); got != "a: 1\nb:\n  x: yes\n  \"y\": 3\n" {
		t.Errorf("into a flow mapping, keeping its boolean unquoted: %q", got)
	}
	if got := edit(t, "a: 1\nb: {x: 1, y: 2}\n", func(d *Doc) error { return d.Delete([]string{"b", "x"}) }); got != "a: 1\nb:\n  \"y\": 2\n" {
		t.Errorf("out of a flow mapping: %q", got)
	}
	if got := edit(t, "cmd: |\n  echo 1\n  echo 2\nnext: 1\n", func(d *Doc) error { return d.Set([]string{"cmd"}, "echo 3") }); got != "cmd: echo 3\nnext: 1\n" {
		t.Errorf("a block scalar: %q", got)
	}
	if got := edit(t, "a: 'it''s' # c\n", func(d *Doc) error { return d.Set([]string{"a"}, "b") }); got != "a: b # c\n" {
		t.Errorf("a single-quoted scalar: %q", got)
	}
	if got := edit(t, "a: x\n", func(d *Doc) error { return d.Set([]string{"a"}, "line1\nline2\n") }); got != "a: |\n  line1\n  line2\n" {
		t.Errorf("a multi-line string: %q", got)
	}
	if got := edit(t, "a: x\n", func(d *Doc) error { return d.Set([]string{"a"}, nil) }); got != "a:\n" {
		t.Errorf("null: %q", got)
	}
	if got := edit(t, "a: x\n", func(d *Doc) error { return d.Set([]string{"a"}, "yes") }); got != "a: \"yes\"\n" {
		t.Errorf("the string yes: %q", got)
	}
	if got := edit(t, "paths:\n  ~^cam: {}\n", func(d *Doc) error { return d.Set([]string{"paths", "~^cam", "record"}, true) }); got != "paths:\n  ~^cam:\n    record: yes\n" {
		t.Errorf("a regex path name: %q", got)
	}

	d, _ := New([]byte(sample))
	if err := d.Delete([]string{"paths", "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a missing key: %v", err)
	}
	if err := d.Delete([]string{"logLevel", "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting below a scalar: %v", err)
	}
	if string(d.Bytes()) != sample {
		t.Error("a failed edit changed the document")
	}
	for _, bad := range []string{"- a\n- b\n", "{a: 1}\n", "a: [\n"} {
		if _, err := New([]byte(bad)); err == nil {
			t.Errorf("New(%q) accepted it", bad)
		}
	}
}

// changedLines reports the first and last line that differ between a and b, counted from each end, so an edit can
// be checked against the lines it was meant to touch.
func changedLines(a, b string) (prefix, suffix int) {
	la, lb := strings.SplitAfter(a, "\n"), strings.SplitAfter(b, "\n")
	for prefix < len(la) && prefix < len(lb) && la[prefix] == lb[prefix] {
		prefix++
	}
	for suffix < len(la)-prefix && suffix < len(lb)-prefix && la[len(la)-1-suffix] == lb[len(lb)-1-suffix] {
		suffix++
	}
	return prefix, suffix
}

// On MediaMTX's own reference config, which is mostly comments: toggling each top-level scalar changes exactly one
// line, and the file reads back as intended.
func TestReferenceTopLevel(t *testing.T) {
	ref, err := os.ReadFile(mtxtest.Reference(t))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := parse(ref)
	lines := len(strings.SplitAfter(string(ref), "\n")) // the element after the final newline counts too
	for i := 0; i < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			continue
		}
		t.Run(k.Value, func(t *testing.T) {
			t.Parallel() // each key edits its own copy of the reference
			d, _ := New(ref)
			if err := d.Set([]string{k.Value}, "changed-value"); err != nil {
				t.Fatal(err)
			}
			out := string(d.Bytes())
			prefix, suffix := changedLines(string(ref), out)
			if len(strings.SplitAfter(out, "\n")) != lines || prefix+suffix != lines-1 || prefix != k.Line-1 {
				t.Errorf("changed lines %d..%d of %d, want only line %d", prefix+1, lines-suffix, lines, k.Line)
			}
		})
	}
}

// A long random sequence of edits on the reference config: every edit must succeed (the engine refuses anything
// whose result does not decode as intended), and the result must match a model that applied the same edits to the
// decoded config.
func TestRandomEdits(t *testing.T) {
	ref, err := os.ReadFile(mtxtest.Reference(t))
	if err != nil {
		t.Fatal(err)
	}
	// Each edit re-parses the 900-line file several times, so the long runs use the small sample; the reference config
	// gets a shorter one. (3,200 edits on it passed when this was written; raise the numbers when touching the engine.)
	// Independent sequences: they run in parallel (seconds instead of half a minute under -race).
	for seed := range uint64(3) {
		t.Run(fmt.Sprint("reference, seed ", seed), func(t *testing.T) {
			t.Parallel()
			randomEdits(t, ref, seed, 80)
		})
	}
	for seed := range uint64(40) {
		t.Run(fmt.Sprint("sample, seed ", seed), func(t *testing.T) {
			t.Parallel()
			randomEdits(t, []byte(sample), seed, 60)
		})
	}
}

func randomEdits(t *testing.T, src []byte, seed uint64, n int) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 42))
	d, err := New(src)
	if err != nil {
		t.Fatal(err)
	}
	model, _ := decodeDoc(src)
	values := []any{
		"x", "yes", "a string with: a colon", 7, 1.5, true, false, nil,
		[]any{"tcp"},
		[]any{},
		map[string]any{},
		map[string]any{"source": "publisher", "record": true},
		"multi\nline\n", "#not a comment", "~^re.*",
	}
	for i := range n {
		paths := keyPaths(model, nil)
		var path []string
		op := r.IntN(10)
		switch {
		case op < 4 && len(paths) > 0: // change an existing key
			path = paths[r.IntN(len(paths))]
		case op < 7: // add a key somewhere
			parents := append([][]string{{}}, paths...)
			path = append(slices.Clone(parents[r.IntN(len(parents))]), fmt.Sprintf("k%d", i))
		case len(paths) > 0: // delete
			path = paths[r.IntN(len(paths))]
			before := string(d.Bytes())
			if err := d.Delete(path); err != nil {
				t.Fatalf("edit %d: delete %v: %v\n%s", i, path, err, before)
			}
			model, _ = deleteIn(model, path)
			fixEmpty(model, path[:len(path)-1])
			continue
		default:
			continue
		}
		v := values[r.IntN(len(values))]
		before := string(d.Bytes())
		if err := d.Set(path, v); err != nil {
			t.Fatalf("edit %d: set %v = %#v: %v\n%s", i, path, v, err, before)
		}
		rv, _ := renderedValue(v)
		model = setIn(model, path, rv)
	}
	got, _ := decodeDoc(d.Bytes())
	if !reflect.DeepEqual(got, model) {
		t.Fatal("the document drifted from the model")
	}
}

// fixEmpty mirrors Delete's rule: a nested mapping emptied by a delete stays an empty mapping.
func fixEmpty(m map[string]any, path []string) {
	for _, k := range path {
		m, _ = m[k].(map[string]any)
	}
}

func keyPaths(m map[string]any, prefix []string) [][]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out [][]string
	for _, k := range keys {
		p := append(slices.Clone(prefix), k)
		out = append(out, p)
		if sub, ok := m[k].(map[string]any); ok {
			out = append(out, keyPaths(sub, p)...)
		}
	}
	return out
}

// FuzzEdits drives edits on the sample from fuzz input; `go test` runs the seeds, `go test -fuzz` explores.
func FuzzEdits(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{9, 9, 9, 0, 0, 0, 200, 13, 77})
	f.Fuzz(func(t *testing.T, data []byte) {
		seed := uint64(0)
		for _, b := range data {
			seed = seed*31 + uint64(b)
		}
		randomEdits(t, []byte(sample), seed, 30)
	})
}
