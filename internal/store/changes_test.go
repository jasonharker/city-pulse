package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/resolve"
	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store"
	"github.com/jasonharker/city-pulse/internal/store/storetest"
)

func TestChangesAndRecordView(t *testing.T) {
	st, _ := storetest.New(t)
	ctx := context.Background()
	resolveAll := func() {
		t.Helper()
		cur, err := st.CurrentObservations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceResolved(ctx, resolve.Build(cur)); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Now().Add(-time.Minute)

	// Run 1 is the initial load; run 2 changes a, drops b and adds c; run 3
	// changes a again, so a's run-2 version is superseded.
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 1}), obs(t, "b", map[string]any{"v": 1})})
	resolveAll()
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 2}), obs(t, "c", map[string]any{"v": 1})})
	resolveAll()
	mid := time.Now()
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 3}), obs(t, "c", map[string]any{"v": 1})})
	resolveAll()

	cs, err := st.Changes(ctx, since, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range cs {
		got[c.RecordID+" "+c.What]++
		if c.PlaceID == "" || c.BusinessID == "" {
			t.Errorf("change %+v has no place or business", c)
		}
	}
	if want := map[string]int{"a updated": 2, "b withdrawn": 1, "c new": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v (the initial load is not a change)", got, want)
	}

	if later, err := st.Changes(ctx, time.Now(), 100); err != nil || len(later) != 0 {
		t.Errorf("Changes since now = %d, %v; want none (all runs started before)", len(later), err)
	}

	if top, err := st.Changes(ctx, since, 1); err != nil || len(top) != 1 || top[0].ObservationID != cs[0].ObservationID {
		t.Errorf("Changes limit 1 = %+v, %v; want the newest change %+v", top, err, cs[0])
	}

	recent, err := st.Changes(ctx, mid, 100)
	if err != nil || len(recent) != 1 || recent[0].RecordID != "a" || recent[0].What != "updated" {
		t.Errorf("Changes since mid = %+v, %v; want only run 3's update of a", recent, err)
	}

	addr, pcs, err := st.PlaceChanges(ctx, cs[0].PlaceID)
	if err != nil || addr != "1 Main St" || len(pcs) != 4 {
		t.Errorf("PlaceChanges = %q, %d changes, %v; want 1 Main St, 4", addr, len(pcs), err)
	}
	name, bcs, err := st.BusinessChanges(ctx, cs[0].BusinessID)
	if err != nil || name != "Joe's" || len(bcs) != 4 {
		t.Errorf("BusinessChanges = %q, %d changes, %v; want Joe's, 4", name, len(bcs), err)
	}
	for _, c := range append(pcs, bcs...) {
		if c.PlaceID == "" || c.BusinessID == "" {
			t.Errorf("scoped change %+v has no place or business", c)
		}
	}
	if _, _, err := st.PlaceChanges(ctx, "nowhere"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("PlaceChanges(nowhere) err = %v, want ErrNotFound", err)
	}

	rv, err := st.RecordView(ctx, "test", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.Versions) != 3 || rv.PlaceID == "" {
		t.Fatalf("RecordView(a) = %+v", rv)
	}
	// Newest first.
	if v := rv.Versions[2]; !v.First || !reflect.DeepEqual(v.Fields, []store.FieldChange{{Field: "v", To: "1"}}) {
		t.Errorf("first version = %+v, want the whole payload", v)
	}
	if v := rv.Versions[1]; v.First || !reflect.DeepEqual(v.Fields, []store.FieldChange{{Field: "v", From: "1", To: "2"}}) {
		t.Errorf("second version = %+v, want v 1 -> 2", v)
	}
	if v := rv.Versions[0]; v.First || !reflect.DeepEqual(v.Fields, []store.FieldChange{{Field: "v", From: "2", To: "3"}}) {
		t.Errorf("third version = %+v, want v 2 -> 3", v)
	}
	if _, err := st.RecordView(ctx, "test", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordView(nope) err = %v, want ErrNotFound", err)
	}
}

// The permit archive publishes several lifecycle snapshots of one permit in
// one run; the record page's newest version must be the one resolve picks.
func TestRecordViewSameRunOrder(t *testing.T) {
	st, _ := storetest.New(t)
	ctx := context.Background()
	closed := obs(t, "p", map[string]any{"STAT": "Closed"})
	closed.Status = "Closed"
	accepted := obs(t, "p", map[string]any{"STAT": "Accepted"})
	accepted.Status = "Accepted"
	// Separate batches, so Closed gets the lower id.
	run(t, st, []source.Observation{closed}, []source.Observation{accepted})

	cur, err := st.CurrentObservations(ctx)
	if err != nil || len(cur) != 1 {
		t.Fatalf("CurrentObservations = %+v, %v", cur, err)
	}
	rv, err := st.RecordView(ctx, "test", "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := rv.Latest().Status; got != "Closed" || got != cur[0].Status {
		t.Errorf("RecordView newest status = %q, CurrentObservations = %q; want both Closed", got, cur[0].Status)
	}
	if v := rv.Latest(); v.First || !reflect.DeepEqual(v.Fields, []store.FieldChange{{Field: "STAT", From: "Accepted", To: "Closed"}}) {
		t.Errorf("newest version = %+v, want STAT Accepted -> Closed", v)
	}
}
