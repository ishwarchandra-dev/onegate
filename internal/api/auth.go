package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authResult describes an accepted credential: who is calling and by
// which mechanism. ViaSession drives CSRF enforcement — cookie-based
// credentials can be forged cross-origin by the browser, bearer tokens
// cannot.
type authResult struct {
	Principal  string
	ViaSession bool
}

// Authenticator decides whether a management request is authenticated.
// Implementations: admin-token (bootstrap/CLI) and session cookies
// (dashboard); the chain tries sessions first, then the token.
type Authenticator interface {
	// Authenticate returns the principal when the request may proceed.
	Authenticate(r *http.Request) (authResult, bool)
}

// chainAuthenticator tries each mechanism in order and returns the first
// success.
type chainAuthenticator struct {
	mechanisms []Authenticator
}

func (c chainAuthenticator) Authenticate(r *http.Request) (authResult, bool) {
	for _, m := range c.mechanisms {
		if res, ok := m.Authenticate(r); ok {
			return res, true
		}
	}
	return authResult{}, false
}

// TokenAuthenticator verifies the bootstrap admin token
// (ONEGATE_ADMIN_TOKEN) via Authorization: Bearer or X-Admin-Token.
// Comparison is constant-time. An empty configured token authenticates
// nobody (safe by default).
type TokenAuthenticator struct {
	token string
}

// NewTokenAuthenticator builds a TokenAuthenticator. Empty token = the
// authenticator rejects everything.
func NewTokenAuthenticator(token string) *TokenAuthenticator {
	return &TokenAuthenticator{token: token}
}

// Authenticate implements Authenticator.
func (t *TokenAuthenticator) Authenticate(r *http.Request) (authResult, bool) {
	if t.token == "" {
		return authResult{}, false
	}
	presented := bearerToken(r)
	if presented == "" {
		presented = r.Header.Get("X-Admin-Token")
	}
	if presented == "" {
		return authResult{}, false
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(t.token)) != 1 {
		return authResult{}, false
	}
	return authResult{Principal: "admin-token"}, true
}

// SessionAuthenticator validates the dashboard session cookie.
type SessionAuthenticator struct {
	sessions *SessionManager
}

// Authenticate implements Authenticator via the one gate session cookie.
func (s *SessionAuthenticator) Authenticate(r *http.Request) (authResult, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return authResult{}, false
	}
	sess, ok := s.sessions.Get(cookie.Value)
	if !ok {
		return authResult{}, false
	}
	return authResult{Principal: "user:" + sess.user, ViaSession: true}, true
}

// bearerToken extracts a Bearer credential (RFC 6750), tolerating case
// variation in the scheme.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	scheme, cred, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(cred)
}
