package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
type authFunc func(r *http.Request) (string, error)

func (f authFunc) Authenticate(r *http.Request) (string, error) { return f(r) }

func allowAll(id string) Authenticator {
	return authFunc(func(*http.Request) (string, error) { return id, nil })
}

// newTestServer creates a Server with a 1 KiB body limit, logging into the returned buffer.
func newTestServer(t *testing.T, svc UserData, auth Authenticator) (*Server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	srv, err := NewServer(ServerConfig{Addr: "127.0.0.1:0", MaxBodyBytes: 1 << 10}, svc, auth, log.New(&buf, "", 0))
	require.NoError(t, err)
	return srv, &buf
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
	id, err := DenyAll{}.Authenticate(httptest.NewRequest(http.MethodGet, "/", http.NoBody))
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
		reached bool
	}{
		{name: "authenticated", auth: allowAll(testUserID), status: http.StatusNoContent, reached: true},
		{name: "unauthenticated", auth: DenyAll{}, status: http.StatusUnauthorized},
		{name: "wrapped unauthenticated", status: http.StatusUnauthorized,
			auth: authFunc(func(*http.Request) (string, error) {
				return "", errors.Join(errors.New("no token"), ErrUnauthenticated)
			})},
		{name: "empty id", auth: allowAll(""), status: http.StatusUnauthorized},
		{name: "provider failure", status: http.StatusUnauthorized, logged: `[WARN] authenticate "GET" "/x": jwks unreachable`,
			auth: authFunc(func(*http.Request) (string, error) { return "", errors.New("jwks unreachable") })},
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
			if tc.status == http.StatusUnauthorized {
				assert.Equal(t, "unauthenticated", decodeError(t, resp).Error.Code)
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
			req := httptest.NewRequest(method, userDataPath, strings.NewReader(`{"schema_version":1,"data":{}}`))
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
	req := httptest.NewRequest(http.MethodPut, userDataPath+"?user_id="+otherID, strings.NewReader(body))
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
