package ratelimit_test

import (
	"sync"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
)

func TestPricing_CalculateCost(t *testing.T) {
	pt := ratelimit.NewPriceTable()

	// 1. gpt-4o test:
	// Input: $2.50 / 1M = 2,500,000 micros
	// Output: $10.00 / 1M = 10,000,000 micros
	// 2,000 input tokens = (2000 * 2,500,000) / 1,000,000 = 5,000 micros
	// 1,000 output tokens = (1000 * 10,000,000) / 1,000,000 = 10,000 micros
	// Expected total = 15,000 micros ($0.015)
	usage1 := domain.TokenUsage{
		InputTokens:  2000,
		OutputTokens: 1000,
		TotalTokens:  3000,
	}
	cost1 := pt.CalculateCost("gpt-4o", usage1, 100)
	if cost1 != 15000 {
		t.Fatalf("gpt-4o standard cost: want 15000 micros, got %d", cost1)
	}

	// 2. Cache read tokens on claude-3-5-sonnet:
	// InputCostPer1M: 3,000,000 ($3.00)
	// CacheReadCostPer1M: 300,000 ($0.30)
	// OutputCostPer1M: 15,000,000 ($15.00)
	// InputTokens: 10,000 of which 8,000 are cached (CacheReadTokens: 8000, uncached: 2000)
	// Uncached: 2000 * 3,000,000 / 1M = 6,000 micros
	// Cached: 8000 * 300,000 / 1M = 2,400 micros
	// Output: 500 * 15,000,000 / 1M = 7,500 micros
	// Expected total: 6000 + 2400 + 7500 = 15,900 micros
	usage2 := domain.TokenUsage{
		InputTokens:     10000,
		CacheReadTokens: 8000,
		OutputTokens:    500,
	}
	cost2 := pt.CalculateCost("claude-3-5-sonnet", usage2, 100)
	if cost2 != 15900 {
		t.Fatalf("claude cached cost: want 15900 micros, got %d", cost2)
	}

	// 3. Cost Multiplier test (e.g. 50% discount -> costMultiplier 50)
	// 15,000 micros * 50 / 100 = 7,500 micros
	costDiscount := pt.CalculateCost("gpt-4o", usage1, 50)
	if costDiscount != 7500 {
		t.Fatalf("discounted cost: want 7500 micros, got %d", costDiscount)
	}

	// 4. Cost Multiplier markup (e.g. 150%)
	// 15,000 micros * 150 / 100 = 22,500 micros
	costMarkup := pt.CalculateCost("gpt-4o", usage1, 150)
	if costMarkup != 22500 {
		t.Fatalf("markup cost: want 22500 micros, got %d", costMarkup)
	}

	// 5. Unknown model returns 0
	costUnknown := pt.CalculateCost("unknown-model", usage1, 100)
	if costUnknown != 0 {
		t.Fatalf("unknown model cost: want 0, got %d", costUnknown)
	}
}

func TestPricing_CustomPriceOverride(t *testing.T) {
	pt := ratelimit.NewPriceTable()

	customModel := "custom-finetuned-llama"
	pt.SetPrice(customModel, ratelimit.ModelPrice{
		InputCostPer1M:  1_000_000, // $1.00 / 1M
		OutputCostPer1M: 2_000_000, // $2.00 / 1M
	})

	price, ok := pt.GetPrice(customModel)
	if !ok || price.InputCostPer1M != 1_000_000 {
		t.Fatalf("custom price not set properly: %+v", price)
	}

	cost := pt.CalculateCost(customModel, domain.TokenUsage{
		InputTokens:  1000,
		OutputTokens: 1000,
	}, 100)

	// 1000 * 1,000,000 / 1M = 1000
	// 1000 * 2,000,000 / 1M = 2000
	// Total = 3000 micros ($0.003)
	if cost != 3000 {
		t.Fatalf("want 3000 micros, got %d", cost)
	}
}

func TestPricing_EstimateCostAndPromptTokens(t *testing.T) {
	pt := ratelimit.NewPriceTable()

	// Estimate cost: 2,000 prompt tokens, 500 maxTokens on gpt-4o
	// (2000 * 2,500,000)/1M + (500 * 10,000,000)/1M = 5,000 + 5,000 = 10,000 micros
	estimate := pt.EstimateCost("gpt-4o", 2000, 500, 100)
	if estimate != 10000 {
		t.Fatalf("want estimate 10000 micros, got %d", estimate)
	}

	// Estimate prompt tokens from characters
	req := domain.Request{
		Messages: []domain.Message{
			{
				Role: domain.RoleUser,
				Content: []domain.ContentBlock{
					{Type: domain.BlockText, Text: "Hello, world! How are you today?"}, // 33 chars -> ~9 tokens
				},
			},
		},
	}
	tokens := ratelimit.EstimatePromptTokens(req)
	if tokens < 8 || tokens > 10 {
		t.Fatalf("unexpected prompt token estimate: %d", tokens)
	}
}

func TestPricing_Concurrency(t *testing.T) {
	pt := ratelimit.NewPriceTable()

	const goroutines = 20
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Readers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = pt.CalculateCost("gpt-4o", domain.TokenUsage{
					InputTokens:  int64(j * 10),
					OutputTokens: int64(j * 5),
				}, 100)
				_ = pt.EstimateCost("claude-3-5-sonnet", 1000, 500, 100)
			}
		}()
	}

	// Writers
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				pt.SetPrice("dynamic-model", ratelimit.ModelPrice{
					InputCostPer1M:  int64(1000 + id + j),
					OutputCostPer1M: int64(2000 + id + j),
				})
			}
		}(i)
	}

	wg.Wait()
}
