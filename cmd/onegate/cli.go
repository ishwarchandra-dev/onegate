// Shared CLI plumbing for the onegate command (p9.cli-polish):
// usage errors (exit code 2), the -h/--help pre-scan (exit 0, stdout),
// and the shared help-screen renderer.
//
// Exit codes, published in docs/cli.md:
//
//	0  success (including -h/--help)
//	1  runtime error (startup, storage, import apply, ...)
//	2  usage error (unknown command, bad flag, wrong arg count)
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// usageError marks exit-code-2 problems: the user invoked the CLI
// wrongly. Runtime failures keep exit code 1.
type usageError struct{ msg string }

// Error implements error.
func (e *usageError) Error() string { return e.msg }

// newUsageErrorf builds a usage error with printf formatting.
func newUsageErrorf(format string, a ...any) *usageError {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// isUsageError reports whether err is a usage error (exit 2).
func isUsageError(err error) bool {
	var ue *usageError
	return errors.As(err, &ue)
}

// isFlagHelp reports whether err is the flag package's built-in ErrHelp.
func isFlagHelp(err error) bool { return errors.Is(err, flag.ErrHelp) }

// helpRequested scans args for -h/--help BEFORE flag parsing, so help
// lands on stdout with exit 0 (the flag package's own ErrHelp path
// writes to stderr; the pre-scan makes -h conventional).
// Scanning stops at "--" (everything after is positional).
func helpRequested(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

// printHelp renders fs.Usage() to w by temporarily repointing the
// flagset's output (stdout for -h, stderr for parse errors — same
// renderer, conventional stream each time).
func printHelp(fs *flag.FlagSet, w io.Writer) {
	prev := fs.Output()
	fs.SetOutput(w)
	defer fs.SetOutput(prev)
	fs.Usage()
}

// helpScreen builds a fs.Usage func rendering the standard shape:
//
//	onegate <command> — one-line purpose
//
//	Usage:
//	  <usageLine>
//
//	Flags:
//	  (flag.PrintDefaults)
//
//	Examples:
//	  ...
//
//	Full reference: docs/cli.md
func helpScreen(fs *flag.FlagSet, command, purpose, usageLine string, examples []string) func() {
	return func() {
		var b strings.Builder
		fmt.Fprintf(&b, "onegate %s — %s\n\n", command, purpose)
		fmt.Fprintf(&b, "Usage:\n  %s\n\n", usageLine)
		b.WriteString("Flags:\n")
		fmt.Fprint(&b, flagDefaults(fs))
		b.WriteString("\n")
		if len(examples) > 0 {
			b.WriteString("Examples:\n")
			for _, e := range examples {
				fmt.Fprintf(&b, "  %s\n", e)
			}
			b.WriteString("\n")
		}
		b.WriteString("Full reference: docs/cli.md\n")
		fmt.Fprint(fs.Output(), b.String())
	}
}

// flagDefaults renders flag.PrintDefaults into a string via the same
// output-swap printHelp uses (a renderer, not a copy of the flag text).
func flagDefaults(fs *flag.FlagSet) string {
	var sb strings.Builder
	prev := fs.Output()
	fs.SetOutput(&sb)
	defer fs.SetOutput(prev)
	fs.PrintDefaults()
	return sb.String()
}
