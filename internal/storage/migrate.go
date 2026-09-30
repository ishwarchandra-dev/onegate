package storage

import (
	"database/sql"
	"fmt"
)

// migration is one forward-only schema step. Never edit an applied
// migration; add a new one. Down-migration is "restore from backup" by
// policy (documented in the graph node p1.storage-schema).
type migration struct {
	version int
	stmts   []string
}

// migrations lists every schema version in order. version N contains the
// statements that transform version N-1 into N.
var migrations = []migration{
	{
		version: 1,
		stmts: []string{
			`CREATE TABLE providers (
				id          TEXT PRIMARY KEY,
				name        TEXT NOT NULL,
				protocol    TEXT NOT NULL,
				base_url    TEXT NOT NULL DEFAULT '',
				api_key_enc BLOB NOT NULL DEFAULT x'',
				enabled     INTEGER NOT NULL DEFAULT 1,
				created_ms  INTEGER NOT NULL,
				updated_ms  INTEGER NOT NULL
			)`,
			`CREATE TABLE models (
				id           TEXT PRIMARY KEY,
				aliases_json TEXT NOT NULL DEFAULT '[]',
				caps_json    TEXT NOT NULL DEFAULT '{}',
				created_ms   INTEGER NOT NULL,
				updated_ms   INTEGER NOT NULL
			)`,
			`CREATE TABLE model_targets (
				model_id        TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
				provider_id     TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
				provider_model  TEXT NOT NULL,
				weight          INTEGER NOT NULL DEFAULT 1,
				position        INTEGER NOT NULL DEFAULT 0,
				cost_multiplier INTEGER NOT NULL DEFAULT 100,
				PRIMARY KEY (model_id, provider_id, provider_model)
			)`,
			`CREATE INDEX idx_model_targets_model ON model_targets(model_id, position)`,
			`CREATE TABLE virtual_keys (
				id           TEXT PRIMARY KEY,
				name         TEXT NOT NULL,
				prefix       TEXT NOT NULL,
				key_hash     TEXT NOT NULL,
				scopes_json  TEXT NOT NULL DEFAULT '{}',
				limits_json  TEXT NOT NULL DEFAULT '{}',
				status       TEXT NOT NULL DEFAULT 'active',
				created_ms   INTEGER NOT NULL,
				expires_ms   INTEGER NOT NULL DEFAULT 0,
				last_used_ms INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX idx_vkeys_hash ON virtual_keys(key_hash)`,
			`CREATE TABLE routing_rules (
				id       TEXT PRIMARY KEY,
				model_id TEXT NOT NULL,
				policy   TEXT NOT NULL DEFAULT 'ordered',
				enabled  INTEGER NOT NULL DEFAULT 1,
				position INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX idx_rules_model ON routing_rules(model_id, position)`,
			`CREATE TABLE requests (
				id                TEXT PRIMARY KEY,
				trace_id          TEXT NOT NULL,
				vkey_id           TEXT NOT NULL DEFAULT '',
				model_requested   TEXT NOT NULL DEFAULT '',
				model_served      TEXT NOT NULL DEFAULT '',
				provider_id       TEXT NOT NULL DEFAULT '',
				status            TEXT NOT NULL,
				error_code        TEXT NOT NULL DEFAULT '',
				stream            INTEGER NOT NULL DEFAULT 0,
				prompt_tokens     INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens      INTEGER NOT NULL DEFAULT 0,
				cost_usd_micros   INTEGER NOT NULL DEFAULT 0,
				latency_ms        INTEGER NOT NULL DEFAULT 0,
				ttft_ms           INTEGER NOT NULL DEFAULT 0,
				attempts          INTEGER NOT NULL DEFAULT 0,
				created_ms        INTEGER NOT NULL
			)`,
			`CREATE INDEX idx_requests_created ON requests(created_ms DESC)`,
			`CREATE INDEX idx_requests_vkey ON requests(vkey_id, created_ms DESC)`,
			`CREATE TABLE usage_rollups (
				bucket_start_ms   INTEGER NOT NULL,
				vkey_id           TEXT NOT NULL DEFAULT '',
				model_id          TEXT NOT NULL DEFAULT '',
				provider_id       TEXT NOT NULL DEFAULT '',
				requests          INTEGER NOT NULL DEFAULT 0,
				errors            INTEGER NOT NULL DEFAULT 0,
				prompt_tokens     INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens      INTEGER NOT NULL DEFAULT 0,
				cost_usd_micros   INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (bucket_start_ms, vkey_id, model_id, provider_id)
			)`,
		},
	},
}

// CurrentVersion is the schema version this binary understands.
func CurrentVersion() int { return migrations[len(migrations)-1].version }

// Migrate applies all pending migrations inside transactions. It is
// idempotent: applied versions are recorded in schema_migrations and
// skipped. Fresh databases run every migration in order.
func (s *Store) Migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_ms INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("storage: migrate bootstrap: %w", err)
	}

	for _, m := range migrations {
		applied, err := s.versionApplied(m.version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := s.applyMigration(m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) versionApplied(version int) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM schema_migrations WHERE version = ?`, version).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, fmt.Errorf("storage: check migration %d: %w", version, err)
	default:
		return true, nil
	}
}

func (s *Store) applyMigration(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("storage: begin migration %d: %w", m.version, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	for _, stmt := range m.stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("storage: migration %d stmt failed: %w (SQL: %.120s)", m.version, err, stmt)
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, applied_ms) VALUES (?, unixepoch()*1000)`,
		m.version,
	); err != nil {
		return fmt.Errorf("storage: record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit migration %d: %w", m.version, err)
	}
	return nil
}

// SchemaVersion returns the currently applied schema version.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("storage: schema version: %w", err)
	}
	return v, nil
}
