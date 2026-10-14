package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/api/mocks"
	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

const (
	testTorrentServer = "Server_12-3"
	testTorrentHash   = "0123456789abcdef0123456789abcdef01234567"
	testHashesURL     = torrserversPrefix + testTorrentServer + "/hashes"
	testHashURL       = testHashesURL + "/" + testTorrentHash
)

func TestServer_TorrentIdentityPrecondition(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			var users []string
			record := func(_ context.Context, user, _, _ string) error {
				users = append(users, user)
				return nil
			}
			svc := &mocks.TorrentsMock{
				ListFunc: func(_ context.Context, user, _ string) ([]string, error) {
					users = append(users, user)
					return []string{}, nil
				},
				ClaimFunc: record, ReleaseFunc: record,
			}
			auth := authFunc(func(_ http.ResponseWriter, r *http.Request) (string, error) {
				cookie, err := r.Cookie("session")
				if err != nil {
					return "", ErrUnauthenticated
				}
				return cookie.Value, nil
			})
			srv, logs := newTestServerWithTorrents(t, &mocks.UserDataMock{}, svc, auth, notFoundRoutes())
			path := testHashURL
			want := http.StatusNoContent
			if method == http.MethodGet {
				path, want = testHashesURL, http.StatusOK
			}
			send := func(cookie, expected string) *httptest.ResponseRecorder {
				t.Helper()
				req := newRequest(method, path, http.NoBody)
				if cookie != "" {
					req.AddCookie(&http.Cookie{Name: "session", Value: cookie,
						Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
				}
				req.Header.Set("X-Lampa-Expected-User", expected)
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, req)
				return w
			}
			// two tabs share a cookie, but keep independent resolved identities
			require.Equal(t, want, send(flowUserA, flowUserA).Code)
			w := send(flowUserB, flowUserA)
			require.Equal(t, http.StatusConflict, w.Code)
			assert.Contains(t, w.Body.String(), `"code":"account_changed"`)
			assert.Equal(t, []string{flowUserA}, users, "stale tab must not read or mutate Bob's rows")
			require.Equal(t, want, send(flowUserB, flowUserB).Code)
			require.Equal(t, want, send(flowUserB, "").Code, "existing API callers remain compatible")
			require.Equal(t, http.StatusUnauthorized, send("", flowUserA).Code, "header cannot authenticate")
			assert.Equal(t, []string{flowUserA, flowUserB, flowUserB}, users)
			assert.NotContains(t, logs.String(), flowUserA)
			assert.NotContains(t, logs.String(), flowUserB)
		})
	}
}

func TestServer_GetTorrentHashes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hashes []string
		body   string
	}{
		{name: "nil", body: `{"hashes":[]}`},
		{name: "empty", hashes: []string{}, body: `{"hashes":[]}`},
		{name: "v1 and v2", hashes: []string{testTorrentHash, strings.Repeat("f", 64)},
			body: `{"hashes":["` + testTorrentHash + `","` + strings.Repeat("f", 64) + `"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mocks.TorrentsMock{ListFunc: func(context.Context, string, string) ([]string, error) {
				return tc.hashes, nil
			}}
			srv, _ := newTestServerWithTorrents(t, &mocks.UserDataMock{}, svc, allowAll(testUserID), notFoundRoutes())
			req := newRequest(http.MethodGet, testHashesURL+"?user_id=attacker&server_id=other", strings.NewReader(`{"user_id":"attacker"}`))
			req.Header.Set("X-User-ID", "attacker")
			w := httptest.NewRecorder()
			start := time.Now()
			srv.Handler().ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			assert.JSONEq(t, tc.body, w.Body.String())
			assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
			require.Len(t, svc.ListCalls(), 1)
			call := svc.ListCalls()[0]
			assert.Equal(t, testUserID, call.UserID)
			assert.Equal(t, testTorrentServer, call.ServerID)
			deadline, ok := call.Ctx.Deadline()
			require.True(t, ok)
			assert.WithinDuration(t, start.Add(requestTimeout), deadline, time.Second)
			require.ErrorIs(t, call.Ctx.Err(), context.Canceled, "deadline context released after request")
			assert.Empty(t, svc.ClaimCalls())
			assert.Empty(t, svc.ReleaseCalls())
		})
	}
}

func TestServer_TorrentMutations(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			check := func(ctx context.Context, userID, serverID, hash string) error {
				assert.Equal(t, testUserID, userID)
				assert.Equal(t, testTorrentServer, serverID)
				assert.Equal(t, strings.ToUpper(testTorrentHash), hash, "service owns canonicalization")
				id, ok := UserID(ctx)
				assert.True(t, ok)
				assert.Equal(t, testUserID, id)
				_, ok = ctx.Deadline()
				assert.True(t, ok)
				return nil
			}
			svc := &mocks.TorrentsMock{ClaimFunc: check, ReleaseFunc: check}
			srv, _ := newTestServerWithTorrents(t, &mocks.UserDataMock{}, svc, allowAll(testUserID), notFoundRoutes())
			for range 2 {
				req := newRequest(method, testHashesURL+"/"+strings.ToUpper(testTorrentHash)+"?user_id=attacker&hash=other", strings.NewReader(`{"user_id":"attacker"}`))
				req.Header.Set("X-User-ID", "attacker")
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, req)
				require.Equal(t, http.StatusNoContent, w.Code)
				assert.Empty(t, w.Body.String())
			}
			assert.Empty(t, svc.ListCalls())
			if method == http.MethodPut {
				assert.Len(t, svc.ClaimCalls(), 2)
				assert.Empty(t, svc.ReleaseCalls())
			} else {
				assert.Len(t, svc.ReleaseCalls(), 2)
				assert.Empty(t, svc.ClaimCalls())
			}
		})
	}
}

func TestServer_TorrentServiceErrors(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		for _, tc := range []struct {
			name   string
			err    error
			status int
			code   string
		}{
			{name: "invalid server", err: fmt.Errorf("validate: %w", &storage.ValidationError{Code: storage.CodeInvalidServerID, Reason: "invalid server id"}),
				status: http.StatusBadRequest, code: storage.CodeInvalidServerID},
			{name: "invalid hash", err: &storage.ValidationError{Code: storage.CodeInvalidHash, Reason: "invalid hash"},
				status: http.StatusBadRequest, code: storage.CodeInvalidHash},
			{name: "unavailable", err: fmt.Errorf("store: %w: dial 10.0.0.5", storage.ErrUnavailable),
				status: http.StatusServiceUnavailable, code: "storage_unavailable"},
			{name: "unexpected", err: errors.New("decode: driver details"), status: http.StatusInternalServerError, code: "internal_error"},
			{name: "invalid user", err: storage.ErrInvalidUserID, status: http.StatusInternalServerError, code: "internal_error"},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				svc := &mocks.TorrentsMock{
					ListFunc:    func(context.Context, string, string) ([]string, error) { return []string{testTorrentHash}, tc.err },
					ClaimFunc:   func(context.Context, string, string, string) error { return tc.err },
					ReleaseFunc: func(context.Context, string, string, string) error { return tc.err },
				}
				srv, logs := newTestServerWithTorrents(t, &mocks.UserDataMock{}, svc, allowAll(testUserID), notFoundRoutes())
				path := testHashURL
				if method == http.MethodGet {
					path = testHashesURL
				}
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, newRequest(method, path, http.NoBody))
				resp := w.Result()
				defer resp.Body.Close()
				require.Equal(t, tc.status, resp.StatusCode)
				assert.Equal(t, tc.code, decodeError(t, resp).Error.Code)
				assert.NotContains(t, w.Body.String(), testTorrentHash, "failed lists discard partial hashes")
				assert.NotContains(t, w.Body.String(), "10.0.0.5")
				assert.NotContains(t, w.Body.String(), "driver details")
				if tc.status == http.StatusInternalServerError {
					assert.Contains(t, logs.String(), fmt.Sprintf(`[ERROR] %q "/api/v1/torrservers/…":`, method))
					assert.Contains(t, logs.String(), tc.err.Error())
				}
				assert.NotContains(t, logs.String(), testTorrentHash)
				assert.NotContains(t, logs.String(), testTorrentServer)
			})
		}
	}
}

func TestServer_TorrentMiddleware(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		for _, tc := range []struct {
			name   string
			auth   Authenticator
			csrf   string
			origin string
			status int
			code   string
		}{
			{name: "anonymous", auth: DenyAll{}, csrf: "1", status: http.StatusUnauthorized, code: "unauthenticated"},
			{name: "auth unavailable", auth: authFunc(func(http.ResponseWriter, *http.Request) (string, error) {
				return "", errors.New("provider unavailable")
			}), csrf: "1", status: http.StatusServiceUnavailable, code: "session_unavailable"},
			{name: "missing csrf", auth: allowAll(testUserID), status: http.StatusForbidden, code: "csrf_rejected"},
			{name: "wrong csrf", auth: allowAll(testUserID), csrf: "0", status: http.StatusForbidden, code: "csrf_rejected"},
			{name: "cross origin", auth: allowAll(testUserID), csrf: "1", origin: "https://other.example", status: http.StatusForbidden, code: "csrf_rejected"},
		} {
			if method == http.MethodGet && tc.status == http.StatusForbidden {
				continue
			}
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				svc := &mocks.TorrentsMock{}
				srv, logs := newTestServerWithTorrents(t, &mocks.UserDataMock{}, svc, tc.auth, notFoundRoutes())
				path := testHashURL
				if method == http.MethodGet {
					path = testHashesURL
				}
				req := httptest.NewRequest(method, path, http.NoBody)
				req.Header.Set(CSRFHeader, tc.csrf)
				if tc.origin != "" {
					req.Header.Set("Origin", tc.origin)
				}
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, req)
				resp := w.Result()
				defer resp.Body.Close()
				require.Equal(t, tc.status, resp.StatusCode)
				assert.Equal(t, tc.code, decodeError(t, resp).Error.Code)
				assert.Empty(t, svc.ListCalls())
				assert.Empty(t, svc.ClaimCalls())
				assert.Empty(t, svc.ReleaseCalls())
				assert.NotContains(t, logs.String(), testTorrentHash)
				assert.NotContains(t, logs.String(), testTorrentServer)
			})
		}
	}
}

func TestServer_TorrentFallbacks(t *testing.T) {
	for _, route := range []struct {
		path    string
		allow   string
		methods []string
	}{
		{path: testHashesURL, allow: "GET", methods: []string{http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPost, http.MethodPatch, http.MethodOptions}},
		{path: testHashURL, allow: "PUT, DELETE", methods: []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPatch, http.MethodOptions}},
	} {
		for _, method := range route.methods {
			t.Run(method+"/"+route.allow, func(t *testing.T) {
				svc := &mocks.TorrentsMock{}
				srv, _ := newTestServerWithTorrents(t, &mocks.UserDataMock{}, svc, DenyAll{}, notFoundRoutes())
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, newRequest(method, route.path, http.NoBody))
				resp := w.Result()
				defer resp.Body.Close()
				require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
				assert.Equal(t, route.allow, resp.Header.Get("Allow"))
				assert.Equal(t, "method_not_allowed", decodeError(t, resp).Error.Code)
				assert.Empty(t, svc.ListCalls())
				assert.Empty(t, svc.ClaimCalls())
				assert.Empty(t, svc.ReleaseCalls())
			})
		}
	}
}

func TestServer_TorrentAccessLog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		status int
	}{
		{name: "list", method: http.MethodGet, path: testHashesURL, status: http.StatusOK},
		{name: "claim", method: http.MethodPut, path: testHashURL, status: http.StatusNoContent},
		{name: "release", method: http.MethodDelete, path: testHashURL, status: http.StatusNoContent},
		{name: "invalid path", method: http.MethodGet, path: testHashURL + "/other", status: http.StatusNotFound},
		{name: "method fallback", method: http.MethodPatch, path: testHashURL, status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mocks.TorrentsMock{
				ListFunc:    func(context.Context, string, string) ([]string, error) { return nil, nil },
				ClaimFunc:   func(context.Context, string, string, string) error { return nil },
				ReleaseFunc: func(context.Context, string, string, string) error { return nil },
			}
			srv, logs := newTestServerWithTorrents(t, &mocks.UserDataMock{}, svc, allowAll(testUserID), notFoundRoutes())
			req := newRequest(tc.method, tc.path+"?secret=query-secret", strings.NewReader("body-secret"))
			req.Header.Set("Authorization", "Bearer header-secret")
			req.AddCookie(&http.Cookie{Name: "session", Value: "cookie-secret", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			assert.Regexp(t, fmt.Sprintf(`^\[INFO\] %q "/api/v1/torrservers/…" %d \S+\n$`, tc.method, tc.status), logs.String())
			for _, secret := range []string{testTorrentServer, testTorrentHash, "query-secret", "body-secret", "header-secret", "cookie-secret"} {
				assert.NotContains(t, logs.String(), secret)
			}
		})
	}
}

func TestServer_TorrentPanicLog(t *testing.T) {
	svc := &mocks.TorrentsMock{ClaimFunc: func(context.Context, string, string, string) error { panic("boom") }}
	srv, logs := newTestServerWithTorrents(t, &mocks.UserDataMock{}, svc, allowAll(testUserID), notFoundRoutes())
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, newRequest(http.MethodPut, testHashURL, http.NoBody))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, logs.String(), `[ERROR] panic serving "PUT" "/api/v1/torrservers/…": boom`)
	assert.NotContains(t, logs.String(), testTorrentHash)
	assert.NotContains(t, logs.String(), testTorrentServer)
}
