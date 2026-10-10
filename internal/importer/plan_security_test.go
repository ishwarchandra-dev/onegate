package importer

// p8.security-review finding S-3: legacy configs are untrusted input;
// invalid providers (bad scheme / metadata IP / unknown protocol) must be
// skipped at plan time with a recorded reason, never imported, and Apply
// must not write skipped rows.
import (
	"strings"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

const maliciousLegacy = `{
  "providers": [
    {"id": "prov_meta", "name": "Meta", "protocol": "openai",
     "baseUrl": "http://169.254.169.254/latest/meta-data", "apiKey": "sk-x"},
    {"id": "prov_file", "name": "File", "protocol": "openai",
     "baseUrl": "file:///etc/passwd", "apiKey": "sk-x"},
    {"id": "prov_proto", "name": "Weird", "protocol": "carrier-pigeon",
     "baseUrl": "https://example.com", "apiKey": "sk-x"},
    {"id": "prov_good", "name": "Good", "protocol": "openai",
     "baseUrl": "https://api.openai.com", "apiKey": "sk-good"}
  ]
}`

func TestPlanSkipsInvalidProviders(t *testing.T) {
	inst, err := Parse([]byte(maliciousLegacy))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := BuildPlan(inst, emptySource())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	skipped := map[string]string{}
	created := map[string]bool{}
	for _, pa := range plan.Providers {
		switch pa.Change {
		case "skip":
			skipped[pa.Provider.ID] = pa.Reason
		case "create":
			created[pa.Provider.ID] = true
		}
	}

	if len(skipped) != 3 {
		t.Fatalf("want 3 skipped providers, got %d: %v", len(skipped), skipped)
	}
	for id, want := range map[string]string{
		"prov_meta":  "blocked",
		"prov_file":  "absolute URL",
		"prov_proto": "protocol",
	} {
		reason, ok := skipped[id]
		if !ok {
			t.Fatalf("provider %s not skipped", id)
		}
		if !strings.Contains(reason, want) {
			t.Fatalf("provider %s skip reason %q missing %q", id, reason, want)
		}
	}
	if !created["prov_good"] {
		t.Fatal("valid provider prov_good was not planned as create")
	}
}

func TestApplyDoesNotWriteSkipped(t *testing.T) {
	inst, err := Parse([]byte(maliciousLegacy))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := BuildPlan(inst, emptySource())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	store := openMigrated(t)
	if err := Apply(plan, store, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	recs, err := store.Providers().List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 || recs[0].Provider.ID != "prov_good" {
		ids := make([]string, 0, len(recs))
		for _, r := range recs {
			ids = append(ids, r.Provider.ID)
		}
		t.Fatalf("want only prov_good stored, got %v", ids)
	}
}

func TestReportShowsSkips(t *testing.T) {
	inst, err := Parse([]byte(maliciousLegacy))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := BuildPlan(inst, emptySource())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	out := Report(plan)
	if !strings.Contains(out, "3 SKIPPED") {
		t.Fatalf("report does not surface skips:\n%s", out)
	}
	if !strings.Contains(out, "skip provider prov_meta") {
		t.Fatalf("report does not name skipped provider:\n%s", out)
	}
	head := ReportHeader(plan, false)
	if !strings.Contains(head, "1 change(s)") {
		t.Fatalf("header counts skips as changes: %s", head)
	}
}

// emptySource is a Source reporting an empty store.
func emptySource() Source { return nullSource{} }

type nullSource struct{}

func (nullSource) ListProviders() ([]storage.ProviderRecord, error) { return nil, nil }
func (nullSource) ListModels() ([]domain.Model, error)              { return nil, nil }
func (nullSource) ListRules() ([]domain.RoutingRule, error)         { return nil, nil }
