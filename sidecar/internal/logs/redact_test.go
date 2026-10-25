package logs

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mtxui/internal/mtxtest"
)

// authBody is the JSON MediaMTX posts to the auth endpoint (internal/auth/manager.go, v1.21.3), which it logs at
// debug level as the body of an outbound request.
func authBody(user, password, query string) string {
	b, _ := json.Marshal(struct {
		IP        string  `json:"ip"`
		User      string  `json:"user"`
		Password  string  `json:"password"`
		Token     string  `json:"token"`
		Action    string  `json:"action"`
		Path      string  `json:"path"`
		Protocol  string  `json:"protocol"`
		ID        *string `json:"id"`
		Query     string  `json:"query"`
		UserAgent string  `json:"userAgent"`
	}{"203.0.113.7", user, password, password, "publish", "cam1", "rtmp", nil, query, "OBS"})
	return string(b)
}

// request is an outbound request as MediaMTX logs it (httpp.RequestForLog): request line, headers, blank line, body.
func request(body string) string {
	return "[c->s] POST /internal/auth HTTP/1.1\r\nHost: sidecar:9998\r\nContent-Type: application/json\r\n\r\n" + body
}

func TestRedact(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct{ name, in, want string }{
		{
			"plain body line",
			authBody("cam1", "s3cret", "user=cam1&pass=s3cret"),
			`{"ip":"203.0.113.7","user":"cam1","password":"<redacted>","token":"<redacted>","action":"publish","path":"cam1","protocol":"rtmp","id":null,"query":"<redacted>","userAgent":"OBS"}`,
		},
		{"an escaped quote", `{"password":"a\"b","user":"u"}`, `{"password":"<redacted>","user":"u"}`},
		{"a trailing backslash", `{"password":"a\\","user":"u"}`, `{"password":"<redacted>","user":"u"}`},
		{"cut off", `{"user":"u","password":"abcd`, `{"user":"u","password":"<redacted>"`},
		{"cut off after a backslash", `{"user":"u","password":"ab\`, `{"user":"u","password":"<redacted>"`},
		{"empty", `{"password":"","user":"u"}`, `{"password":"<redacted>","user":"u"}`},
		{"other fields stay", `{"name":"password","passwordHint":"x","user":"query"}`, `{"name":"password","passwordHint":"x","user":"query"}`},
		{
			"structured",
			structured(at, "DEB", request(authBody("cam1", `s3"c\ret`, ""))),
			structured(at, "DEB", request(`{"ip":"203.0.113.7","user":"cam1","password":"<redacted>","token":"<redacted>","action":"publish","path":"cam1","protocol":"rtmp","id":null,"query":"<redacted>","userAgent":"OBS"}`)),
		},
		{"structured, cut off", `{"message":"[c->s] POST\r\n\r\n{\"user\":\"u\",\"password\":\"abc`, `{"message":"[c->s] POST\r\n\r\n{\"user\":\"u\",\"password\":\"<redacted>\"`},
		{"structured, cut off in an escape", `{"message":"{\"user\":\"u\",\"password\":\"ab\\`, `{"message":"{\"user\":\"u\",\"password\":\"<redacted>\"`},
		{"no secrets", "2026/10/25 12:00:00 INF [RTMP] [conn 1.2.3.4:5] opened", "2026/10/25 12:00:00 INF [RTMP] [conn 1.2.3.4:5] opened"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Redact(tt.in); got != tt.want {
				t.Errorf("Redact(%s)\n got %s\nwant %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestRedactLeavesNoSecret checks bodies with client-chosen fields made to confuse the patterns, in both formats,
// whole and cut off at every byte from the first secret on: no part of the secret is ever left.
func TestRedactLeavesNoSecret(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	tricky := []string{
		`","password":"`, `\",\"password\":\"`, `\\`, `\`, `"`, `\"`, "\x1b[2J", "\u202e", "ä", "\n", `\\\"`, `:"`,
	}
	type field struct{ user, query string }
	var fields []field
	for _, s := range tricky {
		fields = append(fields, field{s, "a=b"}, field{"cam1", s})
	}
	for _, f := range fields {
		for _, secret := range []string{"S3CR3T\"\\\x01\u202e"} {
			body := authBody(f.user, secret, f.query)
			for _, line := range []string{body, structured(at, "DEB", request(body))} {
				if got := ParseMediaMTX(line).Text; strings.Contains(got, "S3C") {
					t.Fatalf("secret left in the viewer's text\n%s\n→ %s", line, got)
				}
				for cut := strings.Index(line, "S3C"); cut <= len(line); cut++ {
					if got := Redact(line[:cut]); strings.Contains(got, "S3C") {
						t.Fatalf("secret left in\n%s\n→ %s", line[:cut], got)
					}
				}
			}
		}
	}
}

func FuzzRedact(f *testing.F) {
	f.Add("cam1", "x", "", 0)
	f.Add(`","password":"`, `a"b\`, `\",\"query\":\"`, 40)
	f.Fuzz(func(t *testing.T, user, secret, query string, cut int) {
		if strings.Contains(user+query, "S3C") {
			t.Skip()
		}
		body := authBody(user, "S3CR3T"+secret, query)
		at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
		for _, line := range []string{body, structured(at, "DEB", request(body))} {
			if cut < 0 || cut > len(line) {
				cut = len(line)
			}
			if got := Redact(line[:cut]); strings.Contains(got, "S3C") {
				t.Fatalf("secret left in\n%s\n→ %s", line[:cut], got)
			}
		}
	})
}

func TestCopyRedacts(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", maxRedactLine+100<<10) // past maxRedactLine: written in pieces, nothing lost
	plain := "2026/10/25 12:00:00 DEB [AuthManager] " + request("") + "\n" + authBody("cam1", "s3cret", "") +
		"\n2026/10/25 12:00:01 INF [RTMP] opened\r\n" + long + "\nlast line, no newline " + `"token":"s3cret"`
	want := strings.ReplaceAll(strings.ReplaceAll(plain, `"s3cret"`, `"<redacted>"`), `"query":""`, `"query":"<redacted>"`)
	live := filepath.Join(dir, "mediamtx.log")
	if err := os.WriteFile(live, []byte(plain), 0o600); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	_, _ = z.Write([]byte(plain))
	_ = z.Close()
	if err := os.WriteFile(rotated(live, 1), gz.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{live, rotated(live, 1)} {
		var out bytes.Buffer
		if err := Copy(context.Background(), &out, name); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Errorf("%s: secrets or line endings differ (%d bytes, want %d); has s3cret: %v",
				filepath.Base(name), out.Len(), len(want), strings.Contains(out.String(), "s3cret"))
		}
	}
}

// TestRedactsMediaMTXDebugLog runs the pinned MediaMTX at debug level against an auth endpoint, in both log formats,
// and checks that its log holds the credentials (what the redaction is for) and that the viewer and the download do not.
// (The query also shows in the logged inbound request line, "GET /cam1/index.m3u8?key=k1", as it did before 1.21.3;
// the UI never puts credentials in a URL.)
func TestRedactsMediaMTXDebugLog(t *testing.T) {
	mtxtest.Bin(t)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer auth.Close()
	for _, structuredLog := range []bool{false, true} {
		t.Run("logStructured "+strconv.FormatBool(structuredLog), func(t *testing.T) {
			dir := t.TempDir()
			logFile := filepath.Join(dir, "mediamtx.log")
			hls := mtxtest.FreePort(t)
			mtxtest.Start(t, fmt.Sprintf(`
logLevel: debug
logDestinations: [file]
logFile: %s
logStructured: %v
authMethod: http
authHTTPAddress: %s
authHTTPExclude: []
api: no
metrics: no
playback: no
pprof: no
rtsp: no
rtmp: no
hls: yes
hlsAddress: 127.0.0.1:%d
webrtc: no
srt: no
moq: no
paths:
  all_others:
`, logFile, structuredLog, auth.URL, hls), hls)

			req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/cam1/index.m3u8?key=k1", hls), nil)
			req.SetBasicAuth("cam1", "S3CR3Tpass")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()

			var raw []byte
			for deadline := time.Now().Add(5 * time.Second); ; {
				raw, _ = os.ReadFile(logFile)
				if bytes.Contains(raw, []byte("S3CR3Tpass")) || time.Now().After(deadline) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			// If MediaMTX stops logging request bodies, this fails: the redaction may then go (and this test with it).
			if !bytes.Contains(raw, []byte("S3CR3Tpass")) {
				t.Fatalf("MediaMTX's debug log does not hold the auth request's password:\n%s", raw)
			}
			lines, _, err := Search(context.Background(), logFile, 0, Filter{}, 10000)
			if err != nil {
				t.Fatal(err)
			}
			redactedSeen := false
			for _, l := range lines {
				if strings.Contains(l.Text, "S3CR3T") {
					t.Errorf("the viewer shows a secret: %s", l.Text)
				}
				redactedSeen = redactedSeen || strings.Contains(l.Text, `"password":"<redacted>","token":"<redacted>"`) &&
					strings.Contains(l.Text, `"query":"<redacted>"`)
			}
			if !redactedSeen {
				t.Errorf("no redacted auth request among %d lines", len(lines))
			}
			var out bytes.Buffer
			if err := Copy(context.Background(), &out, logFile); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(out.Bytes(), []byte("S3CR3T")) {
				t.Errorf("the download holds a secret:\n%s", out.String())
			}
		})
	}
}
