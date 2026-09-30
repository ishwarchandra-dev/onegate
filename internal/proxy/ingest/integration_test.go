package ingest

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/server"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// TestFullStackWithRealVerifier assembles the production inbound path:
// server chain -> ingest -> auth.Verifier (real storage + peppered
// hash) -> proxy. It proves credential hashing, lookup, status and
// expiry enforcement, and envelope rendering work end to end — the
// same wiring cmd/onegate performs.
func TestFullStackWithRealVerifier(t *testing.T) {
	store, err := storage.Open(t.TempDir() + "/it.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	pepper := []byte("integration-pepper")
	verifier := auth.NewVerifier(store, pepper)

	raw, prefix := auth.GenerateVirtualKey()
	if _, err := store.VirtualKeys().Create(domain.VirtualKey{
		Name:    "integration",
		Prefix:  prefix,
		KeyHash: auth.HashVirtualKey(pepper, raw),
	}); err != nil {
		t.Fatal(err)
	}

	proxy := &fakeProxy{}
	r := server.New(server.Options{Logger: nil})
	Register(r.Mux(), Deps{Auth: verifier, Proxy: proxy})
	ts := httptest.NewServer(r.Handler())
	defer ts.Close()

	// Valid key: the request reaches the proxy with the resolved key.
	resp := post(t, ts.URL+"/v1/chat/completions", raw, "",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("valid key must pass: %d", resp.StatusCode)
	}
	resp.Body.Close()
	calls := proxy.recorded()
	if len(calls) != 1 {
		t.Fatalf("proxy calls: %d", len(calls))
	}
	if calls[0].Call.Key.Prefix != prefix || calls[0].Call.Key.Status != domain.KeyActive {
		t.Fatalf("resolved key not propagated: %+v", calls[0].Call.Key)
	}

	// Wrong pepper world (unknown key): 401.
	resp = post(t, ts.URL+"/v1/chat/completions", "ogk-forged", "", `{}`)
	if resp.StatusCode != 401 {
		t.Fatalf("forged key: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Revoked: 403.
	revoked, _ := auth.GenerateVirtualKey()
	k, err := store.VirtualKeys().Create(domain.VirtualKey{
		Name:    "revoked",
		Prefix:  auth.KeyPrefix(revoked),
		KeyHash: auth.HashVirtualKey(pepper, revoked),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.VirtualKeys().UpdateStatus(k.ID, domain.KeyRevoked); err != nil {
		t.Fatal(err)
	}
	resp = post(t, ts.URL+"/v1/messages", revoked, "", `{}`)
	if resp.StatusCode != 403 {
		t.Fatalf("revoked: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(proxy.recorded()) != 1 {
		t.Fatal("revoked request must not reach the proxy")
	}

	// Foreign-shape key (Bearer of an OpenAI-style key): 401, not 500.
	resp = post(t, ts.URL+"/v1/chat/completions", "sk-legacy", "", `{}`)
	if resp.StatusCode != 401 {
		t.Fatalf("foreign key: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestEnvelopesCarryRequestID ties the server middleware to error
// responses: even a 401 carries the request ID header, so support
// tickets map to log lines.
func TestEnvelopesCarryRequestID(t *testing.T) {
	srv, _ := newStack(t, fakeAuth{err: domain.ErrUnknownKey}, &fakeProxy{}, 0)
	resp := post(t, srv.URL+"/v1/chat/completions", "ogk-x", "", `{}`)
	defer resp.Body.Close()
	if id := resp.Header.Get("X-Request-Id"); id == "" {
		t.Fatal("401 responses must carry X-Request-Id")
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("error content type: %q", resp.Header.Get("Content-Type"))
	}
}
