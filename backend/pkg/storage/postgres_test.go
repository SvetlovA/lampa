package storage_test

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
	"github.com/SvetlovA/lampa/backend/pkg/storage/pgtest"
)

// newUserID returns a random canonical uuid, so tests sharing the database never touch the same row.
func newUserID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func newTestStore(t *testing.T, p *pgxpool.Pool) *storage.PgStore {
	t.Helper()
	s, err := storage.NewPgStore(p)
	require.NoError(t, err)
	return s
}

func testRecord(userID, data string) storage.Record {
	return storage.Record{UserID: userID, SchemaVersion: 2, Data: json.RawMessage(data)}
}

func TestNewPgStore_nilPool(t *testing.T) {
	_, err := storage.NewPgStore(nil)
	require.Error(t, err)
}

func TestPgStore_GetMissing(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	_, err := s.Get(t.Context(), newUserID(t))
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestPgStore_InsertThenGet(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))

	userID := newUserID(t)
	const data = `{"string":"x","null":null,"list":[1,true],"object":{"a":1},"secret":{"$sealed":"encoded"}}`
	stored, err := s.Upsert(t.Context(), testRecord(userID, data))
	require.NoError(t, err)
	assert.Equal(t, userID, stored.UserID)
	assert.False(t, stored.CreatedAt.IsZero())
	assert.True(t, stored.CreatedAt.Equal(stored.UpdatedAt))
	got, err := s.Get(t.Context(), userID)
	require.NoError(t, err)
	assert.Equal(t, userID, got.UserID)
	assert.Equal(t, 2, got.SchemaVersion)
	assert.JSONEq(t, data, string(got.Data))
	assert.True(t, stored.CreatedAt.Equal(got.CreatedAt))
	assert.True(t, stored.UpdatedAt.Equal(got.UpdatedAt))
}

func TestPgStore_UpsertReplaces(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	userID := newUserID(t)

	first, err := s.Upsert(t.Context(), testRecord(userID, `{"settings": {"a": 1}}`))
	require.NoError(t, err)
	second, err := s.Upsert(t.Context(), testRecord(userID, `{"favorites": {"b": 2}}`))
	require.NoError(t, err)

	assert.True(t, first.CreatedAt.Equal(second.CreatedAt), "created_at is kept")
	assert.True(t, second.UpdatedAt.After(first.UpdatedAt), "updated_at is bumped")

	got, err := s.Get(t.Context(), userID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"favorites": {"b": 2}}`, string(got.Data), "data is replaced, not merged")
	assert.True(t, second.UpdatedAt.Equal(got.UpdatedAt))
}

func TestPgStore_Delete(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	userID := newUserID(t)

	_, err := s.Upsert(t.Context(), testRecord(userID, `{}`))
	require.NoError(t, err)

	require.NoError(t, s.Delete(t.Context(), userID))
	_, err = s.Get(t.Context(), userID)
	require.ErrorIs(t, err, storage.ErrNotFound)

	require.NoError(t, s.Delete(t.Context(), userID), "delete is idempotent")
}

func TestPgStore_UsersIsolated(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	alice, bob := newUserID(t), newUserID(t)

	_, err := s.Upsert(t.Context(), testRecord(alice, `{"settings": {"who": "alice"}}`))
	require.NoError(t, err)
	_, err = s.Upsert(t.Context(), testRecord(bob, `{"settings": {"who": "bob"}}`))
	require.NoError(t, err)

	require.NoError(t, s.Delete(t.Context(), alice))

	got, err := s.Get(t.Context(), bob)
	require.NoError(t, err)
	assert.JSONEq(t, `{"settings": {"who": "bob"}}`, string(got.Data))
}

func TestPgStore_InvalidUserID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))

	_, err := s.Get(t.Context(), "not-a-uuid")
	require.Error(t, err)
	require.NotErrorIs(t, err, storage.ErrNotFound)
	_, err = s.Upsert(t.Context(), testRecord("not-a-uuid", `{}`))
	require.Error(t, err)
	require.Error(t, s.Delete(t.Context(), "not-a-uuid"))
}

func TestPgStore_Ping(t *testing.T) {
	t.Parallel()

	t.Run("migrated database", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, newTestStore(t, pgtest.DB(t)).Ping(t.Context()))
	})

	t.Run("schema missing", func(t *testing.T) {
		t.Parallel()
		require.Error(t, newTestStore(t, emptyDB(t)).Ping(t.Context()))
	})

	for _, table := range []string{"lampa_user_data", "lampa_torrserver_hashes"} {
		t.Run(table+" missing", func(t *testing.T) {
			t.Parallel()
			p := emptyDB(t)
			require.NoError(t, storage.Migrate(t.Context(), p))
			_, err := p.Exec(t.Context(), "drop table "+table)
			require.NoError(t, err)
			err = newTestStore(t, p).Ping(t.Context())
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr)
			assert.Equal(t, "42P01", pgErr.Code, "missing table error remains wrapped")
			if table == "lampa_torrserver_hashes" {
				require.ErrorContains(t, err, "probe torrserver hashes schema")
			} else {
				require.ErrorContains(t, err, "probe schema")
			}
		})
	}

	t.Run("closed pool", func(t *testing.T) {
		t.Parallel()
		p, err := pgxpool.NewWithConfig(t.Context(), pgtest.DB(t).Config().Copy())
		require.NoError(t, err)
		p.Close()
		require.Error(t, newTestStore(t, p).Ping(t.Context()))
	})
}

func TestPgStore_UpsertRejectsUnstorableNumber(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	userID := newUserID(t)

	// more digits than jsonb's numeric holds (131072 before the decimal point).
	// the store reports content rejection so the service can return a client error.
	data := `{"n":1` + strings.Repeat("0", 140000) + `}`
	_, err := s.Upsert(t.Context(), testRecord(userID, data))
	require.ErrorIs(t, err, storage.ErrInvalidData)
	require.NotErrorIs(t, err, storage.ErrNotFound)

	_, err = s.Get(t.Context(), userID)
	require.ErrorIs(t, err, storage.ErrNotFound, "nothing was stored")
}

func TestPgStore_Update(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	id := newUserID(t)
	rec := testRecord(id, `{"value":"first"}`)
	rec.ETag = "first-tag"
	first, err := s.Upsert(t.Context(), rec)
	require.NoError(t, err)
	got, err := s.Get(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, rec.ETag, got.ETag)

	rec.Data = json.RawMessage(`{"value":"second"}`)
	rec.ETag = "second-tag"
	second, err := s.Update(t.Context(), rec, first.ETag)
	require.NoError(t, err)
	assert.Equal(t, first.CreatedAt, second.CreatedAt)
	assert.True(t, second.UpdatedAt.After(first.UpdatedAt))

	rec.Data = json.RawMessage(`{"value":"stale"}`)
	rec.ETag = "stale-tag"
	_, err = s.Update(t.Context(), rec, first.ETag)
	require.ErrorIs(t, err, storage.ErrPreconditionFailed)
	got, err = s.Get(t.Context(), id)
	require.NoError(t, err)
	assert.JSONEq(t, string(second.Data), string(got.Data))
	assert.Equal(t, second.ETag, got.ETag)
	assert.Equal(t, second.CreatedAt, got.CreatedAt)
	assert.Equal(t, second.UpdatedAt, got.UpdatedAt, "a stale update must not bump timestamps")

	// an update for a different/missing user cannot change the original row or insert.
	rec.UserID = newUserID(t)
	_, err = s.Update(t.Context(), rec, second.ETag)
	require.ErrorIs(t, err, storage.ErrPreconditionFailed)
	_, err = s.Get(t.Context(), rec.UserID)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.NoError(t, s.Delete(t.Context(), id))
	rec.UserID = id
	_, err = s.Update(t.Context(), rec, second.ETag)
	require.ErrorIs(t, err, storage.ErrPreconditionFailed, "a deleted record must not be recreated")
}

func TestPgStore_UpdateFailures(t *testing.T) {
	t.Parallel()
	pool := pgtest.DB(t)
	s := newTestStore(t, pool)
	rec := testRecord(newUserID(t), `{}`)
	rec.ETag = "old"
	_, err := s.Upsert(t.Context(), rec)
	require.NoError(t, err)
	rec.Data = json.RawMessage(`{"n":1` + strings.Repeat("0", 140000) + `}`)
	_, err = s.Update(t.Context(), rec, "old")
	require.ErrorIs(t, err, storage.ErrInvalidData)
	got, err := s.Get(t.Context(), rec.UserID)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(got.Data))
	assert.Equal(t, "old", got.ETag)

	closed, err := pgxpool.NewWithConfig(t.Context(), pool.Config().Copy())
	require.NoError(t, err)
	closed.Close()
	_, err = newTestStore(t, closed).Update(t.Context(), rec, "old")
	require.Error(t, err)
	require.NotErrorIs(t, err, storage.ErrPreconditionFailed)
}
