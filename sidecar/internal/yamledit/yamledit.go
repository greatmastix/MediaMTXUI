// Package yamledit changes a YAML document the way a careful person would: only the lines of the entry being
// changed are touched, every other byte (comments, blank lines, key order, quoting) stays as it was. A decode/encode
// round trip is no option for mediamtx.yml, which is mostly comments.
//
// go.yaml.in/yaml/v3 gives node start positions but no end positions, so an entry's extent is worked out from the
// lines: it runs from its key to the line before the next sibling key, minus trailing blank lines and comments that
// belong to what follows. That is a heuristic, so every edit is checked: the edited document is parsed again and must
// decode to exactly the original with the intended change applied. An edit that fails the check is refused, never
// returned.
//
// Addresses are paths of mapping keys from the root, e.g. ["paths", "cam1", "source"]. Values are Go values, rendered
// in block style with two-space indentation and MediaMTX's yes/no booleans. Sequences are replaced as a whole.
package yamledit

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// ErrNotFound is returned when deleting a key that does not exist.
var ErrNotFound = errors.New("no such key")

// Doc is a YAML document whose root is a block mapping (or that is empty).
type Doc struct {
	src []byte
}

// New checks that src is a YAML document with a mapping at its root.
func New(src []byte) (*Doc, error) {
	if _, err := parse(src); err != nil {
		return nil, err
	}
	return &Doc{src: append([]byte(nil), src...)}, nil
}

// Bytes returns the document.
func (d *Doc) Bytes() []byte { return append([]byte(nil), d.src...) }

// Set sets the value at path, creating missing mappings on the way. A key whose value is not a mapping (a scalar, a
// sequence, null) is turned into one when the path continues below it.
func (d *Doc) Set(path []string, value any) error {
	if len(path) == 0 {
		return errors.New("yamledit: empty path")
	}
	want, err := decodeDoc(d.src)
	if err != nil {
		return err
	}
	rendered, err := renderedValue(value)
	if err != nil {
		return err
	}
	want = setIn(want, path, rendered)
	out, err := d.set(path, value)
	if err != nil {
		return err
	}
	return d.commit(out, want, "set "+strings.Join(path, "."))
}

// Delete removes the key at path with its value and the comment lines directly above it. Removing the last key of a
// nested mapping leaves the mapping empty ({}), not null.
func (d *Doc) Delete(path []string) error {
	if len(path) == 0 {
		return errors.New("yamledit: empty path")
	}
	want, err := decodeDoc(d.src)
	if err != nil {
		return err
	}
	var found bool
	if want, found = deleteIn(want, path); !found {
		return fmt.Errorf("%w: %s", ErrNotFound, strings.Join(path, "."))
	}
	out, err := d.delete(path)
	if err != nil {
		return err
	}
	return d.commit(out, want, "delete "+strings.Join(path, "."))
}

// commit accepts out only if it decodes to want.
func (d *Doc) commit(out []byte, want map[string]any, what string) error {
	got, err := decodeDoc(out)
	if err != nil {
		return fmt.Errorf("yamledit: %s produced an unparsable document: %w", what, err)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("yamledit: %s: the edited document does not decode to the intended config; refusing it", what)
	}
	if _, err := parse(out); err != nil {
		return fmt.Errorf("yamledit: %s: %w", what, err)
	}
	d.src = out
	return nil
}

// level is a mapping on the way down a path, with the extent of the entry that holds it.
type level struct {
	m   *yaml.Node // a block mapping with at least one entry
	end int        // last line (1-based) of the mapping's region
	key *yaml.Node // the key that holds m; nil for the root
}

func (d *Doc) set(path []string, value any) ([]byte, error) {
	root, err := parse(d.src)
	if err != nil {
		return nil, err
	}
	t := newText(d.src)
	if root == nil { // an empty document (or only comments): append
		return t.insertAt(len(d.src), render(path[0], nest(path[1:], value), 0))
	}
	lv := level{m: root, end: t.lines()}
	for i, key := range path {
		idx := find(lv.m, key)
		if idx < 0 {
			return t.insertInto(lv, key, nest(path[i+1:], value))
		}
		k, v := lv.m.Content[idx], lv.m.Content[idx+1]
		end := t.entryEnd(lv, idx)
		if i == len(path)-1 {
			if out, ok := t.replaceInline(k, v, end, value); ok {
				return out, nil
			}
			return t.replaceEntry(k, end, value)
		}
		if v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 {
			lv = level{m: v, end: end, key: k}
			continue
		}
		// Not a block mapping to descend into (null, {}, a flow mapping, a scalar, a sequence): rewrite this entry.
		return t.replaceEntry(k, end, setInAny(toValue(v), path[i+1:], value))
	}
	return nil, errors.New("unreachable")
}

func (d *Doc) delete(path []string) ([]byte, error) {
	root, err := parse(d.src)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, ErrNotFound
	}
	t := newText(d.src)
	lv := level{m: root, end: t.lines()}
	var parent *level
	for i, key := range path {
		idx := find(lv.m, key)
		if idx < 0 {
			return nil, ErrNotFound
		}
		k, v := lv.m.Content[idx], lv.m.Content[idx+1]
		end := t.entryEnd(lv, idx)
		if i == len(path)-1 {
			if len(lv.m.Content) == 2 && parent != nil {
				// The only key: the mapping becomes {}, not null (which MediaMTX would read differently).
				return t.replaceEntry(lv.key, parent.endOf(t, lv.key), map[string]any{})
			}
			return t.remove(t.headCommentStart(k), end), nil
		}
		if v.Kind != yaml.MappingNode || len(v.Content) == 0 {
			return nil, ErrNotFound
		}
		if v.Style&yaml.FlowStyle != 0 { // rewrite the flow mapping without the key
			rest, _ := deleteIn(toValue(v).(map[string]any), path[i+1:])
			return t.replaceEntry(k, end, rest)
		}
		p := lv
		parent = &p
		lv = level{m: v, end: end, key: k}
	}
	return nil, errors.New("unreachable")
}

// endOf returns the last line of the entry for key k in this level's mapping.
func (lv *level) endOf(t *text, k *yaml.Node) int {
	for i := 0; i < len(lv.m.Content); i += 2 {
		if lv.m.Content[i] == k {
			return t.entryEnd(*lv, i)
		}
	}
	panic("yamledit: key not in its mapping")
}

func parse(src []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("yamledit: not valid YAML: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	switch {
	case root.Kind != yaml.MappingNode:
		return nil, errors.New("yamledit: the document is not a mapping")
	case root.Style&yaml.FlowStyle != 0:
		return nil, errors.New("yamledit: the document is a flow mapping")
	case len(root.Content) == 0:
		return nil, nil
	}
	return root, nil
}

func find(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind == yaml.ScalarNode && k.Value == key {
			return i
		}
	}
	return -1
}

// text is the source with its line offsets. Lines are numbered from 1, as in yaml.Node.
type text struct {
	src    []byte
	starts []int
}

func newText(src []byte) *text {
	t := &text{src: src, starts: []int{0}}
	for i, c := range src {
		if c == '\n' && i+1 < len(src) {
			t.starts = append(t.starts, i+1)
		}
	}
	return t
}

func (t *text) lines() int { return len(t.starts) }

// start is the offset of line n; next is the offset after its newline (or the end of the source).
func (t *text) start(n int) int { return t.starts[n-1] }

func (t *text) next(n int) int {
	if n < len(t.starts) {
		return t.starts[n]
	}
	return len(t.src)
}

// line returns line n without its newline.
func (t *text) line(n int) []byte {
	return bytes.TrimSuffix(t.src[t.start(n):t.next(n)], []byte("\n"))
}

func (t *text) indent(n int) int {
	l := t.line(n)
	return len(l) - len(bytes.TrimLeft(l, " "))
}

func (t *text) blank(n int) bool { return len(bytes.TrimSpace(t.line(n))) == 0 }

func (t *text) comment(n int) bool {
	return bytes.HasPrefix(bytes.TrimLeft(t.line(n), " \t"), []byte("#"))
}

// offset converts a node position (1-based line, 1-based column in characters) to a byte offset.
func (t *text) offset(line, col int) int {
	off := t.start(line)
	for c := 1; c < col && off < len(t.src); c++ {
		_, size := utf8.DecodeRune(t.src[off:])
		off += size
	}
	return off
}

// entryEnd returns the last line of entry idx of lv's mapping: up to the next sibling key (or the end of the
// mapping), without trailing blank lines and comments indented no deeper than the key, which belong to what follows.
func (t *text) entryEnd(lv level, idx int) int {
	k := lv.m.Content[idx]
	end := lv.end
	if idx+2 < len(lv.m.Content) {
		end = lv.m.Content[idx+2].Line - 1
	}
	ind := t.indent(k.Line)
	for end > k.Line && (t.blank(end) || (t.comment(end) && t.indent(end) <= ind)) {
		end--
	}
	return end
}

// headCommentStart returns the first line of the comment block directly above key k (same indentation, no blank
// line in between), or k's own line.
func (t *text) headCommentStart(k *yaml.Node) int {
	start := k.Line
	ind := t.indent(k.Line)
	for l := k.Line - 1; l >= 1 && t.comment(l) && t.indent(l) == ind; l-- {
		start = l
	}
	return start
}

func (t *text) splice(from, to int, repl []byte) []byte {
	out := make([]byte, 0, len(t.src)-(to-from)+len(repl))
	out = append(out, t.src[:from]...)
	out = append(out, repl...)
	return append(out, t.src[to:]...)
}

func (t *text) remove(first, last int) []byte { return t.splice(t.start(first), t.next(last), nil) }

func (t *text) insertAt(off int, block []byte) ([]byte, error) {
	if off > 0 && t.src[off-1] != '\n' {
		block = append([]byte("\n"), block...)
	}
	return t.splice(off, off, block), nil
}

// insertInto adds key: value as the last entry of lv's mapping.
func (t *text) insertInto(lv level, key string, value any) ([]byte, error) {
	last := len(lv.m.Content) - 2
	end := t.entryEnd(lv, last)
	return t.insertAt(t.next(end), render(key, value, t.indent(lv.m.Content[0].Line)))
}

// replaceEntry rewrites a whole entry, from its key line to end, keeping the comments above it.
func (t *text) replaceEntry(k *yaml.Node, end int, value any) ([]byte, error) {
	block := render(k.Value, value, t.indent(k.Line))
	if t.next(end) == len(t.src) && !bytes.HasSuffix(t.src, []byte("\n")) {
		block = bytes.TrimSuffix(block, []byte("\n"))
	}
	return t.splice(t.start(k.Line), t.next(end), block), nil
}

// replaceInline swaps a one-line value in place, keeping the key and any comment after the value. It applies when
// both the old and the new value fit on the key's line.
func (t *text) replaceInline(k, v *yaml.Node, end int, value any) ([]byte, bool) {
	if v.Line != k.Line || end != k.Line || (v.Kind == yaml.ScalarNode && v.Tag == "!!null" && v.Value == "") {
		return nil, false
	}
	repl, ok := inline(value)
	if !ok {
		return nil, false
	}
	from := t.offset(v.Line, v.Column)
	to, ok := inlineEnd(t.src[:t.next(v.Line)], from)
	if !ok {
		return nil, false
	}
	return t.splice(from, to, repl), true
}

// inlineEnd finds where a one-line value starting at from ends; the rest of the line must be empty or a comment.
func inlineEnd(src []byte, from int) (int, bool) {
	lineEnd := bytes.IndexByte(src[from:], '\n')
	if lineEnd < 0 {
		lineEnd = len(src)
	} else {
		lineEnd += from
	}
	line := src[from:lineEnd]
	if len(line) == 0 {
		return 0, false
	}
	var n int
	switch line[0] {
	case '"':
		n = -1
		for i := 1; i < len(line); i++ {
			if line[i] == '\\' {
				i++
			} else if line[i] == '"' {
				n = i + 1
				break
			}
		}
	case '\'':
		n = -1
		for i := 1; i < len(line); i++ {
			if line[i] == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				n = i + 1
				break
			}
		}
	case '[', '{':
		n = flowEnd(line)
	case '|', '>', '&', '*', '!', '#':
		return 0, false
	default:
		n = len(line)
		if i := bytes.Index(line, []byte(" #")); i >= 0 {
			n = i
		}
		n = len(bytes.TrimRight(line[:n], " \t"))
	}
	if n <= 0 {
		return 0, false
	}
	rest := bytes.TrimLeft(line[n:], " \t")
	if len(rest) > 0 && rest[0] != '#' {
		return 0, false
	}
	return from + n, true
}

// flowEnd returns the length of the flow collection at the start of line, or -1 if it does not close on it.
func flowEnd(line []byte) int {
	depth := 0
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// inline renders a value that fits on one line: a scalar, or an empty mapping or sequence.
func inline(value any) ([]byte, bool) {
	block := render("v", value, 0)
	s, ok := bytes.CutPrefix(block, []byte("v: "))
	if !ok || bytes.Count(s, []byte("\n")) != 1 {
		return nil, false
	}
	s = bytes.TrimSuffix(s, []byte("\n"))
	if len(s) > 0 && (s[0] == '|' || s[0] == '>') {
		return nil, false
	}
	return s, true
}

// render writes key: value in block style at the given indentation, ending with a newline.
func render(key string, value any, indent int) []byte {
	var vn yaml.Node
	if value != nil {
		if err := vn.Encode(value); err != nil {
			panic(fmt.Sprintf("yamledit: cannot encode %T: %v", value, err)) // callers pass plain data
		}
		mediamtxBools(&vn)
	} else {
		vn = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: ""}
	}
	kn := yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{&kn, &vn}}); err != nil {
		panic(fmt.Sprintf("yamledit: cannot render %q: %v", key, err))
	}
	_ = enc.Close()
	out := bytes.ReplaceAll(buf.Bytes(), []byte(": null\n"), []byte(":\n"))
	if indent == 0 {
		return out
	}
	pad := strings.Repeat(" ", indent)
	var b bytes.Buffer
	for _, l := range bytes.SplitAfter(out, []byte("\n")) {
		if len(l) > 0 && l[0] != '\n' {
			b.WriteString(pad)
		}
		b.Write(l)
	}
	return b.Bytes()
}

// mediamtxBools writes booleans as yes and no, as MediaMTX's own config does (it reads both spellings).
func mediamtxBools(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!bool" {
		if b, err := strconv.ParseBool(n.Value); err == nil {
			n.Value, n.Tag = "no", ""
			if b {
				n.Value = "yes"
			}
		}
	}
	for _, c := range n.Content {
		mediamtxBools(c)
	}
}

// renderedValue is value as it reads back after rendering: what a decoder of the edited document will see.
func renderedValue(value any) (any, error) {
	var m map[string]any
	if err := yaml.Unmarshal(render("v", value, 0), &m); err != nil {
		return nil, err
	}
	return m["v"], nil
}

// toValue converts a node for re-rendering. Unlike a plain decode it keeps MediaMTX's unquoted yes/no/on/off (YAML
// 1.1 booleans, strings to a YAML 1.2 decoder) as booleans, so rewriting a subtree does not turn them into quoted
// strings that MediaMTX would refuse for its boolean settings.
func toValue(n *yaml.Node) any {
	switch n.Kind {
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			m[n.Content[i].Value] = toValue(n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		l := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			l = append(l, toValue(c))
		}
		return l
	case yaml.AliasNode:
		return toValue(n.Alias)
	case yaml.ScalarNode:
		if n.Style == 0 && n.Tag == "!!str" {
			switch strings.ToLower(n.Value) {
			case "yes", "on", "true":
				return true
			case "no", "off", "false":
				return false
			}
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return n.Value
		}
		return v
	}
	return nil
}

func decodeDoc(src []byte) (map[string]any, error) {
	var m map[string]any
	if err := yaml.Unmarshal(src, &m); err != nil {
		return nil, fmt.Errorf("yamledit: not valid YAML: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func nest(path []string, value any) any {
	for i := len(path) - 1; i >= 0; i-- {
		value = map[string]any{path[i]: value}
	}
	return value
}

// setIn sets path in m (in place) to value; anything in the way that is not a mapping becomes one.
func setIn(m map[string]any, path []string, value any) map[string]any {
	if len(path) == 1 {
		m[path[0]] = value
		return m
	}
	child, ok := m[path[0]].(map[string]any)
	if !ok {
		child = map[string]any{}
	}
	m[path[0]] = setIn(child, path[1:], value)
	return m
}

// setInAny is setIn on a decoded value that may not be a mapping.
func setInAny(cur any, path []string, value any) any {
	m, ok := cur.(map[string]any)
	if !ok {
		m = map[string]any{}
	}
	return setIn(m, path, value)
}

func deleteIn(m map[string]any, path []string) (map[string]any, bool) {
	if len(path) == 1 {
		if _, ok := m[path[0]]; !ok {
			return m, false
		}
		delete(m, path[0])
		return m, true
	}
	child, ok := m[path[0]].(map[string]any)
	if !ok {
		return m, false
	}
	_, found := deleteIn(child, path[1:])
	return m, found
}

// Decode returns the document as plain data the way MediaMTX reads it: unquoted yes/no/on/off are booleans (YAML 1.1),
// unlike in a plain YAML 1.2 decode. An empty document is an empty mapping.
func Decode(src []byte) (map[string]any, error) {
	root, err := parse(src)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return map[string]any{}, nil
	}
	m, _ := toValue(root).(map[string]any)
	return m, nil
}
