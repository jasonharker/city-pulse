package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store"
	"github.com/jasonharker/city-pulse/internal/store/storetest"
)

func obs(t *testing.T, id string, fields map[string]any) source.Observation {
	t.Helper()
	r, err := source.NewRecord(id, fields)
	if err != nil {
		t.Fatal(err)
	}
	d := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	lat, lon := 36.1, -115.1
	return source.Observation{
		Source: "test", RecordID: id, Hash: r.Hash(), Payload: r.Payload,
		Kind: "license", Name: "Joe's", Address: "1 Main St", Lat: &lat, Lon: &lon, StartDate: &d,
	}
}

// run performs one complete successful run and returns (new, withdrawn).
func run(t *testing.T, st *store.Store, batches ...[]source.Observation) (int, int) {
	t.Helper()
	ctx := context.Background()
	r, err := st.BeginRun(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	var stats store.RunStats
	for _, b := range batches {
		n, err := r.Add(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		stats.New += n
	}
	if stats.Withdrawn, err = r.Withdraw(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, stats, nil); err != nil {
		t.Fatal(err)
	}
	return stats.New, stats.Withdrawn
}

func TestMigrateIsIdempotent(t *testing.T) {
	st, _ := storetest.New(t)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunsVersionWithdrawAndRestore(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	q := func(sql string) int {
		var n int
		if err := db.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Run 1: a, b, and an exact duplicate of b in another batch.
	n, w := run(t, st,
		[]source.Observation{obs(t, "a", map[string]any{"v": 1}), obs(t, "b", map[string]any{"v": 1})},
		[]source.Observation{obs(t, "b", map[string]any{"v": 1})})
	if n != 2 || w != 0 {
		t.Fatalf("run1 new=%d withdrawn=%d", n, w)
	}

	// Run 2: a changed, b gone.
	n, w = run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 2})})
	if n != 1 || w != 1 {
		t.Fatalf("run2 new=%d withdrawn=%d", n, w)
	}
	if got := q("SELECT count(*) FROM observations WHERE record_id = 'a'"); got != 2 {
		t.Errorf("versions of a = %d, want 2", got)
	}
	if got := q("SELECT count(*) FROM observations WHERE record_id = 'b' AND withdrawn_at IS NOT NULL"); got != 1 {
		t.Errorf("withdrawn b = %d, want 1", got)
	}

	// Run 3: b back unchanged → same version, withdrawal cleared, nothing new.
	n, _ = run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 2}), obs(t, "b", map[string]any{"v": 1})})
	if n != 0 {
		t.Errorf("run3 new=%d, want 0", n)
	}
	if got := q("SELECT count(*) FROM observations WHERE withdrawn_at IS NOT NULL"); got != 0 {
		t.Errorf("withdrawn after reappearance = %d, want 0", got)
	}
}

func TestUnchangedRunWritesNoRows(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	batch := []source.Observation{obs(t, "a", map[string]any{"v": 1})}
	run(t, st, batch)
	var before, after int64
	db.QueryRow(ctx, "SELECT xmin::text::bigint FROM observations").Scan(&before)
	run(t, st, batch)
	db.QueryRow(ctx, "SELECT xmin::text::bigint FROM observations").Scan(&after)
	if before != after {
		t.Errorf("unchanged row was rewritten (xmin %d -> %d)", before, after)
	}
}

func TestAddStoresParsedColumns(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 1})})
	var name, payload, start string
	var lat float64
	err := db.QueryRow(ctx, "SELECT name, payload::text, start_date::text, lat FROM observations").Scan(&name, &payload, &start, &lat)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Joe's" || payload != `{"v": 1}` || start != "2024-01-02" || lat != 36.1 {
		t.Errorf("row = %q %q %q %v", name, payload, start, lat)
	}
}

func TestParseErrorsAndLastGoodRun(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	if _, ok, err := st.LastGoodRun(ctx, "test"); ok || err != nil {
		t.Fatalf("no runs yet: ok=%v err=%v", ok, err)
	}
	r1, err := st.BeginRun(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := source.NewRecord("x", map[string]any{"_raw": "a;b"})
	if err := r1.ParseError(ctx, rec, errors.New("bad row")); err != nil {
		t.Fatal(err)
	}
	if err := r1.Finish(ctx, store.RunStats{Seen: 10}, nil); err != nil {
		t.Fatal(err)
	}
	r2, _ := st.BeginRun(ctx, "test") // reuses the pooled connection: staging tables must be gone
	if err := r2.Finish(ctx, store.RunStats{Seen: 3}, errors.New("boom")); err != nil {
		t.Fatal(err)
	}

	seen, ok, err := st.LastGoodRun(ctx, "test")
	if err != nil || !ok || seen != 10 {
		t.Errorf("LastGoodRun = %d %v %v, want 10 (failed run ignored)", seen, ok, err)
	}
	var msg string
	db.QueryRow(ctx, "SELECT error FROM parse_errors").Scan(&msg)
	if msg != "bad row" {
		t.Errorf("parse error = %q", msg)
	}
}
