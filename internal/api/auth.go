package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Authenticator decides whether a management request is authenticated and
// under which principal. p6.api-impl ships the admin-token verifier;
// p6.auth-sessions will chain a session-cookie verifier (plus CSRF) in
// front of it with the same interface.
type Authenticator interface {
	// Authenticate returns the principal identifier and true when the
	// request may proceed; false renders 401.
	Authenticate(r *http.Request) (principal string, ok bool)
}

// TokenAuthenticator verifies the bootstrap admin token
// (ONEGATE_ADMIN_TOKEN) via the Authorization: Bearer header or the
// X-Admin-Token header. Comparison is constant-time. An empty configured
// token authenticates nobody — admin routes then require the session
// authenticator (safe by default).
type TokenAuthenticator struct {
	token string
}

// NewTokenAuthenticator builds a TokenAuthenticator. Empty token = the
// authenticator rejects everything.
func NewTokenAuthenticator(token string) *TokenAuthenticator {
	return &TokenAuthenticator{token: token}
}

// Authenticate implements Authenticator.
func (t *TokenAuthenticator) Authenticate(r *http.Request) (string, bool) {
	if t.token == "" {
		return "", false
	}
	presented := bearerToken(r)
	if presented == "" {
		presented = r.Header.Get("X-Admin-Token")
	}
	if presented == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(t.token)) != 1 {
		return "", false
	}
	return "admin-token", true
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
