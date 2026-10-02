package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"mtxui/internal/settings"
)

// HTTPS for the public install (MTXUI_TLS=acme): certificates for PUBLIC_URL's host from an ACME CA (Let's Encrypt),
// obtained on the first HTTPS request and renewed before they expire (x/crypto's autocert), cached in state/acme.
// The CA proves the domain with HTTP-01 on the plain listener (published as port 80) or TLS-ALPN-01 on the HTTPS
// one (443); everything else on the plain listener is redirected to PUBLIC_URL.

// ACME returns the HTTPS listener's TLS config and the plain listener's handler.
func ACME(cfg *settings.Settings) (*tls.Config, http.Handler, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	client := &acme.Client{DirectoryURL: cfg.ACMEDirectory, UserAgent: "mediamtx-ui"}
	if cfg.ACMECACert != "" {
		pem, err := os.ReadFile(cfg.ACMECACert)
		if err != nil {
			return nil, nil, fmt.Errorf("MTXUI_ACME_CA_CERT: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, errors.New("MTXUI_ACME_CA_CERT: no certificate in the file")
		}
		base.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	client.HTTPClient = &http.Client{Timeout: time.Minute, Transport: &orderLocations{next: base, orders: map[string]string{}}}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.PublicURL.Hostname()),
		Cache:      autocert.DirCache(filepath.Join(cfg.StateDir(), "acme")),
		Email:      cfg.ACMEEmail,
		Client:     client,
	}
	tc := m.TLSConfig()
	tc.MinVersion = tls.VersionTLS12
	return tc, m.HTTPHandler(redirectToHTTPS(cfg.Origin())), nil
}

// redirectToHTTPS sends every request to the same path on origin: never to the Host the request names.
func redirectToHTTPS(origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Use HTTPS: "+origin, http.StatusBadRequest)
			return
		}
		// Always onto origin: the request only adds a path, so this cannot lead elsewhere.
		http.Redirect(w, r, origin+r.URL.RequestURI(), http.StatusMovedPermanently) //nolint:gosec // G710: see above
	})
}

// Listeners are what Run serves.
type Listeners struct {
	Public, Internal string
	PublicTLS        *tls.Config  // HTTPS on Public when set
	HTTP             string       // with PublicTLS: the plain listener for ACME challenges and redirects
	HTTPHandler      http.Handler // its handler
}

// WarmCertificate asks for the certificate once the listeners are up, so the first visitor does not wait for it
// (and a failure shows in the log at once rather than as a browser error).
func WarmCertificate(ctx context.Context, tc *tls.Config, host string, log interface{ Warn(string, ...any) }) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if _, err := tc.GetCertificate(&tls.ClientHelloInfo{ServerName: host, SupportedProtos: []string{"h2", "http/1.1"}}); err != nil {
			log.Warn("no certificate yet; check that the domain points here and ports 80 or 443 are reachable", "host", host, "err", err)
		}
	}()
}

// orderLocations fills in the order's URL on finalize responses that leave out its Location header. A CA that
// finalizes asynchronously answers "processing", and x/crypto's client then waits on the order at that header: without
// it, at "" (Pebble, Let's Encrypt's test CA, answers so; Let's Encrypt itself sends the header). The order's URL is
// known from the order objects seen before: each names its finalize URL.
type orderLocations struct {
	next   http.RoundTripper
	mu     sync.Mutex
	orders map[string]string // finalize URL -> order URL
}

const maxOrders = 64

func (o *orderLocations) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := o.next.RoundTrip(r)
	if err != nil || r.Method != http.MethodPost || res.StatusCode/100 != 2 ||
		!strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		return res, err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	_ = res.Body.Close()
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	var order struct {
		Finalize string `json:"finalize"`
	}
	if json.Unmarshal(body, &order) != nil || order.Finalize == "" {
		return res, nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if loc := res.Header.Get("Location"); loc != "" && r.URL.String() != order.Finalize {
		if len(o.orders) >= maxOrders {
			clear(o.orders)
		}
		o.orders[order.Finalize] = loc // a new order: its URL is in the header
	} else if r.URL.String() != order.Finalize {
		o.orders[order.Finalize] = r.URL.String() // the order fetched at its own URL
	} else if loc == "" && o.orders[order.Finalize] != "" {
		res.Header.Set("Location", o.orders[order.Finalize]) // the finalize response without one
	}
	return res, nil
}
