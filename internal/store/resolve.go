package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/jasonharker/city-pulse/internal/resolve"
)

// CurrentObservations returns the current version of every source record:
// the one from the latest run, and among versions first seen in the same run
// (the permit archive publishes several lifecycle snapshots at once) the most
// advanced status.
func (s *Store) CurrentObservations(ctx context.Context) ([]resolve.Obs, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (source, record_id)
		       id, name, address, business_key, status, lat, lon,
		       coalesce(start_date, first_seen::date), end_date
		FROM observations
		ORDER BY source, record_id, first_run_id DESC,
		         CASE WHEN status IN ('Closed', 'Expired', 'Cancelled', 'Withdrawn', 'Deleted') THEN 3
		              WHEN status IN ('Issued', 'Stop Work') THEN 2
		              ELSE 1 END DESC,
		         id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (resolve.Obs, error) {
		var o resolve.Obs
		err := r.Scan(&o.ID, &o.Name, &o.Address, &o.BusinessKey, &o.Status, &o.Lat, &o.Lon, &o.Date, &o.End)
		return o, err
	})
}

// ReplaceResolved swaps in a new resolve result in one transaction, so readers
// see either the old result or the new one, never a mix.
// ponytail: TRUNCATE blocks readers for the few seconds the COPYs take;
// build-then-rename tables if that ever shows up in serve latency.
func (s *Store) ReplaceResolved(ctx context.Context, r resolve.Result) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "TRUNCATE links, businesses, places"); err != nil {
		return err
	}
	places := make([][]any, len(r.Places))
	for i, p := range r.Places {
		places[i] = []any{p.ID, p.Address, p.Lat, p.Lon}
	}
	biz := make([][]any, len(r.Businesses))
	for i, b := range r.Businesses {
		biz[i] = []any{b.ID, nullable(b.PlaceID), b.Name, b.First, b.Last, b.Closed, SearchText(b.Name)}
	}
	links := make([][]any, len(r.Links))
	for i, l := range r.Links {
		links[i] = []any{l.ObservationID, nullable(l.PlaceID), l.PlaceRule, nullable(l.BusinessID), l.BusinessRule}
	}
	for _, c := range []struct {
		table string
		cols  []string
		rows  [][]any
	}{
		{"places", []string{"id", "address", "lat", "lon"}, places},
		{"businesses", []string{"id", "place_id", "name", "first_date", "last_date", "closed", "search"}, biz},
		{"links", []string{"observation_id", "place_id", "place_rule", "business_id", "business_rule"}, links},
	} {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{c.table}, c.cols, pgx.CopyFromRows(c.rows)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
