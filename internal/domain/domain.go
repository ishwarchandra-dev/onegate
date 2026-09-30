// Package domain defines the canonical types for OneGate.
//
// These types are the lingua franca of the gateway: every layer boundary
// (protocol adapters, proxy, routing, storage, management API) exchanges
// data as domain types, never as provider-specific or storage-specific
// shapes. This keeps dependencies pointing inward and makes each layer
// independently testable.
//
// Rules for this package:
//   - No imports outside the standard library.
//   - Zero behavioral logic: constructors and pure helpers only.
//   - JSON tags follow snake_case, aligned with the OpenAI/Anthropic wire
//     conventions OmniRoute v3.8.52 exposed to its clients. Full parity
//     verification is owned by the Phase 7 compatibility audit
//     (p7.parity-checklist).
//
// Time is Unix milliseconds (UTC) everywhere. Money and token costs are
// integer micro-units (1 micro = 1e-6 USD) — never floats.
package domain

// ProviderProtocol identifies a provider's wire protocol.
type ProviderProtocol string

const (
	ProtocolOpenAI     ProviderProtocol = "openai"
	ProtocolAnthropic  ProviderProtocol = "anthropic"
	ProtocolGemini     ProviderProtocol = "gemini"
	ProtocolOpenAIComp ProviderProtocol = "openai-compat" // OpenRouter, Groq, Mistral, vLLM, Ollama, ...
)

// Provider is a configured upstream LLM provider.
//
// The API key is never held in this type: storage keeps an encrypted blob
// (internal/auth), and the proxy resolves it at call time. The dashboard
// only ever sees MaskedKey.
type Provider struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Protocol  ProviderProtocol `json:"protocol"`
	BaseURL   string           `json:"base_url"`
	MaskedKey string           `json:"masked_key,omitempty"` // e.g. "sk-...f3a2"; display only
	Enabled   bool             `json:"enabled"`
	CreatedMS int64            `json:"created_ms"`
	UpdatedMS int64            `json:"updated_ms"`
}

// ModelCapabilities declares what a model can do. Routing uses these flags
// to filter targets for a given request (e.g. a tools request never routes
// to a non-tools target).
type ModelCapabilities struct {
	Tools    bool `json:"tools"`
	Vision   bool `json:"vision"`
	JSONMode bool `json:"json_mode"`
	Stream   bool `json:"stream"`
}

// ModelTarget maps a canonical model to one upstream provider's copy of it.
// ProviderModel may differ from the canonical ID (e.g. canonical
// "claude-sonnet" -> provider model "claude-3-5-sonnet-latest").
type ModelTarget struct {
	ProviderID     string `json:"provider_id"`
	ProviderModel  string `json:"provider_model"`
	Weight         int    `json:"weight"`   // weighted policy: relative share
	Position       int    `json:"position"` // ordered policy: lower first
	CostMultiplier int    `json:"cost_multiplier,omitempty"` // percent, 100 = nominal
}

// Model is a canonical model identity exposed to clients.
type Model struct {
	ID           string            `json:"id"` // canonical, e.g. "gpt-4o"
	Aliases      []string          `json:"aliases,omitempty"`
	Targets      []ModelTarget     `json:"targets"`
	Capabilities ModelCapabilities `json:"capabilities"`
	CreatedMS    int64             `json:"created_ms"`
	UpdatedMS    int64             `json:"updated_ms"`
}

// HasCapability reports whether the model supports the named capability.
func (m Model) HasCapability(name string) bool {
	switch name {
	case "tools":
		return m.Capabilities.Tools
	case "vision":
		return m.Capabilities.Vision
	case "json_mode":
		return m.Capabilities.JSONMode
	case "stream":
		return m.Capabilities.Stream
	default:
		return false
	}
}

// KeyStatus is the lifecycle state of a virtual key.
type KeyStatus string

const (
	KeyActive  KeyStatus = "active"
	KeyRevoked KeyStatus = "revoked"
)

// KeyScopes constrains what a virtual key may access. Empty lists mean
// "allow all" (subject to the model existing).
type KeyScopes struct {
	AllowedModels    []string `json:"allowed_models,omitempty"`
	AllowedProviders []string `json:"allowed_providers,omitempty"`
}

// KeyLimits are the enforceable per-key quotas. Zero means unlimited.
type KeyLimits struct {
	RPM               int64 `json:"rpm,omitempty"`                 // requests per minute
	TPM               int64 `json:"tpm,omitempty"`                 // tokens per minute
	Concurrency       int   `json:"concurrency,omitempty"`         // parallel in-flight requests
	MaxSpendUSDMicros int64 `json:"max_spend_usd_micros,omitempty"` // lifetime spend cap
}

// VirtualKey is a client-facing key issued by the gateway. The raw key is
// returned exactly once at creation; only its argon2id hash is stored.
type VirtualKey struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"` // display prefix, e.g. "ogk-lt4x…"
	KeyHash    string    `json:"-"`      // never serialized to clients
	Scopes     KeyScopes `json:"scopes"`
	Limits     KeyLimits `json:"limits"`
	Status     KeyStatus `json:"status"`
	CreatedMS  int64     `json:"created_ms"`
	ExpiresMS  int64     `json:"expires_ms,omitempty"` // 0 = never
	LastUsedMS int64     `json:"last_used_ms,omitempty"`
}

// ExpiredAt reports whether the key is expired at the given Unix-ms time.
func (k VirtualKey) ExpiredAt(nowMS int64) bool {
	return k.ExpiresMS > 0 && nowMS >= k.ExpiresMS
}

// FallbackPolicy selects how a routing rule orders its targets.
type FallbackPolicy string

const (
	PolicyOrdered  FallbackPolicy = "ordered"  // position order, first healthy wins
	PolicyWeighted FallbackPolicy = "weighted" // random by weight, seeded per request
	PolicyCost     FallbackPolicy = "cost"     // cheapest first
	PolicyLatency  FallbackPolicy = "latency"  // lowest observed latency first
)

// RoutingRule binds a canonical model to a fallback policy. Per-key
// overrides may swap the policy at request time (Phase 4).
type RoutingRule struct {
	ID       string         `json:"id"`
	ModelID  string         `json:"model_id"`
	Policy   FallbackPolicy `json:"policy"`
	Enabled  bool           `json:"enabled"`
	Position int            `json:"position"`
}

// RequestStatus classifies the outcome of a proxied request.
type RequestStatus string

const (
	RequestSuccess   RequestStatus = "success"
	RequestError     RequestStatus = "error"
	RequestCancelled RequestStatus = "cancelled"
)

// RequestRecord is the durable record of one proxied request: what was
// asked, where it was served, what it cost, and how long it took. Written
// exactly once per request by the usage pipeline (Phase 5), including for
// cancelled requests.
type RequestRecord struct {
	ID               string        `json:"id"` // equals the trace ID
	TraceID          string        `json:"trace_id"`
	VirtualKeyID     string        `json:"virtual_key_id"`
	ModelRequested   string        `json:"model_requested"`
	ModelServed      string        `json:"model_served,omitempty"`
	ProviderID       string        `json:"provider_id,omitempty"`
	Status           RequestStatus `json:"status"`
	ErrorCode        string        `json:"error_code,omitempty"`
	Stream           bool          `json:"stream"`
	PromptTokens     int64         `json:"prompt_tokens"`
	CompletionTokens int64         `json:"completion_tokens"`
	TotalTokens      int64         `json:"total_tokens"`
	CostUSDMicros    int64         `json:"cost_usd_micros"`
	LatencyMS        int64         `json:"latency_ms"` // total request wall time
	TTFTMS           int64         `json:"ttft_ms"`    // time to first token (streaming)
	Attempts         int           `json:"attempts"`   // provider attempts (fallbacks + 1)
	CreatedMS        int64         `json:"created_ms"`
}

// Usage aggregates token and cost counters for a time bucket. Rollup rows
// (Phase 5) share this shape so analytics queries are uniform.
type Usage struct {
	BucketStartMS    int64  `json:"bucket_start_ms"`
	VirtualKeyID     string `json:"virtual_key_id"`
	ModelID          string `json:"model_id"`
	ProviderID       string `json:"provider_id"`
	Requests         int64  `json:"requests"`
	Errors           int64  `json:"errors"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	CostUSDMicros    int64  `json:"cost_usd_micros"`
}
