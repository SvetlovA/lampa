package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/SvetlovA/lampa/backend/migrations"
)

// migrationLockRetries is how many times, one second apart, Migrate retries taking the advisory lock
// held by another instance before giving up.
const migrationLockRetries = 300

// Migrate applies all pending embedded migrations to the database behind pool.
// concurrent callers are serialized by a postgres session advisory lock, so several instances
// starting at once are safe.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (err error) {
	if pool == nil {
		return errors.New("nil pool")
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, migrationLockRetries))
	if err != nil {
		return fmt.Errorf("create migration locker: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer func() {
		if closeErr := db.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close migration db: %w", closeErr)
		}
	}()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
