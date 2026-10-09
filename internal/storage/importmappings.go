package storage

// ImportMappingRepo tracks legacy OmniRoute id -> OneGate id mappings
// (p7.data-import). It is the idempotency source of truth for
// `onegate import-keys`: every imported legacy entity records its
// counterpart here, so re-imports skip instead of duplicate.
import (
	"database/sql"
	"time"
)

// ImportMapping is one legacy -> OneGate id pair.
type ImportMapping struct {
	Kind       string // "key" | "usage_event"
	LegacyID   string
	OneGateID  string
	ImportedMS int64
}

// ImportMappingRepo wraps the mapping table.
type ImportMappingRepo struct{ s *Store }

// ImportMappings returns the repo.
func (s *Store) ImportMappings() *ImportMappingRepo { return &ImportMappingRepo{s} }

// Put records a mapping (idempotent by primary key).
func (r *ImportMappingRepo) Put(m ImportMapping) error {
	if m.ImportedMS == 0 {
		m.ImportedMS = time.Now().UnixMilli()
	}
	_, err := r.s.db.Exec(
		`INSERT OR REPLACE INTO import_mappings (legacy_kind, legacy_id, onegate_id, imported_ms)
		 VALUES (?, ?, ?, ?)`,
		m.Kind, m.LegacyID, m.OneGateID, m.ImportedMS)
	return err
}

// Get returns the OneGate id for a legacy entity, if imported.
func (r *ImportMappingRepo) Get(kind, legacyID string) (string, bool, error) {
	var onegateID string
	err := r.s.db.QueryRow(
		`SELECT onegate_id FROM import_mappings WHERE legacy_kind = ? AND legacy_id = ?`,
		kind, legacyID).Scan(&onegateID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return onegateID, true, nil
}

// Count reports how many mappings of a kind exist.
func (r *ImportMappingRepo) Count(kind string) (int, error) {
	var n int
	err := r.s.db.QueryRow(
		`SELECT COUNT(*) FROM import_mappings WHERE legacy_kind = ?`, kind).Scan(&n)
	return n, err
}
