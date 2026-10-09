package mockprovider

// Scenario dispatch tests: every "sc-*" model name must produce the
// documented wire behavior on every protocol surface. These are the
// fixtures the parity corpus (test/parity/corpus) relies on — if a
// scenario changes, corpus expectations change with it.
import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, h http.Handler, path string, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestParseScenario(t *testing.T) {
	cases := []struct {
		model string
		want  scenarioKind
		ok    bool
	}{
		{"sc-500", scStatus, true},
		{"sc-401", scStatus, true},
		{"sc-429", scStatus, true},
		{"sc-429rr", scStatus, true},
		{"sc-599", scStatus, true},
		{"sc-399", scNone, false},  // below 400
		{"sc-5000", scNone, false}, // not 3 digits
		{"sc-ctxlen", scCtxLen, true},
		{"sc-stall", scStall, true},
		{"sc-delay-250", scDelay, true},
		{"sc-delay-", scNone, false},
		{"sc-delay-abc", scNone, false},
		{"sc-midstream", scMidstream, true},
		{"sc-badjson", scBadJSON, true},
		{"sc-toolstream", scToolStream, true},
		{"sc-thinkstream", scThinkStream, true},
		{"sc-ping", scPing, true},
		{"mock-echo", scNone, false},
		{"gpt-4o", scNone, false},
		{"disc-500", scNone, false},
		{"", scNone, false},
	}
	for _, tc := range cases {
		sc, ok := parseScenario(tc.model)
		if ok != tc.ok || sc.kind != tc.want {
			t.Errorf("parseScenario(%q) = kind %d ok %v; want kind %d ok %v", tc.model, sc.kind, ok, tc.want, tc.ok)
		}
	}
}

func TestScenarioErrorStatusPerProtocol(t *testing.T) {
	h := NewHandler()
	cases := []struct {
		path string
		body string
		want int
	}{
		{"/v1/chat/completions", `{"model":"sc-500"}`, 500},
		{"/v1/messages", `{"model":"sc-500"}`, 500},
		{"/v1beta/models/sc-500:generateContent", `{}`, 500},
		{"/v1/chat/completions", `{"model":"sc-401","stream":true}`, 401},
	}
	for _, tc := range cases {
		resp := post(t, h, tc.path, tc.body)
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s: status = %d, want %d", tc.path, tc.body, resp.StatusCode, tc.want)
		}
	}
}

func TestScenario429WithRetryAfter(t *testing.T) {
	h := NewHandler()
	resp := post(t, h, "/v1/chat/completions", `{"model":"sc-429rr"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After = %q, want 7", got)
	}
}

func TestScenarioContextLength(t *testing.T) {
	h := NewHandler()
	cases := []struct {
		path     string
		body     string
		contains string
	}{
		{"/v1/chat/completions", `{"model":"sc-ctxlen"}`, "context_length_exceeded"},
		{"/v1/messages", `{"model":"sc-ctxlen"}`, "prompt is too long"},
		{"/v1beta/models/sc-ctxlen:generateContent", `{}`, "INVALID_ARGUMENT"},
	}
	for _, tc := range cases {
		resp := post(t, h, tc.path, tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.path, resp.StatusCode)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(b), tc.contains) {
			t.Errorf("%s: body %q missing %q", tc.path, b, tc.contains)
		}
	}
}

func TestScenarioBadJSON(t *testing.T) {
	h := NewHandler()
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1beta/models/sc-badjson:generateContent"} {
		resp := post(t, h, path, `{"model":"sc-badjson"}`)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestScenarioToolStreamOrder(t *testing.T) {
	h := NewHandler()
	resp := post(t, h, "/v1/chat/completions", `{"model":"sc-toolstream","stream":true}`)
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	if !strings.Contains(s, `"tool_calls":[`) || !strings.Contains(s, `"name":"get_weather"`) {
		t.Fatalf("tool stream missing tool_calls: %q", s)
	}
	if !strings.Contains(s, `Paris`) {
		t.Fatalf("tool stream missing arguments delta: %q", s)
	}
	if !strings.Contains(s, `"finish_reason":"tool_calls"`) {
		t.Fatalf("tool stream missing finish: %q", s)
	}
	if !strings.HasSuffix(s, "data: [DONE]\n\n") {
		t.Fatalf("tool stream must end with [DONE]: %q", s)
	}
	// Non-streaming requests take the normal path.
	resp2 := post(t, h, "/v1/chat/completions", `{"model":"sc-toolstream"}`)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("non-stream toolstream status = %d, want 200 (normal echo)", resp2.StatusCode)
	}
}

func TestScenarioThinkStreamOrder(t *testing.T) {
	h := NewHandler()
	resp := post(t, h, "/v1/messages", `{"model":"sc-thinkstream","stream":true,"max_tokens":10}`)
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	for _, want := range []string{
		"event: message_start",
		`"type":"thinking"`,
		`"thinking_delta"`,
		`"signature_delta"`,
		`"text_delta"`,
		"event: message_stop",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("think stream missing %s in %q", want, s)
		}
	}
	// thinking deltas precede text deltas
	if strings.Index(s, "thinking_delta") > strings.Index(s, "text_delta") {
		t.Errorf("thinking delta must precede text delta")
	}
}

func TestScenarioPingStream(t *testing.T) {
	h := NewHandler()
	resp := post(t, h, "/v1/messages", `{"model":"sc-ping","stream":true,"max_tokens":10}`)
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	if !strings.Contains(s, "event: ping") {
		t.Fatalf("ping stream missing ping event: %q", s)
	}
	if strings.Index(s, "event: ping") > strings.Index(s, "content_block_delta") {
		t.Errorf("ping must precede content deltas")
	}
}

func TestScenarioMidstreamAborts(t *testing.T) {
	h := NewHandler()
	resp := post(t, h, "/v1/chat/completions", `{"model":"sc-midstream","stream":true}`)
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	if !strings.Contains(s, `"role":"assistant"`) {
		t.Fatalf("midstream must emit the first frame: %q", s)
	}
	if strings.Contains(s, "[DONE]") {
		t.Errorf("midstream must not terminate cleanly: %q", s)
	}
}

func TestScenarioDelayStillEchoes(t *testing.T) {
	h := NewHandler()
	resp := post(t, h, "/v1/chat/completions", `{"model":"sc-delay-5"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "Hello from mock OpenAI") {
		t.Errorf("delay scenario must fall through to echo, got %q", b)
	}
}

func TestScenarioUnknownModelsUnaffected(t *testing.T) {
	h := NewHandler()
	for _, model := range []string{"gpt-4o", "mock-echo", "sc-", "sc-nonsense"} {
		resp := post(t, h, "/v1/chat/completions", fmt.Sprintf(`{"model":%q}`, model))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("model %q: status = %d, want normal 200", model, resp.StatusCode)
		}
	}
}

func TestGeminiModelFromTarget(t *testing.T) {
	cases := []struct{ target, want string }{
		{"gemini-1.5-flash:generateContent", "gemini-1.5-flash"},
		{"models/gemini-2.0-flash:streamGenerateContent", "models/gemini-2.0-flash"},
		{":generateContent", ""},
		{"nocolon", ""},
	}
	for _, tc := range cases {
		if got := modelFromTarget(tc.target); got != tc.want {
			t.Errorf("modelFromTarget(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
}
