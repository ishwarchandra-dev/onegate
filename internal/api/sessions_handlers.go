package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// usernamePattern matches the spec's Username schema.
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,64}$`)

// minPasswordLen matches the spec's Password schema.
const minPasswordLen = 12

// credentials is the login/setup request body.
type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// sessionInfo is the SessionInfo response shape (spec).
type sessionInfo struct {
	User        sessionUser `json:"user"`
	CSRFToken   string      `json:"csrf_token"`
	ExpiresAtMS int64       `json:"expires_at_ms"`
}

type sessionUser struct {
	Username string `json:"username"`
}

// handleSetupStatus GET /api/auth/setup-status (public): true until the
// first admin account exists.
func (a *API) handleSetupStatus(w http.ResponseWriter, _ *http.Request) {
	required := true
	if n, err := a.opts.Store.AdminUsers().Count(); err == nil {
		required = n == 0
	}
	writeJSON(w, http.StatusOK, struct {
		SetupRequired bool `json:"setup_required"`
	}{SetupRequired: required})
}

// handleCreateAdmin POST /api/auth/setup (public, first run only):
// creates the admin account and logs it in.
func (a *API) handleCreateAdmin(w http.ResponseWriter, r *http.Request) {
	if a.opts.Sessions == nil {
		a.handleNotYetSessions(w, r)
		return
	}
	var body credentials
	if !decodeJSON(w, r, &body) {
		return
	}
	if verr := validateCredentials(body); verr != nil {
		writeError(w, *verr)
		return
	}
	// First-run gate: exactly one admin may ever exist through this flow.
	if n, err := a.opts.Store.AdminUsers().Count(); err != nil {
		a.mapStorageError(w, err, "admin account")
		return
	} else if n > 0 {
		writeError(w, domain.GatewayError{
			Status:  http.StatusForbidden,
			Type:    domain.ErrPermission,
			Code:    "setup_already_done",
			Message: "an admin account already exists; use login",
		})
		return
	}
	hash, err := a.opts.PasswordHasher(body.Password)
	if err != nil {
		a.mapStorageError(w, err, "admin account")
		return
	}
	rec := storage.AdminUser{Username: body.Username, PasswordHash: hash}
	if err := a.opts.Store.AdminUsers().Create(&rec); err != nil {
		a.mapStorageError(w, err, "admin account")
		return
	}
	a.logger.Info("admin account created (first-run setup)", "username", body.Username)
	a.startSession(w, r, body.Username, http.StatusCreated)
}

// handleLogin POST /api/auth/login (public): credentials -> session
// cookie. Unknown user and wrong password answer identically (no
// username enumeration) and both burn the same hashing work.
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	if a.opts.Sessions == nil {
		a.handleNotYetSessions(w, r)
		return
	}
	var body credentials
	if !decodeJSON(w, r, &body) {
		return
	}
	user, err := a.opts.Store.AdminUsers().Get(body.Username)
	if err != nil {
		if err == storage.ErrNotFound {
			// Equalize timing: burn the same PBKDF2 work a real
			// verification would cost, so response time does not reveal
			// which usernames exist.
			auth.BurnPasswordWork(body.Password, a.opts.PasswordIterations)
			a.loginRejected(w)
			return
		}
		a.mapStorageError(w, err, "admin account")
		return
	}
	ok, err := auth.VerifyPassword(user.PasswordHash, body.Password)
	if err != nil || !ok {
		a.loginRejected(w)
		return
	}
	a.startSession(w, r, user.Username, http.StatusOK)
}

// loginRejected renders the uniform 401 for failed logins.
func (a *API) loginRejected(w http.ResponseWriter) {
	writeError(w, domain.GatewayError{
		Status:  http.StatusUnauthorized,
		Type:    domain.ErrAuthentication,
		Code:    "invalid_credentials",
		Message: "invalid username or password",
	})
}

// handleLogout POST /api/auth/logout: destroys the session server-side
// and expires the cookie.
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if a.opts.Sessions == nil {
		a.handleNotYetSessions(w, r)
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		a.opts.Sessions.Delete(cookie.Value)
	}
	// Expire the cookie regardless of whether one was presented.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleGetSession GET /api/auth/session: the current principal plus the
// CSRF token for subsequent mutations.
func (a *API) handleGetSession(w http.ResponseWriter, r *http.Request) {
	if a.opts.Sessions == nil {
		a.handleNotYetSessions(w, r)
		return
	}
	sess, ok := sessionFromRequest(a.opts.Sessions, r)
	if !ok {
		writeError(w, domain.GatewayError{
			Status:  http.StatusUnauthorized,
			Type:    domain.ErrAuthentication,
			Code:    "unauthenticated",
			Message: "authentication required",
		})
		return
	}
	writeJSON(w, http.StatusOK, sessionInfo{
		User:        sessionUser{Username: sess.user},
		CSRFToken:   sess.csrfToken,
		ExpiresAtMS: sess.expiresMS,
	})
}

// startSession mints a session, sets the hardened cookie, and returns
// the SessionInfo body.
func (a *API) startSession(w http.ResponseWriter, r *http.Request, username string, status int) {
	sess, err := a.opts.Sessions.Create(username)
	if err != nil {
		a.mapStorageError(w, err, "session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.id,
		Path:     "/",
		MaxAge:   int(a.opts.Sessions.ttlMS / 1000),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
	})
	writeJSON(w, status, sessionInfo{
		User:        sessionUser{Username: username},
		CSRFToken:   sess.csrfToken,
		ExpiresAtMS: sess.expiresMS,
	})
}

// requestIsHTTPS reports TLS (direct or via a terminating proxy that
// forwards the standard proto header).
func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// sessionFromRequest resolves the live session for a request.
func sessionFromRequest(m *SessionManager, r *http.Request) (session, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return session{}, false
	}
	return m.Get(cookie.Value)
}

// validateCredentials enforces the spec's Username/Password schemas.
func validateCredentials(c credentials) *domain.GatewayError {
	if !usernamePattern.MatchString(c.Username) {
		e := errInvalid("username must be 3-64 characters (letters, digits, . _ -)", "username")
		return &e
	}
	if len(c.Password) < minPasswordLen {
		e := errInvalid("password must be at least 12 characters", "password")
		return &e
	}
	return nil
}
