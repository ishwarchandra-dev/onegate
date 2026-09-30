package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// ---------------------------------------------------------------------------
// Request building: URL, auth, headers per protocol
// ---------------------------------------------------------------------------

func TestBuildOpenAIBearerAuth(t *testing.T) {
	c := New(TransportConfig{}, nil)
	p := Provider{
		ID:       "openai-main",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  "https://api.openai.com/v1",
		APIKey:   "sk-secret",
	}
	req := Request{Path: "/chat/completions", Body: []byte(`{}`)}

	hr, err := c.Build(context.Background(), p, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := hr.URL.String(); got != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("url: %s", got)
	}
	if got := hr.Header.Get("Authorization"); got != "Bearer sk-secret" {
		t.Fatalf("auth: %q", got)
	}
	if ct := hr.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: %q", ct)
	}
	if acc := hr.Header.Get("Accept"); acc != "application/json" {
		t.Fatalf("accept: %q", acc)
	}
	if ua := hr.Header.Get("User-Agent"); !strings.HasPrefix(ua, "onegate/") {
		t.Fatalf("user-agent: %q", ua)
	}
}

func TestBuildStreamAccept(t *testing.T) {
	c := New(TransportConfig{}, nil)
	p := Provider{ID: "a", Protocol: domain.ProtocolAnthropic, BaseURL: "https://api.anthropic.com", APIKey: "k"}
	hr, err := c.Build(context.Background(), p, Request{Path: "/v1/messages", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if acc := hr.Header.Get("Accept"); acc != "text/event-stream" {
		t.Fatalf("stream accept: %q", acc)
	}
}

func TestBuildAnthropicHeaders(t *testing.T) {
	c := New(TransportConfig{}, nil)
	p := Provider{
		ID:       "anthropic-main",
		Protocol: domain.ProtocolAnthropic,
		BaseURL:  "https://api.anthropic.com",
		APIKey:   "ant-key",
	}
	hr, err := c.Build(context.Background(), p, Request{Path: "/v1/messages", Body: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := hr.Header.Get("x-api-key"); got != "ant-key" {
		t.Fatalf("x-api-key: %q", got)
	}
	if got := hr.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version: %q", got)
	}
	if hr.Header.Get("Authorization") != "" {
		t.Fatal("anthropic must not carry bearer auth")
	}
}

func TestBuildGeminiHeaderAuthNeverQuery(t *testing.T) {
	c := New(TransportConfig{}, nil)
	p := Provider{
		ID:       "gemini-main",
		Protocol: domain.ProtocolGemini,
		BaseURL:  "https://generativelanguage.googleapis.com",
		APIKey:   "g-key",
	}
	hr, err := c.Build(context.Background(), p, Request{
		Path: "/v1beta/models/gemini-2.0-flash:generateContent",
		Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := hr.Header.Get("x-goog-api-key"); got != "g-key" {
		t.Fatalf("x-goog-api-key: %q", got)
	}
	// The key must never ride in the URL (log leakage).
	if strings.Contains(hr.URL.String(), "g-key") {
		t.Fatalf("api key leaked into URL: %s", hr.URL.String())
	}
	if strings.Contains(hr.URL.RawQuery, "key=") {
		t.Fatalf("query auth forbidden: %s", hr.URL.RawQuery)
	}
}

func TestBuildOpenAICompatFlavors(t *testing.T) {
	c := New(TransportConfig{}, nil)
	cases := []struct {
		profile string
		base    string
	}{
		{"groq", "https://api.groq.com/openai/v1"},
		{"mistral", "https://api.mistral.ai/v1"},
		{"ollama", "http://127.0.0.1:11434/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			p := Provider{
				ID:       tc.profile,
				Protocol: domain.ProtocolOpenAIComp,
				BaseURL:  tc.base,
				Profile:  tc.profile,
				APIKey:   "k",
			}
			hr, err := c.Build(context.Background(), p, Request{Body: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(hr.URL.String(), "/chat/completions") {
				t.Fatalf("path: %s", hr.URL.String())
			}
		})
	}
}

func TestExtrasCannotOverrideAuth(t *testing.T) {
	c := New(TransportConfig{}, nil)
	p := Provider{
		ID:           "evil-extras",
		Protocol:     domain.ProtocolOpenAI,
		BaseURL:      "https://api.openai.com/v1",
		APIKey:       "real-key",
		ExtraHeaders: map[string]string{"Authorization": "Bearer attacker"},
	}
	hr, err := c.Build(context.Background(), p, Request{Body: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := hr.Header.Get("Authorization"); got != "Bearer real-key" {
		t.Fatalf("auth must win over extras, got %q", got)
	}
}

func TestMissingKeyRejected(t *testing.T) {
	c := New(TransportConfig{}, nil)
	p := Provider{ID: "openai-x", Protocol: domain.ProtocolOpenAI, BaseURL: "https://api.openai.com/v1"}
	_, err := c.Build(context.Background(), p, Request{Body: nil})
	if err == nil {
		t.Fatal("openai without key must fail")
	}
	e, ok := err.(*Error)
	if !ok || e.GErr.Type != domain.ErrAuthentication {
		t.Fatalf("want authentication error, got %+v", err)
	}

	p2 := Provider{ID: "anthropic-x", Protocol: domain.ProtocolAnthropic, BaseURL: "https://api.anthropic.com"}
	_, err = c.Build(context.Background(), p2, Request{})
	if err == nil {
		t.Fatal("anthropic without key must fail")
	}
}

// ---------------------------------------------------------------------------
// SSRF guard
// ---------------------------------------------------------------------------

func TestSSRFSchemeAllowlist(t *testing.T) {
	c := New(TransportConfig{}, nil)
	for _, scheme := range []string{"file", "gopher", "ftp", "data", "javascript"} {
		p := Provider{ID: "bad", Protocol: domain.ProtocolOpenAI, BaseURL: scheme + "://example.com/v1", APIKey: "k"}
		_, err := c.Build(context.Background(), p, Request{Body: []byte(`{}`)})
		if err == nil {
			t.Fatalf("%s scheme must be rejected", scheme)
		}
		e, ok := err.(*Error)
		if !ok || e.GErr.Type != domain.ErrPermission {
			t.Fatalf("%s: want permission error, got %v", scheme, err)
		}
	}
}

func TestSSRFMetadataIPsBlocked(t *testing.T) {
	c := New(TransportConfig{}, nil)
	// Build-time screening (literal IP in URL).
	for _, host := range []string{
		"169.254.169.254", // AWS/GCP/Azure metadata
		"169.254.170.2",   // ECS credentials
		"100.100.100.200", // Alibaba metadata
		"192.0.0.192",     // Oracle metadata
	} {
		p := Provider{ID: "meta", Protocol: domain.ProtocolOpenAI, BaseURL: "http://" + host + "/v1", APIKey: "k"}
		_, err := c.Build(context.Background(), p, Request{Body: []byte(`{}`)})
		if err == nil {
			t.Fatalf("metadata host %s must be blocked at build time", host)
		}
	}
}

func TestSSRFDialTimeBlocking(t *testing.T) {
	// End-to-end: a base URL pointing at a metadata IP is refused before
	// any socket is opened (build-time screening of literal IPs).
	guard := DefaultSSRFGuard()
	c := New(TransportConfig{}, guard)
	p := Provider{
		ID:       "rebind",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  "http://169.254.169.254/v1",
		APIKey:   "k",
	}
	_, err := c.Do(context.Background(), p, Request{Body: []byte(`{}`)})
	if err == nil {
		t.Fatal("dial to metadata IP must fail")
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T", err)
	}
	if e.GErr.Type != domain.ErrPermission || e.GErr.Retryable {
		t.Fatalf("SSRF refusal must be a non-retryable permission error: %+v", e.GErr)
	}
}

func TestSSRFCheckAddressDialHook(t *testing.T) {
	// The dial-time Control hook screens the post-DNS address. This is
	// the authoritative enforcement point (covers hostnames that resolve
	// into blocked ranges), so its logic is tested directly.
	guard := DefaultSSRFGuard()
	blocked := []struct{ network, addr string }{
		{"tcp", "169.254.169.254:80"},  // AWS/GCP/Azure metadata
		{"tcp4", "169.254.170.2:80"},   // ECS credentials
		{"tcp", "100.100.100.200:80"},  // Alibaba metadata
		{"tcp", "192.0.0.192:80"},      // Oracle metadata
		{"tcp6", "[fe80::1]:443"},      // IPv6 link-local
		{"tcp6", "[fd00:ec2::254]:80"}, // AWS IPv6 metadata
	}
	for _, c := range blocked {
		if err := guard.checkAddress(c.network, c.addr); err == nil {
			t.Fatalf("dial %s must be refused", c.addr)
		}
	}
	allowed := []struct{ network, addr string }{
		{"tcp", "127.0.0.1:11434"},    // local Ollama (by design)
		{"tcp", "10.0.0.5:8080"},      // LAN vLLM
		{"tcp", "1.1.1.1:443"},        // public
		{"tcp6", "[2600::1]:443"},     // public v6
		{"udp", "169.254.169.254:53"}, // non-TCP is not screened
	}
	for _, c := range allowed {
		if err := guard.checkAddress(c.network, c.addr); err != nil {
			t.Fatalf("dial %s must be allowed: %v", c.addr, err)
		}
	}
}

func TestSSRFLocalhostAllowedByDesign(t *testing.T) {
	// Loopback is deliberately allowed: local Ollama is a first-class
	// deployment. This pins the policy decision (documented in ssrf.go).
	guard := DefaultSSRFGuard()
	p := Provider{ID: "local", Protocol: domain.ProtocolOpenAI, BaseURL: "http://127.0.0.1:1/v1", APIKey: "k"}
	hr, err := p.build(Request{Body: []byte(`{}`)}, guard)
	if err != nil {
		t.Fatalf("loopback must be allowed: %v", err)
	}
	if hr == nil {
		t.Fatal("nil request")
	}
}

func TestSSRFCustomExtraBlocks(t *testing.T) {
	guard, err := NewSSRFGuard(nil, []string{"10.1.2.3/32"})
	if err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "x", Protocol: domain.ProtocolOpenAI, BaseURL: "http://10.1.2.3/v1", APIKey: "k"}
	_, berr := p.build(Request{}, guard)
	if berr == nil {
		t.Fatal("custom block must apply")
	}
}

// ---------------------------------------------------------------------------
// Connection reuse
// ---------------------------------------------------------------------------

// countingListener tracks accepted connections.
type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return c, err
}

func TestConnectionReuseSequentialLoad(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cl := &countingListener{Listener: ln}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})}
	go func() { _ = srv.Serve(cl) }()
	defer srv.Close()

	c := New(TransportConfig{}, nil)
	p := Provider{ID: "mock", Protocol: domain.ProtocolOpenAI, BaseURL: "http://" + ln.Addr().String() + "/v1", APIKey: "k"}

	// 50 sequential requests through the same client: one connection.
	for i := 0; i < 50; i++ {
		resp, err := c.Do(context.Background(), p, Request{Body: []byte(`{}`)})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close() // returning to the pool is what enables reuse
	}
	if n := cl.accepted.Load(); n != 1 {
		t.Fatalf("sequential load must reuse one connection, accepted %d", n)
	}
}

func TestConnectionReuseConcurrentLoad(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cl := &countingListener{Listener: ln}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})}
	go func() { _ = srv.Serve(cl) }()
	defer srv.Close()

	const workers = 8
	c := New(TransportConfig{MaxIdleConnsPerHost: workers}, nil)
	p := Provider{ID: "mock", Protocol: domain.ProtocolOpenAI, BaseURL: "http://" + ln.Addr().String() + "/v1", APIKey: "k"}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				resp, err := c.Do(context.Background(), p, Request{Body: []byte(`{}`)})
				if err != nil {
					t.Errorf("do: %v", err)
					return
				}
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	// 200 requests, 8 concurrent workers: at most `workers` connections
	// (steady state), i.e. no per-request churn (which would be 200).
	if n := cl.accepted.Load(); n > workers {
		t.Fatalf("connection churn: %d accepted for %d requests", n, workers*25)
	}
}

// ---------------------------------------------------------------------------
// Transport errors
// ---------------------------------------------------------------------------

func TestTransportErrorClassification(t *testing.T) {
	c := New(TransportConfig{}, nil)
	unreachable := Provider{ID: "dead", Protocol: domain.ProtocolOpenAI, BaseURL: "http://127.0.0.1:1/v1", APIKey: "k"}

	// Cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Do(ctx, unreachable, Request{Body: []byte(`{}`)})
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T", err)
	}
	if e.GErr.Type != domain.ErrCancelled || e.GErr.Retryable {
		t.Fatalf("cancel: %+v", e.GErr)
	}

	// Deadline: a server that accepts but never answers, so the context
	// deadline fires mid-request (not raced by a fast connection error).
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer slow.Close()
	slowP := Provider{ID: "slow", Protocol: domain.ProtocolOpenAI, BaseURL: slow.URL + "/v1", APIKey: "k"}
	dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer dcancel()
	_, err = c.Do(dctx, slowP, Request{Body: []byte(`{}`)})
	e, ok = err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T", err)
	}
	if e.GErr.Type != domain.ErrTimeout || !e.GErr.Retryable {
		t.Fatalf("deadline: %+v", e.GErr)
	}

	// Connection refused (plain network failure).
	_, err = c.Do(context.Background(), unreachable, Request{Body: []byte(`{}`)})
	e, ok = err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T", err)
	}
	if e.GErr.Type != domain.ErrAPI || !e.GErr.Retryable {
		t.Fatalf("refused: %+v", e.GErr)
	}
	if e.Provider != "dead" {
		t.Fatalf("error must carry provider id, got %q", e.Provider)
	}
}

func TestDoRoundTripMock(t *testing.T) {
	var gotAuth, gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := New(TransportConfig{}, nil)
	p := Provider{ID: "mock", Protocol: domain.ProtocolOpenAI, BaseURL: srv.URL + "/v1", APIKey: "mock-key"}
	resp, err := c.Do(context.Background(), p, Request{Body: []byte(`{"q":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if gotAuth.Load() != "Bearer mock-key" {
		t.Fatalf("auth seen by mock: %v", gotAuth.Load())
	}
	if gotPath.Load() != "/v1/chat/completions" {
		t.Fatalf("path seen by mock: %v", gotPath.Load())
	}
}

// ---------------------------------------------------------------------------
// Endpoint paths
// ---------------------------------------------------------------------------

func TestEndpointPath(t *testing.T) {
	cases := []struct {
		name   string
		p      Provider
		model  string
		stream bool
		path   string
		query  string
	}{
		{
			"openai", Provider{Protocol: domain.ProtocolOpenAI}, "", false,
			"/chat/completions", "",
		},
		{
			"groq compat", Provider{Protocol: domain.ProtocolOpenAIComp, Profile: "groq"}, "", false,
			"/chat/completions", "",
		},
		{
			"anthropic", Provider{Protocol: domain.ProtocolAnthropic}, "", false,
			"/v1/messages", "",
		},
		{
			"gemini", Provider{Protocol: domain.ProtocolGemini}, "gemini-2.0-flash", false,
			"/v1beta/models/gemini-2.0-flash:generateContent", "",
		},
		{
			"gemini stream", Provider{Protocol: domain.ProtocolGemini}, "gemini-2.0-flash", true,
			"/v1beta/models/gemini-2.0-flash:streamGenerateContent", "alt=sse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, query, err := EndpointPath(tc.p, tc.model, tc.stream)
			if err != nil {
				t.Fatal(err)
			}
			if path != tc.path || query != tc.query {
				t.Fatalf("want (%q, %q), got (%q, %q)", tc.path, tc.query, path, query)
			}
		})
	}

	// Gemini without a model is a config error.
	if _, _, err := EndpointPath(Provider{Protocol: domain.ProtocolGemini}, "", false); err == nil {
		t.Fatal("gemini without model must fail")
	}
}
