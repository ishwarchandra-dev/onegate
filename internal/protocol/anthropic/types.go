// Package anthropic implements the Anthropic Messages wire protocol
// (messages + streaming + errors), including system prompts, content
// blocks, tool use, and extended thinking. The mapping tables are in
// docs/protocol-mappings.md §2.
package anthropic

import "encoding/json"

// ---------------------------------------------------------------------------
// Request wire types
// ---------------------------------------------------------------------------

// messagesRequest is the Anthropic /v1/messages request body.
type messagesRequest struct {
	Model         string          `json:"model"`
	Messages      []wireMessage   `json:"messages"`
	System        json.RawMessage `json:"system,omitempty"` // string or []block
	MaxTokens     int64           `json:"max_tokens"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int64          `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Tools         []wireTool      `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"` // {"type":"auto"|"any"|"tool"|"none","name"?}
	Metadata      *wireMetadata   `json:"metadata,omitempty"`
}

type wireMessage struct {
	Role    string          `json:"role"` // "user" | "assistant"
	Content json.RawMessage `json:"content"`
}

// wireBlock is the superset content-block union. Decoding accepts any
// shape; encoding is type-driven via MarshalJSON so each block emits
// exactly the fields its type carries (byte-stable goldens).
type wireBlock struct {
	Type string `json:"type"`

	// text / thinking
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"` // redacted_thinking

	// image
	Source *wireImageSource `json:"source,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string or []block
	IsError   bool            `json:"is_error,omitempty"`
}

type wireImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type wireMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

// MarshalJSON renders exactly the fields the block type carries.
func (b wireBlock) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case "text":
		return json.Marshal(map[string]any{"type": "text", "text": b.Text})
	case "image":
		return json.Marshal(map[string]any{"type": "image", "source": b.Source})
	case "tool_use":
		input := b.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		return json.Marshal(map[string]any{
			"type": "tool_use", "id": b.ID, "name": b.Name, "input": input,
		})
	case "tool_result":
		m := map[string]any{"type": "tool_result", "tool_use_id": b.ToolUseID}
		if len(b.Content) > 0 {
			m["content"] = b.Content
		}
		if b.IsError {
			m["is_error"] = true
		}
		return json.Marshal(m)
	case "thinking":
		m := map[string]any{"type": "thinking", "thinking": b.Thinking}
		if b.Signature != "" {
			m["signature"] = b.Signature
		}
		return json.Marshal(m)
	case "redacted_thinking":
		return json.Marshal(map[string]any{"type": "redacted_thinking", "data": b.Data})
	default:
		return json.Marshal(map[string]any{"type": b.Type})
	}
}

// ---------------------------------------------------------------------------
// Response wire types
// ---------------------------------------------------------------------------

// messagesResponse is the Anthropic /v1/messages response body. It is also
// the message envelope carried inside message_start stream events.
type messagesResponse struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"` // "message"
	Role         string      `json:"role"` // "assistant"
	Model        string      `json:"model"`
	Content      []wireBlock `json:"content"`
	StopReason   *string     `json:"stop_reason"`   // null until terminal
	StopSequence *string     `json:"stop_sequence"` // null until a stop seq matched
	Usage        wireUsage   `json:"usage"`
}

type wireUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

// ---------------------------------------------------------------------------
// Stream wire types
// ---------------------------------------------------------------------------

// streamEvent is one SSE data payload. Sub-payloads stay raw and are
// decoded per event type.
type streamEvent struct {
	Type string `json:"type"`

	Message      json.RawMessage `json:"message,omitempty"`       // message_start
	Index        int             `json:"index"`                   // block_* events
	ContentBlock json.RawMessage `json:"content_block,omitempty"` // content_block_start
	Delta        json.RawMessage `json:"delta,omitempty"`         // content_block_delta | message_delta
	Usage        *streamUsage    `json:"usage,omitempty"`         // message_delta
	Error        *errorPayload   `json:"error,omitempty"`         // error
}

// streamDelta covers content_block_delta and message_delta delta shapes.
type streamDelta struct {
	Type string `json:"type"` // text_delta | input_json_delta | thinking_delta | signature_delta | ""

	Text         string  `json:"text,omitempty"`
	PartialJSON  string  `json:"partial_json,omitempty"`
	Thinking     string  `json:"thinking,omitempty"`
	Signature    string  `json:"signature,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`   // message_delta form
	StopSequence *string `json:"stop_sequence,omitempty"` // message_delta form
}

// streamUsage is the message_start/message_delta usage shape. Fields are
// omitempty: message_delta carries only output_tokens.
type streamUsage struct {
	InputTokens              int64 `json:"input_tokens,omitempty"`
	OutputTokens             int64 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
}

// ---------------------------------------------------------------------------
// Error wire types
// ---------------------------------------------------------------------------

type errorEnvelope struct {
	Type  string       `json:"type"` // "error"
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
