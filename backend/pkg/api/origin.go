package api

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const proxySchemeHeader = "X-Lampa-Proto"

var errInvalidOrigin = errors.New("invalid request origin")

// RequestOrigin is the browser-facing origin of a request. Apache preserves Host and replaces
// X-Lampa-Proto with the scheme it received; clients cannot choose the scheme through that header.
// A direct request without the proxy header uses its own TLS state.
func RequestOrigin(r *http.Request) (string, error) {
	scheme, ok := requestScheme(r)
	if !ok || !validRequestHost(r.Host, scheme) {
		return "", errInvalidOrigin
	}
	return scheme + "://" + r.Host, nil
}

func requestScheme(r *http.Request) (string, bool) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if values := r.Header.Values(proxySchemeHeader); len(values) != 0 {
		if len(values) != 1 || (values[0] != "http" && values[0] != "https") {
			return "", false
		}
		scheme = values[0]
	}
	return scheme, true
}

func validRequestHost(host, scheme string) bool {
	if host == "" || host != strings.ToLower(host) || strings.HasSuffix(host, ":") || strings.ContainsAny(host, "/@?#\\ \t\r\n,;\"'") {
		return false
	}
	u, err := url.Parse(scheme + "://" + host)
	if err != nil || u.Host != host || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return validHostPort(host, scheme, u)
}

func validHostPort(host, scheme string, u *url.URL) bool {
	if port := u.Port(); port != "" {
		n, perr := strconv.Atoi(port)
		if perr != nil || n < 1 || n > 65535 || (scheme == "http" && n == 80) || (scheme == "https" && n == 443) {
			return false
		}
	}
	// net.SplitHostPort catches unbracketed IPv6 and malformed bracket/port combinations.
	if strings.Contains(host, ":") {
		if _, _, err := net.SplitHostPort(host); err != nil {
			return strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && u.Port() == ""
		}
	}
	return true
}

// validOrigin rejects malformed hosts before any route can generate a redirect or set a cookie.
func validOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := RequestOrigin(r); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_host", "invalid request host")
			return
		}
		next.ServeHTTP(w, r)
	})
}
