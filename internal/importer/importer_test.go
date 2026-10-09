package importer

// p7.config-import tests: parse a reference OmniRoute v3.8.52 export,
// plan against an empty and a populated store, verify idempotency,
// unmapped-field reporting, and the legacy-untouched contract.
import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// referenceInstall is a representative OmniRoute v3.8.52 omniroute.json:
// every section populated, including fields with no OneGate equivalent.
const referenceInstall = `{
  "server": {"host": "0.0.0.0", "port": 8090},
  "dataDir": "/var/lib/omniroute",
  "logLevel": "debug",
  "timeouts": {"requestTimeoutMs": 120000, "idleTimeoutMs": 60000},
  "providers": [
    {"id": "prov_openai", "name": "OpenAI", "protocol": "openai",
     "baseUrl": "https://api.openai.com/", "apiKey": "sk-legacy-1", "enabled": true,
     "retries": 3, "timeoutSec": 60, "headers": "{\"X-Org\":\"acme\"}", "comment": "primary"},
    {"id": "prov_anthropic", "name": "Anthropic", "protocol": "anthropic",
     "baseUrl": "https://api.anthropic.com", "apiKey": "sk-ant-legacy", "enabled": true}
  ],
  "models": [
    {"id": "gpt-4o", "aliases": ["gpt4o"],
     "targets": [
       {"provider": "prov_openai", "model": "gpt-4o", "weight": 1, "priority": 1},
       {"provider": "prov_anthropic", "model": "claude-3-5-sonnet-latest", "weight": 2, "priority": 2, "costMultiplier": 80}
     ]},
    {"id": "legacy-only", "targets": [{"provider": "prov_openai", "model": "gpt-4o-mini"}], "disabled": true}
  ],
  "routing": [
    {"model": "gpt-4o", "strategy": "cost-preferred", "priority": 1, "enabled": true},
    {"model": "legacy-only", "strategy": "round-robin", "priority": 2}
  ],
  "keys": [
    {"id": "key_1", "name": "prod", "prefix": "ogk-abc…", "hash": "$scrypt$…", "status": "active",
     "createdAt": 1700000000000, "scopes": {}, "limits": {"rpm": 60}}
  ]
}`

func openMigrated(t *testing.T) *storage.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "onegate.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func TestParseLegacyInstall(t *testing.T) {
	inst, err := Parse([]byte(referenceInstall))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(inst.Providers) != 2 || len(inst.Models) != 2 || len(inst.Routing) != 2 || len(inst.Keys) != 1 {
		t.Fatalf("sections: %+v", inst)
	}
	if inst.Providers[0].Retries == nil || *inst.Providers[0].Retries != 3 {
		t.Errorf("retries not parsed")
	}
	if inst.Server.Port != 8090 {
		t.Errorf("server block not parsed")
	}
}

func TestPlanAgainstEmptyStore(t *testing.T) {
	inst, err := Parse([]byte(referenceInstall))
	if err != nil {
		t.Fatal(err)
	}
	store := openMigrated(t)
	plan, err := BuildPlan(inst, NewStoreSource(store))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Providers) != 2 || plan.Providers[0].Change != "create" || plan.Providers[1].Change != "create" {
		t.Fatalf("providers: %+v", plan.Providers)
	}
	if plan.Providers[0].Provider.BaseURL != "https://api.openai.com" {
		t.Errorf("baseUrl trailing slash not trimmed: %q", plan.Providers[0].Provider.BaseURL)
	}
	if len(plan.Models) != 2 || plan.Models[0].Change != "create" {
		t.Fatalf("models: %+v", plan.Models)
	}
	m := plan.Models[0].Model
	if len(m.Targets) != 2 || m.Targets[0].ProviderID != "prov_openai" ||
		m.Targets[1].ProviderModel != "claude-3-5-sonnet-latest" || m.Targets[1].CostMultiplier != 80 {
		t.Fatalf("targets: %+v", m.Targets)
	}
	if len(plan.Rules) != 2 || plan.Rules[0].Rule.Policy != "cost" {
		t.Fatalf("rules: %+v", plan.Rules)
	}
	if plan.KeysDeferred != 1 {
		t.Errorf("keys deferred = %d", plan.KeysDeferred)
	}
	// Unmapped fields are all reported.
	joined := unmappedJoined(plan)
	for _, want := range []string{
		"providers[prov_openai].retries=3",
		"providers[prov_openai].timeoutSec=60",
		`providers[prov_openai].headers="{\"X-Org\":\"acme\"}"`,
		"models[legacy-only].disabled=true",
		`routing[legacy-only].strategy="round-robin"`,
	} {
		if !contains(joined, want) {
			t.Errorf("unmapped missing %q in:\n%s", want, joined)
		}
	}
	// Runtime skips reported.
	if len(plan.RuntimeSkip) == 0 {
		t.Errorf("runtime skips empty")
	}
}

func TestApplyIdempotent(t *testing.T) {
	inst, err := Parse([]byte(referenceInstall))
	if err != nil {
		t.Fatal(err)
	}
	store := openMigrated(t)

	// Cipher needs a master key file.
	masterPath := filepath.Join(t.TempDir(), "master.key")
	master, err := auth.MasterSecret(masterPath)
	if err != nil {
		t.Fatalf("master: %v", err)
	}
	cipher, err := auth.NewCipher(master, auth.PurposeProviderKeys)
	if err != nil {
		t.Fatal(err)
	}

	plan1, err := BuildPlan(inst, NewStoreSource(store))
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan1, store, cipher); err != nil {
		t.Fatalf("apply 1: %v", err)
	}

	// Second plan must be a no-op.
	plan2, err := BuildPlan(inst, NewStoreSource(store))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plan2.Providers {
		if p.Change != "unchanged" {
			t.Errorf("provider %s: %s (%s)", p.Provider.ID, p.Change, p.Reason)
		}
	}
	for _, m := range plan2.Models {
		if m.Change != "unchanged" {
			t.Errorf("model %s: %s", m.Model.ID, m.Change)
		}
	}
	for _, r := range plan2.Rules {
		if r.Change != "unchanged" {
			t.Errorf("rule %s: %s", r.Rule.ModelID, r.Change)
		}
	}

	// Re-apply converges (no error, no duplicates).
	if err := Apply(plan2, store, cipher); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	provs, _ := store.Providers().List()
	if len(provs) != 2 {
		t.Errorf("providers after re-apply: %d", len(provs))
	}
	models, _ := store.Models().List()
	if len(models) != 2 {
		t.Errorf("models after re-apply: %d", len(models))
	}

	// The sealed credential decrypts back.
	rec, err := store.Providers().Get("prov_openai")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := cipher.Decrypt(rec.APIKeyEnc)
	if err != nil || string(plain) != "sk-legacy-1" {
		t.Errorf("credential round-trip: %v %q", err, plain)
	}
}

func TestPlanDetectsUpdates(t *testing.T) {
	inst, err := Parse([]byte(referenceInstall))
	if err != nil {
		t.Fatal(err)
	}
	store := openMigrated(t)
	masterPath := filepath.Join(t.TempDir(), "master.key")
	master, _ := auth.MasterSecret(masterPath)
	cipher, _ := auth.NewCipher(master, auth.PurposeProviderKeys)

	plan1, _ := BuildPlan(inst, NewStoreSource(store))
	if err := Apply(plan1, store, cipher); err != nil {
		t.Fatal(err)
	}

	// Mutate the legacy install: rename a provider, change a strategy.
	mutated := []byte(replaceAll(referenceInstall,
		`"name": "OpenAI"`, `"name": "OpenAI Prod"`,
		`"strategy": "cost-preferred"`, `"strategy": "latency-preferred"`))
	inst2, err := Parse(mutated)
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := BuildPlan(inst2, NewStoreSource(store))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, p := range plan2.Providers {
		found[p.Provider.ID] = p.Change
	}
	if found["prov_openai"] != "update" || found["prov_anthropic"] != "unchanged" {
		t.Errorf("provider changes: %+v", found)
	}
	if plan2.Rules[0].Change != "update" || plan2.Rules[0].Rule.Policy != "latency" {
		t.Errorf("rule update not detected: %+v", plan2.Rules[0])
	}
}

func TestLegacyInstallUntouched(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "omniroute.json")
	if err := os.WriteFile(legacy, []byte(referenceInstall), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(legacy)
	inst, err := Parse(before)
	if err != nil {
		t.Fatal(err)
	}
	store := openMigrated(t)
	plan, err := BuildPlan(inst, NewStoreSource(store))
	if err != nil {
		t.Fatal(err)
	}
	masterPath := filepath.Join(t.TempDir(), "master.key")
	master, _ := auth.MasterSecret(masterPath)
	cipher, _ := auth.NewCipher(master, auth.PurposeProviderKeys)
	if err := Apply(plan, store, cipher); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("legacy install was modified by import")
	}
}

func TestReportRenders(t *testing.T) {
	inst, _ := Parse([]byte(referenceInstall))
	store := openMigrated(t)
	plan, err := BuildPlan(inst, NewStoreSource(store))
	if err != nil {
		t.Fatal(err)
	}
	r := Report(plan)
	for _, want := range []string{
		"providers: 2 create",
		"models: 2 create",
		"routing rules: 2 create",
		"skipped (runtime settings",
		"unmapped legacy fields",
		"keys: 1 deferred",
	} {
		if !contains(r, want) {
			t.Errorf("report missing %q:\n%s", want, r)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte(`{"providers": "not-a-list"}`)); err == nil {
		t.Fatalf("garbage accepted")
	}
}

// --- tiny local helpers (no external deps) ---------------------------

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func unmappedJoined(plan *Plan) string {
	out := ""
	for _, u := range plan.Unmapped {
		out += u + "\n"
	}
	return out
}

func replaceAll(s string, pairs ...string) string {
	for i := 0; i+1 < len(pairs); i += 2 {
		s = stringsReplace(s, pairs[i], pairs[i+1])
	}
	return s
}

func stringsReplace(s, old, new string) string {
	out := ""
	rest := s
	for {
		i := indexOf(rest, old)
		if i < 0 {
			return out + rest
		}
		out += rest[:i] + new
		rest = rest[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
