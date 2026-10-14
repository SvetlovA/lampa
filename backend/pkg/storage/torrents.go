package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:generate go tool moq -out mocks/torrent_store.go -pkg mocks -skip-ensure -fmt goimports . TorrentStore

// TorrentStore persists torrent membership independently of user documents.
type TorrentStore interface {
	ListHashes(ctx context.Context, userID, serverID string) ([]string, error)
	ClaimHash(ctx context.Context, userID, serverID, hash string) error
	ReleaseHash(ctx context.Context, userID, serverID, hash string) error
}

// validation error codes for torrent membership, returned to API clients as-is
const (
	CodeInvalidServerID = "invalid_server_id"
	CodeInvalidHash     = "invalid_hash"
)

var (
	torrentServerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	torrentHashPattern     = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// Torrents validates and manages per-user torrent membership on an opaque server id.
type Torrents struct {
	store  TorrentStore
	logger Logger
}

// NewTorrents creates a Torrents service. both dependencies are required.
func NewTorrents(store TorrentStore, logger Logger) (*Torrents, error) {
	switch {
	case store == nil:
		return nil, errors.New("nil torrent store")
	case logger == nil:
		return nil, errors.New("nil logger")
	}
	return &Torrents{store: store, logger: logger}, nil
}

// List returns sorted hashes for the user and server, or an empty slice when none exist.
func (s *Torrents) List(ctx context.Context, userID, serverID string) ([]string, error) {
	id, err := canonicalTorrentUser(userID, serverID)
	if err != nil {
		return nil, err
	}
	hashes, err := s.store.ListHashes(ctx, id, serverID)
	if err != nil {
		return nil, s.storeError("list torrent hashes", err)
	}
	if hashes == nil {
		hashes = []string{}
	}
	return hashes, nil
}

// Claim records membership after validating the scope and canonicalizing the torrent hash.
func (s *Torrents) Claim(ctx context.Context, userID, serverID, hash string) error {
	id, err := canonicalTorrentUser(userID, serverID)
	if err != nil {
		return err
	}
	hash, err = canonicalTorrentHash(hash)
	if err != nil {
		return err
	}
	if err = s.store.ClaimHash(ctx, id, serverID, hash); err != nil {
		return s.storeError("claim torrent hash", err)
	}
	return nil
}

// Release removes only this user's membership. a missing membership is not an error.
func (s *Torrents) Release(ctx context.Context, userID, serverID, hash string) error {
	id, err := canonicalTorrentUser(userID, serverID)
	if err != nil {
		return err
	}
	hash, err = canonicalTorrentHash(hash)
	if err != nil {
		return err
	}
	if err = s.store.ReleaseHash(ctx, id, serverID, hash); err != nil {
		return s.storeError("release torrent hash", err)
	}
	return nil
}

// storeError logs and wraps every store failure as ErrUnavailable, preserving its cause.
func (s *Torrents) storeError(op string, err error) error {
	s.logger.Printf("[WARN] %s: %v", op, err)
	return fmt.Errorf("%s: %w: %w", op, ErrUnavailable, err)
}

// canonicalTorrentUser canonicalizes the user id and validates the opaque server id as-is.
func canonicalTorrentUser(userID, serverID string) (string, error) {
	id, err := canonicalUserID(userID)
	if err != nil {
		return "", err
	}
	if !torrentServerIDPattern.MatchString(serverID) {
		return "", invalid(CodeInvalidServerID, "server_id must contain 1 to 64 ASCII letters, digits, underscores or hyphens")
	}
	return id, nil
}

// canonicalTorrentHash trims and lowercases a v1 or v2 hexadecimal torrent hash.
func canonicalTorrentHash(hash string) (string, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if !torrentHashPattern.MatchString(hash) {
		return "", invalid(CodeInvalidHash, "hash must contain 40 or 64 hexadecimal characters")
	}
	return hash, nil
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
