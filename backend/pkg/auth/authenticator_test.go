package auth

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/api"
)

// fakeRevalidator is a keycloak with refresh token rotation off: a refresh issues a new token
// and leaves the old one active. it records every refresh token it is asked about.
type fakeRevalidator struct {
	mu         sync.Mutex
	active     map[string]bool
	issued     int
	refreshErr error  // returned by Refresh when set
	introErr   error  // returned by Introspect when set
	newToken   string // refresh token Refresh issues; generated when empty
	refreshed  []string
	introspect []string
}

func newFakeRevalidator(active ...string) *fakeRevalidator {
	f := &fakeRevalidator{active: map[string]bool{}}
	for _, t := range active {
		f.active[t] = true
	}
	return f
}

func (f *fakeRevalidator) Refresh(_ context.Context, refreshToken string) (Tokens, Verdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = append(f.refreshed, refreshToken)
	if f.refreshErr != nil {
		return Tokens{}, VerdictUnknown, f.refreshErr
	}
	if !f.active[refreshToken] {
		return Tokens{}, VerdictRevoked, nil
	}
	f.issued++
	token := f.newToken
	if token == "" {
		token = testRefreshToken + "-" + strconv.Itoa(f.issued)
	}
	f.active[token] = true
	return Tokens{RefreshToken: token, RefreshExpiresAt: testNow.Add(60 * 24 * time.Hour)}, VerdictActive, nil
}

func (f *fakeRevalidator) Introspect(_ context.Context, refreshToken string) (Verdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.introspect = append(f.introspect, refreshToken)
	if f.introErr != nil {
		return VerdictUnknown, f.introErr
	}
	if f.active[refreshToken] {
		return VerdictActive, nil
	}
	return VerdictRevoked, nil
}

func (f *fakeRevalidator) calls() (refreshed, introspected []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refreshed...), append([]string(nil), f.introspect...)
}

// newTestAuthenticator returns a SessionAuthenticator over kc at the fixed time now, logging
// into the returned buffer.
func newTestAuthenticator(c *Cookies, kc revalidator, now time.Time) (*SessionAuthenticator, *bytes.Buffer) {
	var buf bytes.Buffer
	return &SessionAuthenticator{cookies: c, keycloak: kc, logger: log.New(&buf, "", 0), now: func() time.Time { return now }}, &buf
}

// authenticate runs a with cookie and returns the user id, the recorder and the error.
func authenticate(a *SessionAuthenticator, cookie *http.Cookie) (string, *httptest.ResponseRecorder, error) {
	return authenticateRequest(a, requestWith(cookie))
}

func authenticateRequest(a *SessionAuthenticator, r *http.Request) (string, *httptest.ResponseRecorder, error) {
	rec := httptest.NewRecorder()
	id, err := a.Authenticate(rec, r)
	return id, rec, err
}

func TestNewSessionAuthenticator(t *testing.T) {
	a := NewSessionAuthenticator(newTestCookies(t, false), NewKeycloak(KeycloakConfig{}), log.New(&bytes.Buffer{}, "", 0))
	require.NotNil(t, a.now)
	assert.WithinDuration(t, time.Now(), a.now(), time.Minute)
	assert.NotNil(t, a.keycloak)
}

func TestSessionAuthenticator_Introspect(t *testing.T) {
	c := newTestCookies(t, false)
	_, cookie := issue(t, c, testProfile, testTokens(), testNow)
	kc := newFakeRevalidator(testRefreshToken)
	a, logs := newTestAuthenticator(c, kc, testNow.Add(23*time.Hour))

	id, rec, err := authenticate(a, cookie)
	require.NoError(t, err)
	assert.Equal(t, testUserID, id)
	assert.Empty(t, rec.Result().Cookies(), "an introspected session is not re-issued")
	refreshed, introspected := kc.calls()
	assert.Empty(t, refreshed)
	assert.Equal(t, []string{testRefreshToken}, introspected, "exactly one keycloak call")
	assert.Empty(t, logs.String())
}

func TestSessionAuthenticator_RefreshDue(t *testing.T) {
	c := newTestCookies(t, false)
	orig, cookie := issue(t, c, testProfile, testTokens(), testNow)
	kc := newFakeRevalidator(testRefreshToken)
	now := testNow.Add(24 * time.Hour)
	a, logs := newTestAuthenticator(c, kc, now)

	id, rec, err := authenticate(a, cookie)
	require.NoError(t, err)
	assert.Equal(t, testUserID, id)
	refreshed, introspected := kc.calls()
	assert.Equal(t, []string{testRefreshToken}, refreshed)
	assert.Empty(t, introspected, "exactly one keycloak call")
	assert.Empty(t, logs.String())

	s, err := c.OpenSession(requestWith(setCookie(t, rec)), now)
	require.NoError(t, err)
	assert.Equal(t, testRefreshToken+"-1", s.RefreshToken)
	assert.Equal(t, now, s.RefreshedAt)
	assert.Equal(t, orig.CreatedAt, s.CreatedAt)
	assert.Equal(t, orig.IdleExpiresAt, s.IdleExpiresAt, "a refresh does not slide the idle expiry")
	assert.Equal(t, testNow.Add(60*24*time.Hour), s.RefreshExpiresAt)
}

func TestSessionAuthenticator_NoSession(t *testing.T) {
	c := newTestCookies(t, false)
	_, cookie := issue(t, c, testProfile, testTokens(), testNow)

	noRefreshToken, err := newTestCookieSealer(t, 1).Seal(PurposeSession, sessionPayload{
		Sub: testUserID, CreatedAt: testNow.Unix(), IdleExpiresAt: testNow.Add(IdleTimeout).Unix(),
		RefreshedAt: testNow.Unix(), RefreshExpiresAt: testNow.Add(time.Hour).Unix(),
	}, testNow.Add(IdleTimeout))
	require.NoError(t, err)

	tests := []struct {
		name  string
		value string // session cookie value, empty for no cookie
		now   time.Time
	}{
		{name: "no cookie", now: testNow},
		{name: "malformed cookie", value: "not-a-session", now: testNow},
		{name: "tampered cookie", value: cookie.Value[:len(cookie.Value)-2] + "AA", now: testNow},
		{name: "idle expired", value: cookie.Value, now: testNow.Add(IdleTimeout)},
		{name: "absolute expired", value: cookie.Value, now: testNow.Add(AbsoluteTimeout)},
		{name: "no refresh token", value: noRefreshToken, now: testNow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kc := newFakeRevalidator(testRefreshToken)
			a, _ := newTestAuthenticator(c, kc, tc.now)

			r := requestWith(nil)
			if tc.value != "" {
				r = requestWithValue(SessionCookie, tc.value)
			}
			id, rec, err := authenticateRequest(a, r)
			require.ErrorIs(t, err, ErrNoSession)
			require.ErrorIs(t, err, api.ErrUnauthenticated)
			assert.Empty(t, id)
			assert.Empty(t, rec.Result().Cookies())
			refreshed, introspected := kc.calls()
			assert.Empty(t, refreshed, "keycloak is not asked about an unusable cookie")
			assert.Empty(t, introspected)
		})
	}
}

func TestSessionAuthenticator_Revoked(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
	}{
		{name: "by introspection", now: testNow.Add(time.Hour)},
		{name: "by invalid_grant", now: testNow.Add(25 * time.Hour)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCookies(t, false)
			_, cookie := issue(t, c, testProfile, testTokens(), testNow)
			a, _ := newTestAuthenticator(c, newFakeRevalidator(), tc.now) // nothing active

			id, rec, err := authenticate(a, cookie)
			require.ErrorIs(t, err, ErrSessionRevoked)
			require.ErrorIs(t, err, api.ErrUnauthenticated)
			assert.Empty(t, id)
			cleared := setCookie(t, rec)
			assert.Equal(t, SessionCookie, cleared.Name)
			assert.Equal(t, SessionPath, cleared.Path)
			assert.Negative(t, cleared.MaxAge, "the cookie is cleared")
		})
	}
}

func TestSessionAuthenticator_KeycloakFailure(t *testing.T) {
	kcErr := errors.New("keycloak unreachable")
	tests := []struct {
		name   string
		now    time.Time
		kc     *fakeRevalidator
		logged string
	}{
		{name: "introspection", now: testNow.Add(time.Hour), logged: "[WARN] session introspection failed, session kept: keycloak unreachable",
			kc: &fakeRevalidator{introErr: kcErr}},
		{name: "refresh", now: testNow.Add(25 * time.Hour), logged: "[WARN] session refresh failed, session kept: keycloak unreachable",
			kc: &fakeRevalidator{refreshErr: kcErr}},
		{name: "refreshed cookie too large", now: testNow.Add(25 * time.Hour), logged: "[WARN] refreshed session not written, session kept: session cookie too large",
			kc: &fakeRevalidator{active: map[string]bool{testRefreshToken: true}, newToken: strings.Repeat("n", 5000)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCookies(t, false)
			_, cookie := issue(t, c, testProfile, testTokens(), testNow)
			a, logs := newTestAuthenticator(c, tc.kc, tc.now)

			id, rec, err := authenticate(a, cookie)
			require.NoError(t, err, "keycloak failures fail open")
			assert.Equal(t, testUserID, id)
			assert.Empty(t, rec.Result().Cookies(), "the cookie is left unchanged")
			assert.Contains(t, logs.String(), tc.logged)
			assert.NotContains(t, logs.String(), testRefreshToken[:20], "no token values in logs")
		})
	}
}

func TestSessionAuthenticator_ParallelRefresh(t *testing.T) {
	c := newTestCookies(t, false)
	orig, cookie := issue(t, c, testProfile, testTokens(), testNow)
	kc := newFakeRevalidator(testRefreshToken)
	now := testNow.Add(25 * time.Hour)
	a, logs := newTestAuthenticator(c, kc, now)

	// two requests of one device carrying the same due cookie
	recs := make([]*httptest.ResponseRecorder, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range recs {
		wg.Go(func() {
			recs[i] = httptest.NewRecorder()
			_, errs[i] = a.Authenticate(recs[i], requestWith(cookie))
		})
	}
	wg.Wait()

	newCookies := make([]*http.Cookie, 0, len(recs))
	for i, rec := range recs {
		require.NoError(t, errs[i])
		fresh := setCookie(t, rec)
		s, err := c.OpenSession(requestWith(fresh), now)
		require.NoError(t, err)
		assert.Equal(t, orig.CreatedAt, s.CreatedAt, "both refreshes keep created_at")
		assert.NotEqual(t, testRefreshToken, s.RefreshToken)
		newCookies = append(newCookies, fresh)
	}
	refreshed, _ := kc.calls()
	assert.Equal(t, []string{testRefreshToken, testRefreshToken}, refreshed, "both requests refreshed")

	// whichever cookie the browser keeps, the next requests are accepted
	later := now.Add(time.Hour)
	a.now = func() time.Time { return later }
	for _, next := range append(newCookies, cookie) {
		id, _, err := authenticate(a, next)
		require.NoError(t, err)
		assert.Equal(t, testUserID, id)
	}
	assert.Empty(t, logs.String())
}

func TestRefreshDue(t *testing.T) {
	tests := []struct {
		name     string
		lifetime time.Duration // refresh token lifetime from refreshed_at
		elapsed  time.Duration
		due      bool
	}{
		{name: "fresh", lifetime: 30 * 24 * time.Hour, elapsed: time.Hour},
		{name: "daily cap", lifetime: 30 * 24 * time.Hour, elapsed: 24 * time.Hour, due: true},
		{name: "just before daily cap", lifetime: 30 * 24 * time.Hour, elapsed: 24*time.Hour - time.Second},
		{name: "half of a short lifetime", lifetime: 2 * time.Hour, elapsed: time.Hour, due: true},
		{name: "before half of a short lifetime", lifetime: 2 * time.Hour, elapsed: time.Hour - time.Second},
		{name: "already expired token", lifetime: -time.Hour, elapsed: 0, due: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := Session{Tokens: Tokens{RefreshExpiresAt: testNow.Add(tc.lifetime)}, RefreshedAt: testNow}
			assert.Equal(t, tc.due, refreshDue(s, testNow.Add(tc.elapsed)))
		})
	}
}
