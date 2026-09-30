// Package conformance holds the cross-protocol translation conformance
// suite (graph node p2.cross-protocol-tests): one semantic fixture set run
// through every adapter pair, with every known lossy mapping enumerated in
// a machine-readable table (lossy.go) that mirrors docs/protocol-mappings.md.
package conformance

import (
	"fmt"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/anthropic"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/gemini"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/openai"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// Protocols enumerates the implemented wire protocols.
var Protocols = []string{"openai", "anthropic", "gemini"}

// fixedCreatedMS keeps openai stream chunks deterministic in tests.
const fixedCreatedMS = 1770000000000

// ---------------------------------------------------------------------------
// Dispatch helpers (protocol name → adapter call)
// ---------------------------------------------------------------------------

func EncodeRequest(proto string, req domain.Request) ([]byte, error) {
	switch proto {
	case "openai":
		return openai.EncodeRequest(req)
	case "anthropic":
		return anthropic.EncodeRequest(req)
	case "gemini":
		return gemini.EncodeRequest(req)
	default:
		return nil, fmt.Errorf("unknown protocol %q", proto)
	}
}

func DecodeRequest(proto string, body []byte) (domain.Request, error) {
	switch proto {
	case "openai":
		return openai.DecodeRequest(body)
	case "anthropic":
		return anthropic.DecodeRequest(body)
	case "gemini":
		return gemini.DecodeRequest(body)
	default:
		return domain.Request{}, fmt.Errorf("unknown protocol %q", proto)
	}
}

func EncodeResponse(proto string, resp domain.Response) ([]byte, error) {
	switch proto {
	case "openai":
		return openai.EncodeResponse(resp)
	case "anthropic":
		return anthropic.EncodeResponse(resp)
	case "gemini":
		return gemini.EncodeResponse(resp)
	default:
		return nil, fmt.Errorf("unknown protocol %q", proto)
	}
}

func DecodeResponse(proto string, body []byte) (domain.Response, error) {
	switch proto {
	case "openai":
		return openai.DecodeResponse(body)
	case "anthropic":
		return anthropic.DecodeResponse(body)
	case "gemini":
		return gemini.DecodeResponse(body)
	default:
		return domain.Response{}, fmt.Errorf("unknown protocol %q", proto)
	}
}

// EncodeStream renders canonical events as wire frames for a protocol.
func EncodeStream(proto string, events []domain.StreamEvent) ([]sse.Frame, error) {
	var frames []sse.Frame
	switch proto {
	case "openai":
		enc := openai.NewStreamEncoder(fixedCreatedMS)
		for _, ev := range events {
			fs, done, err := enc.Encode(ev)
			if err != nil {
				return nil, err
			}
			frames = append(frames, fs...)
			if done {
				return frames, nil
			}
		}
		return frames, nil
	case "anthropic":
		enc := anthropic.NewStreamEncoder()
		for _, ev := range events {
			f, done, err := enc.Encode(ev)
			if err != nil {
				return nil, err
			}
			frames = append(frames, f)
			if done {
				return frames, nil
			}
		}
		return frames, nil
	case "gemini":
		enc := gemini.NewStreamEncoder()
		for _, ev := range events {
			f, has, done, err := enc.Encode(ev)
			if err != nil {
				return nil, err
			}
			if has {
				frames = append(frames, f)
			}
			if done {
				return frames, nil
			}
		}
		return frames, nil
	default:
		return nil, fmt.Errorf("unknown protocol %q", proto)
	}
}

// DecodeStream parses wire frames back into canonical events, terminating
// at the protocol's natural end (openai [DONE], anthropic message_stop,
// gemini stream close).
func DecodeStream(proto string, frames []sse.Frame) ([]domain.StreamEvent, error) {
	var events []domain.StreamEvent
	switch proto {
	case "openai":
		dec := openai.NewStreamDecoder()
		for _, f := range frames {
			if sse.IsDone(f) {
				break
			}
			evs, err := dec.Decode([]byte(f.Data))
			if err != nil {
				return nil, err
			}
			events = append(events, evs...)
		}
		events = append(events, dec.Finish()...)
		return events, nil
	case "anthropic":
		dec := anthropic.NewStreamDecoder()
		for _, f := range frames {
			evs, err := dec.Decode([]byte(f.Data))
			if err != nil {
				return nil, err
			}
			events = append(events, evs...)
			if len(evs) > 0 && evs[len(evs)-1].Type == domain.EventMessageStop {
				return events, nil
			}
		}
		return events, nil
	case "gemini":
		dec := gemini.NewStreamDecoder()
		for _, f := range frames {
			evs, err := dec.Decode([]byte(f.Data))
			if err != nil {
				return nil, err
			}
			events = append(events, evs...)
		}
		events = append(events, dec.Finish()...)
		return events, nil
	default:
		return nil, fmt.Errorf("unknown protocol %q", proto)
	}
}

// ---------------------------------------------------------------------------
// Semantic fixtures (the shared set every adapter pair must agree on)
// ---------------------------------------------------------------------------

// CanonicalRequest is the kitchen-sink semantic request: system, multimodal
// user, thinking + text + tool call assistant turn, tool result, named tool
// choice, and the sampling superset representable in all three protocols.
func CanonicalRequest() domain.Request {
	temp := 0.3
	topP := 0.9
	topK := int64(64)
	seed := int64(42)
	return domain.Request{
		Model: "test-model",
		Messages: []domain.Message{
			{Role: domain.RoleSystem, Content: []domain.ContentBlock{
				{Type: domain.BlockText, Text: "You are concise."},
			}},
			{Role: domain.RoleUser, Content: []domain.ContentBlock{
				{Type: domain.BlockText, Text: "Weather in SF?"},
				{Type: domain.BlockImage, Image: &domain.ImageContent{
					MimeType: "image/png", Base64: "aGVsbG8=",
				}},
			}},
			{Role: domain.RoleAssistant, Content: []domain.ContentBlock{
				{Type: domain.BlockThinking, Text: "Use the weather tool.", Signature: "sig_1"},
				{Type: domain.BlockText, Text: "Let me check."},
				{Type: domain.BlockToolCall, Call: &domain.ToolCall{
					ID: "call_1", Name: "get_weather", Arguments: `{"city":"SF"}`,
				}},
			}},
			{Role: domain.RoleUser, Content: []domain.ContentBlock{
				{Type: domain.BlockToolResult, Tool: &domain.ToolResult{
					CallID: "call_1", Name: "get_weather",
					Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "Sunny, 18C"}},
				}},
			}},
		},
		Tools: []domain.Tool{{
			Name:        "get_weather",
			Description: "Get the current weather",
			InputSchema: []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}},
		ToolChoice: &domain.ToolChoice{Mode: domain.ToolChoiceNamed, Name: "get_weather"},
		Stream:     true,
		Sampling: domain.SamplingParams{
			MaxTokens: 1024, Temperature: &temp, TopP: &topP, TopK: &topK,
			StopSequences: []string{"END"}, Seed: &seed,
		},
	}
}

// CanonicalResponse is the semantic response fixture.
func CanonicalResponse() domain.Response {
	return domain.Response{
		ID:    "resp_conf_1",
		Role:  domain.RoleAssistant,
		Model: "test-model",
		Content: []domain.ContentBlock{
			{Type: domain.BlockThinking, Text: "Answer directly.", Signature: "sig_2"},
			{Type: domain.BlockText, Text: "It is sunny."},
			{Type: domain.BlockToolCall, Call: &domain.ToolCall{
				ID: "call_2", Name: "lookup", Arguments: `{"q":"weather"}`,
			}},
		},
		FinishReason: domain.FinishToolCalls,
		Usage: domain.TokenUsage{
			InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
			ReasoningTokens: 10, CacheReadTokens: 5,
		},
		CreatedMS: 1770000000000,
	}
}

// CanonicalStream is the semantic event sequence: text block, then a tool
// block, then terminal usage + finish.
func CanonicalStream() []domain.StreamEvent {
	usage := &domain.TokenUsage{
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
		ReasoningTokens: 10, CacheReadTokens: 5,
	}
	start := *usage
	start.OutputTokens = 1
	return []domain.StreamEvent{
		{Type: domain.EventMessageStart, ID: "resp_conf_1", Model: "test-model", Role: domain.RoleAssistant, Usage: &start},
		{Type: domain.EventBlockStart, Index: 0, Block: &domain.ContentBlock{Type: domain.BlockText}},
		{Type: domain.EventBlockDelta, Index: 0, TextDelta: "It is"},
		{Type: domain.EventBlockDelta, Index: 0, TextDelta: " sunny."},
		{Type: domain.EventBlockStop, Index: 0},
		{Type: domain.EventBlockStart, Index: 1, Block: &domain.ContentBlock{
			Type: domain.BlockToolCall, Call: &domain.ToolCall{ID: "call_3", Name: "lookup"},
		}},
		{Type: domain.EventBlockDelta, Index: 1, ArgumentsDelta: `{"q":"weather"}`},
		{Type: domain.EventBlockStop, Index: 1},
		{Type: domain.EventMessageDelta, FinishReason: domain.FinishToolCalls, Usage: usage},
		{Type: domain.EventMessageStop},
	}
}
