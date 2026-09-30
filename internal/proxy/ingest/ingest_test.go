package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/server"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeAuth struct {
	key domain.VirtualKey
	err error
}

func (f fakeAuth) Verify(_ context.Context, _ string) (domain.VirtualKey, error) {
	return f.key, f.err
}

type recordedCall struct {
	Call    Call
	TraceID string
}

type fakeProxy struct {
	mu     sync.Mutex
	calls  []recordedCall
	status int // written on execute (default 200)
}

func (f *fakeProxy) Execute(ctx context.Context, w http.ResponseWriter, call Call) {
	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{Call: call, TraceID: observability.TraceID(ctx)})
	f.mu.Unlock()
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, `{"proxied":true}`)
}

func (f *fakeProxy) recorded() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedCall, len(f.calls))
	copy(out, f.calls)
	return out
}

var activeKey = domain.VirtualKey{ID: "vkey-1", Name: "test", Status: domain.KeyActive}

// newStack builds the full inbound stack: server chain + ingest routes,
// with fake auth/proxy. This mirrors production wiring (main.go).
func newStack(t *testing.T, auth Authenticator, proxy Proxy, maxBody int64) (*httptest.Server, *fakeProxy) {
	t.Helper()
	r := server.New(server.Options{Logger: nil})
	Register(r.Mux(), Deps{Auth: auth, Proxy: proxy, MaxBodyBytes: maxBody})
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	return srv, proxy.(*fakeProxy)
}

func post(t *testing.T, url, keyHeader, keyQuery, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if keyHeader != "" {
		req.Header.Set("Authorization", "Bearer "+keyHeader)
	}
	if keyQuery != "" {
		q := req.URL.Query()
		q.Set("key", keyQuery)
		req.URL.RawQuery = q.Encode()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	return m
}

// ---------------------------------------------------------------------------
// Routing table: endpoint shapes
// ---------------------------------------------------------------------------

func TestRoutingTable(t *testing.T) {
	srv, proxy := newStack(t, fakeAuth{key: activeKey}, &fakeProxy{}, 0)

	openaiBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, srv.URL+"/v1/chat/completions", "ogk-x", "", openaiBody)
	if resp.StatusCode != 200 {
		t.Fatalf("openai: %d", resp.StatusCode)
	}
	resp.Body.Close()
	calls := proxy.recorded()
	if len(calls) != 1 || calls[0].Call.Protocol != domain.ProtocolOpenAI {
		t.Fatalf("openai call not dispatched: %+v", calls)
	}
	if calls[0].Call.Request.Model != "gpt-4o" {
		t.Fatalf("model: %+v", calls[0].Call.Request)
	}

	resp = post(t, srv.URL+"/v1/messages", "ogk-x", "",
		`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("anthropic: %d", resp.StatusCode)
	}
	resp.Body.Close()
	calls = proxy.recorded()
	if len(calls) != 2 || calls[1].Call.Protocol != domain.ProtocolAnthropic {
		t.Fatalf("anthropic call not dispatched: %+v", calls)
	}

	geminiBody := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	resp = post(t, srv.URL+"/v1beta/models/gemini-2.0-flash:generateContent", "ogk-x", "", geminiBody)
	if resp.StatusCode != 200 {
		t.Fatalf("gemini: %d", resp.StatusCode)
	}
	resp.Body.Close()
	calls = proxy.recorded()
	if len(calls) != 3 || calls[2].Call.Protocol != domain.ProtocolGemini {
		t.Fatalf("gemini call not dispatched: %+v", calls)
	}
	if calls[2].Call.Request.Model != "gemini-2.0-flash" {
		t.Fatalf("gemini model must come from the path: %+v", calls[2].Call.Request)
	}
	if calls[2].Call.Stream {
		t.Fatal("generateContent is non-streaming")
	}

	// Streaming variant: stream flag comes from the path.
	resp = post(t, srv.URL+"/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse", "ogk-x", "", geminiBody)
	if resp.StatusCode != 200 {
		t.Fatalf("gemini stream: %d", resp.StatusCode)
	}
	resp.Body.Close()
	calls = proxy.recorded()
	if !calls[3].Call.Stream {
		t.Fatal("streamGenerateContent must set Stream")
	}

	// OpenAI stream flag comes from the body.
	resp = post(t, srv.URL+"/v1/chat/completions", "ogk-x", "",
		`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	if !proxy.recorded()[4].Call.Stream {
		t.Fatal(`"stream":true must propagate`)
	}

	// Request IDs ride through the server chain into the proxy context.
	if id := proxy.recorded()[0].TraceID; id == "" {
		t.Fatal("proxy must receive the trace ID via context")
	}
}

func TestUnknownGeminiMethodIs404(t *testing.T) {
	srv, proxy := newStack(t, fakeAuth{key: activeKey}, &fakeProxy{}, 0)
	resp := post(t, srv.URL+"/v1beta/models/gemini-2.0-flash:bogusMethod", "ogk-x", "", `{}`)
	m := decodeJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	errObj := m["error"].(map[string]any)
	if errObj["status"] != "NOT_FOUND" {
		t.Fatalf("gemini envelope: %v", m)
	}
	if len(proxy.recorded()) != 0 {
		t.Fatal("no proxy call expected")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv, _ := newStack(t, fakeAuth{key: activeKey}, &fakeProxy{}, 0)
	resp, err := http.Get(srv.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET on POST route: %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// 401/403 envelopes (legacy schema, per-protocol shapes)
// ---------------------------------------------------------------------------

func TestAuthErrorEnvelopes(t *testing.T) {
	srv, _ := newStack(t, fakeAuth{err: domain.ErrUnknownKey}, &fakeProxy{}, 0)

	t.Run("openai 401", func(t *testing.T) {
		resp := post(t, srv.URL+"/v1/chat/completions", "ogk-wrong", "", `{"model":"m","messages":[]}`)
		m := decodeJSON(t, resp)
		if resp.StatusCode != 401 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		e := m["error"].(map[string]any)
		if e["type"] != "authentication_error" || e["code"] != "invalid_api_key" {
			t.Fatalf("openai 401 envelope: %v", m)
		}
	})

	t.Run("anthropic 401", func(t *testing.T) {
		resp := post(t, srv.URL+"/v1/messages", "ogk-wrong", "", `{}`)
		m := decodeJSON(t, resp)
		if resp.StatusCode != 401 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		if m["type"] != "error" {
			t.Fatalf("anthropic type field: %v", m)
		}
		e := m["error"].(map[string]any)
		if e["type"] != "authentication_error" {
			t.Fatalf("anthropic 401 envelope: %v", m)
		}
	})

	t.Run("gemini 401", func(t *testing.T) {
		resp := post(t, srv.URL+"/v1beta/models/m:generateContent", "ogk-wrong", "", `{}`)
		m := decodeJSON(t, resp)
		if resp.StatusCode != 401 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		e := m["error"].(map[string]any)
		if e["status"] != "UNAUTHENTICATED" {
			t.Fatalf("gemini 401 envelope: %v", m)
		}
	})
}

func TestAuthError403Revoked(t *testing.T) {
	srv, _ := newStack(t, fakeAuth{err: domain.ErrKeyRevoked}, &fakeProxy{}, 0)

	resp := post(t, srv.URL+"/v1/chat/completions", "ogk-x", "", `{}`)
	m := decodeJSON(t, resp)
	if resp.StatusCode != 403 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	e := m["error"].(map[string]any)
	if e["code"] != "key_revoked" {
		t.Fatalf("403 envelope: %v", m)
	}

	// Anthropic renders permission_error; Gemini PERMISSION_DENIED.
	resp = post(t, srv.URL+"/v1/messages", "ogk-x", "", `{}`)
	m = decodeJSON(t, resp)
	e = m["error"].(map[string]any)
	if e["type"] != "permission_error" {
		t.Fatalf("anthropic 403: %v", m)
	}

	resp = post(t, srv.URL+"/v1beta/models/m:generateContent", "ogk-x", "", `{}`)
	m = decodeJSON(t, resp)
	e = m["error"].(map[string]any)
	if e["status"] != "PERMISSION_DENIED" {
		t.Fatalf("gemini 403: %v", m)
	}
}

func TestMissingCredentialEnvelopes(t *testing.T) {
	srv, _ := newStack(t, fakeAuth{err: domain.ErrNoCredential}, &fakeProxy{}, 0)

	resp := post(t, srv.URL+"/v1/chat/completions", "", "", `{}`)
	m := decodeJSON(t, resp)
	if resp.StatusCode != 401 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	e := m["error"].(map[string]any)
	if e["code"] != "missing_api_key" {
		t.Fatalf("missing key envelope: %v", m)
	}
}

// ---------------------------------------------------------------------------
// Credential extraction
// ---------------------------------------------------------------------------

func TestCredentialExtraction(t *testing.T) {
	srv, proxy := newStack(t, fakeAuth{key: activeKey}, &fakeProxy{}, 0)

	// OpenAI: Bearer primary.
	resp := post(t, srv.URL+"/v1/chat/completions", "ogk-a", "",
		`{"model":"m","messages":[]}`)
	resp.Body.Close()

	// OpenAI: x-api-key tolerated.
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	req.Header.Set("x-api-key", "ogk-b")
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}

	// Anthropic: x-api-key primary.
	req2, _ := http.NewRequest("POST", srv.URL+"/v1/messages", strings.NewReader(`{"model":"m","messages":[]}`))
	req2.Header.Set("x-api-key", "ogk-c")
	if resp, err := http.DefaultClient.Do(req2); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}

	// Gemini: query key tolerated (SDK compatibility).
	resp = post(t, srv.URL+"/v1beta/models/m:generateContent", "", "ogk-d", `{}`)
	resp.Body.Close()

	if got := len(proxy.recorded()); got != 4 {
		t.Fatalf("all four credential styles must pass: got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Body handling
// ---------------------------------------------------------------------------

func TestMalformedBodyIs400(t *testing.T) {
	srv, proxy := newStack(t, fakeAuth{key: activeKey}, &fakeProxy{}, 0)
	resp := post(t, srv.URL+"/v1/chat/completions", "ogk-x", "", `{not json`)
	m := decodeJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	e := m["error"].(map[string]any)
	if e["type"] != "invalid_request_error" {
		t.Fatalf("400 envelope: %v", m)
	}
	if len(proxy.recorded()) != 0 {
		t.Fatal("no proxy call expected")
	}
}

func TestOversizeBodyIs413(t *testing.T) {
	srv, proxy := newStack(t, fakeAuth{key: activeKey}, &fakeProxy{}, 64)
	big := `{"model":"m","messages":[` + `"` + strings.Repeat("x", 256) + `"` + strings.Repeat(",\"y\"", 64) + `]}`
	resp := post(t, srv.URL+"/v1/chat/completions", "ogk-x", "", big)
	m := decodeJSON(t, resp)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	e := m["error"].(map[string]any)
	if e["code"] != "request_body_too_large" {
		t.Fatalf("413 envelope: %v", m)
	}
	if len(proxy.recorded()) != 0 {
		t.Fatal("no proxy call expected")
	}
}

func TestRegisterRequiresDeps(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil auth must panic")
		}
	}()
	Register(http.NewServeMux(), Deps{Proxy: &fakeProxy{}})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestBearerToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},
		{"BEARER abc", "abc"},
		{"Bearer", ""},
		{"Bearer  ", ""},
		{"Basic abc", ""},
		{"", ""},
		{"Bearer  spaced  ", "spaced"},
	}
	for _, tc := range cases {
		if got := bearerToken(tc.in); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitGeminiTarget(t *testing.T) {
	cases := []struct {
		target string
		model  string
		method string
		ok     bool
	}{
		{"gemini-2.0-flash:generateContent", "gemini-2.0-flash", "generateContent", true},
		{"gemini-2.0-flash:streamGenerateContent", "gemini-2.0-flash", "streamGenerateContent", true},
		{"models/gemini:pro:generateContent", "models/gemini:pro", "generateContent", true},
		{"gemini-2.0-flash:bogus", "", "", false},
		{"nomethod", "", "", false},
		{":generateContent", "", "", false},
		{"model:", "", "", false},
	}
	for _, tc := range cases {
		m, meth, ok := splitGeminiTarget(tc.target)
		if ok != tc.ok || (ok && (m != tc.model || meth != tc.method)) {
			t.Errorf("splitGeminiTarget(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.target, m, meth, ok, tc.model, tc.method, tc.ok)
		}
	}
}

var _ = bytes.MinRead // keep bytes import if helpers change
