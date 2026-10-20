package web

import (
	"fmt"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

func TestSuccession(t *testing.T) {
	d := func(y, m, day int) time.Time { return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC) }
	// Newest first, as PlaceView orders them.
	bs := []store.BusinessRow{
		{ID: "undated", First: d(2026, 10, 15), Last: d(2026, 10, 15)},
		{ID: "tacos", First: d(2015, 1, 1), Last: d(2019, 12, 31), Dated: true},
		{ID: "bar", First: d(2010, 1, 1), Last: d(2015, 1, 1), Dated: true},
	}
	s := newSuccession(bs)
	if s == nil || s.From != 2010 || s.To != 2020 || s.Dated != 2 || len(s.Bars) != 2 {
		t.Fatalf("succession = %+v; want 2010-2020 with two bars", s)
	}
	if b := s.Bars[0]; b.ID != "bar" || b.Left != 0 || fmt.Sprintf("%.1f", b.Width) != "50.0" {
		t.Errorf("first bar = %s %.2f+%.2f; want bar at 0 for 50%%", b.ID, b.Left, b.Width)
	}
	if b := s.Bars[1]; b.ID != "tacos" || fmt.Sprintf("%.1f", b.Left) != "50.0" || b.Left+b.Width > 100 {
		t.Errorf("second bar = %s %.2f+%.2f; want tacos from 50%% to the end", b.ID, b.Left, b.Width)
	}

	// A one-day business still gets a visible bar, inside the axis.
	bs[1].Last = bs[1].First.Add(24 * time.Hour)
	bs[2].First, bs[2].Last = d(2010, 12, 31), d(2010, 12, 31)
	for _, b := range newSuccession(bs[1:]).Bars {
		if b.Width < minWidth || b.Left+b.Width > 100 {
			t.Errorf("bar %s = %.2f+%.2f; want at least %.1f wide, inside 100", b.ID, b.Left, b.Width, minWidth)
		}
	}

	if newSuccession(bs[:2]) != nil {
		t.Error("one dated business should give no succession")
	}
	many := make([]store.BusinessRow, maxBars+5)
	for i := range many {
		many[i] = store.BusinessRow{ID: fmt.Sprint(i), First: d(2040-i, 1, 1), Last: d(2040-i, 6, 1), Dated: true} // newest first
	}
	if s := newSuccession(many); len(s.Bars) != maxBars || s.Dated != maxBars+5 {
		t.Errorf("bars = %d of %d; want %d of %d", len(s.Bars), s.Dated, maxBars, maxBars+5)
	}
}
