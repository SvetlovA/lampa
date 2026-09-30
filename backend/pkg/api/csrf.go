package api

import "net/http"

// CSRFHeader must be sent with the value "1" on every state-changing request. a cross-site form or
// a no-cors fetch cannot set a custom header, and the api never answers a preflight with cors headers.
const CSRFHeader = "X-Lampa-Csrf"

// csrf rejects state-changing requests (any method but GET, HEAD and OPTIONS) that lack CSRFHeader
// or carry an Origin other than the configured public origin. an absent Origin is allowed, since
// old tv webviews omit it on same-origin requests; the header alone still blocks cross-site forms.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !s.sameSite(r) {
			WriteError(w, http.StatusForbidden, "csrf_rejected", "cross-site request rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameSite reports whether r carries the csrf header and, when it has an Origin, only the public origin.
// "Origin: null" (sandboxed frames, some redirects) never equals the public origin and is rejected.
func (s *Server) sameSite(r *http.Request) bool {
	if r.Header.Get(CSRFHeader) != "1" {
		return false
	}
	origins := r.Header.Values("Origin")
	switch len(origins) {
	case 0:
		return true
	case 1:
		return origins[0] == s.cfg.PublicOrigin
	default:
		return false
	}
}
