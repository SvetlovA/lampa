package api

import (
	"context"
	"io"
	"log"
	"net"
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

func TestNewServer(t *testing.T) {
	cfg := ServerConfig{Addr: ":8080", MaxBodyBytes: 1}
	logger := log.New(io.Discard, "", 0)
	tests := []struct {
		name    string
		cfg     ServerConfig
		svc     UserData
		auth    Authenticator
		logger  Logger
		wantErr string
	}{
		{name: "valid", cfg: cfg, svc: &mocks.UserDataMock{}, auth: DenyAll{}, logger: logger},
		{name: "nil service", cfg: cfg, auth: DenyAll{}, logger: logger, wantErr: "nil user data service"},
		{name: "nil authenticator", cfg: cfg, svc: &mocks.UserDataMock{}, logger: logger, wantErr: "nil authenticator"},
		{name: "nil logger", cfg: cfg, svc: &mocks.UserDataMock{}, auth: DenyAll{}, wantErr: "nil logger"},
		{name: "zero body limit", cfg: ServerConfig{Addr: ":8080"}, svc: &mocks.UserDataMock{}, auth: DenyAll{}, logger: logger,
			wantErr: "max body bytes must be positive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, err := NewServer(tc.cfg, tc.svc, tc.auth, tc.logger)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				assert.Nil(t, srv)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, srv.Handler())
		})
	}
}

func TestServer_Fallbacks(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		status int
		code   string
		allow  string
	}{
		{name: "post user data", method: http.MethodPost, path: userDataPath, status: http.StatusMethodNotAllowed,
			code: "method_not_allowed", allow: "GET, PUT, DELETE"},
		{name: "patch user data", method: http.MethodPatch, path: userDataPath, status: http.StatusMethodNotAllowed,
			code: "method_not_allowed", allow: "GET, PUT, DELETE"},
		{name: "head user data", method: http.MethodHead, path: userDataPath, status: http.StatusMethodNotAllowed,
			code: "method_not_allowed", allow: "GET, PUT, DELETE"},
		{name: "root", method: http.MethodGet, path: "/", status: http.StatusNotFound, code: "not_found"},
		{name: "unknown path", method: http.MethodGet, path: "/api/v1/other", status: http.StatusNotFound, code: "not_found"},
		{name: "trailing slash", method: http.MethodGet, path: userDataPath + "/", status: http.StatusNotFound, code: "not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(t, &mocks.UserDataMock{}, allowAll(testUserID))
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, http.NoBody))
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, tc.allow, resp.Header.Get("Allow"))
			body := decodeError(t, resp)
			assert.Equal(t, tc.code, body.Error.Code)
			assert.NotEmpty(t, body.Error.Message)
		})
	}
}

func TestServer_Panic(t *testing.T) {
	svc := &mocks.UserDataMock{
		GetFunc: func(context.Context, string) (storage.Document, error) { panic("boom") },
	}
	srv, logs := newTestServer(t, svc, allowAll(testUserID))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, userDataPath, http.NoBody))
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Equal(t, "internal_error", decodeError(t, resp).Error.Code)
	assert.Contains(t, logs.String(), `[ERROR] panic serving "GET" "/api/v1/user-data": boom`)
	assert.Contains(t, logs.String(), `[INFO] "GET" "/api/v1/user-data" 500 `)
}

func TestServer_PanicAbortHandler(t *testing.T) {
	svc := &mocks.UserDataMock{
		GetFunc: func(context.Context, string) (storage.Document, error) { panic(http.ErrAbortHandler) },
	}
	srv, _ := newTestServer(t, svc, allowAll(testUserID))
	assert.PanicsWithError(t, http.ErrAbortHandler.Error(), func() {
		srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, userDataPath, http.NoBody))
	})
}

func TestServer_AccessLog(t *testing.T) {
	const secretBody, secretCookie, secretQuery = "body-secret-value", "cookie-secret-value", "query-secret-value"
	svc := &mocks.UserDataMock{
		ReplaceFunc: func(context.Context, string, []byte) (storage.Document, error) {
			return storage.Document{SchemaVersion: 1}, nil
		},
	}
	srv, logs := newTestServer(t, svc, allowAll(testUserID))
	req := httptest.NewRequest(http.MethodPut, userDataPath+"?token="+secretQuery,
		strings.NewReader(`{"schema_version":1,"data":{"other":{"k":"`+secretBody+`"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secretCookie)
	req.AddCookie(&http.Cookie{Name: "session", Value: secretCookie, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out := logs.String()
	assert.Regexp(t, `^\[INFO\] "PUT" "/api/v1/user-data" 200 \S+\n$`, out)
	for _, secret := range []string{secretBody, secretCookie, secretQuery} {
		assert.NotContains(t, out, secret)
	}
}

func TestServer_AccessLogImplicitStatus(t *testing.T) {
	srv, logs := newTestServer(t, &mocks.UserDataMock{}, DenyAll{})
	h := srv.accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", http.NoBody))
	assert.Contains(t, logs.String(), `[INFO] "GET" "/x" 200 `)

	logs.Reset()
	h = srv.accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
		w.WriteHeader(http.StatusTeapot) // superfluous, the recorded status stays 200
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/y", http.NoBody))
	assert.Contains(t, logs.String(), `[INFO] "GET" "/y" 200 `)
}

func TestWriteJSON_EncodeFailure(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]any{"bad": func() {}})
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Equal(t, "internal_error", decodeError(t, resp).Error.Code)
}

func TestServer_Serve(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	svc := &mocks.UserDataMock{
		GetFunc: func(context.Context, string) (storage.Document, error) {
			close(started)
			<-release
			return storage.Document{SchemaVersion: 1}, nil
		},
	}
	srv, _ := newTestServer(t, svc, allowAll(testUserID))
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, ln) }()

	type result struct {
		status int
		err    error
	}
	got := make(chan result, 1)
	go func() {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+userDataPath, http.NoBody)
		if reqErr != nil {
			got <- result{err: reqErr}
			return
		}
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			got <- result{err: reqErr}
			return
		}
		defer resp.Body.Close()
		got <- result{status: resp.StatusCode}
	}()

	<-started
	cancel()
	select {
	case err := <-served:
		t.Fatalf("serve returned with a request in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	res := <-got
	require.NoError(t, res.err)
	assert.Equal(t, http.StatusOK, res.status)
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after shutdown")
	}
}

func TestServer_ServeListenerFailure(t *testing.T) {
	srv, _ := newTestServer(t, &mocks.UserDataMock{}, DenyAll{})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, ln.Close())

	err = srv.Serve(t.Context(), ln)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "serve 127.0.0.1:")
	assert.NotErrorIs(t, err, http.ErrServerClosed)
}

func TestServer_Start(t *testing.T) {
	busy, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()

	t.Run("bind failure", func(t *testing.T) {
		srv, err := NewServer(ServerConfig{Addr: busy.Addr().String(), MaxBodyBytes: 1}, &mocks.UserDataMock{}, DenyAll{},
			log.New(io.Discard, "", 0))
		require.NoError(t, err)
		err = srv.Start(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "listen "+busy.Addr().String())
	})

	t.Run("canceled", func(t *testing.T) {
		free, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := free.Addr().String()
		require.NoError(t, free.Close())

		srv, err := NewServer(ServerConfig{Addr: addr, MaxBodyBytes: 1}, &mocks.UserDataMock{}, DenyAll{}, log.New(io.Discard, "", 0))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- srv.Start(ctx) }()
		require.Eventually(t, func() bool {
			conn, dialErr := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
			if dialErr != nil {
				return false
			}
			conn.Close()
			return true
		}, 5*time.Second, 10*time.Millisecond)
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("start did not return after cancel")
		}
	})
}
