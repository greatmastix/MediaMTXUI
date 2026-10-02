package mtxconf

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/yamledit"
)

// closingStep works around MediaMTX keeping a path it no longer has a config for (v1.21.1). When a named path
// leaves the file and a regular-expression path (all_others, ~...) matches its name, MediaMTX hands the running
// path over to that config instead of closing it, and the path stays in its list, offline and impossible to delete,
// until MediaMTX restarts. It closes the path only when the two configs differ in a setting it cannot change on the
// fly.
//
// closingStep returns before with maxReaders of each such path set to a value the matching config does not have
// (maxReaders cannot change on the fly), or nil when no path leaves that way. Written and applied before after, it
// makes MediaMTX close those paths when after removes them. For a path that is about to go, the cap does not matter.
func closingStep(before, after []byte) ([]byte, error) {
	var b, a struct {
		PathDefaults map[string]any `yaml:"pathDefaults"`
		Paths        map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(before, &b); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(after, &a); err != nil {
		return nil, err
	}
	var doc *yamledit.Doc
	for _, name := range sortedKeys(b.Paths) {
		if isPattern(name) {
			continue
		}
		if _, stays := a.Paths[name]; stays {
			continue
		}
		catcher, ok := matchingPattern(a.Paths, name)
		if !ok {
			continue // no config matches any more: MediaMTX closes the path itself
		}
		readers, ok := intSetting(asMap(a.Paths[catcher])["maxReaders"])
		if !ok {
			readers, _ = intSetting(a.PathDefaults["maxReaders"])
		}
		if doc == nil {
			var err error
			if doc, err = yamledit.New(before); err != nil {
				return nil, err
			}
		}
		// Only the cap changes: re-rendering the entry from decoded YAML would turn MediaMTX's yes/no into strings.
		err := doc.Set([]string{"paths", name, "maxReaders"}, readers+1)
		if err != nil && len(asMap(b.Paths[name])) == 0 {
			err = doc.Set([]string{"paths", name}, map[string]any{"maxReaders": readers + 1}) // `name: {}`
		}
		if err != nil {
			return nil, fmt.Errorf("closing path %s: %w", name, err)
		}
	}
	if doc == nil {
		return nil, nil
	}
	return doc.Bytes(), nil
}

// isPattern reports whether a path config's name is a pattern rather than a path name.
func isPattern(name string) bool {
	return name == "all" || name == "all_others" || strings.HasPrefix(name, "~")
}

// matchingPattern returns the pattern config MediaMTX would pick for name: the expressions in name order, then all
// and all_others.
func matchingPattern(paths map[string]any, name string) (string, bool) {
	var exprs, catchAll []string
	for k := range paths {
		switch {
		case k == "all" || k == "all_others":
			catchAll = append(catchAll, k)
		case strings.HasPrefix(k, "~"):
			exprs = append(exprs, k)
		}
	}
	sort.Strings(exprs)
	for _, k := range exprs {
		if re, err := regexp.Compile(k[1:]); err == nil && re.MatchString(name) {
			return k, true
		}
	}
	if len(catchAll) > 0 {
		return catchAll[0], true
	}
	return "", false
}

func intSetting(v any) (int, bool) {
	n, ok := v.(int)
	return n, ok
}
