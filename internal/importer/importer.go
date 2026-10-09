// Package importer migrates a legacy OmniRoute v3.8.52 installation into
// OneGate storage (p7.config-import). The legacy on-disk format is a
// single JSON file (`omniroute.json`) holding runtime settings AND data
// sections (providers, models, routing, keys); OneGate moved the data
// sections into SQLite, so import is a translation, not a copy of the
// file (ADR 002).
//
// Contract (checklist E-8 / F-5):
//   - Dry-run by default: plan only, touch nothing.
//   - Idempotent: re-importing converges (Upsert by stable ID).
//   - The legacy install is only ever READ.
//   - Unmapped legacy fields are reported, never silently dropped.
package importer

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// LegacyInstall is the OmniRoute v3.8.52 omniroute.json shape. Field
// names are the legacy camelCase wire format; decode is tolerant of
// missing sections and strict about unknown ones (reported, not fatal).
type LegacyInstall struct {
	Server   *LegacyServer   `json:"server"`
	DataDir  string          `json:"dataDir"`
	LogLevel string          `json:"logLevel"`
	Timeouts *LegacyTimeouts `json:"timeouts"`

	Providers []LegacyProvider `json:"providers"`
	Models    []LegacyModel    `json:"models"`
	Routing   []LegacyRule     `json:"routing"`
	Keys      []LegacyKey      `json:"keys"`
}

// LegacyServer is the runtime listener block (not imported: OneGate's own
// onegate.json governs serve).
type LegacyServer struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// LegacyTimeouts is the legacy HTTP timeout block (mapped names differ).
type LegacyTimeouts struct {
	RequestTimeoutMS  int `json:"requestTimeoutMs"`
	IdleTimeoutMS     int `json:"idleTimeoutMs"`
	ReadHeaderTimeout int `json:"readHeaderTimeoutMs"` // unmapped name: legacy used it for the whole request
}

// LegacyProvider is one upstream provider entry.
type LegacyProvider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseUrl"`
	APIKey   string `json:"apiKey"`
	Enabled  *bool  `json:"enabled"`
	// Fields with no OneGate equivalent — collected for the report:
	Retries    *int   `json:"retries"`    // per-provider retry count
	TimeoutSec *int   `json:"timeoutSec"` // per-provider timeout
	Headers    string `json:"headers"`    // static extra headers
	Comment    string `json:"comment"`
}

// LegacyModel is one canonical model with its targets.
type LegacyModel struct {
	ID       string         `json:"id"`
	Aliases  []string       `json:"aliases"`
	Targets  []LegacyTarget `json:"targets"`
	Caps     *LegacyCaps    `json:"capabilities"`
	Disabled bool           `json:"disabled"`
}

// LegacyTarget maps a canonical model to one provider's copy.
type LegacyTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Weight   int    `json:"weight"`
	Priority int    `json:"priority"`
	CostX    int    `json:"costMultiplier"`
}

// LegacyCaps is the legacy capability flags block.
type LegacyCaps struct {
	Tools    *bool `json:"tools"`
	Vision   *bool `json:"vision"`
	JSONMode *bool `json:"json"`
	Stream   *bool `json:"stream"`
}

// LegacyRule is a routing-table row.
type LegacyRule struct {
	Model    string `json:"model"`
	Strategy string `json:"strategy"`
	Priority int    `json:"priority"`
	Enabled  *bool  `json:"enabled"`
}

// LegacyKey is a virtual key entry (imported by p7.data-import; parsed
// here so the plan can report what will happen to them).
type LegacyKey struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Prefix    string          `json:"prefix"`
	Hash      string          `json:"hash"`
	Scopes    json.RawMessage `json:"scopes"`
	Limits    json.RawMessage `json:"limits"`
	Status    string          `json:"status"`
	CreatedAt int64           `json:"createdAt"`
	ExpiresAt int64           `json:"expiresAt"`
	LastUsed  int64           `json:"lastUsedAt"`
}

// Parse decodes a legacy omniroute.json.
func Parse(data []byte) (*LegacyInstall, error) {
	var inst LegacyInstall
	if err := json.Unmarshal(data, &inst); err != nil {
		return nil, fmt.Errorf("importer: bad omniroute.json: %w", err)
	}
	return &inst, nil
}

// ProviderAction is one planned provider change.
type ProviderAction struct {
	Provider domain.Provider
	APIKey   string // plaintext credential to seal; "" = none
	Change   string // "create" | "update" | "unchanged"
	Reason   string
}

// ModelAction is one planned model change.
type ModelAction struct {
	Model  domain.Model
	Change string // "create" | "update" | "unchanged"
	Reason string
}

// RuleAction is one planned routing-rule change.
type RuleAction struct {
	Rule   domain.RoutingRule
	Change string // "create" | "update" | "unchanged"
	Reason string
}

// Plan is the computed import diff: what will change, what is skipped,
// and every unmapped legacy field.
type Plan struct {
	Providers []ProviderAction
	Models    []ModelAction
	Rules     []RuleAction

	// RuntimeSkip lists legacy runtime settings that are NOT imported
	// (OneGate governs serve through its own onegate.json).
	RuntimeSkip []string

	// Unmapped lists legacy fields with no OneGate equivalent, verbatim.
	Unmapped []string

	// KeysDeferred reports legacy key entries (imported by
	// `onegate import --keys`, p7.data-import).
	KeysDeferred int
}

// strategyMap translates legacy routing strategies to OneGate policies.
var strategyMap = map[string]domain.FallbackPolicy{
	"ordered":           domain.PolicyOrdered,
	"weighted":          domain.PolicyWeighted,
	"cost-preferred":    domain.PolicyCost,
	"latency-preferred": domain.PolicyLatency,
}

// mapStrategy resolves a legacy strategy, reporting unknown values.
func mapStrategy(s string) (domain.FallbackPolicy, bool) {
	p, ok := strategyMap[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return domain.PolicyOrdered, false
	}
	return p, true
}

// providerEquals compares providers ignoring timestamps and credentials.
func providerEquals(a, b domain.Provider) bool {
	return a.Name == b.Name && a.Protocol == b.Protocol &&
		a.BaseURL == b.BaseURL && a.Enabled == b.Enabled
}

// modelEquals compares canonical models (id, aliases, capabilities,
// targets).
func modelEquals(a, b domain.Model) bool {
	if a.ID != b.ID || len(a.Aliases) != len(b.Aliases) || a.Capabilities != b.Capabilities {
		return false
	}
	for i := range a.Aliases {
		if a.Aliases[i] != b.Aliases[i] {
			return false
		}
	}
	if len(a.Targets) != len(b.Targets) {
		return false
	}
	for i := range a.Targets {
		x, y := a.Targets[i], b.Targets[i]
		if x.ProviderID != y.ProviderID || x.ProviderModel != y.ProviderModel ||
			x.Weight != y.Weight || x.Position != y.Position || x.CostMultiplier != y.CostMultiplier {
			return false
		}
	}
	return true
}
