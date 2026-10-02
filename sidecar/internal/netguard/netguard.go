// Package netguard decides which addresses MediaMTX may be told to connect to. A path source or a forward destination
// makes MediaMTX open a connection from inside the stack, so without a check an admin session (or a stolen one) could
// point it at the sidecar's internal listener, MediaMTX's own API, the Docker host or a cloud metadata service.
//
// Allowed: public addresses, and for pulled sources private LAN ranges too (cameras live there); forward destinations
// are other platforms, so private ranges are refused for them (stream owners set forwards, and a forward is a way into
// the host's network). Refused everywhere: loopback, link-local (which holds the metadata services), unspecified,
// multicast and reserved ranges, a few well-known metadata addresses, and the stack's own subnet (the sidecar,
// MediaMTX, the Docker gateway). Hostnames are resolved and every address must pass. Schemes must be written in lower
// case, as MediaMTX reads them.
//
// Not covered, because only MediaMTX sees them: a name that changes its answers after the check (DNS rebinding), and
// HTTP redirects or WHIP Location headers that MediaMTX follows to another address. The internal listener and the API
// authenticate every request regardless, so these reach nothing that trusts the source address alone.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Guard checks outbound addresses.
type Guard struct {
	blocked []blockedRange
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

type blockedRange struct {
	prefix netip.Prefix
	why    string
}

// New returns a guard that also refuses the stack's subnet (the zero prefix when unknown).
func New(stack netip.Prefix) *Guard {
	g := &Guard{resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
	for _, b := range []struct{ cidr, why string }{
		{"0.0.0.0/8", "an unspecified address"},
		{"127.0.0.0/8", "loopback: MediaMTX itself"},
		{"169.254.0.0/16", "link-local, where cloud metadata services live"},
		{"100.100.100.200/32", "a cloud metadata service"},
		{"168.63.129.16/32", "a cloud platform service"},
		{"224.0.0.0/4", "multicast"},
		{"240.0.0.0/4", "reserved"},
		{"::/128", "an unspecified address"},
		{"::1/128", "loopback: MediaMTX itself"},
		{"fe80::/10", "link-local"},
		{"fd00:ec2::254/128", "a cloud metadata service"},
		{"ff00::/8", "multicast"},
	} {
		g.blocked = append(g.blocked, blockedRange{netip.MustParsePrefix(b.cidr), b.why})
	}
	if stack.IsValid() {
		g.blocked = append(g.blocked, blockedRange{stack.Masked(), "the stack's own network (the sidecar, MediaMTX, the Docker host)"})
	}
	return g
}

// CheckAddr refuses a blocked address.
func (g *Guard) CheckAddr(a netip.Addr) error {
	a = a.Unmap()
	for _, b := range g.blocked {
		if b.prefix.Contains(a) {
			return fmt.Errorf("%s is %s", a, b.why)
		}
	}
	return nil
}

// CheckHost resolves host (a name or an IP literal) and refuses it if any of its addresses is blocked.
func (g *Guard) CheckHost(ctx context.Context, host string) error {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return errors.New("the address has no host")
	}
	if strings.ContainsAny(host, "$%") {
		return errors.New("the host contains a variable, so it cannot be checked; put the variable in the path instead")
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return g.CheckAddr(a)
	}
	if name := strings.ToLower(strings.TrimSuffix(host, ".")); name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return fmt.Errorf("%s is loopback: MediaMTX itself", host)
	}
	addrs, err := g.resolve(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("%s cannot be resolved from the stack", host)
	}
	for _, a := range addrs {
		if err := g.CheckAddr(a); err != nil {
			return fmt.Errorf("%s resolves to %w", host, err)
		}
	}
	return nil
}

// Source types (MediaMTX v1.21.1): pulled over the network, listened on, or not a URL at all.
var (
	pullSchemes = map[string]bool{
		"rtsp": true, "rtsps": true, "rtsp+http": true, "rtsps+http": true, "rtsp+ws": true, "rtsps+ws": true,
		"rtmp": true, "rtmps": true, "http": true, "https": true, "srt": true, "moqt": true, "whep": true, "wheps": true,
	}
	listenSchemes  = map[string]bool{"udp+mpegts": true, "udp+rtp": true}
	forwardSchemes = map[string]bool{
		"rtsp": true, "rtsps": true, "rtmp": true, "rtmps": true, "srt": true, "moqt": true, "whip": true, "whips": true,
	}
)

// lowerScheme refuses a URL whose scheme is not written in lower case: Go's parser lowers it, MediaMTX does not.
func lowerScheme(raw string) error {
	if i := strings.Index(raw, ":"); i > 0 && raw[:i] != strings.ToLower(raw[:i]) {
		return fmt.Errorf("write the scheme in lower case (%s)", strings.ToLower(raw[:i]))
	}
	return nil
}

// privateRanges are the LAN ranges forward destinations may not use.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fc00::/7"),
}

// CheckSource vets a path's source setting.
func (g *Guard) CheckSource(ctx context.Context, source string) error {
	switch source {
	case "", "publisher", "redirect", "rpiCamera":
		return nil
	}
	if err := lowerScheme(source); err != nil {
		return err
	}
	u, err := url.Parse(source)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("%q is not a source MediaMTX knows", source)
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case pullSchemes[scheme]:
		return g.CheckHost(ctx, u.Hostname())
	case listenSchemes[scheme]:
		// MediaMTX listens on this address inside its container: no outbound connection, but keep to ports
		// that cannot collide with privileged services.
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1024 || port > 65535 {
			return errors.New("a UDP source needs a port from 1024 to 65535")
		}
		return nil
	case scheme == "unix+mpegts":
		return errors.New("unix socket sources refer to files inside the MediaMTX container and cannot be set from the UI")
	default:
		return fmt.Errorf("%q is not a source type MediaMTX knows", scheme)
	}
}

// CheckForward vets a forward destination: a public address (other platforms).
func (g *Guard) CheckForward(ctx context.Context, dest string) error {
	if err := lowerScheme(dest); err != nil {
		return err
	}
	u, err := url.Parse(dest)
	if err != nil || !forwardSchemes[u.Scheme] {
		return fmt.Errorf("%q is not a forward destination MediaMTX knows", dest)
	}
	if err := g.CheckHost(ctx, u.Hostname()); err != nil {
		return err
	}
	return g.checkHost(ctx, u.Hostname(), func(a netip.Addr) error {
		for _, p := range privateRanges {
			if p.Contains(a.Unmap()) {
				return fmt.Errorf("%s is a private network address: forwards go to other platforms on the internet", a)
			}
		}
		return nil
	})
}

// checkHost runs check on every address host (a name or an IP literal) stands for.
func (g *Guard) checkHost(ctx context.Context, host string, check func(netip.Addr) error) error {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if a, err := netip.ParseAddr(host); err == nil {
		return check(a)
	}
	addrs, err := g.resolve(ctx, host)
	if err != nil {
		return fmt.Errorf("%s cannot be resolved from the stack", host)
	}
	for _, a := range addrs {
		if err := check(a); err != nil {
			return fmt.Errorf("%s resolves to %w", host, err)
		}
	}
	return nil
}
