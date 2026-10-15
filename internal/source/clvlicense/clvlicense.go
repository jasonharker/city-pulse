// Package clvlicense reads City of Las Vegas business licenses.
// The layer has no geometry and no issue date.
package clvlicense

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

const layerURL = "https://services1.arcgis.com/F1v0ufATbBQScMtY/arcgis/rest/services/Business_Licenses_OpenData/FeatureServer/0"

const ownerField = "Owner_and_Owner_Title"

type Source struct{ Layer arcgis.Layer }

func New(c *http.Client) *Source {
	return &Source{Layer: arcgis.Layer{URL: layerURL, OIDField: "ObjectId", PageSize: 1000, HTTP: c}}
}

func (*Source) Name() string { return "clvlicense" }

// license is one license number's rows folded together.
type license struct {
	fields map[string]any
	owners []string
}

// Fetch emits one record per license number. The city publishes one row per
// owner or officer (a three-member LLC is three rows), so rows are grouped
// and the owners collected into a sorted "owners" list. This buffers the
// layer in memory: ~150k licenses.
// ponytail: buffers ~200k rows; stream by license if the layer grows 10x.
func (s *Source) Fetch(ctx context.Context, emit func(source.Record) error) error {
	byID := make(map[string]*license)
	var order []string
	err := s.Layer.Each(ctx, func(f arcgis.Feature) error {
		fields := f.Fields(s.Layer.OIDField)
		owner, _ := fields[ownerField].(string)
		delete(fields, ownerField)
		id, _ := fields["License__"].(string)
		if id == "" {
			// No license number: emit as-is so the runner keeps it as a parse error.
			r, err := source.NewRecord("", fields)
			if err != nil {
				return err
			}
			return emit(r)
		}
		l, ok := byID[id]
		if !ok {
			l = &license{fields: fields, owners: []string{}}
			byID[id] = l
			order = append(order, id)
		}
		if owner = strings.TrimSpace(owner); owner != "" {
			l.owners = append(l.owners, owner)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range order {
		l := byID[id]
		slices.Sort(l.owners)
		l.fields["owners"] = slices.Compact(l.owners)
		r, err := source.NewRecord(id, l.fields)
		if err != nil {
			return err
		}
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

func (*Source) Parse(r source.Record) (source.Observation, error) {
	f, err := source.Decode(r)
	if err != nil {
		return source.Observation{}, err
	}
	return source.Observation{
		Kind:        "license",
		Name:        source.Clean(f.Str("Business_Name")),
		Address:     source.Clean(f.Str("Address")),
		Zip:         source.Zip5(f.Str("Zip_Code")),
		BusinessKey: "clv:" + r.ID,
		Category:    f.Str("Type_of_Business"),
		Status:      f.Str("Status"),
	}, nil
}
