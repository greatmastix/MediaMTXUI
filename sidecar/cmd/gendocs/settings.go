package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/yamledit"
)

// Settings is web/src/api/mediamtx-settings.json: every MediaMTX setting the UI's forms offer, with its type from the
// OpenAPI spec and its section, description and default from the reference mediamtx.yml of the same release.
type Settings struct {
	Version string    `json:"version"`
	Global  []Setting `json:"global"`
	Path    []Setting `json:"path"`
}

// Setting is one setting.
type Setting struct {
	Key         string `json:"key"`
	Section     string `json:"section"`
	Type        string `json:"type"`               // boolean, string, integer, number, array, object
	Items       string `json:"items,omitempty"`    // the item type of an array
	Default     any    `json:"default"`            // from the reference config; null when it has none
	Description string `json:"description"`        // the reference config's comment above the key
	Nullable    bool   `json:"nullable,omitempty"` // the spec allows null
}

var (
	banner  = regexp.MustCompile(`^\s*# (Global settings|Default path settings) -> (.+)$`)
	keyLine = regexp.MustCompile(`^( *)([A-Za-z][A-Za-z0-9]*):`)
)

func renderSettings(version string, specBytes, reference []byte) ([]byte, error) {
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Type       string `yaml:"type"`
				Properties map[string]struct {
					Type       string `yaml:"type"`
					Deprecated bool   `yaml:"deprecated"`
					Nullable   bool   `yaml:"nullable"`
					Items      struct {
						Type string `yaml:"type"`
						Ref  string `yaml:"$ref"`
					} `yaml:"items"`
					Ref   string `yaml:"$ref"`
					AllOf []struct {
						Ref string `yaml:"$ref"`
					} `yaml:"allOf"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(specBytes, &spec); err != nil {
		return nil, err
	}
	defaults, err := yamledit.Decode(reference)
	if err != nil {
		return nil, fmt.Errorf("reference config: %w", err)
	}
	pathDefaults, _ := defaults["pathDefaults"].(map[string]any)
	docs := referenceDocs(reference)

	build := func(schema string, indent int, dflt map[string]any) ([]Setting, error) {
		props := spec.Components.Schemas[schema].Properties
		if len(props) == 0 {
			return nil, fmt.Errorf("schema %s not found in the spec", schema)
		}
		var out []Setting
		for key, p := range props {
			if p.Deprecated || key == "name" || key == "paths" || key == "pathDefaults" {
				continue
			}
			// A $ref (or allOf with one) points at an enum (a string) or an object schema.
			refType := func(ref string) string {
				if t := spec.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")].Type; t != "" {
					return t
				}
				return "object"
			}
			typ := p.Type
			if typ == "" {
				typ = refType(p.Ref + firstRef(p.AllOf))
			}
			items := p.Items.Type
			if typ == "array" && items == "" {
				items = refType(p.Items.Ref)
			}
			d := docs[docKey{indent, key}]
			section := d.section
			if indent == 0 && strings.HasPrefix(key, "moq") {
				section = "MoQ server" // the reference config has no banner of its own for it
			}
			if section == "" {
				section = "Other"
			}
			out = append(out, Setting{
				Key: key, Section: section, Type: typ, Items: items, Default: dflt[key],
				Description: d.description, Nullable: p.Nullable,
			})
		}
		sort.SliceStable(out, func(i, j int) bool {
			oi, oj := docs[docKey{indent, out[i].Key}].order, docs[docKey{indent, out[j].Key}].order
			if (oi == 0) != (oj == 0) {
				return oj == 0 // documented settings first, in the reference config's order
			}
			if oi != oj {
				return oi < oj
			}
			return out[i].Key < out[j].Key
		})
		return out, nil
	}
	global, err := build("GlobalConf", 0, defaults)
	if err != nil {
		return nil, err
	}
	path, err := build("PathConf", 2, pathDefaults)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(Settings{Version: version, Global: global, Path: path}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func firstRef(all []struct {
	Ref string `yaml:"$ref"`
},
) string {
	if len(all) > 0 {
		return all[0].Ref
	}
	return ""
}

type docKey struct {
	indent int
	key    string
}

type doc struct {
	section     string
	description string
	order       int
}

// referenceDocs reads the reference config's structure: the section banners ("# Global settings -> RTSP server") and
// the comment block directly above each key, at the top level and inside pathDefaults.
func referenceDocs(reference []byte) map[docKey]doc {
	out := map[docKey]doc{}
	var section string
	var comment []string
	order := 0
	sc := bufio.NewScanner(bytes.NewReader(reference))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case banner.MatchString(line):
			section = banner.FindStringSubmatch(line)[2]
			comment = nil
		case strings.HasPrefix(trimmed, "####") || trimmed == "":
			comment = nil
		case strings.HasPrefix(trimmed, "#"):
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if indent <= 2 {
				comment = append(comment, strings.TrimSpace(strings.TrimPrefix(trimmed, "#")))
			}
		default:
			if m := keyLine.FindStringSubmatch(line); m != nil && (len(m[1]) == 0 || len(m[1]) == 2) {
				order++
				k := docKey{len(m[1]), m[2]}
				if _, seen := out[k]; !seen {
					out[k] = doc{section: section, description: strings.Join(comment, "\n"), order: order}
				}
			}
			comment = nil
		}
	}
	return out
}
