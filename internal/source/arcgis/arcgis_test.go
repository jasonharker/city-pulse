package arcgis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLayer serves features with OBJECTID 1..n in pages of size, honouring
// the "OBJECTID > k" keyset clause, and records every where clause it saw.
func fakeLayer(t *testing.T, n, size int) (*httptest.Server, *[]string) {
	t.Helper()
	var wheres []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		where := r.URL.Query().Get("where")
		wheres = append(wheres, where)
		if r.URL.Query().Get("outSR") != "4326" {
			t.Errorf("outSR = %q", r.URL.Query().Get("outSR"))
		}
		var after int
		i := strings.LastIndex(where, "> ")
		json.Unmarshal([]byte(where[i+2:]), &after)
		var feats []map[string]any
		for id := max(after+1, 1); id <= n && len(feats) < size; id++ {
			feats = append(feats, map[string]any{
				"attributes": map[string]any{"OBJECTID": id, "NAME": "row"},
				"geometry":   map[string]any{"x": -115.1, "y": 36.1},
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"features": feats})
	}))
	t.Cleanup(srv.Close)
	return srv, &wheres
}

func TestEachPagesByKeyset(t *testing.T) {
	srv, wheres := fakeLayer(t, 5, 2)
	l := Layer{URL: srv.URL, OIDField: "OBJECTID", PageSize: 2, Where: "APNO <> ''", HTTP: srv.Client()}
	var got []string
	err := l.Each(context.Background(), func(f Feature) error {
		got = append(got, f.Attributes["OBJECTID"].(json.Number).String())
		if f.Lat == nil || *f.Lat != 36.1 || *f.Lon != -115.1 {
			t.Errorf("geometry = %v %v", f.Lat, f.Lon)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "1,2,3,4,5" {
		t.Errorf("ids = %v", got)
	}
	want := []string{
		"(APNO <> '') AND OBJECTID > -1",
		"(APNO <> '') AND OBJECTID > 2",
		"(APNO <> '') AND OBJECTID > 4",
		"(APNO <> '') AND OBJECTID > 5",
	}
	if strings.Join(*wheres, "|") != strings.Join(want, "|") {
		t.Errorf("wheres = %q", *wheres)
	}
}

func TestEachReturnsServiceError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":{"code":400,"message":"Invalid URL"}}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // skip the retry back-off: the first attempt runs, the wait sees ctx done
	l := Layer{URL: srv.URL, OIDField: "OBJECTID", PageSize: 2, HTTP: srv.Client()}
	err := l.Each(ctx, func(Feature) error { return nil })
	if err == nil {
		t.Fatal("want error")
	}
}

func TestFieldsDropsOIDAndAddsPoint(t *testing.T) {
	lat, lon := 36.1, -115.1
	f := Feature{Attributes: map[string]any{"OBJECTID": json.Number("7"), "NAME": "x"}, Lat: &lat, Lon: &lon}
	got := f.Fields("OBJECTID")
	if _, ok := got["OBJECTID"]; ok {
		t.Error("OBJECTID kept")
	}
	if got["_lat"] != 36.1 || got["_lon"] != -115.1 || got["NAME"] != "x" {
		t.Errorf("fields = %v", got)
	}
}
