package storage_test

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"

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

func testRecord(userID, data string, blob []byte) storage.Record {
	return storage.Record{UserID: userID, SchemaVersion: 1, Data: json.RawMessage(data), EncryptedConnections: blob}
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

	tests := []struct {
		name string
		blob []byte
	}{
		{name: "with connections", blob: []byte{0x01, 0x02, 0x03}},
		{name: "without connections", blob: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			userID := newUserID(t)
			stored, err := s.Upsert(t.Context(), testRecord(userID, `{"settings": {"a": 1}, "other": {}}`, tc.blob))
			require.NoError(t, err)
			assert.Equal(t, userID, stored.UserID)
			assert.False(t, stored.CreatedAt.IsZero())
			assert.True(t, stored.CreatedAt.Equal(stored.UpdatedAt))

			got, err := s.Get(t.Context(), userID)
			require.NoError(t, err)
			assert.Equal(t, userID, got.UserID)
			assert.Equal(t, 1, got.SchemaVersion)
			assert.JSONEq(t, `{"settings": {"a": 1}, "other": {}}`, string(got.Data))
			assert.Equal(t, tc.blob, got.EncryptedConnections)
			assert.True(t, stored.CreatedAt.Equal(got.CreatedAt))
			assert.True(t, stored.UpdatedAt.Equal(got.UpdatedAt))
		})
	}
}

func TestPgStore_UpsertReplaces(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	userID := newUserID(t)

	first, err := s.Upsert(t.Context(), testRecord(userID, `{"settings": {"a": 1}}`, []byte{0x01}))
	require.NoError(t, err)
	second, err := s.Upsert(t.Context(), testRecord(userID, `{"favorites": {"b": 2}}`, nil))
	require.NoError(t, err)

	assert.True(t, first.CreatedAt.Equal(second.CreatedAt), "created_at is kept")
	assert.True(t, second.UpdatedAt.After(first.UpdatedAt), "updated_at is bumped")

	got, err := s.Get(t.Context(), userID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"favorites": {"b": 2}}`, string(got.Data), "data is replaced, not merged")
	assert.Nil(t, got.EncryptedConnections, "connections are cleared")
	assert.True(t, second.UpdatedAt.Equal(got.UpdatedAt))
}

func TestPgStore_Delete(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	userID := newUserID(t)

	_, err := s.Upsert(t.Context(), testRecord(userID, `{}`, nil))
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

	_, err := s.Upsert(t.Context(), testRecord(alice, `{"settings": {"who": "alice"}}`, []byte{0xa}))
	require.NoError(t, err)
	_, err = s.Upsert(t.Context(), testRecord(bob, `{"settings": {"who": "bob"}}`, []byte{0xb}))
	require.NoError(t, err)

	require.NoError(t, s.Delete(t.Context(), alice))

	got, err := s.Get(t.Context(), bob)
	require.NoError(t, err)
	assert.JSONEq(t, `{"settings": {"who": "bob"}}`, string(got.Data))
	assert.Equal(t, []byte{0xb}, got.EncryptedConnections)
}

func TestPgStore_InvalidUserID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))

	_, err := s.Get(t.Context(), "not-a-uuid")
	require.Error(t, err)
	require.NotErrorIs(t, err, storage.ErrNotFound)
	_, err = s.Upsert(t.Context(), testRecord("not-a-uuid", `{}`, nil))
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

	t.Run("closed pool", func(t *testing.T) {
		t.Parallel()
		p, err := pgxpool.NewWithConfig(t.Context(), pgtest.DB(t).Config().Copy())
		require.NoError(t, err)
		p.Close()
		require.Error(t, newTestStore(t, p).Ping(t.Context()))
	})
}
