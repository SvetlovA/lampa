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

// testDocument has sections in non-response order and a value html escaping would change.
func testDocument() storage.Document {
	return storage.Document{
		SchemaVersion: 1,
		Data: map[string]json.RawMessage{
			"other":     json.RawMessage(`{}`),
			"settings":  json.RawMessage(`{"torrserver_url":"http://a/?x=1&y=<2>"}`),
			"favorites": json.RawMessage(`{"card":[1,2]}`),
		},
		UpdatedAt: testUpdatedAt,
	}
}

const testDocumentJSON = `{"schema_version":1,"data":{"settings":{"torrserver_url":"http://a/?x=1&y=<2>"},` +
	`"favorites":{"card":[1,2]},"other":{}},"updated_at":"2026-09-18T12:30:00.123Z"}` + "\n"

// assertDocumentJSON checks body against testDocumentJSON, including section order and no html escaping.
func assertDocumentJSON(t *testing.T, body []byte) {
	t.Helper()
	assert.JSONEq(t, testDocumentJSON, string(body))
	assert.Regexp(t, `"data":\{"settings":.*"favorites":.*"other":`, string(body), "sections in response order")
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
		{name: "connections unreadable", err: fmt.Errorf("merge connections: %w", storage.ErrConnectionsUnreadable),
			status: http.StatusInternalServerError, code: "connections_unreadable"},
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
	const validBody = `{"schema_version":1,"data":{}}`
	tests := []struct {
		name        string
		contentType string
		body        string
		err         error
		status      int
		code        string
		called      bool
	}{
		{name: "stored", contentType: "application/json", body: validBody, status: http.StatusOK, called: true},
		{name: "json with charset", contentType: "application/json; charset=utf-8", body: validBody, status: http.StatusOK, called: true},
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
			err: &storage.ValidationError{Code: storage.CodeUnsupportedSchemaVersion, Reason: "schema_version must be 1"}},
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
				ReplaceFunc: func(_ context.Context, userID string, raw []byte) (storage.Document, error) {
					if tc.err != nil {
						return storage.Document{}, tc.err
					}
					return testDocument(), nil
				},
			}
			srv, _ := newTestServer(t, svc, allowAll(testUserID))
			req := httptest.NewRequest(http.MethodPut, userDataPath, strings.NewReader(tc.body))
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
				assertDocumentJSON(t, body)
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
	req := httptest.NewRequest(http.MethodPut, userDataPath, failingReader{})
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
			srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodDelete, userDataPath, http.NoBody))
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

func TestOrderedData_MarshalJSON(t *testing.T) {
	out, err := json.Marshal(orderedData{
		"history":  json.RawMessage(`{"a":1}`),
		"unknown":  json.RawMessage(`{"dropped":true}`),
		"settings": json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"settings":{},"history":{"a":1}}`, string(out))
	assert.Regexp(t, `^\{"settings":.*"history":`, string(out), "sections in response order")

	out, err = json.Marshal(orderedData{})
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(out))
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
