package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestBuildForward(t *testing.T) {
	for _, tc := range []struct {
		provider, server, key string
		dest, token, label    string // empty dest: refused
	}{
		{"twitch", "ignored", "live_123_abc", "rtmp://live.twitch.tv/app#live_123_abc", "", "rtmp://live.twitch.tv"},
		{"youtube", "", "abcd-efgh", "rtmps://a.rtmps.youtube.com/live2#abcd-efgh", "", "rtmps://a.rtmps.youtube.com"},
		{"kick", "rtmps://fa723fc1b171.global-contribute.live-video.net", "sk_us-west-2_x", "rtmps://fa723fc1b171.global-contribute.live-video.net/app#sk_us-west-2_x", "", "rtmps://fa723fc1b171.global-contribute.live-video.net"},
		{"custom", "rtmp://203.0.113.10:1936/live", "k", "rtmp://203.0.113.10:1936/live#k", "", "rtmp://203.0.113.10:1936"},
		{"custom", "rtmp://203.0.113.10/live/key", "", "rtmp://203.0.113.10/live/key", "", "rtmp://203.0.113.10"},
		{"custom", "WHIPS://ingest.example.com/whip", "token", "whips://ingest.example.com/whip", "token", "whips://ingest.example.com"},
		{"custom", "srt://203.0.113.10:9000?streamid=publish:x", "", "srt://203.0.113.10:9000?streamid=publish:x", "", "srt://203.0.113.10:9000"},
		{"twitch", "", "", "", "", ""},                            // no key
		{"twitch", "", "has space", "", "", ""},                   // not a key
		{"kick", "", "k", "", "", ""},                             // no server
		{"kick", "https://kick.com", "k", "", "", ""},             // not RTMP
		{"custom", "srt://203.0.113.10:9000", "k", "", "", ""},    // SRT carries it in the URL
		{"custom", "rtmp://203.0.113.10/app#k", "k2", "", "", ""}, // two keys
		{"custom", "http://203.0.113.10/app", "", "", "", ""},     // not a forward scheme
		{"custom", "203.0.113.10/app", "", "", "", ""},            // not a URL
		{"periscope", "rtmp://203.0.113.10/app", "k", "", "", ""}, // unknown provider
	} {
		d, label, err := buildForward(tc.provider, tc.server, tc.key)
		if tc.dest == "" {
			if err == nil {
				t.Errorf("%s %q %q accepted: %+v", tc.provider, tc.server, tc.key, d)
			}
			continue
		}
		if err != nil || d.Dest != tc.dest || d.Token != tc.token || label != tc.label {
			t.Errorf("%s %q %q: %+v %q %v", tc.provider, tc.server, tc.key, d, label, err)
		}
	}
}

// Forwarding end to end: the destination is sealed, written into the path's forward list only while on, next to an
// admin's own entry, with its state from MediaMTX; the key never shows in answers or the audit log.
func TestStreamForwards(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil, fast)
	h.completeSetup()
	const secret = "sk_live_SECRET123"
	var st streamJSON
	h.json(h.do("POST", "/api/v1/streams", map[string]any{"name": "live/fw"}), &st)
	base := "/api/v1/streams/" + itoa(st.ID) + "/forwards"
	if rec := h.do("PUT", "/api/v1/config/paths/live/fw", map[string]any{
		"config": map[string]any{"forward": []any{map[string]any{"dest": "srt://198.51.100.1:9000"}}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("an admin's forward: %d %s", rec.Code, rec.Body)
	}

	if rec := h.do("POST", base, map[string]any{"provider": "custom", "server": "rtmp://127.0.0.1/app", "key": "k"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a forward to loopback: %d %s", rec.Code, rec.Body)
	}
	rec := h.do("POST", base, map[string]any{"provider": "custom", "server": "rtmp://203.0.113.10/app", "key": secret, "enabled": true})
	var f ForwardInfo
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	h.json(rec, &f)
	if f.Label != "rtmp://203.0.113.10" || !f.Enabled || f.State != "idle" || strings.Contains(rec.Body.String(), secret) {
		t.Errorf("created %+v", f)
	}
	file := h.mtxFile()
	if !strings.Contains(file, "rtmp://203.0.113.10/app#"+secret) || !strings.Contains(file, "srt://198.51.100.1:9000") {
		t.Fatalf("mediamtx.yml:\n%s", file)
	}
	if action, target, details := lastAudit(t, h); action != "stream.forward.create" || target != "live/fw" ||
		strings.Contains(string(mustJSON(details)), secret) {
		t.Errorf("audit %s %s %v", action, target, details)
	}
	if rec := h.do("POST", base, map[string]any{"provider": "custom", "server": "rtmp://203.0.113.10/app", "key": secret}); rec.Code != http.StatusConflict {
		t.Errorf("the same destination again: %d", rec.Code)
	}

	// MediaMTX's state, with the key taken out of its error.
	h.mu.Lock()
	h.lists["/v3/paths/forward-dests/list"] = `[{"conf":{"dest":"rtmp://203.0.113.10/app#` + secret + `"},"state":"error",` +
		`"lastError":"publish to rtmp://203.0.113.10/app#` + secret + ` refused (` + secret + `)","outboundBytes":7}]`
	h.mu.Unlock()
	rec = h.do("GET", base, nil)
	var list []ForwardInfo
	h.json(rec, &list)
	if len(list) != 1 || list[0].State != "error" || list[0].LastError == "" || strings.Contains(rec.Body.String(), secret) {
		t.Errorf("list %s", rec.Body)
	}

	// Off: out of mediamtx.yml, the admin's entry stays; on again; deleted.
	fwd := base + "/" + itoa(f.ID)
	if rec := h.do("PATCH", fwd, map[string]any{"enabled": false}); rec.Code != http.StatusOK || strings.Contains(h.mtxFile(), secret) ||
		!strings.Contains(h.mtxFile(), "srt://198.51.100.1:9000") {
		t.Fatalf("off: %d %s\n%s", rec.Code, rec.Body, h.mtxFile())
	}
	if rec := h.do("PATCH", fwd, map[string]any{"enabled": true}); rec.Code != http.StatusOK || !strings.Contains(h.mtxFile(), secret) {
		t.Fatalf("on again: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("DELETE", fwd, nil); rec.Code != http.StatusNoContent || strings.Contains(h.mtxFile(), secret) {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("PATCH", fwd, map[string]any{"enabled": true}); rec.Code != http.StatusNotFound {
		t.Errorf("a deleted forward: %d", rec.Code)
	}

	// Deleting the stream takes its forwards with it.
	h.json(h.do("POST", base, map[string]any{"provider": "custom", "server": "rtmp://203.0.113.11/app", "key": secret}), &f)
	if rec := h.do("DELETE", "/api/v1/streams/"+itoa(st.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("stream delete: %d", rec.Code)
	}
	if left, _ := h.st.Forwards(ctx, st.ID); len(left) != 0 {
		t.Errorf("forwards left: %+v", left)
	}
}
