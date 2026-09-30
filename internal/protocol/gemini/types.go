// Package gemini implements the Google Gemini generateContent /
// streamGenerateContent (alt=sse) wire protocol, including function calls,
// safety blocks, thought summaries, and usage metadata. The mapping tables
// are in docs/protocol-mappings.md §3.
package gemini

import "encoding/json"

// ---------------------------------------------------------------------------
// Request wire types
// ---------------------------------------------------------------------------

// generateRequest is the Gemini generateContent request body. The model
// lives in the URL path (models/{model}:generateContent), not the body.
type generateRequest struct {
	SystemInstruction *wireContent          `json:"systemInstruction,omitempty"`
	Contents          []wireContent         `json:"contents,omitempty"`
	Tools             []wireTool            `json:"tools,omitempty"`
	ToolConfig        *wireToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *wireGenerationConfig `json:"generationConfig,omitempty"`
}

type wireContent struct {
	Role  string     `json:"role,omitempty"` // "user" | "model"; omitted for systemInstruction
	Parts []wirePart `json:"parts"`
}

// wirePart is the part union: text, inlineData, functionCall,
// functionResponse, thought summaries.
type wirePart struct {
	Text             string                `json:"text,omitempty"`
	InlineData       *wireInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *wireFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *wireFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                  `json:"thought,omitempty"`
	ThoughtSignature string                `json:"thoughtSignature,omitempty"`
}

type wireInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type wireFunctionCall struct {
	ID   string          `json:"id,omitempty"` // only newer API versions
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type wireFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type wireTool struct {
	FunctionDeclarations []wireFunctionDecl `json:"functionDeclarations"`
}

type wireFunctionDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters is the classic v1beta field; parametersJsonSchema is the
	// newer full-JSON-schema field. Decode accepts both; encode emits
	// parameters [quirk:gemini-parameters-field].
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type wireToolConfig struct {
	FunctionCallingConfig *wireFnCallingConfig `json:"functionCallingConfig"`
}

type wireFnCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"` // "AUTO" | "ANY" | "NONE"
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type wireGenerationConfig struct {
	MaxOutputTokens  int64           `json:"maxOutputTokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"topP,omitempty"`
	TopK             *int64          `json:"topK,omitempty"`
	StopSequences    []string        `json:"stopSequences,omitempty"`
	Seed             *int64          `json:"seed,omitempty"`
	ResponseMimeType string          `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage `json:"responseSchema,omitempty"`
}

// ---------------------------------------------------------------------------
// Response wire types
// ---------------------------------------------------------------------------

// generateResponse is the Gemini generateContent response body; each
// streamGenerateContent SSE chunk carries one of these.
type generateResponse struct {
	ResponseID     string              `json:"responseId,omitempty"`
	ModelVersion   string              `json:"modelVersion,omitempty"`
	Candidates     []wireCandidate     `json:"candidates,omitempty"`
	UsageMetadata  *wireUsageMetadata  `json:"usageMetadata,omitempty"`
	PromptFeedback *wirePromptFeedback `json:"promptFeedback,omitempty"`
}

type wireCandidate struct {
	Content      *wireContent `json:"content,omitempty"`
	FinishReason string       `json:"finishReason,omitempty"`
}

type wireUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         int64 `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
}

type wirePromptFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

// ---------------------------------------------------------------------------
// Error wire types
// ---------------------------------------------------------------------------

type errorEnvelope struct {
	Error wireErrorObj `json:"error"`
}

type wireErrorObj struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message"`
	Status  string `json:"status,omitempty"` // gRPC code string
}
