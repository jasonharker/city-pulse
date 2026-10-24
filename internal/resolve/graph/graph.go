// Package graph is the state a resolve run builds up: observations, the place
// each one landed at and why, and the evidence that two observations are one
// business. Stages read and extend it in order; only clustering merges.
package graph

import (
	"github.com/jasonharker/city-pulse/internal/model"
)

// Stage is one step of a resolve run.
type Stage interface {
	Name() string
	Apply(*Graph)
}

// Edge is evidence that observations A and B are one business. Rule names the
// stage's reason; "" means a shared source key, the default reason. The order
// edges are added in is the order clustering applies them.
type Edge struct {
	A, B int // indexes into Graph.Obs
	Rule string
}

// Graph is one resolve run in progress. Slices are indexed like Obs.
type Graph struct {
	Obs []model.Obs

	PlaceOf   []string // place ID, or "" if none yet
	PlaceRule []string
	Places    []model.Place

	Edges []Edge

	BizOf      []string // business ID, or "" for unnamed records
	BizRule    []string
	Businesses []model.Business
}

// New starts a run over obs.
func New(obs []model.Obs) *Graph {
	n := len(obs)
	return &Graph{Obs: obs,
		PlaceOf: make([]string, n), PlaceRule: make([]string, n),
		BizOf: make([]string, n), BizRule: make([]string, n)}
}

// Join adds an edge.
func (g *Graph) Join(a, b int, rule string) { g.Edges = append(g.Edges, Edge{A: a, B: b, Rule: rule}) }

// MostCommon returns the most frequent string, ties broken alphabetically.
func MostCommon(m map[string]int) string {
	best, bestN := "", 0
	for s, k := range m {
		if k > bestN || (k == bestN && s < best) {
			best, bestN = s, k
		}
	}
	return best
}
