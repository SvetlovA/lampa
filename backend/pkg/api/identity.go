package api

import (
	"context"
	"errors"
	"net/http"
)

// ErrUnauthenticated is returned by an Authenticator when the request carries no valid identity.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticator resolves the user of a request. it is the only source of the user id:
// no request header, query or body can set it directly.
type Authenticator interface {
	Authenticate(r *http.Request) (userID string, err error)
}

// DenyAll is the Authenticator used until a real identity provider is wired in: it rejects every request.
type DenyAll struct{}

// Authenticate always returns ErrUnauthenticated.
func (DenyAll) Authenticate(*http.Request) (string, error) {
	return "", ErrUnauthenticated
}

type userIDKey struct{}

// UserID returns the authenticated user id stored in ctx by the api middleware.
func UserID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey{}).(string)
	return id, ok && id != ""
}

// authenticate runs auth and passes the request to next with the user id in its context.
// any failure answers 401; failures other than ErrUnauthenticated are logged, since they
// point to a broken identity provider rather than a bad client.
func (s *Server) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.auth.Authenticate(r)
		if err != nil || id == "" {
			if err != nil && !errors.Is(err, ErrUnauthenticated) {
				s.logger.Printf("[WARN] authenticate %q %q: %v", r.Method, r.URL.Path, err)
			}
			writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, id)))
	}
}
