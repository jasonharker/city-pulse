// Package arcgis reads every feature of an ArcGIS FeatureServer layer.
package arcgis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
)

// Layer is one FeatureServer layer, e.g. ".../FeatureServer/0".
type Layer struct {
	URL      string
	OIDField string // object-id field: "OBJECTID" or "ObjectId" depending on the service
	PageSize int    // a hint; the server may return fewer
	Where    string // optional SQL filter; "" means every row
	HTTP     *http.Client
}

// Feature is one row: attributes plus a WGS84 point if the layer has geometry.
type Feature struct {
	Attributes map[string]any // numbers are json.Number
	Lat, Lon   *float64
}

// Fields returns the attributes minus the object id, which a republish can
// renumber, plus the point as "_lat"/"_lon". This is what sources hash.
func (f Feature) Fields(oidField string) map[string]any {
	out := make(map[string]any, len(f.Attributes)+2)
	for k, v := range f.Attributes {
		if k != oidField {
			out[k] = v
		}
	}
	if f.Lat != nil {
		out["_lat"], out["_lon"] = *f.Lat, *f.Lon
	}
	return out
}

// Each calls fn for every feature in object-id order. It pages by keyset
// (OID > last seen) rather than offset, so deep pages stay cheap.
func (l Layer) Each(ctx context.Context, fn func(Feature) error) error {
	last := int64(-1)
	for {
		page, err := l.page(ctx, last)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, f := range page {
			if err := fn(f); err != nil {
				return err
			}
		}
		n, ok := page[len(page)-1].Attributes[l.OIDField].(json.Number)
		if !ok {
			return fmt.Errorf("arcgis %s: feature has no %s", l.URL, l.OIDField)
		}
		if last, err = n.Int64(); err != nil {
			return fmt.Errorf("arcgis %s: bad %s %q", l.URL, l.OIDField, n)
		}
	}
}

type response struct {
	Features []struct {
		Attributes map[string]any `json:"attributes"`
		Geometry   *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"geometry"`
	} `json:"features"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (l Layer) page(ctx context.Context, after int64) ([]Feature, error) {
	where := fmt.Sprintf("%s > %d", l.OIDField, after)
	if l.Where != "" {
		where = "(" + l.Where + ") AND " + where
	}
	q := url.Values{
		"where":             {where},
		"outFields":         {"*"},
		"orderByFields":     {l.OIDField},
		"resultRecordCount": {strconv.Itoa(l.PageSize)},
		"returnGeometry":    {"true"},
		"outSR":             {"4326"},
		"f":                 {"json"},
	}
	var resp response
	if err := l.get(ctx, l.URL+"/query?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	out := make([]Feature, len(resp.Features))
	for i, f := range resp.Features {
		out[i].Attributes = f.Attributes
		if f.Geometry != nil {
			lat, lon := f.Geometry.Y, f.Geometry.X
			out[i].Lat, out[i].Lon = &lat, &lon
		}
	}
	return out, nil
}

// get fetches and decodes one page, retrying transient failures.
// ArcGIS reports many errors as HTTP 200 with an "error" body, so those retry too.
func (l Layer) get(ctx context.Context, u string, into *response) error {
	var err error
	for attempt, wait := 0, time.Second; attempt < 3; attempt, wait = attempt+1, wait*2 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		if err = l.getOnce(ctx, u, into); err == nil {
			return nil
		}
	}
	return err
}

func (l Layer) getOnce(ctx context.Context, u string, into *response) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", source.UserAgent)
	res, err := l.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, res.Body)
		return fmt.Errorf("arcgis %s: HTTP %d", l.URL, res.StatusCode)
	}
	*into = response{}
	d := json.NewDecoder(res.Body)
	d.UseNumber()
	if err := d.Decode(into); err != nil {
		return fmt.Errorf("arcgis %s: decode: %w", l.URL, err)
	}
	if into.Error != nil {
		return fmt.Errorf("arcgis %s: error %d: %s", l.URL, into.Error.Code, into.Error.Message)
	}
	return nil
}
