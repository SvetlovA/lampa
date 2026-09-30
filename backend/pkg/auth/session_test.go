package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/api"
)

const testUserID = "0b6f7c2e-8a3d-4c1f-9e5b-2d7a6f1c3e8b"

var testProfile = Profile{
	UserID:  testUserID,
	Name:    "Artem",
	Email:   "artem@example.com",
	Picture: "https://example.com/a.png",
}

// testRefreshToken is about the size of a keycloak refresh token jwt.
var testRefreshToken = strings.Repeat("r", 1000)

func testTokens() Tokens {
	return Tokens{RefreshToken: testRefreshToken, RefreshExpiresAt: testNow.Add(30 * 24 * time.Hour)}
}

func newTestCookies(t *testing.T, secure bool) *Cookies {
	t.Helper()
	return NewCookies(newTestCookieSealer(t, 1), secure)
}

// setCookie returns the single cookie written to rec.
func setCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	return cookies[0]
}

func requestWith(cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session", http.NoBody)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

// requestWithValue returns a request sending name=value as a cookie, as a browser would.
func requestWithValue(name, value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session", http.NoBody)
	r.Header.Set("Cookie", name+"="+value)
	return r
}

// issue writes a new session at now and returns it with its cookie.
func issue(t *testing.T, c *Cookies, profile Profile, tokens Tokens, now time.Time) (Session, *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	s, err := c.IssueSession(rec, profile, tokens, now)
	require.NoError(t, err)
	return s, setCookie(t, rec)
}

func TestCookies_IssueOpenRoundTrip(t *testing.T) {
	c := newTestCookies(t, false)
	now := testNow.Add(500 * time.Millisecond)
	issued, cookie := issue(t, c, testProfile, testTokens(), now)

	assert.Equal(t, testProfile, issued.Profile)
	assert.Equal(t, testTokens(), issued.Tokens)
	assert.Equal(t, testNow, issued.CreatedAt, "truncated to seconds")
	assert.Equal(t, testNow, issued.RefreshedAt)
	assert.Equal(t, testNow.Add(IdleTimeout), issued.IdleExpiresAt)
	assert.NotContains(t, cookie.Value, testRefreshToken[:20])

	opened, err := c.OpenSession(requestWith(cookie), now.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, issued, opened)
}

func TestCookies_Attributes(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "https"}[secure], func(t *testing.T) {
			c := newTestCookies(t, secure)
			_, cookie := issue(t, c, testProfile, testTokens(), testNow)
			assert.Equal(t, SessionCookie, cookie.Name)
			assert.Equal(t, "/api", cookie.Path)
			assert.True(t, cookie.HttpOnly)
			assert.Equal(t, secure, cookie.Secure)
			assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
			assert.Equal(t, int(IdleTimeout/time.Second), cookie.MaxAge)

			rec := httptest.NewRecorder()
			c.ClearSession(rec)
			header := rec.Header().Get("Set-Cookie")
			assert.Contains(t, header, "lampa_session=;")
			assert.Contains(t, header, "Path=/api")
			assert.Contains(t, header, "Max-Age=0")
			assert.Contains(t, header, "HttpOnly")
			assert.Contains(t, header, "SameSite=Lax")
			assert.Equal(t, secure, strings.Contains(header, "Secure"))

			for _, flow := range []struct{ name, path string }{{LoginCookie, LoginPath}, {DeviceCookie, DevicePath}} {
				fc := c.newCookie(flow.name, flow.path, "v", 600)
				assert.Equal(t, secure, fc.Secure, flow.name)
				assert.True(t, fc.HttpOnly, flow.name)
				assert.Equal(t, http.SameSiteLaxMode, fc.SameSite, flow.name)
				assert.Equal(t, flow.path, fc.Path, flow.name)
			}
		})
	}
}

func TestCookies_RenewSession(t *testing.T) {
	c := newTestCookies(t, false)
	issued, _ := issue(t, c, testProfile, testTokens(), testNow)

	now := testNow.Add(10 * 24 * time.Hour)
	rec := httptest.NewRecorder()
	renewed, err := c.RenewSession(rec, issued, now)
	require.NoError(t, err)
	cookie := setCookie(t, rec)

	assert.Equal(t, issued.CreatedAt, renewed.CreatedAt)
	assert.Equal(t, issued.RefreshedAt, renewed.RefreshedAt)
	assert.Equal(t, issued.Tokens, renewed.Tokens)
	assert.Equal(t, now.Add(IdleTimeout), renewed.IdleExpiresAt)
	assert.Equal(t, int(IdleTimeout/time.Second), cookie.MaxAge)

	opened, err := c.OpenSession(requestWith(cookie), now.Add(IdleTimeout-time.Minute))
	require.NoError(t, err, "still open past the original idle expiry")
	assert.Equal(t, renewed, opened)
}

func TestCookies_RenewSession_AbsoluteCap(t *testing.T) {
	c := newTestCookies(t, false)
	session, cookie := issue(t, c, testProfile, testTokens(), testNow)
	absolute := testNow.Add(AbsoluteTimeout)

	now := testNow
	for range 8 {
		now = now.Add(20 * 24 * time.Hour)
		rec := httptest.NewRecorder()
		var err error
		session, err = c.RenewSession(rec, session, now)
		require.NoError(t, err)
		cookie = setCookie(t, rec)
		assert.Equal(t, testNow, session.CreatedAt)
		assert.False(t, session.IdleExpiresAt.After(absolute))
	}
	// 160 days in: the idle expiry is clamped to the absolute cap
	assert.Equal(t, absolute, session.IdleExpiresAt)
	assert.Equal(t, int(20*24*time.Hour/time.Second), cookie.MaxAge)

	_, err := c.OpenSession(requestWith(cookie), absolute.Add(-time.Minute))
	require.NoError(t, err)
	_, err = c.OpenSession(requestWith(cookie), absolute)
	require.ErrorIs(t, err, ErrNoSession)

	_, err = c.RenewSession(httptest.NewRecorder(), session, absolute.Add(time.Second))
	require.ErrorIs(t, err, ErrInvalidSession)
}

func TestCookies_ReplaceTokens(t *testing.T) {
	c := newTestCookies(t, false)
	issued, _ := issue(t, c, testProfile, testTokens(), testNow)

	now := testNow.Add(2 * 24 * time.Hour)
	tokens := Tokens{RefreshToken: "new-token", RefreshExpiresAt: now.Add(30 * 24 * time.Hour)}
	rec := httptest.NewRecorder()
	replaced, err := c.ReplaceTokens(rec, issued, tokens, now)
	require.NoError(t, err)
	cookie := setCookie(t, rec)

	assert.Equal(t, tokens, replaced.Tokens)
	assert.Equal(t, now, replaced.RefreshedAt)
	assert.Equal(t, issued.CreatedAt, replaced.CreatedAt)
	assert.Equal(t, issued.IdleExpiresAt, replaced.IdleExpiresAt)
	assert.Equal(t, int(issued.IdleExpiresAt.Sub(now)/time.Second), cookie.MaxAge)

	opened, err := c.OpenSession(requestWith(cookie), now)
	require.NoError(t, err)
	assert.Equal(t, replaced, opened)

	_, err = c.ReplaceTokens(httptest.NewRecorder(), issued, Tokens{RefreshExpiresAt: tokens.RefreshExpiresAt}, now)
	require.ErrorIs(t, err, ErrInvalidSession, "empty refresh token")
}

func TestCookies_OpenSession_Expiry(t *testing.T) {
	c := newTestCookies(t, false)
	_, cookie := issue(t, c, testProfile, testTokens(), testNow)

	_, err := c.OpenSession(requestWith(cookie), testNow.Add(IdleTimeout-time.Minute))
	require.NoError(t, err)

	_, err = c.OpenSession(requestWith(cookie), testNow.Add(IdleTimeout))
	require.ErrorIs(t, err, ErrNoSession)
	require.ErrorIs(t, err, ErrCookieExpired)
	require.ErrorIs(t, err, api.ErrUnauthenticated)

	// under a second left: renewing it would write Max-Age=0, which deletes the cookie
	_, err = c.OpenSession(requestWith(cookie), testNow.Add(IdleTimeout-500*time.Millisecond))
	require.ErrorIs(t, err, ErrNoSession)
}

func TestCookies_ReplaceTokens_NearIdleExpiry(t *testing.T) {
	c := newTestCookies(t, false)
	issued, _ := issue(t, c, testProfile, testTokens(), testNow)
	now := issued.IdleExpiresAt.Add(-500 * time.Millisecond)

	rec := httptest.NewRecorder()
	_, err := c.ReplaceTokens(rec, issued, testTokens(), now)
	require.ErrorIs(t, err, ErrInvalidSession)
	assert.Empty(t, rec.Result().Cookies(), "nothing is written")
}

func TestCookies_OpenSession_Rejects(t *testing.T) {
	c := newTestCookies(t, false)
	_, cookie := issue(t, c, testProfile, testTokens(), testNow)

	loginValue, err := c.sealer.Seal(PurposeLogin, sessionPayload{Sub: testUserID}, testNow.Add(time.Hour))
	require.NoError(t, err)
	flipped := []byte(cookie.Value)
	flipped[len(flipped)/2] ^= 0x01

	tests := []struct {
		name    string
		request *http.Request
		wantErr error
	}{
		{"no cookie", requestWith(nil), ErrNoSession},
		{"other cookie name", requestWithValue(LoginCookie, cookie.Value), ErrNoSession},
		{"tampered", requestWithValue(SessionCookie, string(flipped)), ErrCookieTampered},
		{"truncated", requestWithValue(SessionCookie, cookie.Value[:20]), ErrCookieTruncated},
		{"wrong purpose", requestWithValue(SessionCookie, loginValue), ErrCookieWrongPurpose},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.OpenSession(tt.request, testNow)
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorIs(t, err, ErrNoSession)
			require.ErrorIs(t, err, api.ErrUnauthenticated)
		})
	}
}

func TestCookies_OpenSession_InvalidPayload(t *testing.T) {
	c := newTestCookies(t, false)
	valid := sessionPayload{
		Sub:              testUserID,
		CreatedAt:        testNow.Unix(),
		IdleExpiresAt:    testNow.Add(IdleTimeout).Unix(),
		RefreshToken:     "token",
		RefreshedAt:      testNow.Unix(),
		RefreshExpiresAt: testNow.Add(time.Hour).Unix(),
	}
	tests := []struct {
		name   string
		mutate func(p *sessionPayload)
	}{
		{"missing refresh token", func(p *sessionPayload) { p.RefreshToken = "" }},
		{"missing refresh expiry", func(p *sessionPayload) { p.RefreshExpiresAt = 0 }},
		{"non-uuid sub", func(p *sessionPayload) { p.Sub = "alice" }},
		{"missing created_at", func(p *sessionPayload) { p.CreatedAt = 0 }},
		{"past absolute expiry", func(p *sessionPayload) { p.CreatedAt = testNow.Add(-AbsoluteTimeout).Unix() }},
		{"idle expiry before the envelope's", func(p *sessionPayload) { p.IdleExpiresAt = testNow.Unix() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.mutate(&p)
			value, err := c.sealer.Seal(PurposeSession, p, testNow.Add(IdleTimeout))
			require.NoError(t, err)
			_, err = c.OpenSession(requestWithValue(SessionCookie, value), testNow)
			require.ErrorIs(t, err, ErrNoSession)
		})
	}

	value, err := c.sealer.Seal(PurposeSession, valid, testNow.Add(IdleTimeout))
	require.NoError(t, err)
	_, err = c.OpenSession(requestWithValue(SessionCookie, value), testNow)
	require.NoError(t, err, "the unmodified payload opens")
}

func TestCookies_IssueSession_Invalid(t *testing.T) {
	c := newTestCookies(t, false)
	tests := []struct {
		name    string
		profile Profile
		tokens  Tokens
	}{
		{"non-uuid sub", Profile{UserID: "alice"}, testTokens()},
		{"uuid without dashes", Profile{UserID: strings.ReplaceAll(testUserID, "-", "")}, testTokens()},
		{"uuid with bad char", Profile{UserID: "0b6f7c2e-8a3d-4c1f-9e5b-2d7a6f1c3e8z"}, testTokens()},
		{"missing refresh token", testProfile, Tokens{RefreshExpiresAt: testNow.Add(time.Hour)}},
		{"missing refresh expiry", testProfile, Tokens{RefreshToken: "token"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, err := c.IssueSession(rec, tt.profile, tt.tokens, testNow)
			require.ErrorIs(t, err, ErrInvalidSession)
			assert.Empty(t, rec.Header().Values("Set-Cookie"))
		})
	}

	upper := testProfile
	upper.UserID = strings.ToUpper(testUserID)
	_, err := c.IssueSession(httptest.NewRecorder(), upper, testTokens(), testNow)
	require.NoError(t, err, "upper-case uuid is accepted")
}

func TestCookies_IssueSession_OversizedProfile(t *testing.T) {
	c := newTestCookies(t, true)
	profile := Profile{
		UserID:  testUserID,
		Name:    strings.Repeat("Ж", 200),
		Email:   strings.Repeat("e", 300) + "@example.com",
		Picture: "https://example.com/" + strings.Repeat("p", 2000),
	}
	rec := httptest.NewRecorder()
	issued, err := c.IssueSession(rec, profile, testTokens(), testNow)
	require.NoError(t, err)

	assert.Equal(t, strings.Repeat("Ж", maxNameRunes), issued.Name)
	assert.Empty(t, issued.Email, "over-long email dropped, not truncated")
	assert.Empty(t, issued.Picture, "picture dropped to fit, not truncated")
	assert.Equal(t, testRefreshToken, issued.RefreshToken)
	header := rec.Header().Get("Set-Cookie")
	assert.LessOrEqual(t, len(header), maxCookieBytes)

	opened, err := c.OpenSession(requestWith(setCookie(t, rec)), testNow)
	require.NoError(t, err)
	assert.Equal(t, issued, opened)

	// a picture that fits is kept
	profile.Picture = "https://example.com/" + strings.Repeat("p", 500)
	kept, _ := issue(t, c, profile, testTokens(), testNow)
	assert.Equal(t, profile.Picture, kept.Picture)
}

func TestCookies_TooLarge(t *testing.T) {
	c := newTestCookies(t, false)
	huge := Tokens{RefreshToken: strings.Repeat("r", 3500), RefreshExpiresAt: testNow.Add(time.Hour)}

	rec := httptest.NewRecorder()
	_, err := c.IssueSession(rec, testProfile, huge, testNow)
	require.ErrorIs(t, err, ErrSessionTooLarge)
	require.NotErrorIs(t, err, api.ErrUnauthenticated)
	assert.Empty(t, rec.Header().Values("Set-Cookie"), "nothing written")

	issued, _ := issue(t, c, testProfile, testTokens(), testNow)
	rec = httptest.NewRecorder()
	_, err = c.ReplaceTokens(rec, issued, huge, testNow.Add(time.Hour))
	require.ErrorIs(t, err, ErrSessionTooLarge)
	assert.Empty(t, rec.Header().Values("Set-Cookie"), "existing cookie left alone")
}

func TestValidUUID(t *testing.T) {
	assert.True(t, validUUID(testUserID))
	assert.True(t, validUUID(strings.ToUpper(testUserID)))
	for _, s := range []string{"", "alice", "{" + testUserID[:34] + "}", "urn:uuid:" + testUserID, testUserID + " "} {
		assert.False(t, validUUID(s), s)
	}
}

func TestCookies_LogoutMark(t *testing.T) {
	c := newTestCookies(t, false)
	_, before := issue(t, c, testProfile, testTokens(), testNow)

	rec := httptest.NewRecorder()
	c.EndSession(rec, testNow.Add(time.Hour))
	var mark *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		switch ck.Name {
		case SessionCookie:
			assert.Negative(t, ck.MaxAge, "the session cookie is cleared")
		case LogoutCookie:
			mark = ck
		}
	}
	require.NotNil(t, mark)
	assert.Equal(t, "/api", mark.Path)
	assert.True(t, mark.HttpOnly)
	assert.Equal(t, int(AbsoluteTimeout/time.Second), mark.MaxAge)

	opened := func(session, logout *http.Cookie) error {
		r := requestWith(session)
		r.AddCookie(logout)
		_, err := c.OpenSession(r, testNow.Add(2*time.Hour))
		return err
	}
	_, sameSecond := issue(t, c, testProfile, testTokens(), testNow.Add(time.Hour))
	_, after := issue(t, c, testProfile, testTokens(), testNow.Add(time.Hour+time.Second))

	err := opened(before, mark)
	require.ErrorIs(t, err, ErrNoSession, "a session renewed after the logout keeps its created_at")
	require.ErrorIs(t, err, api.ErrUnauthenticated)
	require.ErrorIs(t, opened(sameSecond, mark), ErrNoSession)
	require.NoError(t, opened(after, mark))
	for _, bad := range []string{"", "x", "0", "-5"} {
		require.NoError(t, opened(before, reqCookie(LogoutCookie, bad)), "malformed mark %q is ignored", bad)
	}
}

func TestCookies_ClearLogoutMark(t *testing.T) {
	c := newTestCookies(t, true)

	rec := httptest.NewRecorder()
	c.ClearLogoutMark(rec, requestWith(nil))
	assert.Empty(t, rec.Result().Cookies(), "nothing to clear")

	rec = httptest.NewRecorder()
	c.ClearLogoutMark(rec, requestWith(reqCookie(LogoutCookie, "1")))
	cleared := setCookie(t, rec)
	assert.Equal(t, LogoutCookie, cleared.Name)
	assert.Equal(t, "/api", cleared.Path)
	assert.Negative(t, cleared.MaxAge)
	assert.True(t, cleared.Secure)
}
