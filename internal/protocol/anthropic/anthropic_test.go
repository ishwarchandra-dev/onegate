package anthropic

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
	if req.Model != "claude-sonnet-4-5" || !req.Stream || req.User != "user-123" {
		t.Fatalf("envelope: %+v", req)
	}
	// System folded to a leading system message.
	if req.Messages[0].Role != domain.RoleSystem || req.Messages[0].Content[0].Text != "You are a helpful assistant." {
		t.Fatalf("system: %+v", req.Messages[0])
	}
	// Assistant: thinking + text + tool_use.
	assistant := req.Messages[2].Content
	if assistant[0].Type != domain.BlockThinking || assistant[0].Text != "I should call the weather tool." || assistant[0].Signature != "sig_abc123" {
		t.Fatalf("thinking block: %+v", assistant[0])
	}
	if assistant[1].Type != domain.BlockText || assistant[1].Text != "Let me check." {
		t.Fatalf("text block: %+v", assistant[1])
	}
	if assistant[2].Call.Name != "get_weather" || assistant[2].Call.ID != "toolu_01A" || assistant[2].Call.Arguments != `{"city": "SF"}` {
		t.Fatalf("tool_use block: %+v", assistant[2])
	}
	// Tool result + text in user message.
	if req.Messages[3].Content[0].Tool.CallID != "toolu_01A" || req.Messages[3].Content[0].Tool.Content[0].Text != "Sunny, 18C" {
		t.Fatalf("tool_result: %+v", req.Messages[3].Content[0])
	}
	// Image.
	img := req.Messages[4].Content[0].Image
	if img == nil || img.MimeType != "image/png" || img.Base64 != "aGVsbG8=" {
		t.Fatalf("image: %+v", img)
	}
	// Sampling superset.
	s := req.Sampling
	if s.MaxTokens != 1024 || s.Temperature == nil || *s.Temperature != 0.2 || s.TopK == nil || *s.TopK != 64 ||
		len(s.StopSequences) != 1 || s.StopSequences[0] != "END" {
		t.Fatalf("sampling: %+v", s)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != domain.ToolChoiceAuto {
		t.Fatalf("tool choice: %+v", req.ToolChoice)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("tools: %+v", req.Tools)
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
	if resp.ID != "msg_01TOOL" || resp.FinishReason != domain.FinishToolCalls {
		t.Fatalf("envelope: %+v", resp)
	}
	if resp.Usage.InputTokens != 40 || resp.Usage.OutputTokens != 18 || resp.Usage.TotalTokens != 58 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	if resp.Provider.ProviderModel != "claude-sonnet-4-5" {
		t.Fatalf("provider: %+v", resp.Provider)
	}
	th := resp.Content[0]
	if th.Type != domain.BlockThinking || th.Signature != "sig_9" {
		t.Fatalf("thinking: %+v", th)
	}
}

// decodeStreamFixture runs a .sse fixture through the decoder.
func decodeStreamFixture(t *testing.T, name string) (events []domain.StreamEvent, frames []sse.Frame) {
	t.Helper()
	frames = sse.SplitFrames(load(t, name))
	d := NewStreamDecoder()
	for _, f := range frames {
		evs, err := d.Decode([]byte(f.Data))
		if err != nil {
			t.Fatalf("decode %s: %v", f.Data, err)
		}
		events = append(events, evs...)
	}
	return events, frames
}

// TestStreamRoundTrip: native SSE → canonical → native must reproduce the
// fixture frame-by-frame, preserving event names.
func TestStreamRoundTrip(t *testing.T) {
	for _, name := range []string{"stream_tooluse.sse", "stream_thinking.sse"} {
		t.Run(name, func(t *testing.T) {
			events, frames := decodeStreamFixture(t, name)
			e := NewStreamEncoder()
			var got []sse.Frame
			for _, ev := range events {
				f, done, err := e.Encode(ev)
				if err != nil {
					t.Fatalf("encode %s: %v", ev.Type, err)
				}
				got = append(got, f)
				if done {
					break
				}
			}
			if len(got) != len(frames) {
				t.Fatalf("frame count: want %d got %d", len(frames), len(got))
			}
			for i := range frames {
				if frames[i].Event != got[i].Event {
					t.Fatalf("frame %d: event name %q want %q", i, got[i].Event, frames[i].Event)
				}
				if msg := golden.Diff([]byte(frames[i].Data), []byte(got[i].Data)); msg != "" {
					t.Fatalf("frame %d (%s): %s\nwant: %s", i, frames[i].Event, msg, frames[i].Data)
				}
			}
		})
	}
}

// TestStreamEventByEvent pins the tool-use stream event sequence exactly
// (acceptance: message_start / content_block_delta / message_delta order
// preserved, event by event).
func TestStreamEventByEvent(t *testing.T) {
	events, _ := decodeStreamFixture(t, "stream_tooluse.sse")

	expected := []struct {
		typ   domain.StreamEventType
		index int
		text  string
		think string
		args  string
	}{
		{typ: domain.EventMessageStart},
		{typ: domain.EventPing},
		{typ: domain.EventBlockStart, index: 0},
		{typ: domain.EventBlockDelta, index: 0, text: "Let me check."},
		{typ: domain.EventBlockStop, index: 0},
		{typ: domain.EventBlockStart, index: 1},
		{typ: domain.EventBlockDelta, index: 1, args: "{\"city\""},
		{typ: domain.EventBlockDelta, index: 1, args: ":\"SF\"}"},
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
			t.Fatalf("event %d: got %+v want %+v", i, ev, w)
		}
	}
	// message_start carries early usage (input tokens).
	if events[0].Usage == nil || events[0].Usage.InputTokens != 25 || events[0].Usage.CacheReadTokens != 10 {
		t.Fatalf("message_start usage: %+v", events[0].Usage)
	}
	// message_delta carries merged final usage.
	final := events[len(events)-2].Usage
	if final == nil || final.InputTokens != 25 || final.OutputTokens != 15 || final.TotalTokens != 40 || final.CacheReadTokens != 10 {
		t.Fatalf("final usage: %+v", final)
	}
	if events[len(events)-2].FinishReason != domain.FinishToolCalls {
		t.Fatalf("finish: %+v", events[len(events)-2])
	}
}

// TestStreamThinkingEventByEvent checks thinking deltas and signatures.
func TestStreamThinkingEventByEvent(t *testing.T) {
	events, _ := decodeStreamFixture(t, "stream_thinking.sse")
	var thinking, signature, text string
	blockTypes := map[int]domain.ContentBlockType{}
	for _, ev := range events {
		switch ev.Type {
		case domain.EventBlockStart:
			blockTypes[ev.Index] = ev.Block.Type
		case domain.EventBlockDelta:
			thinking += ev.ThinkingDelta
			signature += ev.SignatureDelta
			text += ev.TextDelta
			if ev.ThinkingDelta != "" && blockTypes[ev.Index] != domain.BlockThinking {
				t.Fatalf("thinking delta on non-thinking block %d", ev.Index)
			}
		}
	}
	if thinking != "Reasoning..." || signature != "sig_123" || text != "The answer is 42." {
		t.Fatalf("assembled: think=%q sig=%q text=%q", thinking, signature, text)
	}
}

func TestErrors(t *testing.T) {
	// Overloaded (529).
	body := load(t, "error_overloaded.json")
	ge := DecodeError(body, 529)
	if ge.Type != domain.ErrOverloaded || ge.Status != 529 || !ge.Retryable {
		t.Fatalf("overloaded: %+v", ge)
	}
	re, status := EncodeError(ge)
	if status != 529 {
		t.Fatalf("status: %d", status)
	}
	if msg := golden.Diff(body, re); msg != "" {
		t.Fatal(msg)
	}

	// Invalid request (400).
	body = load(t, "error_invalid.json")
	ge = DecodeError(body, 400)
	if ge.Type != domain.ErrInvalidRequest || ge.Retryable {
		t.Fatalf("invalid: %+v", ge)
	}
	re, _ = EncodeError(ge)
	if msg := golden.Diff(body, re); msg != "" {
		t.Fatal(msg)
	}

	// Rate limit by status only.
	ge = DecodeError([]byte(`{"type":"error","error":{"message":"slow down"}}`), 429)
	if ge.Type != domain.ErrRateLimit || !ge.Retryable {
		t.Fatalf("rate limit: %+v", ge)
	}
}

func TestDecodeTolerantForms(t *testing.T) {
	// tool_result content as bare string.
	req, err := DecodeRequest([]byte(`{
		"model":"m","max_tokens":10,
		"messages":[
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}
		]}`))
	if err != nil {
		t.Fatal(err)
	}
	tr := req.Messages[0].Content[0].Tool
	if tr.CallID != "t1" || tr.Content[0].Text != "ok" {
		t.Fatalf("string tool_result: %+v", tr)
	}

	// System as block array.
	req, err = DecodeRequest([]byte(`{
		"model":"m","max_tokens":10,
		"system":[{"type":"text","text":"be brief"}],
		"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Messages[0].Role != domain.RoleSystem || req.Messages[0].Content[0].Text != "be brief" {
		t.Fatalf("system blocks: %+v", req.Messages[0])
	}

	// tool_choice any → required; none accepted.
	req, err = DecodeRequest([]byte(`{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"x","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"any"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.ToolChoice.Mode != domain.ToolChoiceRequired {
		t.Fatalf("any: %+v", req.ToolChoice)
	}

	// Canonical "none" encode drops tools entirely.
	req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceNone}
	body, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" || contains(string(body), `"tools"`) {
		t.Fatalf("none should drop tools: %s", body)
	}

	// stop_reason pause_turn decodes to stop.
	if DecodeFinishReason("pause_turn") != domain.FinishStop {
		t.Fatal("pause_turn")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
