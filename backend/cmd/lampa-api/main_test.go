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
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/config"
	"github.com/SvetlovA/lampa/backend/pkg/health"
	"github.com/SvetlovA/lampa/backend/pkg/storage/pgtest"
)

const testSecret = "s3cr3t-pw"

var testKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, config.DataKeySize))

func noEnv(string) (string, bool) { return "", false }

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
	return config.Config{Listen: "api", HealthListen: "health", DBDSN: dsn, DataKey: [config.DataKeySize]byte{7}, MaxBodyBytes: 1 << 20}
}

// testSettings returns an appsettings.json with the given database and listeners; the password
// and data key come from the LAMPA_DB_PASSWORD and LAMPA_API_DATA_KEY placeholders.
func testSettings(t *testing.T, host string, port uint16, apiListen, healthListen string) fstest.MapFS {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"Api":    map[string]any{"Listen": apiListen, "MaxBodyBytes": 1 << 20},
		"Health": map[string]any{"Listen": healthListen},
		"Database": map[string]any{"Host": host, "Port": port, "Name": "lampa", "User": "lampa",
			"Password": "{LAMPA_DB_PASSWORD}", "SSLMode": "disable"},
		"DataKey": "{LAMPA_API_DATA_KEY}",
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
		{name: "missing key", settings: settings, env: map[string]string{"LAMPA_DB_PASSWORD": testSecret}, wantErr: config.ErrMissing},
		{name: "bad key", settings: settings, env: map[string]string{"LAMPA_DB_PASSWORD": testSecret, "LAMPA_API_DATA_KEY": "short"},
			wantErr: config.ErrInvalid},
		{name: "same listen", settings: testSettings(t, "127.0.0.1", 1, ":9000", ":9000"),
			env: map[string]string{"LAMPA_DB_PASSWORD": testSecret, "LAMPA_API_DATA_KEY": testKey}, wantErr: config.ErrInvalid},
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
	apiLn, healthLn := localListener(t), localListener(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var out bytes.Buffer
	done := waitStart(ctx, testConfig(dsn), &out,
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
	env := envOf(map[string]string{"LAMPA_DB_PASSWORD": db.Password, "LAMPA_API_DATA_KEY": testKey})
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
