package mtxauth

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"mtxui/internal/credentials"
	"mtxui/internal/mtxtest"
)

// TestExpiryInTheAnswer: an allowed guest key's answer carries its expiry for MediaMTX; other credentials' answers,
// expiring or not, carry nothing.
func TestExpiryInTheAnswer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	guest, gc, err := f.creds.Add(ctx, credentials.Spec{Name: "guest-view-a", Actions: []string{"read"}, Paths: []string{"cam1"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := f.creds.Add(ctx, credentials.Spec{Name: "monitor", Actions: []string{"read"}, Paths: []string{"cam1"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	post := func(user, pass string) (int, string) {
		rec := httptest.NewRecorder()
		body := fmt.Sprintf(`{"ip":"203.0.113.7","user":%q,"password":%q,"action":"read","path":"cam1","protocol":"rtsp","id":null}`, user, pass)
		f.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/auth", strings.NewReader(body)))
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}

	if code, body := post("guest-view-a", guest); code != 200 || body != "" {
		t.Errorf("without EndsAtExpiry: %d %q, want 200 and no body", code, body)
	}
	f.h.EndsAtExpiry = func(_ context.Context, id int64) bool { return id == gc.ID }
	code, body := post("guest-view-a", guest)
	var got struct {
		ExpireAt time.Time `json:"expireAt"`
	}
	if err := json.Unmarshal([]byte(body), &got); code != 200 || err != nil || !got.ExpireAt.Equal(gc.ExpiresAt.Truncate(time.Millisecond)) {
		t.Errorf("guest key: %d %q, want 200 with expireAt %s", code, body, gc.ExpiresAt)
	}
	if code, body := post("monitor", other); code != 200 || body != "" {
		t.Errorf("expiring credential that is no guest key: %d %q, want 200 and no body", code, body)
	}
	if code, body := post("cam1", f.pub); code != 200 || body != "" {
		t.Errorf("credential without expiry: %d %q, want 200 and no body", code, body)
	}
	if code, body := post("guest-view-a", "wrong"); code != 401 || body != "" {
		t.Errorf("wrong secret: %d %q, want 401 and no body", code, body)
	}
}

// TestGuestKeyExpiryWithMediaMTX: the pinned MediaMTX closes a guest key's RTSP reader when the key expires, by the
// expiry in the answer alone (no sweep runs here), and keeps a reader of another credential going.
func TestGuestKeyExpiryWithMediaMTX(t *testing.T) {
	mtxtest.Bin(t)
	f := newFixture(t)
	p, err := NewPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	f.h.principal = p
	ctx := context.Background()
	const ttl = 3 * time.Second
	guest, gc, err := f.creds.Add(ctx, credentials.Spec{Name: "guest-view-a", Actions: []string{"read"}, Paths: []string{"cam1"}, TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	f.h.EndsAtExpiry = func(_ context.Context, id int64) bool { return id == gc.ID }
	authSrv := httptest.NewServer(f.h)
	defer authSrv.Close()

	api, rtsp := mtxtest.FreePort(t), mtxtest.FreePort(t)
	mtxtest.Start(t, fmt.Sprintf(`
authMethod: http
authHTTPAddress: %s
authHTTPExclude: []
api: yes
apiAddress: 127.0.0.1:%d
metrics: no
playback: no
pprof: no
rtsp: yes
rtspTransports: [tcp]
rtspAddress: 127.0.0.1:%d
rtmp: no
hls: no
webrtc: no
srt: no
moq: no
paths:
  cam1:
    alwaysAvailable: yes
    alwaysAvailableTracks:
      - codec: H264
`, authSrv.URL, api, rtsp), api)

	guestEnded := rtspRead(t, rtsp, "cam1", "guest-view-a", guest)
	otherEnded := rtspRead(t, rtsp, "cam1", "cam1", f.pub)

	type session struct {
		User      string     `json:"user"`
		State     string     `json:"state"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	sessions := func() map[string]session {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v3/rtspsessions/list", api), nil)
		p.Authorize(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var doc struct{ Items []session }
		if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
			t.Fatal(err)
		}
		out := map[string]session{}
		for _, s := range doc.Items {
			out[s.User] = s
		}
		return out
	}
	s := sessions()
	if g := s["guest-view-a"]; g.State != "read" || g.ExpiresAt == nil || !g.ExpiresAt.Equal(gc.ExpiresAt.Truncate(time.Millisecond)) {
		t.Errorf("guest session %+v, want reading with expiresAt %s", g, gc.ExpiresAt)
	}
	if o := s["cam1"]; o.State != "read" || o.ExpiresAt != nil {
		t.Errorf("other session %+v, want reading without expiresAt", o)
	}

	select {
	case <-guestEnded:
		if early := time.Until(*gc.ExpiresAt); early > 200*time.Millisecond {
			t.Errorf("the guest reader ended %s before its key expired", early)
		}
	case <-time.After(time.Until(*gc.ExpiresAt) + 3*time.Second):
		t.Fatal("the guest reader is still connected 3 s after its key expired")
	}
	select {
	case <-otherEnded:
		t.Error("the other reader ended with the guest key")
	default:
	}
	if s := sessions(); s["cam1"].State != "read" {
		t.Errorf("sessions after the expiry: %+v, want the other reader still reading", s)
	}
}

// rtspRead starts reading path over RTSP (TCP) with Basic credentials on every request: DESCRIBE, SETUP of the first
// track, PLAY. The returned channel closes when the server ends the connection; the test's end closes it too.
func rtspRead(t *testing.T, port int, path, user, pass string) <-chan struct{} {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	authz := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	cseq := 0
	do := func(method, url string, extra ...string) (textproto.MIMEHeader, string) {
		t.Helper()
		cseq++
		req := fmt.Sprintf("%s %s RTSP/1.0\r\nCSeq: %d\r\nAuthorization: %s\r\n", method, url, cseq, authz)
		for _, h := range extra {
			req += h + "\r\n"
		}
		if _, err := conn.Write([]byte(req + "\r\n")); err != nil {
			t.Fatal(err)
		}
		tp := textproto.NewReader(br)
		status, err := tp.ReadLine()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		hdr, err := tp.ReadMIMEHeader()
		if err != nil {
			t.Fatalf("%s headers: %v", method, err)
		}
		if !strings.HasPrefix(status, "RTSP/1.0 200") {
			t.Fatalf("%s %s: %s", method, url, status)
		}
		n, _ := strconv.Atoi(hdr.Get("Content-Length"))
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			t.Fatalf("%s body: %v", method, err)
		}
		return hdr, string(body)
	}
	base := fmt.Sprintf("rtsp://127.0.0.1:%d/%s", port, path)
	hdr, sdp := do("DESCRIBE", base, "Accept: application/sdp")
	control := ""
	for _, l := range strings.Split(sdp, "\n") {
		if c, ok := strings.CutPrefix(strings.TrimSpace(l), "a=control:"); ok && c != "*" {
			control = c
			break
		}
	}
	setupURL := control
	if !strings.HasPrefix(control, "rtsp://") {
		setupURL = strings.TrimSuffix(cmpOr(hdr.Get("Content-Base"), base+"/"), "/") + "/" + control
	}
	hdr, _ = do("SETUP", setupURL, "Transport: RTP/AVP/TCP;unicast;interleaved=0-1")
	sess, _, _ := strings.Cut(hdr.Get("Session"), ";")
	do("PLAY", base+"/", "Session: "+sess)

	_ = conn.SetDeadline(time.Time{})
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		_, _ = io.Copy(io.Discard, br) // RTP until the server closes the connection
	}()
	return ended
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
