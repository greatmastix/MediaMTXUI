// Package settings reads the sidecar's configuration from environment variables. Every variable is declared once in
// Vars, which also generates docs/config.md. Load reports every problem at once, and the sidecar refuses to start on any.
package settings

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MediaMTX listener ports. The sidecar's seed config sets them, so they are constants rather than settings.
const (
	MediaMTXAPIPort      = 9997
	MediaMTXMetricsPort  = 9998
	MediaMTXPlaybackPort = 9996
	MediaMTXHLSPort      = 8888
	MediaMTXWebRTCPort   = 8889
)

// Settings is the validated configuration.
type Settings struct {
	PublicURL      *url.URL       // how users reach the UI; decides cookie flags and the allowed Origin
	PublicHost     string         // host stream clients connect to; offered to WebRTC clients
	TrustedProxies []netip.Prefix // peers whose X-Forwarded-* headers are believed
	StackSubnet    netip.Prefix   // the compose network; invalid (zero) means detect it
	Listen         string         // public listener (HTTPS with TLS "acme")
	InternalListen string         // internal listener: MediaMTX callbacks and the healthcheck
	InternalURL    *url.URL       // how MediaMTX reaches the internal listener
	MediaMTXHost   string         // MediaMTX's hostname on the stack network
	DataDir        string
	MediaMTXBin    string // the pinned MediaMTX binary, used only for --validate-conf
	HoldingDir     string // holding clips; mounted at the same path in MediaMTX (read-only)
	// Recordings: the budget, in bytes (0: none), the free space pruning keeps, and the free space below
	// which recording is switched off; how often they are checked; and the caps on exports.
	RecordingsMaxBytes      int64
	RecordingsMinFreeBytes  int64
	RecordingsCriticalBytes int64
	RecordingsCheckEvery    time.Duration
	ExportMaxDuration       time.Duration
	ExportMaxBytes          int64
	ExportConcurrency       int
	// Passkeys: offered when the UI has a secure origin (HTTPS, or localhost); "on" forces them (a test stack whose
	// browser treats its plain-HTTP origin as secure), "off" hides them.
	Passkeys bool
	// HTTPS (the public install): "off" serves plain HTTP behind a reverse proxy; "acme" gets certificates for
	// PUBLIC_URL's host from an ACME CA (Let's Encrypt) and serves HTTPS itself, with HTTPListen answering the CA's
	// challenges and redirecting everything else.
	TLS           string
	ACMEEmail     string
	ACMEDirectory string
	ACMECACert    string // a PEM file the ACME server's own certificate chains to (a private CA, tests); "" for the system roots
	HTTPListen    string
	// ExposureControl: the Exposure page and automatic exposure, through the host helper (mtx-portgate). Off, the
	// stream ports are simply as published.
	ExposureControl bool
	// MediaMTX's log: rotated by copy-truncate past MediaMTXLogMaxBytes, keeping MediaMTXLogKeep gzipped copies.
	MediaMTXLogMaxBytes  int64
	MediaMTXLogKeep      int
	HistoryDays          int // the long-term history's retention
	BackupMaxUploadBytes int64
	LogLevel             slog.Level
	SessionIdleTimeout   time.Duration
	SessionMaxAge        time.Duration
	LoginRatePerMinute   int
	LockoutThreshold     int
	LockoutDuration      time.Duration
}

// Var documents one environment variable.
type Var struct {
	Name        string
	Default     string // empty for required variables and for "not set"
	Required    bool
	Description string
}

// Vars lists every variable Load reads, in documentation order.
var Vars = []Var{
	{Name: "MTXUI_PUBLIC_URL", Required: true, Description: "URL of the UI as users reach it, e.g. `https://mtx.example.com`. Its scheme decides the cookies' `Secure` flag, and state-changing requests must come from its origin. No path, query or credentials."},
	{Name: "MTXUI_PUBLIC_HOST", Required: true, Description: "Hostname or IP address that stream clients connect to. Offered to WebRTC clients as the ICE host."},
	{Name: "MTXUI_TRUSTED_PROXIES", Description: "Comma-separated IPs or CIDRs of reverse proxies whose `X-Forwarded-For` and `X-Forwarded-Proto` headers are believed. For a proxy on the Docker host, the gateway of the stack subnet. Trusting every address (`0.0.0.0/0`, `::/0`) is refused."},
	{Name: "MTXUI_STACK_SUBNET", Description: "CIDR of the stack's Docker network, used for MediaMTX's trusted proxies. Detected from the sidecar's own address when not set."},
	{Name: "MTXUI_LISTEN", Default: ":9080", Description: "Public listener (UI, API, proxies): HTTPS with `MTXUI_TLS=acme`, else plain HTTP to publish only behind your reverse proxy."},
	{Name: "MTXUI_TLS", Default: "off", Description: "`acme`: get and renew a certificate for MTXUI_PUBLIC_URL's host from Let's Encrypt (or MTXUI_ACME_DIRECTORY) and serve HTTPS on MTXUI_LISTEN. Needs MTXUI_PUBLIC_URL on `https://` with a domain name, and port 80 (MTXUI_HTTP_LISTEN) or 443 reachable from the internet. `off`: plain HTTP, for a reverse proxy in front."},
	{Name: "MTXUI_ACME_EMAIL", Description: "Contact address for the certificate authority (expiry warnings). Optional."},
	{Name: "MTXUI_ACME_DIRECTORY", Default: "https://acme-v02.api.letsencrypt.org/directory", Description: "The ACME directory. Let's Encrypt's staging directory (`https://acme-staging-v02.api.letsencrypt.org/directory`) is for trying things out."},
	{Name: "MTXUI_ACME_CA_CERT", Description: "Path of a PEM file to trust for the ACME server's own HTTPS (a private ACME server such as step-ca). Not needed for Let's Encrypt."},
	{Name: "MTXUI_HTTP_LISTEN", Default: ":9082", Description: "With `MTXUI_TLS=acme`: plain HTTP for the certificate authority's challenges; every other request is redirected to HTTPS. Publish it as port 80."},
	{Name: "MTXUI_EXPOSURE_CONTROL", Default: "off", Description: "`on`: the Exposure page and automatic exposure open and close stream ports through the host helper mtx-portgate (docs/exposure-control.md). `off`: the stream ports are as published."},
	{Name: "MTXUI_INTERNAL_LISTEN", Default: ":9081", Description: "Internal listener for MediaMTX's authentication callback and the healthcheck. Never publish it."},
	{Name: "MTXUI_INTERNAL_URL", Default: "http://sidecar:9081", Description: "How MediaMTX reaches the internal listener; written into `mediamtx.yml` as the base of `authHTTPAddress`."},
	{Name: "MTXUI_MEDIAMTX_HOST", Default: "mediamtx", Description: "MediaMTX's hostname on the stack network."},
	{Name: "MTXUI_DATA_DIR", Default: "/data", Description: "Data root: `config/` (mediamtx.yml), `state/` (database and secrets), `logs/`, `recordings/`, `hooks/`, `backups/`."},
	{Name: "MTXUI_HOLDING_DIR", Default: "/holding", Description: "Holding clips, played while nobody streams to a stream. MediaMTX must see the directory at the same path (read-only), since `mediamtx.yml` names the files by it."},
	{Name: "MTXUI_RECORDINGS_MAX_GB", Default: "0", Description: "Storage budget for recordings in GB (decimals allowed); the oldest segments are deleted, through MediaMTX's API, to stay under it. 0 means no budget (free space still applies)."},
	{Name: "MTXUI_RECORDINGS_MIN_FREE_GB", Default: "20", Description: "Free space to keep on the recordings filesystem: below it, the oldest segments are deleted."},
	{Name: "MTXUI_RECORDINGS_CRITICAL_FREE_GB", Default: "5", Description: "Free space below which recording is switched off for every path (with a banner and an audit entry) until an admin switches it back on. Less than MTXUI_RECORDINGS_MIN_FREE_GB."},
	{Name: "MTXUI_RECORDINGS_CHECK_EVERY", Default: "1m", Description: "How often recordings' disk use is measured and the budget enforced (1s to 1h)."},
	{Name: "MTXUI_EXPORT_MAX_DURATION", Default: "2h", Description: "Longest range one recording export may cover (1m to 24h)."},
	{Name: "MTXUI_EXPORT_MAX_GB", Default: "8", Description: "Largest recording export in GB: the download stops there."},
	{Name: "MTXUI_EXPORT_CONCURRENCY", Default: "2", Description: "Recording exports running at once (1 to 16); more are refused until one ends."},
	{Name: "MTXUI_PASSKEYS", Default: "auto", Description: "Passkeys (WebAuthn sign-in): `auto` offers them when MTXUI_PUBLIC_URL is HTTPS or localhost (browsers allow them only there; the relying party is its host), `on` (needs a domain name or localhost there, not an IP address) or `off`."},
	{Name: "MTXUI_MEDIAMTX_BIN", Default: "/usr/libexec/mtxui/mediamtx", Description: "The MediaMTX binary the image carries, used only to run `--validate-conf` on every config before it is written."},
	{Name: "MTXUI_MEDIAMTX_LOG_MAX_MB", Default: "20", Description: "MediaMTX's log file is rotated (copy-truncate, gzipped) when it grows past this many MB (1 to 1024)."},
	{Name: "MTXUI_MEDIAMTX_LOG_KEEP", Default: "5", Description: "Rotated copies of MediaMTX's log to keep (0 to 50); the log viewer searches them too."},
	{Name: "MTXUI_HISTORY_DAYS", Default: "30", Description: "Days of per-minute history (bandwidth, viewers, streams) kept for the charts (1 to 365)."},
	{Name: "MTXUI_BACKUP_MAX_UPLOAD_MB", Default: "2048", Description: "Largest backup file accepted for a restore, in MB (1 to 65536). Backups live in `backups/` under the data root."},
	{Name: "MTXUI_LOG_LEVEL", Default: "info", Description: "`debug`, `info`, `warn` or `error`."},
	{Name: "MTXUI_SESSION_IDLE_TIMEOUT", Default: "12h", Description: "A session ends after this long without a request (5m to 720h)."},
	{Name: "MTXUI_SESSION_MAX_AGE", Default: "168h", Description: "A session ends this long after sign-in, however active (at least the idle timeout, at most 2160h)."},
	{Name: "MTXUI_LOGIN_RATE_PER_MINUTE", Default: "20", Description: "Sign-in attempts allowed per client IP and minute (1 to 600)."},
	{Name: "MTXUI_LOCKOUT_THRESHOLD", Default: "5", Description: "Failed sign-ins for one username before it is locked (1 to 100). Unknown usernames lock the same way, so locks reveal nothing."},
	{Name: "MTXUI_LOCKOUT_DURATION", Default: "15m", Description: "How long a locked username stays locked (1m to 24h)."},
}

// Load reads and validates the settings.
func Load(getenv func(string) string) (*Settings, error) {
	p := &parser{getenv: getenv}
	s := &Settings{
		PublicURL:               p.publicURL("MTXUI_PUBLIC_URL"),
		PublicHost:              p.host("MTXUI_PUBLIC_HOST"),
		TrustedProxies:          p.prefixes("MTXUI_TRUSTED_PROXIES"),
		StackSubnet:             p.prefix("MTXUI_STACK_SUBNET"),
		Listen:                  p.listenAddr("MTXUI_LISTEN"),
		TLS:                     p.oneOf("MTXUI_TLS", "off", "acme"),
		ACMEEmail:               p.get("MTXUI_ACME_EMAIL"),
		ACMEDirectory:           p.get("MTXUI_ACME_DIRECTORY"),
		ACMECACert:              p.get("MTXUI_ACME_CA_CERT"),
		HTTPListen:              p.listenAddr("MTXUI_HTTP_LISTEN"),
		ExposureControl:         p.oneOf("MTXUI_EXPOSURE_CONTROL", "off", "on") == "on",
		InternalListen:          p.listenAddr("MTXUI_INTERNAL_LISTEN"),
		InternalURL:             p.internalURL("MTXUI_INTERNAL_URL"),
		MediaMTXHost:            p.host("MTXUI_MEDIAMTX_HOST"),
		DataDir:                 p.absPath("MTXUI_DATA_DIR"),
		MediaMTXBin:             p.absPath("MTXUI_MEDIAMTX_BIN"),
		HoldingDir:              p.absPath("MTXUI_HOLDING_DIR"),
		RecordingsMaxBytes:      p.gigabytes("MTXUI_RECORDINGS_MAX_GB"),
		RecordingsMinFreeBytes:  p.gigabytes("MTXUI_RECORDINGS_MIN_FREE_GB"),
		RecordingsCriticalBytes: p.gigabytes("MTXUI_RECORDINGS_CRITICAL_FREE_GB"),
		RecordingsCheckEvery:    p.duration("MTXUI_RECORDINGS_CHECK_EVERY", time.Second, time.Hour),
		ExportMaxDuration:       p.duration("MTXUI_EXPORT_MAX_DURATION", time.Minute, 24*time.Hour),
		ExportMaxBytes:          p.gigabytes("MTXUI_EXPORT_MAX_GB"),
		ExportConcurrency:       p.integer("MTXUI_EXPORT_CONCURRENCY", 1, 16),
		MediaMTXLogMaxBytes:     int64(p.integer("MTXUI_MEDIAMTX_LOG_MAX_MB", 1, 1024)) << 20,
		MediaMTXLogKeep:         p.integer("MTXUI_MEDIAMTX_LOG_KEEP", 0, 50),
		HistoryDays:             p.integer("MTXUI_HISTORY_DAYS", 1, 365),
		BackupMaxUploadBytes:    int64(p.integer("MTXUI_BACKUP_MAX_UPLOAD_MB", 1, 65536)) << 20,
		LogLevel:                p.logLevel("MTXUI_LOG_LEVEL"),
		SessionIdleTimeout:      p.duration("MTXUI_SESSION_IDLE_TIMEOUT", 5*time.Minute, 720*time.Hour),
		SessionMaxAge:           p.duration("MTXUI_SESSION_MAX_AGE", 5*time.Minute, 2160*time.Hour),
		LoginRatePerMinute:      p.integer("MTXUI_LOGIN_RATE_PER_MINUTE", 1, 600),
		LockoutThreshold:        p.integer("MTXUI_LOCKOUT_THRESHOLD", 1, 100),
		LockoutDuration:         p.duration("MTXUI_LOCKOUT_DURATION", time.Minute, 24*time.Hour),
	}
	if s.Listen != "" && s.Listen == s.InternalListen {
		p.fail("MTXUI_INTERNAL_LISTEN", "must differ from MTXUI_LISTEN")
	}
	if s.TLS == "acme" {
		switch {
		case s.PublicURL == nil:
		case s.PublicURL.Scheme != "https":
			p.fail("MTXUI_TLS", "acme needs MTXUI_PUBLIC_URL on https://")
		case !relyingParty(s.PublicURL.Hostname()) || s.PublicURL.Hostname() == "localhost":
			p.fail("MTXUI_TLS", "acme needs MTXUI_PUBLIC_URL on a domain name (certificates are not issued for IP addresses or single-label names)")
		}
		if s.HTTPListen == s.Listen || s.HTTPListen == s.InternalListen {
			p.fail("MTXUI_HTTP_LISTEN", "must differ from MTXUI_LISTEN and MTXUI_INTERNAL_LISTEN")
		}
		if u, err := url.Parse(s.ACMEDirectory); err != nil || u.Scheme != "https" || u.Host == "" {
			p.fail("MTXUI_ACME_DIRECTORY", "must be an https:// URL")
		}
		if s.ACMECACert != "" && !path.IsAbs(s.ACMECACert) {
			p.fail("MTXUI_ACME_CA_CERT", "must be an absolute path")
		}
	}
	switch v := p.get("MTXUI_PASSKEYS"); v {
	case "auto":
		s.Passkeys = s.PublicURL != nil && (s.PublicURL.Scheme == "https" || s.PublicURL.Hostname() == "localhost") &&
			relyingParty(s.PublicURL.Hostname())
	case "on":
		s.Passkeys = true
		if s.PublicURL == nil || !relyingParty(s.PublicURL.Hostname()) {
			p.fail("MTXUI_PASSKEYS", "needs MTXUI_PUBLIC_URL on a domain name (or localhost), not an IP address or a single-label name")
		}
	case "off":
	default:
		p.fail("MTXUI_PASSKEYS", "must be auto, on or off")
	}
	if s.RecordingsCriticalBytes >= s.RecordingsMinFreeBytes && s.RecordingsMinFreeBytes > 0 {
		p.fail("MTXUI_RECORDINGS_CRITICAL_FREE_GB", "must be less than MTXUI_RECORDINGS_MIN_FREE_GB")
	}
	if s.SessionMaxAge != 0 && s.SessionMaxAge < s.SessionIdleTimeout {
		p.fail("MTXUI_SESSION_MAX_AGE", "must be at least MTXUI_SESSION_IDLE_TIMEOUT")
	}
	if err := errors.Join(p.errs...); err != nil {
		return nil, err
	}
	return s, nil
}

// Secure reports whether the UI is served over HTTPS, which decides the cookies' Secure flag and __Host- prefix.
func (s *Settings) Secure() bool { return s.PublicURL.Scheme == "https" }

// Origin is the only origin allowed to make state-changing requests.
func (s *Settings) Origin() string { return s.PublicURL.Scheme + "://" + s.PublicURL.Host }

// MediaMTXLog is MediaMTX's log file (logFile in the seed config, under the shared logs directory).
func (s *Settings) MediaMTXLog() string { return path.Join(s.DataDir, "logs", "mediamtx.log") }

// BackupsDir holds the backups (encrypted).
func (s *Settings) BackupsDir() string { return path.Join(s.DataDir, "backups") }

// ConfigDir holds mediamtx.yml.
func (s *Settings) ConfigDir() string { return path.Join(s.DataDir, "config") }

// ConfigFile is mediamtx.yml.
func (s *Settings) ConfigFile() string { return path.Join(s.ConfigDir(), "mediamtx.yml") }

// StateDir holds the database and the secrets generated on first run.
func (s *Settings) StateDir() string { return path.Join(s.DataDir, "state") }

// DBPath is the SQLite database.
func (s *Settings) DBPath() string { return path.Join(s.StateDir(), "mtxui.db") }

// AuthURL is where MediaMTX sends authentication requests (authHTTPAddress).
func (s *Settings) AuthURL() string { return s.InternalURL.JoinPath("internal", "auth").String() }

// MediaMTXURL is the base URL of one of MediaMTX's HTTP listeners.
func (s *Settings) MediaMTXURL(port int) string {
	return "http://" + net.JoinHostPort(s.MediaMTXHost, strconv.Itoa(port))
}

type parser struct {
	getenv func(string) string
	errs   []error
}

// get returns the variable's value, or its default when unset.
func (p *parser) get(name string) string {
	v := strings.TrimSpace(p.getenv(name))
	if v != "" {
		return v
	}
	for _, d := range Vars {
		if d.Name == name {
			if d.Required {
				p.fail(name, "is required")
			}
			return d.Default
		}
	}
	panic("settings: undeclared variable " + name) // a programming error, caught by tests
}

func (p *parser) fail(name, format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf("%s %s", name, fmt.Sprintf(format, args...)))
}

func (p *parser) publicURL(name string) *url.URL {
	v := p.get(name)
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	switch {
	case err != nil:
		p.fail(name, "is not a URL: %v", err)
		return nil
	case u.Scheme != "http" && u.Scheme != "https":
		p.fail(name, "must start with http:// or https://")
	case u.Host == "" || u.Hostname() == "":
		p.fail(name, "has no host")
	case u.User != nil:
		p.fail(name, "must not contain credentials")
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "":
		p.fail(name, "must not have a path, query or fragment (the UI is served at the root)")
	default:
		return &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}
	}
	return nil
}

func (p *parser) internalURL(name string) *url.URL {
	v := p.get(name)
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		p.fail(name, "must be a plain http://host:port URL")
		return nil
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}
}

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (p *parser) host(name string) string {
	v := strings.ToLower(p.get(name))
	if v == "" {
		return ""
	}
	if _, err := netip.ParseAddr(v); err == nil {
		return v
	}
	labels := strings.Split(v, ".")
	valid := len(v) <= 253
	for _, l := range labels {
		valid = valid && hostLabel.MatchString(l)
	}
	if !valid {
		p.fail(name, "is neither a hostname nor an IP address: %q", v)
		return ""
	}
	return v
}

func (p *parser) prefixes(name string) []netip.Prefix {
	var out []netip.Prefix
	for _, item := range strings.Split(p.get(name), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		pfx, err := parsePrefix(item)
		if err != nil {
			p.fail(name, "has an invalid entry %q", item)
			continue
		}
		if pfx.Bits() == 0 {
			p.fail(name, "must not trust every address (%s): any client could then choose its own IP", pfx)
			continue
		}
		out = append(out, pfx)
	}
	return out
}

func (p *parser) prefix(name string) netip.Prefix {
	v := p.get(name)
	if v == "" {
		return netip.Prefix{}
	}
	pfx, err := netip.ParsePrefix(v)
	if err != nil {
		p.fail(name, "is not a CIDR: %q", v)
		return netip.Prefix{}
	}
	return pfx.Masked()
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		pfx, err := netip.ParsePrefix(s)
		return pfx.Masked(), err
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()), nil
}

func (p *parser) listenAddr(name string) string {
	v := p.get(name)
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		p.fail(name, "must be host:port or :port")
		return ""
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		p.fail(name, "has an invalid port %q", port)
		return ""
	}
	if host != "" {
		if _, err := netip.ParseAddr(host); err != nil {
			p.fail(name, "must use an IP address or no host, not %q", host)
			return ""
		}
	}
	return v
}

func (p *parser) absPath(name string) string {
	v := p.get(name)
	if !path.IsAbs(v) || path.Clean(v) != v {
		p.fail(name, "must be an absolute, clean path")
		return ""
	}
	return v
}

func (p *parser) logLevel(name string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(p.get(name))); err != nil {
		p.fail(name, "must be debug, info, warn or error")
	}
	return l
}

func (p *parser) duration(name string, lo, hi time.Duration) time.Duration {
	d, err := time.ParseDuration(p.get(name))
	if err != nil || d < lo || d > hi {
		p.fail(name, "must be a duration between %s and %s", lo, hi)
		return 0
	}
	return d
}

// gigabytes reads a size in GB (decimals allowed, so tests can use megabytes) as bytes.
func (p *parser) gigabytes(name string) int64 {
	f, err := strconv.ParseFloat(p.get(name), 64)
	if err != nil || f < 0 || f > 1e6 {
		p.fail(name, "must be a number of GB from 0 to 1000000")
		return 0
	}
	return int64(f * (1 << 30))
}

func (p *parser) integer(name string, lo, hi int) int {
	n, err := strconv.Atoi(p.get(name))
	if err != nil || n < lo || n > hi {
		p.fail(name, "must be a whole number between %d and %d", lo, hi)
		return 0
	}
	return n
}

// relyingParty reports whether host can be a WebAuthn relying party: a domain name or localhost, never an IP address
// or a single-label name (browsers and go-webauthn refuse both).
func relyingParty(host string) bool {
	return host == "localhost" || (net.ParseIP(host) == nil && strings.Contains(strings.Trim(host, "."), "."))
}

func (p *parser) oneOf(name string, values ...string) string {
	v := p.get(name)
	if !slices.Contains(values, v) {
		p.fail(name, "must be one of %s", strings.Join(values, ", "))
		return values[0]
	}
	return v
}
