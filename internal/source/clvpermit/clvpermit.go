// Package clvpermit reads City of Las Vegas building permits. The city publishes
// them as two layers: a small current layer and a large archive that stops in
// January 2026. Both use the same permit number (APNO); the current row wins.
package clvpermit

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

const (
	org        = "https://services1.arcgis.com/F1v0ufATbBQScMtY/arcgis/rest/services"
	currentURL = org + "/OpenData_Building_Permits_/FeatureServer/0"
	archiveURL = org + "/Bldg_Permits/FeatureServer/379"
)

type Source struct{ Current, Archive arcgis.Layer }

func New(c *http.Client) *Source {
	return &Source{
		Current: arcgis.Layer{URL: currentURL, OIDField: "ObjectId", PageSize: 1000, HTTP: c},
		// ~72k archive rows are blank placeholders with no permit number.
		Archive: arcgis.Layer{URL: archiveURL, OIDField: "OBJECTID", PageSize: 2000, Where: "APNO <> ''", HTTP: c},
	}
}

func (*Source) Name() string { return "clvpermit" }

func (s *Source) Fetch(ctx context.Context, emit func(source.Record) error) error {
	seen := make(map[string]bool)
	err := s.Current.Each(ctx, func(f arcgis.Feature) error {
		fields := f.Fields(s.Current.OIDField)
		id, _ := fields["APNO"].(string)
		seen[id] = true
		return emitLayer(emit, id, "current", fields)
	})
	if err != nil {
		return fmt.Errorf("current layer: %w", err)
	}
	err = s.Archive.Each(ctx, func(f arcgis.Feature) error {
		fields := f.Fields(s.Archive.OIDField)
		id, _ := fields["APNO"].(string)
		if seen[id] {
			return nil
		}
		return emitLayer(emit, id, "archive", fields)
	})
	if err != nil {
		return fmt.Errorf("archive layer: %w", err)
	}
	return nil
}

func emitLayer(emit func(source.Record) error, id, layer string, fields map[string]any) error {
	fields["_layer"] = layer
	r, err := source.NewRecord(id, fields)
	if err != nil {
		return err
	}
	return emit(r)
}

func (*Source) Parse(r source.Record) (source.Observation, error) {
	f, err := source.Decode(r)
	if err != nil {
		return source.Observation{}, err
	}
	obs := source.Observation{Kind: "permit", City: "Las Vegas", Parcel: source.Digits(f.Str("PRCLID"))}
	switch layer := f.Str("_layer"); layer {
	case "current":
		obs.Address = source.Clean(source.Join(f.Str("STNO"), f.Str("PREDIR"), f.Str("STNAME"),
			f.Str("SUFFIX"), f.Str("POSTDIR"), f.Str("STSUB")))
		obs.Zip = source.Zip5(f.Str("ZIP"))
		obs.Category = source.Join(f.Str("APTYPE"), f.Str("WORKTYPE"))
		obs.Status = f.Str("BLDGAPPLSTATUS")
		obs.StartDate = epochMillis(f.Str("ISSDTTM"))
	case "archive":
		obs.Address = source.Clean(source.Join(f.Str("ADDR"), f.Str("SUITE")))
		obs.Category = f.Str("APDESC")
		obs.Status = f.Str("STAT")
		obs.StartDate = source.Date("02-Jan-06", f.Str("ISSUE_DT"))
		obs.EndDate = source.Date("02-Jan-06", f.Str("COMP_D"))
		obs.Lat, obs.Lon = source.LatLon(f.Float("_lat"), f.Float("_lon"))
	default:
		return source.Observation{}, fmt.Errorf("unknown _layer %q", layer)
	}
	return obs, nil
}

// epochMillis converts an ArcGIS date field (milliseconds since 1970, UTC) to a time.
func epochMillis(s string) *time.Time {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms == 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}
