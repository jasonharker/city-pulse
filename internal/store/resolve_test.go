package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jasonharker/city-pulse/internal/model"
	"github.com/jasonharker/city-pulse/internal/resolve"
	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store"
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
	l2 := obs(t, "L", map[string]any{"v": 2, "owners": []string{"ANN SMITH", "BOB JONES"}})
	l2.Name, l2.Parcel, l2.Zip, l2.City = "Joe's Bar", "16209101001", "89109", "Las Vegas"
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
	for _, o := range cur {
		if o.Name == "Joe's Bar" && (o.Parcel != "16209101001" || o.Zip != "89109" || o.City != "Las Vegas" ||
			!slices.Equal(o.Owners, []string{"ANN SMITH", "BOB JONES"})) {
			t.Errorf("license = %+v; want its parcel, zip, city and owners", o)
		}
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

func TestPins(t *testing.T) {
	st, _ := storetest.New(t)
	ctx := context.Background()
	f := func(v float64) *float64 { return &v }
	err := st.ReplaceResolved(ctx, resolve.Result{Places: []model.Place{
		{ID: "1|N|MAIN|", Address: "1 N Main St", Lat: f(36.10), Lon: f(-115.10)},
		{ID: "1|N|MAIN|A", Address: "1 N Main St A", Lat: f(36.1001), Lon: f(-115.1001)}, // a unit shares its building's pin
		{ID: "1|N|MAIN|B", Address: "1 N Main St B"},
		{ID: "2|N|MAIN|7", Address: "2 N Main St 7", Lat: f(36.12), Lon: f(-115.10)}, // no building place: its unit stands in
		{ID: "3|N|MAIN|", Address: "3 N Main St"},                                    // no point: no pin
	}})
	if err != nil {
		t.Fatal(err)
	}
	pins, err := st.Pins(ctx, 36, -116, 37, -115, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]store.Pin{
		"1|N|MAIN|":  {N: 1, Lat: 36.10, Lon: -115.10, ID: "1|N|MAIN|", Address: "1 N Main St", Places: 3},
		"2|N|MAIN|7": {N: 1, Lat: 36.12, Lon: -115.10, ID: "2|N|MAIN|7", Address: "2 N Main St 7", Places: 1},
	}
	if len(pins) != len(want) {
		t.Fatalf("pins = %+v", pins)
	}
	for _, p := range pins {
		if p != want[p.ID] {
			t.Errorf("pin %+v, want %+v", p, want[p.ID])
		}
	}
	// One 0.1-degree cell holds both buildings and all four of their places.
	cells, err := st.Pins(ctx, 36, -116, 37, -115, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 1 || cells[0].N != 2 || cells[0].Places != 4 || cells[0].ID != "" {
		t.Errorf("cells = %+v", cells)
	}
}
