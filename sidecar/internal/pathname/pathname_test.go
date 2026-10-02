package pathname

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	for _, ok := range []string{"cam1", "live/front-door", "a.b_c-d", "x/y/z", "stream.v2"} {
		if err := Valid(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "/cam", "cam/", "a//b", "..", "a/../b", "./a", "a/.", "bad name", "cam?x=1", "cam#1", "cam%2f",
		"üml", "a\\b", "a:b", strings.Repeat("a", 256),
	} {
		if Valid(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidPattern(t *testing.T) {
	for _, ok := range []string{"cam1", "~^cam[0-9]+$", "~.*"} {
		if err := ValidPattern(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"~", "~(", "../x", "~" + strings.Repeat("a", 256)} {
		if ValidPattern(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
