// Package mtxconf renders, validates and writes mediamtx.yml. Nothing reaches the file unvalidated: MediaMTX exits
// on an invalid reload and reloads its open defaults from an empty file, so every candidate passes the
// sidecar's rules and `mediamtx --validate-conf` before an atomic rename puts it in place.
package mtxconf

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"text/template"
)

// SeedParams parameterize the config the sidecar writes on first start and at setup.
type SeedParams struct {
	MediaMTXVersion string
	AuthURL         string       // the sidecar's internal /internal/auth
	PublicHost      string       // offered to WebRTC clients
	StackSubnet     netip.Prefix // trusted proxies for HLS, WebRTC and playback; omitted when unknown
	RTSP, RTMP, SRT bool         // ingest protocols; all off until setup chooses
}

var seedTemplate = template.Must(template.New("seed").Funcs(template.FuncMap{
	"q": func(s string) string { b, _ := json.Marshal(s); return string(b) }, // JSON strings are valid YAML scalars
	"yesno": func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	},
}).Parse(`# MediaMTX configuration, managed by the MediaMTX UI sidecar.
#
# Change settings in the UI: the sidecar validates every change before MediaMTX sees it. MediaMTX stops when it
# reloads an invalid file, so hand edits are risky, and the sidecar reverts edits that fail validation.
# Anything not set here keeps MediaMTX's default:
# https://github.com/bluenviron/mediamtx/blob/v{{.MediaMTXVersion}}/mediamtx.yml

###############################################
# Logs

logLevel: info
logDestinations: [stdout, file]
logFile: /logs/mediamtx.log

###############################################
# Authentication: the sidecar decides every request.

authMethod: http
authHTTPAddress: {{q .AuthURL}}
authHTTPExclude: []

###############################################
# Control API, metrics and playback: on the stack network only; these ports are never published.

api: yes
apiAddress: :9997
metrics: yes
metricsAddress: :9998
playback: yes
playbackAddress: :9996
{{- if .StackSubnet.IsValid}}
playbackTrustedProxies: [{{q .StackSubnet.String}}]
{{- end}}
pprof: no

###############################################
# Protocols. RTSP, RTMP and SRT carry streams in and out; switch them on in the UI.

rtsp: {{yesno .RTSP}}
rtspTransports: [tcp]
rtmp: {{yesno .RTMP}}
srt: {{yesno .SRT}}

# HLS and WebRTC signalling reach browsers through the sidecar; WebRTC media uses UDP 8189.
hls: yes
{{- if .StackSubnet.IsValid}}
hlsTrustedProxies: [{{q .StackSubnet.String}}]
{{- end}}
webrtc: yes
{{- if .StackSubnet.IsValid}}
webrtcTrustedProxies: [{{q .StackSubnet.String}}]
{{- end}}
webrtcIPsFromInterfaces: no
webrtcAdditionalHosts: [{{q .PublicHost}}]
moq: no

###############################################
# Path defaults

pathDefaults:
  recordPath: /recordings/%path/%Y-%m-%d_%H-%M-%S-%f
  recordDeleteAfter: 168h

###############################################
# Paths. Any path name may be used; stream credentials decide who may publish or read where.

paths:
  all_others:
`))

// Seed renders the sidecar's mediamtx.yml.
func Seed(p SeedParams) ([]byte, error) {
	var b bytes.Buffer
	if err := seedTemplate.Execute(&b, p); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
