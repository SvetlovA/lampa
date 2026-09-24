// Package main provides lampa-api - the persistence service for Lampa user data.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SvetlovA/lampa/backend/pkg/api"
	"github.com/SvetlovA/lampa/backend/pkg/config"
	"github.com/SvetlovA/lampa/backend/pkg/health"
	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

var revision = "unknown"

// listenFunc opens a listener on addr. tests replace it to learn the bound addresses.
type listenFunc func(ctx context.Context, addr string) (net.Listener, error)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], config.Defaults, os.LookupEnv, os.Stdout)
	cancel()
	if err != nil {
		log.Printf("[ERROR] %v", err)
		os.Exit(1)
	}
}

// run parses arguments, loads the config from the settings files and lookup, and runs the service until ctx is canceled.
// out receives the version line and the service log.
func run(ctx context.Context, args []string, settings fs.FS, lookup func(string) (string, bool), out io.Writer) error {
	flags := flag.NewFlagSet("lampa-api", flag.ContinueOnError)
	flags.SetOutput(out)
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}

	fmt.Fprintf(out, "lampa-api %s\n", resolveVersion(revision, debug.ReadBuildInfo))
	if *showVersion {
		return nil
	}

	cfg, err := config.Load(settings, lookup)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := log.New(out, "", log.LstdFlags)
	logger.Printf("[INFO] config: %v", cfg)
	return start(ctx, cfg, logger, listenTCP)
}

// start connects and migrates the database, then serves the api and health servers until ctx is
// canceled or one of them fails. the first failure stops the other server, and the pool is closed
// only after both returned.
func start(ctx context.Context, cfg config.Config, logger *log.Logger, listen listenFunc) error {
	poolCfg, err := pgxpool.ParseConfig(cfg.DBDSN)
	if err != nil {
		// the parse error quotes the dsn with a best-effort password redaction, so it is dropped
		return errors.New("parse database dsn: invalid connection string")
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()

	if err = storage.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	logger.Printf("[INFO] database migrated")

	store, err := storage.NewPgStore(pool)
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}
	sealer, err := storage.NewSealer(cfg.DataKey)
	if err != nil {
		return fmt.Errorf("create sealer: %w", err)
	}
	limits := storage.Limits{
		MaxBodyBytes:    cfg.MaxBodyBytes,
		MaxSectionBytes: storage.DefaultMaxSectionBytes,
		MaxDepth:        storage.DefaultMaxDepth,
	}
	svc, err := storage.NewService(store, sealer, limits, logger)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	// user-data routes deny everything until keycloak authentication arrives in plan 2
	apiSrv, err := api.NewServer(api.ServerConfig{Addr: cfg.Listen, MaxBodyBytes: cfg.MaxBodyBytes}, svc, api.DenyAll{}, logger)
	if err != nil {
		return fmt.Errorf("create api server: %w", err)
	}
	reporter, err := health.NewReporter([]health.Check{health.DatabaseCheck(store)}, health.DefaultCheckTimeout)
	if err != nil {
		return fmt.Errorf("create health reporter: %w", err)
	}

	return serveAll(ctx, listen, logger, []server{
		{name: "api", addr: cfg.Listen, handler: apiSrv.Handler()},
		{name: "health", addr: cfg.HealthListen, handler: healthRoutes(reporter)},
	})
}

// server is a named handler served on its own listen address.
type server struct {
	name, addr string
	handler    http.Handler
}

// serveAll runs every server in its own goroutine until ctx is canceled or one of them fails.
// the first failure cancels the others; it returns after all of them stopped, joining their errors.
func serveAll(ctx context.Context, listen listenFunc, logger *log.Logger, servers []server) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			err := listenAndServe(ctx, listen, s.name, s.addr, s.handler, logger)
			if err != nil {
				cancel()
			}
			errs <- err
		}()
	}
	var result error
	for range servers {
		result = errors.Join(result, <-errs)
	}
	logger.Printf("[INFO] servers stopped")
	return result
}

// healthRoutes serves /health (all checks) and /health/critical (critical checks only).
func healthRoutes(r *health.Reporter) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", r.Handler(false))
	mux.Handle("GET /health/critical", r.Handler(true))
	return mux
}

// listenAndServe listens on addr and serves h until ctx is canceled or the server fails.
func listenAndServe(ctx context.Context, listen listenFunc, name, addr string, h http.Handler, logger *log.Logger) error {
	ln, err := listen(ctx, addr)
	if err != nil {
		return fmt.Errorf("%s server: listen %s: %w", name, addr, err)
	}
	logger.Printf("[INFO] %s server listening on %s", name, ln.Addr())
	if err = api.ServeHandler(ctx, ln, h); err != nil {
		return fmt.Errorf("%s server: %w", name, err)
	}
	return nil
}

func listenTCP(ctx context.Context, addr string) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(ctx, "tcp", addr) //nolint:wrapcheck // wrapped by listenAndServe
}

// resolveVersion returns the ldflags rev, falling back to the module version and VCS data of readBuildInfo.
func resolveVersion(rev string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if rev != "unknown" {
		return rev
	}
	bi, ok := readBuildInfo()
	if !ok {
		return rev
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return s.Value[:7]
		}
	}
	return rev
}
