// Package resolve derives places and businesses from observations. It is pure:
// the same observations always produce the same result, with stable IDs.
// A resolve is a pipeline of stages over one graph; see Default.
package resolve

import (
	"time"

	"github.com/jasonharker/city-pulse/internal/model"
	"github.com/jasonharker/city-pulse/internal/resolve/business"
	"github.com/jasonharker/city-pulse/internal/resolve/cluster"
	"github.com/jasonharker/city-pulse/internal/resolve/graph"
	"github.com/jasonharker/city-pulse/internal/resolve/place"
)

// Result is a full resolve.
type Result struct {
	Places     []model.Place
	Businesses []model.Business
	Links      []model.Link
	Stages     []StageRun // timings only; not part of what resolve derives
}

// StageRun is how long one stage took.
type StageRun struct {
	Name string
	Took time.Duration
}

// Default is the production pipeline. Order matters: places are collected
// from exact and dir placements before near searches them, and every edge
// exists before clustering.
func Default() []graph.Stage {
	return []graph.Stage{
		place.Exact{}, place.Dir{}, place.Collect{}, place.Near{},
		business.Key{}, business.Name{}, business.Prefix{},
		cluster.Businesses{},
	}
}

// Build resolves obs with the default pipeline.
func Build(obs []model.Obs) Result { return Run(obs, Default()) }

// Run resolves obs with stages.
func Run(obs []model.Obs, stages []graph.Stage) Result {
	g := graph.New(obs)
	var runs []StageRun
	for _, s := range stages {
		start := time.Now()
		s.Apply(g)
		runs = append(runs, StageRun{Name: s.Name(), Took: time.Since(start)})
	}
	links := make([]model.Link, len(obs))
	for i, o := range obs {
		links[i] = model.Link{ObservationID: o.ID, PlaceID: g.PlaceOf[i], PlaceRule: g.PlaceRule[i],
			BusinessID: g.BizOf[i], BusinessRule: g.BizRule[i]}
	}
	return Result{Places: g.Places, Businesses: g.Businesses, Links: links, Stages: runs}
}
