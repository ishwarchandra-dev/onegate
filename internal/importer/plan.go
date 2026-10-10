package importer

// Plan + Apply: compute the import diff against live storage and apply
// it idempotently (p7.config-import).
import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// Source is the read-only view of current storage the planner needs.
type Source interface {
	ListProviders() ([]storage.ProviderRecord, error)
	ListModels() ([]domain.Model, error)
	ListRules() ([]domain.RoutingRule, error)
}

// storeSource adapts *storage.Store to Source.
type storeSource struct{ store *storage.Store }

func (s storeSource) ListProviders() ([]storage.ProviderRecord, error) {
	return s.store.Providers().List()
}

func (s storeSource) ListModels() ([]domain.Model, error) {
	return s.store.Models().List()
}

func (s storeSource) ListRules() ([]domain.RoutingRule, error) {
	return s.store.RoutingRules().List()
}

// NewStoreSource adapts a store.
func NewStoreSource(store *storage.Store) Source { return storeSource{store} }

// BuildPlan computes the import diff. It never writes.
func BuildPlan(inst *LegacyInstall, src Source) (*Plan, error) {
	plan := &Plan{}

	// --- runtime sections: reported, skipped --------------------------
	if inst.Server != nil {
		plan.RuntimeSkip = append(plan.RuntimeSkip,
			fmt.Sprintf("server.host=%q server.port=%d (configure onegate.json)", inst.Server.Host, inst.Server.Port))
	}
	if inst.DataDir != "" {
		plan.RuntimeSkip = append(plan.RuntimeSkip, fmt.Sprintf("dataDir=%q (use --data-dir)", inst.DataDir))
	}
	if inst.LogLevel != "" {
		plan.RuntimeSkip = append(plan.RuntimeSkip, fmt.Sprintf("logLevel=%q (configure onegate.json)", inst.LogLevel))
	}
	if inst.Timeouts != nil {
		plan.RuntimeSkip = append(plan.RuntimeSkip,
			fmt.Sprintf("timeouts{requestTimeoutMs=%d idleTimeoutMs=%d} (mapped: onegate.json http.*)",
				inst.Timeouts.RequestTimeoutMS, inst.Timeouts.IdleTimeoutMS))
	}

	// --- providers -----------------------------------------------------
	existingProviders := map[string]domain.Provider{}
	recs, err := src.ListProviders()
	if err != nil {
		return nil, fmt.Errorf("importer: list providers: %w", err)
	}
	for _, rec := range recs {
		existingProviders[rec.Provider.ID] = rec.Provider
	}
	for _, lp := range inst.Providers {
		enabled := true
		if lp.Enabled != nil {
			enabled = *lp.Enabled
		}
		prov := domain.Provider{
			ID:       lp.ID,
			Name:     lp.Name,
			Protocol: domain.ProviderProtocol(lp.Protocol),
			BaseURL:  strings.TrimRight(lp.BaseURL, "/"),
			Enabled:  enabled,
		}
		if verr := validateProvider(prov); verr != "" {
			// Security boundary: legacy configs are untrusted input. An
			// invalid provider (bad scheme, metadata host, unknown
			// protocol) is skipped with the reason recorded — never
			// imported. The data-plane SSRF guard would refuse it at
			// request time anyway; failing at plan time is the clear,
			// fail-fast posture (p8.security-review finding S-3).
			plan.Providers = append(plan.Providers, ProviderAction{
				Provider: prov,
				Change:   "skip",
				Reason:   verr,
			})
			continue
		}
		action := ProviderAction{Provider: prov, APIKey: lp.APIKey}
		if cur, ok := existingProviders[lp.ID]; ok {
			if providerEquals(prov, cur) {
				action.Change = "unchanged"
				action.Reason = "identical"
			} else {
				action.Change = "update"
				action.Reason = describeProviderDiff(prov, cur)
			}
		} else {
			action.Change = "create"
		}
		plan.Providers = append(plan.Providers, action)

		// Unmapped provider fields.
		if lp.Retries != nil {
			plan.Unmapped = append(plan.Unmapped, fmt.Sprintf("providers[%s].retries=%d", lp.ID, *lp.Retries))
		}
		if lp.TimeoutSec != nil {
			plan.Unmapped = append(plan.Unmapped, fmt.Sprintf("providers[%s].timeoutSec=%d", lp.ID, *lp.TimeoutSec))
		}
		if lp.Headers != "" {
			plan.Unmapped = append(plan.Unmapped, fmt.Sprintf("providers[%s].headers=%q", lp.ID, lp.Headers))
		}
		if lp.Comment != "" {
			plan.Unmapped = append(plan.Unmapped, fmt.Sprintf("providers[%s].comment=%q", lp.ID, lp.Comment))
		}
	}

	// --- models --------------------------------------------------------
	existingModels := map[string]domain.Model{}
	models, err := src.ListModels()
	if err != nil {
		return nil, fmt.Errorf("importer: list models: %w", err)
	}
	for _, m := range models {
		existingModels[m.ID] = m
	}
	for _, lm := range inst.Models {
		m := domain.Model{
			ID:      lm.ID,
			Aliases: lm.Aliases,
		}
		if lm.Caps != nil {
			m.Capabilities = caps(lm.Caps)
		} else {
			m.Capabilities = domain.ModelCapabilities{Tools: true, Vision: true, JSONMode: true, Stream: true}
		}
		for _, lt := range lm.Targets {
			weight := lt.Weight
			if weight <= 0 {
				weight = 1
			}
			pos := lt.Priority
			if pos <= 0 {
				pos = 1
			}
			costX := lt.CostX
			if costX <= 0 {
				costX = 100 // nominal, matching storage normalization
			}
			m.Targets = append(m.Targets, domain.ModelTarget{
				ProviderID:     lt.Provider,
				ProviderModel:  lt.Model,
				Weight:         weight,
				Position:       pos,
				CostMultiplier: costX,
			})
		}
		sort.SliceStable(m.Targets, func(i, j int) bool {
			return m.Targets[i].Position < m.Targets[j].Position
		})
		action := ModelAction{Model: m}
		if cur, ok := existingModels[m.ID]; ok {
			if modelEquals(m, cur) {
				action.Change = "unchanged"
			} else {
				action.Change = "update"
			}
		} else {
			action.Change = "create"
		}
		if lm.Disabled {
			action.Reason = "legacy disabled flag has no OneGate equivalent (delete the model instead)"
			plan.Unmapped = append(plan.Unmapped, fmt.Sprintf("models[%s].disabled=true", lm.ID))
		}
		plan.Models = append(plan.Models, action)
	}

	// --- routing rules ---------------------------------------------------
	existingRules := map[string]domain.RoutingRule{}
	rules, err := src.ListRules()
	if err != nil {
		return nil, fmt.Errorf("importer: list rules: %w", err)
	}
	for _, r := range rules {
		existingRules[r.ModelID] = r
	}
	for _, lr := range inst.Routing {
		policy, known := mapStrategy(lr.Strategy)
		rule := domain.RoutingRule{
			ID:       "rule_" + lr.Model,
			ModelID:  lr.Model,
			Policy:   policy,
			Enabled:  lr.Enabled == nil || *lr.Enabled,
			Position: lr.Priority,
		}
		action := RuleAction{Rule: rule}
		if !known {
			action.Reason = fmt.Sprintf("unknown strategy %q (kept as ordered)", lr.Strategy)
			plan.Unmapped = append(plan.Unmapped, fmt.Sprintf("routing[%s].strategy=%q", lr.Model, lr.Strategy))
		}
		if cur, ok := existingRules[lr.Model]; ok {
			if cur.Policy == rule.Policy && cur.Enabled == rule.Enabled {
				action.Change = "unchanged"
			} else {
				action.Change = "update"
			}
		} else {
			action.Change = "create"
		}
		plan.Rules = append(plan.Rules, action)
	}

	// --- keys: deferred to the data import ------------------------------
	plan.KeysDeferred = len(inst.Keys)
	if plan.KeysDeferred > 0 {
		plan.RuntimeSkip = append(plan.RuntimeSkip,
			fmt.Sprintf("keys[]: %d entries deferred to `onegate import --keys` (p7.data-import)", plan.KeysDeferred))
	}
	return plan, nil
}

// Apply writes the plan to storage (idempotent Upserts). Cipher seals
// provider credentials; nil cipher stores no credential.
func Apply(plan *Plan, store *storage.Store, cipher *auth.Cipher) error {
	for _, pa := range plan.Providers {
		if pa.Change == "unchanged" || pa.Change == "skip" {
			continue
		}
		rec := storage.ProviderRecord{Provider: pa.Provider}
		if pa.APIKey != "" && cipher != nil {
			enc, err := cipher.Encrypt([]byte(pa.APIKey))
			if err != nil {
				return fmt.Errorf("importer: seal key for provider %s: %w", pa.Provider.ID, err)
			}
			rec.APIKeyEnc = enc
		}
		if err := store.Providers().Upsert(&rec); err != nil {
			return fmt.Errorf("importer: upsert provider %s: %w", pa.Provider.ID, err)
		}
	}
	for _, ma := range plan.Models {
		if ma.Change == "unchanged" {
			continue
		}
		if err := store.Models().Upsert(ma.Model); err != nil {
			return fmt.Errorf("importer: upsert model %s: %w", ma.Model.ID, err)
		}
	}
	for _, ra := range plan.Rules {
		if ra.Change == "unchanged" {
			continue
		}
		if err := store.RoutingRules().Upsert(&ra.Rule); err != nil {
			return fmt.Errorf("importer: upsert rule %s: %w", ra.Rule.ID, err)
		}
	}
	return nil
}

// caps maps legacy capability flags to canonical (defaults true).
func caps(lc *LegacyCaps) domain.ModelCapabilities {
	boolOr := func(p *bool, def bool) bool {
		if p == nil {
			return def
		}
		return *p
	}
	return domain.ModelCapabilities{
		Tools:    boolOr(lc.Tools, true),
		Vision:   boolOr(lc.Vision, true),
		JSONMode: boolOr(lc.JSONMode, true),
		Stream:   boolOr(lc.Stream, true),
	}
}

// describeProviderDiff names the first difference for the report.
func describeProviderDiff(next, cur domain.Provider) string {
	switch {
	case next.Name != cur.Name:
		return fmt.Sprintf("name %q -> %q", cur.Name, next.Name)
	case next.Protocol != cur.Protocol:
		return fmt.Sprintf("protocol %s -> %s", cur.Protocol, next.Protocol)
	case next.BaseURL != cur.BaseURL:
		return fmt.Sprintf("baseUrl %q -> %q", cur.BaseURL, next.BaseURL)
	case next.Enabled != cur.Enabled:
		return fmt.Sprintf("enabled %v -> %v", cur.Enabled, next.Enabled)
	default:
		return "credential rotated"
	}
}

// legalProtocols mirrors the management-API boundary rules
// (internal/api/providers.go). One source of truth would be nicer; the
// api package is a top-level consumer, so the importer keeps its own
// copy and a test pins them together (TestProviderRulesMatchAPI).
var legalProtocols = map[domain.ProviderProtocol]bool{
	domain.ProtocolOpenAI:     true,
	domain.ProtocolAnthropic:  true,
	domain.ProtocolGemini:     true,
	domain.ProtocolOpenAIComp: true,
}

// validateProvider enforces the same provider boundary rules as the
// management API: legal protocol, absolute http(s) base URL, and no
// literal blocked-IP host. Returns "" when valid, else a human reason.
func validateProvider(p domain.Provider) string {
	if !legalProtocols[p.Protocol] {
		return fmt.Sprintf("invalid protocol %q", p.Protocol)
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("baseUrl %q is not an absolute URL", p.BaseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Sprintf("baseUrl scheme %q must be http or https", u.Scheme)
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if reason, blocked := client.BlockedIPReason(ip); blocked {
			return fmt.Sprintf("baseUrl host %s is blocked (%s)", u.Hostname(), reason)
		}
	}
	return ""
}
