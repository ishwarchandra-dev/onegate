package importer

// Human-readable diff report for `onegate import` (dry-run and apply).
import (
        "fmt"
        "strings"
)

// Report renders the plan as the CLI diff report: counts, per-entity
// changes, skips, and unmapped fields.
func Report(plan *Plan) string {
        var b strings.Builder
        created, updated, unchanged, skipped := 0, 0, 0, 0
        for _, p := range plan.Providers {
                countChange(p.Change, &created, &updated, &unchanged, &skipped)
        }
        mCreated, mUpdated, mUnchanged := 0, 0, 0
        for _, m := range plan.Models {
                countChange(m.Change, &mCreated, &mUpdated, &mUnchanged, nil)
        }
        rCreated, rUpdated, rUnchanged := 0, 0, 0
        for _, r := range plan.Rules {
                countChange(r.Change, &rCreated, &rUpdated, &rUnchanged, nil)
        }

        fmt.Fprintf(&b, "providers: %d create, %d update, %d unchanged", created, updated, unchanged)
        if skipped > 0 {
                fmt.Fprintf(&b, ", %d SKIPPED (invalid: %d)", skipped, skipped)
        }
        b.WriteByte('\n')
        for _, p := range plan.Providers {
                if p.Change == "unchanged" {
                        continue
                }
                fmt.Fprintf(&b, "  %s provider %s (%s) %s\n", p.Change, p.Provider.ID, p.Provider.Protocol, p.Reason)
        }
        fmt.Fprintf(&b, "models: %d create, %d update, %d unchanged\n", mCreated, mUpdated, mUnchanged)
        for _, m := range plan.Models {
                if m.Change == "unchanged" {
                        continue
                }
                fmt.Fprintf(&b, "  %s model %s (%d targets) %s\n", m.Change, m.Model.ID, len(m.Model.Targets), m.Reason)
        }
        fmt.Fprintf(&b, "routing rules: %d create, %d update, %d unchanged\n", rCreated, rUpdated, rUnchanged)
        for _, r := range plan.Rules {
                if r.Change == "unchanged" {
                        continue
                }
                fmt.Fprintf(&b, "  %s rule %s -> %s %s\n", r.Change, r.Rule.ModelID, r.Rule.Policy, r.Reason)
        }

        if len(plan.RuntimeSkip) > 0 {
                b.WriteString("skipped (runtime settings, not data):\n")
                for _, s := range plan.RuntimeSkip {
                        fmt.Fprintf(&b, "  - %s\n", s)
                }
        }
        if len(plan.Unmapped) > 0 {
                b.WriteString("unmapped legacy fields (no OneGate equivalent):\n")
                for _, u := range plan.Unmapped {
                        fmt.Fprintf(&b, "  - %s\n", u)
                }
        }
        if plan.KeysDeferred > 0 {
                fmt.Fprintf(&b, "keys: %d deferred (run with --keys to import, p7.data-import)\n", plan.KeysDeferred)
        }
        return strings.TrimRight(b.String(), "\n")
}

func countChange(change string, created, updated, unchanged, skipped *int) {
        switch change {
        case "create":
                if created != nil {
                        *created++
                }
        case "update":
                if updated != nil {
                        *updated++
                }
        case "skip":
                if skipped != nil {
                        *skipped++
                }
        default:
                if unchanged != nil {
                        *unchanged++
                }
        }
}

// ReportHeader renders the one-line mode banner for the CLI.
func ReportHeader(plan *Plan, apply bool) string {
        mode := "dry-run (no changes written; pass --apply)"
        if apply {
                mode = "APPLY"
        }
        changes := 0
        for _, p := range plan.Providers {
                if p.Change == "create" || p.Change == "update" {
                        changes++
                }
        }
        for _, m := range plan.Models {
                if m.Change == "create" || m.Change == "update" {
                        changes++
                }
        }
        for _, r := range plan.Rules {
                if r.Change == "create" || r.Change == "update" {
                        changes++
                }
        }
        return fmt.Sprintf("onegate import — %s — %d change(s) planned", mode, changes)
}
