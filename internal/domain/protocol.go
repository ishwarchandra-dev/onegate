// Canonical protocol schema for OneGate.
//
// This file defines the request/response/event vocabulary that every
// protocol adapter (internal/protocol/*) translates to and from. It is a
// deliberate superset of the OpenAI Chat Completions, Anthropic Messages,
// and Google Gemini generateContent surfaces, designed so that:
//
//   - Anything a client of any of the three protocols can express can be
//     represented canonically (see ADR 004 for the canonical-vs-native
//     routing decision and the documented lossy mappings).
//   - Anything canonical can be rendered back into any of the three wire
//     formats, so the gateway can cross-translate (client protocol A in,
//     provider protocol B out).
//   - The types carry zero behavior: they are data plus small pure helpers.
//
// The per-protocol field-mapping tables live in docs/protocol-mappings.md.
// Deliberate exclusions (n>1 choices, audio blocks, Anthropic 5m/1h cache
// TTL split, OpenAI legacy function calling, Gemini inline request blobs
// beyond the unified schema) are recorded there with rationale.
//
// Package rules (inherited from domain.go): stdlib-only imports, snake_case
// JSON tags, Unix-millisecond times, never floats for money.
package domain

import "encoding/json"

// ---------------------------------------------------------------------------
// Messages and content blocks
// ---------------------------------------------------------------------------

// Role is the author of a message or block.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // reserved for tool results in OpenAI layouts
)

// ContentBlockType discriminates the union in ContentBlock.
type ContentBlockType string

const (
	BlockText       ContentBlockType = "text"
	BlockImage      ContentBlockType = "image"
	BlockToolCall   ContentBlockType = "tool_call"
	BlockToolResult ContentBlockType = "tool_result"
	BlockThinking   ContentBlockType = "thinking" // Anthropic extended thinking / Gemini thought summaries
)

// ContentBlock is one block of message content. Exactly one arm of the
// union is populated, keyed by Type.
type ContentBlock struct {
	Type ContentBlockType `json:"type"`

	// Text carries BlockText payloads and BlockThinking summaries.
	Text string `json:"text,omitempty"`

	// Signature carries the cryptographic signature Anthropic attaches to
	// thinking blocks (needed to round-trip them back to Anthropic).
	Signature string `json:"signature,omitempty"`

	// Redacted carries the opaque payload of an Anthropic redacted_thinking
	// block (BlockThinking with empty Text and this field set).
	Redacted string `json:"redacted,omitempty"`

	Image *ImageContent `json:"image,omitempty"` // BlockImage
	Call  *ToolCall     `json:"call,omitempty"`  // BlockToolCall
	Tool  *ToolResult   `json:"tool,omitempty"`  // BlockToolResult
}

// ImageContent is a multimodal image. Providers accept either inline
// base64 or a URL; the canonical form carries both and lets each adapter
// pick the representation its wire format supports.
type ImageContent struct {
	MimeType string `json:"mime_type,omitempty"`
	Base64   string `json:"base64,omitempty"`
	URL      string `json:"url,omitempty"`
	// Detail is the OpenAI fidelity hint: "auto", "low", or "high".
	// Dropped by adapters whose protocol has no equivalent.
	Detail string `json:"detail,omitempty"`
}

// ToolCall is an assistant-requested tool invocation. Arguments are the
// raw JSON object serialized as a string, preserved verbatim so no
// re-marshalling perturbs provider output (byte-stable goldens).
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments is a JSON object encoded as a string ("" is invalid; use
	// "{}" for no arguments).
	Arguments string `json:"arguments"`
}

// ToolResult is the outcome of a ToolCall, carried in a user message.
type ToolResult struct {
	CallID  string         `json:"call_id"`
	Name    string         `json:"name,omitempty"`     // function name (Gemini functionResponse requires it; resolved by adapters)
	Content []ContentBlock `json:"content,omitempty"`  // text/image blocks
	IsError bool           `json:"is_error,omitempty"` // Anthropic semantics
}

// Message is one conversational turn. System prompts stay inside Messages
// with RoleSystem (superset: OpenAI/Gemini have no separate system slot,
// and Anthropic's single system param is derived by concatenation — see
// the mapping tables for that lossy fold).
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content,omitempty"`
	// Name is the participant name (OpenAI "name" field, Gemini author).
	Name string `json:"name,omitempty"`
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

// Tool is a callable the model may invoke. InputSchema is the raw JSON
// Schema object, preserved verbatim.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ToolChoiceMode selects how the model treats tools.
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceNamed    ToolChoiceMode = "named" // force the tool in Name
)

// ToolChoice is the superset of OpenAI tool_choice, Anthropic tool_choice,
// and Gemini functionCallingConfig modes.
type ToolChoice struct {
	Mode ToolChoiceMode `json:"mode"`
	Name string         `json:"name,omitempty"` // when Mode == ToolChoiceNamed
}

// ---------------------------------------------------------------------------
// Sampling and request options
// ---------------------------------------------------------------------------

// ResponseFormat constrains output structure. JSONSchema is the OpenAI
// json_schema strict mode; Anthropic and Gemini adapters drop what they
// cannot express (see mapping tables).
type ResponseFormat struct {
	Type string `json:"type"` // "text" | "json_object" | "json_schema"
	// JSONSchema is the raw schema object when Type == "json_schema".
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}

// SamplingParams is the superset of generation controls. Floats and Seed
// are pointers so "unset" (nil) is distinguishable from an explicit zero,
// which providers treat differently (e.g. temperature 0 = greedy).
type SamplingParams struct {
	MaxTokens     int64    `json:"max_tokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	TopK          *int64   `json:"top_k,omitempty"` // Anthropic/Gemini only
	StopSequences []string `json:"stop_sequences,omitempty"`
	// Seed is the deterministic-sampling hint (OpenAI/Gemini; dropped for
	// Anthropic).
	Seed *int64 `json:"seed,omitempty"`
	// LogitBias maps token IDs (string keys, as OpenAI does) or
	// single-token strings to bias values in [-100, 100]. Gemini adapters
	// approximate via logprobs? — no: dropped; see mapping tables.
	LogitBias      map[string]int  `json:"logit_bias,omitempty"`
	Logprobs       bool            `json:"logprobs,omitempty"`
	TopLogprobs    int             `json:"top_logprobs,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// Request is a canonical chat request: the superset of OpenAI
// /v1/chat/completions, Anthropic /v1/messages, and Gemini
// generateContent bodies.
type Request struct {
	Model      string         `json:"model"`
	Messages   []Message      `json:"messages"`
	Tools      []Tool         `json:"tools,omitempty"`
	ToolChoice *ToolChoice    `json:"tool_choice,omitempty"`
	Stream     bool           `json:"stream,omitempty"`
	Sampling   SamplingParams `json:"sampling"`
	// User is the end-user identifier (OpenAI "user", Anthropic metadata
	// user_id). Used for abuse tracking, never for auth.
	User string `json:"user,omitempty"`
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

// TokenUsage is the per-request token accounting superset. TotalTokens is
// derived as Input+Output when a provider does not report it. Storage
// rollups reuse the storage-facing Usage type in domain.go; this type is
// the protocol-facing shape.
type TokenUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"`   // OpenAI reasoning_tokens / Gemini thoughts_token_count / Anthropic thinking
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`  // prompt cached tokens
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"` // Anthropic cache_creation / Gemini cache write
}

// Add returns the element-wise sum of two usage values (used by stream
// pipelines that accumulate usage across fallback attempts or chunks).
func (u TokenUsage) Add(o TokenUsage) TokenUsage {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.TotalTokens += o.TotalTokens
	u.ReasoningTokens += o.ReasoningTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
	return u
}

// WithTotalDerivation fills TotalTokens when it is zero.
func (u TokenUsage) WithTotalDerivation() TokenUsage {
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	return u
}

// ---------------------------------------------------------------------------
// Finish reasons and responses
// ---------------------------------------------------------------------------

// FinishReason is the canonical terminal cause. Adapters map native
// reasons into this taxonomy; unmapped values fall to FinishOther and the
// native string is preserved in Response.ProviderMeta.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	FinishSafety        FinishReason = "safety"     // Gemini SAFETY/BLOCKLIST/..., refusals
	FinishRecitation    FinishReason = "recitation" // Gemini RECITATION
	FinishRefusal       FinishReason = "refusal"    // Anthropic "refusal" stop reason
	FinishOther         FinishReason = "other"
)

// ProviderMeta records where and how a response was served. Native fields
// are passthrough strings for diagnostics only — never typed provider
// structures (layering rule: provider types never escape internal/protocol).
type ProviderMeta struct {
	ProviderID    string `json:"provider_id,omitempty"`
	ProviderModel string `json:"provider_model,omitempty"`
	// NativeFinishReason is the provider's own stop reason, e.g. "MALFORMED_FUNCTION_CALL".
	NativeFinishReason string `json:"native_finish_reason,omitempty"`
}

// Response is a canonical non-streaming chat response.
type Response struct {
	ID           string         `json:"id"`
	Model        string         `json:"model"` // canonical model ID served
	Role         Role           `json:"role"`  // always assistant
	Content      []ContentBlock `json:"content,omitempty"`
	FinishReason FinishReason   `json:"finish_reason"`
	Usage        TokenUsage     `json:"usage"`
	CreatedMS    int64          `json:"created_ms"`
	Provider     ProviderMeta   `json:"provider,omitempty"`
}

// ---------------------------------------------------------------------------
// Streaming events
// ---------------------------------------------------------------------------

// StreamEventType is the canonical event taxonomy, modeled on the most
// expressive native protocol (Anthropic SSE) but protocol-neutral. Every
// native stream translates into exactly this sequence:
//
//	message_start (block_start block_delta* block_stop)* message_delta message_stop
//
// ping and error may appear anywhere. See docs/protocol-mappings.md for
// the per-protocol event mapping tables.
type StreamEventType string

const (
	EventMessageStart StreamEventType = "message_start"
	EventBlockStart   StreamEventType = "block_start"
	EventBlockDelta   StreamEventType = "block_delta"
	EventBlockStop    StreamEventType = "block_stop"
	EventMessageDelta StreamEventType = "message_delta"
	EventMessageStop  StreamEventType = "message_stop"
	EventPing         StreamEventType = "ping"
	EventError        StreamEventType = "error"
)

// StreamEvent is one canonical streaming event. Which fields are
// meaningful depends on Type:
//
//	message_start: ID, Model, Role, Usage (when the native protocol
//	               reports early usage — Anthropic does)
//	block_start:   Index, Block (skeleton: type, IDs; empty payload)
//	block_delta:   Index, TextDelta (body text), ThinkingDelta
//	               (reasoning), ArgumentsDelta (tool), SignatureDelta
//	               (thinking signature)
//	block_stop:    Index
//	message_delta: FinishReason, Usage (final, cumulative)
//	message_stop:  —
//	ping:          —
//	error:         Error
type StreamEvent struct {
	Type StreamEventType `json:"type"`

	ID    string `json:"id,omitempty"`    // message_start
	Model string `json:"model,omitempty"` // message_start
	Role  Role   `json:"role,omitempty"`  // message_start

	Index int           `json:"index,omitempty"` // block_* events
	Block *ContentBlock `json:"block,omitempty"`

	TextDelta      string `json:"text_delta,omitempty"`      // body-text append
	ThinkingDelta  string `json:"thinking_delta,omitempty"`  // reasoning-text append (Anthropic thinking_delta / OpenAI reasoning_content / Gemini thought parts)
	ArgumentsDelta string `json:"arguments_delta,omitempty"` // tool arguments append
	SignatureDelta string `json:"signature_delta,omitempty"` // thinking signature append

	FinishReason FinishReason `json:"finish_reason,omitempty"` // message_delta
	Usage        *TokenUsage  `json:"usage,omitempty"`         // message_delta

	Error *GatewayError `json:"error,omitempty"` // error
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrorType is the canonical error taxonomy, aligned with the Anthropic
// error envelope (the most granular of the three) plus gateway-internal
// causes. Adapters map native errors into this set and back out when
// rendering client-facing envelopes.
type ErrorType string

const (
	ErrInvalidRequest ErrorType = "invalid_request_error"
	ErrAuthentication ErrorType = "authentication_error"
	ErrPermission     ErrorType = "permission_error"
	ErrNotFound       ErrorType = "not_found_error"
	ErrTooLarge       ErrorType = "request_too_large"
	ErrRateLimit      ErrorType = "rate_limit_error"
	ErrOverloaded     ErrorType = "overloaded_error" // Anthropic overloaded_error, Gemini 503 UNAVAILABLE "overloaded"
	ErrAPI            ErrorType = "api_error"        // provider 5xx with no finer class
	ErrTimeout        ErrorType = "timeout_error"    // gateway-side deadline
	ErrCancelled      ErrorType = "cancelled_error"  // client disconnected
	ErrInternal       ErrorType = "internal_error"   // OneGate bug
)

// GatewayError is the canonical error. Status is the HTTP status the
// gateway will render to the client; adapters override it only when the
// client protocol mandates a different mapping.
type GatewayError struct {
	Status  int       `json:"status"`
	Type    ErrorType `json:"type"`
	Code    string    `json:"code,omitempty"` // machine-readable sub-code, e.g. "context_length_exceeded"
	Message string    `json:"message"`
	Param   string    `json:"param,omitempty"` // offending request field (OpenAI "param")
	// RetryAfterSec is the Retry-After hint (seconds) for rate-limit and
	// overload errors — from the provider's header or the gateway's own
	// quota budget. Renderers emit it as the HTTP Retry-After header.
	RetryAfterSec int `json:"retry_after_sec,omitempty"`
	// Retryable marks errors the fallback engine may retry on another
	// target (rate limits, overloads, transient 5xx). Never retry
	// invalid-request or authentication errors.
	Retryable bool `json:"retryable"`
}
