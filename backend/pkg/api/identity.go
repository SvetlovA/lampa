package api

import (
	"context"
	"errors"
	"net/http"
)

// ErrUnauthenticated is returned by an Authenticator when the request carries no valid identity.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticator resolves the user of a request. it is the only source of the user id:
// no request header, query or body can set it directly. w lets it clear or re-issue the
// session cookie; it must not write the status or body.
type Authenticator interface {
	Authenticate(w http.ResponseWriter, r *http.Request) (userID string, err error)
}

// DenyAll is the Authenticator used until a real identity provider is wired in: it rejects every request.
type DenyAll struct{}

// Authenticate always returns ErrUnauthenticated.
func (DenyAll) Authenticate(http.ResponseWriter, *http.Request) (string, error) {
	return "", ErrUnauthenticated
}

type userIDKey struct{}

// UserID returns the authenticated user id stored in ctx by the api middleware.
func UserID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey{}).(string)
	return id, ok && id != ""
}

// authenticate runs auth and passes the request to next with the user id in its context.
// only ErrUnauthenticated (or an empty id) answers 401; any other failure is logged and answers
// 503, so a client is never told it is signed out because a dependency failed (design §5.3).
func (s *Server) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.auth.Authenticate(w, r)
		switch {
		case err != nil && !errors.Is(err, ErrUnauthenticated):
			s.logger.Printf("[WARN] authenticate %q %q: %v", r.Method, r.URL.Path, err)
			WriteError(w, http.StatusServiceUnavailable, "session_unavailable", "session cannot be checked")
			return
		case err != nil || id == "":
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, id)))
	}
}
