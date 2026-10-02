package mtxconf

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// HookChanges lists the hook settings (runOn*) that differ between two configs, globally, in pathDefaults and per
// path, e.g. "paths.cam1.runOnReady". Hooks run commands inside the MediaMTX container, so changing them is kept to
// the hooks editor (admin with step-up re-authentication); until that exists, no edit through the UI may
// add, change or remove one. Hooks already in the file are left alone.
func HookChanges(before, after []byte) ([]string, error) {
	b, err := hooks(before)
	if err != nil {
		return nil, err
	}
	a, err := hooks(after)
	if err != nil {
		return nil, err
	}
	var changed []string
	for k, v := range a {
		if !reflect.DeepEqual(b[k], v) {
			changed = append(changed, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

func hooks(content []byte) (map[string]any, error) {
	var conf map[string]any
	if err := yaml.Unmarshal(content, &conf); err != nil {
		return nil, fmt.Errorf("the config is not valid YAML: %w", err)
	}
	out := map[string]any{}
	collect := func(prefix string, m map[string]any) {
		for k, v := range m {
			if strings.HasPrefix(k, "runOn") && v != nil && v != "" && v != false {
				out[prefix+k] = v
			}
		}
	}
	collect("", conf)
	if pd, ok := conf["pathDefaults"].(map[string]any); ok {
		collect("pathDefaults.", pd)
	}
	if paths, ok := conf["paths"].(map[string]any); ok {
		for name, p := range paths {
			if pm, ok := p.(map[string]any); ok {
				collect("paths."+name+".", pm)
			}
		}
	}
	return out, nil
}

// Outbound is a setting that makes MediaMTX connect somewhere: a path's source or a forward destination.
type Outbound struct {
	Where string // e.g. "paths.cam1.source", "pathDefaults.forward"
	Kind  string // "source" or "forward"
	URL   string
}

// OutboundChanges lists the sources and forward destinations that after has and before does not (new or changed).
// Those are what an edit through the UI must have vetted (internal/netguard); entries already in the file stay.
func OutboundChanges(before, after []byte) ([]Outbound, error) {
	b, err := outbound(before)
	if err != nil {
		return nil, err
	}
	a, err := outbound(after)
	if err != nil {
		return nil, err
	}
	var out []Outbound
	for o := range a {
		if !b[o] {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Where+out[i].URL < out[j].Where+out[j].URL })
	return out, nil
}

func outbound(content []byte) (map[Outbound]bool, error) {
	var conf map[string]any
	if err := yaml.Unmarshal(content, &conf); err != nil {
		return nil, fmt.Errorf("the config is not valid YAML: %w", err)
	}
	set := map[Outbound]bool{}
	collect := func(prefix string, p map[string]any) {
		if s, ok := p["source"].(string); ok {
			set[Outbound{prefix + "source", "source", s}] = true
		}
		if list, ok := p["forward"].([]any); ok {
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					if d, ok := m["dest"].(string); ok {
						set[Outbound{prefix + "forward", "forward", d}] = true
					}
				}
			}
		}
	}
	if pd, ok := conf["pathDefaults"].(map[string]any); ok {
		collect("pathDefaults.", pd)
	}
	if paths, ok := conf["paths"].(map[string]any); ok {
		for name, p := range paths {
			if pm, ok := p.(map[string]any); ok {
				collect("paths."+name+".", pm)
			}
		}
	}
	return set, nil
}
