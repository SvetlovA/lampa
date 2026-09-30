package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SvetlovA/lampa/backend/pkg/api"
)

// session lifetimes, design §5.3. they are constants rather than configuration: tests inject a
// clock, and the embedded config cannot change on the server without a redeploy anyway.
const (
	IdleTimeout     = 30 * 24 * time.Hour  // a session unused this long ends
	AbsoluteTimeout = 180 * 24 * time.Hour // no session outlives this, however often it is renewed
)

// cookie names and paths. the flow cookies are scoped to the only route that reads them.
const (
	SessionCookie = "lampa_session"
	SessionPath   = "/api"
	LogoutCookie  = "lampa_logout" // logout time of this device, see EndSession
	LoginCookie   = "lampa_login"
	LoginPath     = "/api/v1/auth/callback"
	DeviceCookie  = "lampa_device"
	DevicePath    = "/api/v1/auth/device"
)

// size limits of the session cookie. browsers cap a cookie at 4096 bytes; the bound applies to
// the whole Set-Cookie value, name and attributes included.
const (
	maxCookieBytes = 4000
	maxNameRunes   = 64
	maxEmailBytes  = 254 // RFC 5321 path limit; a longer address is dropped, never truncated
)

var (
	// ErrNoSession is returned when a request carries no usable session cookie. it wraps
	// api.ErrUnauthenticated, so the api middleware answers 401.
	ErrNoSession = fmt.Errorf("no session: %w", api.ErrUnauthenticated)
	// ErrSessionTooLarge is returned when the session cookie cannot fit the size bound even
	// without a picture. the refresh token is never dropped or truncated to make it fit.
	ErrSessionTooLarge = errors.New("session cookie too large")
	// ErrInvalidSession is returned when a session cannot be issued: a non-uuid user id, a
	// missing refresh token or expiry, or a session already past its expiry.
	ErrInvalidSession = errors.New("invalid session")
)

// Profile is the user identity copied from a verified ID token.
type Profile struct {
	UserID  string // keycloak sub, a uuid
	Name    string
	Email   string
	Picture string // absolute https url or empty
}

// Tokens are the keycloak tokens kept in the session: only the refresh token and its expiry.
type Tokens struct {
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// Session is an open session cookie. times have second precision.
type Session struct {
	Profile
	Tokens
	CreatedAt     time.Time // login time, fixes the absolute expiry
	IdleExpiresAt time.Time // slid forward only by RenewSession
	RefreshedAt   time.Time // last time the refresh token was issued
}

// sessionPayload is the sealed JSON form of a Session, times in unix seconds.
type sessionPayload struct {
	Sub              string `json:"sub"`
	Name             string `json:"name,omitempty"`
	Email            string `json:"email,omitempty"`
	Picture          string `json:"picture,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	IdleExpiresAt    int64  `json:"idle_expires_at"`
	RefreshToken     string `json:"refresh_token"`
	RefreshedAt      int64  `json:"refreshed_at"`
	RefreshExpiresAt int64  `json:"refresh_expires_at"`
}

// Cookies writes and reads the auth cookies: the sealed session cookie and the attributes every
// auth cookie shares. it keeps no state, the session lives entirely in the cookie.
type Cookies struct {
	sealer *CookieSealer
	secure bool
}

// NewCookies returns Cookies sealing with sealer; secure sets the Secure attribute on every
// cookie. Production overrides it per request with ForRequest.
func NewCookies(sealer *CookieSealer, secure bool) *Cookies {
	return &Cookies{sealer: sealer, secure: secure}
}

// ForRequest keeps cookies host-only and marks them Secure when this request arrived over HTTPS.
func (c *Cookies) ForRequest(r *http.Request) *Cookies {
	requestCookies := *c
	origin, err := api.RequestOrigin(r)
	requestCookies.secure = err == nil && strings.HasPrefix(origin, "https://")
	return &requestCookies
}

// IssueSession starts a new session for profile and writes its cookie. name and email are
// length-limited and an over-long picture is dropped; the written session is returned.
func (c *Cookies) IssueSession(w http.ResponseWriter, profile Profile, tokens Tokens, now time.Time) (Session, error) {
	if !validUUID(profile.UserID) {
		return Session{}, fmt.Errorf("%w: user id is not a uuid", ErrInvalidSession)
	}
	now = unixTime(now.Unix())
	s := Session{
		Profile:       limitProfile(profile),
		Tokens:        tokens,
		CreatedAt:     now,
		IdleExpiresAt: now.Add(IdleTimeout),
		RefreshedAt:   now,
	}
	return c.writeSession(w, s, now)
}

// ReplaceTokens writes session with the tokens of a keycloak refresh. created_at and the idle
// expiry are kept: a refresh neither extends nor shortens the session.
func (c *Cookies) ReplaceTokens(w http.ResponseWriter, session Session, tokens Tokens, now time.Time) (Session, error) {
	session.Tokens = tokens
	session.RefreshedAt = unixTime(now.Unix())
	return c.writeSession(w, session, now)
}

// RenewSession slides the idle expiry of an open session to min(now+IdleTimeout,
// created_at+AbsoluteTimeout) and writes its cookie. created_at is kept. a session already past
// its idle expiry is not revived.
func (c *Cookies) RenewSession(w http.ResponseWriter, session Session, now time.Time) (Session, error) {
	if !now.Before(session.IdleExpiresAt) {
		return Session{}, fmt.Errorf("%w: idle expiry reached", ErrInvalidSession)
	}
	session.IdleExpiresAt = minTime(unixTime(now.Unix()).Add(IdleTimeout), session.CreatedAt.Add(AbsoluteTimeout))
	return c.writeSession(w, session, now)
}

// OpenSession reads the session cookie of r. a missing, tampered, idle- or absolute-expired
// cookie, or one without a refresh token, fails with an error wrapping ErrNoSession.
func (c *Cookies) OpenSession(r *http.Request, now time.Time) (Session, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return Session{}, fmt.Errorf("%w: no cookie", ErrNoSession)
	}
	var p sessionPayload
	if err := c.sealer.Open(PurposeSession, cookie.Value, now, &p); err != nil {
		return Session{}, fmt.Errorf("%w: %w", ErrNoSession, err)
	}
	s := Session{
		Profile:       Profile{UserID: p.Sub, Name: p.Name, Email: p.Email, Picture: p.Picture},
		Tokens:        Tokens{RefreshToken: p.RefreshToken, RefreshExpiresAt: unixTime(p.RefreshExpiresAt)},
		CreatedAt:     unixTime(p.CreatedAt),
		IdleExpiresAt: unixTime(p.IdleExpiresAt),
		RefreshedAt:   unixTime(p.RefreshedAt),
	}
	if err := validateSession(s, now); err != nil {
		return Session{}, fmt.Errorf("%w: %w", ErrNoSession, err)
	}
	if loggedOut, ok := logoutMark(r); ok && !s.CreatedAt.After(loggedOut) {
		return Session{}, fmt.Errorf("%w: signed out on this device", ErrNoSession)
	}
	return s, nil
}

// ClearSession deletes the session cookie, with the same attributes it was set with.
func (c *Cookies) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, c.newCookie(SessionCookie, SessionPath, "", -1))
}

// EndSession deletes the session cookie and marks this device signed out at now: OpenSession
// then refuses every session created up to now. a request still in flight (another tab) may
// answer with a renewed session cookie after the logout, and that cookie keeps its created_at.
// the mark lives as long as any such session could, so it needs no sealing: dropping or forging
// it only affects the sender's own sessions.
func (c *Cookies) EndSession(w http.ResponseWriter, now time.Time) {
	c.ClearSession(w)
	http.SetCookie(w, c.newCookie(LogoutCookie, SessionPath, strconv.FormatInt(now.Unix(), 10), int(AbsoluteTimeout/time.Second)))
}

// ClearLogoutMark deletes the logout mark of r, if any, after a login: a session created in the
// same second as the logout would be refused otherwise.
func (c *Cookies) ClearLogoutMark(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(LogoutCookie); err == nil {
		http.SetCookie(w, c.newCookie(LogoutCookie, SessionPath, "", -1))
	}
}

// logoutMark returns the logout time r carries; a missing or malformed mark is ignored.
func logoutMark(r *http.Request) (time.Time, bool) {
	cookie, err := r.Cookie(LogoutCookie)
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(cookie.Value, 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return unixTime(sec), true
}

// newCookie builds an auth cookie: HttpOnly, SameSite=Lax (the login callback arrives as a
// cross-site top-level redirect from keycloak) and Secure when the request is HTTPS.
// maxAge follows http.Cookie: negative deletes the cookie.
func (c *Cookies) newCookie(name, path, value string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure is off only for the http-over-tailnet deployment, design §5.1
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// writeSession validates, seals and sets the session cookie with Max-Age up to its idle expiry.
// when the cookie exceeds the size bound the picture is dropped; if it still does not fit,
// ErrSessionTooLarge is returned and nothing is written.
func (c *Cookies) writeSession(w http.ResponseWriter, s Session, now time.Time) (Session, error) {
	if err := validateSession(s, now); err != nil {
		return Session{}, fmt.Errorf("%w: %w", ErrInvalidSession, err)
	}
	cookie, err := c.sessionCookie(s, now)
	if err != nil {
		return Session{}, err
	}
	if len(cookie.String()) > maxCookieBytes && s.Picture != "" {
		s.Picture = ""
		if cookie, err = c.sessionCookie(s, now); err != nil {
			return Session{}, err
		}
	}
	if size := len(cookie.String()); size > maxCookieBytes {
		return Session{}, fmt.Errorf("%w: %d bytes", ErrSessionTooLarge, size)
	}
	http.SetCookie(w, cookie)
	return s, nil
}

// sessionCookie seals s into a session cookie expiring at its idle expiry.
func (c *Cookies) sessionCookie(s Session, now time.Time) (*http.Cookie, error) {
	value, err := c.sealer.Seal(PurposeSession, sessionPayload{
		Sub:              s.UserID,
		Name:             s.Name,
		Email:            s.Email,
		Picture:          s.Picture,
		CreatedAt:        s.CreatedAt.Unix(),
		IdleExpiresAt:    s.IdleExpiresAt.Unix(),
		RefreshToken:     s.RefreshToken,
		RefreshedAt:      s.RefreshedAt.Unix(),
		RefreshExpiresAt: s.RefreshExpiresAt.Unix(),
	}, s.IdleExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("seal session: %w", err)
	}
	return c.newCookie(SessionCookie, SessionPath, value, int(s.IdleExpiresAt.Sub(now)/time.Second)), nil
}

// validateSession checks the invariants shared by opened and written sessions: a uuid user id,
// a refresh token with its expiry, and neither the idle nor the absolute expiry reached at now.
func validateSession(s Session, now time.Time) error {
	switch {
	case !validUUID(s.UserID):
		return errors.New("user id is not a uuid")
	case s.RefreshToken == "":
		return errors.New("no refresh token")
	case s.RefreshExpiresAt.Unix() <= 0:
		return errors.New("no refresh token expiry")
	case s.CreatedAt.Unix() <= 0:
		return errors.New("no creation time")
	case !now.Before(s.CreatedAt.Add(AbsoluteTimeout)):
		return errors.New("absolute expiry reached")
	case now.Add(time.Second).After(s.IdleExpiresAt):
		// less than a second left would give Max-Age=0, which deletes the cookie
		return errors.New("idle expiry reached")
	}
	return nil
}

// limitProfile truncates the name to maxNameRunes and drops an email over maxEmailBytes.
// the picture is kept here; writeSession drops it only when the cookie would not fit.
func limitProfile(p Profile) Profile {
	if utf8.RuneCountInString(p.Name) > maxNameRunes {
		p.Name = string([]rune(p.Name)[:maxNameRunes])
	}
	if len(p.Email) > maxEmailBytes {
		p.Email = ""
	}
	return p
}

// validUUID reports whether s is a uuid in the canonical 8-4-4-4-12 form, in any letter case.
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHex(s[i]) {
				return false
			}
		}
	}
	return true
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// unixTime converts unix seconds to a UTC time, the only form session times take.
func unixTime(sec int64) time.Time {
	return time.Unix(sec, 0).UTC()
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
