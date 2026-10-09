package openai

import (
	"encoding/json"
	"fmt"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// StreamDecoder translates OpenAI chat.completion.chunk payloads into
// canonical stream events. OpenAI has no explicit block framing, so the
// decoder tracks open blocks and synthesizes block_start/block_stop
// (mapping doc §1.3).
//
// Usage: feed each SSE data payload (not "[DONE]") to Decode; the caller
// handles the SSE framing and the [DONE] sentinel.
type StreamDecoder struct {
	started   bool
	createdMS int64

	openIndex int                     // currently open block, -1 = none
	openType  domain.ContentBlockType //
	nextBlock int                     // next canonical index to assign
	toolIdx   map[int]int             // OpenAI tool_calls[].index → canonical block index
	finished  bool
	usageSent bool
}

// NewStreamDecoder returns a decoder in initial state.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{openIndex: -1, toolIdx: map[int]int{}}
}

// CreatedMS reports the created timestamp captured from the first chunk.
func (d *StreamDecoder) CreatedMS() int64 { return d.createdMS }

// CreatedAtMS satisfies stream.CreatedAtMS: the created timestamp captured
// from the first chunk (unix ms), for encoder seeding.
func (d *StreamDecoder) CreatedAtMS() int64 { return d.createdMS }

// Decode converts one chunk payload into zero or more canonical events.
func (d *StreamDecoder) Decode(data []byte) ([]domain.StreamEvent, error) {
	var chunk chatChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil, fmt.Errorf("openai: bad stream chunk: %w", err)
	}

	var events []domain.StreamEvent
	if !d.started {
		d.started = true
		d.createdMS = chunk.Created * 1000
		events = append(events, domain.StreamEvent{
			Type: domain.EventMessageStart, ID: chunk.ID, Model: chunk.Model, Role: domain.RoleAssistant,
		})
	}

	for _, ch := range chunk.Choices {
		// Reasoning (thinking) deltas — DeepSeek-style field.
		if ch.Delta.ReasoningContent != "" {
			if d.openType != domain.BlockThinking {
				d.closeOpen(&events)
				events = append(events, domain.StreamEvent{
					Type: domain.EventBlockStart, Index: d.nextBlock,
					Block: &domain.ContentBlock{Type: domain.BlockThinking},
				})
				d.openIndex = d.nextBlock
				d.nextBlock++
				d.openType = domain.BlockThinking
			}
			events = append(events, domain.StreamEvent{
				Type: domain.EventBlockDelta, Index: d.openIndex, ThinkingDelta: ch.Delta.ReasoningContent,
			})
		}

		// Text deltas.
		if ch.Delta.Content != "" {
			if d.openType != domain.BlockText {
				d.closeOpen(&events)
				events = append(events, domain.StreamEvent{
					Type: domain.EventBlockStart, Index: d.nextBlock,
					Block: &domain.ContentBlock{Type: domain.BlockText},
				})
				d.openIndex = d.nextBlock
				d.nextBlock++
				d.openType = domain.BlockText
			}
			events = append(events, domain.StreamEvent{
				Type: domain.EventBlockDelta, Index: d.openIndex, TextDelta: ch.Delta.Content,
			})
		}

		// Tool-call deltas.
		for _, tc := range ch.Delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			blockIdx, known := d.toolIdx[idx]
			if !known {
				d.closeOpen(&events)
				blockIdx = d.nextBlock
				d.nextBlock++
				d.toolIdx[idx] = blockIdx
				name := ""
				if tc.Function != nil {
					name = tc.Function.Name
				}
				events = append(events, domain.StreamEvent{
					Type:  domain.EventBlockStart,
					Index: blockIdx,
					Block: &domain.ContentBlock{Type: domain.BlockToolCall, Call: &domain.ToolCall{ID: tc.ID, Name: name}},
				})
			}
			if d.openIndex != blockIdx {
				d.closeOpen(&events)
				d.openIndex = blockIdx
				d.openType = domain.BlockToolCall
			}
			if tc.Function != nil && tc.Function.Arguments != "" {
				events = append(events, domain.StreamEvent{
					Type: domain.EventBlockDelta, Index: blockIdx, ArgumentsDelta: tc.Function.Arguments,
				})
			}
		}

		// Terminal finish_reason.
		if ch.FinishReason != nil && !d.finished {
			d.closeOpen(&events)
			ev := domain.StreamEvent{
				Type:         domain.EventMessageDelta,
				FinishReason: DecodeFinishReason(*ch.FinishReason),
			}
			if chunk.Usage != nil {
				ev.Usage = usagePtr(DecodeUsage(chunk.Usage))
				d.usageSent = true
			}
			events = append(events, ev)
			d.finished = true
		}
	}

	// include_usage terminal chunk: empty choices + usage
	// [quirk:openai-stream-usage].
	if chunk.Usage != nil && !d.usageSent {
		events = append(events, domain.StreamEvent{
			Type:  domain.EventMessageDelta,
			Usage: usagePtr(DecodeUsage(chunk.Usage)),
		})
		d.usageSent = true
	}

	return events, nil
}

func (d *StreamDecoder) closeOpen(events *[]domain.StreamEvent) {
	if d.openIndex >= 0 {
		*events = append(*events, domain.StreamEvent{Type: domain.EventBlockStop, Index: d.openIndex})
		d.openIndex = -1
		d.openType = ""
	}
}

// Finish terminates the stream: it closes any open block and emits
// message_stop. The caller invokes it on [DONE] or upstream EOF.
func (d *StreamDecoder) Finish() []domain.StreamEvent {
	var events []domain.StreamEvent
	d.closeOpen(&events)
	events = append(events, domain.StreamEvent{Type: domain.EventMessageStop})
	return events
}

func usagePtr(u domain.TokenUsage) *domain.TokenUsage { return &u }

// ---------------------------------------------------------------------------
// Encoder: canonical events → OpenAI SSE frames
// ---------------------------------------------------------------------------

// StreamEncoder renders canonical events as OpenAI chat.completion.chunk
// frames. message_stop renders the [DONE] sentinel (returned as the final
// frame with done=true).
type StreamEncoder struct {
	id        string
	model     string
	createdMS int64
	toolIdx   map[int]int // canonical block index → OpenAI tool index
	nextTool  int
}

// NewStreamEncoder builds an encoder. createdMS seeds the chunks' created
// field (typically from the decoded provider stream).
func NewStreamEncoder(createdMS int64) *StreamEncoder {
	return &StreamEncoder{createdMS: createdMS, toolIdx: map[int]int{}}
}

// Encode renders one canonical event as zero or more frames. done=true
// means the stream is terminated ([DONE] was the last returned frame).
func (e *StreamEncoder) Encode(ev domain.StreamEvent) (frames []sse.Frame, done bool, err error) {
	switch ev.Type {
	case domain.EventMessageStart:
		e.id = ev.ID
		e.model = ev.Model
		return []sse.Frame{{Data: e.frameJSON(wireDelta{Role: "assistant"}, nil, nil)}}, false, nil

	case domain.EventBlockStart:
		if ev.Block != nil && ev.Block.Type == domain.BlockToolCall && ev.Block.Call != nil {
			idx := e.nextTool
			e.nextTool++
			e.toolIdx[ev.Index] = idx
			return []sse.Frame{{Data: e.frameJSON(wireDelta{}, &wireToolCall{
				Index:    &idx,
				ID:       ev.Block.Call.ID,
				Type:     "function",
				Function: &wireFnArg{Name: ev.Block.Call.Name, Arguments: ""},
			}, nil)}}, false, nil
		}
		// Text/thinking blocks need no framing on this protocol.
		return nil, false, nil

	case domain.EventBlockDelta:
		switch {
		case ev.ArgumentsDelta != "":
			idx, ok := e.toolIdx[ev.Index]
			if !ok {
				idx = e.nextTool
				e.nextTool++
				e.toolIdx[ev.Index] = idx
			}
			return []sse.Frame{{Data: e.frameJSON(wireDelta{}, &wireToolCall{
				Index:    &idx,
				Function: &wireFnArg{Arguments: ev.ArgumentsDelta},
			}, nil)}}, false, nil
		case ev.ThinkingDelta != "":
			return []sse.Frame{{Data: e.frameJSON(wireDelta{ReasoningContent: ev.ThinkingDelta}, nil, nil)}}, false, nil
		case ev.TextDelta != "":
			return []sse.Frame{{Data: e.frameJSON(wireDelta{Content: ev.TextDelta}, nil, nil)}}, false, nil
		default:
			return nil, false, nil
		}

	case domain.EventBlockStop:
		return nil, false, nil

	case domain.EventMessageDelta:
		var out []sse.Frame
		if ev.FinishReason != "" {
			fr := EncodeFinishReason(ev.FinishReason)
			out = append(out, sse.Frame{Data: e.frameJSON(wireDelta{}, nil, &fr)})
		}
		if ev.Usage != nil {
			out = append(out, sse.Frame{Data: e.usageFrame(*ev.Usage)})
		}
		return out, false, nil

	case domain.EventMessageStop:
		return []sse.Frame{{Data: "[DONE]"}}, true, nil

	case domain.EventPing:
		return nil, false, nil

	case domain.EventError:
		body, _ := json.Marshal(errorBody{Error: wireError{
			Message: errorMessage(ev.Error),
			Type:    errorTypeString(ev.Error),
			Code:    ev.Error.Code,
		}})
		// The error frame terminates the stream: no [DONE] sentinel after
		// an error (checklist D-3/D-8) — OmniRoute never sent one either.
		return []sse.Frame{{Data: string(body)}}, true, nil

	default:
		return nil, false, fmt.Errorf("openai: unknown stream event %q", ev.Type)
	}
}

func errorMessage(ge *domain.GatewayError) string {
	if ge == nil {
		return "stream error"
	}
	return ge.Message
}

func errorTypeString(ge *domain.GatewayError) string {
	if ge == nil || ge.Type == "" {
		return string(domain.ErrAPI)
	}
	return string(ge.Type)
}

func (e *StreamEncoder) frameJSON(delta wireDelta, toolCall *wireToolCall, finish *string) string {
	d := delta
	if toolCall != nil {
		d.ToolCalls = append(d.ToolCalls, *toolCall)
	}
	chunk := chatChunk{
		ID:      e.id,
		Object:  "chat.completion.chunk",
		Created: e.createdMS / 1000,
		Model:   e.model,
		Choices: []wireChunkChoice{{Index: 0, Delta: d, FinishReason: finish}},
	}
	b, _ := json.Marshal(chunk)
	return string(b)
}

func (e *StreamEncoder) usageFrame(u domain.TokenUsage) string {
	chunk := chatChunk{
		ID:      e.id,
		Object:  "chat.completion.chunk",
		Created: e.createdMS / 1000,
		Model:   e.model,
		Choices: []wireChunkChoice{},
		Usage:   encodeUsagePtr(u),
	}
	b, _ := json.Marshal(chunk)
	return string(b)
}
