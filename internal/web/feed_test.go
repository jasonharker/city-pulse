package web

import (
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

func TestNights(t *testing.T) {
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }
	ns := nights([]store.Change{
		{What: "new", At: at(19, 10)},
		{What: "withdrawn", At: at(19, 10)},
		{What: "updated", At: at(18, 23)},
		{What: "new", At: at(18, 10)},
	})
	if len(ns) != 2 {
		t.Fatalf("nights = %d, want 2", len(ns))
	}
	if n := ns[0]; !n.Date.Equal(at(19, 0)) || n.New != 1 || n.Withdrawn != 1 || len(n.Changes) != 2 {
		t.Errorf("first night = %+v", n)
	}
	if n := ns[1]; !n.Date.Equal(at(18, 0)) || n.New != 1 || n.Updated != 1 {
		t.Errorf("second night = %+v", n)
	}
}

func TestPageNights(t *testing.T) {
	at := func(day int) time.Time { return time.Date(2026, 10, day, 10, 0, 0, 0, time.UTC) }
	cs := []store.Change{{What: "new", At: at(19)}, {What: "new", At: at(18)}, {What: "new", At: at(18)}}
	ns, trunc := pageNights(cs, 2)
	if !trunc || len(ns) != 2 || ns[0].Partial || !ns[1].Partial || len(ns[1].Changes) != 1 {
		t.Errorf("capped: trunc=%v nights=%+v", trunc, ns)
	}
	ns, trunc = pageNights(cs, 3)
	if trunc || ns[1].Partial {
		t.Errorf("uncapped: trunc=%v nights=%+v", trunc, ns)
	}
	// The cap falls between nights: every shown night is whole.
	ns, trunc = pageNights(append(cs, store.Change{What: "new", At: at(17)}), 3)
	if !trunc || len(ns) != 2 || ns[1].Partial || len(ns[1].Changes) != 2 {
		t.Errorf("capped between nights: trunc=%v nights=%+v", trunc, ns)
	}
}
