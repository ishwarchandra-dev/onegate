package stream

import (
	"fmt"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/anthropic"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/gemini"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/openai"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// Decoder converts provider-native SSE payload chunks into canonical
// stream events.
type Decoder interface {
	Decode(data []byte) ([]domain.StreamEvent, error)
}

// Finisher is optionally implemented by decoders that emit trailing
// canonical events upon reaching upstream EOF (e.g. Gemini, OpenAI).
type Finisher interface {
	Finish() []domain.StreamEvent
}

// Encoder converts canonical stream events into client-facing SSE frames.
type Encoder interface {
	// Encode renders one canonical event as zero or more client frames.
	// done=true signals that the stream has terminated.
	Encode(ev domain.StreamEvent) ([]sse.Frame, bool, error)
}

// NewDecoder instantiates the canonical stream decoder for the provider protocol.
func NewDecoder(protocol domain.ProviderProtocol) (Decoder, error) {
	switch protocol {
	case domain.ProtocolOpenAI:
		return openai.NewStreamDecoder(), nil
	case domain.ProtocolAnthropic:
		return anthropic.NewStreamDecoder(), nil
	case domain.ProtocolGemini:
		return gemini.NewStreamDecoder(), nil
	default:
		return nil, fmt.Errorf("stream: unsupported provider protocol %q", protocol)
	}
}

// NewEncoder instantiates the canonical stream encoder for the client protocol.
func NewEncoder(protocol domain.ProviderProtocol, createdMS int64) (Encoder, error) {
	if createdMS <= 0 {
		createdMS = time.Now().UnixMilli()
	}
	switch protocol {
	case domain.ProtocolOpenAI:
		return openai.NewStreamEncoder(createdMS), nil
	case domain.ProtocolAnthropic:
		return &anthropicEncoder{enc: anthropic.NewStreamEncoder()}, nil
	case domain.ProtocolGemini:
		return &geminiEncoder{enc: gemini.NewStreamEncoder()}, nil
	default:
		return nil, fmt.Errorf("stream: unsupported client protocol %q", protocol)
	}
}

type anthropicEncoder struct {
	enc *anthropic.StreamEncoder
}

func (a *anthropicEncoder) Encode(ev domain.StreamEvent) ([]sse.Frame, bool, error) {
	frame, done, err := a.enc.Encode(ev)
	if err != nil {
		return nil, done, err
	}
	if frame.Event == "" && frame.Data == "" {
		return nil, done, nil
	}
	return []sse.Frame{frame}, done, nil
}

type geminiEncoder struct {
	enc *gemini.StreamEncoder
}

func (g *geminiEncoder) Encode(ev domain.StreamEvent) ([]sse.Frame, bool, error) {
	frame, ok, done, err := g.enc.Encode(ev)
	if err != nil {
		return nil, done, err
	}
	if !ok {
		return nil, done, nil
	}
	return []sse.Frame{frame}, done, nil
}
