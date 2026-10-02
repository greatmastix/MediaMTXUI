// Package clientip determines who a request came from. X-Forwarded-* headers are believed only when the TCP peer is a
// trusted proxy; the client is then the rightmost X-Forwarded-For entry that is not itself a trusted proxy.
package clientip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Info is what is known about a request's origin.
type Info struct {
	IP     netip.Addr // the client
	Scheme string     // "https" or "http", as the client used it
	Host   string     // the host the client asked for
	Proxy  bool       // whether a trusted proxy forwarded the request
}

// Resolver resolves Info for requests.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a resolver that trusts the given proxies.
func New(trusted []netip.Prefix) *Resolver { return &Resolver{trusted: trusted} }

func (r *Resolver) isTrusted(a netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve determines the client of req.
func (r *Resolver) Resolve(req *http.Request) Info {
	peer := parseAddr(req.RemoteAddr)
	direct := Info{IP: peer, Scheme: "http", Host: req.Host}
	if req.TLS != nil {
		direct.Scheme = "https"
	}
	if !peer.IsValid() || !r.isTrusted(peer) {
		return direct
	}

	info := Info{IP: peer, Scheme: direct.Scheme, Host: req.Host, Proxy: true}
	entries := splitList(req.Header.Values("X-Forwarded-For"))
	for i := len(entries) - 1; i >= 0; i-- {
		a := parseAddr(entries[i])
		if !a.IsValid() {
			break // a trusted proxy passed on garbage: stay with the last address known to be real
		}
		info.IP = a
		if !r.isTrusted(a) {
			break
		}
	}
	if protos := splitList(req.Header.Values("X-Forwarded-Proto")); len(protos) > 0 {
		if p := strings.ToLower(protos[len(protos)-1]); p == "http" || p == "https" {
			info.Scheme = p
		}
	}
	if hosts := splitList(req.Header.Values("X-Forwarded-Host")); len(hosts) > 0 {
		info.Host = hosts[len(hosts)-1]
	}
	return info
}

// splitList flattens comma-separated header values.
func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

// parseAddr accepts "ip", "ip:port", "[ipv6]" and "[ipv6]:port"; IPv4-mapped IPv6 addresses become IPv4.
func parseAddr(s string) netip.Addr {
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}
	}
	return a.Unmap()
}

// RateKey groups addresses for rate limiting: an IPv4 address alone, an IPv6 address by its /64, since one host
// commonly holds a whole /64.
func RateKey(a netip.Addr) string {
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

type ctxKey struct{}

// Middleware stores each request's Info in its context.
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, r.Resolve(req))))
	})
}

// From returns the Info stored by Middleware.
func From(ctx context.Context) Info {
	info, _ := ctx.Value(ctxKey{}).(Info)
	return info
}

// With stores info in ctx (tests, and callers outside HTTP).
func With(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}
