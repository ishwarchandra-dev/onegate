package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

// AdminUser is the persisted dashboard administrator (p6.auth-sessions).
// Exactly one row is expected; the first-run setup flow creates it and
// further setup attempts are refused.
type AdminUser struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"` // PBKDF2 envelope from internal/auth; never serialized
	CreatedMS    int64  `json:"created_ms"`
}

// AdminUserRepo persists dashboard admin accounts.
type AdminUserRepo struct{ s *Store }

// AdminUsers returns the AdminUserRepo accessor.
func (s *Store) AdminUsers() *AdminUserRepo { return &AdminUserRepo{s} }

// Create inserts an admin account, assigning id/timestamps.
func (r *AdminUserRepo) Create(u *AdminUser) error {
	if u.ID == "" {
		u.ID = newID("adm")
	}
	if u.CreatedMS == 0 {
		u.CreatedMS = nowMS()
	}
	_, err := r.s.db.Exec(`INSERT INTO admin_users (id, username, password_hash, created_ms)
		VALUES (?, ?, ?, ?)`, u.ID, u.Username, u.PasswordHash, u.CreatedMS)
	if err != nil {
		return fmt.Errorf("storage: create admin user: %w", err)
	}
	return nil
}

// Get fetches an admin by username.
func (r *AdminUserRepo) Get(username string) (AdminUser, error) {
	var u AdminUser
	err := r.s.db.QueryRow(`SELECT id, username, password_hash, created_ms
		FROM admin_users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, fmt.Errorf("storage: get admin user: %w", err)
	}
	return u, nil
}

// Count returns the number of admin accounts (0 = first-run setup required).
func (r *AdminUserRepo) Count() (int, error) {
	var n int
	if err := r.s.db.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count admin users: %w", err)
	}
	return n, nil
}
