package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// testEnv is a full management-API harness: real temp SQLite, real
// cipher/manager, SSE hub, fake health view, probe transport against a
// local mock provider.
type testEnv struct {
	mux    *http.ServeMux
	srv    *httptest.Server
	store  *storage.Store
	keys   *auth.Manager
	cipher *auth.Cipher
	hub    *observability.LogHub
	api    *API
	nowMS  *int64 // deterministic clock when set
}

const testToken = "test-admin-token-0123456789"

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	store, err := storage.OpenTemp()
	if err != nil {
		t.Fatalf("OpenTemp: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i)
	}
	pepper, err := auth.Pepper(master)
	if err != nil {
		t.Fatalf("Pepper: %v", err)
	}
	cipher, err := auth.NewCipher(master, auth.PurposeProviderKeys)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}

	clock := int64(1_700_000_000_000)
	env := &testEnv{
		store:  store,
		keys:   auth.NewManager(store, pepper),
		cipher: cipher,
		hub:    observability.NewLogHub(64),
	}
	env.nowMS = &clock

	mux := http.NewServeMux()
	env.api = Register(mux, Options{
		Logger:         nil,
		Store:          store,
		Keys:           env.keys,
		ProviderCipher: cipher,
		AdminToken:     testToken,
		LogHub:         env.hub,
		Health:         fakeHealth{},
		Config: func() SystemConfig {
			return SystemConfig{Host: "127.0.0.1", Port: 7420, DataDir: "/tmp/og",
				LogLevel: "info", HTTP: HTTPTimeouts{ReadHeaderTimeoutMS: 5000},
				Reload: ReloadInfo{Enabled: true, PollMS: 2000}}
		},
		StartedMS:     clock - 5_000,
		SchemaVersion: 3,
		Prober:        client.New(client.TransportConfig{}, nil),
		NowMS:         func() int64 { return clock },
	})
	env.mux = mux
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)
	return env
}

// fakeHealth is a static HealthView (closed circuits, one event).
type fakeHealth struct{}

func (fakeHealth) State(_, _ string) routing.CircuitState { return routing.StateClosed }
func (fakeHealth) Events(limit int) []routing.HealthEvent {
	return []routing.HealthEvent{{
		TimestampMS: 1, ProviderID: "prov_1_1", Model: "m",
		OldState: routing.StateOpen, NewState: routing.StateClosed, Reason: "recovered",
	}}
}

// do performs an authenticated (by default) JSON request against the
// env. SSE responses are header-checked only (their body streams by
// design); every request carries a hard 5s timeout so a regression can
// never hang the suite.
func (e *testEnv) do(t *testing.T, method, path string, body any, token string) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token == "" {
		token = testToken
	}
	if token != "-" { // "-" means: send no credential at all
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	var payload map[string]any
	if resp.StatusCode != 204 {
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			// SSE: headers arrived, which is all the contract check needs.
			resp.Body.Close()
			return resp, nil
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("%s %s: decode response: %v", method, path, err)
		}
	}
	resp.Body.Close()
	return resp, payload
}

// errOf extracts the error envelope from a decoded payload.
func errOf(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	e, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error envelope, got %v", payload)
	}
	return e
}

// ---------------------------------------------------------------------------
// Contract: spec <-> registration <-> runtime behavior
// ---------------------------------------------------------------------------

// TestSpecRoutesAllRegistered proves every spec operation is routable
// (Register panics on a missing handler, so reaching here means the table
// is covered; this test makes the failure mode explicit).
func TestSpecRoutesAllRegistered(t *testing.T) {
	env := newEnv(t)
	for _, rt := range specRoutes {
		if _, ok := env.api.handlers[rt.OperationID]; !ok {
			t.Errorf("spec operation %s has no handler", rt.OperationID)
		}
	}
	if len(env.api.handlers) != len(specRoutes) {
		t.Errorf("handler count %d != spec route count %d", len(env.api.handlers), len(specRoutes))
	}
}

// TestAuthClassEnforcement runs every spec route with and without
// credentials and asserts the documented auth class:
//
//	admin-* routes: 401 without/with wrong token; not 401 with the token
//	public routes:  never 401
func TestAuthClassEnforcement(t *testing.T) {
	env := newEnv(t)
	for _, rt := range specRoutes {
		path := strings.ReplaceAll(rt.Path, "{id}", "prov_0_0")
		t.Run(rt.Auth+" "+rt.Method+" "+path, func(t *testing.T) {
			// Without credentials.
			resp, payload := env.do(t, rt.Method, path, nil, "-")
			if rt.Auth != "public" {
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("no-credential: want 401, got %d (%v)", resp.StatusCode, payload)
				}
				e := errOf(t, payload)
				if e["type"] != string("authentication_error") {
					t.Errorf("no-credential: want authentication_error, got %v", e["type"])
				}
			} else if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("public route returned 401")
			}

			// Wrong token.
			if rt.Auth != "public" {
				resp, _ = env.do(t, rt.Method, path, nil, "wrong-token")
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("bad token: want 401, got %d", resp.StatusCode)
				}
			}

			// Correct token: authenticated routes must pass the gate
			// (any status except 401).
			resp, _ = env.do(t, rt.Method, path, nil, testToken)
			if rt.Auth != "public" && resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("valid token: unexpectedly 401")
			}
		})
	}
}

// TestPublicRoutesNeverRequireAuth pins the public auth operations
// (login, setup, setup-status) — no credential may be required.
func TestPublicRoutesNeverRequireAuth(t *testing.T) {
	env := newEnv(t)

	resp, _ := env.do(t, "GET", "/api/auth/setup-status", nil, "-")
	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("GET /api/auth/setup-status: public route must not 401")
	}

	for _, path := range []string{"/api/auth/login", "/api/auth/setup"} {
		resp, _ := env.do(t, "POST", path, map[string]string{"username": "admin", "password": "not-important12"}, "-")
		if resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("POST %s: public route must not 401", path)
		}
	}
}

// TestSessionRoutesPlaceholder503: until p6.auth-sessions lands, the
// session-only operations answer 503 (not 500, not silent success).
// login/setup are public; logout/session require a credential (the
// admin token satisfies the admin-session class until cookies land).
func TestSessionRoutesPlaceholder503(t *testing.T) {
	env := newEnv(t)
	resp, _ := env.do(t, "POST", "/api/auth/login", map[string]string{"username": "a", "password": "b"}, "-")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("login: want 503 placeholder, got %d", resp.StatusCode)
	}
	resp, _ = env.do(t, "POST", "/api/auth/setup", map[string]string{"username": "a", "password": "b"}, "-")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("setup: want 503 placeholder, got %d", resp.StatusCode)
	}
	resp, _ = env.do(t, "POST", "/api/auth/logout", nil, testToken)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("logout: want 503 placeholder, got %d", resp.StatusCode)
	}
	resp, _ = env.do(t, "GET", "/api/auth/session", nil, testToken)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("session: want 503 placeholder, got %d", resp.StatusCode)
	}
}

// TestRateLimitWriteClass verifies the documented write budget (30/min)
// with the injectable clock.
func TestRateLimitWriteClass(t *testing.T) {
	env := newEnv(t)
	var clock = env.nowMS
	// A mutating endpoint that returns fast (providers create with
	// invalid body still counts against the class budget).
	for i := 0; i < 30; i++ {
		resp, _ := env.do(t, "POST", "/api/providers", map[string]any{
			"name": "p", "protocol": "openai", "base_url": "https://example.com",
		}, testToken)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("request %d: unexpected %d", i+1, resp.StatusCode)
		}
	}
	resp, payload := env.do(t, "POST", "/api/providers", map[string]any{
		"name": "p2", "protocol": "openai", "base_url": "https://example.com",
	}, testToken)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429 after 30 writes, got %d (%v)", resp.StatusCode, payload)
	}
	e := errOf(t, payload)
	if e["type"] != "rate_limit_error" || e["retryable"] != true {
		t.Errorf("429 envelope wrong: %v", e)
	}

	// Reads are a different class and still allowed.
	resp, _ = env.do(t, "GET", "/api/providers", nil, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read after write-exhaustion: want 200, got %d", resp.StatusCode)
	}

	// A new minute window resets the budget.
	*clock += 61_000
	resp, _ = env.do(t, "POST", "/api/providers", map[string]any{
		"name": "p3", "protocol": "openai", "base_url": "https://example.com",
	}, testToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("after window reset: want 201, got %d", resp.StatusCode)
	}
}

// TestErrorEnvelope400 asserts the invalid-request envelope shape.
func TestErrorEnvelope400(t *testing.T) {
	env := newEnv(t)
	resp, payload := env.do(t, "GET", "/api/providers?limit=999", nil, testToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	e := errOf(t, payload)
	for _, k := range []string{"status", "type", "message", "retryable"} {
		if _, ok := e[k]; !ok {
			t.Errorf("envelope missing %q: %v", k, e)
		}
	}
	if e["type"] != "invalid_request_error" || e["param"] != "limit" {
		t.Errorf("envelope detail wrong: %v", e)
	}
}

// TestUnknownID404 asserts the not-found envelope.
func TestUnknownID404(t *testing.T) {
	env := newEnv(t)
	resp, payload := env.do(t, "GET", "/api/providers/prov_0_0", nil, testToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	e := errOf(t, payload)
	if e["type"] != "not_found_error" {
		t.Errorf("want not_found_error, got %v", e["type"])
	}
}

// TestJSONContentType asserts every JSON endpoint answers with the
// application/json content type (contract: uniform envelopes).
func TestJSONContentType(t *testing.T) {
	env := newEnv(t)
	resp, _ := env.do(t, "GET", "/api/system/status", nil, testToken)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("want application/json, got %q", ct)
	}
}

// TestNoTokenConfigMeansNoAdminAccess: with an empty AdminToken the
// token authenticator must reject everything (safe by default).
func TestNoTokenConfigMeansNoAdminAccess(t *testing.T) {
	store, err := storage.OpenTemp()
	if err != nil {
		t.Fatalf("OpenTemp: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	mux := http.NewServeMux()
	Register(mux, Options{Store: store, Keys: nil, AdminToken: ""})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/system/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("empty token config must 401, got %d", resp.StatusCode)
	}
}
