package storage_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/migrations"
	"github.com/SvetlovA/lampa/backend/pkg/storage"
	"github.com/SvetlovA/lampa/backend/pkg/storage/pgtest"
)

// emptyDB creates a new, unmigrated database in the test postgres and returns a pool connected to it.
func emptyDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	shared := pgtest.DB(t)

	suffix := make([]byte, 8)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	name := pgx.Identifier{"test_" + hex.EncodeToString(suffix)}
	_, err = shared.Exec(t.Context(), "create database "+name.Sanitize())
	require.NoError(t, err)

	cfg := shared.Config().Copy()
	cfg.ConnConfig.Database = name[0]
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		p.Close()
		_, dropErr := shared.Exec(context.Background(), "drop database if exists "+name.Sanitize()+" with (force)")
		assert.NoError(t, dropErr)
	})
	return p
}

func tableExists(t *testing.T, p *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	err := p.QueryRow(t.Context(), `select exists (select 1 from pg_tables where schemaname = 'public' and tablename = $1)`, name).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func TestMigrate_nilPool(t *testing.T) {
	require.Error(t, storage.Migrate(t.Context(), nil))
}

func TestMigrate(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)
	require.False(t, tableExists(t, p, "lampa_user_data"))
	require.False(t, tableExists(t, p, "lampa_torrserver_hashes"))

	require.NoError(t, storage.Migrate(t.Context(), p))
	assert.True(t, tableExists(t, p, "lampa_user_data"))
	require.True(t, tableExists(t, p, "lampa_torrserver_hashes"))

	userID, hash := newUserID(t), strings.Repeat("a", 40)
	var createdAt time.Time
	err := p.QueryRow(t.Context(), `insert into lampa_torrserver_hashes (user_id, server_id, hash)
		values ($1, 'server', $2) returning created_at`, userID, hash).Scan(&createdAt)
	require.NoError(t, err, "ownership does not require a user-data document")
	assert.False(t, createdAt.IsZero(), "created_at defaults to now")

	require.NoError(t, storage.Migrate(t.Context(), p), "migrate is idempotent")
	assert.True(t, tableExists(t, p, "lampa_user_data"))
	assert.True(t, tableExists(t, p, "lampa_torrserver_hashes"))
	var retainedCreatedAt time.Time
	err = p.QueryRow(t.Context(), `select created_at from lampa_torrserver_hashes
		where user_id = $1 and server_id = 'server' and hash = $2`, userID, hash).Scan(&retainedCreatedAt)
	require.NoError(t, err)
	assert.True(t, createdAt.Equal(retainedCreatedAt), "a second migration preserves ownership rows")
}

func TestMigrate_torrserverConstraints(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)
	require.NoError(t, storage.Migrate(t.Context(), p))

	tests := []struct {
		name     string
		serverID string
		hash     string
		valid    bool
	}{
		{name: "v1 hash", serverID: "123", hash: strings.Repeat("a", 40), valid: true},
		{name: "v2 hash", serverID: "Az09_-", hash: strings.Repeat("0123456789abcdef", 4), valid: true},
		{name: "one character server", serverID: "a", hash: strings.Repeat("b", 40), valid: true},
		{name: "maximum server length", serverID: strings.Repeat("a", 64), hash: strings.Repeat("c", 40), valid: true},
		{name: "empty server", hash: strings.Repeat("a", 40)},
		{name: "server too long", serverID: strings.Repeat("a", 65), hash: strings.Repeat("a", 40)},
		{name: "server URL", serverID: "http://server", hash: strings.Repeat("a", 40)},
		{name: "server whitespace", serverID: " server ", hash: strings.Repeat("a", 40)},
		{name: "server newline", serverID: "server\n", hash: strings.Repeat("a", 40)},
		{name: "empty hash", serverID: "server"},
		{name: "short v1 hash", serverID: "server", hash: strings.Repeat("a", 39)},
		{name: "long v1 hash", serverID: "server", hash: strings.Repeat("a", 41)},
		{name: "short v2 hash", serverID: "server", hash: strings.Repeat("a", 63)},
		{name: "long v2 hash", serverID: "server", hash: strings.Repeat("a", 65)},
		{name: "uppercase hash", serverID: "server", hash: strings.Repeat("A", 40)},
		{name: "nonhex hash", serverID: "server", hash: strings.Repeat("g", 40)},
		{name: "hash newline", serverID: "server", hash: strings.Repeat("a", 40) + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			userID := newUserID(t)
			_, err := p.Exec(t.Context(), `insert into lampa_torrserver_hashes (user_id, server_id, hash)
				values ($1, $2, $3)`, userID, tc.serverID, tc.hash)
			if !tc.valid {
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr)
				assert.Equal(t, "23514", pgErr.Code, "check constraint violation")
				return
			}
			require.NoError(t, err)
			_, err = p.Exec(t.Context(), `insert into lampa_torrserver_hashes (user_id, server_id, hash)
				values ($1, $2, $3)`, userID, tc.serverID, tc.hash)
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr)
			assert.Equal(t, "23505", pgErr.Code, "duplicate membership violates the primary key")
		})
	}
}

func TestMigrate_torrserverDown(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)
	require.NoError(t, storage.Migrate(t.Context(), p))

	db := stdlib.OpenDBFromPool(p)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	require.NoError(t, err)
	_, err = provider.Down(t.Context()) // v2 document migration
	require.NoError(t, err)
	_, err = provider.Down(t.Context()) // torrent ownership migration
	require.NoError(t, err)
	assert.False(t, tableExists(t, p, "lampa_torrserver_hashes"))
	assert.True(t, tableExists(t, p, "lampa_user_data"), "rollback preserves the existing document table")

	require.NoError(t, storage.Migrate(t.Context(), p), "ownership migration can be reapplied after rollback")
	assert.True(t, tableExists(t, p, "lampa_torrserver_hashes"))
}

func TestMigrate_concurrent(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)

	const callers = 2
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() { errs[i] = storage.Migrate(t.Context(), p) })
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "caller %d", i)
	}
	assert.True(t, tableExists(t, p, "lampa_user_data"))
	assert.True(t, tableExists(t, p, "lampa_torrserver_hashes"))
}

func TestMigrate_canceledContext(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, storage.Migrate(ctx, p))
	assert.False(t, tableExists(t, p, "lampa_user_data"))
	assert.False(t, tableExists(t, p, "lampa_torrserver_hashes"))
}

func TestMigrate_userDataV2(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)
	db := stdlib.OpenDBFromPool(p)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	require.NoError(t, err)
	_, err = provider.UpTo(t.Context(), 2)
	require.NoError(t, err)
	id := newUserID(t)
	_, err = p.Exec(t.Context(), `insert into lampa_user_data (user_id, schema_version, data, encrypted_connections)
		values ($1, 1, '{"settings":{"language":"ru"}}', '\x010203')`, id)
	require.NoError(t, err)
	require.NoError(t, storage.Migrate(t.Context(), p))

	var etag string
	var version int
	var data string
	err = p.QueryRow(t.Context(), `select schema_version, data::text, etag from lampa_user_data where user_id = $1`, id).
		Scan(&version, &data, &etag)
	require.NoError(t, err)
	assert.Equal(t, 1, version, "old rows are retained but read as absent")
	assert.JSONEq(t, `{"settings":{"language":"ru"}}`, data)
	assert.Empty(t, etag, "etag defaults to an empty non-null string")
	var oldColumn bool
	err = p.QueryRow(t.Context(), `select exists (select 1 from information_schema.columns
		where table_schema = 'public' and table_name = 'lampa_user_data' and column_name = 'encrypted_connections')`).Scan(&oldColumn)
	require.NoError(t, err)
	assert.False(t, oldColumn)
	svc, _ := newTestService(t, newTestStore(t, p), newTestSealer(t, 1))
	_, err = svc.Get(t.Context(), id)
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = svc.Replace(t.Context(), id, testBody, nil)
	require.NoError(t, err, "a v2 save replaces the old document")
	got, err := svc.Get(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, 2, got.SchemaVersion)

	_, err = provider.Down(t.Context())
	require.NoError(t, err)
	var blob []byte
	err = p.QueryRow(t.Context(), `select encrypted_connections from lampa_user_data where user_id = $1`, id).Scan(&blob)
	require.NoError(t, err, "down restores the old column without pretending to recover old credentials")
	assert.Nil(t, blob)
	require.NoError(t, storage.Migrate(t.Context(), p), "v2 migration can be reapplied")
}
