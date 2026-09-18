// Package main provides lampa-api - the persistence service for Lampa user data.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

var revision = "unknown"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout)
	cancel()
	if err != nil {
		log.Printf("[ERROR] %v", err)
		os.Exit(1)
	}
}

// run parses arguments and runs the service until ctx is canceled.
// the env lookup parameter is unused until the composition root wires config, stdout receives user-facing output.
func run(ctx context.Context, args []string, _ func(string) (string, bool), stdout io.Writer) error {
	fs := flag.NewFlagSet("lampa-api", flag.ContinueOnError)
	fs.SetOutput(stdout)
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	fmt.Fprintf(stdout, "lampa-api %s\n", resolveVersion())
	if *showVersion {
		return nil
	}

	// service wiring arrives with the composition root; until then the stub only waits for shutdown
	<-ctx.Done()
	return nil
}

// resolveVersion returns the ldflags revision, falling back to build info VCS data.
func resolveVersion() string {
	if revision != "unknown" {
		return revision
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return revision
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return s.Value[:7]
		}
	}
	return revision
}
