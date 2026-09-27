package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/SvetlovA/lampa/backend/pkg/api"
)

// maxRefreshInterval caps how long a refresh token is used before it is refreshed, design §5.3.1.
const maxRefreshInterval = 24 * time.Hour

// ErrSessionRevoked is returned when keycloak ended the session of a cookie. it wraps
// api.ErrUnauthenticated, so the api middleware answers 401.
var ErrSessionRevoked = fmt.Errorf("session revoked: %w", api.ErrUnauthenticated)

// revalidator is the part of Keycloak a session check needs.
type revalidator interface {
	Refresh(ctx context.Context, refreshToken string) (Tokens, Verdict, error)
	Introspect(ctx context.Context, refreshToken string) (Verdict, error)
}

// SessionAuthenticator is the api.Authenticator of lampa-api: it opens the session cookie and
// revalidates it against keycloak on every request (design §5.3.1). it never touches the database.
type SessionAuthenticator struct {
	cookies  *Cookies
	keycloak revalidator
	logger   api.Logger
	now      func() time.Time
}

// NewSessionAuthenticator returns a SessionAuthenticator reading cookies and revalidating with
// keycloak.
func NewSessionAuthenticator(cookies *Cookies, keycloak *Keycloak, logger api.Logger) *SessionAuthenticator {
	return &SessionAuthenticator{cookies: cookies, keycloak: keycloak, logger: logger, now: time.Now}
}

// Authenticate returns the user id of the session cookie of r. a missing, tampered or expired
// cookie fails with ErrNoSession; a session keycloak ended is cleared and fails with
// ErrSessionRevoked. a keycloak failure keeps the session: auth fails open.
func (a *SessionAuthenticator) Authenticate(w http.ResponseWriter, r *http.Request) (string, error) {
	s, err := a.session(w, r)
	if err != nil {
		return "", err
	}
	return s.UserID, nil
}

// session opens and revalidates the session cookie of r: a refresh grant when the refresh token
// is due (re-issuing the cookie with created_at and the idle expiry kept), introspection
// otherwise. it returns the session as it now stands in the cookie.
func (a *SessionAuthenticator) session(w http.ResponseWriter, r *http.Request) (Session, error) {
	now := a.now()
	s, err := a.cookies.OpenSession(r, now)
	if err != nil {
		return Session{}, err
	}

	if !refreshDue(s, now) {
		verdict, ierr := a.keycloak.Introspect(r.Context(), s.RefreshToken)
		switch {
		case ierr != nil:
			a.logger.Printf("[WARN] session introspection failed, session kept: %v", ierr)
		case verdict == VerdictRevoked:
			a.cookies.ClearSession(w)
			return Session{}, ErrSessionRevoked
		}
		return s, nil
	}

	tokens, verdict, err := a.keycloak.Refresh(r.Context(), s.RefreshToken)
	switch {
	case err != nil:
		a.logger.Printf("[WARN] session refresh failed, session kept: %v", err)
		return s, nil
	case verdict == VerdictRevoked:
		a.cookies.ClearSession(w)
		return Session{}, ErrSessionRevoked
	}
	refreshed, err := a.cookies.ReplaceTokens(w, s, tokens, now)
	if err != nil {
		// nothing was written: the old cookie and its refresh token stay in use
		a.logger.Printf("[WARN] refreshed session not written, session kept: %v", err)
		return s, nil
	}
	return refreshed, nil
}

// refreshDue reports whether the refresh token of s should be refreshed rather than
// introspected: after half its lifetime, and at least once a day.
func refreshDue(s Session, now time.Time) bool {
	interval := min(maxRefreshInterval, s.RefreshExpiresAt.Sub(s.RefreshedAt)/2)
	return !now.Before(s.RefreshedAt.Add(interval))
}

// compile-time check that SessionAuthenticator is an api.Authenticator.
var _ api.Authenticator = (*SessionAuthenticator)(nil)
