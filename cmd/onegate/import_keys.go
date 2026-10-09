// `onegate import-keys` — migrate legacy virtual keys and usage history
// (p7.data-import). Dry-run by default; raw keys are shown exactly once
// under --apply (written to --keys-out, mode 0600, or stdout).
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

func runImportKeys(args []string) error {
	fs := flag.NewFlagSet("onegate import-keys", flag.ContinueOnError)
	var (
		flagDataDir = fs.String("data-dir", ".onegate", "OneGate data directory (import destination)")
		flagApply   = fs.Bool("apply", false, "apply the import (default: dry-run)")
		flagUsageDB = fs.String("usage-db", "", "legacy omniroute.db to import usage history from (read-only)")
		flagKeysOut = fs.String("keys-out", "", "write minted raw keys to this file (0600) instead of stdout")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: onegate import-keys <omniroute.json> [--apply] [--usage-db PATH] [--keys-out FILE] [--data-dir DIR]")
	}
	legacyPath := fs.Arg(0)

	data, err := os.ReadFile(legacyPath) // legacy install is only ever read
	if err != nil {
		return fmt.Errorf("import-keys: read %s: %w", legacyPath, err)
	}
	inst, err := importer.Parse(data)
	if err != nil {
		return err
	}

	usage, err := importer.ReadLegacyUsage(*flagUsageDB)
	if err != nil {
		return err
	}

	if !*flagApply {
		fmt.Println("onegate import-keys — dry-run (no changes written; pass --apply)")
		fmt.Println()
		fmt.Printf("keys: %d legacy entries, %d revoked-status, %d active-status\n",
			len(inst.Keys), countStatus(inst.Keys, "revoked"), countStatus(inst.Keys, "active"))
		if len(usage) > 0 {
			fmt.Printf("usage events: %d rows read from %s (models and keys are resolved at apply time)\n",
				len(usage), *flagUsageDB)
		}
		fmt.Println()
		fmt.Println("dry-run: nothing written — re-run with --apply")
		return nil
	}

	if err := os.MkdirAll(*flagDataDir, 0o755); err != nil {
		return fmt.Errorf("import-keys: create data dir: %w", err)
	}
	store, err := storage.Open(filepath.Join(*flagDataDir, "onegate.db"))
	if err != nil {
		return fmt.Errorf("import-keys: open storage: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("import-keys: migrate storage: %w", err)
	}

	masterPath := filepath.Join(*flagDataDir, "master.key")
	master, err := auth.MasterSecret(masterPath)
	if err != nil {
		return fmt.Errorf("import-keys: master key: %w", err)
	}
	pepper, err := auth.Pepper(master)
	if err != nil {
		return fmt.Errorf("import-keys: pepper: %w", err)
	}

	fmt.Println("onegate import-keys — APPLY")
	fmt.Println()
	keyRes, err := importer.ImportKeys(inst, store, pepper)
	if err != nil {
		return err
	}
	fmt.Println(importer.RenderKeysReport(keyRes))

	if len(usage) > 0 {
		usageRes, err := importer.ImportUsage(usage, store)
		if err != nil {
			return err
		}
		fmt.Println(importer.RenderUsageReport(usageRes))
	}

	// Show-once raw keys: file (0600) or stdout.
	if len(keyRes.Minted) > 0 {
		out := os.Stdout
		if *flagKeysOut != "" {
			f, err := os.OpenFile(*flagKeysOut, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return fmt.Errorf("import-keys: keys-out: %w", err)
			}
			defer f.Close()
			out = f
		}
		fmt.Fprintln(out, "# raw keys — shown once; store securely and distribute to clients")
		for _, k := range keyRes.Minted {
			if k.RawKey != "" {
				fmt.Fprintf(out, "%s\t%s\t%s\n", k.RawKey, k.Name, k.LegacyID)
			}
		}
		if *flagKeysOut != "" {
			fmt.Printf("\nraw keys written to %s (mode 0600) — NOT shown again\n", *flagKeysOut)
		} else {
			fmt.Println("\nraw keys above — shown ONCE; store them securely now")
		}
	}
	return nil
}

func countStatus(keys []importer.LegacyKey, status string) int {
	n := 0
	for _, k := range keys {
		if k.Status == status {
			n++
		}
	}
	return n
}
