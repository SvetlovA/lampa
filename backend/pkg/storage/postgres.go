package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrPreconditionFailed means an atomic update found no row with the expected ETag.
	ErrPreconditionFailed = errors.New("user data precondition failed")
	// ErrNotFound is returned when a user has no stored document.
	ErrNotFound = errors.New("user data not found")
	// ErrInvalidData is returned by Upsert when postgres rejects the document content itself,
	// for example a json number that overflows jsonb's numeric, as opposed to a failing database.
	ErrInvalidData = errors.New("document content rejected by store")
)

// dataExceptionClass is the SQLSTATE class of errors caused by the data of a statement.
const dataExceptionClass = "22"

// Record is one row of lampa_user_data with selected values sealed in place.
type Record struct {
	UserID        string          // canonical uuid of the owner
	SchemaVersion int             // document schema version
	Data          json.RawMessage // json object with selected values sealed in place
	ETag          string          // sha256 of canonical plaintext data, without quotes
	CreatedAt     time.Time       // set on first insert, never changed
	UpdatedAt     time.Time       // set on every upsert
}

// PgStore keeps user documents and torrent membership in postgres.
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
		`select schema_version, data, etag, created_at, updated_at
		from lampa_user_data where user_id = $1`, userID).
		Scan(&rec.SchemaVersion, &data, &rec.ETag, &rec.CreatedAt, &rec.UpdatedAt)
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
		`insert into lampa_user_data (user_id, schema_version, data, etag)
		values ($1, $2, $3, $4)
		on conflict (user_id) do update set
			schema_version = excluded.schema_version,
			data = excluded.data,
			etag = excluded.etag,
			updated_at = now()
		returning created_at, updated_at`,
		rec.UserID, rec.SchemaVersion, []byte(rec.Data), rec.ETag).
		Scan(&rec.CreatedAt, &rec.UpdatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, dataExceptionClass) {
		return Record{}, fmt.Errorf("upsert user data: %w: %w", ErrInvalidData, err)
	}
	if err != nil {
		return Record{}, fmt.Errorf("upsert user data: %w", err)
	}
	return rec, nil
}

// Update replaces an existing record only if its ETag still matches expectedETag.
// the predicate and write run in one statement, so competing updates cannot both win.
func (s *PgStore) Update(ctx context.Context, rec Record, expectedETag string) (Record, error) {
	err := s.pool.QueryRow(ctx,
		`update lampa_user_data set schema_version = $2, data = $3, etag = $4, updated_at = now()
		where user_id = $1 and etag = $5
		returning created_at, updated_at`,
		rec.UserID, rec.SchemaVersion, []byte(rec.Data), rec.ETag, expectedETag).
		Scan(&rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrPreconditionFailed
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, dataExceptionClass) {
		return Record{}, fmt.Errorf("update user data: %w: %w", ErrInvalidData, err)
	}
	if err != nil {
		return Record{}, fmt.Errorf("update user data: %w", err)
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

// Ping checks that the database is reachable and both application tables exist.
func (s *PgStore) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `select 1 from lampa_user_data limit 0`); err != nil {
		return fmt.Errorf("probe schema: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `select 1 from lampa_torrserver_hashes limit 0`); err != nil {
		return fmt.Errorf("probe torrserver hashes schema: %w", err)
	}
	return nil
}
