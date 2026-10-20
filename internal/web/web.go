// Package web serves the public, read-only site: search, place pages,
// business pages, and /metrics for Prometheus.
package web

import (
	"cmp"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

//go:embed page.html static
var files embed.FS

// static serves the vendored assets; pages link them with a content hash so
// they can be cached for good.
var static, staticVersion = func() (fs.FS, map[string]string) {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	versions := map[string]string{}
	err = fs.WalkDir(sub, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, name)
		sum := sha256.Sum256(b)
		versions[name] = hex.EncodeToString(sum[:5])
		return err
	})
	if err != nil {
		panic(err)
	}
	return sub, versions
}()

// sourceNames label a source and record kind for readers.
var sourceNames = map[string]string{
	"snhd":       "Health inspection (SNHD)",
	"ccbiz":      "Clark County business license",
	"clvlicense": "City of Las Vegas business license",
	"clvpermit":  "City of Las Vegas building permit",
}

func placeURL(id string) string    { return "/place/" + url.PathEscape(id) }
func businessURL(id string) string { return "/business/" + url.PathEscape(id) }
func recordURL(src, id string) string {
	return "/record/" + url.PathEscape(src) + "/" + url.PathEscape(id)
}
func source(s string) string { return cmpOr(sourceNames[s], s) }

var page = template.Must(template.New("page.html").Funcs(template.FuncMap{
	"placeURL":        placeURL,
	"businessURL":     businessURL,
	"recordURL":       recordURL,
	"placeFeedURL":    placeFeedURL,
	"businessFeedURL": businessFeedURL,
	"what":            func(w string) string { return whatLabels[w] },
	"source":          source,
	"succession":      newSuccession,
	"day":             func(t time.Time) string { return t.Format("Jan 2, 2006") },
	"year":            func(t time.Time) int { return t.Year() },
	"deref":           func(f *float64) float64 { return *f },
	"static": func(name string) (string, error) {
		v, ok := staticVersion[name]
		if !ok {
			return "", fmt.Errorf("no static file %q", name)
		}
		return "/static/" + name + "?v=" + v, nil
	},
}).ParseFS(files, "page.html"))

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
	mux.HandleFunc("GET /record/{source}/{id...}", s.record)
	mux.HandleFunc("GET /changes", s.changes)
	mux.HandleFunc("GET /changes.atom", s.changesFeed)
	mux.HandleFunc("GET /feed/place/{id...}", s.placeFeed)
	mux.HandleFunc("GET /feed/business/{id...}", s.businessFeed)
	mux.HandleFunc("GET /map.json", s.pins)
	mux.Handle("GET /static/", http.StripPrefix("/static/", forever(http.FileServerFS(static))))
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
	Title     string
	Query     string
	Hits      []store.Hit
	Searched  bool
	Place     *store.PlaceView
	Business  *store.BusinessView
	Record    *store.RecordView
	Page      string // "changes" or "sources"
	Nights    []night
	Truncated bool   // Nights hold only the newest maxPageChanges changes
	Feed      string // the page's Atom feed, for autodiscovery
	Status    int
	Updated   time.Time // the newest successful ingest, for the front page
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
	} else {
		// The dateline is a nicety; the front page renders without it.
		v.Updated = s.lastIngest(r.Context())
	}
	s.render(w, r, v)
}

// lastIngest is when the newest successful ingest finished; zero if unknown.
func (s *server) lastIngest(ctx context.Context) time.Time {
	var t time.Time
	hs, err := s.st.Health(ctx)
	if err != nil {
		return t
	}
	for _, h := range hs {
		if h.LastSuccess != nil && h.LastSuccess.After(t) {
			t = *h.LastSuccess
		}
	}
	return t
}

func (s *server) record(w http.ResponseWriter, r *http.Request) {
	src := r.PathValue("source")
	if _, ok := sourceNames[src]; !ok {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rv, err := s.st.RecordView(ctx, src, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	l := rv.Latest()
	s.render(w, r, view{Title: cmp.Or(l.Name, l.Address, rv.RecordID) + " · " + source(src) + " · city-pulse", Record: rv})
}

// Pins are clustered into cells about clusterPx pixels wide below rawZoom,
// where one view holds up to ~17,000 buildings; at rawZoom a dense one holds ~1,500.
const (
	rawZoom   = 17
	clusterPx = 64
)

func (s *server) pins(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var b [4]float64 // south, west, north, east
	for i, k := range []string{"s", "w", "n", "e"} {
		f, err := strconv.ParseFloat(q.Get(k), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			http.Error(w, "s, w, n and e must be numbers", http.StatusBadRequest)
			return
		}
		b[i] = f
	}
	z, err := strconv.Atoi(q.Get("z"))
	if err != nil || z < 0 || z > 20 || b[0] >= b[2] || b[1] >= b[3] {
		http.Error(w, "z must be a zoom level 0-20 and s<n, w<e", http.StatusBadRequest)
		return
	}
	cell := 0.0
	if z < rawZoom {
		cell = 360 * clusterPx / (256 * math.Exp2(float64(z))) // degrees of longitude per clusterPx at zoom z
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	pins, err := s.st.Pins(ctx, b[0], b[1], b[2], b[3], cell)
	if err != nil {
		s.log.Error("map", "err", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if err := json.NewEncoder(w).Encode(pins); err != nil {
		s.log.Error("map", "err", err)
	}
}

// forever marks responses cacheable for a year: pages link static files by
// content hash, so a changed file gets a new URL.
func forever(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.ServeHTTP(w, r)
	})
}

func (s *server) place(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := s.st.PlaceView(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, view{Title: p.Address + " · city-pulse", Place: p, Feed: placeFeedURL(p.ID)})
}

func (s *server) business(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	b, err := s.st.BusinessView(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, view{Title: b.Name + " · city-pulse", Business: b, Feed: businessFeedURL(b.ID)})
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

// headers sets the security headers every response carries. Scripts, styles
// and fonts are our own; the only third-party content is OpenStreetMap tiles.
// Inline styles stay allowed: the page's <style> block, and Leaflet positions
// map elements with style attributes. data: images are Leaflet blanking
// tiles it has dropped.
func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; "+
				"img-src 'self' data: https://tile.openstreetmap.org; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.ServeHTTP(w, r)
	})
}
