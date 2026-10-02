package app

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A client that sends a body slowly is cut off after bodyReadTimeout; a long-lived response without a body (the event
// stream) is not, and neither is a request whose body arrived in time.
func TestBodyDeadline(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })
	h, _ := signedInServer(t)
	srv := httptest.NewServer(h.public)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /api/v1/auth/login HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"user")
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = bufio.NewReader(conn).ReadString('\n') // the answer, or the connection closing
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("a trickled body held the connection for %v", took)
	}

	s, code := h.openStream(srv, "")
	if s == nil {
		t.Fatalf("event stream refused: %d", code)
	}
	time.Sleep(3 * bodyReadTimeout)
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	select {
	case ev, ok := <-s.events:
		if !ok {
			t.Fatal("the event stream ended at the body deadline")
		}
		_ = ev
	default:
	}
}
