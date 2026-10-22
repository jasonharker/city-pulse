// Package resolve derives places and businesses from observations. It is pure:
// the same observations always produce the same result, with stable IDs.
package resolve

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jasonharker/city-pulse/internal/address"
	"github.com/jasonharker/city-pulse/internal/model"
)

// minPrefix is the shortest name (letters and digits) that absorbs longer ones,
// so "BAR" does not swallow every "BAR ..." in a casino.
const minPrefix = 5

// Result is a full resolve.
type Result struct {
	Places     []model.Place
	Businesses []model.Business
	Links      []model.Link
}

// nearMeters is how far an unparseable record may be from the place it joins.
const nearMeters = 60

// closed license statuses end a business's span.
var closed = map[string]bool{"closed": true, "inactive": true, "out of business": true,
	"revoked": true, "cancelled": true, "expired": true}

// Build resolves obs.
func Build(obs []model.Obs) Result {
	n := len(obs)
	addrs := make([]address.Address, n)
	parsed := make([]bool, n)
	for i, o := range obs {
		a, err := address.Normalize(o.Address)
		addrs[i], parsed[i] = a, err == nil
	}

	// Fill a missing direction when exactly one is seen at that number and street.
	// A missing unit is never filled: a record without one belongs to the whole
	// building, whose units share its key as a prefix.
	dirsAt := map[string]map[string]bool{}
	for i, a := range addrs {
		if parsed[i] && a.Dir != "" {
			add(dirsAt, a.Number+"|"+a.Street, a.Dir)
		}
	}
	placeRule := make([]string, n)
	placeOf := make([]string, n)
	for i := range addrs {
		if !parsed[i] {
			continue
		}
		placeRule[i] = model.RuleExact
		if d := dirsAt[addrs[i].Number+"|"+addrs[i].Street]; addrs[i].Dir == "" && len(d) == 1 {
			addrs[i].Dir, placeRule[i] = only(d), model.RuleDir
		}
		placeOf[i] = addrs[i].Key()
	}

	places := buildPlaces(obs, placeOf)
	near := newGrid(places)
	for i, o := range obs {
		if placeOf[i] == "" && o.Lat != nil {
			if id := near.nearest(*o.Lat, *o.Lon); id != "" {
				placeOf[i], placeRule[i] = id, model.RuleNear
			}
		}
	}

	bizOf, bizRule, businesses := buildBusinesses(obs, placeOf)
	links := make([]model.Link, n)
	for i, o := range obs {
		links[i] = model.Link{ObservationID: o.ID, PlaceID: placeOf[i], PlaceRule: placeRule[i],
			BusinessID: bizOf[i], BusinessRule: bizRule[i]}
	}
	return Result{Places: places, Businesses: businesses, Links: links}
}

func buildPlaces(obs []model.Obs, placeOf []string) []model.Place {
	spellings := map[string]map[string]int{}
	type sum struct {
		lat, lon float64
		k        int
	}
	coords := map[string]*sum{}
	for i, id := range placeOf {
		if id == "" {
			continue
		}
		if spellings[id] == nil {
			spellings[id] = map[string]int{}
			coords[id] = &sum{}
		}
		spellings[id][obs[i].Address]++
		if obs[i].Lat != nil {
			c := coords[id]
			c.lat, c.lon, c.k = c.lat+*obs[i].Lat, c.lon+*obs[i].Lon, c.k+1
		}
	}
	places := make([]model.Place, 0, len(spellings))
	for id, sp := range spellings {
		p := model.Place{ID: id, Address: mostCommon(sp)}
		if c := coords[id]; c.k > 0 {
			lat, lon := c.lat/float64(c.k), c.lon/float64(c.k)
			p.Lat, p.Lon = &lat, &lon
		}
		places = append(places, p)
	}
	slices.SortFunc(places, func(a, b model.Place) int { return cmp.Compare(a.ID, b.ID) })
	return places
}

// buildBusinesses clusters named observations at each place. Records are one
// business when they share a business key; when their names match ignoring
// spacing or word order ("SPORTSBAR" / "SPORTS BAR", "BOTANICA RAFAELA" /
// "RAFAELA BOTANICA"); or when one name is a whole-word prefix of the other
// ("SUBWAY" / "SUBWAY #27601"; SNHD permits each station of one restaurant).
// Records with no place cluster by business key alone.
func buildBusinesses(obs []model.Obs, placeOf []string) (bizOf, bizRule []string, out []model.Business) {
	n := len(obs)
	bizOf, bizRule = make([]string, n), make([]string, n)
	uf := newUnionFind(n)
	firstByKey := map[string]int{} // place + business key or name form -> first record
	join := func(k string, i int, rule string) {
		if j, ok := firstByKey[k]; !ok {
			firstByKey[k] = i
		} else if uf.union(i, j) && rule != "" {
			bizRule[i] = rule
		}
	}
	type named struct {
		name string
		i    int
	}
	namesAt := map[string][]named{}
	for i, o := range obs {
		if o.Name == "" {
			continue // permits name no operator
		}
		if o.BusinessKey != "" {
			join(placeOf[i]+"|key|"+o.BusinessKey, i, "")
		}
		nm := NormName(o.Name)
		if placeOf[i] == "" || nm == "" {
			continue
		}
		toks := strings.Fields(nm)
		join(placeOf[i]+"|name|"+strings.Join(toks, ""), i, model.RuleName)
		join(placeOf[i]+"|words|"+wordsKey(toks), i, model.RuleName)
		namesAt[placeOf[i]] = append(namesAt[placeOf[i]], named{nm, i})
	}
	// In sorted order every name that extends "subway " follows "subway" directly.
	for _, ns := range namesAt {
		slices.SortFunc(ns, func(a, b named) int { return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.i, b.i)) })
		for a := range ns {
			if len(strings.ReplaceAll(ns[a].name, " ", "")) < minPrefix {
				continue
			}
			for b := a + 1; b < len(ns) && strings.HasPrefix(ns[b].name, ns[a].name+" "); b++ {
				if uf.union(ns[b].i, ns[a].i) {
					bizRule[ns[b].i] = model.RulePrefix
				}
			}
		}
	}

	members := map[int][]int{}
	for i, o := range obs {
		if o.Name != "" {
			r := uf.find(i)
			members[r] = append(members[r], i)
		}
	}
	for _, ms := range members {
		place := placeOf[ms[0]]
		names := map[string]int{}
		keys := []string{}
		b := model.Business{PlaceID: place, Closed: true}
		for _, i := range ms {
			o := obs[i]
			names[o.Name]++
			if o.BusinessKey != "" {
				keys = append(keys, o.BusinessKey)
			}
			if b.First.IsZero() || o.Date.Before(b.First) {
				b.First = o.Date
			}
			last := o.Date
			if o.End != nil && o.End.After(last) {
				last = *o.End
			}
			if last.After(b.Last) {
				b.Last = last
			}
			if !closed[strings.ToLower(o.Status)] && o.End == nil {
				b.Closed = false
			}
		}
		b.Name = mostCommon(names)
		slices.Sort(keys)
		// ponytail: a record with a smaller key joining later changes the ID (and URL);
		// keep a redirect table of old IDs if that starts breaking links people share.
		b.ID = place + "#" + first(keys, "obs:"+strconv.FormatInt(obs[ms[0]].ID, 10))
		out = append(out, b)
		for _, i := range ms {
			bizOf[i] = b.ID
			if bizRule[i] == "" {
				bizRule[i] = model.RuleKey
			}
		}
	}
	slices.SortFunc(out, func(a, b model.Business) int { return cmp.Compare(a.ID, b.ID) })
	return bizOf, bizRule, out
}

// wordsKey is a name's words in sorted order, without store numbers or a plural
// "s": "In N Out Burgers" and "IN-N-OUT BURGER #88" match.
func wordsKey(toks []string) string {
	var ws []string
	for _, t := range toks {
		if strings.Trim(t, "0123456789") == "" {
			continue
		}
		if len(t) > 3 {
			t = strings.TrimSuffix(t, "s")
		}
		ws = append(ws, t)
	}
	slices.Sort(ws)
	return strings.Join(ws, " ")
}

func first(s []string, fallback string) string {
	if len(s) == 0 {
		return fallback
	}
	return s[0]
}

func add(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

func only(m map[string]bool) string {
	for k := range m {
		return k
	}
	return ""
}

// mostCommon returns the most frequent string, ties broken alphabetically.
func mostCommon(m map[string]int) string {
	best, bestN := "", 0
	for s, k := range m {
		if k > bestN || (k == bestN && s < best) {
			best, bestN = s, k
		}
	}
	return best
}

type unionFind []int

func newUnionFind(n int) unionFind {
	u := make(unionFind, n)
	for i := range u {
		u[i] = i
	}
	return u
}

func (u unionFind) find(i int) int {
	for u[i] != i {
		u[i] = u[u[i]]
		i = u[i]
	}
	return i
}

// union joins the sets of i and j and reports whether they were separate.
// The smaller root wins, so the result does not depend on call order.
func (u unionFind) union(i, j int) bool {
	a, b := u.find(i), u.find(j)
	if a == b {
		return false
	}
	u[max(a, b)] = min(a, b)
	return true
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
