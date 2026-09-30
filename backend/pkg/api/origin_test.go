package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestOrigin(t *testing.T) {
	tests := []struct {
		name, host, proxyScheme, want string
		reject                        bool
	}{
		{name: "tailnet IP", host: "100.64.0.1:8092", want: "http://100.64.0.1:8092"},
		{name: "second host", host: "svtlv.tail07c665.ts.net:8092", want: "http://svtlv.tail07c665.ts.net:8092"},
		{name: "trusted HTTPS proxy", host: "lampa.example", proxyScheme: "https", want: "https://lampa.example"},
		{name: "IPv6", host: "[fd7a:115c:a1e0::1]:8092", want: "http://[fd7a:115c:a1e0::1]:8092"},
		{name: "empty host", reject: true},
		{name: "host with path", host: "lampa.example/evil", reject: true},
		{name: "userinfo", host: "other@lampa.example", reject: true},
		{name: "comma", host: "lampa.example,other", reject: true},
		{name: "uppercase host", host: "Lampa.example", reject: true},
		{name: "bad port", host: "lampa.example:abc", reject: true},
		{name: "empty port", host: "lampa.example:", reject: true},
		{name: "default port", host: "lampa.example:80", reject: true},
		{name: "unbracketed IPv6", host: "fd7a:115c:a1e0::1", reject: true},
		{name: "invalid proxy scheme", host: "lampa.example", proxyScheme: "https,http", reject: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			r.Host = tc.host
			if tc.proxyScheme != "" {
				r.Header.Set(proxySchemeHeader, tc.proxyScheme)
			}
			origin, err := RequestOrigin(r)
			if tc.reject {
				require.Error(t, err)
				assert.Empty(t, origin)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, origin)
		})
	}
}

func TestRequestOriginRejectsDuplicateProxyScheme(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.Header.Add(proxySchemeHeader, "https")
	r.Header.Add(proxySchemeHeader, "http")
	_, err := RequestOrigin(r)
	require.Error(t, err)
}

func TestValidOriginRejectsBadHostBeforeHandler(t *testing.T) {
	called := false
	h := validOrigin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", http.NoBody)
	r.Host = "lampa.example/evil"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
}
