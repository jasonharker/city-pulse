# city-pulse Feed, History and Succession Implementation Plan (Plan 5)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show what the nightly runs already know but the site hides: what changed (a `/changes` page and Atom feeds per city, place and business), every stored version of a record (`/record/...`), the businesses at a place on one time axis (a succession strip), and what each source covers (`/sources`).

**Architecture:** No new data is collected. `observations` already keeps every version (`first_run_id`, `first_seen`) and every withdrawal (`withdrawn_at`); this plan adds one migration (two indexes), read queries in `internal/store/changes.go`, and server-rendered pages in `internal/web`. Atom is `encoding/xml`. The strip is positioned `<span>`s in `html/template`: no JavaScript. Spec: `docs/superpowers/specs/2026-10-14-city-pulse-design.md`.

**Tech Stack:** Go 1.27, Postgres 16, `github.com/jackc/pgx/v5`. Dev/test database: `tools/devdb`.

**Provenance:** every code block below was compiled and tested before this plan was written (branch `feat/feed-history`, 2026-10-19): `go vet` clean, all unit and Postgres-backed tests pass, and `citypulse serve` ran against a restored copy of production (1,107,199 observations) with the timings quoted in Task 9. Pages were checked in headless Chromium at 1280 px and 390 px, light and dark.

**Decided with Jason, 2026-10-19 (not to be re-litigated without new data):**
- *Turnover leaderboard: dropped.* Measured on production: ranking places by business count surfaces registered-agent suites (10750 W Charleston #180: 977 undated City licenses); restaurant-only succession surfaces ghost kitchens and commissaries ("@ 4D", catering, trailers). Real storefronts top out at 3 successions. The query also took 6.8 s.
- *Wrong-merge report link: dropped.* The repo is private, so the public can't open issues; revisit with the overrides table.
- *Succession strip: server-rendered now.* A JS timeline can replace it once the JS toolchain is chosen.

**Facts measured on production that shape the code:**
- Nightly change volume: 25–900 new versions per night, all from `snhd` and `clvlicense`. `ccbiz` and `clvpermit` stored 0 new versions on Oct 16–19 because upstream didn't change (the City's permit layers were last edited Oct 11 and Oct 14).
- Each source's first successful run stored 1.1M versions. That initial load is not a change and must never appear in a feed.
- Undated businesses: `clvlicense` publishes no issue dates, so its businesses span only the night city-pulse first saw them. 10750 W Charleston #180 has 977 businesses and 0 dated ones; 3570 S Las Vegas Blvd has 305, all dated.

---

## File structure

```
internal/store/migrations/005_changes.sql   indexes for the change feed
internal/store/changes.go                    Changes, PlaceChanges, BusinessChanges, RecordView, payload diff
internal/store/changes_test.go               Postgres-backed: feed semantics, record history
internal/store/diff_test.go                  unit: payload diff
internal/store/web.go                        (modify) Event.RecordID/Versions, BusinessRow.Dated
internal/web/succession.go                   strip layout (pure)
internal/web/succession_test.go
internal/web/feed.go                         /changes, Atom feeds
internal/web/feed_test.go                    unit: nights grouping, num
internal/web/web.go                          (modify) routes, view, FuncMap, record and sources handlers, num
internal/web/page.html                       (modify) templates and CSS
internal/web/web_test.go                     (modify) two-run fixture, new routes
docs/superpowers/specs/2026-10-14-city-pulse-design.md   (modify) as built
```

Routes added: `GET /record/{source}/{id...}`, `GET /changes`, `GET /changes.atom`, `GET /feed/place/{id...}`, `GET /feed/business/{id...}`, `GET /sources`.

**Running tests:** start the dev database in another terminal (`cd tools/devdb && go run .`), then

```bash
export CITYPULSE_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable'
```

Without the variable, Postgres-backed tests skip; that is not a pass.

---

### Task 1: Migration 005, the change-feed indexes

**Files:**
- Create: `internal/store/migrations/005_changes.sql`

The feed reads "versions stored by runs after a cutoff, per source" and "withdrawals after a time". Measured on the production copy: with no index the citywide query took 1.1 s. An index on `first_seen` alone fixes the steady state, but while a source's initial load is younger than the window it scans the whole load (1.5 s). `(source, first_run_id)` with the window expressed as a run id takes 3 ms in both cases. Building both indexes on production-sized data takes about 0.7 s, inside the migration transaction at serve start.

- [ ] **Step 1: Write the migration**

```sql
-- The change feed reads the versions recent runs stored, per source, and
-- withdrawals by when they happened.
CREATE INDEX observations_run_idx ON observations (source, first_run_id);
CREATE INDEX observations_withdrawn_idx ON observations (withdrawn_at) WHERE withdrawn_at IS NOT NULL;
```

- [ ] **Step 2: Run the migration test**

Run: `go test -count=1 -run TestMigrateIsIdempotent -v ./internal/store/`
Expected: `--- PASS: TestMigrateIsIdempotent`

- [ ] **Step 3: Commit**

```bash
git add internal/store/migrations/005_changes.sql
git commit -m "feat(store): indexes for the change feed"
```

---

### Task 2: Store: changes and record history

**Files:**
- Create: `internal/store/changes.go`, `internal/store/changes_test.go`, `internal/store/diff_test.go`

Rules the code implements:
- A **change** is a version stored by a run after its source's first successful run (`boot`), or a withdrawal. Versions stored by the first successful run, or by failed runs before it, are the initial load.
- `new` vs `updated`: whether an earlier version of the same `(source, record_id)` exists.
- Only current versions have `links` rows. A superseded version takes its place and business from the record's current version (`LATERAL` lookup). A withdrawn record's current version is still linked, because `CurrentObservations` does not filter withdrawals.
- The citywide window is in **runs** (`win` = the newest run started at or before `since`), not `first_seen`. See Task 1 for the measurement.
- Record history diffs the **payload**, the record as the source published it, not the parsed columns, which derive from it. The first version lists every field. `text` encodes non-strings as JSON without HTML escaping, because `json.Marshal` turns `&` into `&` and `html/template` escapes on its own.

- [ ] **Step 1: Write the failing tests**

`internal/store/diff_test.go` (package `store`, unit):

```go
package store

import (
	"reflect"
	"testing"
)

func TestDiff(t *testing.T) {
	got := diff(map[string]any{"same": "x", "gone": 1.0, "owners": []any{"C&A, LLC"}},
		map[string]any{"same": "x", "added": "y", "owners": []any{"C&A, LLC", "PRIMARY"}})
	want := []FieldChange{
		{Field: "added", To: "y"},
		{Field: "gone", From: "1"},
		{Field: "owners", From: `["C&A, LLC"]`, To: `["C&A, LLC","PRIMARY"]`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diff = %+v\nwant %+v", got, want)
	}
}
```

`internal/store/changes_test.go` (package `store_test`, Postgres; uses the `obs` and `run` helpers from `store_test.go`, which write source `"test"`, name `"Joe's"`, address `"1 Main St"`):

```go
package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/resolve"
	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store"
	"github.com/jasonharker/city-pulse/internal/store/storetest"
)

func TestChangesAndRecordView(t *testing.T) {
	st, _ := storetest.New(t)
	ctx := context.Background()
	resolveAll := func() {
		t.Helper()
		cur, err := st.CurrentObservations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceResolved(ctx, resolve.Build(cur)); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Now().Add(-time.Minute)

	// Run 1 is the initial load; run 2 changes a, drops b and adds c.
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 1}), obs(t, "b", map[string]any{"v": 1})})
	resolveAll()
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 2}), obs(t, "c", map[string]any{"v": 1})})
	resolveAll()

	cs, err := st.Changes(ctx, since, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cs {
		got[c.RecordID] = c.What
		if c.PlaceID == "" || c.BusinessID == "" {
			t.Errorf("change %+v has no place or business", c)
		}
	}
	if want := map[string]string{"a": "updated", "b": "withdrawn", "c": "new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v (the initial load is not a change)", got, want)
	}

	if later, err := st.Changes(ctx, time.Now(), 100); err != nil || len(later) != 0 {
		t.Errorf("Changes since now = %d, %v; want none (both runs started before)", len(later), err)
	}

	addr, pcs, err := st.PlaceChanges(ctx, cs[0].PlaceID)
	if err != nil || addr != "1 Main St" || len(pcs) != 3 {
		t.Errorf("PlaceChanges = %q, %d changes, %v; want 1 Main St, 3", addr, len(pcs), err)
	}
	name, bcs, err := st.BusinessChanges(ctx, cs[0].BusinessID)
	if err != nil || name != "Joe's" || len(bcs) != 3 {
		t.Errorf("BusinessChanges = %q, %d changes, %v; want Joe's, 3", name, len(bcs), err)
	}
	if _, _, err := st.PlaceChanges(ctx, "nowhere"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("PlaceChanges(nowhere) err = %v, want ErrNotFound", err)
	}

	rv, err := st.RecordView(ctx, "test", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.Versions) != 2 || rv.PlaceID == "" {
		t.Fatalf("RecordView(a) = %+v", rv)
	}
	// Newest first.
	if v := rv.Versions[1]; !v.First || !reflect.DeepEqual(v.Fields, []store.FieldChange{{Field: "v", To: "1"}}) {
		t.Errorf("first version = %+v, want the whole payload", v)
	}
	if v := rv.Versions[0]; v.First || !reflect.DeepEqual(v.Fields, []store.FieldChange{{Field: "v", From: "1", To: "2"}}) {
		t.Errorf("second version = %+v, want v 1 -> 2", v)
	}
	if _, err := st.RecordView(ctx, "test", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordView(nope) err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/store/`
Expected: build failure, `undefined: diff`, `st.Changes undefined`.

- [ ] **Step 3: Implement**

`internal/store/changes.go`:

```go
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
func (s *Store) Changes(ctx context.Context, since time.Time, limit int) ([]Change, error) {
	return s.changes(ctx, bootSQL+`,
		win AS (SELECT coalesce(max(id), 0) AS id FROM source_runs WHERE started_at <= $1)
		SELECT `+whatSQL+`, v.first_seen, v.id, v.source, v.record_id, v.name, v.category, v.status, v.address,
		       coalesce(c.place_id, ''), coalesce(c.business_id, '')
		FROM observations v JOIN boot ON boot.source = v.source CROSS JOIN win
		LEFT JOIN LATERAL (SELECT l.place_id, l.business_id FROM observations o JOIN links l ON l.observation_id = o.id
			WHERE o.source = v.source AND o.record_id = v.record_id LIMIT 1) c ON true
		WHERE v.first_run_id > greatest(boot.id, win.id)
		UNION ALL
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
	// Not json.Marshal: it writes & as &, and the page escapes on its own.
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -count=1 -run 'TestDiff|TestChangesAndRecordView' -v ./internal/store/`
Expected: both `PASS` (not `SKIP`: check `CITYPULSE_TEST_DATABASE_URL`).

- [ ] **Step 5: Commit**

```bash
git add internal/store/changes.go internal/store/changes_test.go internal/store/diff_test.go
git commit -m "feat(store): change feed queries and record version history"
```

---

### Task 3: Store: record links on events, dated businesses

**Files:**
- Modify: `internal/store/web.go`

Timeline events need the record's id and version count so they can link to `/record/...`. Place businesses need `Dated` so the strip can skip businesses whose span is only "first seen". Measured on production: the version-count subquery adds no measurable time to the busiest place (3799 S Las Vegas Blvd, 19.5 → 17.6 ms); `Dated` costs 27 ms at the 977-business suite and under 1 ms at a normal storefront.

- [ ] **Step 1: Extend `Event` and `BusinessRow`**

Replace the end of `Event` and all of `BusinessRow`:

```go
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
```

- [ ] **Step 2: Select the new event columns**

In `eventsSQL`, replace the line `coalesce(l.business_id, ''), coalesce(l.place_id, ''), o.address, count(*) OVER ()` with:

```go
		coalesce(l.business_id, ''), coalesce(l.place_id, ''), o.address, o.record_id,
		(SELECT count(*) FROM observations v WHERE v.source = o.source AND v.record_id = o.record_id), count(*) OVER ()
```

and in `events`, scan them:

```go
		err := r.Scan(&e.Date, &e.Source, &e.Kind, &e.Name, &e.Category, &e.Status, &e.BusinessID, &e.PlaceID, &e.Address, &e.RecordID, &e.Versions, &total)
```

- [ ] **Step 3: Select `Dated` in `PlaceView`**

Replace the businesses query in `PlaceView` (`pgx.RowToStructByPos[BusinessRow]` maps by position, so the column order must match the struct):

```go
	rows, err = s.pool.Query(ctx, `SELECT id, name, first_date, last_date, closed,
			EXISTS (SELECT 1 FROM links l JOIN observations o ON o.id = l.observation_id
				WHERE l.business_id = b.id AND o.start_date IS NOT NULL)
		FROM businesses b WHERE place_id = $1 ORDER BY first_date DESC, id`, id)
```

`BusinessView` scans its fields explicitly and needs no change.

- [ ] **Step 4: Run the existing tests**

Run: `go vet ./... && go test -count=1 ./internal/...`
Expected: all `ok`. The new fields are asserted through pages in Tasks 4 and 5.

- [ ] **Step 5: Commit**

```bash
git add internal/store/web.go
git commit -m "feat(store): events carry their record, place businesses whether they are dated"
```

---

### Task 4: Record page

**Files:**
- Modify: `internal/web/web.go`, `internal/web/page.html`, `internal/web/web_test.go`

- [ ] **Step 1: Make the site fixture produce a change**

In `TestSite`, replace the fixture (from `d := time.Date(...)` through the `ReplaceResolved` call) with two runs: run 1 is the initial load, and run 2 closes JOE'S BAR (a new payload field `s`, so a new version):

```go
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
```

Then add these cases to the table, the first replacing the existing `{unit, 200, ...}` case:

```go
		{unit, 200, []string{"JOE&#39;S BAR", "&lt;b&gt;Taco&lt;/b&gt; Place", "A unit of", "Health inspection (SNHD)", "May 1, 2019",
			`href="/record/snhd/1">2 versions</a>`}},
		{"/record/snhd/1", 200, []string{"JOE&#39;S BAR", "2 versions", "<td>s</td><td>Closed</td>", "As first published"}},
		{"/record/nope/1", 404, []string{"Not found"}},
		{"/record/snhd/nope", 404, []string{"Not found"}},
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 -run TestSite ./internal/web/`
Expected: FAIL. `/record/snhd/1` returns 404 (no route) and the unit page lacks the `2 versions` link.

- [ ] **Step 3: Name the URL helpers and register the route**

In `web.go`, replace the `placeURL`, `businessURL` and `source` lambdas in the `FuncMap` with named functions, which the feed code (Task 6) also calls, and add `recordURL`:

```go
func placeURL(id string) string    { return "/place/" + url.PathEscape(id) }
func businessURL(id string) string { return "/business/" + url.PathEscape(id) }
func recordURL(src, id string) string {
	return "/record/" + url.PathEscape(src) + "/" + url.PathEscape(id)
}
func source(s string) string { return cmpOr(sourceNames[s], s) }

var page = template.Must(template.New("page.html").Funcs(template.FuncMap{
	"placeURL":    placeURL,
	"businessURL": businessURL,
	"recordURL":   recordURL,
	"source":      source,
	"day":         func(t time.Time) string { return t.Format("Jan 2, 2006") },
	// year, deref and static unchanged
```

Register the route after `GET /business/{id...}`:

```go
	mux.HandleFunc("GET /record/{source}/{id...}", s.record)
```

Add `Record *store.RecordView` to `view` after `Business`, and the handler after `search`:

```go
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
	s.render(w, r, view{Title: cmpOr(l.Name, l.Address) + " · " + source(src) + " · city-pulse", Record: rv})
}
```

- [ ] **Step 4: Templates**

In the `<main>` switch, after the `.Business` branch:

```html
{{- else if .Record}}{{template "record" .Record}}
```

In `{{define "events"}}`, replace the `{{- if or .BusinessID .PlaceID}}<p>` … `</p>{{end}}</div></li>` block so every event links its record (`{{- /**/}}` trims the newline before the link):

```html
        <p>
        {{- if .BusinessID}}<a href="{{businessURL .BusinessID}}">the business</a> · {{end}}
        {{- if .PlaceID}}<a href="{{placeURL .PlaceID}}">the place</a> · {{end}}
        {{- /**/}}<a href="{{recordURL .Source .RecordID}}">{{if gt .Versions 1}}{{.Versions}} versions{{else}}the record{{end}}</a></p></div></li>
```

Append after `{{define "events"}}…{{end}}`:

```html
{{define "record"}}
<article class="results">
  {{- $l := .Latest}}
  <header class="dossier__head">
    <p class="label">{{source .Source}} · record {{.RecordID}}</p>
    <h1>{{or $l.Name $l.Address}}</h1>
    <p class="label">{{len .Versions}} version{{if ne (len .Versions) 1}}s{{end}}
      {{- with $l.Withdrawn}} · no longer published since {{day .}}{{end}}
      {{- if .BusinessID}} · <a href="{{businessURL .BusinessID}}">the business</a>{{end}}
      {{- if .PlaceID}} · <a href="{{placeURL .PlaceID}}">the place</a>{{end}}</p>
  </header>
  <section>
    <h2>Versions <span class="label">newest first, dated by the night city-pulse first saw each</span></h2>
    <ol class="timeline">
    {{- range .Versions}}
      <li><time datetime="{{.FirstSeen.Format "2006-01-02"}}">{{day .FirstSeen}}</time>
        <div><p>{{.Name}}{{if or .Category .Status}}<br><span class="muted">{{.Category}}{{if and .Category .Status}} · {{end}}{{.Status}}</span>{{end}}</p>
        {{- if .First}}
          <details><summary>As first published <span class="label">{{len .Fields}} fields</span></summary>{{template "fields" .Fields}}</details>
        {{- else if .Fields}}{{template "fields" .Fields}}
        {{- end}}</div></li>
    {{- end}}
    </ol>
  </section>
</article>
{{end}}

{{define "fields"}}
<table class="fields">
  <thead><tr><th scope="col">Field</th><th scope="col">Value</th></tr></thead>
  <tbody>
  {{- range .}}
    <tr><td>{{.Field}}</td><td>{{with .From}}<del>{{.}}</del> → {{end}}{{.To}}</td></tr>
  {{- end}}
  </tbody>
</table>
{{end}}
```

Add to the `<style>` block, before `/* Search results */`:

```css
/* A record's versions */
.fields { width: 100%; border-collapse: collapse; font-size: var(--text-sm); }
.fields th, .fields td { padding: var(--space-2xs) var(--space-xs) var(--space-2xs) 0; border-bottom: var(--rule-hair) solid var(--color-rule); text-align: left; vertical-align: top; overflow-wrap: anywhere; }
.fields th { font: 400 var(--text-xs)/1.4 var(--font-label); color: var(--color-muted); }
.fields td:first-child { width: 1%; white-space: nowrap; }
.fields del { color: var(--color-muted); }
```

and after `.results h1 { … }`:

```css
.results > section { display: grid; gap: var(--space-sm); }
.results > section > p { margin: 0; }
```

- [ ] **Step 5: Run the tests**

Run: `go test -count=1 -run TestSite -v ./internal/web/`
Expected: `--- PASS: TestSite`. The test also fails on any `ZgotmplZ`, which is html/template rejecting a value.

- [ ] **Step 6: Commit**

```bash
git add internal/web/web.go internal/web/page.html internal/web/web_test.go
git commit -m "feat(web): record pages with version history; events link their record"
```

---

### Task 5: Succession strip

**Files:**
- Create: `internal/web/succession.go`, `internal/web/succession_test.go`
- Modify: `internal/web/web.go`, `internal/web/page.html`, `internal/web/web_test.go`

Rules: only dated businesses; no strip with fewer than two (one bar is no succession); the newest 30 (3570 S Las Vegas Blvd has 305 dated businesses), drawn oldest first; the axis runs from January 1 of the first year to January 1 after the last; every bar at least 0.5 % wide and inside the axis. Bar positions go into a `style` attribute, which the CSP allows (`style-src 'unsafe-inline'`, already needed by Leaflet).

- [ ] **Step 1: Write the failing tests**

`internal/web/succession_test.go`:

```go
package web

import (
	"fmt"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

func TestSuccession(t *testing.T) {
	d := func(y, m, day int) time.Time { return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC) }
	// Newest first, as PlaceView orders them.
	bs := []store.BusinessRow{
		{ID: "undated", First: d(2026, 10, 15), Last: d(2026, 10, 15)},
		{ID: "tacos", First: d(2015, 1, 1), Last: d(2019, 12, 31), Dated: true},
		{ID: "bar", First: d(2010, 1, 1), Last: d(2015, 1, 1), Dated: true},
	}
	s := newSuccession(bs)
	if s == nil || s.From != 2010 || s.To != 2020 || s.Dated != 2 || len(s.Bars) != 2 {
		t.Fatalf("succession = %+v; want 2010-2020 with two bars", s)
	}
	if b := s.Bars[0]; b.ID != "bar" || b.Left != 0 || fmt.Sprintf("%.1f", b.Width) != "50.0" {
		t.Errorf("first bar = %s %.2f+%.2f; want bar at 0 for 50%%", b.ID, b.Left, b.Width)
	}
	if b := s.Bars[1]; b.ID != "tacos" || fmt.Sprintf("%.1f", b.Left) != "50.0" || b.Left+b.Width > 100 {
		t.Errorf("second bar = %s %.2f+%.2f; want tacos from 50%% to the end", b.ID, b.Left, b.Width)
	}

	// A one-day business still gets a visible bar, inside the axis.
	bs[1].Last = bs[1].First.Add(24 * time.Hour)
	bs[2].First, bs[2].Last = d(2010, 12, 31), d(2010, 12, 31)
	for _, b := range newSuccession(bs[1:]).Bars {
		if b.Width < minWidth || b.Left+b.Width > 100 {
			t.Errorf("bar %s = %.2f+%.2f; want at least %.1f wide, inside 100", b.ID, b.Left, b.Width, minWidth)
		}
	}

	if newSuccession(bs[:2]) != nil {
		t.Error("one dated business should give no succession")
	}
	many := make([]store.BusinessRow, maxBars+5)
	for i := range many {
		many[i] = store.BusinessRow{ID: fmt.Sprint(i), First: d(2040-i, 1, 1), Last: d(2040-i, 6, 1), Dated: true} // newest first
	}
	if s := newSuccession(many); len(s.Bars) != maxBars || s.Dated != maxBars+5 {
		t.Errorf("bars = %d of %d; want %d of %d", len(s.Bars), s.Dated, maxBars, maxBars+5)
	}
}
```

In `TestSite`, extend the unit page case's wants. Both fixture businesses are dated May 1, 2019, so each bar sits at 120/365 = 32.88 % at the minimum width:

```go
			"What was here, when", `style="left: 32.88%; width: 0.50%"`,
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/web/`
Expected: build failure, `undefined: newSuccession`.

- [ ] **Step 3: Implement**

`internal/web/succession.go`:

```go
package web

import (
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

// A succession lays out the businesses at a place as bars on one time axis,
// oldest first. Undated businesses are left out: their span is when
// city-pulse first saw them, which says nothing about when they opened.
type succession struct {
	From, To int // the axis runs from January 1 of From to January 1 of To
	Bars     []bar
	Dated    int // dated businesses at the place; Bars holds the newest maxBars
}

type bar struct {
	store.BusinessRow
	Left, Width float64 // percent of the axis
}

// ponytail: newest 30 only; a casino has 300+. A zoomable timeline is the upgrade.
const (
	maxBars  = 30
	minWidth = 0.5 // percent, so a one-day business still shows
)

// newSuccession returns nil when fewer than two businesses are dated:
// one bar is no succession. bs is newest first, as PlaceView orders it.
func newSuccession(bs []store.BusinessRow) *succession {
	var dated []store.BusinessRow
	for _, b := range bs {
		if b.Dated {
			dated = append(dated, b)
		}
	}
	if len(dated) < 2 {
		return nil
	}
	shown := dated[:min(len(dated), maxBars)]
	first, last := shown[0].First, shown[0].Last
	for _, b := range shown {
		if b.First.Before(first) {
			first = b.First
		}
		if b.Last.After(last) {
			last = b.Last
		}
	}
	s := &succession{From: first.Year(), To: last.Year() + 1, Dated: len(dated)}
	start := time.Date(s.From, 1, 1, 0, 0, 0, 0, time.UTC)
	span := float64(time.Date(s.To, 1, 1, 0, 0, 0, 0, time.UTC).Sub(start))
	for i := len(shown) - 1; i >= 0; i-- {
		b := shown[i]
		left := min(100*float64(b.First.Sub(start))/span, 100-minWidth)
		width := min(max(100*float64(b.Last.Sub(b.First))/span, minWidth), 100-left)
		s.Bars = append(s.Bars, bar{BusinessRow: b, Left: left, Width: width})
	}
	return s
}

// Through is the last year on the axis.
func (s *succession) Through() int { return s.To - 1 }
```

Add `"succession": newSuccession,` to the `FuncMap`.

- [ ] **Step 4: Template and CSS**

In `{{define "place"}}`, replace `<div class="dossier__main">{{template "events" .}}</div>` with:

```html
  <div class="dossier__main">
    {{- with succession .Businesses}}
    <section class="strip">
      <h2>What was here, when{{if gt .Dated (len .Bars)}} <span class="label">newest {{len .Bars}} of {{.Dated}}</span>{{end}}</h2>
      <ol>
      {{- range .Bars}}
        <li><span><a href="{{businessURL .ID}}">{{.Name}}</a> <span class="label">{{year .First}}–{{year .Last}}{{if .Closed}} · closed{{end}}</span></span>
          <span class="strip__track" aria-hidden="true"><span class="strip__bar{{if .Closed}} strip__bar--closed{{end}}" style="left: {{printf "%.2f" .Left}}%; width: {{printf "%.2f" .Width}}%"></span></span></li>
      {{- end}}
      </ol>
      <p class="strip__axis label" aria-hidden="true"><span>{{.From}}</span><span>{{.Through}}</span></p>
    </section>
    {{- end}}
    {{template "events" .}}
  </div>
```

Add to `<style>`, before `/* A record's versions */`:

```css
/* Succession: the businesses at a place on one time axis */
.strip ol { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--space-sm); }
.strip li { display: grid; gap: var(--space-2xs); }
.strip__track { position: relative; height: 0.75rem; background: var(--color-paper-2); }
.strip__bar { position: absolute; inset-block: 0; background: var(--color-pin); }
.strip__bar--closed { background: var(--color-rule-2); }
.strip__axis { display: flex; justify-content: space-between; margin: 0; }
```

Each name stacks above its bar at every width. A 13rem name column beside the track was tried and truncated names on desktop.

- [ ] **Step 5: Run the tests**

Run: `go test -count=1 ./internal/web/`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/web/succession.go internal/web/succession_test.go internal/web/web.go internal/web/page.html internal/web/web_test.go
git commit -m "feat(web): succession strip on place pages"
```

---

### Task 6: What changed: page and Atom feeds

**Files:**
- Create: `internal/web/feed.go`, `internal/web/feed_test.go`
- Modify: `internal/web/web.go`, `internal/web/page.html`, `internal/web/web_test.go`

The `/changes` page shows up to 500 changes from the last 7 days of runs, grouped by night (UTC dates; the 03:00 PT run falls on the same date in both zones). `/changes.atom` shows the newest 100. Place and business feeds carry the newest 50 of all time (the initial load excluded) and return 404 for an unknown id. Entry ids are `tag:` URIs on the observation id, so an entry never changes identity. Links are absolute on `https://citypulse.jasonharker.com`, because feed readers show entries out of context. An empty feed is dated by the last successful ingest.

- [ ] **Step 1: Write the failing tests**

`internal/web/feed_test.go`:

```go
package web

import (
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/store"
)

func TestNights(t *testing.T) {
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }
	ns := nights([]store.Change{
		{What: "new", At: at(19, 10)},
		{What: "withdrawn", At: at(19, 10)},
		{What: "updated", At: at(18, 23)},
		{What: "new", At: at(18, 10)},
	})
	if len(ns) != 2 {
		t.Fatalf("nights = %d, want 2", len(ns))
	}
	if n := ns[0]; !n.Date.Equal(at(19, 0)) || n.New != 1 || n.Withdrawn != 1 || len(n.Changes) != 2 {
		t.Errorf("first night = %+v", n)
	}
	if n := ns[1]; !n.Date.Equal(at(18, 0)) || n.New != 1 || n.Updated != 1 {
		t.Errorf("second night = %+v", n)
	}
}
```

In `TestSite`, add to the unit page wants:

```go
			`<link rel="alternate" type="application/atom+xml" href="/feed/place/3570%7CS%7CLAS%20VEGAS%7CB%2FC">`,
```

and these cases:

```go
		{"/changes", 200, []string{"What changed", "Updated", "JOE&#39;S BAR", `href="/record/snhd/1"`}},
		{"/changes.atom", 200, []string{`<feed xmlns="http://www.w3.org/2005/Atom">`, "Updated: JOE&#39;S BAR (Health inspection (SNHD))",
			"tag:citypulse.jasonharker.com,2026:updated/", "https://citypulse.jasonharker.com/record/snhd/1"}},
		{"/feed/place/" + url.PathEscape("3570|S|LAS VEGAS|B/C"), 200, []string{"<title>3570 S LAS VEGAS BLVD STE B/C · city-pulse</title>", "<entry>"}},
		{"/feed/business/" + url.PathEscape("3570|S|LAS VEGAS|B/C#snhd:2"), 200, []string{"<updated>"}},
		{"/feed/place/nowhere", 404, nil},
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/web/`
Expected: build failure, `undefined: nights`.

- [ ] **Step 3: Implement**

`internal/web/feed.go`:

```go
package web

import (
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

func (s *server) changes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cs, err := s.st.Changes(ctx, time.Now().AddDate(0, 0, -changeDays), maxPageChanges)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, view{Title: "What changed · city-pulse", Page: "changes", Feed: "/changes.atom",
		Nights: nights(cs), Truncated: len(cs) == maxPageChanges})
}

func (s *server) changesFeed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cs, err := s.st.Changes(ctx, time.Now().AddDate(0, 0, -changeDays), maxFeedEntries)
	s.atom(w, r, "Las Vegas place records: what changed", "/changes", "/changes.atom", cs, err)
}

func (s *server) placeFeed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.PathValue("id")
	addr, cs, err := s.st.PlaceChanges(ctx, id)
	s.atom(w, r, addr+" · city-pulse", placeURL(id), placeFeedURL(id), cs, err)
}

func (s *server) businessFeed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.PathValue("id")
	name, cs, err := s.st.BusinessChanges(ctx, id)
	s.atom(w, r, name+" · city-pulse", businessURL(id), businessFeedURL(id), cs, err)
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
	Summary string   `xml:"summary"`
}

// atom writes changes as an Atom feed; page is the HTML page it follows.
func (s *server) atom(w http.ResponseWriter, r *http.Request, title, page, self string, cs []store.Change, err error) {
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
	updated := s.lastIngest(r.Context())
	if len(cs) > 0 {
		updated = cs[0].At
	}
	f.Updated = updated.UTC().Format(time.RFC3339)
	for _, c := range cs {
		f.Entries = append(f.Entries, atomEntry{
			Title:   whatLabels[c.What] + ": " + cmpOr(c.Name, c.Address) + " (" + source(c.Source) + ")",
			ID:      fmt.Sprintf("tag:citypulse.jasonharker.com,2026:%s/%d", c.What, c.ObservationID),
			Updated: c.At.UTC().Format(time.RFC3339),
			Link:    atomLink{Href: siteURL + recordURL(c.Source, c.RecordID)},
			Summary: join(" · ", c.Address, c.Category, c.Status),
		})
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if _, err := w.Write([]byte(xml.Header)); err == nil {
		err = xml.NewEncoder(w).Encode(f)
	}
	if err != nil {
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
```

In `web.go`, add to the `FuncMap`:

```go
	"placeFeedURL":    placeFeedURL,
	"businessFeedURL": businessFeedURL,
	"what":            func(w string) string { return whatLabels[w] },
```

routes, after the record route:

```go
	mux.HandleFunc("GET /changes", s.changes)
	mux.HandleFunc("GET /changes.atom", s.changesFeed)
	mux.HandleFunc("GET /feed/place/{id...}", s.placeFeed)
	mux.HandleFunc("GET /feed/business/{id...}", s.businessFeed)
```

`view` fields, after `Record`:

```go
	Page      string // "changes" or "sources"
	Nights    []night
	Truncated bool   // Nights hold only the newest maxPageChanges changes
	Feed      string // the page's Atom feed, for autodiscovery
```

the place and business handlers set `Feed`:

```go
	s.render(w, r, view{Title: p.Address + " · city-pulse", Place: p, Feed: placeFeedURL(p.ID)})
```

```go
	s.render(w, r, view{Title: b.Name + " · city-pulse", Business: b, Feed: businessFeedURL(b.ID)})
```

and the dateline loop in `search` moves into a helper, which `atom` also uses:

```go
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
```

- [ ] **Step 4: Templates**

In `<head>`, after the icon link:

```html
{{- with .Feed}}
<link rel="alternate" type="application/atom+xml" href="{{.}}">
{{- end}}
```

In the dateline, after the `records through` part:

```html
 · <a href="/changes">What changed this week</a>
```

In the `<main>` switch, after the `.Record` branch:

```html
{{- else if eq .Page "changes"}}{{template "changes" .}}
```

In the place header's count line, append ` · <a href="{{placeFeedURL .ID}}">follow with a feed reader</a>`, and in the business header's line ` · <a href="{{businessFeedURL .ID}}">follow with a feed reader</a>`.

Append:

```html
{{define "changes"}}
<section class="results">
  <h1>What changed</h1>
  <p class="muted">Records the nightly runs found new, changed or no longer published over the last week{{if .Truncated}} (the newest 500){{end}}.
    <a href="/changes.atom">Follow with a feed reader</a>.</p>
  {{- if not .Nights}}<p class="muted">Nothing changed this week.</p>{{end}}
  {{- range .Nights}}
  <section>
    <h2>{{day .Date}} <span class="label">{{.New}} new · {{.Updated}} updated · {{.Withdrawn}} no longer published</span></h2>
    <ol class="timeline">
    {{- range .Changes}}
      <li><span class="label">{{what .What}}</span>
        <div><span class="label">{{source .Source}}</span>
          <p>{{or .Name .Address}}{{if or .Category .Status}}<br><span class="muted">{{.Category}}{{if and .Category .Status}} · {{end}}{{.Status}}</span>{{end}}</p>
          <p>{{if .BusinessID}}<a href="{{businessURL .BusinessID}}">the business</a> · {{end}}
          {{- if .PlaceID}}<a href="{{placeURL .PlaceID}}">the place</a> · {{end}}
          {{- /**/}}<a href="{{recordURL .Source .RecordID}}">the record</a></p></div></li>
    {{- end}}
    </ol>
  </section>
  {{- end}}
</section>
{{end}}
```

- [ ] **Step 5: Run the tests**

Run: `go test -count=1 ./internal/web/`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/web/feed.go internal/web/feed_test.go internal/web/web.go internal/web/page.html internal/web/web_test.go
git commit -m "feat(web): what-changed page and Atom feeds for the city, places and businesses"
```

---

### Task 7: Sources page

**Files:**
- Create: `internal/web/num_test.go`
- Modify: `internal/web/web.go`, `internal/web/page.html`, `internal/web/web_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/web/num_test.go`:

```go
package web

import "testing"

func TestNum(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1807596: "1,807,596", -1234: "-1,234"} {
		if got := num(n); got != want {
			t.Errorf("num(%d) = %q, want %q", n, got, want)
		}
	}
}
```

Add to `TestSite`:

```go
		{"/sources", 200, []string{"Health inspection (SNHD)", "March 2017", "3 rows"}},
```

Run: `go test -count=1 ./internal/web/`. Expected: build failure, `undefined: num`.

- [ ] **Step 2: Implement**

In `web.go`, after `sourceNames`:

```go
// sourceNotes tell readers what each source covers and what it lacks.
var sourceNotes = map[string]string{
	"snhd": "Inspections of restaurants and other food businesses by the Southern Nevada Health District, from 2005. " +
		"Inspections from March 2017 to September 2020 are missing from the district's published data.",
	"ccbiz": "Business licenses issued by Clark County, which covers the unincorporated valley, the Strip included.",
	"clvlicense": "Business licenses issued by the City of Las Vegas. The city publishes no issue dates, " +
		"so these licenses are dated by the night city-pulse first saw them.",
	"clvpermit": "Building permits issued by the City of Las Vegas: the city's archive through January 2026, then its current permits.",
}
```

FuncMap entries:

```go
	"sourceNote": func(s string) string { return sourceNotes[s] },
	"num":        num,
```

route (after the feed routes), `view` field (after `Truncated`), and handler (after `record`):

```go
	mux.HandleFunc("GET /sources", s.sources)
```

```go
	Sources   []store.SourceHealth
```

```go
func (s *server) sources(w http.ResponseWriter, r *http.Request) {
	hs, err := s.st.Health(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, view{Title: "The sources · city-pulse", Page: "sources", Sources: hs})
}
```

At the end of `web.go`:

```go
// num writes n with thousands separators: 1807596 -> "1,807,596".
func num(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
```

- [ ] **Step 3: Templates**

`<main>` switch, after the changes branch:

```html
{{- else if eq .Page "sources"}}{{template "sources" .Sources}}
```

Footer: after `each record is shown as published.` add `<a href="/sources">About the sources</a>.`

Append:

```html
{{define "sources"}}
<section class="results">
  <h1>The sources</h1>
  <p class="muted">city-pulse fetches each dataset whole every night. A record that changes gets a new version; one that disappears
    is marked no longer published, never deleted.</p>
  {{- range .}}
  <section>
    <h2>{{source .Source}}</h2>
    <p>{{sourceNote .Source}}</p>
    <p class="label">last updated {{with .LastSuccess}}{{day .}}{{else}}never{{end}}{{if not .OK}} · the latest run failed{{end}}
      · {{num .Seen}} rows · {{num .New}} new versions in the latest run · {{num .ParseErrors}} unreadable</p>
  </section>
  {{- end}}
</section>
{{end}}
```

- [ ] **Step 4: Run everything**

Run: `go vet ./... && go test -count=1 ./...`
Expected: every package `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/web/num_test.go internal/web/web.go internal/web/page.html internal/web/web_test.go
git commit -m "feat(web): sources page with freshness and known gaps"
```

---

### Task 8: Spec, as built

**Files:**
- Modify: `docs/superpowers/specs/2026-10-14-city-pulse-design.md`

- [ ] **Step 1: Scope**

In **Not in v1**, replace the two lines `- User accounts, watches, alerts.` and `- City-wide change feed.` with:

```markdown
- User accounts and email alerts. Following a place, a business or the city is an Atom feed.
- Ranking places by turnover. Measured: business counts surface registered-agent suites (977 undated City licenses at one), and restaurant-only succession surfaces ghost kitchens; real storefronts top out at 3 successions.
```

- [ ] **Step 2: Serve**

Replace the **Place page** and **Business page** bullets with:

```markdown
- **Place page** — the same map, inset at the place with its neighbours clickable; businesses here with spans, the building it is a unit of or the units it contains, a succession strip (the newest 30 *dated* businesses as bars on one time axis, oldest first, none with fewer than two; undated City licenses only span the night city-pulse first saw them), and the newest 300 records, each linking its record page.
- **Business page** — span, place, newest 300 records.
- **Record page** — `/record/{source}/{id}`: every stored version newest first, dated by the night it was first seen, with the payload fields that changed from the version before (the first version lists them all), and whether the source has stopped publishing it.
- **What changed** — `/changes`: up to 500 changes from the last 7 days of runs, by night; `/changes.atom` the newest 100; `/feed/place/{id}` and `/feed/business/{id}` the newest 50. A change is a version stored by a run after its source's first successful run (the initial load is not a change), new or updated by whether an earlier version exists, or a withdrawal. The window is in runs (`observations (source, first_run_id)`), because a new source's initial load is also recent. Entries link absolute URLs and have `tag:` ids on the observation id.
- **Sources** — `/sources`: each source's coverage, known gaps and latest run.
```

Replace `Measured on the full data: search 5–12 ms, the busiest place page (3,660 records) 65 ms.` with:

```markdown
Measured on the full data: search 5–12 ms, the busiest place page (3,660 records) 65 ms; `/changes` 55 ms, its feed 31 ms, a place or business feed ≤ 40 ms, a record page 2 ms.
```

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-10-14-city-pulse-design.md
git commit -m "docs(spec): change feed, record history, succession strip, sources as built"
```

---

### Task 9: Verify on production data

This repo's serve numbers come from a full copy of production. devdb ships only the server binaries, so restore through hippodamus's `pg_restore` over a reverse tunnel.

- [ ] **Step 1: Copy production into devdb's `citypulse` database** (about 30 s to dump 242 MB, 75 s to restore)

```bash
ssh hippodamus 'kubectl -n city-pulse exec deploy/postgres -- sh -c "pg_dump -U \$POSTGRES_USER -d \$POSTGRES_DB -Fc -Z1 --no-owner --no-acl"' > /tmp/prod.dump
ssh -R 55433:localhost:55432 hippodamus 'PGPASSWORD=postgres pg_restore -h localhost -p 55433 -U postgres -d citypulse --no-owner --no-acl' < /tmp/prod.dump
```

- [ ] **Step 2: Serve it (this applies migration 005) and time the new routes**

```bash
go build -o /tmp/citypulse ./cmd/citypulse
DATABASE_URL='postgres://postgres:postgres@localhost:55432/citypulse?sslmode=disable' /tmp/citypulse serve -addr 127.0.0.1:18080 &
e() { printf %s "$1" | jq -sRr @uri; }
t() { curl -s -o /dev/null -w "%{http_code} %{time_total}s  $1\n" "localhost:18080$1"; }
t /changes; t /changes.atom; t /sources
t "/place/$(e '3799|S|LAS VEGAS|')"; t "/place/$(e '2490|E|DESERT INN|')"; t "/feed/place/$(e '3799|S|LAS VEGAS|')"
curl -s localhost:18080/changes.atom | xmllint --noout - && echo atom-ok
```

Expected (2026-10-19 copy): every status `200`. `/changes` ≈ 55 ms, `/changes.atom` ≈ 31 ms, places ≤ 45 ms, feeds ≤ 40 ms, `atom-ok`. 2490 E Desert Inn Rd shows a four-bar strip (Kentucky Fried Chicken 2005–2010, Ni Hao 2014, Cilantros 2015, Frijoles & Frescas 2017–2026). 10750 W Charleston Blvd #180 shows no strip.

- [ ] **Step 3: Look at it**

Screenshot a place, `/changes`, a record with two versions and `/sources` at 1280 and 390 px wide (`chromium --headless=new --window-size=1280,1100 --screenshot=out.png URL`), and once with `--force-dark-mode --blink-settings=preferredColorScheme=0`. No horizontal scroll at 390 px; names in the strip are not truncated.

---

## Deploy

Merging to `main` is the deploy. Jenkins builds, and the next `serve` start applies migration 005, adding two indexes to `observations` in under a second at today's size. No manifest change, no new secrets, no new egress. The feeds are public under the existing 60/min rate limit.
