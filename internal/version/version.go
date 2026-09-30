// Package version exposes build metadata for the OneGate binary.
//
// Values are injected at build time via:
//
//	-ldflags "-X github.com/ishwarchandra-dev/onegate/internal/version.Version=v1.0.0 \
//	          -X github.com/ishwarchandra-dev/onegate/internal/version.GitCommit=$(git rev-parse --short HEAD) \
//	          -X github.com/ishwarchandra-dev/onegate/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// The Makefile wires this automatically; see the `build` target.
package version

import "fmt"

var (
	// Version is the semantic version. "0.0.0-dev" on main, "vX.Y.Z" on releases.
	Version = "0.0.0-dev"

	// GitCommit is the short hash of the commit the binary was built from.
	GitCommit = "none"

	// BuildDate is the RFC3339 UTC timestamp of the build.
	BuildDate = "unknown"
)

// String returns a human-readable one-line version banner.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, GitCommit, BuildDate)
}
