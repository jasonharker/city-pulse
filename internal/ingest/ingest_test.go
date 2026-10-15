package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store/storetest"
)

// fake publishes rows as {"v": value} records; a value of "bad" fails Parse.
type fake struct {
	name     string
	rows     map[string]string
	fetchErr error
}

func (f *fake) Name() string { return f.name }

func (f *fake) Fetch(_ context.Context, emit func(source.Record) error) error {
	if f.fetchErr != nil {
		return f.fetchErr
	}
	for id, v := range f.rows {
		r, _ := source.NewRecord(id, map[string]any{"v": v})
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *fake) Parse(r source.Record) (source.Observation, error) {
	d, _ := source.Decode(r)
	if d.Str("v") == "bad" {
		return source.Observation{}, errors.New("bad value")
	}
	return source.Observation{Kind: "license", Name: d.Str("v")}, nil
}

func TestRunOneVersionsWithdrawsAndKeepsParseErrors(t *testing.T) {
	st, _ := storetest.New(t)
	ctx := context.Background()
	src := &fake{name: "fake", rows: map[string]string{"a": "1", "b": "1", "c": "bad", "": "1"}}

	stats, err := runOne(ctx, st, src)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Seen != 4 || stats.New != 2 || stats.ParseErrors != 2 || stats.Withdrawn != 0 {
		t.Errorf("run1 = %+v", stats)
	}

	src.rows = map[string]string{"a": "2", "c": "1", "d": "1"}
	stats, err = runOne(ctx, st, src)
	if err != nil {
		t.Fatal(err)
	}
	if stats.New != 3 || stats.Withdrawn != 1 { // a v2, c, d new; b withdrawn
		t.Errorf("run2 = %+v", stats)
	}
}

func TestRunOneRefusesToWithdrawOnCollapse(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	src := &fake{name: "fake", rows: map[string]string{"a": "1", "b": "1", "c": "1", "d": "1"}}
	if _, err := runOne(ctx, st, src); err != nil {
		t.Fatal(err)
	}
	src.rows = map[string]string{"a": "1"}
	stats, err := runOne(ctx, st, src)
	if err == nil {
		t.Fatal("want error when rows collapse")
	}
	var withdrawn int
	db.QueryRow(ctx, "SELECT count(*) FROM observations WHERE withdrawn_at IS NOT NULL").Scan(&withdrawn)
	if stats.Withdrawn != 0 || withdrawn != 0 {
		t.Errorf("withdrew %d/%d rows on a collapsed run", stats.Withdrawn, withdrawn)
	}
}

func TestRunContinuesPastFailingSource(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	broken := &fake{name: "broken", fetchErr: errors.New("HTTP 503")}
	good := &fake{name: "good", rows: map[string]string{"a": "1"}}
	err := Run(ctx, st, []source.Source{broken, good}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("want joined error")
	}
	var n int
	db.QueryRow(ctx, "SELECT count(*) FROM observations WHERE source = 'good'").Scan(&n)
	var failed string
	db.QueryRow(ctx, "SELECT error FROM source_runs WHERE source = 'broken'").Scan(&failed)
	if n != 1 || failed != "HTTP 503" {
		t.Errorf("good rows=%d broken error=%q", n, failed)
	}
}
