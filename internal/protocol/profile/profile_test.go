package profile

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captured records what the mock provider observed.
type captured struct {
	method string
	path   string
	header http.Header
	query  string
	body   string
}

// mockProvider spins an httptest server that records the incoming request.
func mockProvider(t *testing.T) (*httptest.Server, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.header = r.Header.Clone()
		cap.query = r.URL.RawQuery
		cap.body = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// TestProfileMatrix walks every built-in profile against a mock server and
// verifies the transport envelope: path, auth header, body passthrough
// (acceptance: profile matrix tests against mock servers per provider
// flavor).
func TestProfileMatrix(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	cases := []struct {
		id       string
		wantPath string
		wantAuth string // exact Authorization header, "" = absent
		key      string
		wantErr  bool
	}{
		{"openai", "/v1/chat/completions", "Bearer sk-test", "sk-test", false},
		{"openrouter", "/api/v1/chat/completions", "Bearer sk-or", "sk-or", false},
		{"groq", "/openai/v1/chat/completions", "Bearer gsk_test", "gsk_test", false},
		{"mistral", "/v1/chat/completions", "Bearer sk-mistral", "sk-mistral", false},
		{"vllm", "/v1/chat/completions", "Bearer token-abc", "token-abc", false},
		{"ollama", "/v1/chat/completions", "", "", false},
		// Required keys are enforced at build time.
		{"openai", "/v1/chat/completions", "", "", true},
		{"vllm", "/v1/chat/completions", "", "", false}, // optional
		{"bogus-provider", "", "", "", true},            // unknown profile
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			p, err := Resolve(tc.id)
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatal(err)
			}
			srv, cap := mockProvider(t)
			// Per-flavor base URLs mirror real deployments (the version
			// prefix is part of the configured base URL).
			base := map[string]string{
				"openai":     srv.URL + "/v1",
				"openrouter": srv.URL + "/api/v1",
				"groq":       srv.URL + "/openai/v1",
				"mistral":    srv.URL + "/v1",
				"vllm":       srv.URL + "/v1",
				"ollama":     srv.URL + "/v1",
			}[tc.id]

			req, err := p.Build(http.MethodPost, base, tc.key, body)
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			if cap.method != http.MethodPost {
				t.Fatalf("method: %s", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Fatalf("path: %s want %s", cap.path, tc.wantPath)
			}
			if got := cap.header.Get("Authorization"); got != tc.wantAuth {
				t.Fatalf("authorization: %q want %q", got, tc.wantAuth)
			}
			if cap.header.Get("Content-Type") != "application/json" {
				t.Fatalf("content-type: %q", cap.header.Get("Content-Type"))
			}
			if cap.body != string(body) {
				t.Fatalf("body passthrough: %s", cap.body)
			}
		})
	}
}

func TestURLJoining(t *testing.T) {
	p, err := Resolve("openai")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"https://api.openai.com/v1":  "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1/": "https://api.openai.com/v1/chat/completions", // trailing slash trimmed
		"http://localhost:8000":      "http://localhost:8000/chat/completions",
	}
	for base, want := range cases {
		req, err := p.Build(http.MethodPost, base, "k", []byte("{}"))
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if req.URL.String() != want {
			t.Fatalf("%s → %s want %s", base, req.URL.String(), want)
		}
	}
}

// TestAuthTemplating covers header and query auth styles plus {key}
// placeholder expansion in extra headers.
func TestAuthTemplating(t *testing.T) {
	srv, cap := mockProvider(t)
	defer srv.Close()

	p := Profile{
		ID:             "custom-gateway",
		Path:           "/v2/chat",
		Auth:           AuthHeader,
		AuthHeaderName: "x-api-key",
		ExtraHeaders: map[string]string{
			"X-Gateway-Token": "Bearer {key}",
			"X-Static":        "abc",
		},
	}
	req, err := p.Build(http.MethodPost, srv.URL, "secret123", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := cap.header.Get("x-api-key"); got != "secret123" {
		t.Fatalf("x-api-key: %q", got)
	}
	if got := cap.header.Get("X-Gateway-Token"); got != "Bearer secret123" {
		t.Fatalf("templated header: %q", got)
	}
	if got := cap.header.Get("X-Static"); got != "abc" {
		t.Fatalf("static header: %q", got)
	}
	if cap.path != "/v2/chat" {
		t.Fatalf("path: %s", cap.path)
	}

	// Query auth style.
	p2 := Profile{ID: "query-auth", Auth: AuthQuery, AuthQueryParam: "apikey"}
	req2, err := p2.Build(http.MethodPost, srv.URL, "qk", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if cap.query != "apikey=qk" {
		t.Fatalf("query: %q", cap.query)
	}

	// Misconfigured styles fail fast.
	if _, err := (Profile{ID: "x", Auth: AuthHeader}).Build("POST", srv.URL, "k", nil); err == nil {
		t.Fatal("header auth without name should fail")
	}
	if _, err := (Profile{ID: "x", Auth: AuthQuery}).Build("POST", srv.URL, "k", nil); err == nil {
		t.Fatal("query auth without param should fail")
	}
}

func TestOpenRouterAttributionHeaders(t *testing.T) {
	p, err := Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	// ExtraHeaders are a deployment decision (site attribution); a custom
	// profile layered on the preset carries them.
	p.ExtraHeaders = map[string]string{"HTTP-Referer": "https://onegate.local", "X-Title": "OneGate"}
	srv, cap := mockProvider(t)
	defer srv.Close()

	req, err := p.Build(http.MethodPost, srv.URL, "sk-or", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if cap.header.Get("HTTP-Referer") != "https://onegate.local" || cap.header.Get("X-Title") != "OneGate" {
		t.Fatalf("attribution headers: %+v", cap.header)
	}
}
