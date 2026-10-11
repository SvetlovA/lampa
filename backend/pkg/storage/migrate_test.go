package storage_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func tableExists(t *testing.T, p *pgxpool.Pool) bool {
	t.Helper()
	var exists bool
	err := p.QueryRow(t.Context(), `select exists (select 1 from pg_tables where tablename = 'lampa_user_data')`).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func TestMigrate_nilPool(t *testing.T) {
	require.Error(t, storage.Migrate(t.Context(), nil))
}

func TestMigrate(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)
	require.False(t, tableExists(t, p))

	require.NoError(t, storage.Migrate(t.Context(), p))
	assert.True(t, tableExists(t, p))

	require.NoError(t, storage.Migrate(t.Context(), p), "migrate is idempotent")
	assert.True(t, tableExists(t, p))
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
	assert.True(t, tableExists(t, p))
}

func TestMigrate_canceledContext(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, storage.Migrate(ctx, p))
	assert.False(t, tableExists(t, p))
}
