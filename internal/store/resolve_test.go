package store_test

import (
	"context"
	"testing"

	"github.com/jasonharker/city-pulse/internal/resolve"
	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store/storetest"
)

func TestCurrentObservationsAndReplace(t *testing.T) {
	st, conn := storetest.New(t)
	ctx := context.Background()

	// Run 1 publishes two snapshots of permit P at once; run 2 changes license L.
	p1 := obs(t, "P", map[string]any{"s": "Accepted"})
	p1.Status = "Accepted"
	p2 := obs(t, "P", map[string]any{"s": "Closed"})
	p2.Status = "Closed"
	l1 := obs(t, "L", map[string]any{"v": 1})
	run(t, st, []source.Observation{p2, p1, l1}) // Closed first: id order must not decide
	l2 := obs(t, "L", map[string]any{"v": 2})
	l2.Name = "Joe's Bar"
	run(t, st, []source.Observation{p1, p2, l2})

	cur, err := st.CurrentObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, o := range cur {
		got[o.Name+"/"+o.Status] = true
	}
	if len(cur) != 2 || !got["Joe's/Closed"] || !got["Joe's Bar/"] {
		t.Fatalf("current = %+v; want the Closed permit snapshot and run 2's license", cur)
	}

	// Replacing twice leaves only the second result.
	for range 2 {
		if err := st.ReplaceResolved(ctx, resolve.Build(cur)); err != nil {
			t.Fatal(err)
		}
	}
	var places, links int
	if err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM places), (SELECT count(*) FROM links)").Scan(&places, &links); err != nil {
		t.Fatal(err)
	}
	if places != 1 || links != 2 {
		t.Errorf("places, links = %d, %d; want 1, 2", places, links)
	}
}
