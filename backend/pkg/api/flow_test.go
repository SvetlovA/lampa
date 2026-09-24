package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

const (
	flowUserA = testUserID
	flowUserB = "7d1e4c92-5b3a-4f60-8e27-91a0c3d5b6f8"
	flowKey   = "jackett-key-plaintext"
)

// fakeStore is an in-memory storage.Store. failing makes every call fail like an unreachable database.
type fakeStore struct {
	records map[string]storage.Record
	failing bool
}

func (f *fakeStore) Get(_ context.Context, userID string) (storage.Record, error) {
	if f.failing {
		return storage.Record{}, errors.New("dial tcp 10.0.0.5:5432: connection refused")
	}
	rec, ok := f.records[userID]
	if !ok {
		return storage.Record{}, storage.ErrNotFound
	}
	return rec, nil
}

func (f *fakeStore) Upsert(_ context.Context, rec storage.Record) (storage.Record, error) {
	if f.failing {
		return storage.Record{}, errors.New("dial tcp 10.0.0.5:5432: connection refused")
	}
	rec.CreatedAt = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	rec.UpdatedAt = rec.CreatedAt
	f.records[rec.UserID] = rec
	return rec, nil
}

func (f *fakeStore) Delete(_ context.Context, userID string) error {
	if f.failing {
		return errors.New("dial tcp 10.0.0.5:5432: connection refused")
	}
	delete(f.records, userID)
	return nil
}

// newFlowServer wires a Server to a real storage.Service on a fakeStore. the user is taken from the
// X-Test-User header, which stands in for the identity provider.
func newFlowServer(t *testing.T) (*Server, *fakeStore) {
	t.Helper()
	store := &fakeStore{records: map[string]storage.Record{}}
	var key [32]byte
	key[0] = 1
	sealer, err := storage.NewSealer(key)
	require.NoError(t, err)
	limits := storage.Limits{MaxBodyBytes: 1 << 10, MaxSectionBytes: storage.DefaultMaxSectionBytes, MaxDepth: storage.DefaultMaxDepth}
	logger := log.New(io.Discard, "", 0)
	svc, err := storage.NewService(store, sealer, limits, logger)
	require.NoError(t, err)
	auth := authFunc(func(r *http.Request) (string, error) { return r.Header.Get("X-Test-User"), nil })
	srv, err := NewServer(ServerConfig{MaxBodyBytes: limits.MaxBodyBytes}, svc, auth, logger)
	require.NoError(t, err)
	return srv, store
}

// call sends one request as user and returns the status and body.
func call(t *testing.T, srv *Server, method, user, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, userDataPath, strings.NewReader(body))
	req.Header.Set("X-Test-User", user)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	var eb errorBody
	require.NoError(t, json.Unmarshal([]byte(body), &eb))
	return eb.Error.Code
}

func TestServer_UserDataFlow(t *testing.T) {
	srv, store := newFlowServer(t)
	putBody := `{"schema_version":1,"data":{"settings":{"jackett_key":"` + flowKey + `","language":"ru"},"favorites":{"a":1}}}`

	status, body := call(t, srv, http.MethodGet, flowUserA, "")
	require.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "user_data_not_found", errorCode(t, body))

	status, _ = call(t, srv, http.MethodPut, flowUserA, putBody)
	require.Equal(t, http.StatusOK, status)
	stored := store.records[flowUserA]
	assert.NotContains(t, string(stored.Data), flowKey, "credentials are sealed before they reach the store")
	assert.NotEmpty(t, stored.EncryptedConnections)

	status, body = call(t, srv, http.MethodGet, flowUserA, "")
	require.Equal(t, http.StatusOK, status)
	var got struct {
		Data struct {
			Settings map[string]any `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, map[string]any{"jackett_key": flowKey, "language": "ru"}, got.Data.Settings)

	status, body = call(t, srv, http.MethodGet, flowUserB, "")
	require.Equal(t, http.StatusNotFound, status, "another user never sees the document")
	assert.Equal(t, "user_data_not_found", errorCode(t, body))

	status, body = call(t, srv, http.MethodPut, flowUserA, "{")
	require.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_json", errorCode(t, body))
	assert.Equal(t, stored, store.records[flowUserA], "a rejected body leaves the stored document untouched")

	status, _ = call(t, srv, http.MethodDelete, flowUserA, "")
	require.Equal(t, http.StatusNoContent, status)
	status, _ = call(t, srv, http.MethodGet, flowUserA, "")
	assert.Equal(t, http.StatusNotFound, status)
}

func TestServer_StoreDown(t *testing.T) {
	srv, store := newFlowServer(t)
	store.failing = true

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			status, body := call(t, srv, method, flowUserA, `{"schema_version":1,"data":{}}`)
			require.Equal(t, http.StatusServiceUnavailable, status)
			assert.Equal(t, "storage_unavailable", errorCode(t, body))
			assert.NotContains(t, body, "10.0.0.5", "driver details never reach the client")
		})
	}
}
