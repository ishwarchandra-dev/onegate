package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// StreamDecoder translates Gemini streamGenerateContent (alt=sse) chunk
// payloads into canonical events. Gemini chunks have no block framing and
// no terminator sentinel: blocks are synthesized per part, function calls
// arrive complete (block_start + one args delta + block_stop), and the
// stream simply ends (Finish supplies message_stop).
type StreamDecoder struct {
	started         bool
	openIndex       int
	openType        domain.ContentBlockType
	nextBlock       int
	sawFunctionCall bool
}

// NewStreamDecoder returns a decoder in initial state.
func NewStreamDecoder() *StreamDecoder { return &StreamDecoder{openIndex: -1} }

// Decode converts one SSE data payload into zero or more canonical events.
func (d *StreamDecoder) Decode(data []byte) ([]domain.StreamEvent, error) {
	var chunk generateResponse
	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil, fmt.Errorf("gemini: bad stream chunk: %w", err)
	}

	var events []domain.StreamEvent
	if !d.started {
		d.started = true
		events = append(events, domain.StreamEvent{
			Type:  domain.EventMessageStart,
			ID:    chunk.ResponseID,
			Model: chunk.ModelVersion,
			Role:  domain.RoleAssistant,
		})
	}

	var finish string
	var content *wireContent
	if len(chunk.Candidates) > 0 {
		finish = chunk.Candidates[0].FinishReason
		content = chunk.Candidates[0].Content
	}

	if content != nil {
		for _, p := range content.Parts {
			switch {
			case p.FunctionCall != nil:
				d.closeOpen(&events)
				id := p.FunctionCall.ID
				if id == "" {
					id = callID(p.FunctionCall.Name)
				}
				args := string(p.FunctionCall.Args)
				if args == "" || args == "null" {
					args = "{}"
				}
				events = append(events,
					domain.StreamEvent{
						Type:  domain.EventBlockStart,
						Index: d.nextBlock,
						Block: &domain.ContentBlock{Type: domain.BlockToolCall, Call: &domain.ToolCall{ID: id, Name: p.FunctionCall.Name}},
					},
					domain.StreamEvent{
						Type: domain.EventBlockDelta, Index: d.nextBlock, ArgumentsDelta: args,
					},
					domain.StreamEvent{Type: domain.EventBlockStop, Index: d.nextBlock},
				)
				d.nextBlock++
				d.sawFunctionCall = true

			case p.Thought:
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
				if p.Text != "" {
					events = append(events, domain.StreamEvent{
						Type: domain.EventBlockDelta, Index: d.openIndex, ThinkingDelta: p.Text,
					})
				}
				if p.ThoughtSignature != "" {
					events = append(events, domain.StreamEvent{
						Type: domain.EventBlockDelta, Index: d.openIndex, SignatureDelta: p.ThoughtSignature,
					})
				}

			case p.Text != "":
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
					Type: domain.EventBlockDelta, Index: d.openIndex, TextDelta: p.Text,
				})
			}
		}
	}

	if finish != "" {
		d.closeOpen(&events)
		ev := domain.StreamEvent{
			Type:         domain.EventMessageDelta,
			FinishReason: DecodeFinishReason(finish, d.sawFunctionCall),
		}
		if chunk.UsageMetadata != nil {
			u := DecodeUsage(chunk.UsageMetadata)
			ev.Usage = &u
		}
		events = append(events, ev)
	} else if chunk.UsageMetadata != nil {
		u := DecodeUsage(chunk.UsageMetadata)
		events = append(events, domain.StreamEvent{Type: domain.EventMessageDelta, Usage: &u})
	}

	return events, nil
}

// Finish terminates the stream: closes any open block and emits
// message_stop. Gemini has no [DONE] sentinel; the caller invokes Finish
// at upstream EOF.
func (d *StreamDecoder) Finish() []domain.StreamEvent {
	var events []domain.StreamEvent
	d.closeOpen(&events)
	events = append(events, domain.StreamEvent{Type: domain.EventMessageStop})
	return events
}

func (d *StreamDecoder) closeOpen(events *[]domain.StreamEvent) {
	if d.openIndex >= 0 {
		*events = append(*events, domain.StreamEvent{Type: domain.EventBlockStop, Index: d.openIndex})
		d.openIndex = -1
		d.openType = ""
	}
}

// ---------------------------------------------------------------------------
// Encoder: canonical events → Gemini SSE frames
// ---------------------------------------------------------------------------

type toolBuffer struct {
	name string
	args strings.Builder
}

// StreamEncoder renders canonical events as Gemini alt=sse frames. Tool
// argument fragments are buffered per block and flushed complete on
// block_stop (Gemini function calls never stream incrementally).
type StreamEncoder struct {
	id      string
	model   string
	toolBuf map[int]*toolBuffer
}

// NewStreamEncoder builds an encoder.
func NewStreamEncoder() *StreamEncoder { return &StreamEncoder{toolBuf: map[int]*toolBuffer{}} }

// Encode renders one canonical event as zero or one frame. done=true on
// message_stop (no wire sentinel — the stream simply closes).
func (e *StreamEncoder) Encode(ev domain.StreamEvent) (frame sse.Frame, has bool, done bool, err error) {
	switch ev.Type {
	case domain.EventMessageStart:
		e.id = ev.ID
		e.model = ev.Model
		return sse.Frame{}, false, false, nil

	case domain.EventBlockStart:
		if ev.Block != nil && ev.Block.Type == domain.BlockToolCall && ev.Block.Call != nil {
			e.toolBuf[ev.Index] = &toolBuffer{name: ev.Block.Call.Name}
		}
		return sse.Frame{}, false, false, nil

	case domain.EventBlockDelta:
		switch {
		case ev.ArgumentsDelta != "":
			buf, ok := e.toolBuf[ev.Index]
			if !ok {
				buf = &toolBuffer{}
				e.toolBuf[ev.Index] = buf
			}
			buf.args.WriteString(ev.ArgumentsDelta)
			return sse.Frame{}, false, false, nil
		case ev.SignatureDelta != "":
			return e.contentFrame([]wirePart{{ThoughtSignature: ev.SignatureDelta}}), true, false, nil
		case ev.ThinkingDelta != "":
			return e.contentFrame([]wirePart{{Thought: true, Text: ev.ThinkingDelta}}), true, false, nil
		default:
			return e.contentFrame([]wirePart{{Text: ev.TextDelta}}), true, false, nil
		}

	case domain.EventBlockStop:
		if buf, ok := e.toolBuf[ev.Index]; ok {
			delete(e.toolBuf, ev.Index)
			args := json.RawMessage(buf.args.String())
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage(`{}`)
			}
			return e.contentFrame([]wirePart{{FunctionCall: &wireFunctionCall{
				Name: buf.name, Args: args,
			}}}), true, false, nil
		}
		return sse.Frame{}, false, false, nil

	case domain.EventMessageDelta:
		chunk := generateResponse{ResponseID: e.id, ModelVersion: e.model}
		if ev.FinishReason != "" {
			chunk.Candidates = []wireCandidate{{FinishReason: EncodeFinishReason(ev.FinishReason)}}
		}
		if ev.Usage != nil {
			chunk.UsageMetadata = encodeUsagePtr(*ev.Usage)
		}
		return sse.Frame{Data: string(mustJSON(chunk))}, true, false, nil

	case domain.EventMessageStop:
		return sse.Frame{}, false, true, nil

	case domain.EventPing:
		return sse.Frame{}, false, false, nil

	case domain.EventError:
		ge := ev.Error
		msg, t := "stream error", domain.ErrAPI
		if ge != nil {
			msg = ge.Message
			if ge.Type != "" {
				t = ge.Type
			}
		}
		body, status := EncodeError(domain.GatewayError{Status: 0, Type: t, Message: msg})
		_ = status
		return sse.Frame{Data: string(body)}, true, true, nil

	default:
		return sse.Frame{}, false, false, fmt.Errorf("gemini: unknown stream event %q", ev.Type)
	}
}

func (e *StreamEncoder) contentFrame(parts []wirePart) sse.Frame {
	chunk := generateResponse{
		ResponseID:   e.id,
		ModelVersion: e.model,
		Candidates: []wireCandidate{{
			Content: &wireContent{Role: "model", Parts: parts},
		}},
	}
	return sse.Frame{Data: string(mustJSON(chunk))}
}
