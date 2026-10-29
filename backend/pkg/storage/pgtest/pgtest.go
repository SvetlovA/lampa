// Package pgtest provides a migrated postgres database for tests, started once per test binary
// in a testcontainers container.
package pgtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

// Image is the postgres image used by tests, the same version as in production.
const Image = "postgres:18.6"

// RequireDockerEnv names the variable that turns a missing docker from a skip into a failure.
// CI sets it to "1" so database tests can never silently skip there.
const RequireDockerEnv = "LAMPA_API_REQUIRE_DOCKER"

const startTimeout = 5 * time.Minute

var errNoDocker = errors.New("docker is not available")

var (
	once     sync.Once
	pool     *pgxpool.Pool
	errStart error
)

// DB returns a pool connected to a migrated test database shared by every test of the binary.
// the container starts on first use and is removed by the testcontainers reaper when the binary exits.
// without docker it skips the test, or fails it when RequireDockerEnv is "1".
// tests must isolate their rows, e.g. by using random user ids.
func DB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	once.Do(func() { pool, errStart = start() })
	if errors.Is(errStart, errNoDocker) && os.Getenv(RequireDockerEnv) != "1" {
		t.Skipf("skip postgres test: %v", errStart)
	}
	if errStart != nil {
		t.Fatalf("start test postgres: %v", errStart)
	}
	return pool
}

func start() (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	if err := dockerHealth(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", errNoDocker, err)
	}

	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("lampa"),
		postgres.WithUsername("lampa"),
		postgres.WithPassword("lampa"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("run container: %w", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("get connection string: %w", err)
	}
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := storage.Migrate(ctx, p); err != nil {
		p.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return p, nil
}

// dockerHealth reports whether a docker daemon is reachable. testcontainers panics in some setups
// without docker, so the panic is turned into an error.
func dockerHealth(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker provider panic: %v", r)
		}
	}()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return fmt.Errorf("get docker provider: %w", err)
	}
	defer provider.Close()
	if err := provider.Health(ctx); err != nil {
		return fmt.Errorf("check docker health: %w", err)
	}
	return nil
}
