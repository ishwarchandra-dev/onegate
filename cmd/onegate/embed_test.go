package main

// p9.embed-pipeline: release gate. A binary built without the dashboard
// embeds the committed placeholder; release paths (CI e2e job, the
// release script) run with ONEGATE_REQUIRE_EMBED=1 so a placeholder
// build fails loudly instead of shipping a stub UI.
import (
	"os"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/webfs"
	web "github.com/ishwarchandra-dev/onegate/web"
)

func TestEmbeddedDashboardNotPlaceholder(t *testing.T) {
	if os.Getenv("ONEGATE_REQUIRE_EMBED") != "1" {
		t.Skip("set ONEGATE_REQUIRE_EMBED=1 to enforce (release/CI gate; fresh clones embed the placeholder by design — ADR 006)")
	}
	h := webfs.New(web.Dist())
	if h.IsPlaceholder() {
		t.Fatal("embedded dashboard is the placeholder: run `make web && make build` — refusing to ship a binary without the UI")
	}
}
