package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// StreamDecoder translates Anthropic SSE payloads into canonical events.
// The native event order (message_start / content_block_start /
// content_block_delta / content_block_stop / message_delta / message_stop)
// is preserved one-to-one; the only stateful merge is usage:
// message_start carries input tokens, message_delta carries output tokens —
// the canonical message_delta reports the combined total (mapping doc §2.3).
type StreamDecoder struct {
	startInput  int64
	startCacheR int64
	startCacheW int64
}

// NewStreamDecoder returns a decoder in initial state.
func NewStreamDecoder() *StreamDecoder { return &StreamDecoder{} }

// Decode converts one SSE data payload into zero or more canonical events.
// The frame's event: name is redundant (the payload carries "type"), so
// only the data payload is needed.
func (d *StreamDecoder) Decode(data []byte) ([]domain.StreamEvent, error) {
	var ev streamEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, fmt.Errorf("anthropic: bad stream event: %w", err)
	}

	switch ev.Type {
	case "message_start":
		var msg messagesResponse
		if err := json.Unmarshal(ev.Message, &msg); err != nil {
			return nil, fmt.Errorf("anthropic: bad message_start: %w", err)
		}
		d.startInput = msg.Usage.InputTokens
		d.startCacheR = msg.Usage.CacheReadInputTokens
		d.startCacheW = msg.Usage.CacheCreationInputTokens
		usage := domain.TokenUsage{
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		}.WithTotalDerivation()
		return []domain.StreamEvent{{
			Type:  domain.EventMessageStart,
			ID:    msg.ID,
			Model: msg.Model,
			Role:  domain.RoleAssistant,
			Usage: &usage,
		}}, nil

	case "content_block_start":
		var block wireBlock
		if err := json.Unmarshal(ev.ContentBlock, &block); err != nil {
			return nil, fmt.Errorf("anthropic: bad content_block_start: %w", err)
		}
		cb, err := decodeBlock(block)
		if err != nil {
			return nil, err
		}
		// Strip payloads: the start event carries only the skeleton.
		switch cb.Type {
		case domain.BlockToolCall:
			cb.Call = &domain.ToolCall{ID: cb.Call.ID, Name: cb.Call.Name}
		case domain.BlockText, domain.BlockThinking:
			cb.Text = ""
		}
		return []domain.StreamEvent{{
			Type: domain.EventBlockStart, Index: ev.Index, Block: &cb,
		}}, nil

	case "content_block_delta":
		var delta streamDelta
		if err := json.Unmarshal(ev.Delta, &delta); err != nil {
			return nil, fmt.Errorf("anthropic: bad content_block_delta: %w", err)
		}
		out := domain.StreamEvent{Type: domain.EventBlockDelta, Index: ev.Index}
		switch delta.Type {
		case "text_delta":
			out.TextDelta = delta.Text
		case "thinking_delta":
			out.ThinkingDelta = delta.Thinking
		case "signature_delta": // [quirk:anthropic-signature-delta]
			out.SignatureDelta = delta.Signature
		case "input_json_delta":
			out.ArgumentsDelta = delta.PartialJSON
		default:
			return nil, fmt.Errorf("anthropic: unknown delta type %q", delta.Type)
		}
		return []domain.StreamEvent{out}, nil

	case "content_block_stop":
		return []domain.StreamEvent{{Type: domain.EventBlockStop, Index: ev.Index}}, nil

	case "message_delta":
		var delta streamDelta
		if err := json.Unmarshal(ev.Delta, &delta); err != nil {
			return nil, fmt.Errorf("anthropic: bad message_delta: %w", err)
		}
		out := domain.StreamEvent{Type: domain.EventMessageDelta}
		if delta.StopReason != "" {
			out.FinishReason = DecodeFinishReason(delta.StopReason)
		}
		if ev.Usage != nil {
			merged := domain.TokenUsage{
				InputTokens:      d.startInput,
				OutputTokens:     ev.Usage.OutputTokens,
				CacheReadTokens:  max64(d.startCacheR, ev.Usage.CacheReadInputTokens),
				CacheWriteTokens: max64(d.startCacheW, ev.Usage.CacheCreationInputTokens),
			}.WithTotalDerivation()
			out.Usage = &merged
		}
		return []domain.StreamEvent{out}, nil

	case "message_stop":
		return []domain.StreamEvent{{Type: domain.EventMessageStop}}, nil

	case "ping":
		return []domain.StreamEvent{{Type: domain.EventPing}}, nil

	case "error":
		if ev.Error == nil {
			return nil, fmt.Errorf("anthropic: error event without payload")
		}
		t, _ := typeFromString(ev.Error.Type)
		if t == "" {
			t = domain.ErrAPI
		}
		return []domain.StreamEvent{{Type: domain.EventError, Error: &domain.GatewayError{
			Status:  0, // unknown mid-stream; the proxy stamps 529/500 as appropriate
			Type:    t,
			Message: ev.Error.Message,
		}}}, nil

	default:
		// Unknown native events are dropped, not fatal (forward compat).
		return nil, nil
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Encoder: canonical events → Anthropic SSE frames
// ---------------------------------------------------------------------------

// StreamEncoder renders canonical events as Anthropic named-event SSE
// frames (event: <name> + data: {...}).
type StreamEncoder struct {
	id    string
	model string
}

// NewStreamEncoder builds an encoder.
func NewStreamEncoder() *StreamEncoder { return &StreamEncoder{} }

// Encode renders one canonical event as exactly one frame (Anthropic
// events map 1:1). done=true on message_stop.
func (e *StreamEncoder) Encode(ev domain.StreamEvent) (frame sse.Frame, done bool, err error) {
	switch ev.Type {
	case domain.EventMessageStart:
		e.id = ev.ID
		e.model = ev.Model
		u := domain.TokenUsage{}
		if ev.Usage != nil {
			u = *ev.Usage
		}
		payload := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": ev.ID, "type": "message", "role": "assistant", "model": ev.Model,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":                u.InputTokens,
					"cache_creation_input_tokens": u.CacheWriteTokens,
					"cache_read_input_tokens":     u.CacheReadTokens,
					"output_tokens":               u.OutputTokens,
				},
			},
		}
		return sse.Frame{Event: "message_start", Data: string(mustJSON(payload))}, false, nil

	case domain.EventBlockStart:
		name, block := "content_block_start", map[string]any{}
		if ev.Block != nil {
			switch ev.Block.Type {
			case domain.BlockText:
				block = map[string]any{"type": "text", "text": ""}
			case domain.BlockThinking:
				if ev.Block.Redacted != "" {
					block = map[string]any{"type": "redacted_thinking", "data": ev.Block.Redacted}
				} else {
					block = map[string]any{"type": "thinking", "thinking": ""}
				}
			case domain.BlockToolCall:
				block = map[string]any{
					"type": "tool_use", "id": ev.Block.Call.ID, "name": ev.Block.Call.Name, "input": map[string]any{},
				}
			case domain.BlockImage:
				block = map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": ev.Block.Image.MimeType, "data": ev.Block.Image.Base64,
				}}
			default:
				block = map[string]any{"type": string(ev.Block.Type)}
			}
		}
		return sse.Frame{
			Event: name,
			Data:  string(mustJSON(map[string]any{"type": name, "index": ev.Index, "content_block": block})),
		}, false, nil

	case domain.EventBlockDelta:
		var delta map[string]any
		switch {
		case ev.SignatureDelta != "":
			delta = map[string]any{"type": "signature_delta", "signature": ev.SignatureDelta}
		case ev.ThinkingDelta != "":
			delta = map[string]any{"type": "thinking_delta", "thinking": ev.ThinkingDelta}
		case ev.ArgumentsDelta != "":
			delta = map[string]any{"type": "input_json_delta", "partial_json": ev.ArgumentsDelta}
		default:
			delta = map[string]any{"type": "text_delta", "text": ev.TextDelta}
		}
		return sse.Frame{
			Event: "content_block_delta",
			Data: string(mustJSON(map[string]any{
				"type": "content_block_delta", "index": ev.Index, "delta": delta,
			})),
		}, false, nil

	case domain.EventBlockStop:
		return sse.Frame{
			Event: "content_block_stop",
			Data:  string(mustJSON(map[string]any{"type": "content_block_stop", "index": ev.Index})),
		}, false, nil

	case domain.EventMessageDelta:
		delta := map[string]any{"stop_reason": nil, "stop_sequence": nil}
		if ev.FinishReason != "" {
			delta["stop_reason"] = EncodeFinishReason(ev.FinishReason)
		}
		payload := map[string]any{"type": "message_delta", "delta": delta}
		if ev.Usage != nil {
			u := map[string]any{"output_tokens": ev.Usage.OutputTokens}
			if ev.Usage.CacheReadTokens > 0 {
				u["cache_read_input_tokens"] = ev.Usage.CacheReadTokens
			}
			if ev.Usage.CacheWriteTokens > 0 {
				u["cache_creation_input_tokens"] = ev.Usage.CacheWriteTokens
			}
			payload["usage"] = u
		}
		return sse.Frame{Event: "message_delta", Data: string(mustJSON(payload))}, false, nil

	case domain.EventMessageStop:
		return sse.Frame{
			Event: "message_stop",
			Data:  string(mustJSON(map[string]any{"type": "message_stop"})),
		}, true, nil

	case domain.EventPing:
		return sse.Frame{
			Event: "ping",
			Data:  string(mustJSON(map[string]any{"type": "ping"})),
		}, false, nil

	case domain.EventError:
		t := string(domain.ErrAPI)
		msg := "stream error"
		if ev.Error != nil {
			if ev.Error.Type != "" {
				t = string(ev.Error.Type)
			}
			msg = ev.Error.Message
		}
		return sse.Frame{
			Event: "error",
			Data: string(mustJSON(map[string]any{
				"type": "error", "error": map[string]any{"type": t, "message": msg},
			})),
		}, true, nil

	default:
		return sse.Frame{}, false, fmt.Errorf("anthropic: unknown stream event %q", ev.Type)
	}
}
