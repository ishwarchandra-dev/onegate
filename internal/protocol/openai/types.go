// Package openai implements the OpenAI Chat Completions wire protocol
// (chat + streaming + errors), including the legacy `functions` request
// shape at decode time. The mapping tables are in docs/protocol-mappings.md.
package openai

import "encoding/json"

// ---------------------------------------------------------------------------
// Request wire types
// ---------------------------------------------------------------------------

// chatRequest is the OpenAI /v1/chat/completions request body.
type chatRequest struct {
	Model       string           `json:"model"`
	Messages    []wireMessage    `json:"messages"`
	Tools       []wireTool       `json:"tools,omitempty"`
	ToolChoice  json.RawMessage  `json:"tool_choice,omitempty"` // "auto" | "none" | "required" | {type:"function",function:{name}}
	Stream      bool             `json:"stream,omitempty"`
	MaxTokens   int64            `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	TopP        *float64         `json:"top_p,omitempty"`
	Stop        flexStrings      `json:"stop,omitempty"`
	Seed        *int64           `json:"seed,omitempty"`
	LogitBias   map[string]int   `json:"logit_bias,omitempty"`
	Logprobs    bool             `json:"logprobs,omitempty"`
	TopLogprobs int              `json:"top_logprobs,omitempty"`
	ResponseFmt *wireResponseFmt `json:"response_format,omitempty"`
	User        string           `json:"user,omitempty"`

	// Legacy function-calling shape (accepted at decode, folded into
	// Tools/ToolChoice; never emitted).
	Functions  []wireFunction  `json:"functions,omitempty"`
	FunctionCl json.RawMessage `json:"function_call,omitempty"`
}

// flexStrings accepts "x" or ["x"] on the wire (OpenAI allows both for
// stop). It always marshals back as an array.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = []string{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	*f = arr
	return nil
}

// wireMessage is one chat message. Content is raw because OpenAI allows
// string | part[] | null.
type wireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []wireToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"` // role:"tool" messages
	// Legacy shape.
	FunctionCall *wireFnCall `json:"function_call,omitempty"`
}

// wireFnCall is the legacy assistant function_call object.
type wireFnCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// wirePart is one content part of a multimodal message.
type wirePart struct {
	Type     string        `json:"type"` // "text" | "image_url"
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto" | "low" | "high"
}

type wireTool struct {
	Type     string     `json:"type"` // "function"
	Function wireFnSpec `json:"function"`
}

type wireFnSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireToolCall struct {
	ID       string     `json:"id,omitempty"`    // omitted in stream arg fragments
	Index    *int       `json:"index,omitempty"` // present in stream deltas
	Type     string     `json:"type,omitempty"`
	Function *wireFnArg `json:"function,omitempty"`
}

type wireFnArg struct {
	Name string `json:"name,omitempty"`
	// Arguments has no omitempty: "" must round-trip (initial tool-call
	// deltas carry an explicit empty arguments string)
	// [quirk:openai-empty-arguments].
	Arguments string `json:"arguments"`
}

type wireResponseFmt struct {
	Type       string          `json:"type"`                  // "text" | "json_object" | "json_schema"
	JSONSchema json.RawMessage `json:"json_schema,omitempty"` // full json_schema object {name, schema, strict?}
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ---------------------------------------------------------------------------
// Response wire types
// ---------------------------------------------------------------------------

// chatResponse is the OpenAI /v1/chat/completions response body.
type chatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`  // "chat.completion"
	Created int64        `json:"created"` // seconds
	Model   string       `json:"model"`
	Choices []wireChoice `json:"choices"`
	Usage   *wireUsage   `json:"usage,omitempty"`
}

type wireChoice struct {
	Index        int             `json:"index"`
	Message      wireRespMessage `json:"message"`
	FinishReason string          `json:"finish_reason"` // nullable in the wild; "" == null
}

type wireRespMessage struct {
	Role             string         `json:"role"`
	Content          *string        `json:"content"` // always emitted; null when only tool calls
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"` // DeepSeek-style thinking
	Refusal          *string        `json:"refusal,omitempty"`           // OpenAI refusal field
}

type wireUsage struct {
	PromptTokens        int64                    `json:"prompt_tokens"`
	CompletionTokens    int64                    `json:"completion_tokens"`
	TotalTokens         int64                    `json:"total_tokens"`
	PromptTokensDetails *wirePromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	CompletionDetails   *wireCompletionDetails   `json:"completion_tokens_details,omitempty"`
}

type wirePromptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type wireCompletionDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// ---------------------------------------------------------------------------
// Stream wire types
// ---------------------------------------------------------------------------

// chatChunk is one streamed chat.completion.chunk.
type chatChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"` // "chat.completion.chunk"
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []wireChunkChoice `json:"choices"`
	Usage   *wireUsage        `json:"usage,omitempty"` // final chunk when stream_options.include_usage
}

type wireChunkChoice struct {
	Index        int       `json:"index"`
	Delta        wireDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"` // JSON null until the terminal chunk
}

type wireDelta struct {
	Role             string         `json:"role,omitempty"`
	Content          string         `json:"content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	Refusal          *string        `json:"refusal,omitempty"`
}

// ---------------------------------------------------------------------------
// Error wire types
// ---------------------------------------------------------------------------

type errorBody struct {
	Error wireError `json:"error"`
}

type wireError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Param   string `json:"param,omitempty"`
	Code    any    `json:"code,omitempty"` // string or number across providers
}
