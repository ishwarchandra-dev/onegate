package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// SessionTTLMS is the default dashboard session lifetime (24h).
const SessionTTLMS int64 = 24 * 60 * 60 * 1000

// sessionCookieName matches the OpenAPI securitySchemes.sessionCookie.
const sessionCookieName = "onegate_session"

// session is one authenticated dashboard session. IDs and CSRF tokens are
// 32 random bytes (hex); the ID never leaves the cookie (HttpOnly), the
// CSRF token is delivered in the session JSON for the double-submit pattern.
type session struct {
	id        string
	csrfToken string
	user      string
	expiresMS int64
}

// SessionManager is the in-memory session store. Sessions are runtime
// state (a gateway restart logs the dashboard out — acceptable for an
// embedded admin surface and keeps the store auditable); the admin
// account itself is durable in SQLite.
//
// Concurrency: RWMutex; expiry is enforced lazily on Get and swept on
// Create so no background goroutine is needed (nothing to leak).
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*session
	ttlMS    int64
	nowMS    func() int64
}

// NewSessionManager builds a store with the given TTL (default 24h when
// ttlMS <= 0). A nil clock selects the wall clock.
func NewSessionManager(ttlMS int64, nowMS func() int64) *SessionManager {
	if ttlMS <= 0 {
		ttlMS = SessionTTLMS
	}
	if nowMS == nil {
		nowMS = timeNowMS
	}
	return &SessionManager{
		sessions: map[string]*session{},
		ttlMS:    ttlMS,
		nowMS:    nowMS,
	}
}

// Create mints a session for user. Expired sessions are swept here, so
// the map only ever grows with live sessions.
func (m *SessionManager) Create(user string) (session, error) {
	id, err := randomHex(32)
	if err != nil {
		return session{}, err
	}
	csrf, err := randomHex(32)
	if err != nil {
		return session{}, err
	}
	now := m.nowMS()
	s := session{id: id, csrfToken: csrf, user: user, expiresMS: now + m.ttlMS}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Sweep expired entries (bounded work: sessions are few).
	for k, v := range m.sessions {
		if v.expiresMS <= now {
			delete(m.sessions, k)
		}
	}
	m.sessions[s.id] = &s
	return s, nil
}

// Get returns the live session for id, or false when absent/expired.
func (m *SessionManager) Get(id string) (session, bool) {
	if id == "" {
		return session{}, false
	}
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return session{}, false
	}
	if s.expiresMS <= m.nowMS() {
		m.Delete(id)
		return session{}, false
	}
	return *s, true
}

// Delete removes a session (logout). Idempotent.
func (m *SessionManager) Delete(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// randomHex renders n random bytes as hex (2n characters).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
