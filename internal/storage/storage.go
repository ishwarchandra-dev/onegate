// Package storage owns OneGate's embedded SQLite database: open/close,
// WAL tuning, migrations, and the repository layer for every aggregate.
//
// Concurrency model (docs/architecture.md "Runtime model"):
//   - WAL mode + busy_timeout so readers never block the writer.
//   - The proxy hot path never touches the database (Phase 5 usage writer
//     is the only writer); repositories here are used by management and
//     background paths.
//   - Driver: modernc.org/sqlite (pure Go, no cgo) so the single binary
//     cross-compiles trivially. See docs/adr/003-sqlite-driver.md.
package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Store wraps the SQLite database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies pragmas.
// The caller must ensure the parent directory exists (config.EnsureDataDir).
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create dir: %w", err)
	}
	// pragmas: WAL for concurrent readers + one writer; busy_timeout rides
	// out brief lock contention; foreign_keys ON for cascade integrity;
	// synchronous NORMAL is the recommended WAL companion.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	// modernc/sqlite serializes writes; a single connection avoids
	// "database is locked" between our own statements while WAL readers
	// run on their own snapshots.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: ping: %w", err)
	}
	return &Store{db: db}, nil
}

// OpenTemp opens a throwaway database in the system temp dir. Tests use
// this; production always goes through Open.
func OpenTemp() (*Store, error) {
	dir, err := os.MkdirTemp("", "onegate-test-*")
	if err != nil {
		return nil, err
	}
	return Open(filepath.Join(dir, "test.db"))
}

// DB exposes the raw handle (repositories; not for the faint of heart).
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database, flushing WAL.
func (s *Store) Close() error { return s.db.Close() }
