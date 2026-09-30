package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

const (
	testClientID     = "svtlv-lampa"
	testClientSecret = "s3cret"
	testKeyID        = "k1"
	testRealmPath    = "/realms/svtlv"
	testOIDCPath     = testRealmPath + "/protocol/openid-connect"
)

// testSigningKey signs the fake keycloak's ID tokens; testOtherKey is unknown to its jwks.
var testSigningKey, testOtherKey = mustRSAKey(), mustRSAKey()

func mustRSAKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}

// fakeRequest is one request the fake keycloak received.
type fakeRequest struct {
	Path string
	Form url.Values
}

// fakeKeycloak serves discovery, jwks, device authorization, token and introspection endpoints.
// like a confidential client in keycloak, it rejects device, token and introspection requests
// without the client secret, and it records every request.
type fakeKeycloak struct {
	t   *testing.T
	srv *httptest.Server

	down            atomic.Bool // discovery answers 503
	noIntrospection atomic.Bool // discovery advertises no introspection endpoint

	mu         sync.Mutex
	requests   []fakeRequest
	token      http.HandlerFunc // token endpoint after client authentication
	introspect http.HandlerFunc // introspection endpoint after client authentication
	device     http.HandlerFunc // device authorization endpoint after client authentication
}

func newFakeKeycloak(t *testing.T) *fakeKeycloak {
	t.Helper()
	f := &fakeKeycloak{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKeycloak) issuer() string { return f.srv.URL + testRealmPath }

func (f *fakeKeycloak) setToken(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = h
}

func (f *fakeKeycloak) setIntrospect(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.introspect = h
}

func (f *fakeKeycloak) setDevice(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device = h
}

// recorded returns the requests received on path.
func (f *fakeKeycloak) recorded(path string) []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRequest
	for _, r := range f.requests {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeKeycloak) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeKeycloak) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, fakeRequest{Path: r.URL.Path, Form: r.PostForm})
	token, introspect, device := f.token, f.introspect, f.device
	f.mu.Unlock()

	switch r.URL.Path {
	case testRealmPath + "/.well-known/openid-configuration":
		if f.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		doc := map[string]any{
			"issuer":                                f.issuer(),
			"authorization_endpoint":                f.srv.URL + testOIDCPath + "/auth",
			"token_endpoint":                        f.srv.URL + testOIDCPath + "/token",
			"device_authorization_endpoint":         f.srv.URL + testOIDCPath + "/auth/device",
			"introspection_endpoint":                f.srv.URL + testOIDCPath + "/token/introspect",
			"end_session_endpoint":                  f.srv.URL + testOIDCPath + "/logout",
			"jwks_uri":                              f.srv.URL + testOIDCPath + "/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		if f.noIntrospection.Load() {
			delete(doc, "introspection_endpoint")
		}
		writeTestJSON(w, http.StatusOK, doc)
	case testOIDCPath + "/certs":
		writeTestJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": testKeyID, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(testSigningKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(testSigningKey.E)).Bytes()),
		}}})
	case testOIDCPath + "/token":
		f.authenticated(w, r, token)
	case testOIDCPath + "/token/introspect":
		f.authenticated(w, r, introspect)
	case testOIDCPath + "/auth/device":
		f.authenticated(w, r, device)
	case testOIDCPath + "/logout":
		f.authenticated(w, r, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	default:
		http.NotFound(w, r)
	}
}

// authenticated runs h only for a request carrying the client secret, in Basic auth or the form.
func (f *fakeKeycloak) authenticated(w http.ResponseWriter, r *http.Request, h http.HandlerFunc) {
	user, pass, ok := r.BasicAuth()
	basic := ok && user == testClientID && pass == testClientSecret
	form := r.PostForm.Get("client_id") == testClientID && r.PostForm.Get("client_secret") == testClientSecret
	if !basic && !form {
		writeTestJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	if h == nil {
		http.Error(w, "no handler", http.StatusInternalServerError)
		return
	}
	h(w, r)
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// respond returns a handler writing v as JSON with status.
func respond(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeTestJSON(w, status, v) }
}

// idToken signs an ID token for the fake keycloak with claims over the defaults.
func (f *fakeKeycloak) idToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	return signIDToken(t, testSigningKey, f.idClaims(claims))
}

func (f *fakeKeycloak) idClaims(over map[string]any) map[string]any {
	c := map[string]any{
		"iss":                f.issuer(),
		"aud":                testClientID,
		"sub":                testUserID,
		"exp":                testNow.Add(5 * time.Minute).Unix(),
		"iat":                testNow.Unix(),
		"name":               "Artem",
		"preferred_username": "artem",
		"email":              "artem@example.com",
		"picture":            "https://example.com/a.png",
	}
	for k, v := range over {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

func signIDToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(claims)
	require.NoError(t, err)
	return oidctest.SignIDToken(key, testKeyID, oidc.RS256, string(raw))
}

// loginResponse is a keycloak token response for a login.
func loginResponse(idToken string) map[string]any {
	return map[string]any{
		"access_token":       "access",
		"token_type":         "Bearer",
		"expires_in":         300,
		"id_token":           idToken,
		"refresh_token":      "refresh-1",
		"refresh_expires_in": 1800,
	}
}

func newTestKeycloak(t *testing.T, f *fakeKeycloak) *Keycloak {
	t.Helper()
	k := NewKeycloak(KeycloakConfig{
		Issuer:          f.issuer(),
		ClientID:        testClientID,
		ClientSecret:    testClientSecret,
		RedirectURL:     "http://100.64.0.1:8092/api/v1/auth/callback",
		LogoutReturnURL: "http://100.64.0.1:8092/",
	})
	k.now = func() time.Time { return testNow }
	return k
}

func TestKeycloak_lazyDiscovery(t *testing.T) {
	f := newFakeKeycloak(t)
	f.down.Store(true)
	k := newTestKeycloak(t, f)
	assert.Zero(t, f.total(), "construction never calls keycloak")

	_, err := k.AuthCodeURL(t.Context(), "state", "nonce", "verifier")
	require.Error(t, err)

	f.down.Store(false)
	_, err = k.AuthCodeURL(t.Context(), "state", "nonce", "verifier")
	require.NoError(t, err)
	_, err = k.AuthCodeURL(t.Context(), "state", "nonce", "verifier")
	require.NoError(t, err)
	assert.Len(t, f.recorded(testRealmPath+"/.well-known/openid-configuration"), 2, "cached after the first success")
}

func TestKeycloak_discoveryIssuerMismatch(t *testing.T) {
	f := newFakeKeycloak(t)
	k := NewKeycloak(KeycloakConfig{Issuer: f.issuer() + "/", ClientID: testClientID, ClientSecret: testClientSecret})
	_, err := k.AuthCodeURL(t.Context(), "state", "nonce", "verifier")
	require.Error(t, err)
}

func TestKeycloak_AuthCodeURL(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	verifier := oauth2.GenerateVerifier()

	raw, err := k.AuthCodeURL(t.Context(), "st", "nc", verifier)
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, f.srv.URL+testOIDCPath+"/auth", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, testClientID, q.Get("client_id"))
	assert.Equal(t, "http://100.64.0.1:8092/api/v1/auth/callback", q.Get("redirect_uri"))
	assert.Equal(t, "openid profile email", q.Get("scope"))
	assert.NotContains(t, q.Get("scope"), "offline_access")
	assert.Equal(t, "st", q.Get("state"))
	assert.Equal(t, "nc", q.Get("nonce"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, oauth2.S256ChallengeFromVerifier(verifier), q.Get("code_challenge"))
	assert.Empty(t, q.Get("client_secret"))
	assert.Empty(t, q.Get("prompt"))
}

func TestKeycloak_Logout(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	require.NoError(t, k.Logout(t.Context(), "refresh-1"))
	reqs := f.recorded(testOIDCPath + "/logout")
	require.Len(t, reqs, 1)
	assert.Equal(t, "refresh-1", reqs[0].Form.Get("refresh_token"))
	assert.Empty(t, reqs[0].Form.Get("client_secret"), "client credentials stay in Basic auth")

	raw, err := k.LogoutURL(t.Context())
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, f.srv.URL+testOIDCPath+"/logout", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, testClientID, u.Query().Get("client_id"))
	assert.Equal(t, "http://100.64.0.1:8092/", u.Query().Get("post_logout_redirect_uri"))
	assert.Empty(t, u.Query().Get("client_secret"))
	assert.Empty(t, u.Query().Get("refresh_token"))
}

func TestKeycloak_LogoutRejectsForeignEndpoint(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	_, err := k.AuthCodeURL(t.Context(), "s", "n", "v")
	require.NoError(t, err)
	k.disc.endSessionURL = "https://other.example/realms/svtlv/protocol/openid-connect/logout"

	require.ErrorContains(t, k.Logout(t.Context(), "refresh-1"), "invalid end session endpoint")
	_, err = k.LogoutURL(t.Context())
	require.ErrorContains(t, err, "invalid end session endpoint")
	assert.Empty(t, f.recorded(testOIDCPath+"/logout"))
}

func TestKeycloak_Exchange(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	f.setToken(respond(http.StatusOK, loginResponse(f.idToken(t, map[string]any{"nonce": "nc"}))))

	profile, tokens, err := k.Exchange(t.Context(), "the-code", "the-verifier", "nc")
	require.NoError(t, err)
	assert.Equal(t, testProfile, profile)
	assert.Equal(t, Tokens{RefreshToken: "refresh-1", RefreshExpiresAt: testNow.Add(1800 * time.Second)}, tokens)

	reqs := f.recorded(testOIDCPath + "/token")
	require.Len(t, reqs, 1)
	assert.Equal(t, "authorization_code", reqs[0].Form.Get("grant_type"))
	assert.Equal(t, "the-code", reqs[0].Form.Get("code"))
	assert.Equal(t, "the-verifier", reqs[0].Form.Get("code_verifier"))
	assert.Equal(t, "http://100.64.0.1:8092/api/v1/auth/callback", reqs[0].Form.Get("redirect_uri"))
	assert.Empty(t, reqs[0].Form.Get("client_secret"), "the secret goes in basic auth")
}

func TestKeycloak_ExchangeErrors(t *testing.T) {
	f := newFakeKeycloak(t)
	tests := []struct {
		name string
		resp func() (int, any)
	}{
		{"bad signature", func() (int, any) {
			return http.StatusOK, loginResponse(signIDToken(t, testOtherKey, f.idClaims(map[string]any{"nonce": "nc"})))
		}},
		{"wrong audience", func() (int, any) {
			return http.StatusOK, loginResponse(f.idToken(t, map[string]any{"nonce": "nc", "aud": "other"}))
		}},
		{"wrong issuer", func() (int, any) {
			return http.StatusOK, loginResponse(f.idToken(t, map[string]any{"nonce": "nc", "iss": "http://evil"}))
		}},
		{"expired id token", func() (int, any) {
			return http.StatusOK, loginResponse(f.idToken(t, map[string]any{"nonce": "nc", "exp": testNow.Add(-time.Hour).Unix()}))
		}},
		{"nonce mismatch", func() (int, any) {
			return http.StatusOK, loginResponse(f.idToken(t, map[string]any{"nonce": "other"}))
		}},
		{"no nonce", func() (int, any) {
			return http.StatusOK, loginResponse(f.idToken(t, nil))
		}},
		{"non-uuid sub", func() (int, any) {
			return http.StatusOK, loginResponse(f.idToken(t, map[string]any{"nonce": "nc", "sub": "artem"}))
		}},
		{"no id token", func() (int, any) {
			resp := loginResponse("")
			delete(resp, "id_token")
			return http.StatusOK, resp
		}},
		{"no refresh token", func() (int, any) {
			resp := loginResponse(f.idToken(t, map[string]any{"nonce": "nc"}))
			delete(resp, "refresh_token")
			return http.StatusOK, resp
		}},
		{"no refresh expiry", func() (int, any) {
			resp := loginResponse(f.idToken(t, map[string]any{"nonce": "nc"}))
			delete(resp, "refresh_expires_in")
			return http.StatusOK, resp
		}},
		{"invalid code", func() (int, any) {
			return http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "Code not valid"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newTestKeycloak(t, f)
			status, body := tt.resp()
			f.setToken(respond(status, body))
			_, _, err := k.Exchange(t.Context(), "code", "verifier", "nc")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "Code not valid", "response data stays out of errors")
		})
	}
}

func TestKeycloak_StartDevice(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	f.setDevice(respond(http.StatusOK, map[string]any{
		"device_code":               "dev-code",
		"user_code":                 "ABCD-EFGH",
		"verification_uri":          "https://kc/device",
		"verification_uri_complete": "https://kc/device?user_code=ABCD-EFGH",
		"expires_in":                600,
		"interval":                  5,
	}))

	start, err := k.StartDevice(t.Context())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(start.Verifier), 43, "RFC 7636 verifier length")
	verifier := start.Verifier
	start.Verifier = ""
	assert.Equal(t, DeviceStart{
		DeviceCode:              "dev-code",
		UserCode:                "ABCD-EFGH",
		VerificationURI:         "https://kc/device",
		VerificationURIComplete: "https://kc/device?user_code=ABCD-EFGH",
		ExpiresIn:               600 * time.Second,
		Interval:                5 * time.Second,
	}, start)

	reqs := f.recorded(testOIDCPath + "/auth/device")
	require.Len(t, reqs, 1)
	assert.Equal(t, testClientSecret, reqs[0].Form.Get("client_secret"))
	assert.Equal(t, "openid profile email", reqs[0].Form.Get("scope"))
	assert.Equal(t, "S256", reqs[0].Form.Get("code_challenge_method"))
	assert.Equal(t, oauth2.S256ChallengeFromVerifier(verifier), reqs[0].Form.Get("code_challenge"))
	assert.Empty(t, reqs[0].Form.Get("code_verifier"), "the verifier is sent only to the token endpoint")
}

func TestKeycloak_StartDeviceErrors(t *testing.T) {
	f := newFakeKeycloak(t)
	tests := []struct {
		name   string
		status int
		body   any
	}{
		{"keycloak error", http.StatusBadRequest, map[string]string{"error": "unauthorized_client"}},
		{"server error", http.StatusInternalServerError, map[string]string{}},
		{"no device code", http.StatusOK, map[string]any{"user_code": "A", "verification_uri": "https://kc/device", "expires_in": 600}},
		{"no expiry", http.StatusOK, map[string]any{"device_code": "d", "user_code": "A", "verification_uri": "https://kc/device"}},
		{"no user code", http.StatusOK, map[string]any{"device_code": "d", "verification_uri": "https://kc/device", "expires_in": 600}},
		{"no verification uri", http.StatusOK, map[string]any{"device_code": "d", "user_code": "A", "expires_in": 600}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newTestKeycloak(t, f)
			f.setDevice(respond(tt.status, tt.body))
			_, err := k.StartDevice(t.Context())
			require.Error(t, err)
		})
	}

	t.Run("default interval", func(t *testing.T) {
		k := newTestKeycloak(t, f)
		f.setDevice(respond(http.StatusOK, map[string]any{
			"device_code": "d", "user_code": "A", "verification_uri": "https://kc/device", "expires_in": 600,
		}))
		start, err := k.StartDevice(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 5*time.Second, start.Interval)
	})
}

func TestKeycloak_PollDevice(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	// a device-flow ID token carries no nonce
	f.setToken(respond(http.StatusOK, loginResponse(f.idToken(t, nil))))

	res, err := k.PollDevice(t.Context(), "dev-code", "device-pkce-verifier")
	require.NoError(t, err)
	assert.Equal(t, DeviceResult{
		Status:  DeviceAuthorized,
		Profile: testProfile,
		Tokens:  Tokens{RefreshToken: "refresh-1", RefreshExpiresAt: testNow.Add(1800 * time.Second)},
	}, res)

	reqs := f.recorded(testOIDCPath + "/token")
	require.Len(t, reqs, 1)
	assert.Equal(t, deviceGrantType, reqs[0].Form.Get("grant_type"))
	assert.Equal(t, "dev-code", reqs[0].Form.Get("device_code"))
	assert.Equal(t, "device-pkce-verifier", reqs[0].Form.Get("code_verifier"))
}

func TestKeycloak_PollDeviceResults(t *testing.T) {
	f := newFakeKeycloak(t)
	tests := []struct {
		code string
		want DeviceStatus
	}{
		{"authorization_pending", DevicePending},
		{"slow_down", DeviceSlowDown},
		{"expired_token", DeviceExpired},
		{"access_denied", DeviceDenied},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			k := newTestKeycloak(t, f)
			f.setToken(respond(http.StatusBadRequest, map[string]string{"error": tt.code}))
			res, err := k.PollDevice(t.Context(), "dev-code", "device-pkce-verifier")
			require.NoError(t, err)
			assert.Equal(t, DeviceResult{Status: tt.want}, res)
		})
	}
}

func TestKeycloak_PollDeviceErrors(t *testing.T) {
	f := newFakeKeycloak(t)
	tests := []struct {
		name    string
		handler func() http.HandlerFunc
	}{
		{"invalid grant", func() http.HandlerFunc {
			return respond(http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		}},
		{"server error", func() http.HandlerFunc { return respond(http.StatusInternalServerError, map[string]string{}) }},
		{"error in 200", func() http.HandlerFunc {
			return respond(http.StatusOK, map[string]string{"error": "authorization_pending"})
		}},
		{"non-json body", func() http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }
		}},
		{"nonce-less token with bad signature", func() http.HandlerFunc {
			return respond(http.StatusOK, loginResponse(signIDToken(t, testOtherKey, f.idClaims(nil))))
		}},
		{"non-uuid sub", func() http.HandlerFunc {
			return respond(http.StatusOK, loginResponse(f.idToken(t, map[string]any{"sub": "artem"})))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newTestKeycloak(t, f)
			f.setToken(tt.handler())
			_, err := k.PollDevice(t.Context(), "dev-code", "device-pkce-verifier")
			require.Error(t, err)
		})
	}

	t.Run("wrong client secret", func(t *testing.T) {
		k := NewKeycloak(KeycloakConfig{Issuer: f.issuer(), ClientID: testClientID, ClientSecret: "wrong"})
		f.setToken(respond(http.StatusOK, loginResponse(f.idToken(t, nil))))
		_, err := k.PollDevice(t.Context(), "dev-code", "device-pkce-verifier")
		require.ErrorContains(t, err, "status 401")
	})
}

func TestProfileFromClaims(t *testing.T) {
	tests := []struct {
		name   string
		claims idClaims
		want   Profile
	}{
		{"name", idClaims{Subject: testUserID, Name: "Artem", PreferredUsername: "artem", Email: "a@x"},
			Profile{UserID: testUserID, Name: "Artem", Email: "a@x"}},
		{"preferred username", idClaims{Subject: testUserID, PreferredUsername: "artem", Email: "a@x"},
			Profile{UserID: testUserID, Name: "artem", Email: "a@x"}},
		{"email", idClaims{Subject: testUserID, Email: "a@x"}, Profile{UserID: testUserID, Name: "a@x", Email: "a@x"}},
		{"https picture", idClaims{Subject: testUserID, Picture: "https://x/a.png"},
			Profile{UserID: testUserID, Picture: "https://x/a.png"}},
		{"http picture", idClaims{Subject: testUserID, Picture: "http://x/a.png"}, Profile{UserID: testUserID}},
		{"relative picture", idClaims{Subject: testUserID, Picture: "/a.png"}, Profile{UserID: testUserID}},
		{"hostless picture", idClaims{Subject: testUserID, Picture: "https:///a.png"}, Profile{UserID: testUserID}},
		{"javascript picture", idClaims{Subject: testUserID, Picture: "javascript:alert(1)"}, Profile{UserID: testUserID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := profileFromClaims(tt.claims)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	for _, sub := range []string{"", "artem", testUserID + "0", strings.ReplaceAll(testUserID, "-", "")} {
		_, err := profileFromClaims(idClaims{Subject: sub})
		require.Error(t, err, sub)
	}
}

func TestKeycloak_Introspect(t *testing.T) {
	for _, active := range []bool{true, false} {
		f := newFakeKeycloak(t)
		k := newTestKeycloak(t, f)
		f.setIntrospect(respond(http.StatusOK, map[string]any{"active": active}))

		verdict, err := k.Introspect(t.Context(), "rt")
		require.NoError(t, err)
		want := VerdictRevoked
		if active {
			want = VerdictActive
		}
		assert.Equal(t, want, verdict)

		reqs := f.recorded(testOIDCPath + "/token/introspect")
		require.Len(t, reqs, 1, "one call")
		assert.Empty(t, f.recorded(testOIDCPath+"/token"))
		assert.Equal(t, "rt", reqs[0].Form.Get("token"))
		assert.Equal(t, "refresh_token", reqs[0].Form.Get("token_type_hint"))
	}
}

func TestKeycloak_IntrospectErrors(t *testing.T) {
	f := newFakeKeycloak(t)
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"server error", respond(http.StatusInternalServerError, map[string]any{"active": false})},
		{"not found", respond(http.StatusNotFound, map[string]any{"active": false})},
		{"redirect not followed", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}},
		{"non-json body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }},
		{"active missing", respond(http.StatusOK, map[string]any{})},
		{"active null", respond(http.StatusOK, map[string]any{"active": nil})},
		{"active string", respond(http.StatusOK, map[string]any{"active": "false"})},
		{"active number", respond(http.StatusOK, map[string]any{"active": 0})},
		{"over-limit body", respond(http.StatusOK, map[string]any{"active": false, "pad": strings.Repeat("x", maxResponseBytes)})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newTestKeycloak(t, f)
			f.setIntrospect(tt.handler)
			verdict, err := k.Introspect(t.Context(), "rt")
			require.Error(t, err)
			assert.Equal(t, VerdictUnknown, verdict)
			assert.NotContains(t, err.Error(), "rt\"")
			assert.Empty(t, f.recorded("/elsewhere"), "no redirect is followed")
		})
	}

	t.Run("invalid client", func(t *testing.T) {
		k := NewKeycloak(KeycloakConfig{Issuer: f.issuer(), ClientID: testClientID, ClientSecret: "wrong"})
		f.setIntrospect(respond(http.StatusOK, map[string]any{"active": false}))
		verdict, err := k.Introspect(t.Context(), "rt")
		require.ErrorContains(t, err, "status 401")
		assert.Equal(t, VerdictUnknown, verdict)
	})
}

func TestKeycloak_IntrospectNoEndpoint(t *testing.T) {
	f := newFakeKeycloak(t)
	f.noIntrospection.Store(true)
	k := newTestKeycloak(t, f)

	verdict, err := k.Introspect(t.Context(), "rt")
	require.ErrorContains(t, err, "no introspection endpoint")
	assert.Equal(t, VerdictUnknown, verdict)
	assert.Empty(t, f.recorded(testOIDCPath+"/token/introspect"))
}

func TestKeycloak_timeout(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	_, err := k.AuthCodeURL(t.Context(), "s", "n", "v") // discover before shortening the timeout
	require.NoError(t, err)
	k.timeout = 100 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hang := func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}
	f.setIntrospect(hang)
	f.setToken(hang)

	start := time.Now()
	_, err = k.Introspect(t.Context(), "rt")
	require.Error(t, err)
	_, _, err = k.Refresh(t.Context(), "rt")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestKeycloak_Refresh(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	f.setToken(respond(http.StatusOK, map[string]any{
		"access_token":       "access",
		"token_type":         "Bearer",
		"expires_in":         300,
		"refresh_token":      "refresh-2",
		"refresh_expires_in": 1800,
	}))

	tokens, verdict, err := k.Refresh(t.Context(), "refresh-1")
	require.NoError(t, err)
	assert.Equal(t, VerdictActive, verdict)
	assert.Equal(t, Tokens{RefreshToken: "refresh-2", RefreshExpiresAt: testNow.Add(1800 * time.Second)}, tokens)

	reqs := f.recorded(testOIDCPath + "/token")
	require.Len(t, reqs, 1, "one call")
	assert.Empty(t, f.recorded(testOIDCPath+"/token/introspect"))
	assert.Equal(t, "refresh_token", reqs[0].Form.Get("grant_type"))
	assert.Equal(t, "refresh-1", reqs[0].Form.Get("refresh_token"))
	assert.Empty(t, reqs[0].Form.Get("client_secret"), "the secret goes in basic auth")
}

func TestKeycloak_RefreshRevoked(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	f.setToken(respond(http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "Session not active"}))

	tokens, verdict, err := k.Refresh(t.Context(), "refresh-1")
	require.NoError(t, err)
	assert.Equal(t, VerdictRevoked, verdict)
	assert.Equal(t, Tokens{}, tokens)
	assert.Len(t, f.recorded(testOIDCPath+"/token"), 1, "one call")
}

func TestKeycloak_RefreshErrors(t *testing.T) {
	f := newFakeKeycloak(t)
	ok := func(over map[string]any) http.HandlerFunc {
		body := map[string]any{
			"access_token": "access", "token_type": "Bearer", "expires_in": 300,
			"refresh_token": "refresh-2", "refresh_expires_in": 1800,
		}
		for key, v := range over {
			if v == nil {
				delete(body, key)
				continue
			}
			body[key] = v
		}
		return respond(http.StatusOK, body)
	}
	raw := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"server error", respond(http.StatusInternalServerError, map[string]string{"error": "invalid_grant"})},
		{"invalid_grant in 200", respond(http.StatusOK, map[string]string{"error": "invalid_grant"})},
		{"other 400", respond(http.StatusBadRequest, map[string]string{"error": "invalid_request"})},
		{"invalid client", respond(http.StatusUnauthorized, map[string]string{"error": "invalid_client"})},
		{"redirect not followed", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}},
		{"non-json body", raw(http.StatusOK, "<html>")},
		{"over-limit body", ok(map[string]any{"pad": strings.Repeat("x", maxResponseBytes)})},
		{"missing refresh token", ok(map[string]any{"refresh_token": nil})},
		{"empty refresh token", ok(map[string]any{"refresh_token": ""})},
		{"missing refresh expiry", ok(map[string]any{"refresh_expires_in": nil})},
		{"zero refresh expiry", ok(map[string]any{"refresh_expires_in": 0})},
		{"negative refresh expiry", ok(map[string]any{"refresh_expires_in": -5})},
		{"fractional refresh expiry", ok(map[string]any{"refresh_expires_in": 1800.5})},
		{"string refresh expiry", ok(map[string]any{"refresh_expires_in": "1800"})},
		{"overflowing refresh expiry", raw(http.StatusOK,
			`{"access_token":"a","token_type":"Bearer","refresh_token":"r","refresh_expires_in":1e300}`)},
		{"refresh expiry over ten years", ok(map[string]any{"refresh_expires_in": 3651 * 24 * 3600})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newTestKeycloak(t, f)
			f.setToken(tt.handler)
			before := len(f.recorded(testOIDCPath + "/token"))
			tokens, verdict, err := k.Refresh(t.Context(), "refresh-1")
			require.Error(t, err)
			assert.Equal(t, VerdictUnknown, verdict)
			assert.Equal(t, Tokens{}, tokens)
			assert.NotContains(t, err.Error(), "refresh-1")
			assert.Len(t, f.recorded(testOIDCPath+"/token"), before+1, "one call, no auth-style retry")
			assert.Empty(t, f.recorded("/elsewhere"), "no redirect is followed")
		})
	}

	t.Run("wrong client secret", func(t *testing.T) {
		k := NewKeycloak(KeycloakConfig{Issuer: f.issuer(), ClientID: testClientID, ClientSecret: "wrong"})
		f.setToken(ok(nil))
		_, verdict, err := k.Refresh(t.Context(), "refresh-1")
		require.ErrorContains(t, err, "status 401")
		assert.Equal(t, VerdictUnknown, verdict)
	})
}

func TestKeycloak_discoveryWaitHonorsContext(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	k.lock <- struct{}{} // another caller is discovering
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := k.Introspect(ctx, "rt")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	<-k.lock
}

func TestLimitedBody(t *testing.T) {
	f := newFakeKeycloak(t)
	k := newTestKeycloak(t, f)
	f.setIntrospect(respond(http.StatusOK, map[string]any{"active": true, "pad": strings.Repeat("x", maxResponseBytes-100)}))
	verdict, err := k.Introspect(t.Context(), "rt")
	require.NoError(t, err, "a body just under the limit is read whole")
	assert.Equal(t, VerdictActive, verdict)
}
