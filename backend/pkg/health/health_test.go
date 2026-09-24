package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const driverError = "dial tcp 10.1.2.3:5432: password authentication failed for user lampa-secret"

func okRun(context.Context) error { return nil }

func failRun(context.Context) error { return errors.New(driverError) }

func testCheck(name string, tier Tier, run func(context.Context) error) Check {
	return Check{Name: name, Tier: tier, Description: name + " is fine.", Error: name + " failed", Run: run}
}

type pingerFunc func(ctx context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }

// serve runs one health request and returns the response head and body.
func serve(t *testing.T, r *Reporter, criticalOnly bool) (int, http.Header, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)
	w := httptest.NewRecorder()
	r.Handler(criticalOnly).ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, body
}

func TestNewReporter(t *testing.T) {
	tests := []struct {
		name    string
		checks  []Check
		timeout time.Duration
		wantErr string
	}{
		{name: "valid", checks: []Check{testCheck("a", Critical, okRun), testCheck("b", Advisory, okRun)},
			timeout: time.Second},
		{name: "no checks", timeout: time.Second},
		{name: "zero timeout", timeout: 0, wantErr: "timeout must be positive"},
		{name: "empty name", checks: []Check{testCheck("", Critical, okRun)}, timeout: time.Second,
			wantErr: "check 0: empty name"},
		{name: "duplicate name", checks: []Check{testCheck("a", Critical, okRun), testCheck("a", Advisory, okRun)},
			timeout: time.Second, wantErr: `check "a": duplicate name`},
		{name: "unknown tier", checks: []Check{testCheck("a", Tier(7), okRun)}, timeout: time.Second,
			wantErr: `check "a": unknown tier 7`},
		{name: "empty description", checks: []Check{{Name: "a", Error: "x", Run: okRun}}, timeout: time.Second,
			wantErr: `check "a": empty description or error`},
		{name: "empty error", checks: []Check{{Name: "a", Description: "x", Run: okRun}}, timeout: time.Second,
			wantErr: `check "a": empty description or error`},
		{name: "nil run", checks: []Check{testCheck("a", Critical, nil)}, timeout: time.Second,
			wantErr: `check "a": nil run`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewReporter(tc.checks, tc.timeout)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				assert.Nil(t, r)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, r)
		})
	}
}

func TestReporter_Handler(t *testing.T) {
	tests := []struct {
		name         string
		checks       []Check
		criticalOnly bool
		wantCode     int
		wantStatus   Status
		wantChecks   map[string]Status
	}{
		{name: "all healthy", checks: []Check{testCheck("db", Critical, okRun), testCheck("idp", Advisory, okRun)},
			wantCode: http.StatusOK, wantStatus: Healthy, wantChecks: map[string]Status{"db": Healthy, "idp": Healthy}},
		{name: "all healthy critical only",
			checks:       []Check{testCheck("db", Critical, okRun), testCheck("idp", Advisory, okRun)},
			criticalOnly: true, wantCode: http.StatusOK, wantStatus: Healthy, wantChecks: map[string]Status{"db": Healthy}},
		{name: "advisory fail", checks: []Check{testCheck("db", Critical, okRun), testCheck("idp", Advisory, failRun)},
			wantCode: http.StatusOK, wantStatus: Degraded, wantChecks: map[string]Status{"db": Healthy, "idp": Degraded}},
		{name: "advisory fail critical only",
			checks:       []Check{testCheck("db", Critical, okRun), testCheck("idp", Advisory, failRun)},
			criticalOnly: true, wantCode: http.StatusOK, wantStatus: Healthy, wantChecks: map[string]Status{"db": Healthy}},
		{name: "critical fail", checks: []Check{testCheck("db", Critical, failRun), testCheck("idp", Advisory, okRun)},
			wantCode: http.StatusServiceUnavailable, wantStatus: Unhealthy,
			wantChecks: map[string]Status{"db": Unhealthy, "idp": Healthy}},
		{name: "critical fail critical only",
			checks:       []Check{testCheck("db", Critical, failRun), testCheck("idp", Advisory, okRun)},
			criticalOnly: true, wantCode: http.StatusServiceUnavailable, wantStatus: Unhealthy,
			wantChecks: map[string]Status{"db": Unhealthy}},
		{name: "critical and advisory fail",
			checks:   []Check{testCheck("idp", Advisory, failRun), testCheck("db", Critical, failRun)},
			wantCode: http.StatusServiceUnavailable, wantStatus: Unhealthy,
			wantChecks: map[string]Status{"db": Unhealthy, "idp": Degraded}},
		{name: "no checks", wantCode: http.StatusOK, wantStatus: Healthy, wantChecks: map[string]Status{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewReporter(tc.checks, time.Second)
			require.NoError(t, err)
			code, header, body := serve(t, r, tc.criticalOnly)
			assert.Equal(t, tc.wantCode, code)
			assert.Equal(t, "application/json; charset=utf-8", header.Get("Content-Type"))
			assert.Equal(t, "no-store", header.Get("Cache-Control"))

			var rep Report
			require.NoError(t, json.Unmarshal(body, &rep))
			assert.Equal(t, tc.wantStatus, rep.Status)
			got := map[string]Status{}
			for _, c := range rep.Checks {
				got[c.Name] = c.Status
			}
			assert.Equal(t, tc.wantChecks, got)
			assert.NotContains(t, string(body), "lampa-secret")
			assert.NotContains(t, string(body), "10.1.2.3")
		})
	}
}

func TestReporter_Handler_JSONShape(t *testing.T) {
	r, err := NewReporter([]Check{DatabaseCheck(pingerFunc(okRun)), testCheck("idp", Advisory, failRun)}, time.Second)
	require.NoError(t, err)
	_, _, body := serve(t, r, false)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.ElementsMatch(t, []string{"status", "totalDurationMs", "checks"}, keys(doc))
	assert.Equal(t, "Degraded", doc["status"])
	assert.IsType(t, float64(0), doc["totalDurationMs"])

	checks, ok := doc["checks"].([]any)
	require.True(t, ok)
	require.Len(t, checks, 2)

	db, ok := checks[0].(map[string]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"name", "status", "description", "durationMs", "error"}, keys(db))
	assert.Equal(t, "database", db["name"])
	assert.Equal(t, "Healthy", db["status"])
	assert.Equal(t, "PostgreSQL is reachable.", db["description"])
	assert.IsType(t, float64(0), db["durationMs"])
	v, present := db["error"]
	assert.True(t, present, "error key must be present")
	assert.Nil(t, v)

	idp, ok := checks[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Degraded", idp["status"])
	assert.Equal(t, "idp failed", idp["description"])
	assert.Equal(t, "idp failed", idp["error"])

	// an empty report keeps checks as an array, never null
	empty, err := NewReporter(nil, time.Second)
	require.NoError(t, err)
	_, _, body = serve(t, empty, false)
	assert.JSONEq(t, `[]`, string(mustField(t, body, "checks")))
}

func TestReporter_Run_Timeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	tests := []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{name: "honors context", run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		{name: "ignores context", run: func(context.Context) error {
			<-release
			return nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewReporter([]Check{testCheck("slow", Critical, tc.run), testCheck("fast", Critical, okRun)},
				50*time.Millisecond)
			require.NoError(t, err)

			start := time.Now()
			rep := r.Run(t.Context(), false)
			assert.Less(t, time.Since(start), 2*time.Second)
			assert.Equal(t, Unhealthy, rep.Status)
			require.Len(t, rep.Checks, 2)
			assert.Equal(t, Unhealthy, rep.Checks[0].Status)
			require.NotNil(t, rep.Checks[0].Error)
			assert.Equal(t, "slow failed", *rep.Checks[0].Error)
			assert.GreaterOrEqual(t, rep.Checks[0].DurationMs, 50.0)
			assert.Equal(t, Healthy, rep.Checks[1].Status, "each check gets its own timeout")
			assert.GreaterOrEqual(t, rep.TotalDurationMs, rep.Checks[0].DurationMs)
		})
	}
}

func TestDatabaseCheck(t *testing.T) {
	tests := []struct {
		name       string
		ping       error
		wantStatus Status
		wantError  any
	}{
		{name: "reachable", wantStatus: Healthy, wantError: nil},
		{name: "unreachable", ping: errors.New(driverError), wantStatus: Unhealthy, wantError: "database unreachable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var called bool
			c := DatabaseCheck(pingerFunc(func(ctx context.Context) error {
				called = true
				_, hasDeadline := ctx.Deadline()
				assert.True(t, hasDeadline)
				return tc.ping
			}))
			assert.Equal(t, "database", c.Name)
			assert.Equal(t, Critical, c.Tier)

			r, err := NewReporter([]Check{c}, DefaultCheckTimeout)
			require.NoError(t, err)
			code, _, body := serve(t, r, true)
			assert.True(t, called)
			assert.Equal(t, tc.wantStatus == Healthy, code == http.StatusOK)

			var doc struct {
				Checks []map[string]any `json:"checks"`
			}
			require.NoError(t, json.Unmarshal(body, &doc))
			require.Len(t, doc.Checks, 1)
			assert.Equal(t, string(tc.wantStatus), doc.Checks[0]["status"])
			assert.Equal(t, tc.wantError, doc.Checks[0]["error"])
			assert.NotContains(t, string(body), "password")
		})
	}
}

func keys(m map[string]any) []string {
	res := make([]string, 0, len(m))
	for k := range m {
		res = append(res, k)
	}
	return res
}

func mustField(t *testing.T, body []byte, name string) json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &doc))
	v, ok := doc[name]
	require.True(t, ok)
	return v
}
