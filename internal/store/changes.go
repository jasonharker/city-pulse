package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Change is something a nightly run noticed: a record seen for the first
// time, a new version of a known record, or a record its source stopped publishing.
type Change struct {
	What                            string // "new", "updated" or "withdrawn"
	At                              time.Time
	ObservationID                   int64
	Source, RecordID                string
	Name, Category, Status, Address string
	PlaceID, BusinessID             string
}

// maxFeedChanges caps one place's or business's feed.
const maxFeedChanges = 50

// bootSQL names each source's first successful run. Versions it (or a failed
// run before it) stored are the initial load, not changes.
const bootSQL = `WITH boot AS (SELECT source, min(id) AS id FROM source_runs
	WHERE error IS NULL AND finished_at IS NOT NULL GROUP BY source)`

// whatSQL tells a new record from a new version of a known one.
const whatSQL = `CASE WHEN EXISTS (SELECT 1 FROM observations p
		WHERE p.source = v.source AND p.record_id = v.record_id AND p.id < v.id) THEN 'updated' ELSE 'new' END`

// Changes returns up to limit things that changed anywhere since a time, newest first.
// The window is in runs, not first_seen: a new source's initial load is recent
// too, and skipping its million rows by date would scan them all.
// Superseded versions have no link of their own, so each takes its place and
// business from the record's current version.
//
// The LATERAL subquery with OFFSET 0 is an optimization fence. Without it, once
// source_runs has statistics, Postgres flattens the join, hash-joins and
// seq-scans every observation (about 1 s on production instead of 30 ms).
// Tests on small tables cannot catch this.
func (s *Store) Changes(ctx context.Context, since time.Time, limit int) ([]Change, error) {
	return s.changes(ctx, bootSQL+`,
		win AS (SELECT coalesce(max(id), 0) AS id FROM source_runs WHERE started_at <= $1)
		SELECT `+whatSQL+`, v.first_seen, v.id, v.source, v.record_id, v.name, v.category, v.status, v.address,
		       coalesce(c.place_id, ''), coalesce(c.business_id, '')
		FROM boot CROSS JOIN win
		CROSS JOIN LATERAL (SELECT * FROM observations v
			WHERE v.source = boot.source AND v.first_run_id > greatest(boot.id, win.id) OFFSET 0) v
		LEFT JOIN LATERAL (SELECT l.place_id, l.business_id FROM observations o JOIN links l ON l.observation_id = o.id
			WHERE o.source = v.source AND o.record_id = v.record_id LIMIT 1) c ON true
		UNION ALL
		-- Deliberately mixed: versions are windowed by run, withdrawals by time.
		SELECT 'withdrawn', o.withdrawn_at, o.id, o.source, o.record_id, o.name, o.category, o.status, o.address,
		       coalesce(l.place_id, ''), coalesce(l.business_id, '')
		FROM observations o JOIN links l ON l.observation_id = o.id
		WHERE o.withdrawn_at > $1
		ORDER BY 2 DESC, 3 DESC LIMIT $2`, since, limit)
}

// PlaceChanges returns a place's address and what changed there, newest first.
func (s *Store) PlaceChanges(ctx context.Context, id string) (string, []Change, error) {
	return s.scopedChanges(ctx, "SELECT address FROM places WHERE id = $1", "place_id", id)
}

// BusinessChanges returns a business's name and what changed in its records, newest first.
func (s *Store) BusinessChanges(ctx context.Context, id string) (string, []Change, error) {
	return s.scopedChanges(ctx, "SELECT name FROM businesses WHERE id = $1", "business_id", id)
}

func (s *Store) scopedChanges(ctx context.Context, titleSQL, col, id string) (string, []Change, error) {
	var title string
	err := s.pool.QueryRow(ctx, titleSQL, id).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	cs, err := s.changes(ctx, bootSQL+`, recs AS (
			SELECT o.id, o.source, o.record_id, o.withdrawn_at, l.place_id, l.business_id
			FROM links l JOIN observations o ON o.id = l.observation_id WHERE l.`+col+` = $1)
		SELECT `+whatSQL+`, v.first_seen, v.id, v.source, v.record_id, v.name, v.category, v.status, v.address,
		       coalesce(r.place_id, ''), coalesce(r.business_id, '')
		FROM recs r JOIN observations v ON v.source = r.source AND v.record_id = r.record_id
		JOIN boot ON boot.source = v.source AND v.first_run_id > boot.id
		UNION ALL
		SELECT 'withdrawn', r.withdrawn_at, o.id, o.source, o.record_id, o.name, o.category, o.status, o.address,
		       coalesce(r.place_id, ''), coalesce(r.business_id, '')
		FROM recs r JOIN observations o ON o.id = r.id
		WHERE r.withdrawn_at IS NOT NULL
		ORDER BY 2 DESC, 3 DESC LIMIT $2`, id, maxFeedChanges)
	return title, cs, err
}

func (s *Store) changes(ctx context.Context, sql string, args ...any) ([]Change, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Change])
}

// RecordView is every stored version of one source record, newest first.
type RecordView struct {
	Source, RecordID    string
	PlaceID, BusinessID string // from the current version's link
	Versions            []Version
}

// Latest is the newest stored version.
func (v *RecordView) Latest() Version { return v.Versions[0] }

// Version is one stored version of a record. Fields is the whole payload for
// the first version; for later ones, only what changed from the version before.
type Version struct {
	FirstSeen  time.Time
	Withdrawn  *time.Time
	Name       string
	Category   string
	Status     string
	Address    string
	Start, End *time.Time
	First      bool
	Fields     []FieldChange
}

// FieldChange is one payload field of a version; From is empty for a field
// the previous version lacked (and for every field of the first version).
type FieldChange struct {
	Field, From, To string
}

// RecordView loads a record's version history.
func (s *Store) RecordView(ctx context.Context, source, id string) (*RecordView, error) {
	rows, err := s.pool.Query(ctx, `SELECT o.first_seen, o.withdrawn_at, o.name, o.category, o.status, o.address,
			o.start_date, o.end_date, o.payload, coalesce(l.place_id, ''), coalesce(l.business_id, ''), l.observation_id IS NOT NULL
		FROM observations o LEFT JOIN links l ON l.observation_id = o.id
		WHERE o.source = $1 AND o.record_id = $2 ORDER BY o.id`, source, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	v := &RecordView{Source: source, RecordID: id}
	var prev map[string]any
	for rows.Next() {
		var ver Version
		var payload map[string]any
		var place, business string
		var current bool
		if err := rows.Scan(&ver.FirstSeen, &ver.Withdrawn, &ver.Name, &ver.Category, &ver.Status, &ver.Address,
			&ver.Start, &ver.End, &payload, &place, &business, &current); err != nil {
			return nil, err
		}
		if current {
			v.PlaceID, v.BusinessID = place, business
		}
		ver.First, ver.Fields = prev == nil, diff(prev, payload)
		v.Versions = append(v.Versions, ver)
		prev = payload
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(v.Versions) == 0 {
		return nil, ErrNotFound
	}
	slices.Reverse(v.Versions)
	return v, nil
}

// diff lists the payload fields of cur that differ from prev, by field name.
// With no prev, it lists every field of cur.
func diff(prev, cur map[string]any) []FieldChange {
	var out []FieldChange
	keys := make([]string, 0, len(cur)+len(prev))
	for k := range cur {
		keys = append(keys, k)
	}
	for k := range prev {
		if _, ok := cur[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		from, had := prev[k]
		to, has := cur[k]
		f, t := text(from, had), text(to, has)
		if prev == nil || f != t {
			out = append(out, FieldChange{Field: k, From: f, To: t})
		}
	}
	return out
}

func text(v any, ok bool) string {
	if !ok || v == nil {
		return ""
	}
	if s, isStr := v.(string); isStr {
		return s
	}
	// Not json.Marshal: it writes & as \u0026, and the page escapes on its own.
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
