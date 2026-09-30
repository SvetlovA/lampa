package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// keycloak call limits, design §5.3.1.
const (
	keycloakTimeout  = 3 * time.Second // one call, discovery included
	maxResponseBytes = 64 << 10        // a larger keycloak response is an error, never truncated
	maxRefreshExpiry = 3650 * 24 * time.Hour
	defaultInterval  = 5 * time.Second // device poll interval when keycloak sends none, RFC 8628 §3.2
)

// deviceGrantType is the RFC 8628 token request grant type.
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// errResponseTooLarge fails reading a keycloak response body over maxResponseBytes.
var errResponseTooLarge = fmt.Errorf("keycloak response over %d bytes", maxResponseBytes)

// Verdict is keycloak's answer on whether a refresh token is still good. it is only
// meaningful with a nil error: any failure to get an answer is an error, never a verdict.
type Verdict int

// verdicts.
const (
	VerdictUnknown Verdict = iota // returned together with an error
	VerdictActive                 // keycloak confirmed the session
	VerdictRevoked                // keycloak explicitly ended the session
)

// DeviceStatus is the outcome of one device token poll.
type DeviceStatus int

// device poll outcomes, RFC 8628 §3.5.
const (
	DevicePending    DeviceStatus = iota // authorization_pending: the user has not approved yet
	DeviceSlowDown                       // slow_down: poll less often
	DeviceExpired                        // expired_token: the device code expired
	DeviceDenied                         // access_denied: the user declined
	DeviceAuthorized                     // approved, Profile and Tokens are set
)

// DeviceResult is the outcome of PollDevice; Profile and Tokens are set only for DeviceAuthorized.
type DeviceResult struct {
	Status  DeviceStatus
	Profile Profile
	Tokens  Tokens
}

// DeviceStart is a started device login. DeviceCode and Verifier stay on the server.
type DeviceStart struct {
	DeviceCode              string
	Verifier                string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               time.Duration
	Interval                time.Duration
}

// KeycloakConfig is the confidential keycloak client lampa-api signs in with.
type KeycloakConfig struct {
	Issuer          string // realm issuer, equal to the iss keycloak issues
	ClientID        string
	ClientSecret    string
	RedirectURL     string // <PublicURL>/api/v1/auth/callback
	LogoutReturnURL string // <PublicURL>/, registered as a valid post-logout redirect URI
}

// Keycloak is the OIDC client for both login flows and session revalidation. discovery is lazy:
// construction never calls keycloak, the first use does, and a failed discovery is retried on
// the next use, so a keycloak outage never stops the api.
type Keycloak struct {
	cfg     KeycloakConfig
	client  *http.Client
	timeout time.Duration
	now     func() time.Time

	lock chan struct{} // held while discovering, so waiting callers still honor their context
	disc *discovery    // set once discovery succeeded
}

// discovery is everything derived from the issuer's discovery document.
type discovery struct {
	oauth         *oauth2.Config
	verifier      *oidc.IDTokenVerifier
	introspectURL string
	endSessionURL string
}

// NewKeycloak returns a client for cfg without contacting keycloak.
func NewKeycloak(cfg KeycloakConfig) *Keycloak {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Keycloak{
		cfg: cfg,
		client: &http.Client{
			Transport: limitTransport{base: transport},
			// backstop for the jwks fetch go-oidc runs on a background context
			Timeout:       keycloakTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		timeout: keycloakTimeout,
		now:     time.Now,
		lock:    make(chan struct{}, 1),
	}
}

// AuthCodeURL returns the keycloak authorization url for an authorization code login with an
// S256 PKCE challenge for verifier and the given state and nonce.
func (k *Keycloak) AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()
	d, err := k.discover(ctx)
	if err != nil {
		return "", err
	}
	return d.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), nil
}

// LogoutURL sends the browser through keycloak's RP-initiated logout, ending its shared realm
// SSO session before returning to Lampa. the client_id identifies the client when no ID token is
// retained in the session cookie.
func (k *Keycloak) LogoutURL(ctx context.Context) (string, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()
	d, err := k.discover(ctx)
	if err != nil {
		return "", err
	}
	if k.cfg.LogoutReturnURL == "" {
		return "", errors.New("keycloak logout: no return url")
	}
	u, err := k.logoutEndpoint(d)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", k.cfg.ClientID)
	q.Set("post_logout_redirect_uri", k.cfg.LogoutReturnURL)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Logout ends the Keycloak user session represented by this refresh token, including its other
// clients in the same browser session. The handler calls it only for browser sign-out.
func (k *Keycloak) Logout(ctx context.Context, refreshToken string) error {
	ctx, cancel := k.callContext(ctx)
	defer cancel()
	d, err := k.discover(ctx)
	if err != nil {
		return err
	}
	u, err := k.logoutEndpoint(d)
	if err != nil {
		return err
	}
	status, _, err := k.postForm(ctx, u.String(), url.Values{"refresh_token": {refreshToken}})
	if err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("logout: keycloak status %d", status)
	}
	return nil
}

// logoutEndpoint must stay on the issuer origin: the POST sends both the client secret and a
// refresh token, so a different discovery URL must never receive them.
func (k *Keycloak) logoutEndpoint(d *discovery) (*url.URL, error) {
	if d.endSessionURL == "" {
		return nil, errors.New("keycloak discovery: no end session endpoint")
	}
	u, err := url.Parse(d.endSessionURL)
	if err != nil {
		return nil, errors.New("keycloak discovery: invalid end session endpoint")
	}
	issuer, err := url.Parse(k.cfg.Issuer)
	if err != nil || u.Scheme != issuer.Scheme || u.Host != issuer.Host ||
		(u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, strings.TrimRight(issuer.Path, "/")+"/") {
		return nil, errors.New("keycloak discovery: invalid end session endpoint")
	}
	return u, nil
}

// Exchange redeems an authorization code, verifies the ID token and its nonce, and returns the
// user's profile with the refresh token. the access and ID tokens are discarded.
func (k *Keycloak) Exchange(ctx context.Context, code, verifier, nonce string) (Profile, Tokens, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()
	d, err := k.discover(ctx)
	if err != nil {
		return Profile{}, Tokens{}, err
	}
	tok, err := d.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Profile{}, Tokens{}, tokenError("exchange code", err)
	}
	rawIDToken, _ := tok.Extra("id_token").(string)
	return k.login(ctx, d, tokenResponse{
		IDToken:          rawIDToken,
		RefreshToken:     tok.RefreshToken,
		RefreshExpiresIn: tok.Extra("refresh_expires_in"),
	}, nonce)
}

// StartDevice starts a device login with S256 PKCE. keycloak authenticates confidential clients
// at its device endpoint, and oauth2.Config.DeviceAuth sends only client_id, so the secret is
// added explicitly.
func (k *Keycloak) StartDevice(ctx context.Context) (DeviceStart, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()
	d, err := k.discover(ctx)
	if err != nil {
		return DeviceStart{}, err
	}
	verifier := oauth2.GenerateVerifier()
	resp, err := d.oauth.DeviceAuth(ctx,
		oauth2.SetAuthURLParam("client_secret", k.cfg.ClientSecret),
		oauth2.S256ChallengeOption(verifier))
	if err != nil {
		return DeviceStart{}, tokenError("start device login", err)
	}
	if resp.DeviceCode == "" || resp.UserCode == "" || resp.VerificationURI == "" || resp.Expiry.IsZero() {
		return DeviceStart{}, errors.New("start device login: incomplete keycloak response")
	}
	expiresIn := time.Until(resp.Expiry).Round(time.Second)
	if expiresIn <= 0 {
		return DeviceStart{}, errors.New("start device login: device code already expired")
	}
	interval := time.Duration(resp.Interval) * time.Second
	if interval <= 0 {
		interval = defaultInterval
	}
	return DeviceStart{
		DeviceCode:              resp.DeviceCode,
		Verifier:                verifier,
		UserCode:                resp.UserCode,
		VerificationURI:         resp.VerificationURI,
		VerificationURIComplete: resp.VerificationURIComplete,
		ExpiresIn:               expiresIn,
		Interval:                interval,
	}, nil
}

// PollDevice makes one device token request. oauth2.Config.DeviceAccessToken is not used: it
// sleeps an interval before its first request and blocks until the login ends. the pending,
// slow-down, expired and denied answers are results, not errors.
func (k *Keycloak) PollDevice(ctx context.Context, deviceCode, verifier string) (DeviceResult, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()
	d, err := k.discover(ctx)
	if err != nil {
		return DeviceResult{}, err
	}
	status, body, err := k.postForm(ctx, d.oauth.Endpoint.TokenURL, url.Values{
		"grant_type":    {deviceGrantType},
		"device_code":   {deviceCode},
		"code_verifier": {verifier},
	})
	if err != nil {
		return DeviceResult{}, fmt.Errorf("poll device login: %w", err)
	}
	var resp tokenResponse
	if json.Unmarshal(body, &resp) != nil {
		return DeviceResult{}, fmt.Errorf("poll device login: status %d, malformed body", status)
	}
	if status == http.StatusBadRequest {
		switch resp.Error {
		case "authorization_pending":
			return DeviceResult{Status: DevicePending}, nil
		case "slow_down":
			return DeviceResult{Status: DeviceSlowDown}, nil
		case "expired_token":
			return DeviceResult{Status: DeviceExpired}, nil
		case "access_denied":
			return DeviceResult{Status: DeviceDenied}, nil
		}
	}
	if status != http.StatusOK || resp.Error != "" {
		return DeviceResult{}, fmt.Errorf("poll device login: status %d, error %.64q", status, resp.Error)
	}
	// a device-flow ID token carries no nonce
	profile, tokens, err := k.login(ctx, d, resp, "")
	if err != nil {
		return DeviceResult{}, err
	}
	return DeviceResult{Status: DeviceAuthorized, Profile: profile, Tokens: tokens}, nil
}

// Refresh runs a refresh_token grant, which also validates the session and slides keycloak's
// SSO idle timer. only 400 invalid_grant is VerdictRevoked; every other failure is an error.
func (k *Keycloak) Refresh(ctx context.Context, refreshToken string) (Tokens, Verdict, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()
	d, err := k.discover(ctx)
	if err != nil {
		return Tokens{}, VerdictUnknown, err
	}
	// a past expiry makes the token source refresh instead of returning the token as is
	tok, err := d.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Unix(1, 0)}).Token()
	if err != nil {
		if re, ok := errors.AsType[*oauth2.RetrieveError](err); ok && re.Response != nil &&
			re.Response.StatusCode == http.StatusBadRequest && re.ErrorCode == "invalid_grant" {
			return Tokens{}, VerdictRevoked, nil
		}
		return Tokens{}, VerdictUnknown, tokenError("refresh", err)
	}
	// the library falls back to the old refresh token when the response has none, so read the
	// raw response field
	newToken, _ := tok.Extra("refresh_token").(string)
	if newToken == "" {
		return Tokens{}, VerdictUnknown, errors.New("refresh: no refresh token in keycloak response")
	}
	expiresAt, err := k.refreshExpiry(tok.Extra("refresh_expires_in"))
	if err != nil {
		return Tokens{}, VerdictUnknown, fmt.Errorf("refresh: %w", err)
	}
	return Tokens{RefreshToken: newToken, RefreshExpiresAt: expiresAt}, VerdictActive, nil
}

// Introspect asks keycloak whether refreshToken is still active (RFC 7662). it neither slides
// the SSO idle timer nor uses up a token reuse. only a 200 with a JSON boolean active is a
// verdict; go's zero false must never sign anyone out.
func (k *Keycloak) Introspect(ctx context.Context, refreshToken string) (Verdict, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()
	d, err := k.discover(ctx)
	if err != nil {
		return VerdictUnknown, err
	}
	if d.introspectURL == "" {
		return VerdictUnknown, errors.New("introspect: keycloak advertises no introspection endpoint")
	}
	// without the hint keycloak introspects the value as an access token
	status, body, err := k.postForm(ctx, d.introspectURL, url.Values{
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
	})
	if err != nil {
		return VerdictUnknown, fmt.Errorf("introspect: %w", err)
	}
	if status != http.StatusOK {
		return VerdictUnknown, fmt.Errorf("introspect: status %d", status)
	}
	var resp struct {
		Active *bool `json:"active"`
	}
	if json.Unmarshal(body, &resp) != nil || resp.Active == nil {
		return VerdictUnknown, errors.New("introspect: no boolean active in keycloak response")
	}
	if *resp.Active {
		return VerdictActive, nil
	}
	return VerdictRevoked, nil
}

// tokenResponse is the part of a keycloak token response lampa-api reads. refresh_expires_in
// is kept undecoded: a JSON number arrives as float64 and is validated by refreshExpiry.
type tokenResponse struct {
	Error            string `json:"error"`
	IDToken          string `json:"id_token"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn any    `json:"refresh_expires_in"`
}

// idClaims are the ID token claims copied into the profile.
type idClaims struct {
	Subject           string `json:"sub"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	Picture           string `json:"picture"`
}

// login verifies the ID token of a login token response, and its nonce unless nonce is empty,
// and returns the profile and the refresh token.
func (k *Keycloak) login(ctx context.Context, d *discovery, resp tokenResponse, nonce string) (Profile, Tokens, error) {
	if resp.IDToken == "" {
		return Profile{}, Tokens{}, errors.New("login: no id token in keycloak response")
	}
	idToken, err := d.verifier.Verify(ctx, resp.IDToken)
	if err != nil {
		return Profile{}, Tokens{}, fmt.Errorf("login: verify id token: %w", err)
	}
	if nonce != "" && idToken.Nonce != nonce {
		return Profile{}, Tokens{}, errors.New("login: id token nonce mismatch")
	}
	var claims idClaims
	if err = idToken.Claims(&claims); err != nil {
		return Profile{}, Tokens{}, fmt.Errorf("login: id token claims: %w", err)
	}
	profile, err := profileFromClaims(claims)
	if err != nil {
		return Profile{}, Tokens{}, fmt.Errorf("login: %w", err)
	}
	if resp.RefreshToken == "" {
		return Profile{}, Tokens{}, errors.New("login: no refresh token in keycloak response")
	}
	expiresAt, err := k.refreshExpiry(resp.RefreshExpiresIn)
	if err != nil {
		return Profile{}, Tokens{}, fmt.Errorf("login: %w", err)
	}
	return profile, Tokens{RefreshToken: resp.RefreshToken, RefreshExpiresAt: expiresAt}, nil
}

// profileFromClaims builds a profile: sub must be a uuid, the name falls back to
// preferred_username then email, and the picture is kept only as an absolute https url.
func profileFromClaims(c idClaims) (Profile, error) {
	if !validUUID(c.Subject) {
		return Profile{}, errors.New("id token sub is not a uuid")
	}
	name := c.Name
	if name == "" {
		name = c.PreferredUsername
	}
	if name == "" {
		name = c.Email
	}
	picture := ""
	if u, err := url.Parse(c.Picture); err == nil && u.Scheme == "https" && u.Host != "" {
		picture = c.Picture
	}
	return Profile{UserID: c.Subject, Name: name, Email: c.Email, Picture: picture}, nil
}

// refreshExpiry converts refresh_expires_in, a JSON number of seconds, to an absolute time. it
// must be finite, positive, integral and at most maxRefreshExpiry.
func (k *Keycloak) refreshExpiry(v any) (time.Time, error) {
	sec, ok := v.(float64)
	if !ok || math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 || sec != math.Trunc(sec) ||
		sec > maxRefreshExpiry.Seconds() {
		return time.Time{}, errors.New("invalid refresh_expires_in in keycloak response")
	}
	return k.now().Add(time.Duration(sec) * time.Second), nil
}

// discover returns the cached discovery, running it first if needed. one caller discovers at a
// time; the others wait for it or for their own context.
func (k *Keycloak) discover(ctx context.Context) (*discovery, error) {
	select {
	case k.lock <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("keycloak discovery: %w", ctx.Err())
	}
	defer func() { <-k.lock }()
	if k.disc != nil {
		return k.disc, nil
	}
	provider, err := oidc.NewProvider(ctx, k.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("keycloak discovery: %w", err)
	}
	var extra struct {
		IntrospectionEndpoint string `json:"introspection_endpoint"`
		EndSessionEndpoint    string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&extra); err != nil {
		return nil, fmt.Errorf("keycloak discovery: %w", err)
	}
	endpoint := provider.Endpoint()
	if endpoint.TokenURL == "" {
		return nil, errors.New("keycloak discovery: no token endpoint")
	}
	// fixed style: auto-detection retries a failed request with the other style, which would
	// make two keycloak calls where one is allowed
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	k.disc = &discovery{
		oauth: &oauth2.Config{
			ClientID:     k.cfg.ClientID,
			ClientSecret: k.cfg.ClientSecret,
			Endpoint:     endpoint,
			RedirectURL:  k.cfg.RedirectURL,
			// never offline_access: the refresh token must end with the keycloak session
			Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier:      provider.Verifier(&oidc.Config{ClientID: k.cfg.ClientID, Now: k.now}),
		introspectURL: extra.IntrospectionEndpoint,
		endSessionURL: extra.EndSessionEndpoint,
	}
	return k.disc, nil
}

// callContext bounds one keycloak call and hands the shared http client to go-oidc and oauth2.
func (k *Keycloak) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	return oidc.ClientContext(ctx, k.client), cancel
}

// postForm sends a client-authenticated form POST and returns the status and body. client
// credentials go in Basic auth, form-encoded first as RFC 6749 §2.3.1 and oauth2 do.
func (k *Keycloak) postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(k.cfg.ClientID), url.QueryEscape(k.cfg.ClientSecret))
	resp, err := k.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, body, nil
}

// tokenError describes a failed oauth2 call without response data: RetrieveError's own message
// embeds the response body and error description.
func tokenError(op string, err error) error {
	if re, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		return fmt.Errorf("%s: keycloak status %d, error %.64q", op, status, re.ErrorCode)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// limitTransport fails any response body over maxResponseBytes. oauth2 reads through its own
// 1 MiB LimitReader, which truncates instead of failing.
type limitTransport struct {
	base http.RoundTripper
}

// RoundTrip sends req and wraps the response body in the size limit.
func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err //nolint:wrapcheck // a RoundTripper returns the transport error as is
	}
	resp.Body = &limitedBody{body: resp.Body, left: maxResponseBytes}
	return resp, nil
}

// limitedBody reads at most left bytes and fails with errResponseTooLarge past that.
type limitedBody struct {
	body io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.body.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n, errResponseTooLarge
	}
	return n, err //nolint:wrapcheck // io.EOF must reach the reader unwrapped
}

func (b *limitedBody) Close() error {
	return b.body.Close() //nolint:wrapcheck // closing passes the underlying error through
}
