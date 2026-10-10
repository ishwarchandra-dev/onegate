// `onegate config` — print the fully resolved runtime configuration as
// JSON and exit (p9.cli-polish). Resolution is identical to serve:
// defaults <- config file <- environment <- flags, so the output is
// exactly what `onegate serve` would boot with. Nothing sensitive is
// printed: provider keys live in SQLite (encrypted), the master key is
// a file, the admin token is an env var — the resolved Config carries
// none of them.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"

	"github.com/ishwarchandra-dev/onegate/internal/config"
)

// runConfigCmd implements `onegate config [flags]`.
func runConfigCmd(args []string, stdout io.Writer) error {
	if helpRequested(args) {
		fs, _ := configFlags()
		printHelp(fs, os.Stdout)
		return nil
	}
	fs, o := configFlags()
	if err := fs.Parse(args); err != nil {
		if isFlagHelp(err) {
			printHelp(fs, os.Stdout)
			return nil
		}
		return newUsageErrorf("%v", err)
	}
	if fs.NArg() > 0 {
		return newUsageErrorf("config takes no positional arguments (got %q) — run `onegate config -h`", fs.Arg(0))
	}

	cfg, err := config.Load(config.LoadOptions{Path: o.config})
	if err != nil {
		return err
	}
	if o.host != "" {
		cfg.Host = o.host
	}
	if o.port != 0 {
		cfg.Port = o.port
	}
	if o.dataDir != "" {
		cfg.DataDir = o.dataDir
	}
	if o.logLevel != "" {
		cfg.LogLevel = o.logLevel
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg)
}

// configOpts mirrors serveOpts minus -version (config never serves).
type configOpts struct {
	host     string
	port     int
	dataDir  string
	logLevel string
	config   string
}

// configFlags defines the flags config shares with serve (same names,
// same defaults, same help text — one voice across the CLI).
func configFlags() (*flag.FlagSet, *configOpts) {
	fs := flag.NewFlagSet("onegate config", flag.ContinueOnError)
	o := &configOpts{}
	fs.StringVar(&o.host, "host", "", "address serve would listen on (default 127.0.0.1; overrides config/env)")
	fs.IntVar(&o.port, "port", 0, "port serve would listen on (default 7420; overrides config/env)")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory for onegate.db + master.key (default .onegate; overrides config/env)")
	fs.StringVar(&o.logLevel, "log-level", "", "debug | info | warn | error (default info; overrides config/env)")
	fs.StringVar(&o.config, "config", "", "explicit config file path (default: $ONEGATE_CONFIG, ./onegate.json, ~/.onegate/onegate.json)")
	fs.Usage = helpScreen(fs, "config", "print the fully resolved runtime configuration as JSON and exit",
		"onegate config [-host H] [-port P] [-data-dir DIR] [-log-level L] [-config FILE]",
		[]string{
			"onegate config                       # what serve would use, right now",
			"ONEGATE_PORT=9000 onegate config     # preview an env override",
			"onegate config -config prod.json | jq .http",
		})
	return fs, o
}
