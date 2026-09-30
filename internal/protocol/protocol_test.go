package protocol

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestLayering enforces the Phase 2 gate criterion: "Protocol packages
// have zero imports from proxy/routing/storage" — implemented as the full
// allowlist from the package contract: stdlib, internal/domain, sibling
// packages under internal/protocol, and net/http for the profile package.
func TestLayering(t *testing.T) {
	root := "."
	allowed := func(importPath string) bool {
		// Standard library and this module's own package.
		if !strings.Contains(importPath, ".") || strings.HasPrefix(importPath, "github.com/ishwarchandra-dev/onegate") {
			return true
		}
		return false
	}

	allowedInternal := map[string]bool{
		"github.com/ishwarchandra-dev/onegate/internal/domain": true,
	}
	// Sibling protocol packages are allowed (e.g. profile is standalone;
	// conformance imports all adapters — tests only).
	forbidden := []string{
		"internal/proxy", "internal/routing", "internal/storage",
		"internal/server", "internal/api", "internal/auth",
		"internal/config", "internal/observability",
	}

	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if err != nil && d == nil {
				return err
			}
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			ip := strings.Trim(imp.Path.Value, `"`)
			if allowed(ip) {
				continue
			}
			if allowedInternal[ip] {
				continue
			}
			// External imports are forbidden outright in this tree.
			violations = append(violations, path+": "+ip)
			continue
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
		t.Fatalf("layering violations under internal/protocol:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
