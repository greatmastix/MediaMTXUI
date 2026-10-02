// Package pathname validates MediaMTX path names and path patterns. It is stricter than MediaMTX, which also
// accepts ".." segments: path names end up in file paths (recordPath's %path), so the sidecar never accepts one.
package pathname

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var chars = regexp.MustCompile(`^[0-9A-Za-z_.\-/]+$`)

// MaxLen bounds names and patterns.
const MaxLen = 255

// Valid reports whether name is a path name the sidecar accepts: letters, digits, underscore, dot, hyphen and
// slash; no leading, trailing or doubled slash; no "." or ".." segment.
func Valid(name string) error {
	switch {
	case name == "":
		return errors.New("path name is empty")
	case len(name) > MaxLen:
		return fmt.Errorf("path name is longer than %d bytes", MaxLen)
	case !chars.MatchString(name):
		return errors.New("path name may contain only letters, digits, underscore, dot, hyphen and slash")
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//"):
		return errors.New("path name must not start or end with a slash or contain two in a row")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." {
			return errors.New(`path name must not contain "." or ".." segments`)
		}
	}
	return nil
}

// ValidPattern accepts a path name or, as in MediaMTX's permission format, "~" followed by a regular expression.
func ValidPattern(p string) error {
	if re, ok := strings.CutPrefix(p, "~"); ok {
		if re == "" || len(re) > MaxLen {
			return errors.New("path regular expression is empty or too long")
		}
		if _, err := regexp.Compile(re); err != nil {
			return fmt.Errorf("path regular expression: %w", err)
		}
		return nil
	}
	return Valid(p)
}
