package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// ErrNotFound is returned by repositories when the requested row is absent.
var ErrNotFound = errors.New("storage: not found")

// nowMS is swappable for deterministic tests.
var nowMS = func() int64 { return time.Now().UnixMilli() }

func newID(prefix string) string {
	return fmt.Sprintf("%s_%d_%06d", prefix, nowMS(), time.Now().UnixNano()%1_000_000)
}

// ---------------------------------------------------------------------------
// ProviderRepo
// ---------------------------------------------------------------------------

// ProviderRepo persists providers. API key material lives in APIKeyEnc as
// an opaque encrypted blob produced by internal/auth; this repo never
// knows the plaintext.
type ProviderRepo struct{ s *Store }

// ProviderRecord is the storage shape of a provider.
type ProviderRecord struct {
	domain.Provider
	APIKeyEnc []byte // encrypted blob; nil = no key stored
}

func (s *Store) Providers() *ProviderRepo { return &ProviderRepo{s} }

// Upsert inserts or updates a provider, assigning ID/CreatedMS when empty.
// The record is updated in place so callers observe the assigned ID.
func (r *ProviderRepo) Upsert(rec *ProviderRecord) error {
	if rec.ID == "" {
		rec.ID = newID("prov")
	}
	if rec.CreatedMS == 0 {
		rec.CreatedMS = nowMS()
	}
	if rec.APIKeyEnc == nil {
		rec.APIKeyEnc = []byte{} // nil would bind as SQL NULL
	}
	rec.UpdatedMS = nowMS()
	_, err := r.s.db.Exec(`INSERT INTO providers
                (id, name, protocol, base_url, api_key_enc, enabled, created_ms, updated_ms)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                        name = excluded.name,
                        protocol = excluded.protocol,
                        base_url = excluded.base_url,
                        api_key_enc = excluded.api_key_enc,
                        enabled = excluded.enabled,
                        updated_ms = excluded.updated_ms`,
		rec.ID, rec.Name, string(rec.Protocol), rec.BaseURL, rec.APIKeyEnc,
		boolToInt(rec.Enabled), rec.CreatedMS, rec.UpdatedMS)
	if err != nil {
		return fmt.Errorf("storage: upsert provider: %w", err)
	}
	return nil
}

func (r *ProviderRepo) Get(id string) (ProviderRecord, error) {
	row := r.s.db.QueryRow(`SELECT id, name, protocol, base_url, api_key_enc,
                enabled, created_ms, updated_ms FROM providers WHERE id = ?`, id)
	return scanProvider(row)
}

func (r *ProviderRepo) List() ([]ProviderRecord, error) {
	rows, err := r.s.db.Query(`SELECT id, name, protocol, base_url, api_key_enc,
                enabled, created_ms, updated_ms FROM providers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list providers: %w", err)
	}
	defer rows.Close()
	var out []ProviderRecord
	for rows.Next() {
		rec, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *ProviderRepo) Delete(id string) error {
	res, err := r.s.db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete provider: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanProvider(row rowScanner) (ProviderRecord, error) {
	var rec ProviderRecord
	var protocol string
	var enabled int
	var keyEnc []byte
	if err := row.Scan(&rec.ID, &rec.Name, &protocol, &rec.BaseURL, &keyEnc,
		&enabled, &rec.CreatedMS, &rec.UpdatedMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rec, ErrNotFound
		}
		return rec, fmt.Errorf("storage: scan provider: %w", err)
	}
	rec.Protocol = domain.ProviderProtocol(protocol)
	rec.Enabled = enabled != 0
	rec.APIKeyEnc = keyEnc
	return rec, nil
}

// ---------------------------------------------------------------------------
// ModelRepo (models + their targets)
// ---------------------------------------------------------------------------

// ModelRepo persists canonical models with their provider targets.
type ModelRepo struct{ s *Store }

func (s *Store) Models() *ModelRepo { return &ModelRepo{s} }

func (r *ModelRepo) Upsert(m domain.Model) error {
	if m.ID == "" {
		return fmt.Errorf("storage: model id required")
	}
	if m.CreatedMS == 0 {
		m.CreatedMS = nowMS()
	}
	aliases, err := json.Marshal(m.Aliases)
	if err != nil {
		return fmt.Errorf("storage: marshal aliases: %w", err)
	}
	caps, err := json.Marshal(m.Capabilities)
	if err != nil {
		return fmt.Errorf("storage: marshal capabilities: %w", err)
	}

	tx, err := r.s.db.Begin()
	if err != nil {
		return fmt.Errorf("storage: begin model upsert: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`INSERT INTO models
                (id, aliases_json, caps_json, created_ms, updated_ms)
                VALUES (?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                        aliases_json = excluded.aliases_json,
                        caps_json = excluded.caps_json,
                        updated_ms = excluded.updated_ms`,
		m.ID, string(aliases), string(caps), m.CreatedMS, nowMS()); err != nil {
		return fmt.Errorf("storage: upsert model: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM model_targets WHERE model_id = ?`, m.ID); err != nil {
		return fmt.Errorf("storage: clear model targets: %w", err)
	}
	for i, t := range m.Targets {
		pos := t.Position
		if pos == 0 {
			pos = i
		}
		if t.Weight <= 0 {
			t.Weight = 1
		}
		if t.CostMultiplier == 0 {
			t.CostMultiplier = 100
		}
		capsJSON := "{}"
		if t.Capabilities != nil {
			b, err := json.Marshal(t.Capabilities)
			if err != nil {
				return fmt.Errorf("storage: marshal target capabilities: %w", err)
			}
			capsJSON = string(b)
		}
		if _, err := tx.Exec(`INSERT INTO model_targets
                        (model_id, provider_id, provider_model, weight, position, cost_multiplier, caps_json)
                        VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.ID, t.ProviderID, t.ProviderModel, t.Weight, pos, t.CostMultiplier, capsJSON); err != nil {
			return fmt.Errorf("storage: insert model target: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit model upsert: %w", err)
	}
	return nil
}

func (r *ModelRepo) Get(id string) (domain.Model, error) {
	row := r.s.db.QueryRow(`SELECT id, aliases_json, caps_json, created_ms, updated_ms
                FROM models WHERE id = ?`, id)
	m, err := scanModel(row)
	if err != nil {
		return m, err
	}
	targets, err := r.targets(id)
	if err != nil {
		return m, err
	}
	m.Targets = targets
	return m, nil
}

func (r *ModelRepo) List() ([]domain.Model, error) {
	rows, err := r.s.db.Query(`SELECT id, aliases_json, caps_json, created_ms, updated_ms
                FROM models ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list models: %w", err)
	}
	var out []domain.Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range out {
		targets, err := r.targets(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Targets = targets
	}
	return out, nil
}

func (r *ModelRepo) targets(modelID string) ([]domain.ModelTarget, error) {
	rows, err := r.s.db.Query(`SELECT provider_id, provider_model, weight, position, cost_multiplier, caps_json
                FROM model_targets WHERE model_id = ? ORDER BY position, provider_id`, modelID)
	if err != nil {
		return nil, fmt.Errorf("storage: list model targets: %w", err)
	}
	defer rows.Close()
	var out []domain.ModelTarget
	for rows.Next() {
		var t domain.ModelTarget
		var capsJSON string
		if err := rows.Scan(&t.ProviderID, &t.ProviderModel, &t.Weight, &t.Position, &t.CostMultiplier, &capsJSON); err != nil {
			return nil, fmt.Errorf("storage: scan model target: %w", err)
		}
		if capsJSON != "" && capsJSON != "{}" {
			var caps domain.ModelCapabilities
			if err := json.Unmarshal([]byte(capsJSON), &caps); err == nil {
				t.Capabilities = &caps
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanModel(row rowScanner) (domain.Model, error) {
	var m domain.Model
	var aliases, caps string
	if err := row.Scan(&m.ID, &aliases, &caps, &m.CreatedMS, &m.UpdatedMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return m, ErrNotFound
		}
		return m, fmt.Errorf("storage: scan model: %w", err)
	}
	_ = json.Unmarshal([]byte(aliases), &m.Aliases)
	_ = json.Unmarshal([]byte(caps), &m.Capabilities)
	return m, nil
}

func (r *ModelRepo) Delete(id string) error {
	res, err := r.s.db.Exec(`DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete model: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// RoutingRuleRepo
// ---------------------------------------------------------------------------

// RoutingRuleRepo persists fallback routing rules for models.
type RoutingRuleRepo struct{ s *Store }

func (s *Store) RoutingRules() *RoutingRuleRepo { return &RoutingRuleRepo{s} }

// Upsert inserts or updates a routing rule.
func (r *RoutingRuleRepo) Upsert(rule *domain.RoutingRule) error {
	if rule.ID == "" {
		rule.ID = newID("rule")
	}
	if rule.Policy == "" {
		rule.Policy = domain.PolicyOrdered
	}
	_, err := r.s.db.Exec(`INSERT INTO routing_rules
                (id, model_id, policy, enabled, position)
                VALUES (?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                        model_id = excluded.model_id,
                        policy = excluded.policy,
                        enabled = excluded.enabled,
                        position = excluded.position`,
		rule.ID, rule.ModelID, string(rule.Policy), boolToInt(rule.Enabled), rule.Position)
	if err != nil {
		return fmt.Errorf("storage: upsert routing rule: %w", err)
	}
	return nil
}

func (r *RoutingRuleRepo) Get(id string) (domain.RoutingRule, error) {
	row := r.s.db.QueryRow(`SELECT id, model_id, policy, enabled, position
                FROM routing_rules WHERE id = ?`, id)
	return scanRoutingRule(row)
}

func (r *RoutingRuleRepo) GetByModel(modelID string) (domain.RoutingRule, error) {
	row := r.s.db.QueryRow(`SELECT id, model_id, policy, enabled, position
                FROM routing_rules WHERE model_id = ? ORDER BY position LIMIT 1`, modelID)
	return scanRoutingRule(row)
}

func (r *RoutingRuleRepo) List() ([]domain.RoutingRule, error) {
	rows, err := r.s.db.Query(`SELECT id, model_id, policy, enabled, position
                FROM routing_rules ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list routing rules: %w", err)
	}
	defer rows.Close()
	var out []domain.RoutingRule
	for rows.Next() {
		rule, err := scanRoutingRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

func (r *RoutingRuleRepo) Delete(id string) error {
	res, err := r.s.db.Exec(`DELETE FROM routing_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete routing rule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanRoutingRule(row rowScanner) (domain.RoutingRule, error) {
	var rule domain.RoutingRule
	var policy string
	var enabled int
	if err := row.Scan(&rule.ID, &rule.ModelID, &policy, &enabled, &rule.Position); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rule, ErrNotFound
		}
		return rule, fmt.Errorf("storage: scan routing rule: %w", err)
	}
	rule.Policy = domain.FallbackPolicy(policy)
	rule.Enabled = enabled != 0
	return rule, nil
}

// ---------------------------------------------------------------------------
// VirtualKeyRepo
// ---------------------------------------------------------------------------

// VirtualKeyRepo persists virtual keys. KeyHash holds the argon2id hash;
// plaintext keys never reach storage.
type VirtualKeyRepo struct{ s *Store }

func (s *Store) VirtualKeys() *VirtualKeyRepo { return &VirtualKeyRepo{s} }

func (r *VirtualKeyRepo) Create(k domain.VirtualKey) (domain.VirtualKey, error) {
	if k.ID == "" {
		k.ID = newID("vkey")
	}
	if k.CreatedMS == 0 {
		k.CreatedMS = nowMS()
	}
	if k.Status == "" {
		k.Status = domain.KeyActive
	}
	scopes, err := json.Marshal(k.Scopes)
	if err != nil {
		return k, fmt.Errorf("storage: marshal scopes: %w", err)
	}
	limits, err := json.Marshal(k.Limits)
	if err != nil {
		return k, fmt.Errorf("storage: marshal limits: %w", err)
	}
	_, err = r.s.db.Exec(`INSERT INTO virtual_keys
                (id, name, prefix, key_hash, scopes_json, limits_json, status, created_ms, expires_ms, last_used_ms)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.Name, k.Prefix, k.KeyHash, string(scopes), string(limits),
		string(k.Status), k.CreatedMS, k.ExpiresMS, k.LastUsedMS)
	if err != nil {
		return k, fmt.Errorf("storage: create vkey: %w", err)
	}
	return k, nil
}

func (r *VirtualKeyRepo) Get(id string) (domain.VirtualKey, error) {
	return scanVKey(r.s.db.QueryRow(`SELECT id, name, prefix, key_hash, scopes_json,
                limits_json, status, created_ms, expires_ms, last_used_ms
                FROM virtual_keys WHERE id = ?`, id))
}

// GetByHash looks a key up by its hash (the proxy auth path).
func (r *VirtualKeyRepo) GetByHash(hash string) (domain.VirtualKey, error) {
	return scanVKey(r.s.db.QueryRow(`SELECT id, name, prefix, key_hash, scopes_json,
                limits_json, status, created_ms, expires_ms, last_used_ms
                FROM virtual_keys WHERE key_hash = ?`, hash))
}

func (r *VirtualKeyRepo) UpdateStatus(id string, status domain.KeyStatus) error {
	res, err := r.s.db.Exec(`UPDATE virtual_keys SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("storage: update vkey status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *VirtualKeyRepo) TouchLastUsed(id string, atMS int64) error {
	_, err := r.s.db.Exec(`UPDATE virtual_keys SET last_used_ms = ? WHERE id = ?`, atMS, id)
	return err // missing row is fine here (best effort)
}

func (r *VirtualKeyRepo) List() ([]domain.VirtualKey, error) {
	rows, err := r.s.db.Query(`SELECT id, name, prefix, key_hash, scopes_json,
                limits_json, status, created_ms, expires_ms, last_used_ms
                FROM virtual_keys ORDER BY created_ms DESC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list vkeys: %w", err)
	}
	defer rows.Close()
	var out []domain.VirtualKey
	for rows.Next() {
		k, err := scanVKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *VirtualKeyRepo) Delete(id string) error {
	res, err := r.s.db.Exec(`DELETE FROM virtual_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete vkey: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *VirtualKeyRepo) UpdateScopes(id string, scopes domain.KeyScopes) error {
	b, err := json.Marshal(scopes)
	if err != nil {
		return fmt.Errorf("storage: marshal scopes: %w", err)
	}
	res, err := r.s.db.Exec(`UPDATE virtual_keys SET scopes_json = ? WHERE id = ?`, string(b), id)
	if err != nil {
		return fmt.Errorf("storage: update vkey scopes: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *VirtualKeyRepo) UpdateLimits(id string, limits domain.KeyLimits) error {
	b, err := json.Marshal(limits)
	if err != nil {
		return fmt.Errorf("storage: marshal limits: %w", err)
	}
	res, err := r.s.db.Exec(`UPDATE virtual_keys SET limits_json = ? WHERE id = ?`, string(b), id)
	if err != nil {
		return fmt.Errorf("storage: update vkey limits: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanVKey(row rowScanner) (domain.VirtualKey, error) {
	var k domain.VirtualKey
	var scopes, limits, status string
	if err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.KeyHash, &scopes, &limits,
		&status, &k.CreatedMS, &k.ExpiresMS, &k.LastUsedMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return k, ErrNotFound
		}
		return k, fmt.Errorf("storage: scan vkey: %w", err)
	}
	k.Status = domain.KeyStatus(status)
	_ = json.Unmarshal([]byte(scopes), &k.Scopes)
	_ = json.Unmarshal([]byte(limits), &k.Limits)
	return k, nil
}

// ---------------------------------------------------------------------------
// RequestRepo
// ---------------------------------------------------------------------------

// RequestRepo persists request records (written by the Phase 5 usage
// pipeline, read by analytics).
type RequestRepo struct{ s *Store }

func (s *Store) Requests() *RequestRepo { return &RequestRepo{s} }

func (r *RequestRepo) Insert(rec domain.RequestRecord) error {
	return r.InsertBatchCtx(context.Background(), []domain.RequestRecord{rec})
}

// InsertBatch writes multiple request records within a single transaction,
// maintaining hourly usage rollups atomically.
func (r *RequestRepo) InsertBatch(records []domain.RequestRecord) error {
	return r.InsertBatchCtx(context.Background(), records)
}

// InsertBatchCtx writes multiple request records within a single transaction,
// maintaining hourly usage rollups atomically and honoring context cancellation.
func (r *RequestRepo) InsertBatchCtx(ctx context.Context, records []domain.RequestRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin insert batch: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO requests
                (id, trace_id, vkey_id, model_requested, model_served, provider_id, status,
                 error_code, stream, prompt_tokens, completion_tokens, total_tokens,
                 cost_usd_micros, latency_ms, ttft_ms, attempts, created_ms)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("storage: prepare insert batch: %w", err)
	}
	defer stmt.Close()

	for _, rec := range records {
		if rec.ID == "" {
			rec.ID = rec.TraceID
		}
		_, err := stmt.ExecContext(ctx,
			rec.ID, rec.TraceID, rec.VirtualKeyID, rec.ModelRequested, rec.ModelServed,
			rec.ProviderID, string(rec.Status), rec.ErrorCode, boolToInt(rec.Stream),
			rec.PromptTokens, rec.CompletionTokens, rec.TotalTokens, rec.CostUSDMicros,
			rec.LatencyMS, rec.TTFTMS, rec.Attempts, rec.CreatedMS)
		if err != nil {
			return fmt.Errorf("storage: insert batch row: %w", err)
		}
	}

	// Incrementally update usage_rollups in the same transaction
	if err := r.s.Rollups().incrementBatchTx(ctx, tx, records); err != nil {
		return fmt.Errorf("storage: update rollups in batch: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit insert batch: %w", err)
	}
	return nil
}

// Page is a cursor-paginated result window.
type Page[T any] struct {
	Items   []T
	NextCur string // "" when no more pages
}

// ListByTime returns requests newest-first, keyed by (created_ms, id).
// cursor is opaque; empty string starts from the newest.
func (r *RequestRepo) ListByTime(vkeyID string, limit int, cursor string) (Page[domain.RequestRecord], error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	where := ""
	args := []any{}
	conds := []string{}
	if vkeyID != "" {
		conds = append(conds, "vkey_id = ?")
		args = append(args, vkeyID)
	}
	if cur, ok := decodeCursor(cursor); ok {
		// strictly older than the cursor position
		conds = append(conds, "(created_ms < ? OR (created_ms = ? AND id < ?))")
		args = append(args, cur.CreatedMS, cur.CreatedMS, cur.ID)
	}
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit+1) // +1 to detect the next page

	rows, err := r.s.db.Query(`SELECT id, trace_id, vkey_id, model_requested, model_served,
                provider_id, status, error_code, stream, prompt_tokens, completion_tokens,
                total_tokens, cost_usd_micros, latency_ms, ttft_ms, attempts, created_ms
                FROM requests `+where+` ORDER BY created_ms DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return Page[domain.RequestRecord]{}, fmt.Errorf("storage: list requests: %w", err)
	}
	defer rows.Close()

	var items []domain.RequestRecord
	for rows.Next() {
		rec, err := scanRequest(rows)
		if err != nil {
			return Page[domain.RequestRecord]{}, err
		}
		items = append(items, rec)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.RequestRecord]{}, err
	}

	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = encodeCursor(last.CreatedMS, last.ID)
	}
	return Page[domain.RequestRecord]{Items: items, NextCur: next}, nil
}

func scanRequest(row rowScanner) (domain.RequestRecord, error) {
	var rec domain.RequestRecord
	var status string
	var stream int
	if err := row.Scan(&rec.ID, &rec.TraceID, &rec.VirtualKeyID, &rec.ModelRequested,
		&rec.ModelServed, &rec.ProviderID, &status, &rec.ErrorCode, &stream,
		&rec.PromptTokens, &rec.CompletionTokens, &rec.TotalTokens,
		&rec.CostUSDMicros, &rec.LatencyMS, &rec.TTFTMS, &rec.Attempts,
		&rec.CreatedMS); err != nil {
		return rec, fmt.Errorf("storage: scan request: %w", err)
	}
	rec.Status = domain.RequestStatus(status)
	rec.Stream = stream != 0
	return rec, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
