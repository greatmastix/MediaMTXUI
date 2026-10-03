package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mtxui/internal/auth"
	"mtxui/internal/store"
)

// request builds a request the way h.do sends one, for a test that serves it itself: from another goroutine, or
// with a body it writes later.
func (h *harness) request(method, path string, body io.Reader, opts ...reqOpt) *http.Request {
	h.t.Helper()
	req := httptest.NewRequest(method, "http://mtx.example.com"+path, body)
	req.RemoteAddr = caddy + ":40000"
	req.Header.Set("X-Forwarded-For", client)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "__Host-mtxui_session", Value: h.cookie})
	req.Header.Set(auth.CSRFHeader, h.csrf)
	for _, o := range opts {
		o(req)
	}
	return req
}

// serve answers a request built by h.request, in its own goroutine; the channel gets the answer.
func (h *harness) serve(req *http.Request) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.public.ServeHTTP(rec, req)
		done <- rec
	}()
	return done
}

// Regenerations of one key at the same time each retire the key they replaced: afterwards the stream's key is the
// only valid one, where each used to revoke only the key it had read at the start and leave the keys in between
// valid, unlinked and invisible on the stream page.
func TestKeyRegenerationsAtOnce(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/keys"}), &st)

	const n = 10
	answers := make([]<-chan *httptest.ResponseRecorder, n)
	for i := range answers {
		answers[i] = h.serve(h.request("POST", "/api/v1/streams/"+itoa(st.ID)+"/keys/publish/regenerate", nil))
	}
	names := map[string]bool{}
	for _, a := range answers {
		rec := <-a
		var key StreamKey
		h.json(rec, &key)
		if rec.Code != http.StatusOK || key.Name == "" {
			t.Fatalf("regenerate: %d %s", rec.Code, rec.Body)
		}
		names[key.Name] = true
	}
	got, err := h.st.StreamByID(ctx, st.ID)
	if err != nil || got.PublishKeyID == nil {
		t.Fatalf("stream %+v: %v", got, err)
	}
	list, _ := h.st.ListCredentials(ctx)
	var valid []string
	for _, c := range list {
		if strings.HasPrefix(c.Name, "key-") && c.RevokedAt == nil {
			valid = append(valid, c.Name)
			if c.ID != *got.PublishKeyID || !names[c.Name] {
				t.Errorf("%s is valid but is not the stream's key", c.Name)
			}
		}
	}
	if len(valid) != 1 {
		t.Errorf("valid publish keys after %d regenerations: %v", n, valid)
	}
}

// A patch is checked whole before anything changes: a bad field no longer leaves the recording and the viewer cap
// switched on in mediamtx.yml behind a refusal, and the audit entry names the action and the stream.
func TestStreamPatchChecksFirst(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/news"}), &st)
	before := h.mtxFile()
	for _, bad := range []map[string]any{
		{"record": true, "maxReaders": 1, "format": "4k"},
		{"record": true, "maxReaders": 2, "title": ""},
		{"record": true, "maxReaders": 3, "target": "Nope!"},
		{"record": true, "maxReaders": 4, "ownerId": 999},
		{"record": true, "maxReaders": 5, "holding": "file"}, // no clip yet
	} {
		if rec := h.do("PATCH", "/api/v1/streams/"+itoa(st.ID), bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", bad, rec.Code, rec.Body)
		}
		if after := h.mtxFile(); after != before {
			t.Errorf("%v changed mediamtx.yml:\n%s", bad, after)
		}
		if action, target, details := lastAudit(t, h); action != "stream.update" || target != "live/news" || details["status"] != 400.0 {
			t.Errorf("%v: audit %s %s %v", bad, action, target, details)
		}
	}
	rec := h.do("PATCH", "/api/v1/streams/"+itoa(st.ID), map[string]any{"record": true, "maxReaders": 6, "title": "News"})
	if rec.Code != http.StatusOK || decode(rec)["title"] != "News" || !strings.Contains(h.mtxFile(), "maxReaders: 6") {
		t.Fatalf("a good patch: %d %s", rec.Code, rec.Body)
	}
	if _, _, details := lastAudit(t, h); details["record"] != true || details["maxReaders"] != 6.0 || details["title"] != "News" {
		t.Errorf("audit details %v", details)
	}
}

// Reloads of the owners cache run one at a time, from the read to the swap: a reload that read the streams before a
// change used to be able to swap its maps in after the reload that followed the change, which kept a stream made
// private anonymously readable (nothing in between can be paused from here, so this holds a reload "in progress").
func TestOwnersReloadsOneAtATime(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	st, err := h.st.CreateStream(ctx, store.Stream{Name: "live/flip", Title: "flip", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if !h.srv.IsPublicPath("live/flip") {
		t.Fatal("not public to begin with")
	}
	h.srv.owners.reload.Lock() // a reload that has read the streams and not swapped yet
	st.Public = false
	if err := h.st.UpdateStream(ctx, st); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = h.srv.reloadOwners(ctx)
		close(done)
	}()
	select {
	case <-done:
		t.Error("a reload ran while another was in progress")
	case <-time.After(200 * time.Millisecond):
	}
	h.srv.owners.reload.Unlock()
	<-done
	if h.srv.IsPublicPath("live/flip") {
		t.Error("still public after the reload that followed the change")
	}
}
