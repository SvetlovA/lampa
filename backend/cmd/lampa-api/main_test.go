package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/api"
	apimocks "github.com/SvetlovA/lampa/backend/pkg/api/mocks"
	"github.com/SvetlovA/lampa/backend/pkg/auth"
	"github.com/SvetlovA/lampa/backend/pkg/config"
	"github.com/SvetlovA/lampa/backend/pkg/health"
	"github.com/SvetlovA/lampa/backend/pkg/storage"
	"github.com/SvetlovA/lampa/backend/pkg/storage/pgtest"
)

const testSecret = "s3cr3t-pw"

var testKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, config.DataKeySize))

func noEnv(string) (string, bool) { return "", false }

// withAuth returns m plus the Keycloak client secret testSettings refers to.
func withAuth(m map[string]string) map[string]string {
	out := map[string]string{
		"LAMPA_KEYCLOAK_CLIENT_SECRET": testSecret,
	}
	maps.Copy(out, m)
	return out
}

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// testDSN returns the connection string of the shared test database.
func testDSN(t *testing.T) string {
	t.Helper()
	return pgtest.DB(t).Config().ConnString()
}

// localListener opens a listener on a free loopback port.
func localListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	return ln
}

// fakeListen hands out prepared listeners by address, or the prepared error.
func fakeListen(lns map[string]net.Listener, errs map[string]error) listenFunc {
	return func(_ context.Context, addr string) (net.Listener, error) {
		if err, ok := errs[addr]; ok {
			return nil, err
		}
		if ln, ok := lns[addr]; ok {
			return ln, nil
		}
		return nil, errors.New("unexpected address")
	}
}

func testConfig(dsn string) config.Config {
	return config.Config{
		Listen: "api", HealthListen: "health", DBDSN: dsn, DataKey: [config.DataKeySize]byte{7}, MaxBodyBytes: 1 << 20,
		Auth: config.Auth{PublicURL: "http://100.64.0.1:8092", Issuer: "http://100.64.0.2:8080/realms/svtlv", ClientID: "svtlv-lampa", ClientSecret: "s"},
	}
}

// testSettings returns an appsettings.json with the given database and listeners; the password
// and data key come from the LAMPA_DB_PASSWORD and LAMPA_API_DATA_KEY placeholders; the
// Keycloak client secret comes from withAuth. An optional origin overrides the default.
func testSettings(t *testing.T, host string, port uint16, apiListen, healthListen string, origin ...string) fstest.MapFS {
	t.Helper()
	publicURL := "http://100.64.0.1:8092"
	if len(origin) != 0 {
		publicURL = origin[0]
	}
	data, err := json.Marshal(map[string]any{
		"Api":    map[string]any{"Listen": apiListen, "MaxBodyBytes": 1 << 20},
		"Health": map[string]any{"Listen": healthListen},
		"Database": map[string]any{"Host": host, "Port": port, "Name": "lampa", "User": "lampa",
			"Password": "{LAMPA_DB_PASSWORD}", "SSLMode": "disable"},
		"DataKey": "{LAMPA_API_DATA_KEY}",
		"Authentication": map[string]any{"PublicURL": publicURL, "Keycloak": map[string]any{
			"Authority": "https://svtlv.fly.dev/realms/svtlv-test", "ClientId": "svtlv-lampa",
			"ClientSecret": "{LAMPA_KEYCLOAK_CLIENT_SECRET}", "RequireHttpsMetadata": false}},
	})
	require.NoError(t, err)
	return fstest.MapFS{"appsettings.json": {Data: data}}
}

// waitStart runs start in a goroutine and returns its result channel.
func waitStart(ctx context.Context, cfg config.Config, out io.Writer, listen listenFunc) <-chan error {
	done := make(chan error, 1)
	go func() { done <- start(ctx, cfg, log.New(out, "", 0), listen) }()
	return done
}

func requireDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("start did not return")
		return nil
	}
}

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		wantOut string
	}{
		{name: "version flag", args: []string{"--version"}, wantOut: "lampa-api "},
		{name: "help flag", args: []string{"--help"}, wantOut: "-version"},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: "parse arguments"},
		{name: "positional argument", args: []string{"extra"}, wantErr: `unexpected argument "extra"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(t.Context(), tc.args, config.Defaults, noEnv, &out)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out.String(), tc.wantOut)
		})
	}
}

func TestRun_version(t *testing.T) {
	orig := revision
	t.Cleanup(func() { revision = orig })
	revision = "test-rev"

	var out bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"--version"}, config.Defaults, noEnv, &out))
	assert.Equal(t, "lampa-api test-rev\n", out.String())
}

func TestRun_invalidConfig(t *testing.T) {
	// the database points to a closed port: any database contact would fail differently and slower
	settings := testSettings(t, "127.0.0.1", 1, ":9000", ":9001")
	tests := []struct {
		name     string
		settings fs.FS
		env      map[string]string
		wantErr  error
	}{
		{name: "no env", settings: settings, env: map[string]string{}, wantErr: config.ErrMissing},
		{name: "missing key", settings: settings, env: withAuth(map[string]string{"LAMPA_DB_PASSWORD": testSecret}),
			wantErr: config.ErrMissing},
		{name: "bad key", settings: settings,
			env: withAuth(map[string]string{"LAMPA_DB_PASSWORD": testSecret, "LAMPA_API_DATA_KEY": "short"}), wantErr: config.ErrInvalid},
		{name: "same listen", settings: testSettings(t, "127.0.0.1", 1, ":9000", ":9000"),
			env: withAuth(map[string]string{"LAMPA_DB_PASSWORD": testSecret, "LAMPA_API_DATA_KEY": testKey}), wantErr: config.ErrInvalid},
		{name: "missing auth variable", settings: settings,
			env: map[string]string{"LAMPA_DB_PASSWORD": testSecret, "LAMPA_API_DATA_KEY": testKey}, wantErr: config.ErrMissing},
		{name: "bad public url", settings: testSettings(t, "127.0.0.1", 1, ":9000", ":9001", "http://100.64.0.1:8092/path"),
			env: withAuth(map[string]string{"LAMPA_DB_PASSWORD": testSecret, "LAMPA_API_DATA_KEY": testKey}), wantErr: config.ErrInvalid},
		{name: "unknown environment", settings: settings, env: map[string]string{"LAMPA_ENVIRONMENT": "Staging"},
			wantErr: config.ErrInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			startedAt := time.Now()
			err := run(t.Context(), nil, tc.settings, envOf(tc.env), &out)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Contains(t, err.Error(), "load config")
			assert.Less(t, time.Since(startedAt), time.Second)
			assert.NotContains(t, err.Error()+out.String(), testSecret)
		})
	}
}

func TestRun_embeddedDefaultsNeedSecrets(t *testing.T) {
	var out bytes.Buffer
	err := run(t.Context(), nil, config.Defaults, noEnv, &out)
	require.ErrorIs(t, err, config.ErrMissing)
	// the password is resolved before the data key, so it is the one reported
	assert.EqualError(t, err, "load config: Database.Password: LAMPA_DB_PASSWORD: required value is not set")
}

func TestRun_logsEnvironment(t *testing.T) {
	// the database points to a closed port, so run stops at the migration right after logging the config
	settings := testSettings(t, "127.0.0.1", 1, ":9000", ":9001")
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "default", want: "Test"},
		{name: "explicit", env: "Production", want: "Production"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := withAuth(map[string]string{"LAMPA_DB_PASSWORD": testSecret, "LAMPA_API_DATA_KEY": testKey})
			if tc.env != "" {
				env[config.EnvEnvironment] = tc.env
			}
			// the built dsn has no connect_timeout, so a filtered port would otherwise hang the test
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var out bytes.Buffer
			err := run(ctx, nil, settings, envOf(env), &out)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "migrate database")
			assert.Contains(t, out.String(), "[INFO] config: {Environment:"+tc.want+" ")
			assert.NotContains(t, err.Error()+out.String(), testSecret)
			assert.NotContains(t, out.String(), testKey, "log must not contain the data key")
		})
	}
}

func TestStart_invalidDSN(t *testing.T) {
	var out bytes.Buffer
	err := start(t.Context(), testConfig("postgres://lampa:"+testSecret+"@host:notaport/lampa"), log.New(&out, "", 0),
		fakeListen(nil, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse database dsn")
	assert.NotContains(t, err.Error()+out.String(), testSecret)
}

func TestStart_databaseUnreachable(t *testing.T) {
	var out bytes.Buffer
	cfg := testConfig("postgres://lampa:" + testSecret + "@127.0.0.1:1/lampa?connect_timeout=5")
	err := start(t.Context(), cfg, log.New(&out, "", 0), fakeListen(nil, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migrate database")
	assert.NotContains(t, err.Error()+out.String(), testSecret)
	assert.NotContains(t, out.String(), "listening", "no listener may open before migrations succeed")
}

func TestStart_servesUntilCanceled(t *testing.T) {
	dsn := testDSN(t)
	cfg := testConfig(dsn)
	cfg.Auth.Issuer = closedURL(t) + testRealm // keeps /health fast
	apiLn, healthLn := localListener(t), localListener(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var out bytes.Buffer
	done := waitStart(ctx, cfg, &out,
		fakeListen(map[string]net.Listener{"api": apiLn, "health": healthLn}, nil))

	healthURL := "http://" + healthLn.Addr().String()
	code, body := get(t, healthURL+"/health/critical")
	assert.Equal(t, http.StatusOK, code)
	var rep struct {
		Status string `json:"status"`
		Checks []struct {
			Name string `json:"name"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(body, &rep))
	assert.Equal(t, "Healthy", rep.Status)
	require.Len(t, rep.Checks, 1)
	assert.Equal(t, "database", rep.Checks[0].Name)

	code, _ = get(t, healthURL+"/health")
	assert.Equal(t, http.StatusOK, code)

	code, body = get(t, "http://"+apiLn.Addr().String()+"/api/v1/user-data")
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Contains(t, string(body), `"unauthenticated"`)

	code, _ = get(t, "http://"+apiLn.Addr().String()+"/api/v1/session")
	assert.Equal(t, http.StatusOK, code, "auth routes are wired")

	cancel()
	require.NoError(t, requireDone(t, done))

	requireClosed(t, apiLn)
	requireClosed(t, healthLn)
	assert.Contains(t, out.String(), "[INFO] servers stopped")
	assert.NotContains(t, out.String(), dsn)
}

func TestStart_bindFailureStopsBoth(t *testing.T) {
	dsn := testDSN(t)
	apiLn := localListener(t)
	errBind := errors.New("address already in use")

	var out bytes.Buffer
	done := waitStart(t.Context(), testConfig(dsn), &out,
		fakeListen(map[string]net.Listener{"api": apiLn}, map[string]error{"health": errBind}))

	err := requireDone(t, done)
	require.ErrorIs(t, err, errBind)
	assert.Contains(t, err.Error(), "health server: listen health")

	requireClosed(t, apiLn)
}

func TestRun_bindFailure(t *testing.T) {
	dsn := testDSN(t)
	busy := localListener(t)

	db := pgtest.DB(t).Config().ConnConfig
	settings := testSettings(t, db.Host, db.Port, "127.0.0.1:0", busy.Addr().String())
	env := envOf(withAuth(map[string]string{"LAMPA_DB_PASSWORD": db.Password, "LAMPA_API_DATA_KEY": testKey}))
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- run(t.Context(), nil, settings, env, &out) }()

	err := requireDone(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health server: listen "+busy.Addr().String())
	assert.Contains(t, out.String(), "[INFO] config: ")
	assert.NotContains(t, out.String(), dsn)
	assert.NotContains(t, out.String(), testKey, "log must not contain the data key")
}

const (
	testRealm   = "/realms/svtlv"
	testUserID  = "6f1c2a4e-3b5d-4c7e-9f80-1a2b3c4d5e6f"
	testRefresh = "refresh-token-value"
)

// fakeKeycloak serves the discovery document and the introspection endpoint, answering whether
// the refresh token is active. it counts introspection calls.
type fakeKeycloak struct {
	srv         *httptest.Server
	active      atomic.Bool
	introspects atomic.Int32
}

func newFakeKeycloak(t *testing.T) *fakeKeycloak {
	t.Helper()
	f := &fakeKeycloak{}
	f.active.Store(true)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oidcPath := f.srv.URL + testRealm + "/protocol/openid-connect"
		switch r.URL.Path {
		case testRealm + "/.well-known/openid-configuration":
			writeTestJSON(t, w, map[string]any{
				"issuer":                                f.issuer(),
				"authorization_endpoint":                oidcPath + "/auth",
				"token_endpoint":                        oidcPath + "/token",
				"introspection_endpoint":                oidcPath + "/token/introspect",
				"jwks_uri":                              oidcPath + "/certs",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case testRealm + "/protocol/openid-connect/token/introspect":
			f.introspects.Add(1)
			if user, pass, ok := r.BasicAuth(); !ok || user != "svtlv-lampa" || pass != "s" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			assert.Equal(t, testRefresh, r.PostFormValue("token"))
			writeTestJSON(t, w, map[string]any{"active": f.active.Load()})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKeycloak) issuer() string { return f.srv.URL + testRealm }

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	assert.NoError(t, json.NewEncoder(w).Encode(v))
}

// closedURL returns an http url nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	ln := localListener(t)
	u := "http://" + ln.Addr().String()
	ln.Close()
	return u
}

// sessionCookie returns a session cookie for testUserID sealed with the data key of cfg, the way
// a login would write it. its refresh is not due, so every request introspects it.
func sessionCookie(t *testing.T, cfg config.Config) *http.Cookie {
	t.Helper()
	sealer, err := auth.NewCookieSealer(cfg.DataKey)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	now := time.Now()
	_, err = auth.NewCookies(sealer, false).IssueSession(w, auth.Profile{UserID: testUserID, Name: "Test User"},
		auth.Tokens{RefreshToken: testRefresh, RefreshExpiresAt: now.Add(30 * time.Minute)}, now)
	require.NoError(t, err)
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	return cookies[0]
}

// reply is the part of a response the wiring tests read.
type reply struct {
	code    int
	cookies []*http.Cookie
	body    string
}

// send makes one request with the csrf header and an optional cookie and returns its reply.
func send(t *testing.T, method, url string, cookie *http.Cookie, body string) reply {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set(api.CSRFHeader, "1")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return reply{code: resp.StatusCode, cookies: resp.Cookies(), body: string(raw)}
}

// healthStatus returns the overall status of /health and the status of each check by name.
func healthStatus(t *testing.T, healthURL string) (string, map[string]string) {
	t.Helper()
	code, body := get(t, healthURL+"/health")
	require.Equal(t, http.StatusOK, code)
	var rep struct {
		Status string `json:"status"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(body, &rep))
	checks := make(map[string]string, len(rep.Checks))
	for _, c := range rep.Checks {
		checks[c.Name] = c.Status
	}
	return rep.Status, checks
}

// clearsSession reports whether rep expires the session cookie.
func clearsSession(rep reply) bool {
	for _, c := range rep.cookies {
		if c.Name == auth.SessionCookie && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestStart_keycloakDown(t *testing.T) {
	dsn := testDSN(t)
	cfg := testConfig(dsn)
	cfg.Auth.Issuer = closedURL(t) + testRealm
	apiLn, healthLn := localListener(t), localListener(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var out bytes.Buffer
	done := waitStart(ctx, cfg, &out, fakeListen(map[string]net.Listener{"api": apiLn, "health": healthLn}, nil))
	apiURL, healthURL := "http://"+apiLn.Addr().String(), "http://"+healthLn.Addr().String()

	status, checks := healthStatus(t, healthURL)
	assert.Equal(t, "Degraded", status)
	assert.Equal(t, map[string]string{"database": "Healthy", "keycloak": "Degraded"}, checks)
	code, _ := get(t, healthURL+"/health/critical")
	assert.Equal(t, http.StatusOK, code, "keycloak never makes the service unhealthy")

	r := send(t, http.MethodGet, apiURL+auth.SessionRoute, nil, "")
	assert.Equal(t, http.StatusOK, r.code)
	assert.JSONEq(t, `{"authenticated":false}`, r.body)

	r = send(t, http.MethodGet, apiURL+"/api/v1/user-data", nil, "")
	assert.Equal(t, http.StatusUnauthorized, r.code)

	r = send(t, http.MethodGet, apiURL+"/api/v1/user-data", sessionCookie(t, cfg), "")
	assert.Equal(t, http.StatusNotFound, r.code, "an existing session works while keycloak is down")
	assert.Contains(t, r.body, "user_data_not_found")

	r = send(t, http.MethodPost, apiURL+auth.DeviceStartRoute, nil, "")
	assert.Equal(t, http.StatusServiceUnavailable, r.code, "only new logins break")
	assert.Contains(t, r.body, "keycloak_unavailable")

	cancel()
	require.NoError(t, requireDone(t, done))
	assert.Contains(t, out.String(), "[WARN] session introspection failed, session kept")
	assert.NotContains(t, out.String(), testRefresh)
}

func TestNewAPI_sessionRevalidation(t *testing.T) {
	kc := newFakeKeycloak(t)
	cfg := testConfig("")
	cfg.Auth.Issuer = kc.issuer()
	var stored storage.Document
	svc := &apimocks.UserDataMock{
		GetFunc: func(_ context.Context, userID string) (storage.Document, error) {
			assert.Equal(t, testUserID, userID)
			if stored.Data == nil {
				return storage.Document{}, storage.ErrNotFound
			}
			return stored, nil
		},
		ReplaceFunc: func(_ context.Context, userID string, raw []byte) (storage.Document, error) {
			assert.Equal(t, testUserID, userID)
			assert.Contains(t, string(raw), `"language":"ru"`)
			stored = storage.Document{SchemaVersion: 1, Data: map[string]json.RawMessage{"settings": json.RawMessage(`{"language":"ru"}`)}}
			return stored, nil
		},
	}
	var out bytes.Buffer
	srv, err := newAPI(cfg, svc, log.New(&out, "", 0))
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	userData := ts.URL + "/api/v1/user-data"
	check := health.KeycloakCheck(kc.issuer())
	require.NoError(t, check.Run(t.Context()))

	r := send(t, http.MethodGet, userData, nil, "")
	assert.Equal(t, http.StatusUnauthorized, r.code)
	assert.Contains(t, r.body, `"unauthenticated"`)
	assert.Zero(t, kc.introspects.Load(), "no cookie, no keycloak call")

	cookie := sessionCookie(t, cfg)
	r = send(t, http.MethodPut, userData, cookie, `{"schema_version":1,"data":{"settings":{"language":"ru"}}}`)
	require.Equal(t, http.StatusOK, r.code, r.body)
	r = send(t, http.MethodGet, userData, cookie, "")
	assert.Equal(t, http.StatusOK, r.code)
	assert.Contains(t, r.body, `"language":"ru"`)
	assert.Equal(t, int32(2), kc.introspects.Load(), "every authenticated request revalidates")

	r = send(t, http.MethodGet, ts.URL+auth.SessionRoute, cookie, "")
	assert.Equal(t, http.StatusOK, r.code)
	assert.Contains(t, r.body, `"authenticated":true`)
	assert.Contains(t, r.body, testUserID)

	// keycloak ends the session: the next request is signed out and the cookie cleared
	kc.active.Store(false)
	r = send(t, http.MethodGet, userData, cookie, "")
	assert.Equal(t, http.StatusUnauthorized, r.code)
	assert.True(t, clearsSession(r))

	// keycloak stops: a live session keeps working and the health check fails
	kc.active.Store(true)
	kc.srv.Close()
	r = send(t, http.MethodGet, userData, cookie, "")
	assert.Equal(t, http.StatusOK, r.code, "revalidation fails open")
	assert.False(t, clearsSession(r))
	require.Error(t, check.Run(t.Context()))

	r = send(t, http.MethodPost, ts.URL+auth.LogoutRoute, cookie, "")
	assert.Equal(t, http.StatusNoContent, r.code)
	assert.True(t, clearsSession(r))

	assert.Contains(t, out.String(), "[WARN] session introspection failed, session kept")
	assert.NotContains(t, out.String(), testRefresh)
}

func TestNewAPI_databaseDown(t *testing.T) {
	kc := newFakeKeycloak(t)
	cfg := testConfig("")
	cfg.Auth.Issuer = kc.issuer()
	svc := &apimocks.UserDataMock{
		GetFunc: func(context.Context, string) (storage.Document, error) {
			return storage.Document{}, storage.ErrUnavailable
		},
	}
	var out bytes.Buffer
	srv, err := newAPI(cfg, svc, log.New(&out, "", 0))
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cookie := sessionCookie(t, cfg)

	r := send(t, http.MethodGet, ts.URL+auth.SessionRoute, cookie, "")
	assert.Equal(t, http.StatusOK, r.code)
	assert.Contains(t, r.body, `"authenticated":true`)

	r = send(t, http.MethodGet, ts.URL+"/api/v1/user-data", cookie, "")
	assert.Equal(t, http.StatusServiceUnavailable, r.code, "only user data depends on the database")
	assert.Contains(t, r.body, "storage_unavailable")

	r = send(t, http.MethodPost, ts.URL+auth.LogoutRoute, cookie, "")
	assert.Equal(t, http.StatusNoContent, r.code)
	assert.True(t, clearsSession(r))
}

func TestNewAPI_loginWiring(t *testing.T) {
	for _, publicURL := range []string{"http://100.64.0.1:8092", "https://lampa.example"} {
		t.Run(publicURL, func(t *testing.T) {
			kc := newFakeKeycloak(t)
			cfg := testConfig("")
			cfg.Auth.Issuer = kc.issuer()
			cfg.Auth.PublicURL = publicURL
			cfg.Auth.SecureCookies = strings.HasPrefix(publicURL, "https://")
			srv, err := newAPI(cfg, &apimocks.UserDataMock{}, log.New(io.Discard, "", 0))
			require.NoError(t, err)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+auth.LoginRoute, http.NoBody)
			require.NoError(t, err)
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusFound, resp.StatusCode)
			loc, err := resp.Location()
			require.NoError(t, err)
			assert.Equal(t, publicURL+auth.CallbackRoute, loc.Query().Get("redirect_uri"))
			cookies := resp.Cookies()
			require.Len(t, cookies, 1)
			assert.Equal(t, auth.LoginCookie, cookies[0].Name)
			assert.Equal(t, auth.CallbackRoute, cookies[0].Path)
			assert.Equal(t, cfg.Auth.SecureCookies, cookies[0].Secure)
		})
	}
}

func TestNewAPI_invalidConfig(t *testing.T) {
	cfg := testConfig("")
	cfg.Auth.PublicURL = ""
	_, err := newAPI(cfg, &apimocks.UserDataMock{}, log.New(io.Discard, "", 0))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create api server")
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func requireClosed(t *testing.T, ln net.Listener) {
	t.Helper()
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", ln.Addr().String())
	if err == nil {
		conn.Close()
	}
	require.Error(t, err, "listener %s must be closed", ln.Addr())
}

func TestServeAll_stopsOnCancel(t *testing.T) {
	apiLn, healthLn := localListener(t), localListener(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- serveAll(ctx, fakeListen(map[string]net.Listener{"api": apiLn, "health": healthLn}, nil),
			log.New(&out, "", 0), []server{{name: "api", addr: "api", handler: okHandler()},
				{name: "health", addr: "health", handler: okHandler()}})
	}()

	for _, ln := range []net.Listener{apiLn, healthLn} {
		code, _ := get(t, "http://"+ln.Addr().String()+"/")
		assert.Equal(t, http.StatusNoContent, code)
	}

	cancel()
	require.NoError(t, requireDone(t, done))
	requireClosed(t, apiLn)
	requireClosed(t, healthLn)
	assert.Contains(t, out.String(), "[INFO] servers stopped")
}

func TestServeAll_failureStopsOthers(t *testing.T) {
	apiLn := localListener(t)
	errBind := errors.New("address already in use")

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- serveAll(t.Context(), fakeListen(map[string]net.Listener{"api": apiLn}, map[string]error{"health": errBind}),
			log.New(&out, "", 0), []server{{name: "api", addr: "api", handler: okHandler()},
				{name: "health", addr: "health", handler: okHandler()}})
	}()

	err := requireDone(t, done)
	require.ErrorIs(t, err, errBind)
	assert.Contains(t, err.Error(), "health server: listen health")
	requireClosed(t, apiLn)
}

func TestServeAll_realListenFailure(t *testing.T) {
	busy := localListener(t)
	var out bytes.Buffer
	err := serveAll(t.Context(), listenTCP, log.New(&out, "", 0), []server{
		{name: "api", addr: "127.0.0.1:0", handler: okHandler()},
		{name: "health", addr: busy.Addr().String(), handler: okHandler()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health server: listen "+busy.Addr().String())
}

func TestHealthRoutes(t *testing.T) {
	rep, err := health.NewReporter([]health.Check{{Name: "database", Tier: health.Critical, Description: "ok",
		Error: "down", Run: func(context.Context) error { return nil }}}, time.Second)
	require.NoError(t, err)
	h := healthRoutes(rep)

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "all checks", method: http.MethodGet, path: "/health", want: http.StatusOK},
		{name: "critical checks", method: http.MethodGet, path: "/health/critical", want: http.StatusOK},
		{name: "unknown path", method: http.MethodGet, path: "/api/v1/user-data", want: http.StatusNotFound},
		{name: "wrong method", method: http.MethodPost, path: "/health", want: http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, http.NoBody))
			resp := w.Result()
			defer resp.Body.Close()
			assert.Equal(t, tc.want, resp.StatusCode)
		})
	}
}

func TestResolveVersion(t *testing.T) {
	buildInfo := func(version string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: version}, Settings: settings}, true
		}
	}
	vcs := debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef"}
	tests := []struct {
		name string
		rev  string
		read func() (*debug.BuildInfo, bool)
		want string
	}{
		{name: "ldflags revision wins", rev: "v1.2.3", read: buildInfo("v9.9.9", vcs), want: "v1.2.3"},
		{name: "module version", rev: "unknown", read: buildInfo("v0.4.0", vcs), want: "v0.4.0"},
		{name: "devel module uses vcs revision", rev: "unknown", read: buildInfo("(devel)", vcs), want: "0123456"},
		{name: "no module version uses vcs revision", rev: "unknown", read: buildInfo("", vcs), want: "0123456"},
		{name: "short vcs revision is ignored", rev: "unknown", read: buildInfo("", debug.BuildSetting{Key: "vcs.revision", Value: "abc"}),
			want: "unknown"},
		{name: "no vcs data", rev: "unknown", read: buildInfo("(devel)"), want: "unknown"},
		{name: "no build info", rev: "unknown", read: func() (*debug.BuildInfo, bool) { return nil, false }, want: "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveVersion(tc.rev, tc.read))
		})
	}
}
