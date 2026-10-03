// Package ratelimit implements rate limiting, quota enforcement, model pricing,
// and provider 429 backoff for OneGate (Phase 4).
package ratelimit

import (
	"sync"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// Per-million token scaling factor.
const tokensPerMillion int64 = 1_000_000

// ModelPrice defines token pricing for a model in micro-USD (1e-6 USD).
// Float values are strictly forbidden; all amounts are exact integer micro-units.
type ModelPrice struct {
	// InputCostPer1M is the cost per 1 million prompt tokens in micro-USD.
	InputCostPer1M int64 `json:"input_cost_per_1m"`
	// OutputCostPer1M is the cost per 1 million completion tokens in micro-USD.
	OutputCostPer1M int64 `json:"output_cost_per_1m"`
	// CacheReadCostPer1M is the cost per 1 million prompt-cached tokens in micro-USD.
	CacheReadCostPer1M int64 `json:"cache_read_cost_per_1m,omitempty"`
	// CacheWriteCostPer1M is the cost per 1 million cache creation tokens in micro-USD.
	CacheWriteCostPer1M int64 `json:"cache_write_cost_per_1m,omitempty"`
}

// defaultModelPrices contains standard reference pricing for major models (USD micros per 1M tokens).
var defaultModelPrices = map[string]ModelPrice{
	// OpenAI models
	"gpt-4o": {
		InputCostPer1M:     2_500_000,  // $2.50 / 1M
		OutputCostPer1M:    10_000_000, // $10.00 / 1M
		CacheReadCostPer1M: 1_250_000,  // $1.25 / 1M
	},
	"gpt-4o-mini": {
		InputCostPer1M:     150_000, // $0.15 / 1M
		OutputCostPer1M:    600_000, // $0.60 / 1M
		CacheReadCostPer1M: 75_000,  // $0.075 / 1M
	},
	"o1": {
		InputCostPer1M:     15_000_000, // $15.00 / 1M
		OutputCostPer1M:    60_000_000, // $60.00 / 1M
		CacheReadCostPer1M: 7_500_000,  // $7.50 / 1M
	},
	"o1-mini": {
		InputCostPer1M:     3_000_000,  // $3.00 / 1M
		OutputCostPer1M:    12_000_000, // $12.00 / 1M
		CacheReadCostPer1M: 1_500_000,  // $1.50 / 1M
	},

	// Anthropic models
	"claude-3-5-sonnet": {
		InputCostPer1M:      3_000_000,  // $3.00 / 1M
		OutputCostPer1M:     15_000_000, // $15.00 / 1M
		CacheReadCostPer1M:  300_000,    // $0.30 / 1M
		CacheWriteCostPer1M: 3_750_000,  // $3.75 / 1M
	},
	"claude-3-5-haiku": {
		InputCostPer1M:      800_000,   // $0.80 / 1M
		OutputCostPer1M:     4_000_000, // $4.00 / 1M
		CacheReadCostPer1M:  80_000,    // $0.08 / 1M
		CacheWriteCostPer1M: 1_000_000, // $1.00 / 1M
	},
	"claude-3-opus": {
		InputCostPer1M:      15_000_000, // $15.00 / 1M
		OutputCostPer1M:     75_000_000, // $75.00 / 1M
		CacheReadCostPer1M:  1_500_000,  // $1.50 / 1M
		CacheWriteCostPer1M: 18_750_000, // $18.75 / 1M
	},

	// Google Gemini models
	"gemini-1.5-pro": {
		InputCostPer1M:     1_250_000, // $1.25 / 1M
		OutputCostPer1M:    5_000_000, // $5.00 / 1M
		CacheReadCostPer1M: 312_500,   // $0.3125 / 1M
	},
	"gemini-1.5-flash": {
		InputCostPer1M:     75_000,  // $0.075 / 1M
		OutputCostPer1M:    300_000, // $0.30 / 1M
		CacheReadCostPer1M: 18_750,  // $0.01875 / 1M
	},
	"gemini-2.0-flash": {
		InputCostPer1M:     100_000, // $0.10 / 1M
		OutputCostPer1M:    400_000, // $0.40 / 1M
		CacheReadCostPer1M: 25_000,  // $0.025 / 1M
	},

	// Open-weight / Groq / Meta Llama models
	"llama-3.1-70b": {
		InputCostPer1M:  590_000, // $0.59 / 1M
		OutputCostPer1M: 790_000, // $0.79 / 1M
	},
	"llama-3.1-8b": {
		InputCostPer1M:  50_000, // $0.05 / 1M
		OutputCostPer1M: 80_000, // $0.08 / 1M
	},
}

// PriceTable manages price lookup for models and estimates/calculates token costs.
// It is thread-safe for concurrent proxy traffic.
type PriceTable struct {
	mu     sync.RWMutex
	prices map[string]ModelPrice
}

// NewPriceTable initializes a PriceTable populated with standard model prices.
func NewPriceTable() *PriceTable {
	pt := &PriceTable{
		prices: make(map[string]ModelPrice, len(defaultModelPrices)),
	}
	for m, p := range defaultModelPrices {
		pt.prices[m] = p
	}
	return pt
}

// SetPrice registers or overrides the price for a model.
func (pt *PriceTable) SetPrice(modelID string, price ModelPrice) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.prices[modelID] = price
}

// GetPrice looks up the price for a model.
func (pt *PriceTable) GetPrice(modelID string) (ModelPrice, bool) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	p, ok := pt.prices[modelID]
	return p, ok
}

// CalculateCost computes actual USD micros from verified token usage and an optional cost multiplier.
// Cost multiplier is a percentage (100 = nominal; e.g. 80 = 20% discount, 150 = 1.5x markup).
// If costMultiplier is zero, it defaults to 100.
func (pt *PriceTable) CalculateCost(model string, usage domain.TokenUsage, costMultiplier int) int64 {
	pt.mu.RLock()
	price, ok := pt.prices[model]
	pt.mu.RUnlock()

	if !ok {
		return 0
	}

	if costMultiplier <= 0 {
		costMultiplier = 100
	}

	// Calculate sub-totals in integer micro-units
	// Tokens * CostPer1M / 1_000_000
	var totalMicros int64

	// Uncached input tokens = Total InputTokens - CacheReadTokens
	uncachedInput := usage.InputTokens - usage.CacheReadTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}

	totalMicros += (uncachedInput * price.InputCostPer1M) / tokensPerMillion
	totalMicros += (usage.OutputTokens * price.OutputCostPer1M) / tokensPerMillion

	if usage.CacheReadTokens > 0 && price.CacheReadCostPer1M > 0 {
		totalMicros += (usage.CacheReadTokens * price.CacheReadCostPer1M) / tokensPerMillion
	}
	if usage.CacheWriteTokens > 0 && price.CacheWriteCostPer1M > 0 {
		totalMicros += (usage.CacheWriteTokens * price.CacheWriteCostPer1M) / tokensPerMillion
	}

	// Apply cost multiplier
	if costMultiplier != 100 {
		totalMicros = (totalMicros * int64(costMultiplier)) / 100
	}

	return totalMicros
}

// EstimateCost computes estimated USD micros for a request before execution.
// It uses promptTokens and estimated output tokens (e.g. from max_tokens).
func (pt *PriceTable) EstimateCost(model string, promptTokens, maxTokens int64, costMultiplier int) int64 {
	pt.mu.RLock()
	price, ok := pt.prices[model]
	pt.mu.RUnlock()

	if !ok {
		return 0
	}

	if costMultiplier <= 0 {
		costMultiplier = 100
	}

	var estimatedOutputTokens int64 = maxTokens
	if estimatedOutputTokens <= 0 {
		// Heuristic default for open-ended requests if max_tokens is unset: 1,000 tokens
		estimatedOutputTokens = 1000
	}

	totalMicros := (promptTokens * price.InputCostPer1M) / tokensPerMillion
	totalMicros += (estimatedOutputTokens * price.OutputCostPer1M) / tokensPerMillion

	if costMultiplier != 100 {
		totalMicros = (totalMicros * int64(costMultiplier)) / 100
	}

	return totalMicros
}

// EstimatePromptTokens provides a lightweight character-based token count estimation (~4 characters/token)
// when actual tokenizer token counts are not yet available.
func EstimatePromptTokens(req domain.Request) int64 {
	var charCount int64
	for _, msg := range req.Messages {
		for _, b := range msg.Content {
			charCount += int64(len(b.Text))
		}
	}
	for _, tool := range req.Tools {
		charCount += int64(len(tool.Name) + len(tool.Description))
	}
	if charCount <= 0 {
		return 10
	}
	// Rough English text approximation: 4 chars per token (rounded up)
	tokens := (charCount + 3) / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
