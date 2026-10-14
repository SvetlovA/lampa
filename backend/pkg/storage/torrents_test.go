package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
	"github.com/SvetlovA/lampa/backend/pkg/storage/mocks"
	"github.com/SvetlovA/lampa/backend/pkg/storage/pgtest"
)

var _ storage.TorrentStore = (*storage.PgStore)(nil)

func newTestTorrents(t *testing.T, store storage.TorrentStore) (*storage.Torrents, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	svc, err := storage.NewTorrents(store, log.New(&buf, "", 0))
	require.NoError(t, err)
	return svc, &buf
}

func TestNewTorrents(t *testing.T) {
	t.Parallel()
	store := &mocks.TorrentStoreMock{}
	logger := log.New(&bytes.Buffer{}, "", 0)
	for _, tc := range []struct {
		name    string
		store   storage.TorrentStore
		logger  storage.Logger
		wantErr string
	}{
		{name: "valid", store: store, logger: logger},
		{name: "nil store", logger: logger, wantErr: "nil torrent store"},
		{name: "nil logger", store: store, wantErr: "nil logger"},
		{name: "both nil", wantErr: "nil torrent store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, err := storage.NewTorrents(tc.store, tc.logger)
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

func TestTorrents_List(t *testing.T) {
	t.Parallel()
	want := []string{strings.Repeat("a", 40), strings.Repeat("b", 64)}
	for _, tc := range []struct {
		name     string
		serverID string
		stored   []string
		want     []string
	}{
		{name: "decimal id", serverID: "1234567890", stored: want, want: want},
		{name: "single character", serverID: "_", stored: want, want: want},
		{name: "opaque id preserves case", serverID: "AbC-019_z", stored: want, want: want},
		{name: "maximum length", serverID: strings.Repeat("Z", 64), stored: want, want: want},
		{name: "empty list", serverID: "server", stored: []string{}, want: []string{}},
		{name: "nil list", serverID: "server", want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			userID := newUserID(t)
			store := &mocks.TorrentStoreMock{
				ListHashesFunc: func(ctx context.Context, id, serverID string) ([]string, error) {
					assert.Equal(t, t.Context(), ctx)
					assert.Equal(t, userID, id)
					assert.Equal(t, tc.serverID, serverID)
					return tc.stored, nil
				},
			}
			svc, buf := newTestTorrents(t, store)
			hashes, err := svc.List(t.Context(), strings.ToUpper(userID), tc.serverID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, hashes)
			assert.Len(t, store.ListHashesCalls(), 1)
			assert.Empty(t, buf.String())
		})
	}
}

func TestTorrents_ClaimAndRelease(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		hash string
		want string
	}{
		{name: "v1", hash: strings.Repeat("0123456789abcdef", 2) + "01234567",
			want: strings.Repeat("0123456789abcdef", 2) + "01234567"},
		{name: "v2", hash: strings.Repeat("0123456789abcdef", 4), want: strings.Repeat("0123456789abcdef", 4)},
		{name: "uppercase v1", hash: strings.Repeat("ABCDEF1234", 4), want: strings.Repeat("abcdef1234", 4)},
		{name: "uppercase v2", hash: strings.Repeat("ABCDEF01", 8), want: strings.Repeat("abcdef01", 8)},
		{name: "surrounding whitespace", hash: " \t\n" + strings.Repeat("A", 40) + "\r\n ", want: strings.Repeat("a", 40)},
		{name: "unicode whitespace", hash: "\u2003" + strings.Repeat("F", 64) + "\u00a0", want: strings.Repeat("f", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			userID := newUserID(t)
			serverID := "Server_1-"
			check := func(ctx context.Context, id, server, hash string) error {
				assert.Equal(t, t.Context(), ctx)
				assert.Equal(t, userID, id)
				assert.Equal(t, serverID, server)
				assert.Equal(t, tc.want, hash)
				return nil
			}
			store := &mocks.TorrentStoreMock{ClaimHashFunc: check, ReleaseHashFunc: check}
			svc, buf := newTestTorrents(t, store)
			for range 2 {
				require.NoError(t, svc.Claim(t.Context(), strings.ToUpper(userID), serverID, tc.hash))
				require.NoError(t, svc.Release(t.Context(), strings.ToUpper(userID), serverID, tc.hash))
			}
			assert.Len(t, store.ClaimHashCalls(), 2)
			assert.Len(t, store.ReleaseHashCalls(), 2)
			assert.Empty(t, buf.String())
		})
	}
}

func TestTorrents_InvalidScope(t *testing.T) {
	t.Parallel()
	userID := "01234567-89ab-cdef-0123-456789abcdef"
	for _, tc := range []struct {
		name     string
		userID   string
		serverID string
		userErr  bool
	}{
		{name: "empty user", serverID: "server", userErr: true},
		{name: "malformed user", userID: "not-a-uuid", serverID: "server", userErr: true},
		{name: "nonhex user", userID: strings.Replace(userID, "a", "g", 1), serverID: "server", userErr: true},
		{name: "unhyphenated user", userID: strings.ReplaceAll(userID, "-", ""), serverID: "server", userErr: true},
		{name: "padded user", userID: " " + userID, serverID: "server", userErr: true},
		{name: "invalid user precedes invalid server", userID: "bad", userErr: true},
		{name: "empty server", userID: userID},
		{name: "long server", userID: userID, serverID: strings.Repeat("a", 65)},
		{name: "padded server", userID: userID, serverID: " server "},
		{name: "newline server", userID: userID, serverID: "server\n"},
		{name: "server url", userID: userID, serverID: "https://server"},
		{name: "server path", userID: userID, serverID: "one/two"},
		{name: "server punctuation", userID: userID, serverID: "server.id"},
		{name: "server unicode", userID: userID, serverID: "sérver"},
		{name: "server null", userID: userID, serverID: "server\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &mocks.TorrentStoreMock{}
			svc, buf := newTestTorrents(t, store)
			hash := strings.Repeat("a", 40)
			hashes, listErr := svc.List(t.Context(), tc.userID, tc.serverID)
			assert.Nil(t, hashes)
			for _, err := range []error{listErr,
				svc.Claim(t.Context(), tc.userID, tc.serverID, hash),
				svc.Release(t.Context(), tc.userID, tc.serverID, hash)} {
				if tc.userErr {
					require.ErrorIs(t, err, storage.ErrInvalidUserID)
					continue
				}
				var verr *storage.ValidationError
				require.ErrorAs(t, err, &verr)
				assert.Same(t, verr, err, "validation errors are returned unwrapped")
				assert.Equal(t, storage.CodeInvalidServerID, verr.Code)
				assert.Equal(t, "server_id must contain 1 to 64 ASCII letters, digits, underscores or hyphens", verr.Reason)
			}
			assert.Empty(t, store.ListHashesCalls())
			assert.Empty(t, store.ClaimHashCalls())
			assert.Empty(t, store.ReleaseHashCalls())
			assert.Empty(t, buf.String())
		})
	}
}

func TestTorrents_InvalidHash(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		hash string
	}{
		{name: "empty"},
		{name: "whitespace", hash: " \t\n "},
		{name: "short v1", hash: strings.Repeat("a", 39)},
		{name: "long v1", hash: strings.Repeat("a", 41)},
		{name: "short v2", hash: strings.Repeat("a", 63)},
		{name: "long v2", hash: strings.Repeat("a", 65)},
		{name: "nonhex", hash: strings.Repeat("g", 40)},
		{name: "hex prefix", hash: "0x" + strings.Repeat("a", 40)},
		{name: "embedded whitespace", hash: strings.Repeat("a", 20) + " " + strings.Repeat("b", 19)},
		{name: "embedded newline", hash: strings.Repeat("a", 20) + "\n" + strings.Repeat("b", 19)},
		{name: "unicode", hash: strings.Repeat("é", 20)},
		{name: "null", hash: strings.Repeat("a", 39) + "\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &mocks.TorrentStoreMock{}
			svc, buf := newTestTorrents(t, store)
			userID := newUserID(t)
			for _, err := range []error{
				svc.Claim(t.Context(), userID, "server", tc.hash),
				svc.Release(t.Context(), userID, "server", tc.hash)} {
				var verr *storage.ValidationError
				require.ErrorAs(t, err, &verr)
				assert.Same(t, verr, err, "validation errors are returned unwrapped")
				assert.Equal(t, storage.CodeInvalidHash, verr.Code)
				assert.Equal(t, "hash must contain 40 or 64 hexadecimal characters", verr.Reason)
			}
			assert.Empty(t, store.ClaimHashCalls())
			assert.Empty(t, store.ReleaseHashCalls())
			assert.Empty(t, buf.String())
		})
	}
}

func TestTorrents_StoreFailures(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{
		errors.New("database offline"), context.Canceled, context.DeadlineExceeded,
		fmt.Errorf("wrapped store failure: %w", storage.ErrNotFound),
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			t.Parallel()
			store := &mocks.TorrentStoreMock{
				ListHashesFunc: func(context.Context, string, string) ([]string, error) {
					return []string{strings.Repeat("a", 40)}, cause
				},
				ClaimHashFunc:   func(context.Context, string, string, string) error { return cause },
				ReleaseHashFunc: func(context.Context, string, string, string) error { return cause },
			}
			svc, buf := newTestTorrents(t, store)
			userID, hash := newUserID(t), strings.Repeat("a", 40)
			hashes, listErr := svc.List(t.Context(), userID, "server")
			assert.Nil(t, hashes, "a failed list must not expose partial results")
			for _, tc := range []struct {
				op  string
				err error
			}{
				{op: "list torrent hashes", err: listErr},
				{op: "claim torrent hash", err: svc.Claim(t.Context(), userID, "server", hash)},
				{op: "release torrent hash", err: svc.Release(t.Context(), userID, "server", hash)},
			} {
				require.ErrorIs(t, tc.err, storage.ErrUnavailable)
				require.ErrorIs(t, tc.err, cause)
				require.ErrorContains(t, tc.err, tc.op)
				assert.Contains(t, buf.String(), "[WARN] "+tc.op+": "+cause.Error())
			}
			assert.Len(t, store.ListHashesCalls(), 1)
			assert.Len(t, store.ClaimHashCalls(), 1)
			assert.Len(t, store.ReleaseHashCalls(), 1)
			assert.NotContains(t, buf.String(), userID)
			assert.NotContains(t, buf.String(), hash)
		})
	}
}

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
