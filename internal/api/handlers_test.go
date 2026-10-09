package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// seedProvider creates a provider through the API and returns its ID.
func (e *testEnv) seedProvider(t *testing.T, name, baseURL string) string {
	t.Helper()
	resp, payload := e.do(t, "POST", "/api/providers", map[string]any{
		"name": name, "protocol": "openai", "base_url": baseURL, "api_key": "sk-secret-key-1234",
	}, testToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed provider: %d (%v)", resp.StatusCode, payload)
	}
	id, _ := payload["id"].(string)
	if id == "" {
		t.Fatal("seed provider: no id in response")
	}
	return id
}

// TestProviderLifecycle covers create (masked key) -> list -> get ->
// patch (rotate key, toggle) -> delete.
func TestProviderLifecycle(t *testing.T) {
	env := newEnv(t)
	id := env.seedProvider(t, "OpenAI", "https://api.openai.com")

	// Create must never echo the raw key — only the masked form.
	resp, payload := env.do(t, "GET", "/api/providers/"+id, nil, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: %d", resp.StatusCode)
	}
	masked, _ := payload["masked_key"].(string)
	if masked == "" || strings.Contains(masked, "sk-secret-key-1234") {
		t.Fatalf("masked_key must be masked, got %q", masked)
	}
	if !strings.Contains(masked, "…") {
		t.Fatalf("masked_key should use the ellipsis form, got %q", masked)
	}
	if raw, ok := payload["api_key"]; ok {
		t.Fatalf("api_key must never appear in reads, got %v", raw)
	}

	// List contains the provider, paginated envelope shape.
	resp, payload = env.do(t, "GET", "/api/providers?limit=10", nil, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want 1 provider, got %d", len(items))
	}
	if _, ok := payload["next_cursor"]; !ok {
		t.Fatal("list response missing next_cursor")
	}

	// Patch: disable + rename; omitting api_key keeps the credential.
	resp, payload = env.do(t, "PATCH", "/api/providers/"+id, map[string]any{
		"name": "OpenAI-2", "enabled": false,
	}, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d (%v)", resp.StatusCode, payload)
	}
	if payload["name"] != "OpenAI-2" || payload["enabled"] != false {
		t.Fatalf("patch did not apply: %v", payload)
	}
	if payload["masked_key"] != masked {
		t.Fatalf("patch without api_key must keep the credential: %v", payload["masked_key"])
	}

	// Delete -> 204, then 404.
	resp, _ = env.do(t, "DELETE", "/api/providers/"+id, nil, testToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = env.do(t, "GET", "/api/providers/"+id, nil, testToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", resp.StatusCode)
	}
}

// TestProviderValidation pins the create invariants.
func TestProviderValidation(t *testing.T) {
	env := newEnv(t)
	cases := []struct {
		name   string
		body   map[string]any
		status int
		param  string
	}{
		{"missing name", map[string]any{"protocol": "openai", "base_url": "https://x.com"}, 400, "name"},
		{"bad protocol", map[string]any{"name": "x", "protocol": "grpc", "base_url": "https://x.com"}, 400, "protocol"},
		{"bad url", map[string]any{"name": "x", "protocol": "openai", "base_url": "ftp://x"}, 400, "base_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, payload := env.do(t, "POST", "/api/providers", tc.body, testToken)
			if resp.StatusCode != tc.status {
				t.Fatalf("want %d, got %d (%v)", tc.status, resp.StatusCode, payload)
			}
			e := errOf(t, payload)
			if e["param"] != tc.param {
				t.Fatalf("want param %q, got %v", tc.param, e["param"])
			}
		})
	}
}

// TestProviderIdempotentCreate: same id twice returns 200 the second time.
func TestProviderIdempotentCreate(t *testing.T) {
	env := newEnv(t)
	body := map[string]any{
		"id": "prov_1_1", "name": "X", "protocol": "openai", "base_url": "https://x.com",
	}
	resp, _ := env.do(t, "POST", "/api/providers", body, testToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: %d", resp.StatusCode)
	}
	resp, payload := env.do(t, "POST", "/api/providers", body, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay must be 200, got %d (%v)", resp.StatusCode, payload)
	}
	if payload["id"] != "prov_1_1" {
		t.Fatalf("replay returned wrong id: %v", payload["id"])
	}
}

// TestProviderProbeSuccess: a live mock provider answers ok=true.
func TestProviderProbeSuccess(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Protocol-native model list must be hit with auth.
		if r.URL.Path != "/models" {
			t.Errorf("probe hit %q, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-secret-key-1234" {
			t.Errorf("probe missing bearer auth")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer mock.Close()

	env := newEnv(t)
	id := env.seedProvider(t, "Mock", mock.URL)
	resp, payload := env.do(t, "POST", "/api/providers/"+id+"/test", nil, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe: %d (%v)", resp.StatusCode, payload)
	}
	if payload["ok"] != true {
		t.Fatalf("probe should be ok, got %v", payload)
	}
	if lat, _ := payload["latency_ms"].(float64); lat < 0 {
		t.Fatalf("latency must be >= 0, got %v", lat)
	}
	if _, present := payload["error"]; present {
		t.Fatalf("successful probe must not carry error, got %v", payload["error"])
	}
}

// TestProviderProbeFailureSurfacesDetail: a provider 401 surfaces
// ok=false with per-protocol error detail (auth type).
func TestProviderProbeFailureSurfacesDetail(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer mock.Close()

	env := newEnv(t)
	id := env.seedProvider(t, "Mock", mock.URL)
	resp, payload := env.do(t, "POST", "/api/providers/"+id+"/test", nil, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe endpoint itself should be 200, got %d", resp.StatusCode)
	}
	if payload["ok"] != false {
		t.Fatalf("probe should report failure, got %v", payload)
	}
	errObj, _ := payload["error"].(map[string]any)
	inner, _ := errObj["error"].(map[string]any)
	if inner == nil || inner["type"] != "authentication_error" {
		t.Fatalf("probe error detail wrong: %v", payload)
	}
	if inner["status"] != float64(401) {
		t.Fatalf("probe should preserve provider status, got %v", inner["status"])
	}
}

// TestModelLifecycle: create requires real providers; replace is
// full-replace; delete cascades.
func TestModelLifecycle(t *testing.T) {
	env := newEnv(t)
	provID := env.seedProvider(t, "P", "https://p.com")

	// Unknown provider reference -> 400.
	resp, payload := env.do(t, "POST", "/api/models", map[string]any{
		"id": "gpt-x", "targets": []map[string]any{{"provider_id": "prov_9_9", "provider_model": "m"}},
	}, testToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown provider must 400, got %d", resp.StatusCode)
	}
	e := errOf(t, payload)
	if e["param"] != "targets" {
		t.Fatalf("want param targets, got %v", e["param"])
	}

	// Valid create.
	create := map[string]any{
		"id": "gpt-x", "aliases": []string{"gx"},
		"targets": []map[string]any{
			{"provider_id": provID, "provider_model": "gpt-x-upstream", "weight": 2},
		},
		"capabilities": map[string]any{"tools": true},
	}
	resp, payload = env.do(t, "POST", "/api/models", create, testToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create model: %d (%v)", resp.StatusCode, payload)
	}

	// Idempotent replay -> 200.
	resp, _ = env.do(t, "POST", "/api/models", create, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("model replay: want 200, got %d", resp.StatusCode)
	}

	// Replace: two targets, position ordering applied.
	resp, payload = env.do(t, "PUT", "/api/models/gpt-x", map[string]any{
		"id": "gpt-x",
		"targets": []map[string]any{
			{"provider_id": provID, "provider_model": "b"},
			{"provider_id": provID, "provider_model": "a"},
		},
	}, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace model: %d (%v)", resp.StatusCode, payload)
	}
	targets, _ := payload["targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("replace must swap targets atomically, got %d", len(targets))
	}

	// GET reflects the replaced shape.
	_, payload = env.do(t, "GET", "/api/models/gpt-x", nil, testToken)
	targets, _ = payload["targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("get model targets: %d", len(targets))
	}

	// Delete.
	resp, _ = env.do(t, "DELETE", "/api/models/gpt-x", nil, testToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete model: %d", resp.StatusCode)
	}
}

// TestRoutingRulesAndHealth: rules CRUD + the health view contract.
func TestRoutingRulesAndHealth(t *testing.T) {
	env := newEnv(t)
	provID := env.seedProvider(t, "P", "https://p.com")
	_, payload := env.do(t, "POST", "/api/models", map[string]any{
		"id": "m1", "targets": []map[string]any{{"provider_id": provID, "provider_model": "m1-up"}},
	}, testToken)
	_ = payload

	// Invalid policy -> 400; unknown model -> 400.
	resp, _ := env.do(t, "POST", "/api/routing-rules", map[string]any{
		"model_id": "m1", "policy": "random",
	}, testToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad policy: %d", resp.StatusCode)
	}
	resp, _ = env.do(t, "POST", "/api/routing-rules", map[string]any{
		"model_id": "nope", "policy": "ordered",
	}, testToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown model: %d", resp.StatusCode)
	}

	// Create + list.
	resp, payload = env.do(t, "POST", "/api/routing-rules", map[string]any{
		"model_id": "m1", "policy": "cost", "position": 1,
	}, testToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create rule: %d (%v)", resp.StatusCode, payload)
	}
	ruleID, _ := payload["id"].(string)

	_, payload = env.do(t, "GET", "/api/routing-rules", nil, testToken)
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("rules list: want 1, got %d", len(items))
	}

	// Health: one target per (provider, model), closed; one event.
	_, payload = env.do(t, "GET", "/api/routing/health", nil, testToken)
	targets, _ := payload["targets"].([]any)
	events, _ := payload["events"].([]any)
	if len(targets) != 1 {
		t.Fatalf("health targets: want 1, got %d (%v)", len(targets), payload)
	}
	tgt := targets[0].(map[string]any)
	if tgt["state"] != "closed" || tgt["provider_id"] != provID {
		t.Fatalf("health target wrong: %v", tgt)
	}
	if len(events) != 1 {
		t.Fatalf("health events: want 1, got %d", len(events))
	}

	// Replace + delete.
	resp, _ = env.do(t, "PUT", "/api/routing-rules/"+ruleID, map[string]any{
		"model_id": "m1", "policy": "latency",
	}, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace rule: %d", resp.StatusCode)
	}
	resp, _ = env.do(t, "DELETE", "/api/routing-rules/"+ruleID, nil, testToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete rule: %d", resp.StatusCode)
	}
}

// TestKeyShowOnceSemantics: raw key appears exactly once (mint), never
// in list/get/patch responses.
func TestKeyShowOnceSemantics(t *testing.T) {
	env := newEnv(t)
	resp, payload := env.do(t, "POST", "/api/keys", map[string]any{
		"name": "prod", "limits": map[string]any{"rpm": 60},
	}, testToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mint: %d (%v)", resp.StatusCode, payload)
	}
	raw, _ := payload["raw_key"].(string)
	if !strings.HasPrefix(raw, "ogk-") {
		t.Fatalf("raw key must be ogk-…, got %q", raw)
	}
	key, _ := payload["key"].(map[string]any)
	id, _ := key["id"].(string)

	// The minted key authenticates against the real proxy verifier.
	if _, err := env.keys.VerifyKey(t.Context(), raw); err != nil {
		t.Fatalf("minted key must verify: %v", err)
	}

	// List and get never carry raw material.
	for _, path := range []string{"/api/keys", "/api/keys/" + id} {
		_, payload = env.do(t, "GET", path, nil, testToken)
		if s := fmt.Sprint(payload); strings.Contains(s, raw) {
			t.Fatalf("%s leaked the raw key", path)
		}
	}

	// Patch limits + expiry.
	resp, payload = env.do(t, "PATCH", "/api/keys/"+id, map[string]any{
		"limits": map[string]any{"rpm": 120, "tpm": 1000000},
	}, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch key: %d", resp.StatusCode)
	}
	limits, _ := payload["limits"].(map[string]any)
	if limits["rpm"] != float64(120) {
		t.Fatalf("patched limits wrong: %v", limits)
	}

	// Revoke -> status revoked; revoked key fails proxy auth.
	resp, payload = env.do(t, "POST", "/api/keys/"+id+"/revoke", nil, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if payload["status"] != "revoked" {
		t.Fatalf("status after revoke: %v", payload["status"])
	}
	if _, err := env.keys.VerifyKey(t.Context(), raw); err == nil {
		t.Fatal("revoked key must fail verification")
	}

	// Revoke is idempotent.
	resp, _ = env.do(t, "POST", "/api/keys/"+id+"/revoke", nil, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke replay must be 200, got %d", resp.StatusCode)
	}
}

// TestUsageEndpoints: seeded requests flow into range/summary/lists.
func TestUsageEndpoints(t *testing.T) {
	env := newEnv(t)
	raw, rec, err := env.keys.CreateKey(t.Context(), createKeyParams("k1"))
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	_ = raw

	base := int64(1_700_000_000_000)
	recs := []domain.RequestRecord{
		{ID: "r1", TraceID: "r1", VirtualKeyID: rec.ID, ModelRequested: "m",
			ModelServed: "m", ProviderID: "prov_1_1", Status: domain.RequestSuccess,
			PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CostUSDMicros: 25, LatencyMS: 100, CreatedMS: base},
		{ID: "r2", TraceID: "r2", VirtualKeyID: rec.ID, ModelRequested: "m",
			ModelServed: "m", ProviderID: "prov_1_1", Status: domain.RequestError, ErrorCode: "x",
			PromptTokens: 7, CompletionTokens: 0, TotalTokens: 7, CostUSDMicros: 10, LatencyMS: 50, CreatedMS: base + 1000},
		{ID: "r3", TraceID: "r3", VirtualKeyID: rec.ID, ModelRequested: "m",
			ModelServed: "m", ProviderID: "prov_1_1", Status: domain.RequestSuccess,
			PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5, CostUSDMicros: 5, LatencyMS: 80, CreatedMS: base + 3610_000},
	}
	if err := env.store.Requests().InsertBatch(recs); err != nil {
		t.Fatalf("seed requests: %v", err)
	}

	// Summary totals. The window is bucket-aligned (rollups bucket by
	// hour; a raw start_ms would exclude the containing bucket).
	start := storage.HourlyBucketStart(base)
	_, payload := env.do(t, "GET", fmt.Sprintf("/api/usage/summary?vkey_id=%s&start_ms=%d&end_ms=%d",
		rec.ID, start, base+7_200_000), nil, testToken)
	if payload["requests"] != float64(3) || payload["errors"] != float64(1) {
		t.Fatalf("summary wrong: %v", payload)
	}
	if payload["total_tokens"] != float64(27) || payload["cost_usd_micros"] != float64(40) {
		t.Fatalf("summary tokens/cost wrong: %v", payload)
	}

	// Range buckets (hourly): two distinct hours.
	_, payload = env.do(t, "GET", fmt.Sprintf("/api/usage/range?start_ms=%d&end_ms=%d&step=hour",
		start, base+7_200_000), nil, testToken)
	buckets, _ := payload["buckets"].([]any)
	if len(buckets) != 2 {
		t.Fatalf("want 2 hourly buckets, got %d (%v)", len(buckets), payload)
	}

	// Requests list: newest first, paginated 2 + 1.
	_, payload = env.do(t, "GET", "/api/requests?limit=2", nil, testToken)
	items, _ := payload["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("page 1: want 2, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["id"] != "r3" {
		t.Fatalf("newest first violated: %v", first["id"])
	}
	cursor, _ := payload["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("expected next_cursor")
	}
	_, payload = env.do(t, "GET", "/api/requests?limit=2&cursor="+cursor, nil, testToken)
	items, _ = payload["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "r1" {
		t.Fatalf("page 2 wrong: %v", payload)
	}
	if payload["next_cursor"] != nil && payload["next_cursor"] != "" {
		t.Fatalf("page 2 must be last, got cursor %v", payload["next_cursor"])
	}

	// Per-key usage endpoint matches the summary.
	_, payload = env.do(t, "GET", fmt.Sprintf("/api/keys/%s/usage?start_ms=%d", rec.ID, start), nil, testToken)
	if payload["requests"] != float64(3) {
		t.Fatalf("key usage wrong: %v", payload)
	}
}

// createKeyParams is a small helper to build mint parameters.
func createKeyParams(name string) auth.CreateKeyParams {
	return auth.CreateKeyParams{Name: name}
}

// TestLogsRecentAndFilters: hub entries flow through the snapshot
// endpoint with level filtering.
func TestLogsRecentAndFilters(t *testing.T) {
	env := newEnv(t)
	for _, e := range []observability.LogEntry{
		{Timestamp: time.Now(), Level: "info", Message: "one"},
		{Timestamp: time.Now(), Level: "error", Message: "two", TraceID: "req-1"},
		{Timestamp: time.Now(), Level: "debug", Message: "three"},
	} {
		env.hub.Publish(e)
	}

	_, payload := env.do(t, "GET", "/api/logs/recent?limit=10", nil, testToken)
	entries, _ := payload["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}

	_, payload = env.do(t, "GET", "/api/logs/recent?min_level=error", nil, testToken)
	entries, _ = payload["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("min_level=error: want 1, got %d", len(entries))
	}
	ent := entries[0].(map[string]any)
	if ent["message"] != "two" || ent["trace_id"] != "req-1" {
		t.Fatalf("filtered entry wrong: %v", ent)
	}

	// Invalid level -> 400.
	resp, _ := env.do(t, "GET", "/api/logs/recent?min_level=verbose", nil, testToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad min_level: %d", resp.StatusCode)
	}
}

// TestSystemEndpoints: status counts and the config snapshot.
func TestSystemEndpoints(t *testing.T) {
	env := newEnv(t)
	env.seedProvider(t, "P", "https://p.com")

	_, payload := env.do(t, "GET", "/api/system/status", nil, testToken)
	if payload["version"] == "" || payload["schema_version"] != float64(3) {
		t.Fatalf("status version fields wrong: %v", payload)
	}
	counts, _ := payload["counts"].(map[string]any)
	if counts["providers"] != float64(1) {
		t.Fatalf("provider count wrong: %v", counts)
	}
	if up, _ := payload["uptime_ms"].(float64); up < 0 {
		t.Fatalf("uptime negative: %v", up)
	}

	_, payload = env.do(t, "GET", "/api/system/config", nil, testToken)
	if payload["host"] != "127.0.0.1" || payload["port"] != float64(7420) {
		t.Fatalf("config wrong: %v", payload)
	}
	httpCfg, _ := payload["http"].(map[string]any)
	if httpCfg["read_header_timeout_ms"] != float64(5000) {
		t.Fatalf("http timeouts wrong: %v", httpCfg)
	}
}

// TestKeyPagination verifies the composite (created_ms, id) cursor.
func TestKeyPagination(t *testing.T) {
	env := newEnv(t)
	for i := 0; i < 3; i++ {
		_, _, err := env.keys.CreateKey(t.Context(), auth.CreateKeyParams{Name: fmt.Sprintf("k%d", i)})
		if err != nil {
			t.Fatalf("create key %d: %v", i, err)
		}
	}
	_, payload := env.do(t, "GET", "/api/keys?limit=2", nil, testToken)
	items, _ := payload["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("page 1: want 2, got %d", len(items))
	}
	cursor, _ := payload["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("expected cursor")
	}
	_, payload = env.do(t, "GET", "/api/keys?limit=2&cursor="+cursor, nil, testToken)
	items, _ = payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("page 2: want 1, got %d", len(items))
	}
}

// TestMalformedCursor400: garbage cursors are rejected, not 500s or
// silent first pages.
func TestMalformedCursor400(t *testing.T) {
	env := newEnv(t)
	for _, path := range []string{"/api/providers?cursor=!!!", "/api/keys?cursor=bm90LWpzb24", "/api/models?cursor=__", "/api/routing-rules?cursor=!!"} {
		resp, _ := env.do(t, "GET", path, nil, testToken)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: want 400 for malformed cursor, got %d", path, resp.StatusCode)
		}
	}
	// The requests endpoint uses storage's own cursor codec.
	resp, _ := env.do(t, "GET", "/api/requests?cursor=garbage", nil, testToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("requests: want 400 for malformed cursor, got %d", resp.StatusCode)
	}
}

// Compile-time interface checks for the storage dependency used above.
var _ = storage.ErrNotFound
