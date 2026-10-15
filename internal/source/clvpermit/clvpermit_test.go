package clvpermit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

// Real rows from each layer, as the arcgis client hands them over.
const currentRow = `{"APNO":"R23-17258","APTYPE":"Res","WORKTYPE":"OTC","BLDGAPPLSTATUS":"Closed",
"ISSDTTM":1698105600000,"STNO":2440,"PREDIR":null,"STNAME":"SAGE POINTE","SUFFIX":"CIR","POSTDIR":null,
"STSUB":null,"PRCLID":13816415069,"ZIP":"89128","_layer":"current"}`

const archiveRow = `{"Status":"M","APNO":"R26-00072","APNAME":"KALANI WILBURN","APTYPE":"Res",
"APDESC":"Residential Building Permit","PROPERTY":"2808 SING SONG WAY  ","PRCLID":"13920415030",
"STAT":"Closed","ISSUE_DT":"05-JAN-26","COMP_D":null,"SUITE":null,"ADDR":"2808 SING SONG WAY",
"_lat":36.19132518828868,"_lon":-115.1798476050579,"_layer":"archive"}`

func TestParseCurrent(t *testing.T) {
	obs, err := (&Source{}).Parse(source.Record{ID: "R23-17258", Payload: json.RawMessage(currentRow)})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Kind != "permit" || obs.Address != "2440 SAGE POINTE CIR" || obs.Parcel != "13816415069" ||
		obs.Zip != "89128" || obs.Category != "Res OTC" || obs.Status != "Closed" || obs.Name != "" {
		t.Errorf("obs = %+v", obs)
	}
	if obs.StartDate == nil || obs.StartDate.Format(time.DateOnly) != "2023-10-24" {
		t.Errorf("StartDate = %v", obs.StartDate)
	}
}

func TestParseArchive(t *testing.T) {
	obs, err := (&Source{}).Parse(source.Record{ID: "R26-00072", Payload: json.RawMessage(archiveRow)})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Address != "2808 SING SONG WAY" || obs.Parcel != "13920415030" ||
		obs.Category != "Residential Building Permit" || obs.EndDate != nil {
		t.Errorf("obs = %+v", obs)
	}
	if obs.StartDate == nil || obs.StartDate.Format(time.DateOnly) != "2026-01-05" {
		t.Errorf("StartDate = %v", obs.StartDate)
	}
	if obs.Lat == nil || *obs.Lat != 36.19132518828868 {
		t.Errorf("Lat = %v", obs.Lat)
	}
}

func TestParseRejectsUnknownLayer(t *testing.T) {
	if _, err := (&Source{}).Parse(source.Record{ID: "x", Payload: json.RawMessage(`{"_layer":"?"}`)}); err == nil {
		t.Fatal("want error")
	}
}

// serve answers the first query with feats and every later one with no features.
func serve(t *testing.T, feats ...map[string]any) arcgis.Layer {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		out := []map[string]any{}
		if calls == 1 {
			for _, a := range feats {
				out = append(out, map[string]any{"attributes": a})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"features": out})
	}))
	t.Cleanup(srv.Close)
	return arcgis.Layer{URL: srv.URL, OIDField: "OBJECTID", PageSize: 10, HTTP: srv.Client()}
}

func TestFetchPrefersCurrentOverArchive(t *testing.T) {
	s := &Source{
		Current: serve(t, map[string]any{"OBJECTID": 1, "APNO": "A-1"}),
		Archive: serve(t, map[string]any{"OBJECTID": 1, "APNO": "A-1"}, map[string]any{"OBJECTID": 2, "APNO": "A-2"}),
	}
	var got []string
	err := s.Fetch(context.Background(), func(r source.Record) error {
		f, _ := source.Decode(r)
		got = append(got, r.ID+"/"+f.Str("_layer"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "A-1/current,A-2/archive" {
		t.Errorf("got %v", got)
	}
}
