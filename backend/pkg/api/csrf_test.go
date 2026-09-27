package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/api/mocks"
)

func TestServer_csrf(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		header   []string // CSRFHeader values, nil for none
		origins  []string // Origin values, nil for none
		rejected bool
	}{
		{name: "get without header", method: http.MethodGet},
		{name: "get with foreign origin", method: http.MethodGet, origins: []string{"https://evil.example"}},
		{name: "head without header", method: http.MethodHead},
		{name: "options without header", method: http.MethodOptions},
		{name: "post with header and no origin", method: http.MethodPost, header: []string{"1"}},
		{name: "post with header and same origin", method: http.MethodPost, header: []string{"1"}, origins: []string{testOrigin}},
		{name: "put with header", method: http.MethodPut, header: []string{"1"}, origins: []string{testOrigin}},
		{name: "delete with header", method: http.MethodDelete, header: []string{"1"}},
		{name: "post without header", method: http.MethodPost, origins: []string{testOrigin}, rejected: true},
		{name: "put without header", method: http.MethodPut, rejected: true},
		{name: "delete without header", method: http.MethodDelete, rejected: true},
		{name: "patch without header", method: http.MethodPatch, rejected: true},
		{name: "wrong header value", method: http.MethodPost, header: []string{"true"}, rejected: true},
		{name: "empty header value", method: http.MethodPost, header: []string{""}, rejected: true},
		{name: "foreign origin", method: http.MethodPost, header: []string{"1"}, origins: []string{"https://evil.example"}, rejected: true},
		{name: "same host other scheme", method: http.MethodPost, header: []string{"1"}, origins: []string{"https://100.64.0.1:8092"},
			rejected: true},
		{name: "same host other port", method: http.MethodPost, header: []string{"1"}, origins: []string{"http://100.64.0.1:8093"},
			rejected: true},
		{name: "origin with trailing slash", method: http.MethodPost, header: []string{"1"}, origins: []string{testOrigin + "/"},
			rejected: true},
		{name: "null origin", method: http.MethodPost, header: []string{"1"}, origins: []string{"null"}, rejected: true},
		{name: "empty origin", method: http.MethodPost, header: []string{"1"}, origins: []string{""}, rejected: true},
		{name: "two origins", method: http.MethodPost, header: []string{"1"}, origins: []string{testOrigin, testOrigin}, rejected: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(t, &mocks.UserDataMock{}, DenyAll{})
			reached := false
			h := srv.csrf(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(tc.method, "/x", http.NoBody)
			for _, v := range tc.header {
				req.Header.Add(CSRFHeader, v)
			}
			for _, v := range tc.origins {
				req.Header.Add("Origin", v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, !tc.rejected, reached)
			if tc.rejected {
				assert.Equal(t, http.StatusForbidden, resp.StatusCode)
				assert.Equal(t, "csrf_rejected", decodeError(t, resp).Error.Code)
			} else {
				assert.Equal(t, http.StatusNoContent, resp.StatusCode)
			}
			for name := range resp.Header {
				assert.False(t, strings.HasPrefix(name, "Access-Control-"), "cors header %s", name)
			}
		})
	}
}

func TestServer_csrfBeforeAuthentication(t *testing.T) {
	svc := &mocks.UserDataMock{}
	authCalled := false
	auth := authFunc(func(http.ResponseWriter, *http.Request) (string, error) {
		authCalled = true
		return testUserID, nil
	})
	srv, logs := newTestServer(t, svc, auth)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, userDataPath, strings.NewReader(`{"schema_version":1,"data":{}}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "https://evil.example")
			req.Header.Set(CSRFHeader, "1")
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusForbidden, resp.StatusCode)
			assert.Equal(t, "csrf_rejected", decodeError(t, resp).Error.Code)
		})
	}
	assert.False(t, authCalled)
	assert.Empty(t, svc.ReplaceCalls())
	assert.Empty(t, svc.DeleteCalls())
	assert.Contains(t, logs.String(), `[INFO] "PUT" "/api/v1/user-data" 403 `)
}

func TestServer_authRoutes(t *testing.T) {
	type seen struct {
		path        string
		hasDeadline bool
		bodyErr     error
	}
	var got []seen
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasDeadline := r.Context().Deadline()
		_, err := io.ReadAll(r.Body)
		got = append(got, seen{path: r.URL.Path, hasDeadline: hasDeadline, bodyErr: err})
		w.WriteHeader(http.StatusTeapot)
	})
	srv, logs := newTestServerWithRoutes(t, &mocks.UserDataMock{}, DenyAll{}, routes)

	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		csrf    bool
		status  int
		reached bool
	}{
		{name: "session", method: http.MethodGet, path: "/api/v1/session", status: http.StatusTeapot, reached: true},
		{name: "login", method: http.MethodGet, path: "/api/v1/auth/login?return=/x", status: http.StatusTeapot, reached: true},
		{name: "callback", method: http.MethodGet, path: "/api/v1/auth/callback", status: http.StatusTeapot, reached: true},
		{name: "unknown auth path", method: http.MethodGet, path: "/api/v1/auth/other", status: http.StatusTeapot, reached: true},
		{name: "logout", method: http.MethodPost, path: "/api/v1/auth/logout", csrf: true, status: http.StatusTeapot, reached: true},
		{name: "device start", method: http.MethodPost, path: "/api/v1/auth/device/start", csrf: true, status: http.StatusTeapot,
			reached: true},
		{name: "logout without csrf header", method: http.MethodPost, path: "/api/v1/auth/logout", status: http.StatusForbidden},
		{name: "device poll without csrf header", method: http.MethodPost, path: "/api/v1/auth/device/poll", status: http.StatusForbidden},
		{name: "auth prefix without slash", method: http.MethodGet, path: "/api/v1/auth", status: http.StatusNotFound},
		{name: "session subpath", method: http.MethodGet, path: "/api/v1/session/x", status: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got = nil
			req := httptest.NewRequest(tc.method, tc.path, http.NoBody)
			if tc.csrf {
				req.Header.Set(CSRFHeader, "1")
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)

			assert.Equal(t, tc.status, w.Code)
			if !tc.reached {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, strings.SplitN(tc.path, "?", 2)[0], got[0].path)
			assert.True(t, got[0].hasDeadline, "request deadline applies to auth routes")
		})
	}
	assert.Contains(t, logs.String(), `[INFO] "GET" "/api/v1/session" 418 `)

	t.Run("body limit", func(t *testing.T) {
		got = nil
		req := newRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(strings.Repeat("x", 2<<10)))
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
		require.Len(t, got, 1)
		var maxErr *http.MaxBytesError
		assert.ErrorAs(t, got[0].bodyErr, &maxErr)
	})

	t.Run("panic recovered", func(t *testing.T) {
		panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
		srv, logs := newTestServerWithRoutes(t, &mocks.UserDataMock{}, DenyAll{}, panicking)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/session", http.NoBody))
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, logs.String(), `[ERROR] panic serving "GET" "/api/v1/session": boom`)
	})
}
