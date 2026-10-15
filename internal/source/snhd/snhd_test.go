package snhd

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
)

const estHeader = "permit_number;facility_id;owner_id;PE;restaurant_name;location_name;address;latitude;longitude;city_id;city_name;zip_code;nciaa;plan_review;record_status;current_grade;current_demerits;date_current;previous_grade;date_previous;search_text;"

// Real lines from the district's file: a normal row (coordinates swapped, as
// published), a name containing ';', and an unrepairable row.
var files = map[string]string{
	"restaurant_inspection_types.csv": "inspection_type_id;inspection_type;\n916;Routine Inspection;\n",
	"restaurant_establishments.csv": estHeader + "\n" +
		"PR0000002;FA0002677;;1006;McDonald's #3549 D HOTEL;McDonalds (Store #3549) @ D HOTEL;301 Fremont ST;-115.14308440;36.16939030;10;Las Vegas;89101-5600;Y;;02;A;9;2024-07-16 00:00:00;A;2023-02-21 00:00:00;McDonald's #3549 D HOTEL McDonalds (Store #3549) @ D HOTEL;\n" +
		"PR0114877;FA0077379;;1122;PANCHO DOG'S-DELETE;NONPAYMENT OF FEES 4/13/26 NBS;PANCHO DOG'S;3757 E Lake Mead BLVD;-115.09251300;36.19541790;10;Las Vegas;89115;Y;;02;;;;;;PANCHO DOG'S-DELETE;NONPAYMENT OF FEES 4/13/26 NBS PANCHO DOG'S;\n" +
		"PR0134961;FA0092178;;1009;ASTAS NUTRITION SPOT;ASTAS NUTRITION SPOT;4161 S EASTERN AVE #A-3;-115.11932000;36.11324000;-1;;AS VEGAS;89119;Y;;01;A;3;2025-02-26 00:00:00;A;2024-01-19 00:00:00;ASTAS NUTRITION SPOT ASTAS NUTRITION SPOT;\n",
	"restaurant_inspections.csv": "serial_number;permit_number;facility_id;owner_id;inspection_date;inspection_time;employee_id;inspection_type_id;inspection_demerits;inspection_grade;inspection_grade_new;permit_status;inspection_result;violations;record_updated;\n" +
		"100002;PR0000002;FA0002677;;2023-08-16 00:00:00;2023-08-16 00:00:00;ORTIZ-RIVERA;916;0;A;A;;\"A\" Grade;;2026-09-10 19:14:55;\n" +
		"100003;PR0114877;FA0077379;;2024-01-02 00:00:00;2024-01-02 00:00:00;X;916;3;A;A;;\"A\" Grade;;2026-09-10 19:14:55;\n" +
		"100004;PR9999999;FA9999999;;2024-01-03 00:00:00;2024-01-03 00:00:00;X;916;3;A;A;;\"A\" Grade;;2026-09-10 19:14:55;\n",
	"restaurant_inspections_stanford.csv": "serial_number;permit_number;facility_id;inspection_date;inspection_time;employee_id;inspection_type_id;inspection_demerits;inspection_grade;inspection_grade_new;permit_status;inspection_result;violations;record_updated;\n" +
		"DA0001121;PR0000002;FA0002677;2005-03-25 00:00:00;2005-03-25 10:20:00;EE7000535;916;4;A;A;;Compliant;113;2013-02-21 22:26:12;\n",
}

func zipServer(t *testing.T) *Source {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	return &Source{URL: srv.URL, HTTP: srv.Client()}
}

func fetchAll(t *testing.T) (*Source, map[string]source.Record) {
	t.Helper()
	s := zipServer(t)
	got := map[string]source.Record{}
	if err := s.Fetch(context.Background(), func(r source.Record) error { got[r.ID] = r; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, got
}

func TestFetchEmitsInspectionsAndErrors(t *testing.T) {
	_, got := fetchAll(t)
	for _, id := range []string{"100002", "100003", "100004", "DA0001121", "establishment:PR0134961"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing record %s", id)
		}
	}
	if len(got) != 5 {
		t.Errorf("got %d records, want 5", len(got))
	}
}

func TestParseJoinsEstablishmentAndRepairsCoordinates(t *testing.T) {
	s, got := fetchAll(t)
	obs, err := s.Parse(got["100002"])
	if err != nil {
		t.Fatal(err)
	}
	if obs.Kind != "inspection" || obs.Name != "McDonald's #3549 D HOTEL" || obs.Address != "301 Fremont ST" ||
		obs.City != "Las Vegas" || obs.Zip != "89101" || obs.BusinessKey != "snhd:PR0000002" ||
		obs.Category != "Routine Inspection" || obs.Status != `"A" Grade` {
		t.Errorf("obs = %+v", obs)
	}
	if obs.Lat == nil || *obs.Lat != 36.16939030 || *obs.Lon != -115.14308440 {
		t.Errorf("point = %v %v", obs.Lat, obs.Lon)
	}
	if obs.StartDate == nil || obs.StartDate.Format(time.DateOnly) != "2023-08-16" {
		t.Errorf("StartDate = %v", obs.StartDate)
	}
}

func TestParseRepairsSemicolonInName(t *testing.T) {
	s, got := fetchAll(t)
	obs, err := s.Parse(got["100003"])
	if err != nil {
		t.Fatal(err)
	}
	if obs.Name != "PANCHO DOG'S-DELETE;NONPAYMENT OF FEES 4/13/26 NBS" || obs.Address != "3757 E Lake Mead BLVD" {
		t.Errorf("obs = %+v", obs)
	}
}

func TestParseKeepsOrphanInspection(t *testing.T) {
	s, got := fetchAll(t)
	obs, err := s.Parse(got["100004"])
	if err != nil {
		t.Fatal(err)
	}
	if obs.Name != "" || obs.BusinessKey != "snhd:PR9999999" {
		t.Errorf("obs = %+v", obs)
	}
}

func TestParseRejectsBrokenEstablishment(t *testing.T) {
	s, got := fetchAll(t)
	if _, err := s.Parse(got["establishment:PR0134961"]); err == nil {
		t.Fatal("want error for unrepairable row")
	}
}

func TestVolatileEstablishmentColumnsAreNotCopied(t *testing.T) {
	_, got := fetchAll(t)
	f, _ := source.Decode(got["100002"])
	if _, ok := f["est_current_grade"]; ok {
		t.Error("est_current_grade copied; every grade change would re-version all past inspections")
	}
}
