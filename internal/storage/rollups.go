package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// Standard bucket duration constants in milliseconds.
const (
	HourBucketMS int64 = 3600 * 1000  // 1 hour
	DayBucketMS  int64 = 86400 * 1000 // 1 day (24 hours)
)

// HourlyBucketStart rounds tsMS down to the beginning of the containing hour (UTC).
func HourlyBucketStart(tsMS int64) int64 {
	if tsMS <= 0 {
		return 0
	}
	return (tsMS / HourBucketMS) * HourBucketMS
}

// DailyBucketStart rounds tsMS down to the beginning of the containing day (UTC).
func DailyBucketStart(tsMS int64) int64 {
	if tsMS <= 0 {
		return 0
	}
	return (tsMS / DayBucketMS) * DayBucketMS
}

// RollupRepo provides incremental maintenance and aggregation queries over
// the usage_rollups table (Phase 5).
type RollupRepo struct {
	s *Store
}

// Rollups returns the RollupRepo accessor.
func (s *Store) Rollups() *RollupRepo { return &RollupRepo{s} }

// RollupQueryParams defines the filtering and time window for aggregation queries.
type RollupQueryParams struct {
	VirtualKeyID string
	ModelID      string
	ProviderID   string
	StartMS      int64
	EndMS        int64
	// StepMS controls bucket aggregation (typically HourBucketMS or DayBucketMS).
	// Defaults to HourBucketMS.
	StepMS int64
}

// Increment incrementally updates hourly rollups with a single request record.
func (r *RollupRepo) Increment(rec domain.RequestRecord) error {
	return r.IncrementBatch([]domain.RequestRecord{rec})
}

// IncrementBatch incrementally updates hourly rollups with a batch of request records.
func (r *RollupRepo) IncrementBatch(records []domain.RequestRecord) error {
	return r.IncrementBatchCtx(context.Background(), records)
}

// IncrementBatchCtx incrementally updates hourly rollups with a batch of request records,
// honoring context cancellation.
func (r *RollupRepo) IncrementBatchCtx(ctx context.Context, records []domain.RequestRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin rollup tx: %w", err)
	}
	defer tx.Rollback()

	if err := r.incrementBatchTx(ctx, tx, records); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit rollup tx: %w", err)
	}
	return nil
}

type rollupKey struct {
	bucketStartMS int64
	vkeyID        string
	modelID       string
	providerID    string
}

// incrementBatchTx aggregates records in memory and executes UPSERTs inside tx.
func (r *RollupRepo) incrementBatchTx(ctx context.Context, tx *sql.Tx, records []domain.RequestRecord) error {
	if len(records) == 0 {
		return nil
	}

	// In-memory aggregation by (bucket, vkey, model, provider)
	buckets := make(map[rollupKey]*domain.Usage)
	for _, rec := range records {
		bucketStart := HourlyBucketStart(rec.CreatedMS)
		model := rec.ModelServed
		if model == "" {
			model = rec.ModelRequested
		}
		key := rollupKey{
			bucketStartMS: bucketStart,
			vkeyID:        rec.VirtualKeyID,
			modelID:       model,
			providerID:    rec.ProviderID,
		}

		u, exists := buckets[key]
		if !exists {
			u = &domain.Usage{
				BucketStartMS: bucketStart,
				VirtualKeyID:  rec.VirtualKeyID,
				ModelID:       model,
				ProviderID:    rec.ProviderID,
			}
			buckets[key] = u
		}

		u.Requests++
		if rec.Status == domain.RequestError {
			u.Errors++
		}
		u.PromptTokens += rec.PromptTokens
		u.CompletionTokens += rec.CompletionTokens
		u.TotalTokens += rec.TotalTokens
		u.CostUSDMicros += rec.CostUSDMicros
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO usage_rollups
		(bucket_start_ms, vkey_id, model_id, provider_id,
		 requests, errors, prompt_tokens, completion_tokens, total_tokens, cost_usd_micros)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (bucket_start_ms, vkey_id, model_id, provider_id) DO UPDATE SET
			requests          = requests + excluded.requests,
			errors            = errors + excluded.errors,
			prompt_tokens     = prompt_tokens + excluded.prompt_tokens,
			completion_tokens = completion_tokens + excluded.completion_tokens,
			total_tokens      = total_tokens + excluded.total_tokens,
			cost_usd_micros   = cost_usd_micros + excluded.cost_usd_micros`)
	if err != nil {
		return fmt.Errorf("storage: prepare rollup upsert: %w", err)
	}
	defer stmt.Close()

	for key, u := range buckets {
		_, err := stmt.ExecContext(ctx,
			key.bucketStartMS, key.vkeyID, key.modelID, key.providerID,
			u.Requests, u.Errors, u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.CostUSDMicros)
		if err != nil {
			return fmt.Errorf("storage: exec rollup upsert: %w", err)
		}
	}
	return nil
}

// QueryRange returns aggregated usage buckets within [StartMS, EndMS).
func (r *RollupRepo) QueryRange(ctx context.Context, params RollupQueryParams) ([]domain.Usage, error) {
	step := params.StepMS
	if step <= 0 {
		step = HourBucketMS
	}

	where, args := r.buildWhere(params)

	var query string
	if step >= DayBucketMS {
		query = fmt.Sprintf(`SELECT (bucket_start_ms / %d) * %d AS bucket_ts,
			COALESCE(vkey_id, ''), COALESCE(model_id, ''), COALESCE(provider_id, ''),
			SUM(requests), SUM(errors), SUM(prompt_tokens), SUM(completion_tokens), SUM(total_tokens), SUM(cost_usd_micros)
			FROM usage_rollups %s
			GROUP BY bucket_ts, vkey_id, model_id, provider_id
			ORDER BY bucket_ts ASC, vkey_id ASC, model_id ASC`, DayBucketMS, DayBucketMS, where)
	} else {
		query = fmt.Sprintf(`SELECT bucket_start_ms,
			COALESCE(vkey_id, ''), COALESCE(model_id, ''), COALESCE(provider_id, ''),
			SUM(requests), SUM(errors), SUM(prompt_tokens), SUM(completion_tokens), SUM(total_tokens), SUM(cost_usd_micros)
			FROM usage_rollups %s
			GROUP BY bucket_start_ms, vkey_id, model_id, provider_id
			ORDER BY bucket_start_ms ASC, vkey_id ASC, model_id ASC`, where)
	}

	rows, err := r.s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query rollups range: %w", err)
	}
	defer rows.Close()

	var result []domain.Usage
	for rows.Next() {
		var u domain.Usage
		if err := rows.Scan(
			&u.BucketStartMS, &u.VirtualKeyID, &u.ModelID, &u.ProviderID,
			&u.Requests, &u.Errors, &u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.CostUSDMicros,
		); err != nil {
			return nil, fmt.Errorf("storage: scan rollup: %w", err)
		}
		result = append(result, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: rows err in query rollups: %w", err)
	}
	return result, nil
}

// QuerySummary returns aggregate totals across the requested time window and filters.
func (r *RollupRepo) QuerySummary(ctx context.Context, params RollupQueryParams) (domain.Usage, error) {
	where, args := r.buildWhere(params)

	query := fmt.Sprintf(`SELECT
		COALESCE(SUM(requests), 0),
		COALESCE(SUM(errors), 0),
		COALESCE(SUM(prompt_tokens), 0),
		COALESCE(SUM(completion_tokens), 0),
		COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(cost_usd_micros), 0)
		FROM usage_rollups %s`, where)

	var u domain.Usage
	u.BucketStartMS = params.StartMS
	u.VirtualKeyID = params.VirtualKeyID
	u.ModelID = params.ModelID
	u.ProviderID = params.ProviderID

	row := r.s.db.QueryRowContext(ctx, query, args...)
	if err := row.Scan(
		&u.Requests, &u.Errors, &u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.CostUSDMicros,
	); err != nil {
		return u, fmt.Errorf("storage: scan rollup summary: %w", err)
	}
	return u, nil
}

// RecomputeFromRaw recalculates usage_rollups for the given window [startMS, endMS)
// directly from the raw requests table.
func (r *RollupRepo) RecomputeFromRaw(ctx context.Context, startMS, endMS int64) error {
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin recompute tx: %w", err)
	}
	defer tx.Rollback()

	// Clear existing rollups in range
	where := ""
	args := []any{}
	if startMS > 0 && endMS > 0 {
		where = "WHERE bucket_start_ms >= ? AND bucket_start_ms < ?"
		args = append(args, startMS, endMS)
	} else if startMS > 0 {
		where = "WHERE bucket_start_ms >= ?"
		args = append(args, startMS)
	} else if endMS > 0 {
		where = "WHERE bucket_start_ms < ?"
		args = append(args, endMS)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM usage_rollups "+where, args...); err != nil {
		return fmt.Errorf("storage: delete rollups in recompute: %w", err)
	}

	// Recompute from requests table
	reqWhere := ""
	reqArgs := []any{}
	if startMS > 0 && endMS > 0 {
		reqWhere = "WHERE created_ms >= ? AND created_ms < ?"
		reqArgs = append(reqArgs, startMS, endMS)
	} else if startMS > 0 {
		reqWhere = "WHERE created_ms >= ?"
		reqArgs = append(reqArgs, startMS)
	} else if endMS > 0 {
		reqWhere = "WHERE created_ms < ?"
		reqArgs = append(reqArgs, endMS)
	}

	recomputeSQL := fmt.Sprintf(`INSERT INTO usage_rollups
		(bucket_start_ms, vkey_id, model_id, provider_id,
		 requests, errors, prompt_tokens, completion_tokens, total_tokens, cost_usd_micros)
		SELECT
			(created_ms / %d) * %d AS bucket_start,
			COALESCE(vkey_id, ''),
			COALESCE(CASE WHEN model_served != '' THEN model_served ELSE model_requested END, '') AS m_id,
			COALESCE(provider_id, ''),
			COUNT(*),
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END),
			SUM(prompt_tokens),
			SUM(completion_tokens),
			SUM(total_tokens),
			SUM(cost_usd_micros)
		FROM requests %s
		GROUP BY bucket_start, vkey_id, m_id, provider_id`, HourBucketMS, HourBucketMS, reqWhere)

	if _, err := tx.ExecContext(ctx, recomputeSQL, reqArgs...); err != nil {
		return fmt.Errorf("storage: insert recomputed rollups: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit recompute tx: %w", err)
	}
	return nil
}

func (r *RollupRepo) buildWhere(params RollupQueryParams) (string, []any) {
	var conds []string
	var args []any

	if params.StartMS > 0 {
		conds = append(conds, "bucket_start_ms >= ?")
		args = append(args, params.StartMS)
	}
	if params.EndMS > 0 {
		conds = append(conds, "bucket_start_ms < ?")
		args = append(args, params.EndMS)
	}
	if params.VirtualKeyID != "" {
		conds = append(conds, "vkey_id = ?")
		args = append(args, params.VirtualKeyID)
	}
	if params.ModelID != "" {
		conds = append(conds, "model_id = ?")
		args = append(args, params.ModelID)
	}
	if params.ProviderID != "" {
		conds = append(conds, "provider_id = ?")
		args = append(args, params.ProviderID)
	}

	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}
