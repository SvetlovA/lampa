package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

//go:generate go tool moq -out mocks/torrent_store.go -pkg mocks -skip-ensure -fmt goimports . TorrentStore

// TorrentStore persists torrent membership independently of user documents.
type TorrentStore interface {
	ListHashes(ctx context.Context, userID, serverID string) ([]string, error)
	ClaimHash(ctx context.Context, userID, serverID, hash string) error
	ReleaseHash(ctx context.Context, userID, serverID, hash string) error
}

// ListHashes returns the user's hashes for one server, sorted, or an empty slice when none exist.
func (s *PgStore) ListHashes(ctx context.Context, userID, serverID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select hash from lampa_torrserver_hashes
		where user_id = $1 and server_id = $2 order by hash`, userID, serverID)
	if err != nil {
		return nil, fmt.Errorf("list torrent hashes: %w", err)
	}
	hashes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read torrent hashes: %w", err)
	}
	return hashes, nil
}

// ClaimHash records membership. claiming an existing hash preserves the original row.
func (s *PgStore) ClaimHash(ctx context.Context, userID, serverID, hash string) error {
	if _, err := s.pool.Exec(ctx, `insert into lampa_torrserver_hashes (user_id, server_id, hash)
		values ($1, $2, $3) on conflict (user_id, server_id, hash) do nothing`, userID, serverID, hash); err != nil {
		return fmt.Errorf("claim torrent hash: %w", err)
	}
	return nil
}

// ReleaseHash removes only this user's membership on this server. a missing row is not an error.
func (s *PgStore) ReleaseHash(ctx context.Context, userID, serverID, hash string) error {
	if _, err := s.pool.Exec(ctx, `delete from lampa_torrserver_hashes
		where user_id = $1 and server_id = $2 and hash = $3`, userID, serverID, hash); err != nil {
		return fmt.Errorf("release torrent hash: %w", err)
	}
	return nil
}
