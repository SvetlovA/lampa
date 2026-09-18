// Package api serves the user-data HTTP API behind an Authenticator seam.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

//go:generate go tool moq -out mocks/userdata.go -pkg mocks -skip-ensure -fmt goimports . UserData

// http server hardening, see the plan's technical details
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 16 << 10

	// requestTimeout bounds the store work of one request. it stays below writeTimeout so a stalled
	// database still answers 503 storage_unavailable instead of a dropped connection.
	requestTimeout = 10 * time.Second

	// ShutdownTimeout bounds the wait for in-flight requests after the serve context is canceled.
	ShutdownTimeout = 15 * time.Second
)

const userDataPath = "/api/v1/user-data"

// UserData reads, replaces and deletes the document of a user. storage.Service is the production implementation.
type UserData interface {
	Get(ctx context.Context, userID string) (storage.Document, error)
	Replace(ctx context.Context, userID string, raw []byte) (storage.Document, error)
	Delete(ctx context.Context, userID string) error
}

// Logger is the logging dependency of Server.
type Logger interface {
	Printf(format string, args ...any)
}

// ServerConfig holds configuration for the api server.
type ServerConfig struct {
	Addr         string // listen address used by Start, host:port
	MaxBodyBytes int64  // request body limit, bytes
}

// Server serves the user-data API.
type Server struct {
	cfg     ServerConfig
	svc     UserData
	auth    Authenticator
	logger  Logger
	handler http.Handler
}

// NewServer creates a Server. every dependency is required and the body limit must be positive.
func NewServer(cfg ServerConfig, svc UserData, auth Authenticator, logger Logger) (*Server, error) {
	switch {
	case svc == nil:
		return nil, errors.New("nil user data service")
	case auth == nil:
		return nil, errors.New("nil authenticator")
	case logger == nil:
		return nil, errors.New("nil logger")
	case cfg.MaxBodyBytes <= 0:
		return nil, errors.New("max body bytes must be positive")
	}
	s := &Server{cfg: cfg, svc: svc, auth: auth, logger: logger}
	s.handler = s.routes()
	return s, nil
}

// Handler returns the api handler with its full middleware chain.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Start listens on the configured address and serves until ctx is canceled.
func (s *Server) Start(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve serves on ln until ctx is canceled or the server fails. it closes ln.
// after cancellation it returns only once in-flight requests finished or ShutdownTimeout expired.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	return ServeHandler(ctx, ln, s.handler)
}

// routes registers method patterns plus json fallbacks, since ServeMux answers 404/405 in plain text.
// middleware order, outermost first: access log, recover, body limit, deadline, authenticator (per route).
func (s *Server) routes() http.Handler {
	methodNotAllowed := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+userDataPath, s.authenticate(s.getUserData))
	mux.HandleFunc("PUT "+userDataPath, s.authenticate(s.putUserData))
	mux.HandleFunc("DELETE "+userDataPath, s.authenticate(s.deleteUserData))
	// a "GET" pattern also matches HEAD, so HEAD needs its own route to reach the 405 fallback
	// instead of running the authenticated get handler.
	mux.HandleFunc("HEAD "+userDataPath, methodNotAllowed)
	mux.HandleFunc(userDataPath, methodNotAllowed)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
	})
	return s.accessLog(s.recoverPanic(s.limitBody(s.withDeadline(mux))))
}

// ServeHandler runs an http.Server with the hardening timeouts serving h on ln until ctx is canceled
// or the server fails. it closes ln. a goroutine shuts the server down on <-ctx.Done(), and ServeHandler
// waits for that shutdown so callers never release dependencies under in-flight handlers.
func ServeHandler(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	stop := make(chan struct{})
	shutdown := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
			shutdown <- nil
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			srv.Close()
			shutdown <- fmt.Errorf("shutdown: %w", err)
			return
		}
		shutdown <- nil
	}()

	err := srv.Serve(ln)
	if !errors.Is(err, http.ErrServerClosed) {
		close(stop)
		<-shutdown
		srv.Close()
		return fmt.Errorf("serve %s: %w", ln.Addr(), err)
	}
	if err := <-shutdown; err != nil {
		return err
	}
	return nil
}

// errorBody is the json error contract: {"error": {"code": ..., "message": ...}}.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeError writes the json error contract. message must be a fixed string, never request input.
func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code, body.Error.Message = code, message
	writeJSON(w, status, body)
}

// internalErrorBody is sent when a response cannot be encoded.
const internalErrorBody = `{"error":{"code":"internal_error","message":"internal error"}}` + "\n"

// writeJSON writes v as a json response without html escaping. v is encoded before the header
// is sent, so an encoding failure still answers 500 internal_error.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		status = http.StatusInternalServerError
		buf.Reset()
		buf.WriteString(internalErrorBody)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// statusRecorder remembers the response status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status and passes it on.
func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

// Write records an implicit 200 and passes the data on.
func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p) //nolint:wrapcheck // transparent ResponseWriter wrapper
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// accessLog logs method, path, status and duration of every request. bodies, headers,
// cookies and query strings are never logged.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		s.logger.Printf("[INFO] %q %q %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Microsecond))
	})
}

// recoverPanic turns a handler panic into a logged 500 internal_error. http.ErrAbortHandler is re-panicked,
// it is the documented way to abort a response.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(p)
			}
			s.logger.Printf("[ERROR] panic serving %q %q: %v\n%s", r.Method, r.URL.Path, p, debug.Stack())
			writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}

// withDeadline gives the request context a deadline. the http server timeouts only set connection
// deadlines and never cancel the request context, so without it a stalled database blocks handlers.
func (s *Server) withDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// limitBody caps the request body at the configured size; reading past it fails with *http.MaxBytesError.
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
