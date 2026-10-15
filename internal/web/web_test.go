package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/resolve"
	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store"
	"github.com/jasonharker/city-pulse/internal/store/storetest"
	"github.com/jasonharker/city-pulse/internal/web"
)

func TestSite(t *testing.T) {
	st, _ := storetest.New(t)
	ctx := context.Background()
	d := time.Date(2019, 5, 1, 0, 0, 0, 0, time.UTC)
	var obs []source.Observation
	for _, o := range []struct{ id, name, addr string }{
		{"1", "JOE'S BAR", "3570 S LAS VEGAS BLVD STE B/C"},
		{"2", "<b>Taco</b> Place", "3570 S LAS VEGAS BLVD STE B/C"},
		{"3", "VAPE CITY", "3570 S LAS VEGAS BLVD"},
	} {
		r, err := source.NewRecord(o.id, map[string]any{"n": o.name})
		if err != nil {
			t.Fatal(err)
		}
		obs = append(obs, source.Observation{Source: "snhd", RecordID: o.id, Hash: r.Hash(), Payload: r.Payload,
			Kind: "inspection", Name: o.name, Address: o.addr, BusinessKey: "snhd:" + o.id, StartDate: &d})
	}
	run, err := st.BeginRun(ctx, "snhd")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Add(ctx, obs); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(ctx, store.RunStats{Seen: 3, New: 3}, nil); err != nil {
		t.Fatal(err)
	}
	cur, err := st.CurrentObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceResolved(ctx, resolve.Build(cur)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(web.Handler(st, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)

	unit := "/place/" + url.PathEscape("3570|S|LAS VEGAS|B/C")
	for _, c := range []struct {
		path   string
		status int
		want   []string
	}{
		{"/", 200, []string{"What used to be here?"}},
		{"/?q=3570+las+vegas+blvd", 200, []string{`href="` + unit + `"`, "3570 S LAS VEGAS BLVD STE B/C"}},
		{"/?q=joes", 200, []string{"JOE&#39;S BAR"}},
		{unit, 200, []string{"JOE&#39;S BAR", "&lt;b&gt;Taco&lt;/b&gt; Place", "A unit of", "Health inspection (SNHD)", "May 1, 2019"}},
		{"/place/" + url.PathEscape("3570|S|LAS VEGAS|"), 200, []string{"VAPE CITY", "Units in this building", `href="` + unit + `"`}},
		{"/business/" + url.PathEscape("3570|S|LAS VEGAS|B/C#snhd:1"), 200, []string{"JOE&#39;S BAR", "2019–2019"}},
		{"/place/nowhere", 404, []string{"Not found"}},
		{"/metrics", 200, []string{`citypulse_source_rows_seen{source="snhd"} 3`, `citypulse_source_last_run_ok{source="snhd"} 1`}},
	} {
		res, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != c.status {
			t.Errorf("GET %s: status %d, want %d", c.path, res.StatusCode, c.status)
		}
		for _, w := range c.want {
			if !strings.Contains(string(body), w) {
				t.Errorf("GET %s: body lacks %q", c.path, w)
			}
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("GET %s: CSP %q", c.path, csp)
		}
	}
}
