package importer

// Virtual key + usage history migration (p7.data-import).
//
// Key re-hash handling: OmniRoute v3.8.52 stored key hashes as salted
// scrypt envelopes — raw keys are cryptographically unrecoverable, and
// OneGate's verification uses a peppered HMAC under the data-dir master
// key. Legacy hashes therefore CANNOT be re-used. Instead every legacy
// key is re-minted as a fresh `ogk-` key preserving all metadata
// (name, scopes, limits, status, created/expiry/last-used); the new raw
// key is shown exactly once for redistribution. Revoked keys import as
// revoked (no raw key needed — they can never authenticate).
//
// Usage history: legacy `usage_events` rows import as OneGate request
// records (id `imp_<legacy-id>`) with float dollars converted to integer
// micro-USD. Aggregates (requests, tokens, cost per bucket/key/model)
// are preserved because OneGate rollups compute from request records.
import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// MintedKey is a show-once result of the key import.
type MintedKey struct {
	LegacyID string
	Name     string
	RawKey   string // ogk-… shown exactly once
	Note     string
}

// KeyImportResult summarizes the key import.
type KeyImportResult struct {
	Minted  []MintedKey
	Skipped []string // already-imported legacy ids
}

// legacyScopes is the camelCase legacy scope block.
type legacyScopes struct {
	AllowedModels    []string          `json:"allowedModels"`
	AllowedProviders []string          `json:"allowedProviders"`
	PolicyOverride   string            `json:"policyOverride"`
	ModelOverrides   map[string]string `json:"modelOverrides"`
}

// legacyLimits is the camelCase legacy limit block (maxSpendUsd is a
// FLOAT in dollars — converted to integer micro-USD).
type legacyLimits struct {
	RPM         int64   `json:"rpm"`
	TPM         int64   `json:"tpm"`
	Concurrency int     `json:"concurrency"`
	MaxSpendUSD float64 `json:"maxSpendUsd"`
}

// translateScopes maps legacy scopes to canonical.
func translateScopes(raw json.RawMessage) (domain.KeyScopes, error) {
	var s domain.KeyScopes
	if len(raw) == 0 {
		return s, nil
	}
	var ls legacyScopes
	if err := json.Unmarshal(raw, &ls); err != nil {
		return s, fmt.Errorf("importer: legacy scopes: %w", err)
	}
	s.AllowedModels = ls.AllowedModels
	s.AllowedProviders = ls.AllowedProviders
	if ls.PolicyOverride != "" {
		if p, ok := mapStrategy(ls.PolicyOverride); ok {
			s.PolicyOverride = p
		}
	}
	for model, strategy := range ls.ModelOverrides {
		if p, ok := mapStrategy(strategy); ok {
			if s.ModelOverrides == nil {
				s.ModelOverrides = map[string]domain.FallbackPolicy{}
			}
			s.ModelOverrides[model] = p
		}
	}
	return s, nil
}

// translateLimits maps legacy limits to canonical micro-USD ints.
func translateLimits(raw json.RawMessage) (domain.KeyLimits, error) {
	var l domain.KeyLimits
	if len(raw) == 0 {
		return l, nil
	}
	var ll legacyLimits
	if err := json.Unmarshal(raw, &ll); err != nil {
		return l, fmt.Errorf("importer: legacy limits: %w", err)
	}
	l.RPM = ll.RPM
	l.TPM = ll.TPM
	l.Concurrency = ll.Concurrency
	l.MaxSpendUSDMicros = int64(math.Round(ll.MaxSpendUSD * 1e6))
	return l, nil
}

// ImportKeys migrates legacy virtual keys. Idempotent via the
// import_mappings table; the legacy hash is never copied (see the
// package comment). Returns show-once raw keys for redistribution.
func ImportKeys(inst *LegacyInstall, store *storage.Store, pepper []byte) (*KeyImportResult, error) {
	res := &KeyImportResult{}
	for _, lk := range inst.Keys {
		if _, done, err := store.ImportMappings().Get("key", lk.ID); err != nil {
			return nil, err
		} else if done {
			res.Skipped = append(res.Skipped, lk.ID)
			continue
		}

		scopes, err := translateScopes(lk.Scopes)
		if err != nil {
			return nil, err
		}
		limits, err := translateLimits(lk.Limits)
		if err != nil {
			return nil, err
		}

		minted := MintedKey{LegacyID: lk.ID, Name: lk.Name}
		k := domain.VirtualKey{
			Name:      lk.Name,
			Prefix:    lk.Prefix,
			Scopes:    scopes,
			Limits:    limits,
			ExpiresMS: lk.ExpiresAt,
			CreatedMS: lk.CreatedAt,
			Status:    domain.KeyActive,
		}
		if lk.Status == "revoked" {
			k.Status = domain.KeyRevoked
			minted.Note = "imported as revoked (no raw key issued)"
		} else {
			raw, prefix := auth.GenerateVirtualKey()
			k.Prefix = prefix
			k.KeyHash = auth.HashVirtualKey(pepper, raw)
			minted.RawKey = raw
		}

		created, err := store.VirtualKeys().Create(k)
		if err != nil {
			return nil, fmt.Errorf("importer: create key %s: %w", lk.ID, err)
		}
		if lk.LastUsed > 0 {
			if err := store.VirtualKeys().TouchLastUsed(created.ID, lk.LastUsed); err != nil {
				return nil, err
			}
		}
		if err := store.ImportMappings().Put(storage.ImportMapping{
			Kind: "key", LegacyID: lk.ID, OneGateID: created.ID,
		}); err != nil {
			return nil, err
		}
		res.Minted = append(res.Minted, minted)
	}
	return res, nil
}

// LegacyUsageEvent is one row of the legacy usage_events table.
type LegacyUsageEvent struct {
	ID               string
	KeyID            string
	Model            string
	Provider         string
	Status           string
	PromptTokens     int64
	CompletionTokens int64
	CostUSD          float64
	LatencyMS        int64
	CreatedMS        int64
}

// UsageImportResult summarizes the usage import.
type UsageImportResult struct {
	Imported int
	Skipped  []string // reason-tagged skip lines
}

// ReadLegacyUsage opens the legacy OmniRoute SQLite database READ-ONLY
// and streams its usage_events. The legacy file is never written.
func ReadLegacyUsage(dbPath string) ([]LegacyUsageEvent, error) {
	if dbPath == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + abs + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("importer: open legacy db: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id, key_id, model, provider, status,
	                       prompt_tokens, completion_tokens, cost_usd, latency_ms, created_at
	                       FROM usage_events ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("importer: legacy usage_events: %w", err)
	}
	defer rows.Close()

	var out []LegacyUsageEvent
	for rows.Next() {
		var e LegacyUsageEvent
		if err := rows.Scan(&e.ID, &e.KeyID, &e.Model, &e.Provider, &e.Status,
			&e.PromptTokens, &e.CompletionTokens, &e.CostUSD, &e.LatencyMS, &e.CreatedMS); err != nil {
			return nil, fmt.Errorf("importer: scan usage row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ImportUsage maps legacy usage rows onto request records. Only rows
// whose key has an import mapping AND whose model exists in OneGate are
// importable; everything else lands in the skip report with a reason.
func ImportUsage(events []LegacyUsageEvent, store *storage.Store) (*UsageImportResult, error) {
	res := &UsageImportResult{}

	knownModels := map[string]bool{}
	models, err := store.Models().List()
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		knownModels[m.ID] = true
		// Legacy rows may reference aliases too.
		for _, a := range m.Aliases {
			knownModels[a] = true
		}
	}

	var batch []domain.RequestRecord
	for _, e := range events {
		if _, done, err := store.ImportMappings().Get("usage_event", e.ID); err != nil {
			return nil, err
		} else if done {
			res.Skipped = append(res.Skipped, fmt.Sprintf("usage[%s]: already imported", e.ID))
			continue
		}
		keyID, mapped, err := store.ImportMappings().Get("key", e.KeyID)
		if err != nil {
			return nil, err
		}
		if !mapped {
			res.Skipped = append(res.Skipped, fmt.Sprintf("usage[%s]: unknown key %q (import keys first)", e.ID, e.KeyID))
			continue
		}
		if !knownModels[e.Model] {
			res.Skipped = append(res.Skipped, fmt.Sprintf("usage[%s]: unmapped model %q", e.ID, e.Model))
			continue
		}

		status := domain.RequestStatus(e.Status)
		switch status {
		case domain.RequestSuccess, domain.RequestError, domain.RequestCancelled:
		default:
			status = domain.RequestSuccess
		}
		rec := domain.RequestRecord{
			ID:               "imp_" + e.ID,
			TraceID:          "imp_" + e.ID,
			VirtualKeyID:     keyID,
			ModelRequested:   e.Model,
			ModelServed:      e.Model,
			ProviderID:       e.Provider,
			Status:           status,
			PromptTokens:     e.PromptTokens,
			CompletionTokens: e.CompletionTokens,
			TotalTokens:      e.PromptTokens + e.CompletionTokens,
			CostUSDMicros:    int64(math.Round(e.CostUSD * 1e6)),
			LatencyMS:        e.LatencyMS,
			Attempts:         1,
			CreatedMS:        e.CreatedMS,
		}
		batch = append(batch, rec)
		if err := store.ImportMappings().Put(storage.ImportMapping{
			Kind: "usage_event", LegacyID: e.ID, OneGateID: rec.ID,
		}); err != nil {
			return nil, err
		}
	}

	if len(batch) > 0 {
		if err := store.Requests().InsertBatch(batch); err != nil {
			return nil, fmt.Errorf("importer: insert usage batch: %w", err)
		}
	}
	res.Imported = len(batch)
	return res, nil
}

// RenderKeysReport formats the key import result (raw keys redacted from
// the generic report; shown once by the CLI under --apply).
func RenderKeysReport(res *KeyImportResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "keys: %d imported, %d skipped (already present)\n", len(res.Minted), len(res.Skipped))
	for _, k := range res.Minted {
		if k.RawKey != "" {
			fmt.Fprintf(&b, "  minted %q (legacy %s) — raw key shown once below\n", k.Name, k.LegacyID)
		} else {
			fmt.Fprintf(&b, "  imported %q (legacy %s) as revoked\n", k.Name, k.LegacyID)
		}
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(&b, "  skipped legacy %s (already imported)\n", s)
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderUsageReport formats the usage import result.
func RenderUsageReport(res *UsageImportResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage events: %d imported, %d skipped\n", res.Imported, len(res.Skipped))
	for _, s := range res.Skipped {
		fmt.Fprintf(&b, "  - %s\n", s)
	}
	return strings.TrimRight(b.String(), "\n")
}
