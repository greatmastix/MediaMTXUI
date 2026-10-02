package netguard

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func guard() *Guard {
	g := New(netip.MustParsePrefix("172.29.42.0/24"))
	names := map[string][]string{
		"cam.lan":      {"192.168.1.20"},
		"example.com":  {"93.184.216.34", "2606:2800:220:1::1"},
		"sidecar":      {"172.29.42.3"},
		"sneaky.test":  {"93.184.216.34", "169.254.169.254"},
		"mapped.test":  {"::ffff:127.0.0.1"},
		"metadata.v6":  {"fd00:ec2::254"},
		"gateway.test": {"172.29.42.1"},
	}
	g.resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range names[host] {
			out = append(out, netip.MustParseAddr(a))
		}
		if out == nil {
			return nil, errors.New("no such host")
		}
		return out, nil
	}
	return g
}

func TestSources(t *testing.T) {
	g := guard()
	ctx := context.Background()
	for _, ok := range []string{
		"publisher", "redirect", "rpiCamera", "",
		"rtsp://user:pass@cam.lan:554/stream1", "rtsps://example.com/live", "rtsp+ws://cam.lan/x",
		"rtmp://example.com/app#key", "https://example.com/stream.m3u8", "srt://example.com:8890?streamid=read:x",
		"whep://10.1.2.3:8889/cam/whep", "moqt://example.com/x", "udp+mpegts://238.0.0.1:1234", "udp+rtp://:5004",
		"rtsp://[2606:2800:220:1::1]:554/x", "rtsp://cam.lan/$G1?q=$MTX_QUERY",
	} {
		if err := g.CheckSource(ctx, ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for bad, why := range map[string]string{
		"rtsp://127.0.0.1:8554/other":        "loopback",
		"rtsp://localhost/x":                 "loopback",
		"http://169.254.169.254/latest/meta": "metadata",
		"http://sidecar:9081/internal/auth":  "stack's own network",
		"rtsp://gateway.test/x":              "stack's own network",
		"rtsp://172.29.42.1/x":               "stack's own network",
		"rtsp://sneaky.test/x":               "link-local",
		"rtsp://mapped.test/x":               "loopback",
		"rtsp://metadata.v6/x":               "metadata",
		"rtsp://[::1]/x":                     "loopback",
		"rtsp://0.0.0.0/x":                   "unspecified",
		"rtsp://nowhere.invalid/x":           "cannot be resolved",
		"rtsp://$G1/x":                       "variable",
		"udp+mpegts://:80":                   "1024",
		"unix+mpegts:///tmp/sock":            "unix socket",
		"file:///etc/passwd":                 "not a source type",
		"just words":                         "not a source",
	} {
		err := g.CheckSource(ctx, bad)
		if err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%q: %v, want an error about %q", bad, err, why)
		}
	}
}

func TestForward(t *testing.T) {
	g := guard()
	ctx := context.Background()
	for _, ok := range []string{"rtmp://example.com/live#key", "srt://example.com:9000", "whips://example.com/x/whip"} {
		if err := g.CheckForward(ctx, ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"rtmp://127.0.0.1/live", "http://example.com/", "whip://sidecar:9080/x"} {
		if err := g.CheckForward(ctx, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
