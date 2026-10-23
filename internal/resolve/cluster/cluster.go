// Package cluster turns business edges into businesses: it applies the edges
// in order, then gives each cluster of named records a name, span and ID.
package cluster

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/jasonharker/city-pulse/internal/model"
	"github.com/jasonharker/city-pulse/internal/resolve/graph"
)

// closed license statuses end a business's span.
var closed = map[string]bool{"closed": true, "inactive": true, "out of business": true,
	"revoked": true, "cancelled": true, "expired": true}

// Businesses is the clustering stage.
type Businesses struct{}

func (Businesses) Name() string { return "cluster" }

// Apply labels a record with the rule of the last edge that merged it into a
// larger cluster; a record merged only by shared keys, or not at all, is "key".
func (Businesses) Apply(g *graph.Graph) {
	uf := newUnionFind(len(g.Obs))
	for _, e := range g.Edges {
		if uf.union(e.A, e.B) && e.Rule != "" {
			g.BizRule[e.A] = e.Rule
		}
	}

	members := map[int][]int{}
	for i, o := range g.Obs {
		if o.Name != "" {
			r := uf.find(i)
			members[r] = append(members[r], i)
		}
	}
	var out []model.Business
	for _, ms := range members {
		place := g.PlaceOf[ms[0]]
		names := map[string]int{}
		keys := []string{}
		b := model.Business{PlaceID: place, Closed: true}
		for _, i := range ms {
			o := g.Obs[i]
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
		b.Name = graph.MostCommon(names)
		slices.Sort(keys)
		// ponytail: a record with a smaller key joining later changes the ID (and URL);
		// keep a redirect table of old IDs if that starts breaking links people share.
		b.ID = place + "#" + first(keys, "obs:"+strconv.FormatInt(g.Obs[ms[0]].ID, 10))
		out = append(out, b)
		for _, i := range ms {
			g.BizOf[i] = b.ID
			if g.BizRule[i] == "" {
				g.BizRule[i] = model.RuleKey
			}
		}
	}
	slices.SortFunc(out, func(a, b model.Business) int { return cmp.Compare(a.ID, b.ID) })
	g.Businesses = out
}

func first(s []string, fallback string) string {
	if len(s) == 0 {
		return fallback
	}
	return s[0]
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
