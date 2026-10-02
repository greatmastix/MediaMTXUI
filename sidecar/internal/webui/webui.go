// Package webui serves the single-page app that the image build copies into dist/ before compiling.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// dist holds only .gitkeep in a source checkout; the Dockerfile copies web/dist here before `go build`.
//
//go:embed all:dist
var embedded embed.FS

// Handler serves the embedded SPA (see New).
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // unreachable: dist is embedded at compile time
	}
	h, err := New(sub)
	if err != nil {
		panic(err) // unreachable: reading an embed.FS cannot fail
	}
	return h
}

type file struct {
	body  []byte
	etag  string
	ctype string
	cache string
}

type handler struct {
	files map[string]file // keyed by URL path, e.g. "/assets/index-3f2a.js"
	index *file
}

// New builds a handler for the SPA in fsys. Files are served from memory with strong ETags. Vite's
// content-hashed files under assets/ are cached for a year; everything else must revalidate. A GET for a
// path without a file extension falls back to index.html so client-side routes survive a reload, while
// a missing file with an extension is a 404, so a stale asset URL never gets HTML back. Without an
// index.html (a binary built without the UI) every request gets 503.
func New(fsys fs.FS) (http.Handler, error) {
	h := &handler{files: map[string]file{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return err
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		f := file{body: body, etag: `"` + hex.EncodeToString(sum[:16]) + `"`, ctype: contentType(p), cache: "no-cache"}
		if strings.HasPrefix(p, "assets/") {
			f.cache = "public, max-age=31536000, immutable"
		}
		h.files["/"+p] = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	if f, ok := h.files["/index.html"]; ok {
		h.index = &f
	}
	return h, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.index == nil {
		http.Error(w, "The web UI is not built into this binary. Build the image with ./dev build.", http.StatusServiceUnavailable)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	f, ok := h.files[p]
	if !ok {
		if path.Ext(p) != "" {
			http.NotFound(w, r)
			return
		}
		f = *h.index
	}
	w.Header().Set("Content-Type", f.ctype)
	w.Header().Set("Cache-Control", f.cache)
	w.Header().Set("ETag", f.etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.body))
}

func contentType(p string) string {
	switch ext := path.Ext(p); ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".webmanifest":
		return "application/manifest+json"
	case ".woff2":
		return "font/woff2"
	default:
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
		return "application/octet-stream"
	}
}
