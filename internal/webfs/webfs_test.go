package webfs

// Unit tests for the embedded-dashboard serving layer (p9.embed-pipeline):
// SPA fallback, cache policy, gzip sibling negotiation, placeholder
// detection, path traversal, and method handling.
import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// testDist builds an in-memory dashboard distribution shaped like the
// real build output (index.html, hashed assets, favicon, .gz siblings).
func testDist(indexBody string) fstest.MapFS {
	return fstest.MapFS{
		"index.html":                  {Data: []byte(indexBody)},
		"favicon.ico":                 {Data: []byte("ICO")},
		"assets/entry-ABC123.js":      {Data: []byte("console.log(1)")},
		"assets/entry-ABC123.js.gz":   {Data: []byte("GZ-JS")},
		"assets/root-DEF456.css":      {Data: []byte("body{}")},
		"assets/inter-latin.woff2":    {Data: []byte("FONT")},
		"assets/inter-latin.woff2.gz": {Data: []byte("GZ-FONT")},
	}
}

const realIndex = `<!DOCTYPE html><html><head>
<script>window.__reactRouterContext = {"ssr":false,"isSpaMode":true};</script>
<link rel="modulepreload" href="/assets/entry-ABC123.js">
</head><body></body></html>`

func newServer(t *testing.T, indexBody string) *httptest.Server {
	t.Helper()
	h := New(testDist(indexBody))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestSPAFallbackForUnknownPaths(t *testing.T) {
	srv := newServer(t, realIndex)
	for _, p := range []string{"/", "/providers", "/keys/mint", "/usage?range=24h"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: status %d", p, resp.StatusCode)
		}
		if !strings.Contains(string(body), "__reactRouterContext") {
			t.Fatalf("GET %s: expected SPA index, got %.80q", p, body)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("GET %s: index cache-control %q, want no-cache", p, got)
		}
	}
}

func TestHashedAssetsImmutable(t *testing.T) {
	srv := newServer(t, realIndex)
	resp, err := http.Get(srv.URL + "/assets/entry-ABC123.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset cache-control %q", got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("asset content-type %q", got)
	}
}

func TestUnknownHashedAssetIs404NotFallback(t *testing.T) {
	// A stale hashed name must NOT return the SPA shell with 200 —
	// clients would cache HTML under an immutable asset URL.
	srv := newServer(t, realIndex)
	resp, err := http.Get(srv.URL + "/assets/entry-NEWHASH.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("stale asset status %d, want 404", resp.StatusCode)
	}
}

func TestGzipSiblingServedWhenAccepted(t *testing.T) {
	srv := newServer(t, realIndex)

	req, _ := http.NewRequest("GET", srv.URL+"/assets/entry-ABC123.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("expected Content-Encoding: gzip")
	}
	if string(body) != "GZ-JS" {
		t.Fatalf("gzip sibling not served, got %.20q", body)
	}
	if resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("missing Vary: Accept-Encoding")
	}

	// Without Accept-Encoding: raw file.
	req2, _ := http.NewRequest("GET", srv.URL+"/assets/entry-ABC123.js", nil)
	req2.Header.Set("Accept-Encoding", "identity")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.Header.Get("Content-Encoding") != "" {
		t.Fatal("identity request must not get gzip")
	}
}

func TestPlaceholderDetection(t *testing.T) {
	real := New(testDist(realIndex))
	if real.IsPlaceholder() {
		t.Fatal("real build detected as placeholder")
	}
	stub := New(testDist("<html><body>This binary was built without the dashboard.</body></html>"))
	if !stub.IsPlaceholder() {
		t.Fatal("placeholder not detected")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	h := New(testDist(realIndex))
	for _, p := range []string{"/../embed.go", "/assets/../../embed.go", "/..%2fembed.go"} {
		req := httptest.NewRequest("GET", p, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Body)
		if strings.Contains(string(body), "go:embed") {
			t.Fatalf("traversal %s leaked file content", p)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := New(testDist(realIndex))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/providers", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d, want 405", rec.Code)
	}
}

func TestFaviconServed(t *testing.T) {
	srv := newServer(t, realIndex)
	resp, err := http.Get(srv.URL + "/favicon.ico")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("favicon status %d", resp.StatusCode)
	}
}
