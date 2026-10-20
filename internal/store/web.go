package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/jasonharker/city-pulse/internal/address"
)

// ErrNotFound means no place or business has the requested ID.
var ErrNotFound = errors.New("not found")

// Hit is one search result.
type Hit struct {
	Kind  string // "place" or "business"
	ID    string
	Title string
	Sub   string // address for a business, nothing for a place
	Since time.Time
	Until time.Time
}

// Event is one record on a timeline.
type Event struct {
	Date       time.Time
	Source     string
	Kind       string
	Name       string
	Category   string
	Status     string
	BusinessID string
	PlaceID    string
	Address    string
	RecordID   string
	Versions   int // stored versions of the record
}

// BusinessRow is a business as listed on a place page.
type BusinessRow struct {
	ID, Name    string
	First, Last time.Time
	Closed      bool
	Dated       bool // a record carries a date; otherwise First and Last are when city-pulse first saw it
}

// PlaceView is everything the place page shows.
type PlaceView struct {
	Place
	Building   *Place // the whole building, when this place is one of its units
	Units      []Place
	Businesses []BusinessRow
	Events     []Event
	Total      int // events at the place; Events holds the newest maxEvents
}

// Place is a place's identity, address and point.
type Place struct {
	ID, Address string
	Lat, Lon    *float64
}

// BusinessView is everything the business page shows.
type BusinessView struct {
	BusinessRow
	Place  *Place
	Events []Event
	Total  int
}

const (
	maxHits   = 50
	maxEvents = 300
)

// Search finds places by address and businesses by name. An address query
// matches every unit at that number and street, in any direction it omits.
func (s *Store) Search(ctx context.Context, q string) ([]Hit, error) {
	var hits []Hit
	if a, err := address.Normalize(q); err == nil {
		dir := a.Dir
		if dir == "" {
			dir = "%"
		}
		pattern := likeEscape(a.Number) + "|" + dir + "|" + likeEscape(a.Street) + "|" + likeEscape(a.Unit) + "%"
		rows, err := s.pool.Query(ctx, `SELECT 'place', id, address, '', NULL::date, NULL::date FROM places
			WHERE id LIKE $1 ORDER BY id LIMIT $2`, pattern, maxHits)
		if err != nil {
			return nil, err
		}
		if hits, err = collectHits(rows); err != nil {
			return nil, err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT 'business', b.id, b.name, coalesce(p.address, ''), b.first_date, b.last_date
		FROM businesses b LEFT JOIN places p ON p.id = b.place_id
		WHERE b.search LIKE '%' || $1 || '%'
		ORDER BY similarity(b.search, $2) DESC, b.last_date DESC, b.id
		LIMIT $3`, likeEscape(SearchText(q)), SearchText(q), maxHits)
	if err != nil {
		return nil, err
	}
	names, err := collectHits(rows)
	return append(hits, names...), err
}

func collectHits(rows pgx.Rows) ([]Hit, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Hit, error) {
		var h Hit
		var since, until *time.Time
		err := r.Scan(&h.Kind, &h.ID, &h.Title, &h.Sub, &since, &until)
		if since != nil {
			h.Since, h.Until = *since, *until
		}
		return h, err
	})
}

// SearchText is the form names are searched in: lower case, apostrophes
// dropped, other punctuation as spaces. "JOE'S BAR & GRILL" -> "joes bar grill".
func SearchText(s string) string {
	s = strings.NewReplacer("'", "", "’", "").Replace(strings.ToLower(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// eventsSQL selects timeline events; the caller appends the WHERE column.
const eventsSQL = `SELECT coalesce(o.start_date, o.first_seen::date), o.source, o.kind, o.name, o.category, o.status,
		coalesce(l.business_id, ''), coalesce(l.place_id, ''), o.address, o.record_id,
		(SELECT count(*) FROM observations v WHERE v.source = o.source AND v.record_id = o.record_id), count(*) OVER ()
	FROM links l JOIN observations o ON o.id = l.observation_id
	WHERE l.`

func (s *Store) events(ctx context.Context, col, id string) ([]Event, int, error) {
	rows, err := s.pool.Query(ctx, eventsSQL+col+` = $1
		ORDER BY coalesce(o.start_date, o.first_seen::date) DESC, o.id DESC LIMIT $2`, id, maxEvents)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	evs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		err := r.Scan(&e.Date, &e.Source, &e.Kind, &e.Name, &e.Category, &e.Status, &e.BusinessID, &e.PlaceID, &e.Address, &e.RecordID, &e.Versions, &total)
		// Drop links back to the page being shown: a business has one place.
		e.PlaceID = ""
		if col == "business_id" {
			e.BusinessID = ""
		}
		return e, err
	})
	return evs, total, err
}

func (s *Store) place(ctx context.Context, id string) (*Place, error) {
	var p Place
	err := s.pool.QueryRow(ctx, "SELECT id, address, lat, lon FROM places WHERE id = $1", id).Scan(&p.ID, &p.Address, &p.Lat, &p.Lon)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

// PlaceView loads a place page.
func (s *Store) PlaceView(ctx context.Context, id string) (*PlaceView, error) {
	p, err := s.place(ctx, id)
	if err != nil {
		return nil, err
	}
	v := &PlaceView{Place: *p}
	building := id[:strings.LastIndex(id, "|")+1] // "number|dir|street|"
	if building != id {
		if v.Building, err = s.place(ctx, building); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id, address, lat, lon FROM places
		WHERE id LIKE $1 AND id <> $2 ORDER BY id LIMIT 500`, likeEscape(building)+"_%", id)
	if err != nil {
		return nil, err
	}
	if v.Units, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Place]); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT id, name, first_date, last_date, closed,
			EXISTS (SELECT 1 FROM links l JOIN observations o ON o.id = l.observation_id
				WHERE l.business_id = b.id AND o.start_date IS NOT NULL)
		FROM businesses b WHERE place_id = $1 ORDER BY first_date DESC, id`, id)
	if err != nil {
		return nil, err
	}
	if v.Businesses, err = pgx.CollectRows(rows, pgx.RowToStructByPos[BusinessRow]); err != nil {
		return nil, err
	}
	v.Events, v.Total, err = s.events(ctx, "place_id", id)
	return v, err
}

// BusinessView loads a business page.
func (s *Store) BusinessView(ctx context.Context, id string) (*BusinessView, error) {
	v := &BusinessView{}
	var placeID *string
	err := s.pool.QueryRow(ctx, "SELECT id, name, first_date, last_date, closed, place_id FROM businesses WHERE id = $1", id).
		Scan(&v.ID, &v.Name, &v.First, &v.Last, &v.Closed, &placeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if placeID != nil {
		if v.Place, err = s.place(ctx, *placeID); err != nil {
			return nil, err
		}
	}
	v.Events, v.Total, err = s.events(ctx, "business_id", id)
	return v, err
}

// Pin is one dot on the map: a building with its place, or N buildings
// clustered into one grid cell (no place; N may be 1).
type Pin struct {
	N       int     `json:"n"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	ID      string  `json:"id,omitempty"`
	Address string  `json:"address,omitempty"`
	Places  int     `json:"places,omitempty"` // the building and its units
}

// ponytail: caps on what one map request can return; a viewport never needs more.
const (
	maxPins  = 3000
	maxCells = 5000
)

// Pins returns the buildings inside a box, clustered into square cells of the
// given size in degrees, or one by one when cell is 0.
func (s *Store) Pins(ctx context.Context, south, west, north, east, cell float64) ([]Pin, error) {
	q := `SELECT 1, lat, lon, id, address, places FROM pins
		WHERE lat BETWEEN $1 AND $2 AND lon BETWEEN $3 AND $4 LIMIT $5`
	args := []any{south, north, west, east, maxPins}
	if cell > 0 {
		// No id or address: text aggregates triple the cost of a valley-wide view.
		q = `SELECT count(*)::int, avg(lat), avg(lon), '', '', sum(places)::int FROM pins
			WHERE lat BETWEEN $1 AND $2 AND lon BETWEEN $3 AND $4
			GROUP BY floor(lat / $6), floor(lon / $6) LIMIT $5`
		args = append(args[:4], maxCells, cell)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Pin, error) {
		var p Pin
		return p, r.Scan(&p.N, &p.Lat, &p.Lon, &p.ID, &p.Address, &p.Places)
	})
}

// SourceHealth is the latest run of one source, for /metrics.
type SourceHealth struct {
	Source                 string
	Seen, New, ParseErrors int
	OK                     bool
	LastSuccess            *time.Time
}

// Health returns the latest finished run of every source.
func (s *Store) Health(ctx context.Context) ([]SourceHealth, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (source) source, rows_seen, rows_new, parse_errors, error IS NULL,
			(SELECT max(finished_at) FROM source_runs g WHERE g.source = r.source AND g.error IS NULL AND g.finished_at IS NOT NULL)
		FROM source_runs r WHERE finished_at IS NOT NULL ORDER BY source, id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SourceHealth])
}

// Ping checks the database connection.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
