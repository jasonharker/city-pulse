package web

import (
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

// A succession lays out the businesses at a place as bars on one time axis,
// oldest first. Undated businesses are left out: their span is when
// city-pulse first saw them, which says nothing about when they opened.
type succession struct {
	From, To int // the axis runs from January 1 of From to January 1 of To
	Bars     []bar
	Dated    int // dated businesses at the place; Bars holds the newest maxBars
}

type bar struct {
	store.BusinessRow
	Left, Width float64 // percent of the axis
}

// ponytail: newest 30 only; a casino has 300+. A zoomable timeline is the upgrade.
const (
	maxBars  = 30
	minWidth = 0.5 // percent, so a one-day business still shows
)

// newSuccession returns nil when fewer than two businesses are dated:
// one bar is no succession. bs is newest first, as PlaceView orders it.
func newSuccession(bs []store.BusinessRow) *succession {
	var dated []store.BusinessRow
	for _, b := range bs {
		if b.Dated {
			dated = append(dated, b)
		}
	}
	if len(dated) < 2 {
		return nil
	}
	shown := dated[:min(len(dated), maxBars)]
	first, last := shown[0].First, shown[0].Last
	for _, b := range shown {
		if b.First.Before(first) {
			first = b.First
		}
		if b.Last.After(last) {
			last = b.Last
		}
	}
	s := &succession{From: first.Year(), To: last.Year() + 1, Dated: len(dated)}
	start := time.Date(s.From, 1, 1, 0, 0, 0, 0, time.UTC)
	span := float64(time.Date(s.To, 1, 1, 0, 0, 0, 0, time.UTC).Sub(start))
	for i := len(shown) - 1; i >= 0; i-- {
		b := shown[i]
		left := min(100*float64(b.First.Sub(start))/span, 100-minWidth)
		width := min(max(100*float64(b.Last.Sub(b.First))/span, minWidth), 100-left)
		s.Bars = append(s.Bars, bar{BusinessRow: b, Left: left, Width: width})
	}
	return s
}

// Through is the last year on the axis.
func (s *succession) Through() int { return s.To - 1 }
