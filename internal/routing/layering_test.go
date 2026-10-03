package routing_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestLayering enforces the Phase 4 architecture constraint:
// "internal/routing NEVER imports internal/storage (dependency inversion via
// StorageSource interface)."
// Production files in internal/routing may depend ONLY on the Go standard library
// and internal/domain. They must have zero dependencies on storage, proxy,
// server, config, auth, or external third-party modules.
func TestLayering(t *testing.T) {
	root := "."
	forbidden := []string{
		"internal/storage",
		"internal/proxy",
		"internal/server",
		"internal/api",
		"internal/auth",
		"internal/config",
		"internal/observability",
		"internal/ratelimit",
	}

	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		for _, imp := range file.Imports {
			ip := strings.Trim(imp.Path.Value, `"`)

			// External dependencies are forbidden outright.
			if strings.Contains(ip, ".") && !strings.HasPrefix(ip, "github.com/ishwarchandra-dev/onegate") {
				violations = append(violations, path+": external import "+ip)
				continue
			}

			// Must not import forbidden internal packages.
			for _, f := range forbidden {
				if strings.Contains(ip, f) {
					violations = append(violations, path+": forbidden internal import "+ip)
				}
			}

			// Only internal/domain is allowed as an internal dependency.
			if strings.HasPrefix(ip, "github.com/ishwarchandra-dev/onegate") &&
				ip != "github.com/ishwarchandra-dev/onegate/internal/domain" {
				violations = append(violations, path+": unauthorized internal import "+ip)
			}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("routing layering contract violated by %d import(s):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}
