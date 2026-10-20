package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/api/mocks"
	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

var testUpdatedAt = time.Date(2026, 9, 18, 21, 30, 0, 123000000, time.FixedZone("JDT", 9*3600))

// testDocument includes arbitrary keys and a value html escaping would change.
func testDocument() storage.Document {
	return storage.Document{
		SchemaVersion: 2,
		ETag:          "test-etag",
		Data: map[string]json.RawMessage{
			"custom.key:1": json.RawMessage(`[null,true,42,"x"]`),
			"settings":     json.RawMessage(`{"torrserver_url":"http://a/?x=1&y=<2>"}`),
			"favorites":    json.RawMessage(`{"card":[1,2]}`),
		},
		UpdatedAt: testUpdatedAt,
	}
}

const testDocumentJSON = `{"schema_version":2,"data":{"settings":{"torrserver_url":"http://a/?x=1&y=<2>"},` +
	`"favorites":{"card":[1,2]},"custom.key:1":[null,true,42,"x"]},"updated_at":"2026-09-18T12:30:00.123Z"}` + "\n"

// assertDocumentJSON checks the document values and no html escaping.
func assertDocumentJSON(t *testing.T, body []byte) {
	t.Helper()
	assert.JSONEq(t, testDocumentJSON, string(body))
	assert.Contains(t, string(body), `&y=<2>`, "no html escaping")
}

func TestServer_GetUserData(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		logged string
	}{
		{name: "found", status: http.StatusOK},
		{name: "not found", err: storage.ErrNotFound, status: http.StatusNotFound, code: "user_data_not_found"},
		{name: "unavailable", err: fmt.Errorf("get user data: %w: %w", storage.ErrUnavailable, errors.New("dial 10.0.0.5")),
			status: http.StatusServiceUnavailable, code: "storage_unavailable"},
		{name: "unexpected", err: errors.New("decode stored data: bad"), status: http.StatusInternalServerError,
			code: "internal_error", logged: `[ERROR] "GET" "/api/v1/user-data": decode stored data: bad`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mocks.UserDataMock{
				GetFunc: func(_ context.Context, userID string) (storage.Document, error) {
					if tc.err != nil {
						return storage.Document{}, tc.err
					}
					return testDocument(), nil
				},
			}
			srv, logs := newTestServer(t, svc, allowAll(testUserID))
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, userDataPath, http.NoBody))
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, tc.status, resp.StatusCode)
			require.Len(t, svc.GetCalls(), 1)
			assert.Equal(t, testUserID, svc.GetCalls()[0].UserID)
			assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
			if tc.err == nil {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assertDocumentJSON(t, body)
				return
			}
			errBody := decodeError(t, resp)
			assert.Equal(t, tc.code, errBody.Error.Code)
			assert.NotContains(t, errBody.Error.Message, "10.0.0.5")
			if tc.logged != "" {
				assert.Contains(t, logs.String(), tc.logged)
			}
		})
	}
}

func TestServer_PutUserData(t *testing.T) {
	const validBody = `{"schema_version":2,"data":{}}`
	tests := []struct {
		name        string
		contentType string
		body        string
		err         error
		status      int
		code        string
		called      bool
	}{
		{name: "stored", contentType: "application/json", body: validBody, status: http.StatusNoContent, called: true},
		{name: "json with charset", contentType: "application/json; charset=utf-8", body: validBody, status: http.StatusNoContent, called: true},
		{name: "missing content type", body: validBody, status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "form content type", contentType: "application/x-www-form-urlencoded", body: validBody,
			status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "text plain", contentType: "text/plain", body: validBody, status: http.StatusUnsupportedMediaType,
			code: "unsupported_media_type"},
		{name: "body over transport limit", contentType: "application/json", body: strings.Repeat(" ", 1<<10+1),
			status: http.StatusRequestEntityTooLarge, code: "request_too_large"},
		{name: "invalid json", contentType: "application/json", body: "{", called: true, status: http.StatusBadRequest,
			code: "invalid_json", err: &storage.ValidationError{Code: storage.CodeInvalidJSON, Reason: "body is not valid json"}},
		{name: "invalid document", contentType: "application/json", body: validBody, called: true, status: http.StatusBadRequest,
			code: "invalid_document", err: &storage.ValidationError{Code: storage.CodeInvalidDocument, Reason: "data must be an object"}},
		{name: "unsupported schema version", contentType: "application/json", body: validBody, called: true,
			status: http.StatusBadRequest, code: "unsupported_schema_version",
			err: &storage.ValidationError{Code: storage.CodeUnsupportedSchemaVersion, Reason: "schema_version must be 2"}},
		{name: "document too large", contentType: "application/json", body: validBody, called: true,
			status: http.StatusRequestEntityTooLarge, code: "request_too_large",
			err: &storage.ValidationError{Code: storage.CodeTooLarge, Reason: "body exceeds size limit"}},
		{name: "unavailable", contentType: "application/json", body: validBody, called: true,
			status: http.StatusServiceUnavailable, code: "storage_unavailable",
			err: fmt.Errorf("upsert user data: %w: %w", storage.ErrUnavailable, errors.New("conn reset"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mocks.UserDataMock{
				ReplaceFunc: func(_ context.Context, userID string, raw []byte, expectedETag *string) (storage.Document, error) {
					if tc.err != nil {
						return storage.Document{}, tc.err
					}
					return testDocument(), nil
				},
			}
			srv, _ := newTestServer(t, svc, allowAll(testUserID))
			req := newRequest(http.MethodPut, userDataPath, strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, tc.status, resp.StatusCode)
			if !tc.called {
				assert.Empty(t, svc.ReplaceCalls())
			} else {
				require.Len(t, svc.ReplaceCalls(), 1)
				assert.Equal(t, testUserID, svc.ReplaceCalls()[0].UserID)
				assert.Equal(t, tc.body, string(svc.ReplaceCalls()[0].Raw))
			}
			if tc.code == "" {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Empty(t, body)
				return
			}
			assert.Equal(t, tc.code, decodeError(t, resp).Error.Code)
		})
	}
}

// failingReader fails every read with a non-limit error.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestServer_PutUserDataReadFailure(t *testing.T) {
	svc := &mocks.UserDataMock{}
	srv, logs := newTestServer(t, svc, allowAll(testUserID))
	req := newRequest(http.MethodPut, userDataPath, failingReader{})
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalid_json", decodeError(t, resp).Error.Code)
	assert.Empty(t, svc.ReplaceCalls())
	assert.Contains(t, logs.String(), `[WARN] read body of "PUT" "/api/v1/user-data": connection reset`)
}

func TestServer_DeleteUserData(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "deleted", status: http.StatusNoContent},
		{name: "unavailable", err: fmt.Errorf("delete user data: %w", storage.ErrUnavailable),
			status: http.StatusServiceUnavailable, code: "storage_unavailable"},
		{name: "invalid user id", err: storage.ErrInvalidUserID, status: http.StatusInternalServerError, code: "internal_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mocks.UserDataMock{
				DeleteFunc: func(context.Context, string) error { return tc.err },
			}
			srv, _ := newTestServer(t, svc, allowAll(testUserID))
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, newRequest(http.MethodDelete, userDataPath, http.NoBody))
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, tc.status, resp.StatusCode)
			require.Len(t, svc.DeleteCalls(), 1)
			assert.Equal(t, testUserID, svc.DeleteCalls()[0].UserID)
			if tc.code == "" {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Empty(t, body)
				return
			}
			assert.Equal(t, tc.code, decodeError(t, resp).Error.Code)
		})
	}
}

func TestIsJSON(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"application/json", true},
		{"Application/JSON; charset=utf-8", true},
		{"application/jsonp", false},
		{"text/json", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.contentType, func(t *testing.T) {
			assert.Equal(t, tc.want, isJSON(tc.contentType))
		})
	}
}

func TestServer_RequestContextHasDeadline(t *testing.T) {
	var deadline time.Time
	var hasDeadline bool
	svc := &mocks.UserDataMock{
		GetFunc: func(ctx context.Context, _ string) (storage.Document, error) {
			deadline, hasDeadline = ctx.Deadline()
			return testDocument(), nil
		},
	}
	srv, _ := newTestServer(t, svc, allowAll(testUserID))

	start := time.Now()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, userDataPath, http.NoBody))

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, hasDeadline, "the store must not run on a context without a deadline")
	assert.WithinDuration(t, start.Add(requestTimeout), deadline, time.Second)
	assert.Less(t, requestTimeout, writeTimeout, "a stalled store has to answer 503 before the write timeout drops the connection")
}

func TestServer_UserDataExpectedUser(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		for _, expected := range []string{"", testUserID, flowUserB} {
			t.Run(method+"/"+expected, func(t *testing.T) {
				svc := &mocks.UserDataMock{
					GetFunc: func(context.Context, string) (storage.Document, error) { return testDocument(), nil },
					ReplaceFunc: func(context.Context, string, []byte, *string) (storage.Document, error) {
						return testDocument(), nil
					},
					DeleteFunc: func(context.Context, string) error { return nil },
				}
				srv, _ := newTestServer(t, svc, allowAll(testUserID))
				req := newRequest(method, userDataPath, strings.NewReader(`{"schema_version":2,"data":{}}`))
				req.Header.Set("Content-Type", "application/json")
				if expected != "" {
					req.Header.Set("X-Lampa-Expected-User", expected)
				}
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, req)
				calls := len(svc.GetCalls()) + len(svc.ReplaceCalls()) + len(svc.DeleteCalls())
				if expected == flowUserB {
					assert.Equal(t, http.StatusConflict, w.Code)
					assert.Equal(t, "account_changed", errorCode(t, w.Body.String()))
					assert.Zero(t, calls, "identity mismatch must not access storage")
					return
				}
				assert.Equal(t, 1, calls)
				if method == http.MethodGet {
					assert.Equal(t, http.StatusOK, w.Code)
				} else {
					assert.Equal(t, http.StatusNoContent, w.Code)
				}
			})
		}
	}
}

func TestServer_GetUserDataETag(t *testing.T) {
	for _, tc := range []struct {
		name, tag  string
		status     int
		unreadable bool
	}{
		{name: "absent", status: http.StatusOK},
		{name: "match", tag: `"test-etag"`, status: http.StatusNotModified},
		{name: "weak", tag: `W/"test-etag"`, status: http.StatusNotModified},
		{name: "different", tag: `"other"`, status: http.StatusOK},
		{name: "unreadable values", tag: `"test-etag"`, status: http.StatusOK, unreadable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mocks.UserDataMock{GetFunc: func(context.Context, string) (storage.Document, error) {
				doc := testDocument()
				doc.UnreadableValues = tc.unreadable
				return doc, nil
			}}
			srv, _ := newTestServer(t, svc, allowAll(testUserID))
			req := newRequest(http.MethodGet, userDataPath, http.NoBody)
			req.Header.Set("If-None-Match", tc.tag)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			assert.Equal(t, tc.status, w.Code)
			assert.Equal(t, `"test-etag"`, w.Header().Get("ETag"))
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			if tc.status == http.StatusNotModified {
				assert.Empty(t, w.Body.String())
			} else {
				assertDocumentJSON(t, w.Body.Bytes())
			}
		})
	}
}

func TestServer_PutUserDataIfMatch(t *testing.T) {
	for _, tc := range []struct {
		name, tag string
		present   bool
		want      *string
		err       error
	}{
		{name: "absent"},
		{name: "match", present: true, tag: `"old"`, want: new("old")},
		{name: "weak", present: true, tag: `W/"old"`, want: new("old")},
		{name: "empty", present: true, want: new("")},
		{name: "quoted empty", present: true, tag: `""`, want: new("")},
		{name: "stale", present: true, tag: `"stale"`, want: new("stale"), err: storage.ErrPreconditionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mocks.UserDataMock{ReplaceFunc: func(context.Context, string, []byte, *string) (storage.Document, error) {
				return testDocument(), tc.err
			}}
			srv, _ := newTestServer(t, svc, allowAll(testUserID))
			req := newRequest(http.MethodPut, userDataPath, strings.NewReader(`{"schema_version":2,"data":{}}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.present {
				req.Header.Set("If-Match", tc.tag)
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			require.Len(t, svc.ReplaceCalls(), 1)
			assert.Equal(t, tc.want, svc.ReplaceCalls()[0].ExpectedETag)
			if tc.err != nil {
				assert.Equal(t, http.StatusPreconditionFailed, w.Code)
				assert.Equal(t, "precondition_failed", errorCode(t, w.Body.String()))
				assert.Empty(t, w.Header().Get("ETag"))
				return
			}
			assert.Equal(t, http.StatusNoContent, w.Code)
			assert.Empty(t, w.Body.String())
			assert.Equal(t, `"test-etag"`, w.Header().Get("ETag"))
		})
	}
}
