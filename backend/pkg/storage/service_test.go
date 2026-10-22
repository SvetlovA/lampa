package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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

var testLimits = storage.Limits{MaxBodyBytes: 1 << 16, MaxDepth: storage.DefaultMaxDepth}

// testBody includes client-selected secrets, a plain string and an object value.
var testBody = []byte(`{"schema_version":2,"data":{"secret.password":"` + secretPassword +
	`","secret:key":"` + secretKey + `","language":"ru","favorites":{"title":"` + contentMarker +
	`"}},"encrypt":["secret.password","secret:key","missing"]}`)

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

func stringValue(t *testing.T, doc storage.Document, key string) string {
	t.Helper()
	var value string
	require.NoError(t, json.Unmarshal(doc.Data[key], &value))
	return value
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
			limits: storage.Limits{MaxDepth: 1}},
		{name: "zero depth", store: memStore(), sealer: sealer, logger: logger, wantErr: "limits must be positive",
			limits: storage.Limits{MaxBodyBytes: 1}},
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

	stored, err := svc.Replace(t.Context(), userID, testBody, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, stored.SchemaVersion)
	assert.Equal(t, time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC), stored.UpdatedAt)
	assert.Len(t, stored.Data, 4)
	assert.Equal(t, secretPassword, stringValue(t, stored, "secret.password"))

	// the record reaching the store has a lower case id, sealed credentials and no plaintext
	require.Len(t, store.UpsertCalls(), 1)
	rec := store.UpsertCalls()[0].Rec
	assert.Equal(t, strings.ToLower(userID), rec.UserID)
	assert.Contains(t, string(rec.Data), `"$sealed"`)
	assert.NotContains(t, string(rec.Data), secretPassword)
	assert.NotContains(t, string(rec.Data), secretKey)
	assert.Contains(t, string(rec.Data), "secret.password")
	assert.Contains(t, string(rec.Data), contentMarker)

	got, err := svc.Get(t.Context(), userID)
	require.NoError(t, err)
	assert.Equal(t, stored.UpdatedAt, got.UpdatedAt)
	assert.Equal(t, 2, got.SchemaVersion)
	assert.Empty(t, got.UnreadableKeys)
	assert.Len(t, got.Data, 4)
	assert.Equal(t, secretPassword, stringValue(t, got, "secret.password"))
	assert.Equal(t, secretKey, stringValue(t, got, "secret:key"))
	assert.Equal(t, "ru", stringValue(t, got, "language"))
	assert.JSONEq(t, `{"title":"`+contentMarker+`"}`, string(got.Data["favorites"]))
	assert.NotContains(t, got.Data, "history")
}

func TestService_ReplaceWithoutCredentials(t *testing.T) {
	store := memStore()
	svc, _ := newTestService(t, store, newTestSealer(t, 1))

	_, err := svc.Replace(t.Context(), newUserID(t), []byte(`{"schema_version":2,"data":{"settings":{"language":"en"}}}`), nil)
	require.NoError(t, err)
	require.Len(t, store.UpsertCalls(), 1)
	assert.JSONEq(t, `{"settings":{"language":"en"}}`, string(store.UpsertCalls()[0].Rec.Data))
}

func TestService_ReplaceValidationError(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "invalid json", body: `{`, code: storage.CodeInvalidJSON},
		{name: "reserved object", body: `{"schema_version":2,"data":{"key":{"$sealed":"x"}}}`, code: storage.CodeInvalidDocument},
		{name: "wrong schema version", body: `{"schema_version":1,"data":{}}`, code: storage.CodeUnsupportedSchemaVersion},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := memStore()
			svc, _ := newTestService(t, store, newTestSealer(t, 1))
			_, err := svc.Replace(t.Context(), newUserID(t), []byte(tc.body), nil)
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

	_, err := svc.Replace(t.Context(), userID, testBody, nil)
	require.NoError(t, err)
	require.NoError(t, svc.Delete(t.Context(), strings.ToUpper(userID)))
	require.NoError(t, svc.Delete(t.Context(), userID), "second delete is not an error")

	_, err = svc.Get(t.Context(), userID)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.Len(t, store.DeleteCalls(), 2)
	assert.Equal(t, userID, store.DeleteCalls()[0].UserID)
}

func TestService_UsersIsolated(t *testing.T) {
	store := memStore()
	svc, _ := newTestService(t, store, newTestSealer(t, 1))
	alice, bob := newUserID(t), newUserID(t)

	_, err := svc.Replace(t.Context(), alice, testBody, nil)
	require.NoError(t, err)
	_, err = svc.Get(t.Context(), bob)
	require.ErrorIs(t, err, storage.ErrNotFound, "one user's write is invisible to another")

	_, err = svc.Replace(t.Context(), bob, []byte(`{"schema_version":2,"data":{"settings":{"language":"en"}}}`), nil)
	require.NoError(t, err)
	require.NoError(t, svc.Delete(t.Context(), bob))

	got, err := svc.Get(t.Context(), alice)
	require.NoError(t, err, "another user's replace and delete leave the document intact")
	assert.Equal(t, secretPassword, stringValue(t, got, "secret.password"))
	assert.JSONEq(t, `{"title":"`+contentMarker+`"}`, string(got.Data["favorites"]))

	// sealed credentials are bound to their owner: a row copied to another user id cannot be opened
	rec, err := store.GetFunc(t.Context(), alice)
	require.NoError(t, err)
	rec.UserID = bob
	_, err = store.UpsertFunc(t.Context(), rec)
	require.NoError(t, err)
	got, err = svc.Get(t.Context(), bob)
	require.NoError(t, err)
	assert.NotContains(t, got.Data, "secret.password")
	assert.NotContains(t, got.Data, "secret:key")
	assert.Equal(t, "ru", stringValue(t, got, "language"))
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
			_, err = svc.Replace(t.Context(), id, testBody, nil)
			require.ErrorIs(t, err, storage.ErrInvalidUserID)
			require.ErrorIs(t, svc.Delete(t.Context(), id), storage.ErrInvalidUserID)
		})
	}
}

func TestService_StoreRejectsContent(t *testing.T) {
	dbErr := errors.New("ERROR: value overflows numeric format (SQLSTATE 22003)")
	store := &mocks.StoreMock{
		UpsertFunc: func(context.Context, storage.Record) (storage.Record, error) {
			return storage.Record{}, errors.Join(storage.ErrInvalidData, dbErr)
		},
	}
	svc, buf := newTestService(t, store, newTestSealer(t, 1))

	_, err := svc.Replace(t.Context(), newUserID(t), testBody, nil)
	var verr *storage.ValidationError
	require.ErrorAs(t, err, &verr, "rejected content is a client error, not unavailability")
	assert.Equal(t, storage.CodeInvalidDocument, verr.Code)
	require.NotErrorIs(t, err, storage.ErrUnavailable)

	logged := buf.String()
	assert.Contains(t, logged, "[WARN] upsert user data: ")
	assert.Contains(t, logged, "SQLSTATE 22003")
	assert.NotContains(t, logged, secretPassword)
	assert.NotContains(t, logged, contentMarker)
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
	_, err = svc.Replace(t.Context(), userID, testBody, nil)
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

	stored, err := writer.Replace(t.Context(), userID, testBody, nil)
	require.NoError(t, err)

	got, err := reader.Get(t.Context(), userID)
	require.NoError(t, err)
	assert.Equal(t, []string{"secret.password", "secret:key"}, got.UnreadableKeys)
	assert.Equal(t, stored.ETag, got.ETag, "retain the stored tag for a conditional repair")
	assert.NotContains(t, got.Data, "secret.password")
	assert.NotContains(t, got.Data, "secret:key")
	assert.Equal(t, "ru", stringValue(t, got, "language"))
	logged := buf.String()
	assert.Contains(t, logged, "[WARN] omit unreadable sealed value for user "+userID)
	assert.NotContains(t, logged, secretPassword)
	assert.NotContains(t, logged, secretKey)
	assert.NotContains(t, logged, contentMarker)
}

func TestService_GetCorruptData(t *testing.T) {
	store := &mocks.StoreMock{
		GetFunc: func(_ context.Context, userID string) (storage.Record, error) {
			return storage.Record{UserID: userID, SchemaVersion: 2, Data: json.RawMessage(`[1,2]`)}, nil
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

	stored, err := svc.Replace(t.Context(), userID, testBody, nil)
	require.NoError(t, err)
	assert.False(t, stored.UpdatedAt.IsZero())

	var data string
	err = pool.QueryRow(t.Context(),
		`select data::text from lampa_user_data where user_id = $1`, userID).Scan(&data)
	require.NoError(t, err)
	assert.NotContains(t, data, secretPassword)
	assert.NotContains(t, data, secretKey)
	assert.NotContains(t, data, "torrserver_password")
	assert.NotContains(t, data, "jackett_key")
	assert.Contains(t, data, contentMarker)
	assert.Contains(t, data, `"language": "ru"`)
	assert.Contains(t, data, `"$sealed"`)

	got, err := svc.Get(t.Context(), userID)
	require.NoError(t, err)
	assert.True(t, stored.UpdatedAt.Equal(got.UpdatedAt))
	assert.Equal(t, secretPassword, stringValue(t, got, "secret.password"))
	assert.Equal(t, secretKey, stringValue(t, got, "secret:key"))
	assert.Len(t, got.Data, 4)
}

func TestService_OldSchemaNotFound(t *testing.T) {
	store := memStore()
	svc, _ := newTestService(t, store, newTestSealer(t, 1))
	id := newUserID(t)
	for _, version := range []int{1, 0, 3} {
		_, err := store.Upsert(t.Context(), storage.Record{UserID: id, SchemaVersion: version, Data: json.RawMessage(`{}`)})
		require.NoError(t, err)
		_, err = svc.Get(t.Context(), id)
		require.ErrorIs(t, err, storage.ErrNotFound)
	}
}

func TestService_AnyValuesRoundTrip(t *testing.T) {
	for _, database := range []bool{false, true} {
		t.Run(fmt.Sprintf("database=%t", database), func(t *testing.T) {
			var store storage.Store = memStore()
			if database {
				store = newTestStore(t, pgtest.DB(t))
			}
			svc, _ := newTestService(t, store, newTestSealer(t, 1))
			const raw = `{"schema_version":2,"data":{"text":"<a&b>","number":12345678901234567890,"bool":false,"null":null,"list":[1,"x"],"obj":{"nested":true},"unlisted":"plain-secret"},"encrypt":["text","number","bool","null","list","obj","missing"]}`
			id := newUserID(t)
			_, err := svc.Replace(t.Context(), id, []byte(raw), nil)
			require.NoError(t, err)
			rec, err := store.Get(t.Context(), id)
			require.NoError(t, err)
			assert.NotContains(t, string(rec.Data), "nested")
			assert.NotContains(t, string(rec.Data), "12345678901234567890")
			assert.Contains(t, string(rec.Data), "plain-secret")
			got, err := svc.Get(t.Context(), id)
			require.NoError(t, err)
			var envelope struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &envelope))
			for key, expected := range envelope.Data {
				assert.JSONEq(t, string(expected), string(got.Data[key]), key)
			}
			assert.Len(t, got.Data, len(envelope.Data))
		})
	}
}

func TestService_ETagCanonicalData(t *testing.T) {
	store := memStore()
	svc, _ := newTestService(t, store, newTestSealer(t, 1))
	id := newUserID(t)
	const canonical = `{"nested":{"a":[{"a":null,"b":true}],"z":1},"secret":"é"}`
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
	bodies := []string{
		`{"schema_version":2,"data":{"secret":"é", "nested": {"z":1, "a":[{"b":true,"a":null}]}},"encrypt":["secret"]}`,
		`{"schema_version":2,"data":{"nested":{"a":[{"a":null,"b":true}],"z":1},"secret":"\u00e9"},"encrypt":["secret"]}`,
		`{"schema_version":2,"data":` + canonical + `}`,
	}
	for _, body := range bodies {
		stored, err := svc.Replace(t.Context(), id, []byte(body), nil)
		require.NoError(t, err)
		assert.Equal(t, want, stored.ETag)
		assert.Equal(t, want, store.UpsertCalls()[len(store.UpsertCalls())-1].Rec.ETag)
		got, err := svc.Get(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, stored.ETag, got.ETag)
	}
	tags := make([]string, 0, 3)
	for _, data := range []string{`{"n":9007199254740992}`, `{"n":9007199254740993}`, `{"n":1.00000000000000001}`} {
		doc, err := svc.Replace(t.Context(), id, []byte(`{"schema_version":2,"data":`+data+`}`), nil)
		require.NoError(t, err)
		tags = append(tags, doc.ETag)
	}
	assert.NotEqual(t, tags[0], tags[1], "canonicalization must not round large integers")
	assert.NotEqual(t, tags[1], tags[2], "changed data gets a new tag")
}

func TestService_ConditionalReplace(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "updated"},
		{name: "stale", err: storage.ErrPreconditionFailed},
		{name: "unavailable", err: errors.New("database disconnected")},
		{name: "invalid content", err: storage.ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &mocks.StoreMock{UpdateFunc: func(_ context.Context, rec storage.Record, _ string) (storage.Record, error) {
				return rec, tc.err
			}}
			svc, logs := newTestService(t, store, newTestSealer(t, 1))
			expected := "old-tag"
			doc, err := svc.Replace(t.Context(), newUserID(t), testBody, &expected)
			switch tc.name {
			case "updated":
				require.NoError(t, err)
				assert.Len(t, doc.ETag, 64)
			case "stale":
				require.ErrorIs(t, err, storage.ErrPreconditionFailed)
				require.NotErrorIs(t, err, storage.ErrUnavailable)
				assert.Empty(t, logs.String())
			case "unavailable":
				require.ErrorIs(t, err, storage.ErrUnavailable)
				require.ErrorIs(t, err, tc.err)
			case "invalid content":
				var verr *storage.ValidationError
				require.ErrorAs(t, err, &verr)
				assert.Equal(t, storage.CodeInvalidDocument, verr.Code)
			}
			require.Len(t, store.UpdateCalls(), 1)
			assert.Equal(t, expected, store.UpdateCalls()[0].ExpectedETag)
			assert.Contains(t, string(store.UpdateCalls()[0].Rec.Data), `"$sealed"`)
			assert.NotContains(t, string(store.UpdateCalls()[0].Rec.Data), secretPassword)
			assert.Empty(t, store.UpsertCalls(), "a conditional write must never fall back to upsert")
		})
	}
}

func TestService_ConcurrentConditionalReplace(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, pgtest.DB(t))
	svc, _ := newTestService(t, store, newTestSealer(t, 1))
	id := newUserID(t)
	first, err := svc.Replace(t.Context(), id, testBody, nil)
	require.NoError(t, err)
	start := make(chan struct{})
	type result struct {
		doc storage.Document
		err error
	}
	results := make(chan result, 2)
	for _, value := range []string{"en", "de"} {
		go func() {
			<-start
			body := []byte(`{"schema_version":2,"data":{"language":"` + value + `"}}`)
			doc, writeErr := svc.Replace(t.Context(), id, body, &first.ETag)
			results <- result{doc: doc, err: writeErr}
		}()
	}
	close(start)
	var winner storage.Document
	var successes, conflicts int
	for range 2 {
		res := <-results
		if res.err == nil {
			successes++
			winner = res.doc
		} else {
			require.ErrorIs(t, res.err, storage.ErrPreconditionFailed)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
	got, err := svc.Get(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, winner.ETag, got.ETag)
	assert.Equal(t, stringValue(t, winner, "language"), stringValue(t, got, "language"))
}
