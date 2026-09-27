package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/api/mocks"
	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

const testUserID = "0b5c8a6e-3f7d-4a51-9c1e-2d4f6a8b0c1d"

// authFunc is a fake Authenticator.
type authFunc func(w http.ResponseWriter, r *http.Request) (string, error)

func (f authFunc) Authenticate(w http.ResponseWriter, r *http.Request) (string, error) {
	return f(w, r)
}

func allowAll(id string) Authenticator {
	return authFunc(func(http.ResponseWriter, *http.Request) (string, error) { return id, nil })
}

// testOrigin is the public origin of test servers.
const testOrigin = "http://100.64.0.1:8092"

// notFoundRoutes stands in for the auth handler where the auth routes are not under test.
func notFoundRoutes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, "not_found", "not found")
	})
}

// newTestServer creates a Server with a 1 KiB body limit, logging into the returned buffer.
func newTestServer(t *testing.T, svc UserData, auth Authenticator) (*Server, *bytes.Buffer) {
	t.Helper()
	return newTestServerWithRoutes(t, svc, auth, notFoundRoutes())
}

// newTestServerWithRoutes is newTestServer with the given auth routes.
func newTestServerWithRoutes(t *testing.T, svc UserData, auth Authenticator, authRoutes http.Handler) (*Server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	cfg := ServerConfig{Addr: "127.0.0.1:0", MaxBodyBytes: 1 << 10, PublicOrigin: testOrigin}
	srv, err := NewServer(cfg, svc, auth, authRoutes, log.New(&buf, "", 0))
	require.NoError(t, err)
	return srv, &buf
}

// newRequest is httptest.NewRequest with the csrf header the add-on sends on every request.
func newRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set(CSRFHeader, "1")
	return req
}

// decodeError reads the json error contract of resp.
func decodeError(t *testing.T, resp *http.Response) errorBody {
	t.Helper()
	assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))
	var body errorBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

func TestDenyAll_Authenticate(t *testing.T) {
	id, err := DenyAll{}.Authenticate(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	require.ErrorIs(t, err, ErrUnauthenticated)
	assert.Empty(t, id)
}

func TestUserID(t *testing.T) {
	_, ok := UserID(context.Background())
	assert.False(t, ok)

	_, ok = UserID(context.WithValue(context.Background(), userIDKey{}, ""))
	assert.False(t, ok, "empty id is not an identity")

	id, ok := UserID(context.WithValue(context.Background(), userIDKey{}, testUserID))
	assert.True(t, ok)
	assert.Equal(t, testUserID, id)
}

func TestServer_authenticate(t *testing.T) {
	tests := []struct {
		name    string
		auth    Authenticator
		status  int
		logged  string
		cookie  string // expected Set-Cookie prefix, empty for none
		reached bool
	}{
		{name: "authenticated", auth: allowAll(testUserID), status: http.StatusNoContent, reached: true},
		{name: "unauthenticated", auth: DenyAll{}, status: http.StatusUnauthorized},
		{name: "wrapped unauthenticated", status: http.StatusUnauthorized,
			auth: authFunc(func(http.ResponseWriter, *http.Request) (string, error) {
				return "", errors.Join(errors.New("no token"), ErrUnauthenticated)
			})},
		{name: "empty id", auth: allowAll(""), status: http.StatusUnauthorized},
		{name: "provider failure", status: http.StatusServiceUnavailable, logged: `[WARN] authenticate "GET" "/x": jwks unreachable`,
			auth: authFunc(func(http.ResponseWriter, *http.Request) (string, error) { return "", errors.New("jwks unreachable") })},
		{name: "provider failure with id", status: http.StatusServiceUnavailable, logged: `[WARN] authenticate "GET" "/x": partial`,
			auth: authFunc(func(http.ResponseWriter, *http.Request) (string, error) { return testUserID, errors.New("partial") })},
		{name: "cookie written by authenticator", status: http.StatusNoContent, reached: true, cookie: "lampa_session=new",
			auth: authFunc(func(w http.ResponseWriter, _ *http.Request) (string, error) {
				http.SetCookie(w, &http.Cookie{Name: "lampa_session", Value: "new", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
				return testUserID, nil
			})},
		{name: "cookie cleared on unauthenticated", status: http.StatusUnauthorized, cookie: "lampa_session=;",
			auth: authFunc(func(w http.ResponseWriter, _ *http.Request) (string, error) {
				http.SetCookie(w, &http.Cookie{Name: "lampa_session", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
				return "", ErrUnauthenticated
			})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, logs := newTestServer(t, &mocks.UserDataMock{}, tc.auth)
			reached := false
			h := srv.authenticate(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				id, ok := UserID(r.Context())
				assert.True(t, ok)
				assert.Equal(t, testUserID, id)
				w.WriteHeader(http.StatusNoContent)
			})

			w := httptest.NewRecorder()
			h(w, httptest.NewRequest(http.MethodGet, "/x", http.NoBody))
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, tc.reached, reached)
			switch tc.status {
			case http.StatusUnauthorized:
				assert.Equal(t, "unauthenticated", decodeError(t, resp).Error.Code)
			case http.StatusServiceUnavailable:
				assert.Equal(t, "session_unavailable", decodeError(t, resp).Error.Code)
			}
			if tc.cookie == "" {
				assert.Empty(t, resp.Header.Get("Set-Cookie"))
			} else {
				assert.True(t, strings.HasPrefix(resp.Header.Get("Set-Cookie"), tc.cookie), resp.Header.Get("Set-Cookie"))
			}
			if tc.logged == "" {
				assert.Empty(t, logs.String())
			} else {
				assert.Contains(t, logs.String(), tc.logged)
			}
		})
	}
}

func TestServer_DenyAllRoutes(t *testing.T) {
	svc := &mocks.UserDataMock{}
	srv, _ := newTestServer(t, svc, DenyAll{})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := newRequest(method, userDataPath, strings.NewReader(`{"schema_version":1,"data":{}}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			assert.Equal(t, "unauthenticated", decodeError(t, resp).Error.Code)
		})
	}
	assert.Empty(t, svc.GetCalls())
	assert.Empty(t, svc.ReplaceCalls())
	assert.Empty(t, svc.DeleteCalls())
}

func TestServer_UserIDFromRequestIgnored(t *testing.T) {
	const otherID = "11111111-2222-4333-8444-555555555555"
	svc := &mocks.UserDataMock{
		ReplaceFunc: func(_ context.Context, userID string, raw []byte) (storage.Document, error) {
			return storage.Document{SchemaVersion: 1}, nil
		},
	}
	srv, _ := newTestServer(t, svc, allowAll(testUserID))

	body := `{"schema_version":1,"user_id":"` + otherID + `","data":{}}`
	req := newRequest(http.MethodPut, userDataPath+"?user_id="+otherID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", otherID)
	req.AddCookie(&http.Cookie{Name: "user_id", Value: otherID, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, svc.ReplaceCalls(), 1)
	assert.Equal(t, testUserID, svc.ReplaceCalls()[0].UserID)
}
