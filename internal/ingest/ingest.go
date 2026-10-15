// Package ingest runs sources and stores what they publish.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store"
)

const batchSize = 2000

// Run ingests each source in turn. One source failing does not stop the rest;
// the returned error joins every failure.
func Run(ctx context.Context, st *store.Store, sources []source.Source, log *slog.Logger) error {
	var errs []error
	for _, s := range sources {
		stats, err := runOne(ctx, st, s)
		log.Info("ingest", "source", s.Name(), "seen", stats.Seen, "new", stats.New,
			"parse_errors", stats.ParseErrors, "withdrawn", stats.Withdrawn, "err", err)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	return errors.Join(errs...)
}

func runOne(ctx context.Context, st *store.Store, s source.Source) (store.RunStats, error) {
	var stats store.RunStats
	prevSeen, hasPrev, err := st.LastGoodRun(ctx, s.Name())
	if err != nil {
		return stats, err
	}
	run, err := st.BeginRun(ctx, s.Name())
	if err != nil {
		return stats, err
	}

	batch := make([]source.Observation, 0, batchSize)
	flush := func() error {
		n, err := run.Add(ctx, batch)
		stats.New += n
		batch = batch[:0]
		return err
	}
	runErr := s.Fetch(ctx, func(r source.Record) error {
		stats.Seen++
		obs, err := parse(s, r)
		if err != nil {
			stats.ParseErrors++
			return run.ParseError(ctx, r, err)
		}
		if batch = append(batch, obs); len(batch) == batchSize {
			return flush()
		}
		return nil
	})
	if runErr == nil {
		runErr = flush()
	}
	if runErr == nil {
		// An upstream outage can return an empty or truncated layer. Withdrawing
		// on that would mark most of the city as gone, so refuse and fail loudly.
		if hasPrev && stats.Seen < prevSeen/2 {
			runErr = fmt.Errorf("saw %d rows, last good run saw %d: withdrawal skipped", stats.Seen, prevSeen)
		} else {
			stats.Withdrawn, runErr = run.Withdraw(ctx)
		}
	}
	// Record the outcome even when ctx was cancelled mid-run.
	if err := run.Finish(context.WithoutCancel(ctx), stats, runErr); err != nil {
		return stats, errors.Join(runErr, err)
	}
	return stats, runErr
}

// parse runs the source's parser and fills in the record-level fields.
func parse(s source.Source, r source.Record) (source.Observation, error) {
	if strings.TrimSpace(r.ID) == "" {
		return source.Observation{}, errors.New("record has no id")
	}
	obs, err := s.Parse(r)
	if err != nil {
		return source.Observation{}, err
	}
	obs.Source, obs.RecordID, obs.Hash, obs.Payload = s.Name(), r.ID, r.Hash(), r.Payload
	return obs, nil
}
