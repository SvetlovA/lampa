package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKeycloakClient is a keycloak for the handlers: revalidation from fakeRevalidator, the
// login calls recorded and answered from its fields.
type fakeKeycloakClient struct {
	*fakeRevalidator

	authURLErr     error
	authCalls      []loginState // state, nonce and verifier AuthCodeURL was called with
	logoutErr      error
	logoutTokens   []string
	logoutURL      string
	logoutURLCalls int

	exchangeErr    error
	exchangeTokens Tokens
	exchangeCalls  []loginState // code in State, verifier and nonce Exchange was called with

	start    DeviceStart
	startErr error

	poll          DeviceResult
	pollErr       error
	pollCodes     []string
	pollVerifiers []string
}

func newFakeKeycloakClient(active ...string) *fakeKeycloakClient {
	return &fakeKeycloakClient{
		fakeRevalidator: newFakeRevalidator(active...),
		exchangeTokens:  testTokens(),
		logoutURL:       "https://kc.example/logout?client_id=svtlv-lampa",
		start: DeviceStart{
			DeviceCode:              "device-code-secret",
			Verifier:                "device-pkce-verifier",
			UserCode:                "WDJB-MJHT",
			VerificationURI:         "https://kc.example/device",
			VerificationURIComplete: "https://kc.example/device?user_code=WDJB-MJHT",
			ExpiresIn:               600 * time.Second,
			Interval:                5 * time.Second,
		},
	}
}

func (f *fakeKeycloakClient) AuthCodeURL(_ context.Context, state, nonce, verifier string) (string, error) {
	f.authCalls = append(f.authCalls, loginState{State: state, Nonce: nonce, Verifier: verifier})
	if f.authURLErr != nil {
		return "", f.authURLErr
	}
	return "https://kc.example/auth?state=" + url.QueryEscape(state), nil
}

func (f *fakeKeycloakClient) Logout(_ context.Context, refreshToken string) error {
	f.logoutTokens = append(f.logoutTokens, refreshToken)
	return f.logoutErr
}

func (f *fakeKeycloakClient) LogoutURL(context.Context) (string, error) {
	f.logoutURLCalls++
	if f.logoutErr != nil {
		return "", f.logoutErr
	}
	return f.logoutURL, nil
}

func (f *fakeKeycloakClient) Exchange(_ context.Context, code, verifier, nonce string) (Profile, Tokens, error) {
	f.exchangeCalls = append(f.exchangeCalls, loginState{State: code, Nonce: nonce, Verifier: verifier})
	if f.exchangeErr != nil {
		return Profile{}, Tokens{}, f.exchangeErr
	}
	return testProfile, f.exchangeTokens, nil
}

func (f *fakeKeycloakClient) StartDevice(context.Context) (DeviceStart, error) {
	if f.startErr != nil {
		return DeviceStart{}, f.startErr
	}
	return f.start, nil
}

func (f *fakeKeycloakClient) PollDevice(_ context.Context, deviceCode, verifier string) (DeviceResult, error) {
	f.pollCodes = append(f.pollCodes, deviceCode)
	f.pollVerifiers = append(f.pollVerifiers, verifier)
	if f.pollErr != nil {
		return DeviceResult{}, f.pollErr
	}
	return f.poll, nil
}

// testHandlers bundles Handlers with its fake keycloak, a settable clock and the log buffer.
type testHandlers struct {
	h    *Handlers
	c    *Cookies
	kc   *fakeKeycloakClient
	now  time.Time
	logs *bytes.Buffer
}

func newTestHandlers(t *testing.T, kc *fakeKeycloakClient) *testHandlers {
	t.Helper()
	th := &testHandlers{c: newTestCookies(t, false), kc: kc, now: testNow, logs: &bytes.Buffer{}}
	th.h = newHandlers(th.c, kc, log.New(th.logs, "", 0), func() time.Time { return th.now })
	return th
}

// do serves method target with cookies and returns the recorder.
func (th *testHandlers) do(method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, http.NoBody)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	th.h.ServeHTTP(rec, r)
	return rec
}

// cookieNamed returns the cookie name written to rec, failing when it is not written exactly once.
func cookieNamed(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	var found []*http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			found = append(found, c)
		}
	}
	require.Len(t, found, 1, "cookie %s", name)
	return found[0]
}

func noCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		assert.NotEqual(t, name, c.Name, "cookie %s written", name)
	}
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v))
	return v
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := decodeJSON[struct {
		Error struct{ Code string } `json:"error"`
	}](t, rec)
	return body.Error.Code
}

func assertAnonymous(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"authenticated":false}`, rec.Body.String())
}

func assertSignedIn(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"authenticated":true,"user":{"id":"`+testUserID+
		`","name":"Artem","email":"artem@example.com","picture":"https://example.com/a.png"}}`, rec.Body.String())
}

func assertCleared(t *testing.T, c *http.Cookie, path string) {
	t.Helper()
	assert.Empty(t, c.Value)
	assert.Equal(t, path, c.Path)
	assert.Negative(t, c.MaxAge, "the cookie is cleared")
}

// sealed returns name=value sealed for purpose, as a browser would send it.
func (th *testHandlers) sealed(t *testing.T, name string, purpose Purpose, payload any, expiresAt time.Time) *http.Cookie {
	t.Helper()
	value, err := th.c.sealer.Seal(purpose, payload, expiresAt)
	require.NoError(t, err)
	return reqCookie(name, value)
}

// reqCookie returns the request cookie name=value. AddCookie sends only the name and value; the
// attributes are set so no insecure cookie literal appears in the tests.
func reqCookie(name, value string) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

func TestNewHandlers(t *testing.T) {
	h := NewHandlers(newTestCookies(t, false), NewKeycloak(KeycloakConfig{}), log.New(&bytes.Buffer{}, "", 0))
	require.NotNil(t, h.now)
	assert.WithinDuration(t, time.Now(), h.now(), time.Minute)
	assert.NotNil(t, h.mux)
}

func TestHandlers_Session(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	orig, cookie := issue(t, th.c, testProfile, testTokens(), testNow)
	th.now = testNow.Add(10 * time.Hour)

	rec := th.do(http.MethodGet, SessionRoute, cookie)
	assertSignedIn(t, rec)
	renewed := cookieNamed(t, rec, SessionCookie)
	assert.Equal(t, int(IdleTimeout/time.Second), renewed.MaxAge)

	s, err := th.c.OpenSession(requestWith(renewed), th.now)
	require.NoError(t, err)
	assert.Equal(t, orig.CreatedAt, s.CreatedAt, "renewal keeps created_at")
	assert.Equal(t, th.now.Add(IdleTimeout), s.IdleExpiresAt, "the idle expiry slides")
	assert.Equal(t, testRefreshToken, s.RefreshToken)
	refreshed, introspected := th.kc.calls()
	assert.Empty(t, refreshed)
	assert.Equal(t, []string{testRefreshToken}, introspected)
	assert.Empty(t, th.logs.String())
}

func TestHandlers_SessionAnonymous(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	rec := th.do(http.MethodGet, SessionRoute)
	assertAnonymous(t, rec)
	assert.Empty(t, rec.Result().Cookies(), "no cookie to clear")
	refreshed, introspected := th.kc.calls()
	assert.Empty(t, refreshed)
	assert.Empty(t, introspected)
}

func TestHandlers_SessionRefreshDue(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	orig, cookie := issue(t, th.c, testProfile, testTokens(), testNow)
	th.now = testNow.Add(25 * time.Hour)

	rec := th.do(http.MethodGet, SessionRoute, cookie)
	assertSignedIn(t, rec)
	s, err := th.c.OpenSession(requestWith(cookieNamed(t, rec, SessionCookie)), th.now)
	require.NoError(t, err, "one cookie, renewed and carrying the refreshed token")
	assert.Equal(t, testRefreshToken+"-1", s.RefreshToken)
	assert.Equal(t, th.now, s.RefreshedAt)
	assert.Equal(t, orig.CreatedAt, s.CreatedAt)
	assert.Equal(t, th.now.Add(IdleTimeout), s.IdleExpiresAt)
	refreshed, introspected := th.kc.calls()
	assert.Equal(t, []string{testRefreshToken}, refreshed)
	assert.Empty(t, introspected)
}

func TestHandlers_SessionRefreshedTooLarge(t *testing.T) {
	kc := newFakeKeycloakClient(testRefreshToken)
	kc.newToken = strings.Repeat("n", 5000)
	th := newTestHandlers(t, kc)
	_, cookie := issue(t, th.c, testProfile, testTokens(), testNow)
	th.now = testNow.Add(25 * time.Hour)

	rec := th.do(http.MethodGet, SessionRoute, cookie)
	assertSignedIn(t, rec)
	s, err := th.c.OpenSession(requestWith(cookieNamed(t, rec, SessionCookie)), th.now)
	require.NoError(t, err)
	assert.Equal(t, testRefreshToken, s.RefreshToken, "the old refresh token is kept")
	assert.Equal(t, th.now.Add(IdleTimeout), s.IdleExpiresAt, "the session is still renewed")
	assert.Contains(t, th.logs.String(), "[WARN] refreshed session not written, session kept: session cookie too large")
}

func TestHandlers_SessionAbsoluteCap(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	orig, cookie := issue(t, th.c, testProfile, testTokens(), testNow)
	// renew every 20 days, as a device used now and then would; the last renewal is 20 days
	// before the absolute cap, where a full idle period no longer fits
	for day := 20; day <= 160; day += 20 {
		th.now = testNow.Add(time.Duration(day) * 24 * time.Hour)
		rec := th.do(http.MethodGet, SessionRoute, cookie)
		assertSignedIn(t, rec)
		cookie = cookieNamed(t, rec, SessionCookie)
	}
	s, err := th.c.OpenSession(requestWith(cookie), th.now)
	require.NoError(t, err)
	absolute := orig.CreatedAt.Add(AbsoluteTimeout)
	assert.Equal(t, absolute, s.IdleExpiresAt, "the idle expiry is clamped to the absolute cap")
	assert.Equal(t, int(absolute.Sub(th.now)/time.Second), cookie.MaxAge)
	assert.Less(t, cookie.MaxAge, int(IdleTimeout/time.Second))

	th.now = absolute
	rec := th.do(http.MethodGet, SessionRoute, cookie)
	assertAnonymous(t, rec)
	assertCleared(t, cookieNamed(t, rec, SessionCookie), SessionPath)
}

func TestHandlers_SessionUnusable(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient()) // nothing active: introspection revokes
	_, cookie := issue(t, th.c, testProfile, testTokens(), testNow)

	tests := []struct {
		name  string
		value string
		now   time.Time
	}{
		{name: "expired", value: cookie.Value, now: testNow.Add(IdleTimeout)},
		{name: "tampered", value: cookie.Value[:len(cookie.Value)-2] + "AA", now: testNow},
		{name: "malformed", value: "garbage", now: testNow},
		{name: "revoked", value: cookie.Value, now: testNow.Add(time.Hour)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			th.now = tc.now
			rec := th.do(http.MethodGet, SessionRoute, reqCookie(SessionCookie, tc.value))
			assertAnonymous(t, rec)
			assertCleared(t, cookieNamed(t, rec, SessionCookie), SessionPath)
		})
	}
}

func TestHandlers_SessionKeycloakFailure(t *testing.T) {
	kc := newFakeKeycloakClient()
	kc.introErr = errors.New("keycloak unreachable")
	th := newTestHandlers(t, kc)
	_, cookie := issue(t, th.c, testProfile, testTokens(), testNow)
	th.now = testNow.Add(time.Hour)

	rec := th.do(http.MethodGet, SessionRoute, cookie)
	assertSignedIn(t, rec)
	s, err := th.c.OpenSession(requestWith(cookieNamed(t, rec, SessionCookie)), th.now)
	require.NoError(t, err, "the session is still renewed")
	assert.Equal(t, testRefreshToken, s.RefreshToken)
	assert.Contains(t, th.logs.String(), "[WARN] session introspection failed, session kept")
}

func TestHandlers_SessionRenewedNearIdleExpiry(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	_, cookie := issue(t, th.c, testProfile, testTokens(), testNow)
	// two seconds of idle time left, the least a session still opens with
	th.now = testNow.Add(IdleTimeout - 2*time.Second)
	rec := th.do(http.MethodGet, SessionRoute, cookie)
	assertSignedIn(t, rec)
	assert.Len(t, rec.Result().Cookies(), 1)
	assert.Empty(t, th.logs.String())
}

func TestHandlers_Login(t *testing.T) {
	kc := newFakeKeycloakClient()
	th := newTestHandlers(t, kc)

	rec := th.do(http.MethodGet, LoginRoute+"?return="+url.QueryEscape("/?card=42&source=tmdb"))
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Empty(t, rec.Body.String())
	require.Len(t, kc.authCalls, 1)
	call := kc.authCalls[0]
	assert.Equal(t, "https://kc.example/auth?state="+url.QueryEscape(call.State), rec.Header().Get("Location"))

	lc := cookieNamed(t, rec, LoginCookie)
	assert.Equal(t, LoginPath, lc.Path)
	assert.Equal(t, 600, lc.MaxAge)
	assert.True(t, lc.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, lc.SameSite)
	var st loginState
	require.NoError(t, th.c.sealer.Open(PurposeLogin, lc.Value, testNow, &st))
	assert.Equal(t, loginState{State: call.State, Nonce: call.Nonce, Verifier: call.Verifier, Return: "/?card=42&source=tmdb"}, st)
	assert.NotEqual(t, st.State, st.Nonce)
	assert.GreaterOrEqual(t, len(st.Verifier), 43, "RFC 7636 verifier length")
	require.ErrorIs(t, th.c.sealer.Open(PurposeLogin, lc.Value, testNow.Add(loginTimeout), &st), ErrCookieExpired)

	// a second login gets fresh values
	th.do(http.MethodGet, LoginRoute)
	require.Len(t, kc.authCalls, 2)
	assert.NotEqual(t, call, kc.authCalls[1])
}

func TestHandlers_LoginKeycloakDown(t *testing.T) {
	kc := newFakeKeycloakClient()
	kc.authURLErr = errors.New("discovery failed")
	th := newTestHandlers(t, kc)

	rec := th.do(http.MethodGet, LoginRoute)
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/#svtlv-login=failed", rec.Header().Get("Location"))
	noCookie(t, rec, LoginCookie)
	assert.Contains(t, th.logs.String(), "[WARN] login: keycloak unavailable: discovery failed")
}

func TestReturnPath(t *testing.T) {
	tests := []struct{ raw, want string }{
		{raw: "", want: "/"},
		{raw: "/", want: "/"},
		{raw: "/?card=1&source=tmdb", want: "/?card=1&source=tmdb"},
		{raw: "/index.html", want: "/index.html"},
		{raw: "//evil", want: "/"},
		{raw: "//evil.example/path", want: "/"},
		{raw: "https://evil", want: "/"},
		{raw: "http:/evil", want: "/"},
		{raw: "javascript:alert(1)", want: "/"},
		{raw: `\\evil`, want: "/"},
		{raw: `/\evil`, want: "/"},
		{raw: "/a\\b", want: "/"},
		{raw: "/#frag", want: "/"},
		{raw: "/a b", want: "/"},
		{raw: "/a\tb", want: "/"},
		{raw: "/\r\nSet-Cookie: x=1", want: "/"},
		{raw: "/é", want: "/"},
		{raw: "evil", want: "/"},
		{raw: "/" + strings.Repeat("a", maxReturnPathSize), want: "/"},
		{raw: "/" + strings.Repeat("a", maxReturnPathSize-1), want: "/" + strings.Repeat("a", maxReturnPathSize-1)},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			assert.Equal(t, tc.want, returnPath(tc.raw))
		})
	}
}

// startLogin runs /auth/login with return and returns the login cookie and the state keycloak got.
func (th *testHandlers) startLogin(t *testing.T, ret string) (*http.Cookie, loginState) {
	t.Helper()
	rec := th.do(http.MethodGet, LoginRoute+"?return="+url.QueryEscape(ret))
	require.Equal(t, http.StatusFound, rec.Code)
	return cookieNamed(t, rec, LoginCookie), th.kc.authCalls[len(th.kc.authCalls)-1]
}

func TestHandlers_Callback(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient())
	lc, call := th.startLogin(t, "/?card=42")
	th.now = testNow.Add(time.Minute)

	rec := th.do(http.MethodGet, CallbackRoute+"?code=the-code&state="+url.QueryEscape(call.State), lc)
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/?card=42#svtlv-login=ok", rec.Header().Get("Location"))
	assert.Equal(t, []loginState{{State: "the-code", Nonce: call.Nonce, Verifier: call.Verifier}}, th.kc.exchangeCalls)
	assertCleared(t, cookieNamed(t, rec, LoginCookie), LoginPath)

	s, err := th.c.OpenSession(requestWith(cookieNamed(t, rec, SessionCookie)), th.now)
	require.NoError(t, err)
	assert.Equal(t, testProfile, s.Profile)
	assert.Equal(t, testTokens(), s.Tokens)
	assert.Equal(t, th.now, s.CreatedAt)
	assert.Empty(t, th.logs.String())
}

func TestHandlers_CallbackOpenRedirect(t *testing.T) {
	for _, ret := range []string{"//evil", "https://evil", `\\evil`, `/\evil`} {
		t.Run(ret, func(t *testing.T) {
			th := newTestHandlers(t, newFakeKeycloakClient())
			lc, call := th.startLogin(t, ret)
			rec := th.do(http.MethodGet, CallbackRoute+"?code=c&state="+url.QueryEscape(call.State), lc)
			assert.Equal(t, "/#svtlv-login=ok", rec.Header().Get("Location"))
		})
	}
}

func TestHandlers_CallbackFailures(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(th *testHandlers, lc *http.Cookie, st loginState) (query string, cookie *http.Cookie)
		logged string
	}{
		{name: "no login cookie", logged: "no login cookie",
			setup: func(_ *testHandlers, _ *http.Cookie, st loginState) (string, *http.Cookie) {
				return "code=c&state=" + url.QueryEscape(st.State), nil
			}},
		{name: "expired login cookie", logged: "expired",
			setup: func(th *testHandlers, lc *http.Cookie, st loginState) (string, *http.Cookie) {
				th.now = testNow.Add(loginTimeout)
				return "code=c&state=" + url.QueryEscape(st.State), lc
			}},
		{name: "tampered login cookie", logged: "tampered",
			setup: func(_ *testHandlers, lc *http.Cookie, st loginState) (string, *http.Cookie) {
				return "code=c&state=" + url.QueryEscape(st.State), reqCookie(LoginCookie, lc.Value[:len(lc.Value)-2]+"AA")
			}},
		{name: "session cookie as login cookie", logged: "wrong purpose",
			setup: func(th *testHandlers, _ *http.Cookie, st loginState) (string, *http.Cookie) {
				value, err := th.c.sealer.Seal(PurposeSession, st, testNow.Add(time.Hour))
				if err != nil {
					panic(err)
				}
				return "code=c&state=" + url.QueryEscape(st.State), reqCookie(LoginCookie, value)
			}},
		{name: "state mismatch", logged: "state mismatch",
			setup: func(_ *testHandlers, lc *http.Cookie, _ loginState) (string, *http.Cookie) {
				return "code=c&state=other", lc
			}},
		{name: "no state", logged: "state mismatch",
			setup: func(_ *testHandlers, lc *http.Cookie, _ loginState) (string, *http.Cookie) {
				return "code=c", lc
			}},
		{name: "keycloak error", logged: "keycloak returned an error",
			setup: func(_ *testHandlers, lc *http.Cookie, st loginState) (string, *http.Cookie) {
				return "error=access_denied&state=" + url.QueryEscape(st.State), lc
			}},
		{name: "no code", logged: "no code",
			setup: func(_ *testHandlers, lc *http.Cookie, st loginState) (string, *http.Cookie) {
				return "state=" + url.QueryEscape(st.State), lc
			}},
		{name: "exchange failed", logged: "exchange boom",
			setup: func(th *testHandlers, lc *http.Cookie, st loginState) (string, *http.Cookie) {
				th.kc.exchangeErr = errors.New("exchange boom")
				return "code=c&state=" + url.QueryEscape(st.State), lc
			}},
		{name: "session cookie cannot fit", logged: "session cookie too large",
			setup: func(th *testHandlers, lc *http.Cookie, st loginState) (string, *http.Cookie) {
				th.kc.exchangeTokens.RefreshToken = strings.Repeat("r", 5000)
				return "code=c&state=" + url.QueryEscape(st.State), lc
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			th := newTestHandlers(t, newFakeKeycloakClient())
			lc, call := th.startLogin(t, "/?card=42")
			query, cookie := tc.setup(th, lc, call)
			var cookies []*http.Cookie
			if cookie != nil {
				cookies = append(cookies, cookie)
			}

			rec := th.do(http.MethodGet, CallbackRoute+"?"+query, cookies...)
			assert.Equal(t, http.StatusFound, rec.Code)
			assert.Equal(t, "/#svtlv-login=failed", rec.Header().Get("Location"))
			assert.Empty(t, rec.Body.String(), "never a json page")
			assertCleared(t, cookieNamed(t, rec, LoginCookie), LoginPath)
			noCookie(t, rec, SessionCookie)
			assert.Contains(t, th.logs.String(), "[WARN] login callback failed: ")
			assert.Contains(t, th.logs.String(), tc.logged)
			assert.NotContains(t, th.logs.String(), "access_denied", "request input is not logged")
		})
	}
}

func TestHandlers_DeviceStart(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient())
	rec := th.do(http.MethodPost, DeviceStartRoute)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"user_code":"WDJB-MJHT","verification_uri":"https://kc.example/device",
		"verification_uri_complete":"https://kc.example/device?user_code=WDJB-MJHT","expires_in":600,"interval":5}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "device-code-secret", "the device code stays on the server")
	assert.NotContains(t, rec.Body.String(), "device-pkce-verifier", "the PKCE verifier stays on the server")

	dc := cookieNamed(t, rec, DeviceCookie)
	assert.Equal(t, DevicePath, dc.Path)
	assert.Equal(t, 600, dc.MaxAge)
	assert.True(t, dc.HttpOnly)
	var st deviceState
	require.NoError(t, th.c.sealer.Open(PurposeDevice, dc.Value, testNow, &st))
	assert.Equal(t, deviceState{DeviceCode: "device-code-secret", Verifier: "device-pkce-verifier", Interval: 5, ExpiresAt: testNow.Add(600 * time.Second).Unix()}, st)
	assert.ErrorIs(t, th.c.sealer.Open(PurposeDevice, dc.Value, testNow.Add(600*time.Second), &st), ErrCookieExpired)
}

func TestHandlers_DeviceStartNoCompleteURI(t *testing.T) {
	kc := newFakeKeycloakClient()
	kc.start.VerificationURIComplete = ""
	th := newTestHandlers(t, kc)
	rec := th.do(http.MethodPost, DeviceStartRoute)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeJSON[deviceStartJSON](t, rec)
	assert.Equal(t, "https://kc.example/device", body.VerificationURIComplete)
}

func TestHandlers_DeviceStartErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(kc *fakeKeycloakClient)
	}{
		{name: "keycloak down", setup: func(kc *fakeKeycloakClient) { kc.startErr = errors.New("down") }},
		{name: "already expired", setup: func(kc *fakeKeycloakClient) { kc.start.ExpiresIn = 500 * time.Millisecond }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kc := newFakeKeycloakClient()
			tc.setup(kc)
			th := newTestHandlers(t, kc)
			rec := th.do(http.MethodPost, DeviceStartRoute)
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Equal(t, "keycloak_unavailable", errorCode(t, rec))
			noCookie(t, rec, DeviceCookie)
		})
	}
}

// deviceCookie returns a lampa_device cookie for a login started at testNow.
func (th *testHandlers) deviceCookie(t *testing.T) *http.Cookie {
	t.Helper()
	return th.sealed(t, DeviceCookie, PurposeDevice,
		deviceState{DeviceCode: "device-code-secret", Verifier: "device-pkce-verifier", Interval: 5, ExpiresAt: testNow.Add(600 * time.Second).Unix()},
		testNow.Add(600*time.Second))
}

func TestHandlers_DevicePollAuthorized(t *testing.T) {
	kc := newFakeKeycloakClient()
	kc.poll = DeviceResult{Status: DeviceAuthorized, Profile: testProfile, Tokens: testTokens()}
	th := newTestHandlers(t, kc)
	th.now = testNow.Add(30 * time.Second)

	rec := th.do(http.MethodPost, DevicePollRoute, th.deviceCookie(t))
	assertSignedIn(t, rec)
	assert.Equal(t, []string{"device-code-secret"}, kc.pollCodes)
	assert.Equal(t, []string{"device-pkce-verifier"}, kc.pollVerifiers)
	assertCleared(t, cookieNamed(t, rec, DeviceCookie), DevicePath)
	s, err := th.c.OpenSession(requestWith(cookieNamed(t, rec, SessionCookie)), th.now)
	require.NoError(t, err)
	assert.Equal(t, testTokens(), s.Tokens, "the session carries the returned refresh token")
	assert.Equal(t, th.now, s.CreatedAt)
}

func TestHandlers_DevicePollPending(t *testing.T) {
	kc := newFakeKeycloakClient()
	kc.poll = DeviceResult{Status: DevicePending}
	th := newTestHandlers(t, kc)
	rec := th.do(http.MethodPost, DevicePollRoute, th.deviceCookie(t))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"status":"pending","interval":5}`, rec.Body.String())
	assert.Empty(t, rec.Result().Cookies())
}

func TestHandlers_DevicePollSlowDown(t *testing.T) {
	kc := newFakeKeycloakClient()
	kc.poll = DeviceResult{Status: DeviceSlowDown}
	th := newTestHandlers(t, kc)
	th.now = testNow.Add(100 * time.Second)

	rec := th.do(http.MethodPost, DevicePollRoute, th.deviceCookie(t))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"status":"slow_down","interval":10}`, rec.Body.String())
	dc := cookieNamed(t, rec, DeviceCookie)
	assert.Equal(t, 500, dc.MaxAge, "the original expiry is kept")
	var st deviceState
	require.NoError(t, th.c.sealer.Open(PurposeDevice, dc.Value, th.now, &st))
	assert.Equal(t, int64(10), st.Interval)
	assert.Equal(t, "device-code-secret", st.DeviceCode)
	assert.Equal(t, "device-pkce-verifier", st.Verifier)

	// the next poll answers with the slower interval
	kc.poll = DeviceResult{Status: DevicePending}
	rec = th.do(http.MethodPost, DevicePollRoute, dc)
	assert.JSONEq(t, `{"status":"pending","interval":10}`, rec.Body.String())
	assert.Equal(t, []string{"device-pkce-verifier", "device-pkce-verifier"}, kc.pollVerifiers)
}

func TestHandlers_DevicePollSlowDownNearExpiry(t *testing.T) {
	kc := newFakeKeycloakClient()
	kc.poll = DeviceResult{Status: DeviceSlowDown}
	th := newTestHandlers(t, kc)
	cookie := th.deviceCookie(t)
	// under a second left: the cookie cannot be rewritten, so the old one keeps the old interval
	th.now = testNow.Add(600*time.Second - 500*time.Millisecond)

	rec := th.do(http.MethodPost, DevicePollRoute, cookie)
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"status":"slow_down","interval":10}`, rec.Body.String())
	noCookie(t, rec, DeviceCookie)
}

func TestHandlers_DevicePollEnded(t *testing.T) {
	tests := []struct {
		name   string
		result DeviceResult
		status int
		code   string
	}{
		{name: "expired", result: DeviceResult{Status: DeviceExpired}, status: http.StatusGone, code: "expired"},
		{name: "denied", result: DeviceResult{Status: DeviceDenied}, status: http.StatusForbidden, code: "access_denied"},
		{name: "session cannot fit", status: http.StatusInternalServerError, code: "login_failed",
			result: DeviceResult{Status: DeviceAuthorized, Profile: testProfile,
				Tokens: Tokens{RefreshToken: strings.Repeat("r", 5000), RefreshExpiresAt: testNow.Add(time.Hour)}}},
		{name: "invalid profile", status: http.StatusInternalServerError, code: "login_failed",
			result: DeviceResult{Status: DeviceAuthorized, Profile: Profile{UserID: "not-a-uuid"}, Tokens: testTokens()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kc := newFakeKeycloakClient()
			kc.poll = tc.result
			th := newTestHandlers(t, kc)
			rec := th.do(http.MethodPost, DevicePollRoute, th.deviceCookie(t))
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, errorCode(t, rec))
			assertCleared(t, cookieNamed(t, rec, DeviceCookie), DevicePath)
			noCookie(t, rec, SessionCookie)
		})
	}
}

func TestHandlers_DevicePollErrors(t *testing.T) {
	tests := []struct {
		name   string
		cookie func(t *testing.T, th *testHandlers) *http.Cookie
		setup  func(th *testHandlers)
		status int
		code   string
	}{
		{name: "no cookie", status: http.StatusBadRequest, code: "no_device_login"},
		{name: "expired cookie", status: http.StatusBadRequest, code: "no_device_login",
			cookie: func(t *testing.T, th *testHandlers) *http.Cookie { return th.deviceCookie(t) },
			setup:  func(th *testHandlers) { th.now = testNow.Add(600 * time.Second) }},
		{name: "tampered cookie", status: http.StatusBadRequest, code: "no_device_login",
			cookie: func(t *testing.T, th *testHandlers) *http.Cookie {
				value := th.deviceCookie(t).Value
				return reqCookie(DeviceCookie, value[:len(value)-2]+"AA")
			}},
		{name: "login cookie", status: http.StatusBadRequest, code: "no_device_login",
			cookie: func(t *testing.T, th *testHandlers) *http.Cookie {
				c := th.sealed(t, DeviceCookie, PurposeLogin, deviceState{DeviceCode: "x"}, testNow.Add(time.Hour))
				return c
			}},
		{name: "old device cookie without verifier", status: http.StatusBadRequest, code: "no_device_login",
			cookie: func(t *testing.T, th *testHandlers) *http.Cookie {
				return th.sealed(t, DeviceCookie, PurposeDevice,
					deviceState{DeviceCode: "device-code-secret", Interval: 5, ExpiresAt: testNow.Add(600 * time.Second).Unix()},
					testNow.Add(600*time.Second))
			}},
		{name: "keycloak down", status: http.StatusServiceUnavailable, code: "keycloak_unavailable",
			cookie: func(t *testing.T, th *testHandlers) *http.Cookie { return th.deviceCookie(t) },
			setup:  func(th *testHandlers) { th.kc.pollErr = errors.New("down") }},
		{name: "unknown status", status: http.StatusServiceUnavailable, code: "keycloak_unavailable",
			cookie: func(t *testing.T, th *testHandlers) *http.Cookie { return th.deviceCookie(t) },
			setup:  func(th *testHandlers) { th.kc.poll = DeviceResult{Status: DeviceStatus(99)} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			th := newTestHandlers(t, newFakeKeycloakClient())
			var cookies []*http.Cookie
			if tc.cookie != nil {
				cookies = append(cookies, tc.cookie(t, th))
			}
			if tc.setup != nil {
				tc.setup(th)
			}
			rec := th.do(http.MethodPost, DevicePollRoute, cookies...)
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, errorCode(t, rec))
			noCookie(t, rec, SessionCookie)
			noCookie(t, rec, DeviceCookie)
		})
	}
}

func TestHandlers_Logout(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	_, cookie := issue(t, th.c, testProfile, testTokens(), testNow)

	for name, cookies := range map[string][]*http.Cookie{"signed in": {cookie}, "already signed out": nil} {
		t.Run(name, func(t *testing.T) {
			rec := th.do(http.MethodPost, LogoutRoute, cookies...)
			assert.Equal(t, http.StatusNoContent, rec.Code)
			assert.Empty(t, rec.Body.String())
			assertCleared(t, cookieNamed(t, rec, SessionCookie), SessionPath)
			assertCleared(t, cookieNamed(t, rec, LoginCookie), LoginPath)
			assertCleared(t, cookieNamed(t, rec, DeviceCookie), DevicePath)
			mark := cookieNamed(t, rec, LogoutCookie)
			assert.Equal(t, strconv.FormatInt(testNow.Unix(), 10), mark.Value, "the logout time")
			assert.Equal(t, SessionPath, mark.Path)
		})
	}
	refreshed, introspected := th.kc.calls()
	assert.Empty(t, th.kc.logoutTokens, "TV logout leaves the phone's Keycloak browser session alone")
	assert.Empty(t, refreshed)
	assert.Empty(t, introspected)
}

func TestHandlers_LogoutBrowser(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	_, cookie := issue(t, th.c, testProfile, testTokens(), testNow)
	rec := th.do(http.MethodPost, LogoutRoute+"?sso=1", cookie)
	assert.Equal(t, http.StatusOK, rec.Code)
	var body logoutJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body.SSOLoggedOut)
	assert.Equal(t, th.kc.logoutURL, body.LogoutURL)
	assert.Equal(t, []string{testRefreshToken}, th.kc.logoutTokens)
	assert.Equal(t, 1, th.kc.logoutURLCalls)
	assertCleared(t, cookieNamed(t, rec, SessionCookie), SessionPath)
	assertCleared(t, cookieNamed(t, rec, LoginCookie), LoginPath)
	assertCleared(t, cookieNamed(t, rec, DeviceCookie), DevicePath)
}

func TestHandlers_LogoutKeycloakUnavailable(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	th.kc.logoutErr = errors.New("unavailable")
	_, cookie := issue(t, th.c, testProfile, testTokens(), testNow)
	rec := th.do(http.MethodPost, LogoutRoute+"?sso=1", cookie)
	assert.Equal(t, http.StatusOK, rec.Code)
	var body logoutJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.False(t, body.SSOLoggedOut)
	assertCleared(t, cookieNamed(t, rec, SessionCookie), SessionPath)
	assert.Contains(t, th.logs.String(), "keycloak SSO logout unavailable")
}

// another tab's /session request, in flight during the logout, answers with a renewed cookie
// after it: that cookie must not sign the device back in.
func TestHandlers_LogoutRenewedLate(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient(testRefreshToken))
	_, cookie := issue(t, th.c, testProfile, testTokens(), testNow.Add(-time.Hour))
	late := cookieNamed(t, th.do(http.MethodGet, SessionRoute, cookie), SessionCookie)
	mark := cookieNamed(t, th.do(http.MethodPost, LogoutRoute, cookie), LogoutCookie)

	th.now = testNow.Add(time.Minute)
	rec := th.do(http.MethodGet, SessionRoute, late, mark)
	assertAnonymous(t, rec)
	assertCleared(t, cookieNamed(t, rec, SessionCookie), SessionPath)

	r := requestWith(late)
	r.AddCookie(mark)
	_, err := NewSessionAuthenticator(th.c, nil, nil).Authenticate(httptest.NewRecorder(), r)
	require.ErrorIs(t, err, ErrNoSession, "user data refuses it too, before any keycloak call")
}

func TestHandlers_LoginClearsLogoutMark(t *testing.T) {
	mark := reqCookie(LogoutCookie, "1789000000")

	t.Run("callback", func(t *testing.T) {
		th := newTestHandlers(t, newFakeKeycloakClient())
		lc, call := th.startLogin(t, "/")
		rec := th.do(http.MethodGet, CallbackRoute+"?code=c&state="+url.QueryEscape(call.State), lc, mark)
		require.Equal(t, http.StatusFound, rec.Code)
		assertCleared(t, cookieNamed(t, rec, LogoutCookie), SessionPath)
		_, err := th.c.OpenSession(requestWith(cookieNamed(t, rec, SessionCookie)), th.now)
		require.NoError(t, err)
	})
	t.Run("device", func(t *testing.T) {
		kc := newFakeKeycloakClient()
		kc.poll = DeviceResult{Status: DeviceAuthorized, Profile: testProfile, Tokens: testTokens()}
		th := newTestHandlers(t, kc)
		rec := th.do(http.MethodPost, DevicePollRoute, th.deviceCookie(t), mark)
		assertSignedIn(t, rec)
		assertCleared(t, cookieNamed(t, rec, LogoutCookie), SessionPath)
	})
	t.Run("no mark", func(t *testing.T) {
		th := newTestHandlers(t, newFakeKeycloakClient())
		lc, call := th.startLogin(t, "/")
		rec := th.do(http.MethodGet, CallbackRoute+"?code=c&state="+url.QueryEscape(call.State), lc)
		noCookie(t, rec, LogoutCookie)
	})
}

func TestHandlers_Routing(t *testing.T) {
	th := newTestHandlers(t, newFakeKeycloakClient())
	tests := []struct {
		method, path string
		status       int
		allow        string
	}{
		{method: http.MethodPost, path: SessionRoute, status: http.StatusMethodNotAllowed, allow: "GET"},
		{method: http.MethodHead, path: SessionRoute, status: http.StatusMethodNotAllowed, allow: "GET"},
		{method: http.MethodHead, path: LoginRoute, status: http.StatusMethodNotAllowed, allow: "GET"},
		{method: http.MethodPost, path: CallbackRoute, status: http.StatusMethodNotAllowed, allow: "GET"},
		{method: http.MethodGet, path: DeviceStartRoute, status: http.StatusMethodNotAllowed, allow: "POST"},
		{method: http.MethodGet, path: DevicePollRoute, status: http.StatusMethodNotAllowed, allow: "POST"},
		{method: http.MethodGet, path: LogoutRoute, status: http.StatusMethodNotAllowed, allow: "POST"},
		{method: http.MethodGet, path: "/api/v1/auth/unknown", status: http.StatusNotFound},
		{method: http.MethodGet, path: "/api/v1/auth/", status: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := th.do(tc.method, tc.path)
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.allow, rec.Header().Get("Allow"))
			if tc.method != http.MethodHead {
				assert.NotEmpty(t, errorCode(t, rec))
			}
		})
	}
	assert.Empty(t, th.kc.authCalls, "HEAD never starts a login")
}
