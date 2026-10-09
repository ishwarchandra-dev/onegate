package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// testPasswordHasher is a cheap PBKDF2 (fast tests; production uses the
// default 600k iterations).
func testPasswordHasher(p string) (string, error) { return auth.HashPassword(p, 128) }

// newSessionEnv builds an env with session auth wired (cheap hasher,
// injectable clock shared with the session store and rate limiter).
func newSessionEnv(t *testing.T) (*testEnv, *int64) {
	t.Helper()
	env := newEnv(t)
	clock := int64(1_700_000_000_000)
	env.nowMS = &clock

	// Re-register the API with sessions on the same mux patterns.
	mux := http.NewServeMux()
	env.api = Register(mux, Options{
		Store:              env.store,
		Keys:               env.keys,
		ProviderCipher:     env.cipher,
		AdminToken:         testToken,
		Sessions:           NewSessionManager(SessionTTLMS, func() int64 { return clock }),
		PasswordHasher:     testPasswordHasher,
		PasswordIterations: 128,
		LogHub:             env.hub,
		Health:             fakeHealth{},
		Config: func() SystemConfig {
			return SystemConfig{Host: "h", Port: 1}
		},
		StartedMS:     clock,
		SchemaVersion: 4,
		NowMS:         func() int64 { return clock },
	})
	env.srv.Close()
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)
	return env, &clock
}

// doCookie performs a request with explicit cookie + CSRF headers (the
// browser-equivalent paths the session tests need).
func (e *testEnv) doCookie(t *testing.T, method, path, cookie, csrf string, body any) (*http.Response, map[string]any, string) {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", sessionCookieName+"="+cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if resp.StatusCode != 204 {
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			return resp, nil, ""
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp, payload, setCookieValue(resp)
}

// setCookieValue extracts the Set-Cookie header value (session id).
func setCookieValue(resp *http.Response) string {
	for _, c := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(c, sessionCookieName+"=") {
			v := strings.TrimPrefix(c, sessionCookieName+"=")
			if i := strings.Index(v, ";"); i >= 0 {
				return v[:i]
			}
			return v
		}
	}
	return ""
}

// TestFirstRunSetupFlow: fresh install reports setup_required, setup
// creates the admin (201 + cookie + CSRF), setup then refuses (403).
func TestFirstRunSetupFlow(t *testing.T) {
	env, _ := newSessionEnv(t)

	// Fresh: setup required.
	_, payload, _ := env.doCookie(t, "GET", "/api/auth/setup-status", "", "", nil)
	if payload["setup_required"] != true {
		t.Fatalf("fresh install must require setup, got %v", payload)
	}

	// Setup with a weak password -> 400.
	resp, payload, _ := env.doCookie(t, "POST", "/api/auth/setup", "", "",
		credentials{Username: "admin", Password: "short"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("weak password: want 400, got %d (%v)", resp.StatusCode, payload)
	}

	// Proper setup -> 201, session cookie, CSRF token.
	resp, payload, cookie := env.doCookie(t, "POST", "/api/auth/setup", "", "",
		credentials{Username: "admin", Password: "correct-horse-battery"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: want 201, got %d (%v)", resp.StatusCode, payload)
	}
	if cookie == "" {
		t.Fatal("setup must set the session cookie")
	}
	csrf, _ := payload["csrf_token"].(string)
	if csrf == "" {
		t.Fatal("setup must return a CSRF token")
	}

	// Setup is done now.
	_, payload, _ = env.doCookie(t, "GET", "/api/auth/setup-status", "", "", nil)
	if payload["setup_required"] != false {
		t.Fatalf("after setup: setup_required must be false, got %v", payload)
	}

	// Second setup attempt -> 403.
	resp, _, _ = env.doCookie(t, "POST", "/api/auth/setup", "", "",
		credentials{Username: "hacker", Password: "correct-horse-battery"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("second setup: want 403, got %d", resp.StatusCode)
	}
}

// TestLoginFlowAndCookieHardening: login sets a hardened cookie; bad
// credentials answer a uniform 401.
func TestLoginFlowAndCookieHardening(t *testing.T) {
	env, _ := newSessionEnv(t)
	_, _, cookie := env.doCookie(t, "POST", "/api/auth/setup", "", "",
		credentials{Username: "admin", Password: "correct-horse-battery"})
	if cookie == "" {
		t.Fatal("setup failed")
	}

	// Logout the setup session to test login afresh.
	env.doCookie(t, "POST", "/api/auth/logout", cookie, "x", nil)

	// Wrong password and unknown user -> identical 401 shape.
	for _, creds := range []credentials{
		{Username: "admin", Password: "wrong-password-123"},
		{Username: "ghost", Password: "wrong-password-123"},
	} {
		resp, payload, _ := env.doCookie(t, "POST", "/api/auth/login", "", "", creds)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("login %q: want 401, got %d", creds.Username, resp.StatusCode)
		}
		e := errOf(t, payload)
		if e["message"] != "invalid username or password" {
			t.Fatalf("login rejection must be uniform, got %v", e["message"])
		}
	}

	// Correct login -> 200 + hardened cookie.
	resp, payload, cookie2 := env.doCookie(t, "POST", "/api/auth/login", "", "",
		credentials{Username: "admin", Password: "correct-horse-battery"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d (%v)", resp.StatusCode, payload)
	}
	if cookie2 == "" || cookie2 == cookie {
		t.Fatal("login must mint a fresh session id (fixation defense)")
	}

	// Cookie hardening: HttpOnly + SameSite=Lax + Path=/.
	var raw string
	for _, c := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(c, sessionCookieName+"=") {
			raw = c
		}
	}
	for _, want := range []string{"HttpOnly", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(raw, want) {
			t.Errorf("cookie missing %s: %s", want, raw)
		}
	}

	// GET /api/auth/session with the cookie works and echoes the CSRF.
	_, payload, _ = env.doCookie(t, "GET", "/api/auth/session", cookie2, "", nil)
	user, _ := payload["user"].(map[string]any)
	if user["username"] != "admin" {
		t.Fatalf("session user wrong: %v", payload)
	}
	if payload["csrf_token"] == "" {
		t.Fatal("session must expose the CSRF token")
	}
	if payload["expires_at_ms"] == float64(0) {
		t.Fatal("session must carry an expiry")
	}
}

// TestCSRFBlockedForSessionMutations: a cookie-authenticated mutation
// without (or with a wrong) CSRF token is rejected; the correct token
// passes; admin-token requests are exempt.
func TestCSRFBlockedForSessionMutations(t *testing.T) {
	env, _ := newSessionEnv(t)
	_, payload, cookie := env.doCookie(t, "POST", "/api/auth/setup", "", "",
		credentials{Username: "admin", Password: "correct-horse-battery"})
	csrf, _ := payload["csrf_token"].(string)

	// Session-authenticated GET (no CSRF needed).
	resp, _, _ := env.doCookie(t, "GET", "/api/providers", cookie, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session GET should work, got %d", resp.StatusCode)
	}

	// Mutation without CSRF -> 403 csrf_token_invalid.
	resp, payload2, _ := env.doCookie(t, "POST", "/api/providers", cookie, "",
		map[string]any{"name": "X", "protocol": "openai", "base_url": "https://x.com"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mutation without CSRF: want 403, got %d", resp.StatusCode)
	}
	e := errOf(t, payload2)
	if e["code"] != "csrf_token_invalid" {
		t.Fatalf("want csrf_token_invalid, got %v", e["code"])
	}

	// Wrong CSRF -> 403.
	resp, _, _ = env.doCookie(t, "POST", "/api/providers", cookie, "deadbeefdeadbeef",
		map[string]any{"name": "X", "protocol": "openai", "base_url": "https://x.com"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong CSRF: want 403, got %d", resp.StatusCode)
	}

	// Correct CSRF -> passes.
	resp, _, _ = env.doCookie(t, "POST", "/api/providers", cookie, csrf,
		map[string]any{"name": "X", "protocol": "openai", "base_url": "https://x.com"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("correct CSRF: want 201, got %d", resp.StatusCode)
	}

	// Admin-token requests are exempt from CSRF by design.
	resp, _ = env.do(t, "POST", "/api/providers", map[string]any{
		"name": "Y", "protocol": "openai", "base_url": "https://y.com",
	}, testToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin token mutation must skip CSRF, got %d", resp.StatusCode)
	}
}

// TestSessionExpiry: after the TTL, the session is gone (401).
func TestSessionExpiry(t *testing.T) {
	env, clock := newSessionEnv(t)
	_, _, cookie := env.doCookie(t, "POST", "/api/auth/setup", "", "",
		credentials{Username: "admin", Password: "correct-horse-battery"})

	// Before expiry: works.
	resp, _, _ := env.doCookie(t, "GET", "/api/auth/session", cookie, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pre-expiry session: %d", resp.StatusCode)
	}

	// Advance past the TTL.
	*clock += SessionTTLMS + 1

	resp, _, _ = env.doCookie(t, "GET", "/api/auth/session", cookie, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session must 401, got %d", resp.StatusCode)
	}
	// And the API surface rejects it too.
	resp, _, _ = env.doCookie(t, "GET", "/api/providers", cookie, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session on admin route: want 401, got %d", resp.StatusCode)
	}
}

// TestLogoutInvalidatesSession: logout destroys the server-side session
// and clears the cookie.
func TestLogoutInvalidatesSession(t *testing.T) {
	env, _ := newSessionEnv(t)
	_, payload, cookie := env.doCookie(t, "POST", "/api/auth/setup", "", "",
		credentials{Username: "admin", Password: "correct-horse-battery"})
	csrf, _ := payload["csrf_token"].(string)

	resp, _, setRaw := env.doCookie(t, "POST", "/api/auth/logout", cookie, csrf, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: want 204, got %d", resp.StatusCode)
	}
	if setRaw != "" {
		t.Fatalf("logout must clear the cookie, got %q", setRaw)
	}

	// The session id no longer authenticates.
	resp, _, _ = env.doCookie(t, "GET", "/api/auth/session", cookie, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout session: want 401, got %d", resp.StatusCode)
	}
}

// TestLoginBruteForceLimited: the auth class budget (10/min per IP)
// throttles repeated failures.
func TestLoginBruteForceLimited(t *testing.T) {
	env, _ := newSessionEnv(t)
	creds := credentials{Username: "admin", Password: "wrong-password-123"}
	var last int
	for i := 0; i < 11; i++ {
		resp, _, _ := env.doCookie(t, "POST", "/api/auth/login", "", "", creds)
		last = resp.StatusCode
		if resp.StatusCode == http.StatusTooManyRequests {
			break
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i+1, resp.StatusCode)
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("brute force must be throttled within 11 attempts, last=%d", last)
	}
}

// TestSetupStatusCountsAdmins pins the durable-account semantics.
func TestSetupStatusCountsAdmins(t *testing.T) {
	env, _ := newSessionEnv(t)
	_, payload, _ := env.doCookie(t, "GET", "/api/auth/setup-status", "", "", nil)
	if payload["setup_required"] != true {
		t.Fatalf("want setup_required=true on fresh store, got %v", payload)
	}
	// Insert an admin directly through the repo (no setup flow).
	hash, err := testPasswordHasher("direct-insert-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.AdminUsers().Create(&storage.AdminUser{Username: "root", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	_, payload, _ = env.doCookie(t, "GET", "/api/auth/setup-status", "", "", nil)
	if payload["setup_required"] != false {
		t.Fatalf("want setup_required=false once an admin exists, got %v", payload)
	}
}
