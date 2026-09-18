package storage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
	"github.com/SvetlovA/lampa/backend/pkg/storage/mocks"
	"github.com/SvetlovA/lampa/backend/pkg/storage/pgtest"
)

const (
	secretPassword = "hunter2-secret"
	secretKey      = "jackett-api-key-secret"
	contentMarker  = "favorite-movie-marker"
)

var testLimits = storage.Limits{MaxBodyBytes: 1 << 16, MaxSectionBytes: 1 << 15, MaxDepth: storage.DefaultMaxDepth}

// testBody is a valid PUT body with credentials, plain settings and content in another section.
var testBody = []byte(`{"schema_version":1,"data":{"settings":{"torrserver_password":"` + secretPassword +
	`","jackett_key":"` + secretKey + `","language":"ru"},"favorites":{"title":"` + contentMarker + `"}}}`)

func newTestSealer(t *testing.T, fill byte) *storage.Sealer {
	t.Helper()
	var key [32]byte
	for i := range key {
		key[i] = fill
	}
	s, err := storage.NewSealer(key)
	require.NoError(t, err)
	return s
}

func newTestService(t *testing.T, store storage.Store, sealer *storage.Sealer) (*storage.Service, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	svc, err := storage.NewService(store, sealer, testLimits, log.New(&buf, "", 0))
	require.NoError(t, err)
	return svc, &buf
}

// memStore returns a mock store keeping records in a map, like PgStore does.
func memStore() *mocks.StoreMock {
	records := map[string]storage.Record{}
	return &mocks.StoreMock{
		GetFunc: func(_ context.Context, userID string) (storage.Record, error) {
			rec, ok := records[userID]
			if !ok {
				return storage.Record{}, storage.ErrNotFound
			}
			return rec, nil
		},
		UpsertFunc: func(_ context.Context, rec storage.Record) (storage.Record, error) {
			rec.CreatedAt = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
			rec.UpdatedAt = rec.CreatedAt
			records[rec.UserID] = rec
			return rec, nil
		},
		DeleteFunc: func(_ context.Context, userID string) error {
			delete(records, userID)
			return nil
		},
	}
}

func settingsOf(t *testing.T, doc storage.Document) map[string]any {
	t.Helper()
	var settings map[string]any
	require.NoError(t, json.Unmarshal(doc.Data["settings"], &settings))
	return settings
}

func TestNewService(t *testing.T) {
	sealer := newTestSealer(t, 1)
	logger := log.New(&bytes.Buffer{}, "", 0)
	tests := []struct {
		name    string
		store   storage.Store
		sealer  *storage.Sealer
		limits  storage.Limits
		logger  storage.Logger
		wantErr string
	}{
		{name: "ok", store: memStore(), sealer: sealer, limits: testLimits, logger: logger},
		{name: "nil store", sealer: sealer, limits: testLimits, logger: logger, wantErr: "nil store"},
		{name: "nil sealer", store: memStore(), limits: testLimits, logger: logger, wantErr: "nil sealer"},
		{name: "nil logger", store: memStore(), sealer: sealer, limits: testLimits, wantErr: "nil logger"},
		{name: "zero body limit", store: memStore(), sealer: sealer, logger: logger, wantErr: "limits must be positive",
			limits: storage.Limits{MaxSectionBytes: 1, MaxDepth: 1}},
		{name: "zero section limit", store: memStore(), sealer: sealer, logger: logger, wantErr: "limits must be positive",
			limits: storage.Limits{MaxBodyBytes: 1, MaxDepth: 1}},
		{name: "zero depth", store: memStore(), sealer: sealer, logger: logger, wantErr: "limits must be positive",
			limits: storage.Limits{MaxBodyBytes: 1, MaxSectionBytes: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := storage.NewService(tc.store, tc.sealer, tc.limits, tc.logger)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				assert.Nil(t, svc)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, svc)
		})
	}
}

func TestService_ReplaceThenGet(t *testing.T) {
	store := memStore()
	svc, _ := newTestService(t, store, newTestSealer(t, 1))
	userID := strings.ToUpper(newUserID(t))

	stored, err := svc.Replace(t.Context(), userID, testBody)
	require.NoError(t, err)
	assert.Equal(t, 1, stored.SchemaVersion)
	assert.Equal(t, time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC), stored.UpdatedAt)
	assert.Len(t, stored.Data, len(storage.Sections))
	assert.Equal(t, secretPassword, settingsOf(t, stored)["torrserver_password"])

	// the record reaching the store has a lower case id, sealed credentials and no plaintext
	require.Len(t, store.UpsertCalls(), 1)
	rec := store.UpsertCalls()[0].Rec
	assert.Equal(t, strings.ToLower(userID), rec.UserID)
	assert.NotEmpty(t, rec.EncryptedConnections)
	assert.NotContains(t, string(rec.Data), secretPassword)
	assert.NotContains(t, string(rec.Data), secretKey)
	assert.NotContains(t, string(rec.Data), "torrserver_password")
	assert.Contains(t, string(rec.Data), contentMarker)
	assert.NotContains(t, string(rec.EncryptedConnections), secretPassword)

	got, err := svc.Get(t.Context(), userID)
	require.NoError(t, err)
	assert.Equal(t, stored.UpdatedAt, got.UpdatedAt)
	assert.Equal(t, 1, got.SchemaVersion)
	assert.Len(t, got.Data, len(storage.Sections))
	settings := settingsOf(t, got)
	assert.Equal(t, secretPassword, settings["torrserver_password"])
	assert.Equal(t, secretKey, settings["jackett_key"])
	assert.Equal(t, "ru", settings["language"])
	assert.JSONEq(t, `{"title":"`+contentMarker+`"}`, string(got.Data["favorites"]))
	assert.JSONEq(t, `{}`, string(got.Data["history"]))
}

func TestService_ReplaceWithoutCredentials(t *testing.T) {
	store := memStore()
	svc, _ := newTestService(t, store, newTestSealer(t, 1))

	_, err := svc.Replace(t.Context(), newUserID(t), []byte(`{"schema_version":1,"data":{"settings":{"language":"en"}}}`))
	require.NoError(t, err)
	require.Len(t, store.UpsertCalls(), 1)
	assert.Nil(t, store.UpsertCalls()[0].Rec.EncryptedConnections)
}

func TestService_ReplaceValidationError(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "invalid json", body: `{`, code: storage.CodeInvalidJSON},
		{name: "unknown section", body: `{"schema_version":1,"data":{"nope":{}}}`, code: storage.CodeInvalidDocument},
		{name: "wrong schema version", body: `{"schema_version":2,"data":{}}`, code: storage.CodeUnsupportedSchemaVersion},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := memStore()
			svc, _ := newTestService(t, store, newTestSealer(t, 1))
			_, err := svc.Replace(t.Context(), newUserID(t), []byte(tc.body))
			var verr *storage.ValidationError
			require.ErrorAs(t, err, &verr)
			assert.Same(t, verr, err, "validation error is returned unwrapped")
			assert.Equal(t, tc.code, verr.Code)
			assert.Empty(t, store.UpsertCalls())
		})
	}
}

func TestService_Delete(t *testing.T) {
	store := memStore()
	svc, _ := newTestService(t, store, newTestSealer(t, 1))
	userID := newUserID(t)

	_, err := svc.Replace(t.Context(), userID, testBody)
	require.NoError(t, err)
	require.NoError(t, svc.Delete(t.Context(), strings.ToUpper(userID)))
	require.NoError(t, svc.Delete(t.Context(), userID), "second delete is not an error")

	_, err = svc.Get(t.Context(), userID)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.Len(t, store.DeleteCalls(), 2)
	assert.Equal(t, userID, store.DeleteCalls()[0].UserID)
}

func TestService_GetNotFound(t *testing.T) {
	svc, buf := newTestService(t, memStore(), newTestSealer(t, 1))
	_, err := svc.Get(t.Context(), newUserID(t))
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.NotErrorIs(t, err, storage.ErrUnavailable)
	assert.Empty(t, buf.String(), "a missing document is not logged")
}

func TestService_InvalidUserID(t *testing.T) {
	store := &mocks.StoreMock{} // any call panics
	svc, _ := newTestService(t, store, newTestSealer(t, 1))

	for _, id := range []string{"", "not-a-uuid", "0000000000000000000000000000000000000", "../../etc/passwd"} {
		t.Run(id, func(t *testing.T) {
			_, err := svc.Get(t.Context(), id)
			require.ErrorIs(t, err, storage.ErrInvalidUserID)
			_, err = svc.Replace(t.Context(), id, testBody)
			require.ErrorIs(t, err, storage.ErrInvalidUserID)
			require.ErrorIs(t, svc.Delete(t.Context(), id), storage.ErrInvalidUserID)
		})
	}
}

func TestService_StoreUnavailable(t *testing.T) {
	dbErr := errors.New("dial tcp 10.0.0.5:5432: connection refused")
	store := &mocks.StoreMock{
		GetFunc:    func(context.Context, string) (storage.Record, error) { return storage.Record{}, dbErr },
		UpsertFunc: func(context.Context, storage.Record) (storage.Record, error) { return storage.Record{}, dbErr },
		DeleteFunc: func(context.Context, string) error { return dbErr },
	}
	svc, buf := newTestService(t, store, newTestSealer(t, 1))
	userID := newUserID(t)

	checkErr := func(t *testing.T, err error) {
		t.Helper()
		require.ErrorIs(t, err, storage.ErrUnavailable)
		require.ErrorIs(t, err, dbErr, "driver error stays in the chain")
		require.NotErrorIs(t, err, storage.ErrNotFound)
	}
	_, err := svc.Get(t.Context(), userID)
	checkErr(t, err)
	_, err = svc.Replace(t.Context(), userID, testBody)
	checkErr(t, err)
	checkErr(t, svc.Delete(t.Context(), userID))

	logged := buf.String()
	assert.Contains(t, logged, "[WARN] get user data: ")
	assert.Contains(t, logged, "[WARN] upsert user data: ")
	assert.Contains(t, logged, "[WARN] delete user data: ")
	assert.NotContains(t, logged, secretPassword)
	assert.NotContains(t, logged, secretKey)
	assert.NotContains(t, logged, contentMarker)
}

func TestService_GetConnectionsUnreadable(t *testing.T) {
	store := memStore()
	writer, _ := newTestService(t, store, newTestSealer(t, 1))
	reader, buf := newTestService(t, store, newTestSealer(t, 2)) // different key, same rows
	userID := newUserID(t)

	_, err := writer.Replace(t.Context(), userID, testBody)
	require.NoError(t, err)

	_, err = reader.Get(t.Context(), userID)
	require.ErrorIs(t, err, storage.ErrConnectionsUnreadable)
	logged := buf.String()
	assert.Contains(t, logged, "[ERROR] merge connections of user "+userID)
	assert.NotContains(t, logged, secretPassword)
	assert.NotContains(t, logged, secretKey)
	assert.NotContains(t, logged, contentMarker)
}

func TestService_GetCorruptData(t *testing.T) {
	store := &mocks.StoreMock{
		GetFunc: func(_ context.Context, userID string) (storage.Record, error) {
			return storage.Record{UserID: userID, SchemaVersion: 1, Data: json.RawMessage(`[1,2]`)}, nil
		},
	}
	svc, _ := newTestService(t, store, newTestSealer(t, 1))
	_, err := svc.Get(t.Context(), newUserID(t))
	require.ErrorContains(t, err, "decode stored data")
	require.NotErrorIs(t, err, storage.ErrUnavailable)
}

func TestService_PgStoreRoundTrip(t *testing.T) {
	t.Parallel()
	pool := pgtest.DB(t)
	svc, _ := newTestService(t, newTestStore(t, pool), newTestSealer(t, 3))
	userID := newUserID(t)

	stored, err := svc.Replace(t.Context(), userID, testBody)
	require.NoError(t, err)
	assert.False(t, stored.UpdatedAt.IsZero())

	var data string
	var blob []byte
	err = pool.QueryRow(t.Context(),
		`select data::text, encrypted_connections from lampa_user_data where user_id = $1`, userID).Scan(&data, &blob)
	require.NoError(t, err)
	assert.NotContains(t, data, secretPassword)
	assert.NotContains(t, data, secretKey)
	assert.NotContains(t, data, "torrserver_password")
	assert.NotContains(t, data, "jackett_key")
	assert.Contains(t, data, contentMarker)
	assert.Contains(t, data, `"language": "ru"`)
	assert.NotEmpty(t, blob)
	assert.NotContains(t, string(blob), secretPassword)

	got, err := svc.Get(t.Context(), userID)
	require.NoError(t, err)
	assert.True(t, stored.UpdatedAt.Equal(got.UpdatedAt))
	settings := settingsOf(t, got)
	assert.Equal(t, secretPassword, settings["torrserver_password"])
	assert.Equal(t, secretKey, settings["jackett_key"])
	assert.Len(t, got.Data, len(storage.Sections))
}
