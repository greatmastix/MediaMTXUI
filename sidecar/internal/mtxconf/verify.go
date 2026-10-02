package mtxconf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"mtxui/internal/yamledit"
)

// Applied says what MediaMTX made of a written config.
type Applied struct {
	State   string   `json:"state"`   // verified, mismatch, skipped, restored
	Message string   `json:"message"` // for people
	Keys    []string `json:"keys,omitempty"`
}

// Apply states.
const (
	AppliedVerified = "verified" // MediaMTX runs with the new settings
	AppliedMismatch = "mismatch" // MediaMTX answers, but some settings read back differently
	AppliedSkipped  = "skipped"  // MediaMTX was not answering before the write, so there was nothing to compare
	AppliedRestored = "restored" // MediaMTX stopped answering after the write; the previous file is back
)

// Getter reads from MediaMTX's Control API with the sidecar's credentials.
type Getter interface {
	Get(ctx context.Context, path string) (status int, body []byte, err error)
}

// Verifier checks, after a write, that MediaMTX reloaded the file and runs with what it says: it reads the effective
// settings back from the Control API until every changed setting matches, or the time is up.
type Verifier struct {
	API      Getter
	Timeout  time.Duration // how long to wait for the reload; 10 s when zero
	Interval time.Duration // between reads; 250 ms when zero
	Down     time.Duration // no answer for this long means MediaMTX stopped; 4 s when zero
}

// Answering reports whether MediaMTX's API answers now.
func (v *Verifier) Answering(ctx context.Context) bool {
	code, _, err := v.API.Get(ctx, "/v3/info")
	return err == nil && code == http.StatusOK
}

// Verify compares MediaMTX's effective config with the changes from before to after. It never restores anything
// itself: a result with State "" and a non-nil error means MediaMTX stopped answering, and the caller restores.
func (v *Verifier) Verify(ctx context.Context, before, after []byte) (Applied, error) {
	timeout, interval := v.Timeout, v.Interval
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if interval == 0 {
		interval = 250 * time.Millisecond
	}
	checks, err := changedSettings(before, after)
	if err != nil {
		return Applied{}, err
	}
	down := v.Down
	if down == 0 {
		down = 4 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var off []string
	var answered bool
	var silentSince time.Time
poll:
	for {
		off, answered = v.compare(ctx, checks)
		if answered && len(off) == 0 {
			return Applied{State: AppliedVerified, Message: "MediaMTX applied the change."}, nil
		}
		switch {
		case answered:
			silentSince = time.Time{}
		case silentSince.IsZero():
			silentSince = time.Now()
		case time.Since(silentSince) > down:
			break poll // a reload takes milliseconds; this long without an answer, MediaMTX is gone
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return Applied{}, ctx.Err()
		case <-time.After(interval):
		}
	}
	if !answered {
		return Applied{}, fmt.Errorf("MediaMTX stopped answering after the change")
	}
	return Applied{
		State: AppliedMismatch, Keys: off,
		Message: "MediaMTX answers, but these settings read back differently than written: " + strings.Join(off, ", ") +
			". The change is saved; check MediaMTX's log.",
	}, nil
}

// check is one setting to read back: where MediaMTX reports it and what it should be.
type check struct {
	label  string // e.g. "readTimeout", "paths.cam1.source", "paths.cam1" (for existence)
	api    string // the API path of the object that holds it
	key    string // "" to check only that the object exists (or, with gone, that it does not)
	value  any
	gone   bool
	scalar bool // objects and lists of objects are not compared: MediaMTX fills in defaults
}

// compare reads every object once and returns the checks that do not match; answered is false when MediaMTX did
// not answer this round (it may have answered with the old config before, then exited on the reload).
func (v *Verifier) compare(ctx context.Context, checks []check) (off []string, answered bool) {
	if len(checks) == 0 { // a change of comments or layout only: MediaMTX just has to be there
		return nil, v.Answering(ctx)
	}
	objects := map[string]map[string]any{}
	status := map[string]int{}
	for _, c := range checks {
		if _, done := status[c.api]; !done {
			code, body, err := v.API.Get(ctx, c.api)
			if err != nil || code >= 500 {
				return []string{"(no answer)"}, false
			}
			status[c.api] = code
			var obj map[string]any
			if code == http.StatusOK && json.Unmarshal(body, &obj) == nil {
				objects[c.api] = obj
			}
		}
		switch {
		case c.gone:
			if status[c.api] != http.StatusNotFound && status[c.api] != http.StatusBadRequest {
				off = append(off, c.label)
			}
		case status[c.api] != http.StatusOK:
			off = append(off, c.label)
		case c.key == "" || !c.scalar:
		case !sameSetting(objects[c.api][c.key], c.value):
			off = append(off, c.label)
		}
	}
	return off, true
}

// sameSetting compares a value as written with MediaMTX's report of it: durations by length ("1m" and "1m0s"),
// empty and null alike, numbers by value.
func sameSetting(effective, written any) bool {
	if written == nil || written == "" {
		return effective == nil || effective == "" || reflect.DeepEqual(effective, []any{})
	}
	if ws, ok := written.(string); ok {
		if es, ok := effective.(string); ok {
			wd, werr := time.ParseDuration(ws)
			ed, eerr := time.ParseDuration(es)
			if werr == nil && eerr == nil {
				return wd == ed
			}
		}
	}
	a, _ := json.Marshal(effective)
	b, _ := json.Marshal(written)
	return string(a) == string(b)
}

// changedSettings lists what to read back for the difference between two configs: changed global settings and path
// defaults (their new values; removed ones fall back to defaults the sidecar does not know, so they are not
// compared), and for each path that was added, changed or removed, its existence and changed settings.
func changedSettings(before, after []byte) ([]check, error) {
	b, err := yamledit.Decode(before)
	if err != nil {
		return nil, err
	}
	a, err := yamledit.Decode(after)
	if err != nil {
		return nil, err
	}
	var out []check
	section := func(api, prefix string, bm, am map[string]any) {
		for _, k := range sortedKeys(am) {
			if !reflect.DeepEqual(bm[k], am[k]) {
				out = append(out, check{label: prefix + k, api: api, key: k, value: am[k], scalar: isScalar(am[k])})
			}
		}
	}
	global := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			if k != "paths" && k != "pathDefaults" {
				out[k] = v
			}
		}
		return out
	}
	section("/v3/config/global/get", "", global(b), global(a))
	section("/v3/config/path-defaults/get", "pathDefaults.", asMap(b["pathDefaults"]), asMap(a["pathDefaults"]))
	bp, ap := asMap(b["paths"]), asMap(a["paths"])
	for _, name := range sortedKeys(ap) {
		if reflect.DeepEqual(bp[name], ap[name]) {
			continue
		}
		api := "/v3/config/paths/get/" + escapePath(name)
		out = append(out, check{label: "paths." + name, api: api})
		section(api, "paths."+name+".", asMap(bp[name]), asMap(ap[name]))
	}
	for _, name := range sortedKeys(bp) {
		if _, ok := ap[name]; !ok {
			out = append(out, check{label: "paths." + name + " (removed)", api: "/v3/config/paths/get/" + escapePath(name), gone: true})
		}
	}
	return out, nil
}

func escapePath(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func isScalar(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return false
	case []any:
		for _, e := range x {
			if !isScalar(e) {
				return false
			}
		}
	}
	return true
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Authorizer adds the sidecar's credentials to a request.
type Authorizer interface {
	Authorize(*http.Request)
}

// APIClient is a Getter for MediaMTX's Control API.
type APIClient struct {
	Base      string
	Principal Authorizer
	Client    *http.Client
}

// Get reads path from the API.
func (c *APIClient) Get(ctx context.Context, path string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.Base, "/")+path, nil)
	if err != nil {
		return 0, nil, err
	}
	c.Principal.Authorize(req)
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, body, err
}

// Post calls an operation without a body (the kick operations) and returns MediaMTX's status code.
func (c *APIClient) Post(ctx context.Context, path string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.Base, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	c.Principal.Authorize(req)
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// Delete calls a DELETE operation (recordings/segments/delete) and returns MediaMTX's status code.
func (c *APIClient) Delete(ctx context.Context, path string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimSuffix(c.Base, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	c.Principal.Authorize(req)
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// Open sends GET path and hands the response to the caller, who reads and closes it: no size or time limit beyond
// ctx (recording exports stream for minutes).
func (c *APIClient) Open(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.Base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	c.Principal.Authorize(req)
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}
