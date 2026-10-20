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
	// Run 1 is the initial load; run 2 closes JOE'S BAR, a change.
	for _, closed := range []bool{false, true} {
		var obs []source.Observation
		for _, o := range []struct{ id, name, addr string }{
			{"1", "JOE'S BAR", "3570 S LAS VEGAS BLVD STE B/C"},
			{"2", "<b>Taco</b> Place", "3570 S LAS VEGAS BLVD STE B/C"},
			{"3", "VAPE CITY", "3570 S LAS VEGAS BLVD"},
		} {
			fields, status := map[string]any{"n": o.name}, ""
			if closed && o.id == "1" {
				fields["s"], status = "Closed", "Closed"
			}
			r, err := source.NewRecord(o.id, fields)
			if err != nil {
				t.Fatal(err)
			}
			obs = append(obs, source.Observation{Source: "snhd", RecordID: o.id, Hash: r.Hash(), Payload: r.Payload,
				Kind: "inspection", Name: o.name, Address: o.addr, BusinessKey: "snhd:" + o.id, Status: status, StartDate: &d})
		}
		run, err := st.BeginRun(ctx, "snhd")
		if err != nil {
			t.Fatal(err)
		}
		n, err := run.Add(ctx, obs)
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Finish(ctx, store.RunStats{Seen: 3, New: n}, nil); err != nil {
			t.Fatal(err)
		}
		cur, err := st.CurrentObservations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceResolved(ctx, resolve.Build(cur)); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(web.Handler(st, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)

	unit := "/place/" + url.PathEscape("3570|S|LAS VEGAS|B/C")
	// Bodies that must not contain a string, by path.
	deny := map[string][]string{
		"/changes.atom": {"New: JOE&#39;S BAR"}, // the initial load is not a change
		"/feed/business/" + url.PathEscape("3570|S|LAS VEGAS|B/C#snhd:2"): {"0001-01-01"},
	}
	for _, c := range []struct {
		path   string
		status int
		want   []string
	}{
		{"/", 200, []string{"What used to be here?", `<link rel="alternate" type="application/atom+xml" href="/changes.atom">`}},
		{"/?q=3570+las+vegas+blvd", 200, []string{`href="` + unit + `"`, "3570 S LAS VEGAS BLVD STE B/C"}},
		{"/?q=joes", 200, []string{"JOE&#39;S BAR"}},
		{unit, 200, []string{"JOE&#39;S BAR", "&lt;b&gt;Taco&lt;/b&gt; Place", "A unit of", "Health inspection (SNHD)", "May 1, 2019",
			"What was here, when", `style="left: 32.88%; width: 0.50%"`,
			`<link rel="alternate" type="application/atom+xml" href="/feed/place/3570%7CS%7CLAS%20VEGAS%7CB%2FC">`,
			`href="/record/snhd/1">2 versions</a>`}},
		{"/record/snhd/1", 200, []string{"JOE&#39;S BAR", "2 versions", "<td>s</td><td>Closed</td>", "As first published"}},
		{"/record/nope/1", 404, []string{"Not found"}},
		{"/record/snhd/nope", 404, []string{"Not found"}},
		{"/place/" + url.PathEscape("3570|S|LAS VEGAS|"), 200, []string{"VAPE CITY", "Units in this building", `href="` + unit + `"`}},
		{"/business/" + url.PathEscape("3570|S|LAS VEGAS|B/C#snhd:1"), 200, []string{"JOE&#39;S BAR", "2019–2019"}},
		{"/place/nowhere", 404, []string{"Not found"}},
		{"/changes", 200, []string{"What changed", "Updated", "JOE&#39;S BAR", `href="/record/snhd/1"`}},
		{"/changes.atom", 200, []string{`<feed xmlns="http://www.w3.org/2005/Atom">`, "Updated: JOE&#39;S BAR (Health inspection (SNHD))",
			"tag:citypulse.jasonharker.com,2026:updated/", "https://citypulse.jasonharker.com/record/snhd/1"}},
		{"/feed/place/" + url.PathEscape("3570|S|LAS VEGAS|B/C"), 200, []string{"<title>3570 S LAS VEGAS BLVD STE B/C · city-pulse</title>", "<entry>"}},
		{"/feed/business/" + url.PathEscape("3570|S|LAS VEGAS|B/C#snhd:2"), 200, []string{"<updated>"}},
		{"/feed/place/nowhere", 404, nil},
		{"/feed/business/nowhere", 404, nil},
		{"/sources", 200, []string{"Health inspection (SNHD)", "March 2017", "3 rows"}},
		// The fixture's addresses have no coordinates, so no pins; see TestPins.
		{"/map.json?z=12&s=36&w=-115.3&n=36.3&e=-115", 200, []string{"[]"}},
		{"/map.json?z=12&s=36&w=-115.3&n=NaN&e=-115", 400, nil},
		{"/map.json?z=99&s=36&w=-115.3&n=36.3&e=-115", 400, nil},
		{"/static/map.js", 200, []string{"/map.json?"}},
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
		for _, d := range deny[c.path] {
			if strings.Contains(string(body), d) {
				t.Errorf("GET %s: body has %q", c.path, d)
			}
		}
		for _, w := range c.want {
			if !strings.Contains(string(body), w) {
				t.Errorf("GET %s: body lacks %q", c.path, w)
			}
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("GET %s: CSP %q", c.path, csp)
		}
		if strings.Contains(string(body), "ZgotmplZ") {
			t.Errorf("GET %s: html/template rejected a value", c.path)
		}
	}
}
