package clientip

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestResolve(t *testing.T) {
	r := New([]netip.Prefix{
		netip.MustParsePrefix("172.29.42.1/32"), // the Docker gateway: Caddy on the host
		netip.MustParsePrefix("10.0.0.0/8"),     // an inner proxy tier
		netip.MustParsePrefix("fd00::/8"),
	})
	tests := []struct {
		name       string
		remote     string
		xff        []string
		proto      string
		tls        bool
		wantIP     string
		wantScheme string
		wantProxy  bool
	}{
		{"direct client, no headers", "203.0.113.7:5000", nil, "", false, "203.0.113.7", "http", false},
		{"direct TLS client", "203.0.113.7:5000", nil, "", true, "203.0.113.7", "https", false},
		{"spoofed XFF from an untrusted peer is ignored", "203.0.113.7:5000", []string{"1.2.3.4"}, "https", false, "203.0.113.7", "http", false},
		{"one trusted proxy", "172.29.42.1:40000", []string{"198.51.100.9"}, "https", false, "198.51.100.9", "https", true},
		{"client-supplied XFF before the proxy's entry is skipped", "172.29.42.1:40000", []string{"6.6.6.6, 198.51.100.9"}, "https", false, "198.51.100.9", "https", true},
		{"two trusted hops", "172.29.42.1:40000", []string{"198.51.100.9, 10.1.2.3"}, "https", false, "198.51.100.9", "https", true},
		{"hops split over several headers", "172.29.42.1:40000", []string{"198.51.100.9", "10.1.2.3"}, "", false, "198.51.100.9", "http", true},
		{"all hops trusted: leftmost wins", "172.29.42.1:40000", []string{"10.9.9.9, 10.1.2.3"}, "", false, "10.9.9.9", "http", true},
		{"garbage from the proxy keeps the proxy's address", "172.29.42.1:40000", []string{"1.2.3.4, not-an-ip"}, "", false, "172.29.42.1", "http", true},
		{"garbage left of the real client is never reached", "172.29.42.1:40000", []string{"<script>, 198.51.100.9"}, "", false, "198.51.100.9", "http", true},
		{"entries with ports and brackets", "172.29.42.1:40000", []string{"[2001:db8::1]:443, 10.1.2.3:80"}, "", false, "2001:db8::1", "http", true},
		{"IPv6 trusted proxy", "[fd00::5]:40000", []string{"2001:db8::7"}, "https", false, "2001:db8::7", "https", true},
		{"IPv4-mapped peer is unmapped", "[::ffff:172.29.42.1]:40000", []string{"198.51.100.9"}, "", false, "198.51.100.9", "http", true},
		{"no XFF from a trusted proxy", "172.29.42.1:40000", nil, "https", false, "172.29.42.1", "https", true},
		{"bogus proto is ignored", "172.29.42.1:40000", []string{"198.51.100.9"}, "gopher", false, "198.51.100.9", "http", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://mtx.example.com/", nil)
			req.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if tt.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}
			got := r.Resolve(req)
			if got.IP.String() != tt.wantIP || got.Scheme != tt.wantScheme || got.Proxy != tt.wantProxy {
				t.Errorf("Resolve = %v %s proxy=%v; want %s %s proxy=%v", got.IP, got.Scheme, got.Proxy, tt.wantIP, tt.wantScheme, tt.wantProxy)
			}
		})
	}
}

func TestForwardedHostOnlyFromTrustedProxies(t *testing.T) {
	r := New([]netip.Prefix{netip.MustParsePrefix("172.29.42.1/32")})
	req := httptest.NewRequest(http.MethodGet, "http://internal:9080/", nil)
	req.Header.Set("X-Forwarded-Host", "mtx.example.com")
	req.RemoteAddr = "203.0.113.7:1"
	if got := r.Resolve(req).Host; got != "internal:9080" {
		t.Errorf("untrusted peer: host %q", got)
	}
	req.RemoteAddr = "172.29.42.1:1"
	if got := r.Resolve(req).Host; got != "mtx.example.com" {
		t.Errorf("trusted peer: host %q", got)
	}
}

func TestRateKey(t *testing.T) {
	if k := RateKey(netip.MustParseAddr("203.0.113.7")); k != "203.0.113.7" {
		t.Errorf("IPv4 key %q", k)
	}
	a, b := netip.MustParseAddr("2001:db8:1:2::1"), netip.MustParseAddr("2001:db8:1:2:ffff::9")
	if RateKey(a) != RateKey(b) || RateKey(a) != "2001:db8:1:2::/64" {
		t.Errorf("IPv6 keys %q %q, want one /64", RateKey(a), RateKey(b))
	}
}

func TestMiddleware(t *testing.T) {
	r := New(nil)
	var got Info
	h := r.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { got = From(req.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:1"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.IP.String() != "203.0.113.7" {
		t.Errorf("From = %+v", got)
	}
}
