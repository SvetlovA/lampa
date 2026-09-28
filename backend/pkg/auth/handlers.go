package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/SvetlovA/lampa/backend/pkg/api"
)

// auth routes, mounted by pkg/api next to the user-data routes.
const (
	SessionRoute      = "/api/v1/session"
	LoginRoute        = "/api/v1/auth/login"
	CallbackRoute     = LoginPath
	DeviceStartRoute  = DevicePath + "/start"
	DevicePollRoute   = DevicePath + "/poll"
	LogoutRoute       = "/api/v1/auth/logout"
	loginTimeout      = 10 * time.Minute // lifetime of a lampa_login cookie
	slowDownStep      = 5 * time.Second  // interval increase on slow_down, RFC 8628 §3.5
	maxReturnPathSize = 1024             // a longer return path falls back to "/"
)

// login outcome fragments the add-on reads and removes from the page url.
const (
	loginOKFragment     = "#svtlv-login=ok"
	loginFailedLocation = "/#svtlv-login=failed"
)

// keycloakClient is the part of Keycloak the handlers use.
type keycloakClient interface {
	revalidator
	AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error)
	Exchange(ctx context.Context, code, verifier, nonce string) (Profile, Tokens, error)
	StartDevice(ctx context.Context) (DeviceStart, error)
	PollDevice(ctx context.Context, deviceCode string) (DeviceResult, error)
}

// loginState is the sealed content of the lampa_login cookie.
type loginState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Return   string `json:"return"`
}

// deviceState is the sealed content of the lampa_device cookie. the device code never leaves the
// server any other way.
type deviceState struct {
	DeviceCode string `json:"device_code"`
	Interval   int64  `json:"interval"`   // seconds between polls
	ExpiresAt  int64  `json:"expires_at"` // unix seconds, kept when the cookie is re-sealed
}

// userJSON is the profile part of session and device poll responses.
type userJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Picture string `json:"picture"`
}

// sessionJSON answers GET /api/v1/session and a successful device poll.
type sessionJSON struct {
	Authenticated bool      `json:"authenticated"`
	User          *userJSON `json:"user,omitempty"`
}

// deviceStartJSON answers POST /api/v1/auth/device/start.
type deviceStartJSON struct {
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

// devicePendingJSON answers a device poll the user has not approved yet.
type devicePendingJSON struct {
	Status   string `json:"status"` // pending or slow_down
	Interval int64  `json:"interval"`
}

// Handlers serves the session, login, device login and logout routes. like the authenticator it
// keeps no state and never touches the database: everything lives in sealed cookies.
type Handlers struct {
	cookies  *Cookies
	keycloak keycloakClient
	logger   api.Logger
	now      func() time.Time
	mux      *http.ServeMux
}

// NewHandlers returns the auth handlers over cookies and keycloak.
func NewHandlers(cookies *Cookies, keycloak *Keycloak, logger api.Logger) *Handlers {
	return newHandlers(cookies, keycloak, logger, time.Now)
}

func newHandlers(cookies *Cookies, keycloak keycloakClient, logger api.Logger, now func() time.Time) *Handlers {
	h := &Handlers{cookies: cookies, keycloak: keycloak, logger: logger, now: now}
	h.mux = h.routes()
	return h
}

// ServeHTTP routes r to its auth handler; unknown paths and methods answer in the api json shape.
func (h *Handlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handlers) routes() *http.ServeMux {
	mux := http.NewServeMux()
	get := func(path string, fn http.HandlerFunc) {
		mux.HandleFunc("GET "+path, fn)
		// a "GET" pattern also matches HEAD; HEAD must not start a login or slide a session
		mux.HandleFunc("HEAD "+path, methodNotAllowed(http.MethodGet))
		mux.HandleFunc(path, methodNotAllowed(http.MethodGet))
	}
	post := func(path string, fn http.HandlerFunc) {
		mux.HandleFunc("POST "+path, fn)
		mux.HandleFunc(path, methodNotAllowed(http.MethodPost))
	}
	get(SessionRoute, h.session)
	get(LoginRoute, h.login)
	get(CallbackRoute, h.callback)
	post(DeviceStartRoute, h.deviceStart)
	post(DevicePollRoute, h.devicePoll)
	post(LogoutRoute, h.logout)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		api.WriteError(w, http.StatusNotFound, "not_found", "not found")
	})
	return mux
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		api.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// session answers whether the request is signed in, always with 200. a valid session is
// revalidated like the authenticator does and then renewed: this is the only route sliding the
// idle expiry. an unusable or revoked cookie is cleared and the answer is anonymous.
func (h *Handlers) session(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	s, err := h.cookies.OpenSession(r, now)
	if err != nil {
		if _, cerr := r.Cookie(SessionCookie); cerr == nil {
			h.cookies.ClearSession(w)
		}
		api.WriteJSON(w, http.StatusOK, sessionJSON{})
		return
	}
	tokens, refreshed, err := revalidate(r.Context(), h.keycloak, h.logger, s, now)
	if err != nil {
		h.cookies.ClearSession(w)
		api.WriteJSON(w, http.StatusOK, sessionJSON{})
		return
	}
	if refreshed {
		next := s
		next.Tokens, next.RefreshedAt = tokens, unixTime(now.Unix())
		renewed, rerr := h.cookies.RenewSession(w, next, now)
		if rerr == nil {
			api.WriteJSON(w, http.StatusOK, signedIn(renewed.Profile))
			return
		}
		// nothing was written: the old refresh token stays in use
		h.logger.Printf("[WARN] refreshed session not written, session kept: %v", rerr)
	}
	if renewed, rerr := h.cookies.RenewSession(w, s, now); rerr != nil {
		// the old cookie stays valid until its own idle expiry
		h.logger.Printf("[WARN] session not renewed: %v", rerr)
	} else {
		s = renewed
	}
	api.WriteJSON(w, http.StatusOK, signedIn(s.Profile))
}

// login starts an authorization code login with PKCE: the state, nonce, verifier and return
// path are sealed into lampa_login and the browser is sent to keycloak.
func (h *Handlers) login(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	st := loginState{
		State:    rand.Text(),
		Nonce:    rand.Text(),
		Verifier: oauth2.GenerateVerifier(),
		Return:   returnPath(r.URL.Query().Get("return")),
	}
	target, err := h.keycloak.AuthCodeURL(r.Context(), st.State, st.Nonce, st.Verifier)
	if err != nil {
		h.logger.Printf("[WARN] login: keycloak unavailable: %v", err)
		redirect(w, loginFailedLocation)
		return
	}
	value, err := h.cookies.sealer.Seal(PurposeLogin, st, now.Add(loginTimeout))
	if err != nil {
		h.logger.Printf("[WARN] login: seal state: %v", err)
		redirect(w, loginFailedLocation)
		return
	}
	http.SetCookie(w, h.cookies.newCookie(LoginCookie, LoginPath, value, int(loginTimeout/time.Second)))
	redirect(w, target)
}

// callback finishes an authorization code login. every failure clears lampa_login and sends the
// browser to the failure fragment, never to a json page.
func (h *Handlers) callback(w http.ResponseWriter, r *http.Request) {
	ret, err := h.finishLogin(w, r)
	http.SetCookie(w, h.cookies.newCookie(LoginCookie, LoginPath, "", -1))
	if err != nil {
		h.logger.Printf("[WARN] login callback failed: %v", err)
		redirect(w, loginFailedLocation)
		return
	}
	redirect(w, ret+loginOKFragment)
}

// finishLogin checks the callback of r against lampa_login, redeems the code and writes the
// session cookie. it returns the return path of the login.
func (h *Handlers) finishLogin(w http.ResponseWriter, r *http.Request) (string, error) {
	now := h.now()
	cookie, err := r.Cookie(LoginCookie)
	if err != nil {
		return "", errors.New("no login cookie")
	}
	var st loginState
	if oerr := h.cookies.sealer.Open(PurposeLogin, cookie.Value, now, &st); oerr != nil {
		return "", oerr
	}
	q := r.URL.Query()
	switch {
	case q.Get("error") != "":
		// the error code is request input, so it is not logged
		return "", errors.New("keycloak returned an error")
	case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1:
		return "", errors.New("state mismatch")
	case q.Get("code") == "":
		return "", errors.New("no code")
	}
	profile, tokens, err := h.keycloak.Exchange(r.Context(), q.Get("code"), st.Verifier, st.Nonce)
	if err != nil {
		return "", fmt.Errorf("exchange: %w", err)
	}
	if _, err = h.cookies.IssueSession(w, profile, tokens, now); err != nil {
		return "", err
	}
	h.cookies.ClearLogoutMark(w, r)
	return returnPath(st.Return), nil
}

// deviceStart starts a device login for a tv. the device code is sealed into lampa_device; the
// response carries only what the user needs to approve the login on another device.
func (h *Handlers) deviceStart(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ds, err := h.keycloak.StartDevice(r.Context())
	if err != nil {
		h.logger.Printf("[WARN] device login: keycloak unavailable: %v", err)
		api.WriteError(w, http.StatusServiceUnavailable, "keycloak_unavailable", "sign-in is unavailable")
		return
	}
	expiresIn := int64(ds.ExpiresIn / time.Second)
	st := deviceState{DeviceCode: ds.DeviceCode, Interval: int64(ds.Interval / time.Second), ExpiresAt: now.Unix() + expiresIn}
	if !h.setDeviceCookie(w, st, now) {
		api.WriteError(w, http.StatusServiceUnavailable, "keycloak_unavailable", "sign-in is unavailable")
		return
	}
	complete := ds.VerificationURIComplete
	if complete == "" {
		complete = ds.VerificationURI
	}
	api.WriteJSON(w, http.StatusOK, deviceStartJSON{
		UserCode:                ds.UserCode,
		VerificationURI:         ds.VerificationURI,
		VerificationURIComplete: complete,
		ExpiresIn:               expiresIn,
		Interval:                st.Interval,
	})
}

// devicePoll makes one keycloak token request for the device login in lampa_device. an approved
// login clears lampa_device and writes the session cookie.
func (h *Handlers) devicePoll(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	cookie, err := r.Cookie(DeviceCookie)
	var st deviceState
	if err == nil {
		err = h.cookies.sealer.Open(PurposeDevice, cookie.Value, now, &st)
	}
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "no_device_login", "no device login in progress")
		return
	}
	res, err := h.keycloak.PollDevice(r.Context(), st.DeviceCode)
	if err != nil {
		h.logger.Printf("[WARN] device login: poll failed: %v", err)
		api.WriteError(w, http.StatusServiceUnavailable, "keycloak_unavailable", "sign-in is unavailable")
		return
	}
	switch res.Status {
	case DevicePending:
		api.WriteJSON(w, http.StatusAccepted, devicePendingJSON{Status: "pending", Interval: st.Interval})
	case DeviceSlowDown:
		st.Interval += int64(slowDownStep / time.Second)
		h.setDeviceCookie(w, st, now) // on failure the old interval stays in the cookie
		api.WriteJSON(w, http.StatusAccepted, devicePendingJSON{Status: "slow_down", Interval: st.Interval})
	case DeviceExpired:
		h.clearDeviceCookie(w)
		api.WriteError(w, http.StatusGone, "expired", "device login expired")
	case DeviceDenied:
		h.clearDeviceCookie(w)
		api.WriteError(w, http.StatusForbidden, "access_denied", "device login denied")
	case DeviceAuthorized:
		h.clearDeviceCookie(w)
		s, err := h.cookies.IssueSession(w, res.Profile, res.Tokens, now)
		if err != nil {
			h.logger.Printf("[WARN] device login: session not written: %v", err)
			api.WriteError(w, http.StatusInternalServerError, "login_failed", "sign-in failed")
			return
		}
		h.cookies.ClearLogoutMark(w, r)
		api.WriteJSON(w, http.StatusOK, signedIn(s.Profile))
	default:
		h.logger.Printf("[WARN] device login: unknown poll status %d", res.Status)
		api.WriteError(w, http.StatusServiceUnavailable, "keycloak_unavailable", "sign-in is unavailable")
	}
}

// logout clears the session cookie of this device and marks it signed out, so a renewed cookie
// from a request still in flight cannot sign it back in. it answers 204 also when already signed
// out; the keycloak session is left alone (design §5.3).
func (h *Handlers) logout(w http.ResponseWriter, _ *http.Request) {
	h.cookies.EndSession(w, h.now())
	w.WriteHeader(http.StatusNoContent)
}

// setDeviceCookie seals st into lampa_device, expiring at st.ExpiresAt. it reports whether the
// cookie was written.
func (h *Handlers) setDeviceCookie(w http.ResponseWriter, st deviceState, now time.Time) bool {
	expiresAt := unixTime(st.ExpiresAt)
	maxAge := int(expiresAt.Sub(now) / time.Second)
	if maxAge <= 0 {
		return false
	}
	value, err := h.cookies.sealer.Seal(PurposeDevice, st, expiresAt)
	if err != nil {
		h.logger.Printf("[WARN] device login: seal state: %v", err)
		return false
	}
	http.SetCookie(w, h.cookies.newCookie(DeviceCookie, DevicePath, value, maxAge))
	return true
}

func (h *Handlers) clearDeviceCookie(w http.ResponseWriter) {
	http.SetCookie(w, h.cookies.newCookie(DeviceCookie, DevicePath, "", -1))
}

func signedIn(p Profile) sessionJSON {
	return sessionJSON{Authenticated: true, User: &userJSON{ID: p.UserID, Name: p.Name, Email: p.Email, Picture: p.Picture}}
}

// returnPath returns raw when it is a local absolute path, else "/". a scheme, a host
// ("//evil"), a backslash (browsers read "/\evil" as "//evil"), anything but printable ascii
// (browsers send the url percent-encoded) and a fragment are refused; the login outcome fragment
// is appended later.
func returnPath(raw string) string {
	if raw == "" || len(raw) > maxReturnPathSize || raw[0] != '/' || strings.HasPrefix(raw, "//") {
		return "/"
	}
	for _, c := range raw {
		if c == '\\' || c == '#' || c <= ' ' || c > '~' {
			return "/"
		}
	}
	return raw
}

// redirect sends a 302 to location without the html body http.Redirect adds.
func redirect(w http.ResponseWriter, location string) {
	h := w.Header()
	h.Set("Location", location)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
}
