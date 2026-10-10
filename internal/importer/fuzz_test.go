package importer

// p8.fuzzing: legacy omniroute.json parsing + plan building fuzz
// target. Arbitrary bytes flow through Parse -> BuildPlan -> Report;
// nothing may panic, and any provider a plan would create must have
// passed boundary validation (skip actions carry invalid ones).
import "testing"

func FuzzLegacyParseAndPlan(f *testing.F) {
	seeds := []string{
		`{"providers":[{"id":"p1","name":"P","protocol":"openai","baseUrl":"https://api.openai.com","apiKey":"sk-1"}],"models":[],"routing":[]}`,
		`{"providers":[{"id":"p1","protocol":"file://x","baseUrl":"file:///etc/passwd"}]}`,
		`{"providers":[{"id":"p1","protocol":"openai","baseUrl":"http://169.254.169.254/x"}],"models":[{"id":"m","targets":[{"provider":"p1","model":"g"}]}],"routing":[{"model":"m","strategy":"weighted"}]}`,
		`{"keys":[{"id":"k","prefix":"ogk-x","hash":"$scrypt$v=1$abc"}],"timeouts":{"requestTimeoutMs":1}}`,
		`{"providers":"no","models":"no","routing":"no"}`,
		`{"logLevel":"debug","dataDir":"/x","server":{"host":"0.0.0.0","port":1}}`,
		`{`,
		``,
		`null`,
		`[]`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		inst, err := Parse(data)
		if err != nil {
			return // rejecting garbage is the contract
		}
		plan, err := BuildPlan(inst, nullSource{})
		if err != nil {
			return
		}
		_ = Report(plan) // must render without panic for any plan
		// Invariant: only validated providers are planned as writes.
		for _, pa := range plan.Providers {
			switch pa.Change {
			case "create", "update":
				if validateProvider(pa.Provider) != "" {
					t.Fatalf("plan %s unvalidated provider %q (baseURL %q)",
						pa.Change, pa.Provider.ID, pa.Provider.BaseURL)
				}
			case "skip":
				if validateProvider(pa.Provider) == "" {
					t.Fatalf("provider %q skipped but passes validation", pa.Provider.ID)
				}
			}
		}
	})
}
