// Package health runs tiered dependency checks and reports them in the Svtlv health report format.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultCheckTimeout is the time budget of a single check.
const DefaultCheckTimeout = 3 * time.Second

// Tier tells how a failed check affects the overall status.
type Tier int

// check tiers.
const (
	Critical Tier = iota // failure makes the service Unhealthy
	Advisory             // failure makes the service Degraded
)

// Status is a health status string of the Svtlv contract.
type Status string

// statuses of the Svtlv contract.
const (
	Healthy   Status = "Healthy"
	Degraded  Status = "Degraded"
	Unhealthy Status = "Unhealthy"
)

// Check is a single named dependency check.
// Description and Error are fixed strings: the underlying error of Run is never reported,
// since driver errors can contain hosts, users or credentials.
type Check struct {
	Name        string                          // reported check name
	Tier        Tier                            // effect of a failure on the overall status
	Description string                          // reported when the check passes
	Error       string                          // reported as description and error when the check fails
	Run         func(ctx context.Context) error // returns nil when the dependency is usable
}

// Report is the JSON document served by the health endpoints.
type Report struct {
	Status          Status        `json:"status"`
	TotalDurationMs float64       `json:"totalDurationMs"`
	Checks          []CheckReport `json:"checks"`
}

// CheckReport is the result of one check inside a Report.
type CheckReport struct {
	Name        string  `json:"name"`
	Status      Status  `json:"status"`
	Description string  `json:"description"`
	DurationMs  float64 `json:"durationMs"`
	Error       *string `json:"error"`
}

// Reporter runs checks and serves their report over HTTP.
type Reporter struct {
	checks  []Check
	timeout time.Duration
}

// NewReporter creates a Reporter running checks in the given order, each under its own timeout.
// returns an error if the timeout is not positive or a check is incomplete or duplicated.
func NewReporter(checks []Check, timeout time.Duration) (*Reporter, error) {
	if timeout <= 0 {
		return nil, errors.New("timeout must be positive")
	}
	seen := make(map[string]bool, len(checks))
	for i, c := range checks {
		switch {
		case c.Name == "":
			return nil, fmt.Errorf("check %d: empty name", i)
		case seen[c.Name]:
			return nil, fmt.Errorf("check %q: duplicate name", c.Name)
		case c.Tier != Critical && c.Tier != Advisory:
			return nil, fmt.Errorf("check %q: unknown tier %d", c.Name, c.Tier)
		case c.Description == "" || c.Error == "":
			return nil, fmt.Errorf("check %q: empty description or error", c.Name)
		case c.Run == nil:
			return nil, fmt.Errorf("check %q: nil run", c.Name)
		}
		seen[c.Name] = true
	}
	return &Reporter{checks: append([]Check(nil), checks...), timeout: timeout}, nil
}

// Handler serves the report of all checks, or of critical checks only when criticalOnly is set.
// Healthy and Degraded answer 200, Unhealthy answers 503.
func (r *Reporter) Handler(criticalOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rep := r.Run(req.Context(), criticalOnly)
		code := http.StatusOK
		if rep.Status == Unhealthy {
			code = http.StatusServiceUnavailable
		}
		body, err := json.Marshal(rep)
		if err != nil {
			http.Error(w, "encode health report", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		w.Write(body)
	})
}

// Run executes the checks sequentially and aggregates their statuses.
// any failed critical check makes the report Unhealthy, otherwise any failed advisory check makes it Degraded.
func (r *Reporter) Run(ctx context.Context, criticalOnly bool) Report {
	start := time.Now()
	rep := Report{Status: Healthy, Checks: make([]CheckReport, 0, len(r.checks))}
	for _, c := range r.checks {
		if criticalOnly && c.Tier != Critical {
			continue
		}
		cr := r.runCheck(ctx, c)
		rep.Checks = append(rep.Checks, cr)
		switch {
		case cr.Status == Unhealthy:
			rep.Status = Unhealthy
		case cr.Status == Degraded && rep.Status == Healthy:
			rep.Status = Degraded
		}
	}
	rep.TotalDurationMs = millis(time.Since(start))
	return rep
}

// runCheck runs one check under the reporter timeout. a check ignoring its context is abandoned
// when the timeout expires, so a stuck dependency cannot hang the endpoint.
func (r *Reporter) runCheck(ctx context.Context, c Check) CheckReport {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}

	cr := CheckReport{Name: c.Name, Status: Healthy, Description: c.Description, DurationMs: millis(time.Since(start))}
	if err != nil {
		msg := c.Error
		cr.Status, cr.Description, cr.Error = Unhealthy, msg, &msg
		if c.Tier == Advisory {
			cr.Status = Degraded
		}
	}
	return cr
}

// Pinger reports whether the database is reachable and its schema usable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// DatabaseCheck creates the critical "database" check on top of p.
func DatabaseCheck(p Pinger) Check {
	return Check{
		Name:        "database",
		Tier:        Critical,
		Description: "PostgreSQL is reachable.",
		Error:       "database unreachable",
		Run:         p.Ping,
	}
}

// maxDiscoveryBytes bounds the discovery document the keycloak check reads.
const maxDiscoveryBytes = 64 << 10

// KeycloakCheck creates the advisory "keycloak" check: it fetches the discovery document of
// issuer and passes when it answers 200 with that same issuer, the one go-oidc will require.
// keycloak down only breaks new logins, existing sessions fail open, so the tier is advisory.
func KeycloakCheck(issuer string) Check {
	client := &http.Client{
		// a redirect is a misconfigured issuer, not a discovery document
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// joined the way go-oidc does, so a trailing slash in issuer is kept in the comparison only
	discoveryURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	return Check{
		Name:        "keycloak",
		Tier:        Advisory,
		Description: "Keycloak discovery is reachable.",
		Error:       "keycloak unreachable",
		Run: func(ctx context.Context) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, http.NoBody)
			if err != nil {
				return fmt.Errorf("keycloak discovery request: %w", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("keycloak discovery: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("keycloak discovery: status %d", resp.StatusCode)
			}
			var doc struct {
				Issuer string `json:"issuer"`
			}
			if err := json.NewDecoder(io.LimitReader(resp.Body, maxDiscoveryBytes)).Decode(&doc); err != nil {
				return fmt.Errorf("keycloak discovery: decode: %w", err)
			}
			if doc.Issuer != issuer {
				return errors.New("keycloak discovery: issuer mismatch")
			}
			return nil
		},
	}
}

func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
