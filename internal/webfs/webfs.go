// Package webfs serves the embedded dashboard distribution behind the
// gateway's HTTP router (p9.embed-pipeline, ADR 006).
//
// Behavior:
//
//   - /assets/<hashed file>  → served with Cache-Control: immutable
//     (content-hashed names make revalidation unnecessary); a
//     build-time gzip sibling (*.gz) is served when the client accepts
//   - other exact files (favicon.ico, …) → served as-is
//   - anything else → index.html (the SPA router owns every dashboard
//     path) with Cache-Control: no-cache so deploys are picked up
//   - the mux registers this handler under "/" (all methods) LAST:
//     /healthz, /metrics, /api/*, /v1/* and the gemini paths are more
//     specific patterns and win per net/http.ServeMux precedence — one
//     port serves everything.
//
// Non-GET/HEAD requests that fall through to the dashboard answer 404
// (mux-style "page not found"), never 405: a method-less catch-all at
// "/" would otherwise turn every would-be-404 (parity A-12, corpus
// CC-14: POST /v1/messages/ must 404, not silently match anything) into
// a 405 from the mux. 405s still happen where they should — on real
// endpoints registered with method-specific patterns (parity A-11).
package webfs

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// placeholderMarker is a string unique to the committed placeholder
// index.html (fresh clones without a dashboard build). The real SPA
// build contains the React Router bootstrap instead.
const placeholderMarker = "This binary was built without the dashboard."

// zeroTime suppresses Last-Modified for embedded content: it has no
// meaningful modtime, and ServeContent skips the header for the zero
// value.
var zeroTime time.Time

// readSeeker is what http.ServeContent needs to serve ranges and HEAD.
type readSeeker interface {
	io.ReadSeeker
}

// Handler serves an embedded dashboard filesystem. Construct with New.
type Handler struct {
	dist        fs.FS
	placeholder bool
	index       []byte
}

// New builds a Handler over dist (web.Dist()). The returned handler is
// safe for concurrent use.
func New(dist fs.FS) *Handler {
	h := &Handler{dist: dist}
	h.index, _ = fs.ReadFile(dist, "index.html")
	h.placeholder = strings.Contains(string(h.index), placeholderMarker)
	return h
}

// IsPlaceholder reports whether the embedded distribution is the
// committed "dashboard not built" stub rather than a real SPA build.
func (h *Handler) IsPlaceholder() bool { return h.placeholder }

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// 404, not 405: this handler is the method-less "/" catch-all,
		// so anything reaching it is by definition unmatched. Answering
		// 405 here would rewrite every would-be-404 (parity A-12/CC-14:
		// POST /v1/messages/ must be a plain 404) into a method error.
		http.NotFound(w, r)
		return
	}

	// Normalize: path.Clean removes ".", ".." and duplicate separators;
	// prefixing "/" makes clean absolute so traversal cannot escape.
	name := path.Clean("/" + r.URL.Path)
	name = strings.TrimPrefix(name, "/")
	if name == "" || name == "." {
		name = "index.html"
	}

	// The SPA entry always goes through serveIndex: its cache policy is
	// no-cache (deploys change asset hashes), whether reached as "/",
	// "/index.html", or an unknown route fallback.
	if name == "index.html" {
		h.serveIndex(w, r)
		return
	}

	// Hashed build assets are immutable.
	if strings.HasPrefix(name, "assets/") {
		if h.serveFile(w, r, name, true) {
			return
		}
		// Unknown hashed asset: 404, never fall back (a stale hashed
		// name means the client cache is from an older deploy).
		http.NotFound(w, r)
		return
	}

	// Exact non-asset file (favicon.ico, robots.txt, …).
	if h.serveFile(w, r, name, false) {
		return
	}

	// SPA fallback: every other path belongs to the client router.
	h.serveIndex(w, r)
}

// serveFile writes one embedded file, honoring precompressed siblings
// and the immutable cache policy. Reports whether the file existed.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string, immutable bool) bool {
	if !validName(name) {
		return false
	}
	f, err := h.dist.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	if immutable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}

	// Build-time gzip sibling: serve it when the client accepts.
	if acceptsGzip(r) {
		if h.serveGzipSibling(w, r, name) {
			return true
		}
	}

	rs, ok := f.(readSeeker)
	if !ok {
		// embed.FS files always implement Seek; defensive branch for
		// alternate FS implementations.
		data, err := fs.ReadFile(h.dist, name)
		if err != nil {
			return false
		}
		w.Header().Set("Content-Type", contentType(name))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
		return true
	}
	http.ServeContent(w, r, name, zeroTime, rs)
	return true
}

// serveGzipSibling serves name+".gz" with Content-Encoding when it
// exists and the client accepts gzip. Reports whether it served.
func (h *Handler) serveGzipSibling(w http.ResponseWriter, r *http.Request, name string) bool {
	gz, err := h.dist.Open(name + ".gz")
	if err != nil {
		return false
	}
	defer gz.Close()
	gs, err := gz.Stat()
	if err != nil || gs.IsDir() {
		return false
	}
	rs, ok := gz.(readSeeker)
	if !ok {
		return false
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Add("Vary", "Accept-Encoding")
	// Pass the ORIGINAL name so ServeContent infers the right type.
	http.ServeContent(w, r, name, zeroTime, rs)
	return true
}

// validName rejects anything that could escape the embedded root. The
// mux already normalizes, but the handler is also mounted directly in
// tests; belt and suspenders for a file-serving surface.
func validName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// serveIndex writes the SPA entry with no-cache so new deploys (new
// asset hashes) are adopted on the next reload.
func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	if len(h.index) == 0 {
		http.Error(w, "dashboard not embedded", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Add("Vary", "Accept-Encoding")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(h.index)
	}
}

// acceptsGzip reports whether the request allows a gzip response.
func acceptsGzip(r *http.Request) bool {
	ae := r.Header.Get("Accept-Encoding")
	for _, part := range strings.Split(ae, ",") {
		enc, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if enc == "gzip" || enc == "*" {
			return true
		}
	}
	return false
}

// contentType maps the dashboard's file extensions to MIME types with
// explicit charsets where the browser cares.
func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
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
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
