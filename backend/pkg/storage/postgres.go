package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a user has no stored document.
var ErrNotFound = errors.New("user data not found")

// Record is one row of lampa_user_data: the document data without credentials plus their sealed blob.
type Record struct {
	UserID               string          // canonical uuid of the owner
	SchemaVersion        int             // document schema version
	Data                 json.RawMessage // json object with all sections, sensitive settings removed
	EncryptedConnections []byte          // sealed credentials, nil when the user has none
	CreatedAt            time.Time       // set on first insert, never changed
	UpdatedAt            time.Time       // set on every upsert
}

// PgStore keeps user documents in postgres.
type PgStore struct {
	pool *pgxpool.Pool
}

// NewPgStore creates a PgStore on top of an already migrated pool.
func NewPgStore(pool *pgxpool.Pool) (*PgStore, error) {
	if pool == nil {
		return nil, errors.New("nil pool")
	}
	return &PgStore{pool: pool}, nil
}

// Get returns the record of userID, or ErrNotFound when there is none.
func (s *PgStore) Get(ctx context.Context, userID string) (Record, error) {
	rec := Record{UserID: userID}
	var data []byte
	err := s.pool.QueryRow(ctx,
		`select schema_version, data, encrypted_connections, created_at, updated_at
		from lampa_user_data where user_id = $1`, userID).
		Scan(&rec.SchemaVersion, &data, &rec.EncryptedConnections, &rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("select user data: %w", err)
	}
	rec.Data = data
	return rec, nil
}

// Upsert inserts or fully replaces the record of rec.UserID and returns it with the stored timestamps.
// the CreatedAt and UpdatedAt fields of rec are ignored.
func (s *PgStore) Upsert(ctx context.Context, rec Record) (Record, error) {
	err := s.pool.QueryRow(ctx,
		`insert into lampa_user_data (user_id, schema_version, data, encrypted_connections)
		values ($1, $2, $3, $4)
		on conflict (user_id) do update set
			schema_version = excluded.schema_version,
			data = excluded.data,
			encrypted_connections = excluded.encrypted_connections,
			updated_at = now()
		returning created_at, updated_at`,
		rec.UserID, rec.SchemaVersion, []byte(rec.Data), rec.EncryptedConnections).
		Scan(&rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		return Record{}, fmt.Errorf("upsert user data: %w", err)
	}
	return rec, nil
}

// Delete removes the record of userID. deleting a missing record is not an error.
func (s *PgStore) Delete(ctx context.Context, userID string) error {
	if _, err := s.pool.Exec(ctx, `delete from lampa_user_data where user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete user data: %w", err)
	}
	return nil
}

// Ping checks that the database is reachable and the lampa_user_data table exists.
func (s *PgStore) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `select 1 from lampa_user_data limit 0`); err != nil {
		return fmt.Errorf("probe schema: %w", err)
	}
	return nil
}
