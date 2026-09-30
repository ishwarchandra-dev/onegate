package conformance

// LossyMapping enumerates every known lossy field mapping, mirroring
// docs/protocol-mappings.md. Protocol names the TARGET protocol that cannot
// carry the field. The conformance tests use these keys as the ignore sets
// when comparing canonical forms across adapters; the docs test verifies
// the table and the mapping document stay in sync.
type LossyMapping struct {
	Protocol string // "openai" | "anthropic" | "gemini"
	Key      string // ignore-set key used by the comparators
	Field    string // canonical field
	Note     string
}

// Ignore-set keys (single source of truth for the comparators).
const (
	IgTopK            = "top_k"
	IgSeed            = "seed"
	IgLogitBias       = "logit_bias"
	IgLogprobs        = "logprobs"
	IgResponseFormat  = "response_format"
	IgUser            = "user"
	IgThinkingHistory = "thinking_history"
	IgToolCallIDs     = "tool_call_ids"
	IgImageDetail     = "image_detail"
	IgReasoningUsage  = "reasoning_usage"
	IgCreated         = "created_ms"
	IgSystemFold      = "system_fold"
	IgJSONSchemaEnv   = "json_schema_envelope"
	IgCacheWriteUsage = "cache_write_usage"
	IgModel           = "model"
	IgStream          = "stream"
)

// LossyMappings is the documented diff table (acceptance: "Lossy mappings
// enumerated in a documented diff table").
var LossyMappings = []LossyMapping{
	// --- OpenAI cannot carry ---
	{Protocol: "openai", Key: IgTopK, Field: "sampling.top_k", Note: "no top_k on the OpenAI wire"},
	{Protocol: "openai", Key: IgThinkingHistory, Field: "messages[].thinking", Note: "assistant thinking blocks are dropped from history (no standard field; DeepSeek reasoning_content is response-only)"},
	{Protocol: "openai", Key: IgImageDetail, Field: "image.detail", Note: "detail is an OpenAI-only hint"},

	// --- Anthropic cannot carry ---
	{Protocol: "anthropic", Key: IgSeed, Field: "sampling.seed", Note: "no seed parameter"},
	{Protocol: "anthropic", Key: IgLogitBias, Field: "sampling.logit_bias", Note: "no logit biases"},
	{Protocol: "anthropic", Key: IgLogprobs, Field: "sampling.logprobs/top_logprobs", Note: "no logprob request flags"},
	{Protocol: "anthropic", Key: IgResponseFormat, Field: "sampling.response_format", Note: "no structured-output mode"},
	{Protocol: "anthropic", Key: IgSystemFold, Field: "messages[].role=system ordering", Note: "system messages fold into one top-level system param (concatenated, position lost)"},
	{Protocol: "anthropic", Key: IgReasoningUsage, Field: "usage.reasoning_tokens", Note: "thinking tokens are reported inside output tokens, not separately"},
	{Protocol: "anthropic", Key: IgCreated, Field: "response.created_ms", Note: "no created timestamp on the wire"},
	{Protocol: "anthropic", Key: IgImageDetail, Field: "image.detail", Note: "detail is an OpenAI-only hint"},

	// --- Gemini cannot carry ---
	{Protocol: "gemini", Key: IgModel, Field: "model", Note: "carried in the URL path (models/{model}:generateContent), not the body"},
	{Protocol: "gemini", Key: IgStream, Field: "stream", Note: "streaming is the :streamGenerateContent endpoint, not a body flag"},
	{Protocol: "gemini", Key: IgLogitBias, Field: "sampling.logit_bias", Note: "no logit biases"},
	{Protocol: "gemini", Key: IgLogprobs, Field: "sampling.logprobs/top_logprobs", Note: "no logprob request flags"},
	{Protocol: "gemini", Key: IgUser, Field: "user", Note: "no end-user identifier"},
	{Protocol: "gemini", Key: IgToolCallIDs, Field: "tool_call.id / tool_result.call_id", Note: "function calls carry names only; IDs are synthesized as call_{name}"},
	{Protocol: "gemini", Key: IgImageDetail, Field: "image.detail", Note: "detail is an OpenAI-only hint"},
	{Protocol: "gemini", Key: IgJSONSchemaEnv, Field: "response_format.json_schema envelope", Note: "only the inner schema survives (name/strict dropped)"},
	{Protocol: "gemini", Key: IgCacheWriteUsage, Field: "usage.cache_write_tokens", Note: "no cache-write counter in usageMetadata"},
	{Protocol: "gemini", Key: IgCreated, Field: "response.created_ms", Note: "no created timestamp on the wire"},
}

// LossKeysFor returns the ignore-set keys for a target protocol.
func LossKeysFor(protocol string) map[string]bool {
	set := map[string]bool{}
	for _, m := range LossyMappings {
		if m.Protocol == protocol {
			set[m.Key] = true
		}
	}
	return set
}
