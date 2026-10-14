package storage_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
	"github.com/SvetlovA/lampa/backend/pkg/storage/pgtest"
)

var _ storage.TorrentStore = (*storage.PgStore)(nil)

func TestPgStore_ListHashes(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	userID := newUserID(t)

	hashes, err := s.ListHashes(t.Context(), userID, "server")
	require.NoError(t, err)
	assert.Equal(t, []string{}, hashes, "an empty list must encode as [] rather than null")

	want := []string{strings.Repeat("0", 40), strings.Repeat("a", 64), strings.Repeat("f", 40)}
	for _, hash := range slices.Backward(want) {
		require.NoError(t, s.ClaimHash(t.Context(), userID, "server", hash))
	}
	hashes, err = s.ListHashes(t.Context(), userID, "server")
	require.NoError(t, err)
	assert.Equal(t, want, hashes)
}

func TestPgStore_ClaimHashIdempotent(t *testing.T) {
	t.Parallel()
	p := pgtest.DB(t)
	s := newTestStore(t, p)
	userID, hash := newUserID(t), strings.Repeat("a", 40)
	require.NoError(t, s.ClaimHash(t.Context(), userID, "server", hash))

	var createdAt time.Time
	err := p.QueryRow(t.Context(), `select created_at from lampa_torrserver_hashes
		where user_id = $1 and server_id = 'server' and hash = $2`, userID, hash).Scan(&createdAt)
	require.NoError(t, err)
	assert.False(t, createdAt.IsZero())

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			assert.NoError(t, s.ClaimHash(t.Context(), userID, "server", hash))
		})
	}
	wg.Wait()

	hashes, err := s.ListHashes(t.Context(), userID, "server")
	require.NoError(t, err)
	assert.Equal(t, []string{hash}, hashes)
	var retainedCreatedAt time.Time
	err = p.QueryRow(t.Context(), `select created_at from lampa_torrserver_hashes
		where user_id = $1 and server_id = 'server' and hash = $2`, userID, hash).Scan(&retainedCreatedAt)
	require.NoError(t, err)
	assert.True(t, createdAt.Equal(retainedCreatedAt), "repeated claims preserve the original timestamp")
	_, err = s.Get(t.Context(), userID)
	require.ErrorIs(t, err, storage.ErrNotFound, "membership needs no user document")
}

func TestPgStore_TorrentMembershipIsolation(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, pgtest.DB(t))
	alice, bob := newUserID(t), newUserID(t)
	hash, otherHash := strings.Repeat("a", 40), strings.Repeat("b", 64)

	for _, userID := range []string{alice, bob} {
		for _, serverID := range []string{"one", "two"} {
			require.NoError(t, s.ClaimHash(t.Context(), userID, serverID, hash))
		}
	}
	require.NoError(t, s.ClaimHash(t.Context(), alice, "one", otherHash))

	// deleting a user document leaves its independent torrent membership intact.
	_, err := s.Upsert(t.Context(), testRecord(alice, `{}`, nil))
	require.NoError(t, err)
	require.NoError(t, s.Delete(t.Context(), alice))
	hashes, err := s.ListHashes(t.Context(), alice, "one")
	require.NoError(t, err)
	assert.Equal(t, []string{hash, otherHash}, hashes)

	require.NoError(t, s.ReleaseHash(t.Context(), alice, "one", hash))
	require.NoError(t, s.ReleaseHash(t.Context(), alice, "one", hash), "release is idempotent")
	require.NoError(t, s.ReleaseHash(t.Context(), newUserID(t), "missing", hash), "missing membership is ok")

	for _, tc := range []struct {
		name     string
		userID   string
		serverID string
		want     []string
	}{
		{name: "other hash retained", userID: alice, serverID: "one", want: []string{otherHash}},
		{name: "same user other server", userID: alice, serverID: "two", want: []string{hash}},
		{name: "other user same server", userID: bob, serverID: "one", want: []string{hash}},
		{name: "other user other server", userID: bob, serverID: "two", want: []string{hash}},
		{name: "unclaimed server", userID: alice, serverID: "missing", want: []string{}},
		{name: "unclaimed user", userID: newUserID(t), serverID: "one", want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hashes, err := s.ListHashes(t.Context(), tc.userID, tc.serverID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, hashes)
		})
	}
}

func TestPgStore_TorrentStoreFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"closed pool", "canceled context"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			p, err := pgxpool.NewWithConfig(t.Context(), pgtest.DB(t).Config().Copy())
			require.NoError(t, err)
			t.Cleanup(p.Close)
			s := newTestStore(t, p)
			ctx := t.Context()
			if mode == "closed pool" {
				p.Close()
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			cause := p.Ping(ctx)
			require.Error(t, cause)
			userID, hash := newUserID(t), strings.Repeat("a", 40)

			hashes, err := s.ListHashes(ctx, userID, "server")
			require.ErrorContains(t, err, "list torrent hashes")
			require.ErrorIs(t, err, cause)
			assert.Nil(t, hashes, "failed lookup returns no partial list")
			err = s.ClaimHash(ctx, userID, "server", hash)
			require.ErrorContains(t, err, "claim torrent hash")
			require.ErrorIs(t, err, cause)
			err = s.ReleaseHash(ctx, userID, "server", hash)
			require.ErrorContains(t, err, "release torrent hash")
			require.ErrorIs(t, err, cause)
		})
	}
}

func TestPgStore_ListHashesReadFailure(t *testing.T) {
	t.Parallel()
	p := emptyDB(t)
	// a damaged schema with a null hash exercises row decoding, after the query has succeeded.
	_, err := p.Exec(t.Context(), `create table lampa_torrserver_hashes (user_id uuid, server_id text, hash text)`)
	require.NoError(t, err)
	userID := newUserID(t)
	_, err = p.Exec(t.Context(), `insert into lampa_torrserver_hashes values ($1, 'server', null)`, userID)
	require.NoError(t, err)
	hashes, err := newTestStore(t, p).ListHashes(t.Context(), userID, "server")
	require.ErrorContains(t, err, "read torrent hashes")
	require.Error(t, errors.Unwrap(err), "row decoding errors remain wrapped")
	assert.Nil(t, hashes, "failed row decoding returns no partial list")
}
