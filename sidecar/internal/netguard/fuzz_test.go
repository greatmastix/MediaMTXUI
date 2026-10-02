package netguard

import (
	"context"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

// Whatever a source or forward destination looks like, an accepted one never names a blocked address as an IP
// literal, and names resolve through the guard (here: every name to a public address, so only literals can fail).
func FuzzCheckURL(f *testing.F) {
	for _, s := range []string{
		"rtsp://192.0.2.1/x", "rtsp://[::1]/x", "rtmp://127.0.0.1/a", "srt://0.0.0.0:8890", "whep://169.254.169.254/x",
		"rtsp://[::ffff:127.0.0.1]/x", "rtsp://0x7f000001/x", "rtsp://user:pass@10.0.0.1/x", "udp+mpegts://0.0.0.0:1234",
		"rtsp://localhost./x", "rtsp://LOCALHOST/x", "rtsp://[fe80::1%25eth0]/x", "rtmp://203.0.113.5#key",
	} {
		f.Add(s)
	}
	g := New(netip.MustParsePrefix("172.29.44.0/24"))
	g.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, check := range []func(context.Context, string) error{g.CheckSource, g.CheckForward} {
			if check(context.Background(), s) != nil {
				continue
			}
			u, err := url.Parse(s)
			if err != nil || listenSchemes[strings.ToLower(u.Scheme)] {
				continue // "publisher" and friends; listen addresses are MediaMTX's own sockets
			}
			host := strings.TrimSuffix(strings.TrimPrefix(u.Hostname(), "["), "]")
			if a, err := netip.ParseAddr(host); err == nil && g.CheckAddr(a) != nil {
				t.Fatalf("accepted %q, whose host %s is blocked", s, a)
			}
			if name := strings.ToLower(strings.TrimSuffix(host, ".")); name == "localhost" || strings.HasSuffix(name, ".localhost") {
				t.Fatalf("accepted %q, a loopback name", s)
			}
		}
	})
}
