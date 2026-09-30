package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// roundTrip marshals v, unmarshals into a fresh T, re-marshals, and
// requires byte-stable output (JSON-normalized stability, ADR 004).
func roundTrip[T any](t *testing.T, v T) T {
	t.Helper()
	b1, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out T
	if err := json.Unmarshal(b1, &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, b1)
	}
	b2, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("round-trip not byte-stable:\n first: %s\nsecond: %s", b1, b2)
	}
	return out
}

func TestRequestSupersetExpressibility(t *testing.T) {
	temp := 0.0 // explicit zero must survive the round trip (nil vs 0 matters)
	topk := int64(64)
	seed := int64(7)
	req := Request{
		Model: "claude-sonnet",
		Messages: []Message{
			{Role: RoleSystem, Content: []ContentBlock{{Type: BlockText, Text: "You are terse."}}},
			{Role: RoleUser, Content: []ContentBlock{
				{Type: BlockText, Text: "What is in this image?"},
				{Type: BlockImage, Image: &ImageContent{
					MimeType: "image/png", Base64: "aGVsbG8=", Detail: "high",
				}},
			}},
			{Role: RoleAssistant, Content: []ContentBlock{
				{Type: BlockText, Text: "Checking."},
				{Type: BlockToolCall, Call: &ToolCall{
					ID: "call_1", Name: "read_file", Arguments: `{"path":"/etc/hosts"}`,
				}},
			}},
			{Role: RoleUser, Content: []ContentBlock{
				{Type: BlockToolResult, Tool: &ToolResult{
					CallID:  "call_1",
					Content: []ContentBlock{{Type: BlockText, Text: "127.0.0.1 localhost"}},
				}},
			}},
			{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "Summarize."}}},
		},
		Tools: []Tool{{
			Name:        "read_file",
			Description: "Read a file from disk",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
		ToolChoice: &ToolChoice{Mode: ToolChoiceNamed, Name: "read_file"},
		Stream:     true,
		Sampling: SamplingParams{
			MaxTokens: 1024, Temperature: &temp, TopP: nil, TopK: &topk,
			StopSequences: []string{"END"},
			Seed:          &seed,
			LogitBias:     map[string]int{"50256": -100, " \n": 5},
			Logprobs:      true, TopLogprobs: 5,
			ResponseFormat: &ResponseFormat{Type: "json_schema", JSONSchema: json.RawMessage(`{"name":"out","schema":{"type":"object"}}`)},
		},
		User: "user-123",
	}

	got := roundTrip(t, req)

	// Acceptance: tool calls expressible.
	assistant := got.Messages[2].Content[1].Call
	if assistant == nil || assistant.Name != "read_file" || assistant.Arguments != `{"path":"/etc/hosts"}` {
		t.Fatalf("tool call lost in round trip: %+v", got.Messages[2].Content[1])
	}
	// Acceptance: multimodal blocks expressible.
	img := got.Messages[1].Content[1].Image
	if img == nil || img.MimeType != "image/png" || img.Base64 != "aGVsbG8=" || img.Detail != "high" {
		t.Fatalf("image block lost in round trip: %+v", got.Messages[1].Content[1])
	}
	// Acceptance: logit biases expressible (both token-id and string keys).
	if got.Sampling.LogitBias["50256"] != -100 || got.Sampling.LogitBias[" \n"] != 5 {
		t.Fatalf("logit bias lost: %+v", got.Sampling.LogitBias)
	}
	// Explicit zero temperature must survive (nil means "provider default").
	if got.Sampling.Temperature == nil || *got.Sampling.Temperature != 0 {
		t.Fatalf("explicit temperature 0 collapsed to nil: %+v", got.Sampling.Temperature)
	}
	// Raw JSON schema must survive verbatim.
	if string(got.Tools[0].InputSchema) != string(req.Tools[0].InputSchema) {
		t.Fatalf("input schema perturbed: %s", got.Tools[0].InputSchema)
	}
	// Tool arguments must not be re-marshalled (verbatim string).
	if got.Messages[2].Content[1].Call.Arguments != req.Messages[2].Content[1].Call.Arguments {
		t.Fatal("tool arguments perturbed")
	}
}

func TestResponseRoundTrip(t *testing.T) {
	resp := Response{
		ID: "resp_1", Model: "gpt-4o", Role: RoleAssistant,
		Content: []ContentBlock{
			{Type: BlockThinking, Text: "Let me think.", Signature: "sig1"},
			{Type: BlockText, Text: "Hello!"},
			{Type: BlockToolCall, Call: &ToolCall{ID: "call_9", Name: "search", Arguments: `{"q":"onegate"` + "}"}},
		},
		FinishReason: FinishToolCalls,
		Usage:        TokenUsage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3, CacheReadTokens: 2},
		CreatedMS:    1770000000000,
		Provider:     ProviderMeta{ProviderID: "prov-1", NativeFinishReason: "MALFORMED_FUNCTION_CALL"},
	}
	got := roundTrip(t, resp)
	if got.Content[0].Signature != "sig1" {
		t.Fatal("thinking signature lost")
	}
	if got.FinishReason != FinishToolCalls {
		t.Fatalf("finish reason lost: %s", got.FinishReason)
	}
}

func TestStreamEventRoundTrip(t *testing.T) {
	events := []StreamEvent{
		{Type: EventMessageStart, ID: "msg_1", Model: "claude-sonnet", Role: RoleAssistant},
		{Type: EventBlockStart, Index: 0, Block: &ContentBlock{Type: BlockToolCall, Call: &ToolCall{ID: "toolu_1", Name: "read_file"}}},
		{Type: EventBlockDelta, Index: 0, ArgumentsDelta: `{"path"`},
		{Type: EventBlockDelta, Index: 0, ArgumentsDelta: `:"/tmp"}`},
		{Type: EventBlockStop, Index: 0},
		{Type: EventMessageDelta, FinishReason: FinishToolCalls, Usage: &TokenUsage{InputTokens: 12, OutputTokens: 8}},
		{Type: EventMessageStop},
		{Type: EventPing},
		{Type: EventError, Error: &GatewayError{Status: 429, Type: ErrRateLimit, Code: "rate_limit", Message: "slow down", Retryable: true}},
	}
	for i, ev := range events {
		got := roundTrip(t, ev)
		if got.Type != ev.Type {
			t.Fatalf("event %d: type changed: %s", i, got.Type)
		}
	}
}

func TestTokenUsageHelpers(t *testing.T) {
	u := TokenUsage{InputTokens: 7, OutputTokens: 3}.WithTotalDerivation()
	if u.TotalTokens != 10 {
		t.Fatalf("total derivation: got %d want 10", u.TotalTokens)
	}
	// Explicit total is not overwritten.
	u = TokenUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 99}.WithTotalDerivation()
	if u.TotalTokens != 99 {
		t.Fatalf("explicit total overwritten: %d", u.TotalTokens)
	}
	sum := TokenUsage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 4}.Add(
		TokenUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 40})
	if sum.InputTokens != 11 || sum.OutputTokens != 22 || sum.CacheReadTokens != 44 {
		t.Fatalf("Add misbehaves: %+v", sum)
	}
}

// TestEnumStringStability pins the wire strings: adapters, storage, and the
// dashboard all switch on these exact values.
func TestEnumStringStability(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{string(ProtocolOpenAI), "openai"},
		{string(ProtocolAnthropic), "anthropic"},
		{string(ProtocolGemini), "gemini"},
		{string(ProtocolOpenAIComp), "openai-compat"},
		{string(BlockToolResult), "tool_result"},
		{string(BlockThinking), "thinking"},
		{string(FinishToolCalls), "tool_calls"},
		{string(FinishContentFilter), "content_filter"},
		{string(EventMessageStart), "message_start"},
		{string(EventBlockDelta), "block_delta"},
		{string(ErrRateLimit), "rate_limit_error"},
		{string(ErrOverloaded), "overloaded_error"},
		{string(ToolChoiceRequired), "required"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("enum drift: got %q want %q", c.got, c.want)
		}
	}
	if !strings.Contains(string(ErrInvalidRequest), "invalid_request") {
		t.Error("sanity")
	}
}
