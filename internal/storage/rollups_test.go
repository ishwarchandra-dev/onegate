package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

func TestRollups_ExplainQueryPlanIndexOnly(t *testing.T) {
	s := openMigrated(t)

	// Ensure some data exists
	repo := s.Requests()
	for i := 0; i < 10; i++ {
		_ = repo.Insert(domain.RequestRecord{
			ID:               fmt.Sprintf("eqp-%d", i),
			TraceID:          fmt.Sprintf("eqp-%d", i),
			VirtualKeyID:     "vkey-1",
			ModelRequested:   "gpt-4o",
			ModelServed:      "gpt-4o",
			ProviderID:       "openai",
			Status:           domain.RequestSuccess,
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			CostUSDMicros:    300,
			CreatedMS:        int64(1_700_000_000_000 + i*1000),
		})
	}

	testCases := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "global_hot_window",
			query: `EXPLAIN QUERY PLAN
				SELECT bucket_start_ms, sum(requests), sum(errors), sum(prompt_tokens), sum(completion_tokens), sum(total_tokens), sum(cost_usd_micros)
				FROM usage_rollups
				WHERE bucket_start_ms >= ? AND bucket_start_ms < ?
				GROUP BY bucket_start_ms`,
			args: []any{int64(1_699_000_000_000), int64(1_701_000_000_000)},
		},
		{
			name: "vkey_hot_window",
			query: `EXPLAIN QUERY PLAN
				SELECT vkey_id, bucket_start_ms, sum(requests), sum(errors), sum(prompt_tokens), sum(completion_tokens), sum(total_tokens), sum(cost_usd_micros)
				FROM usage_rollups
				WHERE vkey_id = ? AND bucket_start_ms >= ? AND bucket_start_ms < ?
				GROUP BY bucket_start_ms`,
			args: []any{"vkey-1", int64(1_699_000_000_000), int64(1_701_000_000_000)},
		},
		{
			name: "model_hot_window",
			query: `EXPLAIN QUERY PLAN
				SELECT model_id, bucket_start_ms, sum(requests), sum(errors), sum(prompt_tokens), sum(completion_tokens), sum(total_tokens), sum(cost_usd_micros)
				FROM usage_rollups
				WHERE model_id = ? AND bucket_start_ms >= ? AND bucket_start_ms < ?
				GROUP BY bucket_start_ms`,
			args: []any{"gpt-4o", int64(1_699_000_000_000), int64(1_701_000_000_000)},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.db.Query(tc.query, tc.args...)
			if err != nil {
				t.Fatalf("query plan failed: %v", err)
			}
			defer rows.Close()

			var planParts []string
			isCoveringIndex := false

			for rows.Next() {
				var id, parent, notused int
				var detail string
				if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
					t.Fatalf("scan plan row: %v", err)
				}
				planParts = append(planParts, detail)
				if strings.Contains(strings.ToUpper(detail), "COVERING INDEX") {
					isCoveringIndex = true
				}
			}

			fullPlan := strings.Join(planParts, "\n")
			// Acceptance: EXPLAIN QUERY PLAN shows index-only scans for hot windows
			if !isCoveringIndex {
				t.Fatalf("expected EXPLAIN QUERY PLAN to show COVERING INDEX (index-only scan), got:\n%s", fullPlan)
			}
		})
	}
}

func TestRollups_ChecksumReconcileWithRawRows(t *testing.T) {
	s := openMigrated(t)
	repo := s.Requests()
	rollupRepo := s.Rollups()
	ctx := context.Background()

	baseTime := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	const totalRecords = 300

	records := make([]domain.RequestRecord, totalRecords)
	for i := 0; i < totalRecords; i++ {
		// Distribute across 5 hours, 3 virtual keys, 2 models
		hourOffset := int64((i % 5)) * HourBucketMS
		vkey := fmt.Sprintf("vkey-%d", i%3)
		model := "gpt-4o"
		if i%2 == 1 {
			model = "claude-3-5-sonnet"
		}
		status := domain.RequestSuccess
		if i%7 == 0 {
			status = domain.RequestError
		}

		records[i] = domain.RequestRecord{
			ID:               fmt.Sprintf("chk-rec-%03d", i),
			TraceID:          fmt.Sprintf("chk-rec-%03d", i),
			VirtualKeyID:     vkey,
			ModelRequested:   model,
			ModelServed:      model,
			ProviderID:       "provider-test",
			Status:           status,
			PromptTokens:     int64(50 + i*2),
			CompletionTokens: int64(20 + i),
			TotalTokens:      int64(70 + i*3),
			CostUSDMicros:    int64(100 + i*10),
			CreatedMS:        baseTime + hourOffset + int64(i*10),
		}
	}

	// Insert in batches of 50
	for i := 0; i < totalRecords; i += 50 {
		end := i + 50
		if end > totalRecords {
			end = totalRecords
		}
		if err := repo.InsertBatch(records[i:end]); err != nil {
			t.Fatalf("InsertBatch [%d:%d]: %v", i, end, err)
		}
	}

	// 1. Raw rows checksum
	var rawRequests, rawErrors, rawPrompt, rawCompletion, rawTotal, rawCost int64
	err := s.db.QueryRow(`SELECT
		COUNT(*),
		SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END),
		SUM(prompt_tokens),
		SUM(completion_tokens),
		SUM(total_tokens),
		SUM(cost_usd_micros)
		FROM requests`).Scan(&rawRequests, &rawErrors, &rawPrompt, &rawCompletion, &rawTotal, &rawCost)
	if err != nil {
		t.Fatalf("query raw checksum: %v", err)
	}

	// 2. Rollup summary checksum
	summary, err := rollupRepo.QuerySummary(ctx, RollupQueryParams{
		StartMS: baseTime - 1000,
		EndMS:   baseTime + 10*HourBucketMS,
	})
	if err != nil {
		t.Fatalf("QuerySummary: %v", err)
	}

	// Acceptance: Rollups reconcile with raw rows (checksum test)
	if summary.Requests != rawRequests {
		t.Errorf("requests mismatch: rollup=%d raw=%d", summary.Requests, rawRequests)
	}
	if summary.Errors != rawErrors {
		t.Errorf("errors mismatch: rollup=%d raw=%d", summary.Errors, rawErrors)
	}
	if summary.PromptTokens != rawPrompt {
		t.Errorf("prompt_tokens mismatch: rollup=%d raw=%d", summary.PromptTokens, rawPrompt)
	}
	if summary.CompletionTokens != rawCompletion {
		t.Errorf("completion_tokens mismatch: rollup=%d raw=%d", summary.CompletionTokens, rawCompletion)
	}
	if summary.TotalTokens != rawTotal {
		t.Errorf("total_tokens mismatch: rollup=%d raw=%d", summary.TotalTokens, rawTotal)
	}
	if summary.CostUSDMicros != rawCost {
		t.Errorf("cost_usd_micros mismatch: rollup=%d raw=%d", summary.CostUSDMicros, rawCost)
	}

	// 3. Range query sum must match summary sum
	rangeItems, err := rollupRepo.QueryRange(ctx, RollupQueryParams{
		StartMS: baseTime - 1000,
		EndMS:   baseTime + 10*HourBucketMS,
	})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}

	var rangeRequests, rangeCost int64
	for _, item := range rangeItems {
		rangeRequests += item.Requests
		rangeCost += item.CostUSDMicros
	}
	if rangeRequests != rawRequests {
		t.Errorf("range items requests mismatch: sum=%d raw=%d", rangeRequests, rawRequests)
	}
	if rangeCost != rawCost {
		t.Errorf("range items cost mismatch: sum=%d raw=%d", rangeCost, rawCost)
	}
}

func TestRollups_DailyAggregation(t *testing.T) {
	s := openMigrated(t)
	repo := s.Requests()
	rollupRepo := s.Rollups()
	ctx := context.Background()

	day1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	day2 := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC).UnixMilli()

	// Insert requests across 24 hours of day 1
	for h := 0; h < 24; h++ {
		_ = repo.Insert(domain.RequestRecord{
			ID:               fmt.Sprintf("day1-%d", h),
			TraceID:          fmt.Sprintf("day1-%d", h),
			VirtualKeyID:     "vkey-daily",
			ModelRequested:   "gpt-4o",
			Status:           domain.RequestSuccess,
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
			CostUSDMicros:    30,
			CreatedMS:        day1 + int64(h)*HourBucketMS,
		})
	}

	// Query with StepMS: DayBucketMS
	dailyUsage, err := rollupRepo.QueryRange(ctx, RollupQueryParams{
		VirtualKeyID: "vkey-daily",
		StartMS:      day1,
		EndMS:        day2 + DayBucketMS,
		StepMS:       DayBucketMS,
	})
	if err != nil {
		t.Fatalf("QueryRange daily: %v", err)
	}

	if len(dailyUsage) != 1 {
		t.Fatalf("expected 1 daily bucket, got %d", len(dailyUsage))
	}

	d := dailyUsage[0]
	if d.BucketStartMS != day1 {
		t.Errorf("expected bucket start %d, got %d", day1, d.BucketStartMS)
	}
	if d.Requests != 24 {
		t.Errorf("expected 24 requests, got %d", d.Requests)
	}
	if d.TotalTokens != 24*15 {
		t.Errorf("expected %d tokens, got %d", 24*15, d.TotalTokens)
	}
	if d.CostUSDMicros != 24*30 {
		t.Errorf("expected %d cost micros, got %d", 24*30, d.CostUSDMicros)
	}
}

func TestRollups_RecomputeFromRaw(t *testing.T) {
	s := openMigrated(t)
	repo := s.Requests()
	rollupRepo := s.Rollups()
	ctx := context.Background()

	baseTime := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < 20; i++ {
		_ = repo.Insert(domain.RequestRecord{
			ID:             fmt.Sprintf("recomp-%d", i),
			TraceID:        fmt.Sprintf("recomp-%d", i),
			VirtualKeyID:   "vkey-recomp",
			ModelRequested: "gpt-4o",
			Status:         domain.RequestSuccess,
			TotalTokens:    100,
			CostUSDMicros:  200,
			CreatedMS:      baseTime + int64(i*1000),
		})
	}

	// Purge usage_rollups to simulate recovery/repair scenario
	_, err := s.db.Exec("DELETE FROM usage_rollups")
	if err != nil {
		t.Fatalf("delete rollups: %v", err)
	}

	// Recompute
	if err := rollupRepo.RecomputeFromRaw(ctx, baseTime-1000, baseTime+HourBucketMS); err != nil {
		t.Fatalf("RecomputeFromRaw: %v", err)
	}

	summary, err := rollupRepo.QuerySummary(ctx, RollupQueryParams{
		VirtualKeyID: "vkey-recomp",
		StartMS:      baseTime - 1000,
		EndMS:        baseTime + HourBucketMS,
	})
	if err != nil {
		t.Fatalf("QuerySummary after recompute: %v", err)
	}

	if summary.Requests != 20 {
		t.Fatalf("expected 20 requests after recompute, got %d", summary.Requests)
	}
	if summary.TotalTokens != 2000 {
		t.Fatalf("expected 2000 tokens after recompute, got %d", summary.TotalTokens)
	}
	if summary.CostUSDMicros != 4000 {
		t.Fatalf("expected 4000 cost micros after recompute, got %d", summary.CostUSDMicros)
	}
}
