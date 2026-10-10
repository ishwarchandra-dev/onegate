// `onegate import` — migrate a legacy OmniRoute v3.8.52 installation
// into OneGate storage (p7.config-import). Dry-run by default;
// `--apply` writes the plan. Full reference: docs/cli.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/importer"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// importOpts holds parsed import flags.
type importOpts struct {
	dataDir string
	apply   bool
}

// importFlags defines the import flag set (shared by -h and parse).
func importFlags() (*flag.FlagSet, *importOpts) {
	fs := flag.NewFlagSet("onegate import", flag.ContinueOnError)
	o := &importOpts{}
	fs.StringVar(&o.dataDir, "data-dir", ".onegate", "OneGate data directory (import destination)")
	fs.BoolVar(&o.apply, "apply", false, "apply the plan (default: dry-run, nothing is written)")
	fs.Usage = helpScreen(fs, "import", "migrate a legacy OmniRoute install into this data dir (dry-run by default)",
		"onegate import <omniroute.json> [--apply] [--data-dir DIR]",
		[]string{
			"onegate import omniroute.json                       # plan only, nothing written",
			"onegate import omniroute.json --apply --data-dir /var/lib/onegate",
			"onegate import-keys omniroute.json                  # keys + usage history (separate command)",
		})
	return fs, o
}

// runImport implements the import subcommand.
func runImport(args []string) error {
	if helpRequested(args) {
		fs, _ := importFlags()
		printHelp(fs, os.Stdout)
		return nil
	}
	fs, o := importFlags()
	if err := fs.Parse(args); err != nil {
		if isFlagHelp(err) {
			printHelp(fs, os.Stdout)
			return nil
		}
		return newUsageErrorf("%v", err)
	}
	if fs.NArg() != 1 {
		return newUsageErrorf(
			"import needs exactly one file argument (the legacy omniroute.json), got %d — run `onegate import -h`",
			fs.NArg(),
		)
	}
	legacyPath := fs.Arg(0)

	data, err := os.ReadFile(legacyPath) // the legacy install is only ever read
	if err != nil {
		return fmt.Errorf("import: read %s: %w", legacyPath, err)
	}
	inst, err := importer.Parse(data)
	if err != nil {
		return err
	}

	// Destination store: open + migrate the OneGate data dir.
	if err := os.MkdirAll(o.dataDir, 0o755); err != nil {
		return fmt.Errorf("import: create data dir: %w", err)
	}
	store, err := storage.Open(filepath.Join(o.dataDir, "onegate.db"))
	if err != nil {
		return fmt.Errorf("import: open storage: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("import: migrate storage: %w", err)
	}

	plan, err := importer.BuildPlan(inst, importer.NewStoreSource(store))
	if err != nil {
		return err
	}

	fmt.Println(importer.ReportHeader(plan, o.apply))
	fmt.Println()
	fmt.Println(importer.Report(plan))

	if !o.apply {
		fmt.Println()
		fmt.Println("dry-run: nothing written — re-run with --apply")
		return nil
	}

	// Master key + cipher for sealing provider credentials.
	masterPath := filepath.Join(o.dataDir, "master.key")
	master, err := auth.MasterSecret(masterPath)
	if err != nil {
		return fmt.Errorf("import: master key: %w", err)
	}
	cipher, err := auth.NewCipher(master, auth.PurposeProviderKeys)
	if err != nil {
		return fmt.Errorf("import: cipher: %w", err)
	}

	if err := importer.Apply(plan, store, cipher); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("applied.")
	return nil
}
