package importer

// p7.data-import tests: key re-minting preserves metadata, revoked stays
// revoked, idempotency via import_mappings, usage aggregates preserved
// (float dollars -> micro-USD), skip report for unmapped models/keys.
import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// keyInstall has two active keys (one scoped+limited, one plain) and a
// revoked one.
const keyInstall = `{
  "providers": [{"id": "p1", "name": "P", "protocol": "openai", "baseUrl": "https://x.test", "apiKey": "sk"}],
  "models": [{"id": "gpt-4o", "targets": [{"provider": "p1", "model": "gpt-4o"}]}],
  "keys": [
    {"id": "key_prod", "name": "prod", "prefix": "ogk-old1…", "hash": "$scrypt$N$r$p$…", "status": "active",
     "createdAt": 1700000000000, "lastUsedAt": 1700099999000,
     "scopes": {"allowedModels": ["gpt-4o"], "policyOverride": "cost-preferred", "modelOverrides": {"gpt-4o": "weighted"}},
     "limits": {"rpm": 60, "tpm": 90000, "concurrency": 4, "maxSpendUsd": 12.5}},
    {"id": "key_dev", "name": "dev", "prefix": "ogk-old2…", "hash": "$scrypt$…", "status": "active", "createdAt": 1700000001000},
    {"id": "key_dead", "name": "dead", "prefix": "ogk-old3…", "hash": "$scrypt$…", "status": "revoked", "createdAt": 1700000002000}
  ]
}`

func setupKeyStore(t *testing.T) (*storage.Store, []byte) {
	t.Helper()
	dir := t.TempDir()
	master, err := auth.MasterSecret(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatalf("master: %v", err)
	}
	pepper, err := auth.Pepper(master)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "onegate.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// seed the provider + model the usage rows will reference
	if err := store.Providers().Upsert(&storage.ProviderRecord{Provider: domain.Provider{
		ID: "p1", Name: "P", Protocol: domain.ProtocolOpenAI, BaseURL: "https://x.test", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Models().Upsert(domain.Model{
		ID: "gpt-4o", Capabilities: domain.ModelCapabilities{Tools: true, Vision: true, JSONMode: true, Stream: true},
		Targets: []domain.ModelTarget{{ProviderID: "p1", ProviderModel: "gpt-4o", Weight: 1, Position: 1, CostMultiplier: 100}},
	}); err != nil {
		t.Fatal(err)
	}
	return store, pepper
}

func TestImportKeysPreservesMetadata(t *testing.T) {
	inst, err := Parse([]byte(keyInstall))
	if err != nil {
		t.Fatal(err)
	}
	store, pepper := setupKeyStore(t)

	res, err := ImportKeys(inst, store, pepper)
	if err != nil {
		t.Fatalf("import keys: %v", err)
	}
	if len(res.Minted) != 3 {
		t.Fatalf("minted = %d", len(res.Minted))
	}

	keys, _ := store.VirtualKeys().List()
	byName := map[string]domain.VirtualKey{}
	for _, k := range keys {
		byName[k.Name] = k
	}
	prod := byName["prod"]
	if prod.Scopes.AllowedModels == nil || len(prod.Scopes.AllowedModels) != 1 || prod.Scopes.AllowedModels[0] != "gpt-4o" {
		t.Errorf("prod scopes: %+v", prod.Scopes)
	}
	if prod.Scopes.PolicyOverride != domain.PolicyCost {
		t.Errorf("prod policy override: %v", prod.Scopes.PolicyOverride)
	}
	if prod.Scopes.ModelOverrides["gpt-4o"] != domain.PolicyWeighted {
		t.Errorf("prod model override: %+v", prod.Scopes.ModelOverrides)
	}
	if prod.Limits.RPM != 60 || prod.Limits.TPM != 90000 || prod.Limits.Concurrency != 4 {
		t.Errorf("prod limits: %+v", prod.Limits)
	}
	if prod.Limits.MaxSpendUSDMicros != 12_500_000 {
		t.Errorf("max spend micros = %d", prod.Limits.MaxSpendUSDMicros)
	}
	if prod.CreatedMS != 1700000000000 {
		t.Errorf("created not preserved: %d", prod.CreatedMS)
	}
	if prod.LastUsedMS != 1700099999000 {
		t.Errorf("last used not preserved: %d", prod.LastUsedMS)
	}
	if prod.Prefix == "ogk-old1…" {
		t.Errorf("prefix not re-minted")
	}

	// The minted raw key authenticates.
	raw := findRaw(t, res, "key_prod")
	v := auth.NewVerifier(store, pepper)
	if _, err := v.Verify(t.Context(), raw); err != nil {
		t.Errorf("minted key does not verify: %v", err)
	}

	// Revoked key imports as revoked with no raw.
	dead := byName["dead"]
	if dead.Status != domain.KeyRevoked {
		t.Errorf("dead status: %v", dead.Status)
	}
	for _, m := range res.Minted {
		if m.LegacyID == "key_dead" && m.RawKey != "" {
			t.Errorf("revoked key should not carry a raw key")
		}
	}
}

func TestImportKeysIdempotent(t *testing.T) {
	inst, _ := Parse([]byte(keyInstall))
	store, pepper := setupKeyStore(t)

	if _, err := ImportKeys(inst, store, pepper); err != nil {
		t.Fatal(err)
	}
	res2, err := ImportKeys(inst, store, pepper)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Minted) != 0 || len(res2.Skipped) != 3 {
		t.Fatalf("re-import: minted=%d skipped=%d", len(res2.Minted), len(res2.Skipped))
	}
	keys, _ := store.VirtualKeys().List()
	if len(keys) != 3 {
		t.Errorf("duplicate keys after re-import: %d", len(keys))
	}
}

// legacyUsageDB synthesizes an OmniRoute v3.8.52 omniroute.db with a
// usage_events table: importable rows + unmapped model + unknown key.
func legacyUsageDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "omniroute.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := `CREATE TABLE usage_events (
		id TEXT PRIMARY KEY, key_id TEXT, model TEXT, provider TEXT, status TEXT,
		prompt_tokens INTEGER, completion_tokens INTEGER,
		cost_usd REAL, latency_ms INTEGER, created_at INTEGER)`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	stmt, err := db.Prepare(`INSERT INTO usage_events
		(id, key_id, model, provider, status, prompt_tokens, completion_tokens, cost_usd, latency_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id, key, model, provider, status string
		pt, ct                           int64
		cost                             float64
		lat, at                          int64
	}{
		{"u1", "key_prod", "gpt-4o", "p1", "success", 100, 50, 0.00125, 812, 1700000100000},
		{"u2", "key_prod", "gpt-4o", "p1", "error", 10, 0, 0.0, 42, 1700000200000},
		{"u3", "key_dev", "gpt-4o", "p1", "cancelled", 20, 5, 0.00002, 100, 1700000300000},
		{"u4", "key_prod", "unmapped-model", "p1", "success", 1, 1, 0.0, 5, 1700000400000},
		{"u5", "key_ghost", "gpt-4o", "p1", "success", 1, 1, 0.0, 5, 1700000500000},
	}
	for _, r := range rows {
		if _, err := stmt.Exec(r.id, r.key, r.model, r.provider, r.status, r.pt, r.ct, r.cost, r.lat, r.at); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestImportUsagePreservesAggregates(t *testing.T) {
	inst, _ := Parse([]byte(keyInstall))
	store, pepper := setupKeyStore(t)
	if _, err := ImportKeys(inst, store, pepper); err != nil {
		t.Fatal(err)
	}

	dbPath := legacyUsageDB(t)
	events, err := ReadLegacyUsage(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("read legacy usage: %d", len(events))
	}

	res, err := ImportUsage(events, store)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 3 {
		t.Fatalf("imported = %d, skips: %v", res.Imported, res.Skipped)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skips = %v", res.Skipped)
	}

	// Re-import: all rows skipped (idempotent).
	res2, err := ImportUsage(events, store)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Imported != 0 || len(res2.Skipped) != 5 {
		t.Fatalf("re-import: %+v", res2)
	}

	// Aggregate preservation: the rollup query for the import window
	// reports the exact legacy totals (3 requests, 130+55 tokens,
	// $0.00127 == 1270 micros).
	totals, err := store.Rollups().QuerySummary(t.Context(), storage.RollupQueryParams{
		// Wide window: rollup rows are hour-floored, so the query must
		// span the full bucket containing the imported events.
		StartMS: 1699990000000, EndMS: 1700010000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if totals.Requests != 3 {
		t.Errorf("requests = %d", totals.Requests)
	}
	if totals.PromptTokens != 130 || totals.CompletionTokens != 55 {
		t.Errorf("tokens = %d/%d", totals.PromptTokens, totals.CompletionTokens)
	}
	if totals.CostUSDMicros != 1270 {
		t.Errorf("cost micros = %d (want 1270: 1250 + 0 + 20)", totals.CostUSDMicros)
	}
}

func TestUsageRequiresKeyImportFirst(t *testing.T) {
	store, _ := setupKeyStore(t)
	events := []LegacyUsageEvent{{ID: "u1", KeyID: "key_prod", Model: "gpt-4o", Status: "success", CreatedMS: 1}}
	res, err := ImportUsage(events, store)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 0 || len(res.Skipped) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if !contains(res.Skipped[0], "import keys first") {
		t.Errorf("skip reason: %q", res.Skipped[0])
	}
}

func TestReadLegacyUsageMissingFile(t *testing.T) {
	if _, err := ReadLegacyUsage(filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Fatalf("missing file accepted")
	}
}

func findRaw(t *testing.T, res *KeyImportResult, legacyID string) string {
	t.Helper()
	for _, m := range res.Minted {
		if m.LegacyID == legacyID {
			return m.RawKey
		}
	}
	t.Fatalf("no minted key for %s", legacyID)
	return ""
}

var _ = fmt.Sprintf // keep fmt if unused in future edits
var _ = os.Getenv
