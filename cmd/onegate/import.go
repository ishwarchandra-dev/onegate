// `onegate import` — migrate a legacy OmniRoute v3.8.52 installation
// into OneGate storage (p7.config-import). Dry-run by default.
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

func runImport(args []string) error {
	fs := flag.NewFlagSet("onegate import", flag.ContinueOnError)
	var (
		flagDataDir = fs.String("data-dir", ".onegate", "OneGate data directory (import destination)")
		flagApply   = fs.Bool("apply", false, "apply the plan (default: dry-run)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: onegate import <omniroute.json> [--apply] [--data-dir DIR]\n" +
			"       (keys and usage history import: `onegate import-keys`, p7.data-import)")
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
	if err := os.MkdirAll(*flagDataDir, 0o755); err != nil {
		return fmt.Errorf("import: create data dir: %w", err)
	}
	store, err := storage.Open(filepath.Join(*flagDataDir, "onegate.db"))
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

	fmt.Println(importer.ReportHeader(plan, *flagApply))
	fmt.Println()
	fmt.Println(importer.Report(plan))

	if !*flagApply {
		fmt.Println()
		fmt.Println("dry-run: nothing written — re-run with --apply")
		return nil
	}

	// Master key + cipher for sealing provider credentials.
	masterPath := filepath.Join(*flagDataDir, "master.key")
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
