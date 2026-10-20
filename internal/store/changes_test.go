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

	// Run 1 is the initial load; run 2 changes a, drops b and adds c.
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 1}), obs(t, "b", map[string]any{"v": 1})})
	resolveAll()
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 2}), obs(t, "c", map[string]any{"v": 1})})
	resolveAll()

	cs, err := st.Changes(ctx, since, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cs {
		got[c.RecordID] = c.What
		if c.PlaceID == "" || c.BusinessID == "" {
			t.Errorf("change %+v has no place or business", c)
		}
	}
	if want := map[string]string{"a": "updated", "b": "withdrawn", "c": "new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v (the initial load is not a change)", got, want)
	}

	if later, err := st.Changes(ctx, time.Now(), 100); err != nil || len(later) != 0 {
		t.Errorf("Changes since now = %d, %v; want none (both runs started before)", len(later), err)
	}

	addr, pcs, err := st.PlaceChanges(ctx, cs[0].PlaceID)
	if err != nil || addr != "1 Main St" || len(pcs) != 3 {
		t.Errorf("PlaceChanges = %q, %d changes, %v; want 1 Main St, 3", addr, len(pcs), err)
	}
	name, bcs, err := st.BusinessChanges(ctx, cs[0].BusinessID)
	if err != nil || name != "Joe's" || len(bcs) != 3 {
		t.Errorf("BusinessChanges = %q, %d changes, %v; want Joe's, 3", name, len(bcs), err)
	}
	if _, _, err := st.PlaceChanges(ctx, "nowhere"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("PlaceChanges(nowhere) err = %v, want ErrNotFound", err)
	}

	rv, err := st.RecordView(ctx, "test", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.Versions) != 2 || rv.PlaceID == "" {
		t.Fatalf("RecordView(a) = %+v", rv)
	}
	// Newest first.
	if v := rv.Versions[1]; !v.First || !reflect.DeepEqual(v.Fields, []store.FieldChange{{Field: "v", To: "1"}}) {
		t.Errorf("first version = %+v, want the whole payload", v)
	}
	if v := rv.Versions[0]; v.First || !reflect.DeepEqual(v.Fields, []store.FieldChange{{Field: "v", From: "1", To: "2"}}) {
		t.Errorf("second version = %+v, want v 1 -> 2", v)
	}
	if _, err := st.RecordView(ctx, "test", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordView(nope) err = %v, want ErrNotFound", err)
	}
}
