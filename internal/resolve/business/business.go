// Package business finds evidence that observations at one place are one
// operator. Each stage only adds edges; clustering decides.
package business

import (
	"cmp"
	"slices"
	"strings"

	"github.com/jasonharker/city-pulse/internal/model"
	"github.com/jasonharker/city-pulse/internal/resolve/graph"
)

// joiner links each observation to the first one that had the same key.
type joiner struct {
	g     *graph.Graph
	first map[string]int
	rule  string
}

func newJoiner(g *graph.Graph, rule string) *joiner {
	return &joiner{g: g, first: map[string]int{}, rule: rule}
}

func (j *joiner) join(k string, i int) {
	if f, ok := j.first[k]; !ok {
		j.first[k] = i
	} else {
		j.g.Join(i, f, j.rule)
	}
}

// Key joins named records at one place that share a source business key.
// Records with no place join by key alone.
type Key struct{}

func (Key) Name() string { return model.RuleKey }

func (Key) Apply(g *graph.Graph) {
	j := newJoiner(g, "") // "" is the default reason: clustering labels it key
	for i, o := range g.Obs {
		if o.Name != "" && o.BusinessKey != "" { // permits name no operator
			j.join(g.PlaceOf[i]+"|"+o.BusinessKey, i)
		}
	}
}

// Name joins records at one place whose names match ignoring spacing
// ("SPORTSBAR" / "SPORTS BAR") or word order ("BOTANICA RAFAELA" / "RAFAELA BOTANICA").
type Name struct{}

func (Name) Name() string { return model.RuleName }

func (Name) Apply(g *graph.Graph) {
	spacing, words := newJoiner(g, model.RuleName), newJoiner(g, model.RuleName)
	for i, o := range g.Obs {
		nm := NormName(o.Name)
		if o.Name == "" || g.PlaceOf[i] == "" || nm == "" {
			continue
		}
		toks := strings.Fields(nm)
		spacing.join(g.PlaceOf[i]+"|"+strings.Join(toks, ""), i)
		words.join(g.PlaceOf[i]+"|"+wordsKey(toks), i)
	}
}

// minPrefix is the shortest name (letters and digits) that absorbs longer ones,
// so "BAR" does not swallow every "BAR ..." in a casino.
const minPrefix = 5

// Prefix joins a record to another at its place whose name is a whole-word
// prefix of its own ("SUBWAY" / "SUBWAY #27601"; SNHD permits each station of
// one restaurant).
type Prefix struct{}

func (Prefix) Name() string { return model.RulePrefix }

func (Prefix) Apply(g *graph.Graph) {
	type named struct {
		name string
		i    int
	}
	namesAt := map[string][]named{}
	for i, o := range g.Obs {
		if nm := NormName(o.Name); o.Name != "" && g.PlaceOf[i] != "" && nm != "" {
			namesAt[g.PlaceOf[i]] = append(namesAt[g.PlaceOf[i]], named{nm, i})
		}
	}
	// Places in order, so edges are too.
	places := make([]string, 0, len(namesAt))
	for p := range namesAt {
		places = append(places, p)
	}
	slices.Sort(places)
	// In sorted order every name that extends "subway " follows "subway" directly.
	for _, p := range places {
		ns := namesAt[p]
		slices.SortFunc(ns, func(a, b named) int { return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.i, b.i)) })
		for a := range ns {
			if len(strings.ReplaceAll(ns[a].name, " ", "")) < minPrefix {
				continue
			}
			for b := a + 1; b < len(ns) && strings.HasPrefix(ns[b].name, ns[a].name+" "); b++ {
				g.Join(ns[b].i, ns[a].i, model.RulePrefix)
			}
		}
	}
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
