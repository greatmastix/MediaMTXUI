package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With TLS, the public listener serves HTTPS (with HSTS from the security headers) and the plain listener only
// redirects to PUBLIC_URL, whatever Host the request names.
func TestRunTLS(t *testing.T) {
	h := newHarness(t, nil, fast)
	ts := httptest.NewUnstartedServer(nil) // only for its certificate
	ts.StartTLS()
	cert := ts.TLS.Certificates[0]
	ts.Close()
	pub, internal, plain := freeAddr(t), freeAddr(t), freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	l := Listeners{
		Public: pub, Internal: internal, HTTP: plain, HTTPHandler: redirectToHTTPS(h.srv.d.Settings.Origin()),
		PublicTLS: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	go func() { done <- Run(ctx, l, h.public, http.NotFoundHandler(), log) }()

	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	client := &http.Client{
		Transport:     &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		if resp, err = client.Get("https://" + pub + "/api/v1/setup"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Strict-Transport-Security") == "" || resp.ProtoMajor != 2 {
		t.Fatalf("https: %d, HSTS %q, HTTP/%d", resp.StatusCode, resp.Header.Get("Strict-Transport-Security"), resp.ProtoMajor)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+plain+"/streams?x=1", nil)
	req.Host = "evil.example"
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusMovedPermanently ||
		resp.Header.Get("Location") != origin+"/streams?x=1" {
		t.Fatalf("redirect: %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}
	if resp, err := client.Post("http://"+plain+"/api/v1/auth/login", "application/json", nil); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a POST to plain HTTP: %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A finalize response without the order's Location (an asynchronous CA like Pebble) gets the order's URL from the
// order objects seen before; responses that have it, or are not orders, pass unchanged.
func TestOrderLocations(t *testing.T) {
	ca := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/new-order":
			w.Header().Set("Location", "http://"+r.Host+"/order/1")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"status":"pending","finalize":"http://`+r.Host+`/finalize/1"}`)
		case "/finalize/1":
			_, _ = io.WriteString(w, `{"status":"processing","finalize":"http://`+r.Host+`/finalize/1"}`)
		case "/order/2":
			_, _ = io.WriteString(w, `{"status":"ready","finalize":"http://`+r.Host+`/finalize/2"}`)
		case "/finalize/2":
			_, _ = io.WriteString(w, `{"status":"processing","finalize":"http://`+r.Host+`/finalize/2"}`)
		default:
			_, _ = io.WriteString(w, `{"status":"valid"}`)
		}
	}))
	defer ca.Close()
	c := &http.Client{Transport: &orderLocations{next: http.DefaultTransport, orders: map[string]string{}}}
	post := func(path string) http.Header {
		t.Helper()
		res, err := c.Post(ca.URL+path, "application/jose+json", nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body) // the body is still readable after the transport looked at it
		_ = res.Body.Close()
		if len(b) == 0 {
			t.Fatalf("%s: empty body", path)
		}
		return res.Header
	}
	post("/new-order")
	if loc := post("/finalize/1").Get("Location"); loc != ca.URL+"/order/1" {
		t.Errorf("finalize of a new order: %q", loc)
	}
	post("/order/2")
	if loc := post("/finalize/2").Get("Location"); loc != ca.URL+"/order/2" {
		t.Errorf("finalize of a fetched order: %q", loc)
	}
	if loc := post("/other").Get("Location"); loc != "" {
		t.Errorf("not an order: %q", loc)
	}
}

// Each ACME directory has its own cache, so leaving the staging server gets a real certificate at once.
func TestACMECacheDir(t *testing.T) {
	const (
		prod    = "https://acme-v02.api.letsencrypt.org/directory"
		staging = "https://acme-staging-v02.api.letsencrypt.org/directory"
	)
	p, s := acmeCacheDir("/data/state", prod), acmeCacheDir("/data/state", staging)
	if p == s || p != acmeCacheDir("/data/state", prod) {
		t.Fatalf("prod %s, staging %s", p, s)
	}
	if !strings.HasPrefix(p, "/data/state/acme/acme-v02.api.letsencrypt.org-") {
		t.Errorf("prod cache %s", p)
	}
	// Two directories on one host, and a host with a port or odd characters, stay apart and inside state/acme.
	a, b := acmeCacheDir("/s", "https://ca.lan:9000/acme/one/directory"), acmeCacheDir("/s", "https://ca.lan:9000/acme/two/directory")
	if a == b || !strings.HasPrefix(a, "/s/acme/ca.lan_9000-") {
		t.Errorf("one host: %s, %s", a, b)
	}
	if d := acmeCacheDir("/s", "https://../..%2f@evil/directory"); filepath.Dir(d) != "/s/acme" {
		t.Errorf("odd host escapes: %s", d)
	}
}
