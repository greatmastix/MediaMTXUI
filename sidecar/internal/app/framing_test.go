package app

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// TestRequestFraming sends the sidecar's servers the requests that request smuggling is made of: each is refused, or
// read one way only, and nothing after an ambiguous body is ever taken for a second request.
func TestRequestFraming(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = newServer("", http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+string(b))
		mu.Unlock()
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Start()
	defer srv.Close()

	const next = "GET /smuggled HTTP/1.1\r\nHost: x\r\n\r\n"
	for _, c := range []struct {
		name, raw string
		codes     []int
		seen      []string
	}{
		{"length and chunked", "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n" + next, []int{200}, []string{"/a "}},
		{"chunked, then a pipelined request", "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n" + next, []int{200}, []string{"/a "}},
		{"two lengths", "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\nContent-Length: 39\r\n\r\nabcd" + next, []int{400}, nil},
		{"signed length", "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: +4\r\n\r\nabcd", []int{400}, nil},
		{"unknown coding", "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: xchunked\r\n\r\n0\r\n\r\n", []int{501}, nil},
		{"coding list", "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked, identity\r\n\r\n0\r\n\r\n", []int{501}, nil},
		{"identity with a length", "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: identity\r\nContent-Length: 4\r\n\r\nabcd", []int{501}, nil},
		{"space before the colon", "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding : chunked\r\n\r\n0\r\n\r\n", []int{400}, nil},
		{"folded header", "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: x\r\n chunked\r\n\r\n0\r\n\r\n", []int{501}, nil},
		{"two hosts", "GET /a HTTP/1.1\r\nHost: x\r\nHost: y\r\n\r\n", []int{400}, nil},
		{"no host", "GET /a HTTP/1.1\r\n\r\n", []int{400}, nil},
		{"a plain pipeline still works", "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\n\r\nabcd" + next, []int{200, 200}, []string{"/a abcd", "/smuggled "}},
	} {
		mu.Lock()
		seen = nil
		mu.Unlock()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(conn, c.raw)
		_ = conn.(*net.TCPConn).CloseWrite() // everything is sent; the server answers what it reads, then closes
		br := bufio.NewReader(conn)
		var codes []int
		for {
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				break // the server closed the connection
			}
			codes = append(codes, resp.StatusCode)
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		_ = conn.Close()
		mu.Lock()
		if !slices.Equal(codes, c.codes) || !slices.Equal(seen, c.seen) {
			t.Errorf("%s: answers %v, handled %q; want %v, %q", c.name, codes, seen, c.codes, c.seen)
		}
		mu.Unlock()
	}
}
