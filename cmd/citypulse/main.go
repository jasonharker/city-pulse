// Command citypulse ingests Las Vegas civic data.
//
//	citypulse migrate
//	citypulse ingest [-only snhd,ccbiz]
//	citypulse resolve
//	citypulse nightly    (ingest, then resolve even if a source failed)
//
// DATABASE_URL must be a postgres:// URL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jasonharker/city-pulse/internal/ingest"
	"github.com/jasonharker/city-pulse/internal/resolve"
	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/ccbiz"
	"github.com/jasonharker/city-pulse/internal/source/clvlicense"
	"github.com/jasonharker/city-pulse/internal/source/clvpermit"
	"github.com/jasonharker/city-pulse/internal/source/snhd"
	"github.com/jasonharker/city-pulse/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: citypulse migrate | ingest [-only a,b] | resolve | nightly")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "migrate":
		err = withStore(ctx, func(st *store.Store) error { return nil })
	case "ingest":
		err = runIngest(ctx, os.Args[2:], log)
	case "resolve":
		err = withStore(ctx, func(st *store.Store) error { return runResolve(ctx, st, log) })
	case "nightly":
		err = errors.Join(runIngest(ctx, nil, log),
			withStore(ctx, func(st *store.Store) error { return runResolve(ctx, st, log) }))
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Error("citypulse failed", "cmd", os.Args[1], "err", err)
		os.Exit(1)
	}
}

// withStore opens and migrates the database, then runs fn.
func withStore(ctx context.Context, fn func(*store.Store) error) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return fn(st)
}

func runIngest(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	only := fs.String("only", "", "comma-separated source names (default: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := &http.Client{Timeout: 2 * time.Minute}
	all := []source.Source{snhd.New(c), ccbiz.New(c), clvlicense.New(c), clvpermit.New(c)}
	sources := all
	if *only != "" {
		names := strings.Split(*only, ",")
		sources = slices.DeleteFunc(slices.Clone(all), func(s source.Source) bool { return !slices.Contains(names, s.Name()) })
		if len(sources) != len(names) {
			return fmt.Errorf("-only %q: unknown source name", *only)
		}
	}
	return withStore(ctx, func(st *store.Store) error { return ingest.Run(ctx, st, sources, log) })
}

func runResolve(ctx context.Context, st *store.Store, log *slog.Logger) error {
	start := time.Now()
	obs, err := st.CurrentObservations(ctx)
	if err != nil {
		return fmt.Errorf("resolve: load: %w", err)
	}
	r := resolve.Build(obs)
	if err := st.ReplaceResolved(ctx, r); err != nil {
		return fmt.Errorf("resolve: write: %w", err)
	}
	log.Info("resolve", "observations", len(obs), "places", len(r.Places),
		"businesses", len(r.Businesses), "took", time.Since(start).Round(time.Second))
	return nil
}
