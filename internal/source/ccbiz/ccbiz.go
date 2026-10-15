// Package ccbiz reads Clark County business licenses (unincorporated county, incl. the Strip).
package ccbiz

import (
	"context"
	"net/http"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

const layerURL = "https://maps.clarkcountynv.gov/arcgis/rest/services/BusinessLicense/BusinessLicenseData/FeatureServer/1"

type Source struct{ Layer arcgis.Layer }

func New(c *http.Client) *Source {
	return &Source{Layer: arcgis.Layer{URL: layerURL, OIDField: "OBJECTID", PageSize: 5000, HTTP: c}}
}

func (*Source) Name() string { return "ccbiz" }

func (s *Source) Fetch(ctx context.Context, emit func(source.Record) error) error {
	return s.Layer.Each(ctx, func(f arcgis.Feature) error {
		fields := f.Fields(s.Layer.OIDField)
		id, _ := fields["USER_FullCredentialNumber"].(string)
		r, err := source.NewRecord(id, fields)
		if err != nil {
			return err
		}
		return emit(r)
	})
}

func (*Source) Parse(r source.Record) (source.Observation, error) {
	f, err := source.Decode(r)
	if err != nil {
		return source.Observation{}, err
	}
	lat, lon := source.LatLon(f.Float("_lat"), f.Float("_lon"))
	return source.Observation{
		Kind:    "license",
		Name:    source.Clean(f.Str("USER_LocationName")),
		Address: source.Clean(source.Join(f.Str("USER_AddressLine1"), f.Str("USER_unitLine"))),
		City:    f.Str("USER_City"),
		Zip:     source.Zip5(f.Str("USER_ZipCode")),
		Parcel:  source.Digits(f.Str("USER_ParcelNumber")),
		// "1001277.LIQ-107": the part before "." is shared by all of one business's licenses.
		BusinessKey: "cc:" + source.Before(r.ID, "."),
		Category:    f.Str("USER_CredentialDescription"),
		Status:      f.Str("USER_StatusDescription"),
		Lat:         lat,
		Lon:         lon,
		StartDate:   source.Date("20060102", f.Str("USER_InitialLicenseIssuedDate")),
		EndDate:     source.Date("20060102", f.Str("USER_OutOfBusinessDateActual")),
	}, nil
}
