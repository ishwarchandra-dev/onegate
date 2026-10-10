# Upgrading

This release's upgrade path is **OmniRoute v3.8.52 → OneGate v1.0.0**.
Future entries will appear here per the deprecation policy (two releases
of warning before anything is removed).

## OmniRoute v3.8.52 → OneGate v1.0.0

OneGate is a behavioral drop-in for OmniRoute v3.8.52: same proxy
paths, same error envelopes, same quota semantics — proven by a 41-case
replay corpus that runs in CI. The differences that matter to an
operator:

| OmniRoute v3.8.52 | OneGate v1.0.0 |
|---|---|
| Node runtime | Single static binary (no Node, no services) |
| File-only config | SQLite state + optional JSON config file (`onegate config` previews the resolved chain) |
| Raw provider keys in config | AES-GCM at rest; dashboard shows masked keys |
| — | Virtual keys (`ogk-`), quotas, routing policies, usage analytics, embedded dashboard |

### Step-by-step

1. **Survey first (read-only)** — dry-run the import against your
   legacy `omniroute.json`:

   ```bash
   onegate import /path/to/omniroute.json
   onegate import-keys /path/to/omniroute.json --usage-db /path/to/omniroute.db
   ```

   Both default to dry-run: the plan prints, nothing is written, the
   legacy install is only ever read. Idempotent when applied.

2. **Apply** to a fresh data directory:

   ```bash
   onegate import     /path/to/omniroute.json --apply --data-dir /var/lib/onegate
   onegate import-keys /path/to/omniroute.json --apply \
       --usage-db /path/to/omniroute.db \
       --keys-out /root/new-keys.txt \
       --data-dir /var/lib/onegate
   ```

   - Providers, models and routing rules import with idempotent
     mappings; unmapped rows are reported, never guessed.
   - Legacy key hashes are unrecoverable by design (scrypt), so keys
     are **re-minted**: new `ogk-…` raw keys print once (to
     `--keys-out`, mode 0600). Revoked stays revoked.
   - Usage history imports with aggregate-preserving rollups — your
     analytics continue where OmniRoute left them.

3. **Create the admin account** (first-run setup wizard on the
   dashboard, or `POST /api/auth/setup`), then review the imported
   topology in the dashboard (providers, routing, keys).

4. **Redistribute keys**: give each client its new `ogk-…` key. Point
   every client's SDK `baseURL` at the gateway and keep using the same
   model names and native paths. No client-side code changes beyond
   the credential.

5. **Cut over** traffic to OneGate and watch the Usage view; OmniRoute
   can serve as a fallback until you retire it. Both can read the same
   upstream provider accounts simultaneously.

### Things that are intentionally different

- **Envelope bodies on wrong-method responses** (parity A-11): status
  codes match; OmniRoute sent an error envelope, OneGate sends an empty
  body with `Allow`. Cosmetic — recorded in the parity report.
- **Trailing-slash variants** (`/v1/messages/`) are 404, not silent
  matches — same as OmniRoute, pinned by parity.
- **Dashboard sessions are in-memory**: a restart logs the dashboard
  out (nothing is lost; log back in). The admin account is durable.

### Rolling back

Replace the binary and restore the data-directory backup taken before
the migration. OmniRoute itself is untouched by any step above, so
repointing clients back is always available.
