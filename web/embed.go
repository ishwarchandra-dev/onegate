// Package web embeds the built dashboard distribution.
//
// This Go file lives inside web/ (the dashboard workspace) because
// go:embed patterns cannot traverse upwards — build/client is only
// reachable from a package whose directory contains it.
//
// A fresh clone contains only the committed placeholder
// build/client/index.html ("dashboard not built"), so `go build ./...`
// works without a Node toolchain. The real pipeline (`make web` then
// `make build`) overwrites the placeholder with the SPA build
// (ADR 006); internal/webfs.IsPlaceholder detects which one is
// embedded, and the release path (ONEGATE_REQUIRE_EMBED=1) refuses
// placeholder binaries.
package web

import (
	"embed"
	"io/fs"
)

// dist holds the dashboard build output. `all:` includes dotfiles and
// keeps directory structure; gzip-precompressed siblings (*.gz, made
// by `make web`) ride along and are served when the client accepts.
//
//go:embed all:build/client
var dist embed.FS

// Dist returns the embedded dashboard filesystem rooted at the build
// output (index.html + assets/).
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "build/client")
	if err != nil {
		// The pattern is a compile-time constant; Sub cannot fail.
		panic("web: embed pattern broken: " + err.Error())
	}
	return sub
}
