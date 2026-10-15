package clvlicense

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

const row = `{"Business_Name":"ADCELO","Type_of_Business":"General Retail Sales","Address":"4637 Michillinda Ln",
"Zip_Code":"89121","Phone":"(702)426-0533","License__":"G69-06397","Status":"Closed","CLV":null,"NLV":null,
"HEN":null,"CC":null,"owners":["ADCELLO LLC - LLC"],"Within_City_Limits":"Outside"}`

func TestParse(t *testing.T) {
	obs, err := (&Source{}).Parse(source.Record{ID: "G69-06397", Payload: json.RawMessage(row)})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Kind != "license" || obs.Name != "ADCELO" || obs.Address != "4637 Michillinda Ln" ||
		obs.Zip != "89121" || obs.BusinessKey != "clv:G69-06397" || obs.Category != "General Retail Sales" ||
		obs.Status != "Closed" {
		t.Errorf("obs = %+v", obs)
	}
	if obs.Lat != nil || obs.StartDate != nil {
		t.Errorf("layer has no point or date, got %v %v", obs.Lat, obs.StartDate)
	}
}

// Real shape: one row per owner, plus a row with no license number.
func TestFetchFoldsOwnerRows(t *testing.T) {
	rows := []map[string]any{
		{"ObjectId": 1, "License__": "3000000025", "Business_Name": "CADIZ CONSTRUCTION LLC", "Owner_and_Owner_Title": "SHIELDS, SEAN - Member"},
		{"ObjectId": 2, "License__": "3000000025", "Business_Name": "CADIZ CONSTRUCTION LLC", "Owner_and_Owner_Title": "FRANCIS, LORRAINE - Member"},
		{"ObjectId": 3, "License__": "3000000025", "Business_Name": "CADIZ CONSTRUCTION LLC", "Owner_and_Owner_Title": "SHIELDS, SEAN - Member"},
		{"ObjectId": 4, "License__": "G69-06397", "Business_Name": "ADCELO", "Owner_and_Owner_Title": nil},
		{"ObjectId": 5, "License__": nil, "Business_Name": "ACORN STAIRLIFTS"},
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		feats := []map[string]any{}
		if calls == 1 {
			for _, a := range rows {
				feats = append(feats, map[string]any{"attributes": a})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"features": feats})
	}))
	defer srv.Close()
	s := &Source{Layer: arcgis.Layer{URL: srv.URL, OIDField: "ObjectId", PageSize: 10, HTTP: srv.Client()}}

	got := map[string]string{}
	err := s.Fetch(context.Background(), func(r source.Record) error {
		f, _ := source.Decode(r)
		owners, _ := json.Marshal(f["owners"])
		got[r.ID] = string(owners)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"3000000025": `["FRANCIS, LORRAINE - Member","SHIELDS, SEAN - Member"]`,
		"G69-06397":  `[]`,
		"":           `null`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%q owners = %s, want %s", id, got[id], w)
		}
	}
}
