package openai

import (
	"os"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/golden"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// TestRequestRoundTrip proves decode→encode byte-stability
// (JSON-normalized) over the golden fixtures.
func TestRequestRoundTrip(t *testing.T) {
	for _, name := range []string{"request_minimal.json", "request_tools.json"} {
		t.Run(name, func(t *testing.T) {
			body := load(t, name)
			req, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			re, err := EncodeRequest(req)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if msg := golden.Diff(body, re); msg != "" {
				t.Fatal(msg)
			}
		})
	}
}

// TestRequestDecodedShape pins the canonical decode of the kitchen-sink
// fixture: this is the input side of cross-protocol conformance.
func TestRequestDecodedShape(t *testing.T) {
	req, err := DecodeRequest(load(t, "request_tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "gpt-4o" || !req.Stream || req.User != "user-123" {
		t.Fatalf("envelope wrong: %+v", req)
	}
	if got := req.Messages[0].Role; got != domain.RoleSystem {
		t.Fatalf("system role: %s", got)
	}
	if len(req.Messages) != 5 {
		t.Fatalf("message count: %d", len(req.Messages))
	}
	// assistant tool call block
	call := req.Messages[2].Content[0].Call
	if call == nil || call.ID != "call_1" || call.Name != "get_weather" || call.Arguments != `{"city":"SF"}` {
		t.Fatalf("assistant tool call: %+v", req.Messages[2])
	}
	// tool result in a user message
	tr := req.Messages[3].Content[0].Tool
	if tr == nil || tr.CallID != "call_1" || tr.Content[0].Text != "Sunny, 18C" {
		t.Fatalf("tool result: %+v", req.Messages[3])
	}
	// multimodal user message
	img := req.Messages[4].Content[1].Image
	if img == nil || img.MimeType != "image/png" || img.Base64 != "aGVsbG8=" || img.Detail != "high" {
		t.Fatalf("image block: %+v", req.Messages[4])
	}
	// tools + named choice
	if req.Tools[0].Name != "get_weather" || string(req.Tools[0].InputSchema) == "" {
		t.Fatalf("tools: %+v", req.Tools)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != domain.ToolChoiceNamed || req.ToolChoice.Name != "get_weather" {
		t.Fatalf("tool choice: %+v", req.ToolChoice)
	}
	// sampling superset
	s := req.Sampling
	if s.MaxTokens != 1024 || s.Temperature == nil || *s.Temperature != 0.2 ||
		s.TopP == nil || *s.TopP != 0.9 || s.Seed == nil || *s.Seed != 42 ||
		len(s.StopSequences) != 1 || s.LogitBias["50256"] != -100 || !s.Logprobs || s.TopLogprobs != 5 {
		t.Fatalf("sampling: %+v", s)
	}
	if s.ResponseFormat == nil || s.ResponseFormat.Type != "json_schema" {
		t.Fatalf("response format: %+v", s.ResponseFormat)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	for _, name := range []string{"response_text.json", "response_tools.json"} {
		t.Run(name, func(t *testing.T) {
			body := load(t, name)
			resp, err := DecodeResponse(body)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			re, err := EncodeResponse(resp)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if msg := golden.Diff(body, re); msg != "" {
				t.Fatal(msg)
			}
		})
	}
}

func TestResponseDecodedShape(t *testing.T) {
	resp, err := DecodeResponse(load(t, "response_tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "chatcmpl-tool456" || resp.FinishReason != domain.FinishToolCalls {
		t.Fatalf("envelope: %+v", resp)
	}
	if len(resp.Content) != 2 || resp.Content[0].Call.Name != "get_weather" || resp.Content[1].Call.ID != "call_2" {
		t.Fatalf("content: %+v", resp.Content)
	}
	if resp.Usage.InputTokens != 40 || resp.Usage.OutputTokens != 18 || resp.Usage.TotalTokens != 58 || resp.Usage.ReasoningTokens != 6 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	if resp.Provider.ProviderModel != "gpt-4o-2024-08-06" || resp.Provider.NativeFinishReason != "tool_calls" {
		t.Fatalf("provider meta: %+v", resp.Provider)
	}
}

// decodeStreamFixture runs a full .sse fixture through the decoder and
// returns the canonical event sequence (including Finish()).
func decodeStreamFixture(t *testing.T, name string) (events []domain.StreamEvent, frames []sse.Frame) {
	t.Helper()
	frames = sse.SplitFrames(load(t, name))
	d := NewStreamDecoder()
	for _, f := range frames {
		if sse.IsDone(f) {
			break
		}
		evs, err := d.Decode([]byte(f.Data))
		if err != nil {
			t.Fatalf("decode chunk %q: %v", f.Data, err)
		}
		events = append(events, evs...)
	}
	events = append(events, d.Finish()...)
	return events, frames
}

// encodeEvents renders canonical events back to wire frames.
func encodeEvents(t *testing.T, events []domain.StreamEvent, createdMS int64) []sse.Frame {
	t.Helper()
	e := NewStreamEncoder(createdMS)
	var out []sse.Frame
	for _, ev := range events {
		fs, done, err := e.Encode(ev)
		if err != nil {
			t.Fatalf("encode event %s: %v", ev.Type, err)
		}
		out = append(out, fs...)
		if done {
			break
		}
	}
	return out
}

// TestStreamRoundTrip: SSE fixture → canonical events → SSE frames must
// reproduce the fixture frame-by-frame (JSON-normalized).
func TestStreamRoundTrip(t *testing.T) {
	for _, name := range []string{"stream_text.sse", "stream_toolcall.sse"} {
		t.Run(name, func(t *testing.T) {
			events, frames := decodeStreamFixture(t, name)
			var createdMS int64
			for _, f := range frames {
				if !sse.IsDone(f) {
					createdMS = 0 // derived inside the test via decoder below
					break
				}
			}
			// Re-decode to capture created.
			d := NewStreamDecoder()
			for _, f := range frames {
				if sse.IsDone(f) {
					break
				}
				if _, err := d.Decode([]byte(f.Data)); err != nil {
					t.Fatal(err)
				}
			}
			createdMS = d.CreatedMS()

			got := encodeEvents(t, events, createdMS)
			if len(got) != len(frames) {
				t.Fatalf("frame count: want %d got %d\n%v", len(frames), len(got), got)
			}
			for i := range frames {
				if sse.IsDone(frames[i]) {
					if got[i].Data != "[DONE]" {
						t.Fatalf("frame %d: want [DONE] got %q", i, got[i].Data)
					}
					continue
				}
				if msg := golden.Diff([]byte(frames[i].Data), []byte(got[i].Data)); msg != "" {
					t.Fatalf("frame %d: %s\nwant: %s", i, msg, frames[i].Data)
				}
			}
		})
	}
}

// TestStreamEventByEvent verifies the tool-call stream event sequence
// exactly (acceptance: event-by-event verification).
func TestStreamEventByEvent(t *testing.T) {
	events, _ := decodeStreamFixture(t, "stream_toolcall.sse")

	type want struct {
		typ   domain.StreamEventType
		index int
		text  string
		args  string
		call  string
	}
	expected := []want{
		{typ: domain.EventMessageStart},
		{typ: domain.EventBlockStart, index: 0},
		{typ: domain.EventBlockDelta, index: 0, text: "Let me check."},
		{typ: domain.EventBlockStop, index: 0},
		{typ: domain.EventBlockStart, index: 1, call: "get_weather"},
		{typ: domain.EventBlockDelta, index: 1, args: "{\"city\""},
		{typ: domain.EventBlockDelta, index: 1, args: ":\"SF\"}"},
		{typ: domain.EventBlockStop, index: 1},
		{typ: domain.EventMessageDelta}, // finish tool_calls
		{typ: domain.EventMessageDelta}, // usage
		{typ: domain.EventMessageStop},
	}
	if len(events) != len(expected) {
		for i, ev := range events {
			t.Logf("event %d: %+v", i, ev)
		}
		t.Fatalf("event count: want %d got %d", len(expected), len(events))
	}
	for i, w := range expected {
		ev := events[i]
		if ev.Type != w.typ {
			t.Fatalf("event %d: type %s want %s", i, ev.Type, w.typ)
		}
		if ev.Index != w.index {
			t.Fatalf("event %d (%s): index %d want %d", i, ev.Type, ev.Index, w.index)
		}
		if ev.TextDelta != w.text {
			t.Fatalf("event %d (%s): text %q want %q", i, ev.Type, ev.TextDelta, w.text)
		}
		if ev.ArgumentsDelta != w.args {
			t.Fatalf("event %d (%s): args %q want %q", i, ev.Type, ev.ArgumentsDelta, w.args)
		}
		if w.call != "" && (ev.Block == nil || ev.Block.Call == nil || ev.Block.Call.Name != w.call) {
			t.Fatalf("event %d: block %+v want call name %q", i, ev.Block, w.call)
		}
	}
	// Terminal semantics.
	if events[0].ID != "chatcmpl-t9" || events[0].Role != domain.RoleAssistant {
		t.Fatalf("message_start: %+v", events[0])
	}
	if events[len(events)-3].FinishReason != domain.FinishToolCalls {
		t.Fatalf("message_delta finish: %+v", events[len(events)-3])
	}
	if u := events[len(events)-2].Usage; u == nil || u.InputTokens != 15 || u.OutputTokens != 9 || u.TotalTokens != 24 {
		t.Fatalf("message_delta usage: %+v", events[len(events)-2].Usage)
	}
}

// TestStreamAssembledPayload reassembles the canonical events into a
// response and checks the assembled tool arguments.
func TestStreamAssembledPayload(t *testing.T) {
	events, _ := decodeStreamFixture(t, "stream_toolcall.sse")
	var args, text string
	for _, ev := range events {
		switch ev.Type {
		case domain.EventBlockDelta:
			text += ev.TextDelta
			args += ev.ArgumentsDelta
		}
	}
	if text != "Let me check." {
		t.Fatalf("assembled text: %q", text)
	}
	if args != `{"city":"SF"}` {
		t.Fatalf("assembled args: %q", args)
	}
}

// TestDecodeTolerantForms covers real-world wire variants that must decode
// (byte-stability is not required for these — conformance is semantic).
func TestDecodeTolerantForms(t *testing.T) {
	// stop as bare string
	req, err := DecodeRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stop":"END"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Sampling.StopSequences) != 1 || req.Sampling.StopSequences[0] != "END" {
		t.Fatalf("string stop: %+v", req.Sampling.StopSequences)
	}

	// developer role maps to system
	req, err = DecodeRequest([]byte(`{"model":"m","messages":[{"role":"developer","content":"be brief"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Messages[0].Role != domain.RoleSystem {
		t.Fatalf("developer role: %s", req.Messages[0].Role)
	}

	// non-data image URLs survive as URL-only image content
	req, err = DecodeRequest([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/cat.png"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	img := req.Messages[0].Content[0].Image
	if img == nil || img.URL != "https://x/cat.png" || img.Base64 != "" {
		t.Fatalf("url image: %+v", img)
	}

	// assistant message with content null + no tool calls
	req, err = DecodeRequest([]byte(`{"model":"m","messages":[{"role":"assistant","content":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages[0].Content) != 0 {
		t.Fatalf("null content: %+v", req.Messages[0].Content)
	}

	// string tool_choice
	req, err = DecodeRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tool_choice":"required"}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.ToolChoice.Mode != domain.ToolChoiceRequired {
		t.Fatalf("tool_choice: %+v", req.ToolChoice)
	}
}

// TestDecodeLegacyFunctions folds the deprecated shape into canonical
// tools (mapping doc §1.1).
func TestDecodeLegacyFunctions(t *testing.T) {
	body := []byte(`{
                "model": "gpt-4-turbo",
                "messages": [
                        {"role": "user", "content": "weather?"},
                        {"role": "assistant", "content": null, "function_call": {"name": "get_weather", "arguments": "{\"city\":\"SF\"}"}},
                        {"role": "function", "name": "get_weather", "content": "Sunny"}
                ],
                "functions": [{"name": "get_weather", "parameters": {"type": "object"}}],
                "function_call": {"name": "get_weather"}
        }`)
	req, err := DecodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("legacy tools: %+v", req.Tools)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != domain.ToolChoiceNamed || req.ToolChoice.Name != "get_weather" {
		t.Fatalf("legacy choice: %+v", req.ToolChoice)
	}
	call := req.Messages[1].Content[0].Call
	if call == nil || call.Name != "get_weather" || call.Arguments != `{"city":"SF"}` {
		t.Fatalf("legacy call: %+v", req.Messages[1])
	}
	tr := req.Messages[2].Content[0].Tool
	if tr == nil || tr.CallID != "get_weather" || tr.Content[0].Text != "Sunny" {
		t.Fatalf("legacy result: %+v", req.Messages[2])
	}
}

func TestErrors(t *testing.T) {
	// Round-trip: rate limit.
	body := load(t, "error_ratelimit.json")
	ge := DecodeError(body, 429)
	if ge.Type != domain.ErrRateLimit || ge.Status != 429 || !ge.Retryable || ge.Code != "rate_limit_exceeded" {
		t.Fatalf("rate limit decode: %+v", ge)
	}
	re, status := EncodeError(ge)
	if status != 429 {
		t.Fatalf("encode status: %d", status)
	}
	if msg := golden.Diff(body, re); msg != "" {
		t.Fatal(msg)
	}

	// Round-trip: invalid request with param.
	body = load(t, "error_invalid.json")
	ge = DecodeError(body, 400)
	if ge.Type != domain.ErrInvalidRequest || ge.Param != "model" || ge.Retryable {
		t.Fatalf("invalid decode: %+v", ge)
	}
	re, _ = EncodeError(ge)
	if msg := golden.Diff(body, re); msg != "" {
		t.Fatal(msg)
	}

	// Non-JSON body → status classification.
	ge = DecodeError([]byte("<html>Bad Gateway</html>"), 502)
	if ge.Type != domain.ErrAPI || !ge.Retryable || ge.Status != 502 {
		t.Fatalf("html error: %+v", ge)
	}

	// Authentication by status + code.
	ge = DecodeError([]byte(`{"error":{"message":"Incorrect API key","code":"invalid_api_key"}}`), 401)
	if ge.Type != domain.ErrAuthentication || ge.Retryable {
		t.Fatalf("auth error: %+v", ge)
	}

	// Azure quota subtype.
	ge = DecodeError([]byte(`{"error":{"message":"quota exceeded","type":"tokens","code":"429"}}`), 429)
	if ge.Type != domain.ErrRateLimit {
		t.Fatalf("azure quota: %+v", ge)
	}
}

func TestFinishReasonTable(t *testing.T) {
	cases := map[string]domain.FinishReason{
		"stop":               domain.FinishStop,
		"length":             domain.FinishLength,
		"tool_calls":         domain.FinishToolCalls,
		"function_call":      domain.FinishToolCalls,
		"content_filter":     domain.FinishContentFilter,
		"weird_future_value": domain.FinishStop,
	}
	for in, want := range cases {
		if got := DecodeFinishReason(in); got != want {
			t.Errorf("decode %q: %s want %s", in, got, want)
		}
	}
	enc := map[domain.FinishReason]string{
		domain.FinishStop:          "stop",
		domain.FinishRefusal:       "stop",
		domain.FinishLength:        "length",
		domain.FinishToolCalls:     "tool_calls",
		domain.FinishSafety:        "content_filter",
		domain.FinishRecitation:    "content_filter",
		domain.FinishContentFilter: "content_filter",
	}
	for in, want := range enc {
		if got := EncodeFinishReason(in); got != want {
			t.Errorf("encode %s: %s want %s", in, got, want)
		}
	}
}
