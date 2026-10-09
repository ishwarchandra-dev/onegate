package gemini

import (
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/golden"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	return readFile(t, "testdata/"+name)
}

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

func TestRequestDecodedShape(t *testing.T) {
	req, err := DecodeRequest(load(t, "request_tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 5 {
		t.Fatalf("messages: %d", len(req.Messages))
	}
	if req.Messages[0].Role != domain.RoleSystem || req.Messages[0].Content[0].Text != "You are a helpful assistant." {
		t.Fatalf("system: %+v", req.Messages[0])
	}
	// Model turn: thinking + text + function call.
	model := req.Messages[2].Content
	if model[0].Type != domain.BlockThinking || model[0].Text != "I should check the weather." || model[0].Signature != "sig_1" {
		t.Fatalf("thought part: %+v", model[0])
	}
	if model[2].Call == nil || model[2].Call.Name != "get_weather" || model[2].Call.ID != "call_get_weather" || model[2].Call.Arguments != `{"city": "SF"}` {
		t.Fatalf("function call: %+v", model[2])
	}
	// Tool result resolves name + synthesized ID.
	tr := req.Messages[3].Content[0].Tool
	if tr.CallID != "call_get_weather" || tr.Name != "get_weather" || tr.Content[0].Text != "Sunny, 18C" {
		t.Fatalf("function response: %+v", tr)
	}
	// Image.
	img := req.Messages[4].Content[0].Image
	if img == nil || img.MimeType != "image/png" || img.Base64 != "aGVsbG8=" {
		t.Fatalf("image: %+v", img)
	}
	// Named tool choice via ANY + allowedFunctionNames.
	if req.ToolChoice == nil || req.ToolChoice.Mode != domain.ToolChoiceNamed || req.ToolChoice.Name != "get_weather" {
		t.Fatalf("tool choice: %+v", req.ToolChoice)
	}
	// Sampling.
	s := req.Sampling
	if s.MaxTokens != 1024 || s.Temperature == nil || *s.Temperature != 0.2 || s.TopK == nil || *s.TopK != 64 ||
		s.Seed == nil || *s.Seed != 42 || s.ResponseFormat == nil || s.ResponseFormat.Type != "json_object" {
		t.Fatalf("sampling: %+v", s)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	for _, name := range []string{"response_text.json", "response_toolcall.json", "response_blocked.json"} {
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
	resp, err := DecodeResponse(load(t, "response_toolcall.json"))
	if err != nil {
		t.Fatal(err)
	}
	// STOP + functionCall → tool_calls (mapping doc §3.2).
	if resp.FinishReason != domain.FinishToolCalls {
		t.Fatalf("finish: %s", resp.FinishReason)
	}
	if resp.Provider.NativeFinishReason != "STOP" || resp.Provider.ProviderModel != "gemini-2.0-flash" {
		t.Fatalf("provider: %+v", resp.Provider)
	}
	if len(resp.Content) != 2 || resp.Content[1].Call.Name != "get_weather" {
		t.Fatalf("content: %+v", resp.Content)
	}
	if resp.Usage.ReasoningTokens != 6 || resp.Usage.TotalTokens != 24 {
		t.Fatalf("usage: %+v", resp.Usage)
	}

	// Blocked response → safety finish with native reason.
	blocked, err := DecodeResponse(load(t, "response_blocked.json"))
	if err != nil {
		t.Fatal(err)
	}
	if blocked.FinishReason != domain.FinishSafety || blocked.Provider.NativeFinishReason != "SAFETY" {
		t.Fatalf("blocked: %+v", blocked)
	}
}

func decodeStreamFixture(t *testing.T, name string) (events []domain.StreamEvent, frames []sse.Frame) {
	t.Helper()
	frames = sse.SplitFrames(readFile(t, "testdata/"+name))
	d := NewStreamDecoder()
	for _, f := range frames {
		evs, err := d.Decode([]byte(f.Data))
		if err != nil {
			t.Fatalf("decode %s: %v", f.Data, err)
		}
		events = append(events, evs...)
	}
	events = append(events, d.Finish()...)
	return events, frames
}

// TestStreamRoundTrip: alt=sse chunks → canonical → chunks must reproduce
// the fixture frame-by-frame. Gemini has no [DONE]: the stream just ends.
func TestStreamRoundTrip(t *testing.T) {
	for _, name := range []string{"stream_text.sse", "stream_toolcall.sse"} {
		t.Run(name, func(t *testing.T) {
			events, frames := decodeStreamFixture(t, name)
			e := NewStreamEncoder()
			var got []sse.Frame
			for _, ev := range events {
				f, has, done, err := e.Encode(ev)
				if err != nil {
					t.Fatalf("encode %s: %v", ev.Type, err)
				}
				if has {
					got = append(got, f)
				}
				if done {
					break
				}
			}
			if len(got) != len(frames) {
				t.Fatalf("frame count: want %d got %d\n%v", len(frames), len(got), got)
			}
			for i := range frames {
				if msg := golden.Diff([]byte(frames[i].Data), []byte(got[i].Data)); msg != "" {
					t.Fatalf("frame %d: %s\nwant: %s", i, msg, frames[i].Data)
				}
			}
		})
	}
}

// TestStreamEventByEvent pins the tool-call stream sequence.
func TestStreamEventByEvent(t *testing.T) {
	events, _ := decodeStreamFixture(t, "stream_toolcall.sse")
	expected := []struct {
		typ   domain.StreamEventType
		index int
		text  string
		args  string
	}{
		{typ: domain.EventMessageStart},
		{typ: domain.EventBlockStart, index: 0},
		{typ: domain.EventBlockDelta, index: 0, text: "Let me check."},
		{typ: domain.EventBlockStop, index: 0},
		{typ: domain.EventBlockStart, index: 1},
		{typ: domain.EventBlockDelta, index: 1, args: `{"city":"SF"}`},
		{typ: domain.EventBlockStop, index: 1},
		{typ: domain.EventMessageDelta},
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
		if ev.Type != w.typ || ev.Index != w.index || ev.TextDelta != w.text || ev.ArgumentsDelta != w.args {
			t.Fatalf("event %d: %+v want %+v", i, ev, w)
		}
	}
	if events[0].ID != "resp_t9" || events[0].Model != "gemini-2.0-flash" {
		t.Fatalf("message_start: %+v", events[0])
	}
	// STOP after a function call → tool_calls; usage carries thoughts.
	delta := events[len(events)-2]
	if delta.FinishReason != domain.FinishToolCalls {
		t.Fatalf("finish: %+v", delta)
	}
	if delta.Usage == nil || delta.Usage.ReasoningTokens != 6 || delta.Usage.InputTokens != 15 {
		t.Fatalf("usage: %+v", delta.Usage)
	}
}

func TestErrors(t *testing.T) {
	// Authentication via gRPC status (HTTP 400 on Gemini).
	body := load(t, "error_auth.json")
	ge := DecodeError(body, 400)
	// Provider-side auth failures retry across targets (checklist C-16).
	if ge.Type != domain.ErrAuthentication || ge.Status != 400 || !ge.Retryable || ge.Code != "UNAUTHENTICATED" {
		t.Fatalf("auth: %+v", ge)
	}
	re, status := EncodeError(ge)
	if status != 400 {
		t.Fatalf("status: %d", status)
	}
	if msg := golden.Diff(body, re); msg != "" {
		t.Fatal(msg)
	}

	// Quota (429 RESOURCE_EXHAUSTED).
	body = load(t, "error_quota.json")
	ge = DecodeError(body, 429)
	if ge.Type != domain.ErrRateLimit || !ge.Retryable {
		t.Fatalf("quota: %+v", ge)
	}
	re, _ = EncodeError(ge)
	if msg := golden.Diff(body, re); msg != "" {
		t.Fatal(msg)
	}

	// Overloaded (503 UNAVAILABLE — "The model is overloaded").
	ge = DecodeError([]byte(`{"error":{"code":503,"message":"The model is overloaded","status":"UNAVAILABLE"}}`), 503)
	if ge.Type != domain.ErrOverloaded || !ge.Retryable {
		t.Fatalf("overloaded: %+v", ge)
	}
}

func TestDecodeTolerantForms(t *testing.T) {
	// parametersJsonSchema accepted alongside parameters.
	req, err := DecodeRequest([]byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"tools":[{"functionDeclarations":[{"name":"t","parametersJsonSchema":{"type":"object"}}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Tools[0].Name != "t" || string(req.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("parametersJsonSchema: %+v", req.Tools[0])
	}

	// Error tool result via {"error": "..."} response.
	req, err = DecodeRequest([]byte(`{
		"contents":[{"role":"user","parts":[
			{"functionResponse":{"name":"f","response":{"error":"disk full"}}}
		]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	tr := req.Messages[0].Content[0].Tool
	if !tr.IsError || tr.Content[0].Text != "disk full" || tr.Name != "f" {
		t.Fatalf("error tool result: %+v", tr)
	}

	// json_schema response format → responseSchema extraction.
	req = domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "x"}}}},
		Sampling: domain.SamplingParams{ResponseFormat: &domain.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: []byte(`{"name":"out","schema":{"type":"object"}}`),
		}},
	}
	body, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if msg := golden.Diff([]byte(`{
		"contents":[{"role":"user","parts":[{"text":"x"}]}],
		"generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"object"}}
	}`), body); msg != "" {
		t.Fatal(msg)
	}

	// MALFORMED_FUNCTION_CALL after a call still maps to tool_calls.
	if DecodeFinishReason("MALFORMED_FUNCTION_CALL", true) != domain.FinishToolCalls {
		t.Fatal("malformed function call")
	}
}
