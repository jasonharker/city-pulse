package web

import (
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

// siteURL makes feed links absolute: feed readers show entries out of context.
const siteURL = "https://citypulse.jasonharker.com"

// changeDays is how far back /changes and its feed look.
const changeDays = 7

// Most changes the /changes page and its feed show.
const (
	maxPageChanges = 500
	maxFeedEntries = 100
)

// whatLabels name a change for readers.
var whatLabels = map[string]string{"new": "New", "updated": "Updated", "withdrawn": "No longer published"}

// night is one nightly run's worth of changes.
type night struct {
	Date                    time.Time
	Changes                 []store.Change
	New, Updated, Withdrawn int
	Partial                 bool // the page's cap cut this night short
}

// nights groups changes, newest first, by the day they were found.
func nights(cs []store.Change) []night {
	var ns []night
	for _, c := range cs {
		y, m, d := c.At.UTC().Date()
		day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		if len(ns) == 0 || !ns[len(ns)-1].Date.Equal(day) {
			ns = append(ns, night{Date: day})
		}
		n := &ns[len(ns)-1]
		n.Changes = append(n.Changes, c)
		switch c.What {
		case "new":
			n.New++
		case "updated":
			n.Updated++
		default:
			n.Withdrawn++
		}
	}
	return ns
}

// pageNights groups cs, which was fetched with one row beyond max, into nights;
// when that row came back the oldest night is partial.
func pageNights(cs []store.Change, max int) (ns []night, truncated bool) {
	if truncated = len(cs) > max; truncated {
		cs = cs[:max]
	}
	ns = nights(cs)
	if truncated && len(ns) > 0 {
		ns[len(ns)-1].Partial = true
	}
	return ns, truncated
}

func (s *server) changes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cs, err := s.st.Changes(ctx, time.Now().AddDate(0, 0, -changeDays), maxPageChanges+1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ns, truncated := pageNights(cs, maxPageChanges)
	s.render(w, r, view{Title: "What changed · city-pulse", Page: "changes", Feed: "/changes.atom",
		Nights: ns, Truncated: truncated})
}

func (s *server) changesFeed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cs, err := s.st.Changes(ctx, time.Now().AddDate(0, 0, -changeDays), maxFeedEntries)
	s.atom(ctx, w, r, "Las Vegas place records: what changed", "/changes", "/changes.atom", cs, err)
}

func (s *server) placeFeed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.PathValue("id")
	addr, cs, err := s.st.PlaceChanges(ctx, id)
	s.atom(ctx, w, r, addr+" · city-pulse", placeURL(id), placeFeedURL(id), cs, err)
}

func (s *server) businessFeed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.PathValue("id")
	name, cs, err := s.st.BusinessChanges(ctx, id)
	s.atom(ctx, w, r, name+" · city-pulse", businessURL(id), businessFeedURL(id), cs, err)
}

func placeFeedURL(id string) string    { return "/feed/place/" + url.PathEscape(id) }
func businessFeedURL(id string) string { return "/feed/business/" + url.PathEscape(id) }

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Author  string      `xml:"author>name"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Href string `xml:"href,attr"`
}

type atomEntry struct {
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Updated string   `xml:"updated"`
	Link    atomLink `xml:"link"`
	Summary string   `xml:"summary,omitempty"`
}

// atom writes changes as an Atom feed; page is the HTML page it follows.
func (s *server) atom(ctx context.Context, w http.ResponseWriter, r *http.Request, title, page, self string, cs []store.Change, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.log.Error("feed", "path", r.URL.Path, "err", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	f := atomFeed{Title: title, ID: siteURL + self, Author: "city-pulse",
		Links: []atomLink{{Rel: "self", Href: siteURL + self}, {Rel: "alternate", Href: siteURL + page}}}
	// An empty feed is as fresh as the data behind it.
	var updated time.Time
	if len(cs) > 0 {
		updated = cs[0].At
	} else {
		updated = s.lastIngest(ctx)
	}
	f.Updated = updated.UTC().Format(time.RFC3339)
	for _, c := range cs {
		f.Entries = append(f.Entries, atomEntry{
			Title:   whatLabels[c.What] + ": " + cmp.Or(c.Name, c.Address, c.RecordID) + " (" + source(c.Source) + ")",
			ID:      fmt.Sprintf("tag:citypulse.jasonharker.com,2026:%s/%d", c.What, c.ObservationID),
			Updated: c.At.UTC().Format(time.RFC3339),
			Link:    atomLink{Href: siteURL + recordURL(c.Source, c.RecordID)},
			Summary: join(" · ", c.Address, c.Category, c.Status),
		})
	}
	var buf bytes.Buffer
	if err := xml.NewEncoder(&buf).Encode(f); err != nil {
		s.log.Error("feed", "path", r.URL.Path, "err", err)
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if _, err := w.Write(append([]byte(xml.Header), buf.Bytes()...)); err != nil {
		s.log.Error("feed", "path", r.URL.Path, "err", err)
	}
}

// join joins the non-empty parts.
func join(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
