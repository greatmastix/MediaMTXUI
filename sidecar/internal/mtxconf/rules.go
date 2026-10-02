package mtxconf

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/pathname"
)

// Rules are the sidecar's own checks, for what `mediamtx --validate-conf` does not catch (listener
// conflicts, an empty file) and for the invariants the security model rests on.
type Rules struct {
	AuthURL string // authHTTPAddress must point here
}

// Check returns every rule the config breaks.
func (r Rules) Check(content []byte) error {
	if len(bytes.TrimSpace(content)) == 0 {
		return errors.New("the config is empty: MediaMTX would load its defaults, which allow anonymous publishing")
	}
	var conf map[string]any
	if err := yaml.Unmarshal(content, &conf); err != nil {
		return fmt.Errorf("the config is not valid YAML: %w", err)
	}
	if conf == nil {
		return errors.New("the config holds no settings: MediaMTX would load its defaults")
	}
	var errs []error
	if s, _ := conf["authMethod"].(string); s != "http" {
		errs = append(errs, errors.New(`authMethod must be "http": the sidecar decides every authentication`))
	}
	if s, _ := conf["authHTTPAddress"].(string); s != r.AuthURL {
		errs = append(errs, fmt.Errorf("authHTTPAddress must be %q", r.AuthURL))
	}
	if ex, ok := conf["authHTTPExclude"]; ok {
		if list, isList := ex.([]any); !isList || len(list) > 0 {
			errs = append(errs, errors.New("authHTTPExclude must be empty: an excluded action needs no credentials at all"))
		}
	}
	if b, ok := asBool(conf["api"]); !ok || !b {
		errs = append(errs, errors.New("api must be on: the sidecar needs the Control API"))
	}
	if b, _ := asBool(conf["pprof"]); b {
		errs = append(errs, errors.New("pprof must be off"))
	}
	errs = append(errs, listenerConflicts(conf)...)
	errs = append(errs, pathRules(conf)...)
	return errors.Join(errs...)
}

// listener is one socket MediaMTX opens.
type listener struct {
	key   string // the setting that holds its address
	proto string // tcp or udp
}

// listeners describes MediaMTX v1.21.1's servers: the switch that enables each, and its sockets with their default
// addresses. TestDefaultsMatchReference checks the defaults against the pinned release's reference config.
var listeners = []struct {
	enable  string
	dflt    bool
	sockets []listener
}{
	{"rtsp", true, []listener{{"rtspAddress", "tcp"}}},
	{"rtmp", true, []listener{{"rtmpAddress", "tcp"}}},
	{"hls", true, []listener{{"hlsAddress", "tcp"}}},
	{"webrtc", true, []listener{{"webrtcAddress", "tcp"}, {"webrtcLocalUDPAddress", "udp"}, {"webrtcLocalTCPAddress", "tcp"}}},
	{"srt", true, []listener{{"srtAddress", "udp"}}},
	{"moq", true, []listener{{"moqHTTP2Address", "tcp"}, {"moqHTTP3Address", "udp"}, {"moqQUICAddress", "udp"}}},
	{"api", false, []listener{{"apiAddress", "tcp"}}},
	{"metrics", false, []listener{{"metricsAddress", "tcp"}}},
	{"pprof", false, []listener{{"pprofAddress", "tcp"}}},
	{"playback", false, []listener{{"playbackAddress", "tcp"}}},
}

// defaultAddress holds v1.21.1's default for every address setting above and the conditional ones below.
var defaultAddress = map[string]string{
	"rtspAddress": ":8554", "rtspsAddress": ":8322", "rtpAddress": ":8000", "rtcpAddress": ":8001",
	"rtmpAddress": ":1935", "rtmpsAddress": ":1936", "hlsAddress": ":8888", "webrtcAddress": ":8889",
	"webrtcLocalUDPAddress": ":8189", "webrtcLocalTCPAddress": "", "srtAddress": ":8890",
	"moqHTTP2Address": ":8892", "moqHTTP3Address": ":8892", "moqQUICAddress": ":8893",
	"apiAddress": ":9997", "metricsAddress": ":9998", "pprofAddress": ":9999", "playbackAddress": ":9996",
}

func listenerConflicts(conf map[string]any) []error {
	type socket struct{ key, proto, host, port string }
	var socks []socket
	add := func(key, proto string) {
		addr, ok := conf[key].(string)
		if !ok {
			addr = defaultAddress[key]
		}
		if addr == "" {
			return // an empty address disables that socket
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return // the binary validator reports malformed addresses
		}
		socks = append(socks, socket{key, proto, host, port})
	}
	on := func(key string, dflt bool) bool {
		if b, ok := asBool(conf[key]); ok {
			return b
		}
		return dflt
	}
	for _, l := range listeners {
		if !on(l.enable, l.dflt) {
			continue
		}
		for _, s := range l.sockets {
			add(s.key, s.proto)
		}
	}
	if on("rtsp", true) {
		if enc, _ := conf["rtspEncryption"].(string); enc == "optional" || enc == "strict" {
			add("rtspsAddress", "tcp")
		}
		if transportsInclude(conf["rtspTransports"], "udp") {
			add("rtpAddress", "udp")
			add("rtcpAddress", "udp")
		}
	}
	if on("rtmp", true) {
		if enc, _ := conf["rtmpEncryption"].(string); enc == "optional" || enc == "strict" {
			add("rtmpsAddress", "tcp")
		}
	}

	var errs []error
	for i := range socks {
		for j := i + 1; j < len(socks); j++ {
			a, b := socks[i], socks[j]
			if a.proto == b.proto && a.port == b.port && (a.host == "" || b.host == "" || a.host == b.host) {
				errs = append(errs, fmt.Errorf("%s and %s both listen on %s port %s", a.key, b.key, a.proto, a.port))
			}
		}
	}
	return errs
}

// asBool reads a boolean the way MediaMTX does. The YAML parser here follows YAML 1.2, where yes and no are
// strings, while MediaMTX's own config files (and its reference config) use yes and no for booleans.
func asBool(v any) (value, ok bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(x) {
		case "yes", "true", "on":
			return true, true
		case "no", "false", "off":
			return false, true
		}
	}
	return false, false
}

// transportsInclude reports whether rtspTransports includes t; unset means MediaMTX's default [udp, multicast, tcp].
func transportsInclude(v any, t string) bool {
	list, ok := v.([]any)
	if !ok {
		return v == nil
	}
	return slices.ContainsFunc(list, func(x any) bool { s, _ := x.(string); return s == t })
}

var recordPathVars = regexp.MustCompile(`%path`)

func pathRules(conf map[string]any) []error {
	var errs []error
	checkRecord := func(where string, p map[string]any) {
		rp, ok := p["recordPath"].(string)
		if !ok {
			return
		}
		switch {
		case !strings.HasPrefix(rp, "/recordings/"):
			errs = append(errs, fmt.Errorf("%s: recordPath must be under /recordings/", where))
		case slices.Contains(strings.Split(rp, "/"), ".."):
			errs = append(errs, fmt.Errorf(`%s: recordPath must not contain ".."`, where))
		case !recordPathVars.MatchString(rp):
			errs = append(errs, fmt.Errorf("%s: recordPath must contain %%path, so paths cannot overwrite each other's recordings", where))
		}
	}
	if pd, ok := conf["pathDefaults"].(map[string]any); ok {
		checkRecord("pathDefaults", pd)
	}
	paths, _ := conf["paths"].(map[string]any)
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch {
		case name == "all_others":
		case strings.HasPrefix(name, "~"):
			if _, err := regexp.Compile(name[1:]); err != nil {
				errs = append(errs, fmt.Errorf("path %q: invalid regular expression: %w", name, err))
			}
		default:
			if err := pathname.Valid(name); err != nil {
				errs = append(errs, fmt.Errorf("path %q: %w", name, err))
			}
		}
		if p, ok := paths[name].(map[string]any); ok {
			checkRecord("path "+name, p)
		}
	}
	return errs
}

// Locked are the global settings the sidecar depends on, with the reason. The UI shows them read-only and the
// settings endpoint refuses them; the rules above (or a broken connection to MediaMTX) would catch a change anyway.
var Locked = map[string]string{
	"authMethod":          "MediaMTX asks the sidecar about every authentication.",
	"authHTTPAddress":     "Points MediaMTX at the sidecar's authentication endpoint.",
	"authHTTPExclude":     "Must stay empty: an excluded action would need no credentials at all.",
	"authHTTPFingerprint": "The sidecar's authentication endpoint is plain HTTP on the stack network.",
	"api":                 "The sidecar needs MediaMTX's Control API.",
	"apiAddress":          "The sidecar reaches the Control API at this address.",
	"apiEncryption":       "The sidecar talks to the Control API over the stack network.",
	"pprof":               "Must stay off: it exposes MediaMTX's internals.",
	"hlsAddress":          "The sidecar's live view reaches MediaMTX's HLS server at this address.",
	"hlsEncryption":       "The sidecar talks to the HLS server over the stack network.",
	"webrtcAddress":       "The sidecar's live view reaches MediaMTX's WebRTC signalling at this address.",
	"webrtcEncryption":    "The sidecar talks to the WebRTC signalling over the stack network.",
}
