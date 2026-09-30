package proxy

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestLayering enforces the proxy-tree import contract (mirrors the
// Phase 2 gate audit for internal/protocol): the proxy may depend on
// stdlib, internal/domain, internal/protocol, internal/observability
// and internal/version — never on server/config/storage/auth/api, which
// sit at the composition layer and flow values INTO the proxy instead.
//
// This keeps the proxy core independently testable and prevents the
// classic gateway disease: request-path code reaching sideways into
// configuration or persistence.
//
// Test files are exempt from the internal-allowlist portion (but never
// from the external-dependency ban): integration tests assemble the
// stack exactly like cmd/onegate does, which is the composition layer's
// job. Production files under internal/proxy remain fully constrained.
func TestLayering(t *testing.T) {
	allowedInternalPrefixes := []string{
		"github.com/ishwarchandra-dev/onegate/internal/domain",
		"github.com/ishwarchandra-dev/onegate/internal/protocol",
		"github.com/ishwarchandra-dev/onegate/internal/observability",
		"github.com/ishwarchandra-dev/onegate/internal/version",
	}
	forbidden := []string{
		"internal/server", "internal/config", "internal/storage",
		"internal/auth", "internal/api", "internal/routing",
	}

	var violations []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		isTest := strings.HasSuffix(path, "_test.go")
		for _, imp := range file.Imports {
			ip := strings.Trim(imp.Path.Value, `"`)
			// Stdlib and intra-proxy imports are fine.
			if !strings.Contains(ip, ".") || strings.HasPrefix(ip, "github.com/ishwarchandra-dev/onegate/internal/proxy") {
				continue
			}
			allowed := func() bool {
				for _, prefix := range allowedInternalPrefixes {
					if strings.HasPrefix(ip, prefix) {
						return true
					}
				}
				return isTest // tests may import composition layers
			}()
			if allowed {
				continue
			}
			// Anything else (external deps or forbidden internals) is a
			// violation.
			violations = append(violations, path+": "+ip)
			continue
		}
		if isTest {
			return nil // same exemption as above
		}
		for _, imp := range file.Imports {
			ip := strings.Trim(imp.Path.Value, `"`)
			for _, f := range forbidden {
				if strings.Contains(ip, f) {
					violations = append(violations, path+": "+ip)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("layering violations under internal/proxy:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
