// Package place assigns observations to places: by the published address,
// by a direction filled in from its neighbours, or by coordinates.
package place

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/jasonharker/city-pulse/internal/address"
	"github.com/jasonharker/city-pulse/internal/model"
	"github.com/jasonharker/city-pulse/internal/resolve/graph"
)

// Exact parses every address and places each parseable one at its address key.
type Exact struct{}

func (Exact) Name() string { return model.RuleExact }

func (Exact) Apply(g *graph.Graph) {
	for i, o := range g.Obs {
		a, err := address.Normalize(o.Address)
		if err != nil {
			continue
		}
		g.PlaceOf[i], g.PlaceRule[i] = a.Key(), model.RuleExact
	}
}

// Dir fills a missing direction when exactly one is seen at that number and street.
// A missing unit is never filled: a record without one belongs to the whole
// building, whose units share its key as a prefix.
type Dir struct{}

func (Dir) Name() string { return model.RuleDir }

// It reads the exact placements' IDs, which are parsed addresses:
// "number|dir|street|unit" (see address.Key).
func (Dir) Apply(g *graph.Graph) {
	dirsAt := map[string]map[string]bool{}
	for i, id := range g.PlaceOf {
		if num, dir, street, _ := parts(id); g.PlaceRule[i] == model.RuleExact && dir != "" {
			k := num + "|" + street
			if dirsAt[k] == nil {
				dirsAt[k] = map[string]bool{}
			}
			dirsAt[k][dir] = true
		}
	}
	for i, id := range g.PlaceOf {
		num, dir, street, unit := parts(id)
		if d := dirsAt[num+"|"+street]; g.PlaceRule[i] == model.RuleExact && dir == "" && len(d) == 1 {
			g.PlaceOf[i], g.PlaceRule[i] = num+"|"+only(d)+"|"+street+"|"+unit, model.RuleDir
		}
	}
}

// parts splits a place ID. address.Normalize reads "|" as a space, so no part holds one.
func parts(id string) (num, dir, street, unit string) {
	p := strings.SplitN(id, "|", 4)
	if len(p) < 4 {
		return "", "", "", ""
	}
	return p[0], p[1], p[2], p[3]
}

// Collect derives the places from the observations placed so far: each takes
// its most common published spelling and its records' average point.
type Collect struct{}

func (Collect) Name() string { return "places" }

func (Collect) Apply(g *graph.Graph) {
	spellings := map[string]map[string]int{}
	type sum struct {
		lat, lon float64
		k        int
	}
	coords := map[string]*sum{}
	for i, id := range g.PlaceOf {
		if id == "" {
			continue
		}
		if spellings[id] == nil {
			spellings[id] = map[string]int{}
			coords[id] = &sum{}
		}
		o := g.Obs[i]
		spellings[id][o.Address]++
		if o.Lat != nil {
			c := coords[id]
			c.lat, c.lon, c.k = c.lat+*o.Lat, c.lon+*o.Lon, c.k+1
		}
	}
	places := make([]model.Place, 0, len(spellings))
	for id, sp := range spellings {
		p := model.Place{ID: id, Address: graph.MostCommon(sp)}
		if c := coords[id]; c.k > 0 {
			lat, lon := c.lat/float64(c.k), c.lon/float64(c.k)
			p.Lat, p.Lon = &lat, &lon
		}
		places = append(places, p)
	}
	slices.SortFunc(places, func(a, b model.Place) int { return cmp.Compare(a.ID, b.ID) })
	g.Places = places
}

// nearMeters is how far an unparseable record may be from the place it joins.
const nearMeters = 60

// Near places a record that has no place yet but has coordinates at the
// nearest place within nearMeters. It must run after Collect.
type Near struct{}

func (Near) Name() string { return model.RuleNear }

func (Near) Apply(g *graph.Graph) {
	near := newGrid(g.Places)
	for i, o := range g.Obs {
		if g.PlaceOf[i] == "" && o.Lat != nil {
			if id := near.nearest(*o.Lat, *o.Lon); id != "" {
				g.PlaceOf[i], g.PlaceRule[i] = id, model.RuleNear
			}
		}
	}
}

// grid finds the nearest place within nearMeters. Cells are ~110 m tall.
// ponytail: equirectangular distance, fine at city scale.
type grid map[[2]int][]model.Place

const cell = 0.001

func newGrid(places []model.Place) grid {
	g := grid{}
	for _, p := range places {
		if p.Lat != nil {
			k := [2]int{int(math.Floor(*p.Lat / cell)), int(math.Floor(*p.Lon / cell))}
			g[k] = append(g[k], p)
		}
	}
	return g
}

func (g grid) nearest(lat, lon float64) string {
	best, bestD := "", float64(nearMeters)
	cy, cx := int(math.Floor(lat/cell)), int(math.Floor(lon/cell))
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			for _, p := range g[[2]int{cy + dy, cx + dx}] {
				y := (*p.Lat - lat) * 111_320
				x := (*p.Lon - lon) * 111_320 * math.Cos(lat*math.Pi/180)
				if d := math.Hypot(x, y); d < bestD || (d == bestD && p.ID < best) {
					best, bestD = p.ID, d
				}
			}
		}
	}
	return best
}

// only returns the one key of a one-key set.
func only(m map[string]bool) string {
	for k := range m {
		return k
	}
	return ""
}
