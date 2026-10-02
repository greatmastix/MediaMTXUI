// Package proxy forwards the UI's MediaMTX Control API calls. The operations table (internal/mtxapi) is the
// allowlist: a request must match an operation's method and path template, carry only the query parameters the
// spec declares, and come from a user whose role reaches the operation's minimum. The upstream URL is rebuilt from
// the template and validated parameters, never copied from the request, and MediaMTX sees the sidecar's own
// principal, never the browser's cookies or headers.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/mtxapi"
	"mtxui/internal/pathname"
)

// Authorizer adds the sidecar's credentials to an upstream request.
type Authorizer interface {
	Authorize(*http.Request)
}

// maxResponse caps what the proxy relays: a bigger answer is an upstream fault, not something to buffer.
const maxResponse = 32 << 20

type route struct {
	op      mtxapi.Operation
	minRole auth.Role
	lits    []string // literal segments before the parameter
	param   string   // "" when the template has none
	rest    bool     // the parameter is a path name and takes the rest of the path, slashes included
	query   map[string]bool
}

// Proxy serves the API proxy. Mount it with the prefix stripped: it sees paths like /v3/paths/list.
type Proxy struct {
	routes    []route
	upstream  string
	principal Authorizer
	role      func(*http.Request) (auth.Role, bool)
	client    *http.Client
}

// New builds the proxy for the operations that UI roles may call. role reports the signed-in user's role.
func New(ops []mtxapi.Operation, upstream string, principal Authorizer, role func(*http.Request) (auth.Role, bool)) (*Proxy, error) {
	p := &Proxy{
		upstream: strings.TrimSuffix(upstream, "/"), principal: principal, role: role,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // never follow redirects away from MediaMTX
		}},
	}
	for _, op := range ops {
		var minRole auth.Role
		switch op.Access {
		case mtxapi.AccessViewer, mtxapi.AccessOperator, mtxapi.AccessAdmin:
			minRole = auth.Role(op.Access)
		default:
			continue // sidecar-only and unused operations are not reachable from browsers
		}
		rt, err := compile(op, minRole)
		if err != nil {
			return nil, err
		}
		p.routes = append(p.routes, rt)
	}
	return p, nil
}

func compile(op mtxapi.Operation, minRole auth.Role) (route, error) {
	rt := route{op: op, minRole: minRole, query: map[string]bool{}}
	segs := strings.Split(strings.TrimPrefix(op.Path, "/"), "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			if i != len(segs)-1 {
				return route{}, errors.New(op.ID + ": a parameter must be the last path segment")
			}
			rt.param = strings.Trim(s, "{}")
			rt.rest = rt.param == "name"
			break
		}
		rt.lits = append(rt.lits, s)
	}
	for _, q := range op.Query {
		rt.query[q] = true
	}
	return rt, nil
}

// match finds the operation for method and path and extracts its parameter. found reports whether the path exists
// under any method, to tell 405 from 404.
func (p *Proxy) match(method, path string) (rt *route, param string, found bool) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := range p.routes {
		r := &p.routes[i]
		n := len(r.lits)
		switch {
		case r.param == "" && len(segs) != n, // no parameter: exactly the literals
			r.rest && len(segs) < n+1,                    // a path name: at least one more segment
			r.param != "" && !r.rest && len(segs) != n+1: // an id: exactly one more segment
			continue
		}
		matched := true
		for j, lit := range r.lits {
			if segs[j] != lit {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		found = true
		if r.op.Method == method {
			if r.param != "" {
				param = strings.Join(segs[n:], "/")
			}
			return r, param, true
		}
	}
	return nil, "", found
}

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	errBadParam = errors.New("invalid parameter")
)

// validParam checks a path or query parameter by name: path names by MediaMTX's (stricter) rules, ids as UUIDs,
// page numbers as small integers, times as RFC 3339.
func validParam(name, v string) error {
	switch name {
	case "name", "path":
		return pathname.Valid(v)
	case "id":
		if !uuidPattern.MatchString(v) {
			return errBadParam
		}
	case "page", "itemsPerPage":
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 10_000 {
			return errBadParam
		}
	case "start":
		if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
			return errBadParam
		}
	default:
		return errBadParam
	}
	return nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	role, ok := p.role(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "sign in first")
		return
	}
	rt, param, found := p.match(r.Method, r.URL.Path)
	if rt == nil {
		if found {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "no such MediaMTX operation")
		return
	}
	if !role.AtLeast(rt.minRole) {
		writeError(w, http.StatusForbidden, "forbidden", "this needs the "+string(rt.minRole)+" role")
		return
	}

	upstream := p.upstream + "/" + strings.Join(rt.lits, "/")
	if rt.param != "" {
		if err := validParam(rt.param, param); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "invalid "+rt.param)
			return
		}
		escaped := strings.Split(param, "/")
		for i, s := range escaped {
			escaped[i] = url.PathEscape(s)
		}
		upstream += "/" + strings.Join(escaped, "/")
	}
	q := url.Values{}
	for k, vs := range r.URL.Query() {
		if !rt.query[k] {
			writeError(w, http.StatusBadRequest, "invalid", "unknown query parameter "+strconv.Quote(k))
			return
		}
		for _, v := range vs {
			if err := validParam(k, v); err != nil {
				writeError(w, http.StatusBadRequest, "invalid", "invalid "+k)
				return
			}
			q.Add(k, v)
		}
	}
	if len(q) > 0 {
		upstream += "?" + q.Encode()
	}
	if !auth.SafeMethod(r.Method) {
		audit.Set(r.Context(), "mediamtx."+rt.op.ID, param, map[string]any{"query": q.Encode()})
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, rt.op.Method, upstream, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "cannot build the request")
		return
	}
	req.Header.Set("Accept", "application/json")
	p.principal.Authorize(req)
	resp, err := p.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", "MediaMTX is unreachable")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		writeError(w, http.StatusBadGateway, "upstream", "MediaMTX sent an unreadable or oversized answer")
		return
	}
	if len(rt.op.Redact) > 0 && resp.StatusCode == http.StatusOK {
		if body, err = redact(body, rt.op.Redact); err != nil {
			writeError(w, http.StatusBadGateway, "upstream", "MediaMTX sent an unexpected answer")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// redact removes fields from a JSON object and from each object in its "items".
func redact(body []byte, fields []string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	for _, f := range fields {
		delete(doc, f)
	}
	if items, ok := doc["items"].([]any); ok {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				for _, f := range fields {
					delete(m, f)
				}
			}
		}
	}
	return json.Marshal(doc)
}

func writeError(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": kind, "message": msg})
}
