package resolve

import (
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/model"
	"github.com/jasonharker/city-pulse/internal/resolve/graph"
)

func TestBuild(t *testing.T) {
	ll := func(v float64) *float64 { return &v }
	y := func(year int) time.Time { return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC) }
	obs := []model.Obs{
		// One storefront: a bar seen by two agencies, then a taco place.
		{ID: 1, Name: "JOE'S BAR", Address: "3570 S LAS VEGAS BLVD STE 100", BusinessKey: "snhd:1", Date: y(2018), Lat: ll(36.1), Lon: ll(-115.17)},
		{ID: 2, Name: "Joes Bar LLC", Address: "3570 Las Vegas Blvd South Suite 100", BusinessKey: "clv:9", Status: "Closed", Date: y(2019)},
		{ID: 3, Name: "TACO PLACE", Address: "3570 S LAS VEGAS BLVD #100", BusinessKey: "snhd:2", Date: y(2023)},
		{ID: 4, Name: "TACO PLACE", Address: "3570 S LAS VEGAS BLVD #100", BusinessKey: "snhd:2", Date: y(2024)},
		// The unit next door is another place; a permit without direction finds it.
		{ID: 5, Name: "VAPE CITY", Address: "3570 S LAS VEGAS BLVD STE 200", BusinessKey: "snhd:3", Date: y(2020)},
		{ID: 6, Address: "3570 LAS VEGAS BLVD STE 200", Date: y(2021)},
		// A record without a unit stays on the building, not its one known unit.
		{ID: 7, Name: "OAK DENTAL", Address: "500 W OAK AVE STE 7", BusinessKey: "clv:1", Date: y(2015)},
		{ID: 8, Address: "500 W OAK AVE", Date: y(2016)},
		// Name variants at one place: a station permit, and reordered words.
		{ID: 11, Name: "OAK DENTAL - CLEANING ROOM", Address: "500 W OAK AVE STE 7", BusinessKey: "snhd:8", Date: y(2016)},
		{ID: 12, Name: "Dental, Oak", Address: "500 W OAK AVE STE 7", BusinessKey: "snhd:9", Date: y(2017)},
		{ID: 13, Name: "Oak Dentals #2", Address: "500 W OAK AVE STE 7", BusinessKey: "cc:4", Date: y(2018)},
		// No usable address: coordinates near the bar, or nothing at all.
		{ID: 9, Name: "BAR CART", Address: "", BusinessKey: "snhd:4", Date: y(2022), Lat: ll(36.1002), Lon: ll(-115.17)},
		{ID: 10, Name: "HOME BIZ", Address: "", BusinessKey: "clv:5", Date: y(2020)},
	}
	r := Build(obs)

	bar := "3570|S|LAS VEGAS|100"
	lk := func(id int64, place, placeRule, biz, bizRule string) model.Link {
		return model.Link{ObservationID: id, PlaceID: place, PlaceRule: placeRule, BusinessID: biz, BusinessRule: bizRule}
	}
	want := map[int64]model.Link{
		1:  lk(1, bar, model.RuleExact, bar+"#clv:9", model.RuleKey),
		2:  lk(2, bar, model.RuleExact, bar+"#clv:9", model.RuleName),
		3:  lk(3, bar, model.RuleExact, bar+"#snhd:2", model.RuleKey),
		4:  lk(4, bar, model.RuleExact, bar+"#snhd:2", model.RuleKey),
		5:  lk(5, "3570|S|LAS VEGAS|200", model.RuleExact, "3570|S|LAS VEGAS|200#snhd:3", model.RuleKey),
		6:  lk(6, "3570|S|LAS VEGAS|200", model.RuleDir, "", ""),
		7:  lk(7, "500|W|OAK|7", model.RuleExact, "500|W|OAK|7#cc:4", model.RuleKey),
		8:  lk(8, "500|W|OAK|", model.RuleExact, "", ""),
		9:  lk(9, bar, model.RuleNear, bar+"#snhd:4", model.RuleKey),
		10: lk(10, "", "", "#clv:5", model.RuleKey),
		11: lk(11, "500|W|OAK|7", model.RuleExact, "500|W|OAK|7#cc:4", model.RulePrefix),
		12: lk(12, "500|W|OAK|7", model.RuleExact, "500|W|OAK|7#cc:4", model.RuleName),
		13: lk(13, "500|W|OAK|7", model.RuleExact, "500|W|OAK|7#cc:4", model.RuleName),
	}
	for _, l := range r.Links {
		if l != want[l.ObservationID] {
			t.Errorf("link %d = %+v, want %+v", l.ObservationID, l, want[l.ObservationID])
		}
	}
	if len(r.Places) != 4 || len(r.Businesses) != 6 {
		t.Errorf("got %d places, %d businesses; want 4, 6", len(r.Places), len(r.Businesses))
	}
	for _, b := range r.Businesses {
		switch b.ID {
		case bar + "#clv:9":
			if b.First != y(2018) || b.Last != y(2019) || b.Closed {
				t.Errorf("bar span %+v: want 2018-2019, open (the inspection carries no closed status)", b)
			}
		case bar + "#snhd:2":
			if b.Name != "TACO PLACE" || b.First != y(2023) || b.Last != y(2024) {
				t.Errorf("taco span %+v", b)
			}
		}
	}
}

func TestRunWithoutStage(t *testing.T) {
	// Without dir, a permit missing its direction stays at its own address key.
	obs := []model.Obs{
		{ID: 1, Name: "VAPE CITY", Address: "3570 S LAS VEGAS BLVD STE 200", BusinessKey: "snhd:3"},
		{ID: 2, Address: "3570 LAS VEGAS BLVD STE 200"},
	}
	var stages []graph.Stage
	for _, s := range Default() {
		if s.Name() != model.RuleDir {
			stages = append(stages, s)
		}
	}
	r := Run(obs, stages)
	if l := r.Links[1]; l.PlaceID != "3570||LAS VEGAS|200" || l.PlaceRule != model.RuleExact {
		t.Errorf("link 2 = %+v; want its own key, exact", l)
	}
	if got := len(r.Stages); got != len(Default())-1 {
		t.Errorf("%d stage runs, want %d", got, len(Default())-1)
	}
}
