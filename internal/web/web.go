// Package web serves the public, read-only site: search, place pages,
// business pages, and /metrics for Prometheus.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

//go:embed page.html
var pageFS embed.FS

// sourceNames label a source and record kind for readers.
var sourceNames = map[string]string{
	"snhd":       "Health inspection (SNHD)",
	"ccbiz":      "Clark County business license",
	"clvlicense": "City of Las Vegas business license",
	"clvpermit":  "City of Las Vegas building permit",
}

var page = template.Must(template.New("page.html").Funcs(template.FuncMap{
	"placeURL":    func(id string) string { return "/place/" + url.PathEscape(id) },
	"businessURL": func(id string) string { return "/business/" + url.PathEscape(id) },
	"source":      func(s string) string { return cmpOr(sourceNames[s], s) },
	"day":         func(t time.Time) string { return t.Format("Jan 2, 2006") },
	"year":        func(t time.Time) int { return t.Year() },
	"deref":       func(f *float64) float64 { return *f },
	"mapURL": func(lat, lon float64) string {
		const d = 0.002
		return fmt.Sprintf("https://www.openstreetmap.org/export/embed.html?bbox=%f,%f,%f,%f&layer=mapnik&marker=%f,%f",
			lon-d, lat-d, lon+d, lat+d, lat, lon)
	},
}).ParseFS(pageFS, "page.html"))

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Handler returns the site's routes.
func Handler(st *store.Store, log *slog.Logger) http.Handler {
	s := &server{st: st, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.search)
	mux.HandleFunc("GET /place/{id...}", s.place)
	mux.HandleFunc("GET /business/{id...}", s.business)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /healthz", s.healthz)
	return headers(mux)
}

type server struct {
	st  *store.Store
	log *slog.Logger
}

// view is the data every page renders with; one of the parts is set.
type view struct {
	Title    string
	Query    string
	Hits     []store.Hit
	Searched bool
	Place    *store.PlaceView
	Business *store.BusinessView
	Status   int
}

func (s *server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(strings.ToValidUTF8(r.URL.Query().Get("q"), ""))
	if rs := []rune(q); len(rs) > 100 {
		q = string(rs[:100])
	}
	v := view{Title: "Las Vegas place history", Query: q}
	if len(q) >= 2 {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		hits, err := s.st.Search(ctx, q)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		v.Hits, v.Searched, v.Title = hits, true, q+" · city-pulse"
	}
	s.render(w, r, v)
}

func (s *server) place(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := s.st.PlaceView(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, view{Title: p.Address + " · city-pulse", Place: p})
}

func (s *server) business(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	b, err := s.st.BusinessView(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, view{Title: b.Name + " · city-pulse", Business: b})
}

func (s *server) render(w http.ResponseWriter, r *http.Request, v view) {
	if v.Status == 0 {
		v.Status = http.StatusOK
		// The data changes once a night.
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(v.Status)
	if err := page.Execute(w, v); err != nil {
		s.log.Error("render", "path", r.URL.Path, "err", err)
	}
}

func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, view{Title: "Not found · city-pulse", Status: http.StatusNotFound})
		return
	}
	s.log.Error("request", "path", r.URL.Path, "err", err)
	s.render(w, r, view{Title: "Error · city-pulse", Status: http.StatusInternalServerError})
}

// metrics exposes the latest run of every source in the Prometheus text format.
// A CronJob can't be scraped, so serve reports for it.
func (s *server) metrics(w http.ResponseWriter, r *http.Request) {
	hs, err := s.st.Health(r.Context())
	if err != nil {
		s.log.Error("metrics", "err", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	gauge := func(name, help string, val func(store.SourceHealth) (float64, bool)) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		for _, h := range hs {
			if v, ok := val(h); ok {
				fmt.Fprintf(w, "%s{source=%q} %g\n", name, h.Source, v)
			}
		}
	}
	gauge("citypulse_source_last_success_timestamp_seconds", "End of the source's last successful ingest run.",
		func(h store.SourceHealth) (float64, bool) {
			if h.LastSuccess == nil {
				return 0, false
			}
			return float64(h.LastSuccess.Unix()), true
		})
	gauge("citypulse_source_last_run_ok", "1 if the source's latest ingest run succeeded.",
		func(h store.SourceHealth) (float64, bool) { return b2f(h.OK), true })
	gauge("citypulse_source_rows_seen", "Rows the latest run fetched.",
		func(h store.SourceHealth) (float64, bool) { return float64(h.Seen), true })
	gauge("citypulse_source_rows_new", "New record versions the latest run stored.",
		func(h store.SourceHealth) (float64, bool) { return float64(h.New), true })
	gauge("citypulse_source_parse_errors", "Rows the latest run could not parse.",
		func(h store.SourceHealth) (float64, bool) { return float64(h.ParseErrors), true })
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.st.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

// headers sets the security headers every response carries. The only
// third-party content is the OpenStreetMap embed on place pages.
func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; frame-src https://www.openstreetmap.org; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.ServeHTTP(w, r)
	})
}
