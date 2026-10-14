# city-pulse Ingest Implementation Plan (Plan 1 of 4)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `citypulse ingest` fetches full snapshots of four Las Vegas civic datasets and stores every record version in Postgres, with change detection, withdrawal and kept parse errors.

**Architecture:** One Go module, one binary. Each dataset is a `source.Source` (fetch a snapshot, parse a record into a typed `Observation`). `internal/ingest` runs sources independently, batches upserts keyed on `(source, record_id, content hash)`, records each run, and withdraws records a successful run no longer sees — unless the row count collapsed. Spec: `docs/superpowers/specs/2026-10-14-city-pulse-design.md`.

**Tech Stack:** Go 1.27, Postgres 16, `github.com/jackc/pgx/v5` v5.11.0. Dev/test database: `tools/devdb` (embedded Postgres in its own module; no root or Docker).

**Provenance:** every code block below was compiled and tested before this plan was written: unit tests pass, the Postgres-backed tests pass against a real Postgres 16, the SNHD connector was run against the real nightly zip, and a live `citypulse ingest` of all four sources completed on 2026-10-14 (2,379,124 rows seen, 1,105,611 versions stored), with an immediate second run storing 0 new versions.

---

## File structure

```
go.mod, go.sum
.gitignore
cmd/citypulse/main.go                 subcommands: migrate, ingest
internal/source/source.go             Record, Observation, Source interface
internal/source/fields.go             payload decoding and value helpers
internal/source/fields_test.go
internal/source/arcgis/arcgis.go      FeatureServer client, keyset paging, retries
internal/source/arcgis/arcgis_test.go
internal/source/ccbiz/                Clark County business licenses
internal/source/clvlicense/           City of LV business licenses
internal/source/clvpermit/            City of LV building permits (current + archive layers)
internal/source/snhd/                 SNHD restaurant inspections (zip of ';' tables)
internal/store/migrations/001_observations.sql
internal/store/store.go               Open, Migrate
internal/store/observations.go        runs, upsert, parse errors, withdrawal
internal/store/store_test.go
internal/store/storetest/storetest.go migrated store in a throwaway schema
internal/ingest/ingest.go             runs sources, batching, withdrawal guard
internal/ingest/ingest_test.go
tools/devdb/                          separate module: local Postgres 16
```

Packages depend downward only: `cmd` → `ingest` → `store`, `source/*` → `source`.

---

### Task 1: Module skeleton

**Files:**
- Create: `go.mod`, `.gitignore`

- [ ] **Step 1: Create the module**

Run from the repo root:

```bash
go mod init github.com/jasonharker/city-pulse
```

- [ ] **Step 2: Write `.gitignore`**

```gitignore
/citypulse
/.devdb/
```

- [ ] **Step 3: Commit**

```bash
git add go.mod .gitignore
git commit -m "chore: initialize Go module"
```

---

### Task 2: Source contract and value helpers

Every connector produces `Record`s (the row as published, canonical JSON) and parses them into `Observation`s. Canonical JSON matters: `encoding/json` sorts map keys, so an unchanged row always hashes the same, and the hash is how change detection works.

**Files:**
- Create: `internal/source/source.go`, `internal/source/fields.go`
- Test: `internal/source/fields_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/source/fields_test.go`:

```go
package source

import (
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func TestRecordHashIsStableAcrossKeyOrder(t *testing.T) {
	a, err := NewRecord("1", map[string]any{"b": 2, "a": "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRecord("1", map[string]any{"a": "x", "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() != b.Hash() {
		t.Fatalf("hash differs: %s vs %s", a.Hash(), b.Hash())
	}
	if string(a.Payload) != `{"a":"x","b":2}` {
		t.Fatalf("payload = %s", a.Payload)
	}
}

func TestDecodeKeepsNumbersExact(t *testing.T) {
	r, _ := NewRecord("1", map[string]any{"id": 13816415069, "name": "  Joe's  ", "nothing": nil})
	f, err := Decode(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Str("id"); got != "13816415069" {
		t.Errorf("Str(id) = %q", got)
	}
	if got := f.Str("name"); got != "Joe's" {
		t.Errorf("Str(name) = %q", got)
	}
	if got := f.Str("nothing"); got != "" {
		t.Errorf("Str(nothing) = %q", got)
	}
	if got := f.Float("name"); got != nil {
		t.Errorf("Float(name) = %v, want nil", *got)
	}
}

func TestDate(t *testing.T) {
	cases := []struct {
		layout, in string
		want       string // "" means nil
	}{
		{"20060102", "20220214", "2022-02-14"},
		{"2006-01-02 15:04:05", "2023-08-16 00:00:00", "2023-08-16"},
		{"02-Jan-06", "05-JAN-26", "2026-01-05"}, // month match is case-insensitive
		{"02-Jan-06", "15-MAR-85", "1985-03-15"}, // two-digit years >= 69 are 19xx
		{"20060102", "", ""},
		{"20060102", "garbage", ""},
	}
	for _, c := range cases {
		got := Date(c.layout, c.in)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("Date(%q, %q) = %v, want nil", c.layout, c.in, got)
		case c.want != "" && (got == nil || got.Format(time.DateOnly) != c.want):
			t.Errorf("Date(%q, %q) = %v, want %s", c.layout, c.in, got, c.want)
		}
	}
}

func TestZip5DigitsJoinBeforeClean(t *testing.T) {
	if got := Zip5("89101-5600"); got != "89101" {
		t.Errorf("Zip5 = %q", got)
	}
	if got := Zip5("AS VEGAS"); got != "" {
		t.Errorf("Zip5 = %q", got)
	}
	if got := Digits("178-06-101-019"); got != "17806101019" {
		t.Errorf("Digits = %q", got)
	}
	if got := Join(" 4161 S Eastern Ave ", "", "#A-3"); got != "4161 S Eastern Ave #A-3" {
		t.Errorf("Join = %q", got)
	}
	if got := Before("1001277.LIQ-107", "."); got != "1001277" {
		t.Errorf("Before = %q", got)
	}
	if got := Clean("  9110 Las Vegas  Blvd S \t"); got != "9110 Las Vegas Blvd S" {
		t.Errorf("Clean = %q", got)
	}
}

func TestLatLon(t *testing.T) {
	lat, lon := LatLon(f64(36.16939030), f64(-115.14308440))
	if lat == nil || *lat != 36.16939030 || *lon != -115.14308440 {
		t.Errorf("valid pair changed: %v %v", lat, lon)
	}
	lat, lon = LatLon(f64(-115.14308440), f64(36.16939030)) // SNHD order
	if lat == nil || *lat != 36.16939030 || *lon != -115.14308440 {
		t.Errorf("swapped pair not repaired: %v %v", lat, lon)
	}
	if lat, lon = LatLon(f64(0), f64(0)); lat != nil || lon != nil {
		t.Errorf("(0,0) accepted")
	}
	if lat, lon = LatLon(nil, f64(1)); lat != nil || lon != nil {
		t.Errorf("nil accepted")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/`
Expected: build failure, `undefined: NewRecord` (and the other helpers).

- [ ] **Step 3: Write the contract**

`internal/source/source.go`:

```go
// Package source defines the contract every civic dataset connector implements.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// UserAgent identifies city-pulse to the agencies it fetches from.
const UserAgent = "city-pulse/0.1 (+https://citypulse.jasonharker.com)"

// Record is one row exactly as a source published it.
type Record struct {
	ID      string          // the source's own identifier for the row
	Payload json.RawMessage // canonical JSON: object keys sorted
}

// NewRecord marshals fields to canonical JSON. encoding/json sorts map keys,
// so an unchanged row always yields the same bytes and therefore the same hash.
func NewRecord(id string, fields map[string]any) (Record, error) {
	b, err := json.Marshal(fields)
	if err != nil {
		return Record{}, err
	}
	return Record{ID: id, Payload: b}, nil
}

// Hash identifies one version of a record.
func (r Record) Hash() string {
	sum := sha256.Sum256(r.Payload)
	return hex.EncodeToString(sum[:])
}

// Observation is the parsed, typed view of one record version.
// The ingest runner fills Source, RecordID, Hash and Payload; Parse fills the rest.
type Observation struct {
	Source   string
	RecordID string
	Hash     string
	Payload  json.RawMessage

	Kind        string // "license", "inspection" or "permit"
	Name        string
	Address     string // street line as published, not normalized
	City        string
	Zip         string // 5 digits or ""
	Parcel      string // Clark County APN, digits only, or ""
	BusinessKey string // source-scoped id naming one operator across records
	Category    string
	Status      string
	Lat, Lon    *float64
	StartDate   *time.Time
	EndDate     *time.Time
}

// Source is one civic dataset.
type Source interface {
	Name() string
	// Fetch streams a full snapshot of the dataset to emit.
	Fetch(ctx context.Context, emit func(Record) error) error
	// Parse maps one record to an observation. It must not do I/O.
	Parse(Record) (Observation, error)
}
```

- [ ] **Step 4: Write the helpers**

`internal/source/fields.go`:

```go
package source

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Fields is a decoded record payload.
type Fields map[string]any

// Decode unmarshals a record payload, keeping numbers as json.Number so
// identifiers and coordinates survive without float rounding.
func Decode(r Record) (Fields, error) {
	d := json.NewDecoder(bytes.NewReader(r.Payload))
	d.UseNumber()
	var f Fields
	if err := d.Decode(&f); err != nil {
		return nil, err
	}
	return f, nil
}

// Str returns a field as trimmed text. Missing and null fields are "".
func (f Fields) Str(key string) string {
	switch v := f[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

// Float returns a numeric field, or nil if it is missing or not a number.
func (f Fields) Float(key string) *float64 {
	s := f.Str(key)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// Date parses s with layout. Empty or unparseable input returns nil:
// a missing date is normal in this data, not an error.
func Date(layout, s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return nil
	}
	return &t
}

// Zip5 returns the first five characters of a ZIP if they are digits, else "".
func Zip5(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 5 {
		return ""
	}
	for _, c := range s[:5] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return s[:5]
}

// Digits keeps only the ASCII digits of s.
func Digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r < '0' || r > '9' {
			return -1
		}
		return r
	}, s)
}

// Join joins the non-empty parts of ps with single spaces.
func Join(ps ...string) string {
	var out []string
	for _, p := range ps {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// Before returns s up to the first sep, or all of s if sep is absent.
func Before(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i]
	}
	return s
}

// Clean collapses whitespace runs to one space and trims.
func Clean(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// Southern Nevada, generously. Anything outside is a bad or swapped coordinate.
const (
	minLat, maxLat = 34.5, 37.5
	minLon, maxLon = -116.5, -113.5
)

// LatLon validates a claimed (lat, lon) pair for Southern Nevada. A pair that
// is valid only when swapped is returned swapped (SNHD publishes them reversed).
// Anything else, including (0, 0), returns nil, nil.
func LatLon(lat, lon *float64) (*float64, *float64) {
	if lat == nil || lon == nil {
		return nil, nil
	}
	in := func(a, b float64) bool { return a >= minLat && a <= maxLat && b >= minLon && b <= maxLon }
	switch {
	case in(*lat, *lon):
		return lat, lon
	case in(*lon, *lat):
		return lon, lat
	}
	return nil, nil
}
```

Notes for the implementer:
- `Date` returns nil rather than an error: a missing or malformed date is routine in this data and must not fail the row.
- Go's `time.Parse` matches month names case-insensitively (`JAN` parses with layout `Jan`) and maps two-digit years 69–99 to 19xx, 00–68 to 20xx. The tests pin both.
- `LatLon` swaps a reversed pair because SNHD publishes longitude in its `latitude` column.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/source/`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal/source/source.go internal/source/fields.go internal/source/fields_test.go
git commit -m "feat(source): record/observation contract and value helpers"
```

---

### Task 3: ArcGIS FeatureServer client

Three of the four sources are ArcGIS layers. Paging is keyset (`OID > last`), not `resultOffset`: offsets get slow on the 1.8M-row permit archive and break if rows shift. Geometry is requested as WGS84 (`outSR=4326`) because the layers are stored in Nevada State Plane feet. The object id is dropped from what gets hashed, because a republish can renumber it.

**Files:**
- Create: `internal/source/arcgis/arcgis.go`
- Test: `internal/source/arcgis/arcgis_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/source/arcgis/arcgis_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/arcgis/`
Expected: build failure, `undefined: Layer`.

- [ ] **Step 3: Write the client**

`internal/source/arcgis/arcgis.go`:

```go
// Package arcgis reads every feature of an ArcGIS FeatureServer layer.
package arcgis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
)

// Layer is one FeatureServer layer, e.g. ".../FeatureServer/0".
type Layer struct {
	URL      string
	OIDField string // object-id field: "OBJECTID" or "ObjectId" depending on the service
	PageSize int    // a hint; the server may return fewer
	Where    string // optional SQL filter; "" means every row
	HTTP     *http.Client
}

// Feature is one row: attributes plus a WGS84 point if the layer has geometry.
type Feature struct {
	Attributes map[string]any // numbers are json.Number
	Lat, Lon   *float64
}

// Fields returns the attributes minus the object id, which a republish can
// renumber, plus the point as "_lat"/"_lon". This is what sources hash.
func (f Feature) Fields(oidField string) map[string]any {
	out := make(map[string]any, len(f.Attributes)+2)
	for k, v := range f.Attributes {
		if k != oidField {
			out[k] = v
		}
	}
	if f.Lat != nil {
		out["_lat"], out["_lon"] = *f.Lat, *f.Lon
	}
	return out
}

// Each calls fn for every feature in object-id order. It pages by keyset
// (OID > last seen) rather than offset, so deep pages stay cheap.
func (l Layer) Each(ctx context.Context, fn func(Feature) error) error {
	last := int64(-1)
	for {
		page, err := l.page(ctx, last)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, f := range page {
			if err := fn(f); err != nil {
				return err
			}
		}
		n, ok := page[len(page)-1].Attributes[l.OIDField].(json.Number)
		if !ok {
			return fmt.Errorf("arcgis %s: feature has no %s", l.URL, l.OIDField)
		}
		if last, err = n.Int64(); err != nil {
			return fmt.Errorf("arcgis %s: bad %s %q", l.URL, l.OIDField, n)
		}
	}
}

type response struct {
	Features []struct {
		Attributes map[string]any `json:"attributes"`
		Geometry   *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"geometry"`
	} `json:"features"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (l Layer) page(ctx context.Context, after int64) ([]Feature, error) {
	where := fmt.Sprintf("%s > %d", l.OIDField, after)
	if l.Where != "" {
		where = "(" + l.Where + ") AND " + where
	}
	q := url.Values{
		"where":             {where},
		"outFields":         {"*"},
		"orderByFields":     {l.OIDField},
		"resultRecordCount": {strconv.Itoa(l.PageSize)},
		"returnGeometry":    {"true"},
		"outSR":             {"4326"},
		"f":                 {"json"},
	}
	var resp response
	if err := l.get(ctx, l.URL+"/query?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	out := make([]Feature, len(resp.Features))
	for i, f := range resp.Features {
		out[i].Attributes = f.Attributes
		if f.Geometry != nil {
			lat, lon := f.Geometry.Y, f.Geometry.X
			out[i].Lat, out[i].Lon = &lat, &lon
		}
	}
	return out, nil
}

// get fetches and decodes one page, retrying transient failures.
// ArcGIS reports many errors as HTTP 200 with an "error" body, so those retry too.
func (l Layer) get(ctx context.Context, u string, into *response) error {
	var err error
	for attempt, wait := 0, time.Second; attempt < 3; attempt, wait = attempt+1, wait*2 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		if err = l.getOnce(ctx, u, into); err == nil {
			return nil
		}
	}
	return err
}

func (l Layer) getOnce(ctx context.Context, u string, into *response) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", source.UserAgent)
	res, err := l.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, res.Body)
		return fmt.Errorf("arcgis %s: HTTP %d", l.URL, res.StatusCode)
	}
	*into = response{}
	d := json.NewDecoder(res.Body)
	d.UseNumber()
	if err := d.Decode(into); err != nil {
		return fmt.Errorf("arcgis %s: decode: %w", l.URL, err)
	}
	if into.Error != nil {
		return fmt.Errorf("arcgis %s: error %d: %s", l.URL, into.Error.Code, into.Error.Message)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/arcgis/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/source/arcgis
git commit -m "feat(arcgis): FeatureServer client with keyset paging and retries"
```

---

### Task 4: Clark County business licenses (`ccbiz`)

Unincorporated Clark County, which includes the Strip. 90,019 rows; credential numbers are unique, and the part before the `.` is shared by every license one business holds, which becomes the business key. Dates are `YYYYMMDD`.

**Files:**
- Create: `internal/source/ccbiz/ccbiz.go`
- Test: `internal/source/ccbiz/ccbiz_test.go`

- [ ] **Step 1: Write the failing test**

`internal/source/ccbiz/ccbiz_test.go` (the fixture is a real row):

```go
package ccbiz

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
)

// A real row (OBJECTID 2), as the arcgis client hands it over: OID removed, point added.
const row = `{"USER_newCredentialIdnt":0,"USER_DbaAddressIdnt":null,"USER_addressIdnt":null,
"USER_FullCredentialNumber":"1002273.545","USER_MjblIdNum":null,"USER_attentionName":" ",
"USER_LocationName":"Accudata","USER_attentionLine2":null,"USER_unitLine":null,
"USER_AddressLine1":"3486 Happy Ln","USER_City":"Las Vegas","USER_State":"NV","USER_ZipCode":"89120",
"USER_ProvincePostalCode":"","USER_phoneNumber":null,"USER_CredentialDescription":"Miscellaneous Service",
"USER_StatusDescription":"License Revoked","USER_InitialLicenseIssuedDate":"19930727",
"USER_OutOfBusinessDateActual":"20220214","USER_LocationAddress":"Accudata\r\n3486 Happy Ln\r\nLas Vegas, NV 89120",
"USER_ParcelNumber":"17806101019","USER_CommissionerDistrict":"G","USER_LandUse":"RN",
"_lat":36.1001,"_lon":-115.1002}`

func TestParse(t *testing.T) {
	obs, err := (&Source{}).Parse(source.Record{ID: "1002273.545", Payload: json.RawMessage(row)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Kind": "license", "Name": "Accudata", "Address": "3486 Happy Ln", "City": "Las Vegas",
		"Zip": "89120", "Parcel": "17806101019", "BusinessKey": "cc:1002273",
		"Category": "Miscellaneous Service", "Status": "License Revoked",
	}
	got := map[string]string{
		"Kind": obs.Kind, "Name": obs.Name, "Address": obs.Address, "City": obs.City,
		"Zip": obs.Zip, "Parcel": obs.Parcel, "BusinessKey": obs.BusinessKey,
		"Category": obs.Category, "Status": obs.Status,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if obs.StartDate == nil || obs.StartDate.Format(time.DateOnly) != "1993-07-27" {
		t.Errorf("StartDate = %v", obs.StartDate)
	}
	if obs.EndDate == nil || obs.EndDate.Format(time.DateOnly) != "2022-02-14" {
		t.Errorf("EndDate = %v", obs.EndDate)
	}
	if obs.Lat == nil || *obs.Lat != 36.1001 || *obs.Lon != -115.1002 {
		t.Errorf("point = %v %v", obs.Lat, obs.Lon)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ccbiz/`
Expected: build failure, `undefined: Source`.

- [ ] **Step 3: Write the source**

`internal/source/ccbiz/ccbiz.go`:

```go
// Package ccbiz reads Clark County business licenses (unincorporated county, incl. the Strip).
package ccbiz

import (
	"context"
	"net/http"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

const layerURL = "https://maps.clarkcountynv.gov/arcgis/rest/services/BusinessLicense/BusinessLicenseData/FeatureServer/1"

type Source struct{ Layer arcgis.Layer }

func New(c *http.Client) *Source {
	return &Source{Layer: arcgis.Layer{URL: layerURL, OIDField: "OBJECTID", PageSize: 5000, HTTP: c}}
}

func (*Source) Name() string { return "ccbiz" }

func (s *Source) Fetch(ctx context.Context, emit func(source.Record) error) error {
	return s.Layer.Each(ctx, func(f arcgis.Feature) error {
		fields := f.Fields(s.Layer.OIDField)
		id, _ := fields["USER_FullCredentialNumber"].(string)
		r, err := source.NewRecord(id, fields)
		if err != nil {
			return err
		}
		return emit(r)
	})
}

func (*Source) Parse(r source.Record) (source.Observation, error) {
	f, err := source.Decode(r)
	if err != nil {
		return source.Observation{}, err
	}
	lat, lon := source.LatLon(f.Float("_lat"), f.Float("_lon"))
	return source.Observation{
		Kind:    "license",
		Name:    source.Clean(f.Str("USER_LocationName")),
		Address: source.Clean(source.Join(f.Str("USER_AddressLine1"), f.Str("USER_unitLine"))),
		City:    f.Str("USER_City"),
		Zip:     source.Zip5(f.Str("USER_ZipCode")),
		Parcel:  source.Digits(f.Str("USER_ParcelNumber")),
		// "1001277.LIQ-107": the part before "." is shared by all of one business's licenses.
		BusinessKey: "cc:" + source.Before(r.ID, "."),
		Category:    f.Str("USER_CredentialDescription"),
		Status:      f.Str("USER_StatusDescription"),
		Lat:         lat,
		Lon:         lon,
		StartDate:   source.Date("20060102", f.Str("USER_InitialLicenseIssuedDate")),
		EndDate:     source.Date("20060102", f.Str("USER_OutOfBusinessDateActual")),
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ccbiz/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/source/ccbiz
git commit -m "feat(ccbiz): Clark County business licenses"
```

---

### Task 5: City of Las Vegas business licenses (`clvlicense`)

214,049 rows, but license numbers are not unique: the city publishes one row per owner or officer (a three-member LLC is three rows; 43,902 licenses have several). `Fetch` folds rows by license number into one record with a sorted `owners` list, which buffers the layer in memory (~150k licenses). 101 rows have no license number and become parse errors. The layer has no geometry and no issue date, so these observations carry neither; the timeline dates them by when city-pulse first saw them (`first_seen`).

**Files:**
- Create: `internal/source/clvlicense/clvlicense.go`
- Test: `internal/source/clvlicense/clvlicense_test.go`

- [ ] **Step 1: Write the failing test**

`internal/source/clvlicense/clvlicense_test.go`:

```go
package clvlicense

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

const row = `{"Business_Name":"ADCELO","Type_of_Business":"General Retail Sales","Address":"4637 Michillinda Ln",
"Zip_Code":"89121","Phone":"(702)426-0533","License__":"G69-06397","Status":"Closed","CLV":null,"NLV":null,
"HEN":null,"CC":null,"owners":["ADCELLO LLC - LLC"],"Within_City_Limits":"Outside"}`

func TestParse(t *testing.T) {
	obs, err := (&Source{}).Parse(source.Record{ID: "G69-06397", Payload: json.RawMessage(row)})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Kind != "license" || obs.Name != "ADCELO" || obs.Address != "4637 Michillinda Ln" ||
		obs.Zip != "89121" || obs.BusinessKey != "clv:G69-06397" || obs.Category != "General Retail Sales" ||
		obs.Status != "Closed" {
		t.Errorf("obs = %+v", obs)
	}
	if obs.Lat != nil || obs.StartDate != nil {
		t.Errorf("layer has no point or date, got %v %v", obs.Lat, obs.StartDate)
	}
}

// Real shape: one row per owner, plus a row with no license number.
func TestFetchFoldsOwnerRows(t *testing.T) {
	rows := []map[string]any{
		{"ObjectId": 1, "License__": "3000000025", "Business_Name": "CADIZ CONSTRUCTION LLC", "Owner_and_Owner_Title": "SHIELDS, SEAN - Member"},
		{"ObjectId": 2, "License__": "3000000025", "Business_Name": "CADIZ CONSTRUCTION LLC", "Owner_and_Owner_Title": "FRANCIS, LORRAINE - Member"},
		{"ObjectId": 3, "License__": "3000000025", "Business_Name": "CADIZ CONSTRUCTION LLC", "Owner_and_Owner_Title": "SHIELDS, SEAN - Member"},
		{"ObjectId": 4, "License__": "G69-06397", "Business_Name": "ADCELO", "Owner_and_Owner_Title": nil},
		{"ObjectId": 5, "License__": nil, "Business_Name": "ACORN STAIRLIFTS"},
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		feats := []map[string]any{}
		if calls == 1 {
			for _, a := range rows {
				feats = append(feats, map[string]any{"attributes": a})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"features": feats})
	}))
	defer srv.Close()
	s := &Source{Layer: arcgis.Layer{URL: srv.URL, OIDField: "ObjectId", PageSize: 10, HTTP: srv.Client()}}

	got := map[string]string{}
	err := s.Fetch(context.Background(), func(r source.Record) error {
		f, _ := source.Decode(r)
		owners, _ := json.Marshal(f["owners"])
		got[r.ID] = string(owners)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"3000000025": `["FRANCIS, LORRAINE - Member","SHIELDS, SEAN - Member"]`,
		"G69-06397":  `[]`,
		"":           `null`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%q owners = %s, want %s", id, got[id], w)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/clvlicense/`
Expected: build failure, `undefined: Source`.

- [ ] **Step 3: Write the source**

`internal/source/clvlicense/clvlicense.go`:

```go
// Package clvlicense reads City of Las Vegas business licenses.
// The layer has no geometry and no issue date.
package clvlicense

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

const layerURL = "https://services1.arcgis.com/F1v0ufATbBQScMtY/arcgis/rest/services/Business_Licenses_OpenData/FeatureServer/0"

const ownerField = "Owner_and_Owner_Title"

type Source struct{ Layer arcgis.Layer }

func New(c *http.Client) *Source {
	return &Source{Layer: arcgis.Layer{URL: layerURL, OIDField: "ObjectId", PageSize: 1000, HTTP: c}}
}

func (*Source) Name() string { return "clvlicense" }

// license is one license number's rows folded together.
type license struct {
	fields map[string]any
	owners []string
}

// Fetch emits one record per license number. The city publishes one row per
// owner or officer (a three-member LLC is three rows), so rows are grouped
// and the owners collected into a sorted "owners" list. This buffers the
// layer in memory: ~150k licenses.
// ponytail: buffers ~200k rows; stream by license if the layer grows 10x.
func (s *Source) Fetch(ctx context.Context, emit func(source.Record) error) error {
	byID := make(map[string]*license)
	var order []string
	err := s.Layer.Each(ctx, func(f arcgis.Feature) error {
		fields := f.Fields(s.Layer.OIDField)
		owner, _ := fields[ownerField].(string)
		delete(fields, ownerField)
		id, _ := fields["License__"].(string)
		if id == "" {
			// No license number: emit as-is so the runner keeps it as a parse error.
			r, err := source.NewRecord("", fields)
			if err != nil {
				return err
			}
			return emit(r)
		}
		l, ok := byID[id]
		if !ok {
			l = &license{fields: fields, owners: []string{}}
			byID[id] = l
			order = append(order, id)
		}
		if owner = strings.TrimSpace(owner); owner != "" {
			l.owners = append(l.owners, owner)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range order {
		l := byID[id]
		slices.Sort(l.owners)
		l.fields["owners"] = slices.Compact(l.owners)
		r, err := source.NewRecord(id, l.fields)
		if err != nil {
			return err
		}
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

func (*Source) Parse(r source.Record) (source.Observation, error) {
	f, err := source.Decode(r)
	if err != nil {
		return source.Observation{}, err
	}
	return source.Observation{
		Kind:        "license",
		Name:        source.Clean(f.Str("Business_Name")),
		Address:     source.Clean(f.Str("Address")),
		Zip:         source.Zip5(f.Str("Zip_Code")),
		BusinessKey: "clv:" + r.ID,
		Category:    f.Str("Type_of_Business"),
		Status:      f.Str("Status"),
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/clvlicense/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/source/clvlicense
git commit -m "feat(clvlicense): City of Las Vegas business licenses"
```

---

### Task 6: City of Las Vegas building permits (`clvpermit`)

Two layers with one permit-number namespace: a current table (26,000 rows, epoch-millisecond dates, address in parts, no geometry) and an archive (1,807,326 non-blank rows ending January 2026, `DD-MON-YY` dates, geometry). The current layer is read first and wins on overlap; `_layer` in the payload tells `Parse` which shape it has. Permits have no business name: they attach to places, not businesses.

**Files:**
- Create: `internal/source/clvpermit/clvpermit.go`
- Test: `internal/source/clvpermit/clvpermit_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/source/clvpermit/clvpermit_test.go`:

```go
package clvpermit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

// Real rows from each layer, as the arcgis client hands them over.
const currentRow = `{"APNO":"R23-17258","APTYPE":"Res","WORKTYPE":"OTC","BLDGAPPLSTATUS":"Closed",
"ISSDTTM":1698105600000,"STNO":2440,"PREDIR":null,"STNAME":"SAGE POINTE","SUFFIX":"CIR","POSTDIR":null,
"STSUB":null,"PRCLID":13816415069,"ZIP":"89128","_layer":"current"}`

const archiveRow = `{"Status":"M","APNO":"R26-00072","APNAME":"KALANI WILBURN","APTYPE":"Res",
"APDESC":"Residential Building Permit","PROPERTY":"2808 SING SONG WAY  ","PRCLID":"13920415030",
"STAT":"Closed","ISSUE_DT":"05-JAN-26","COMP_D":null,"SUITE":null,"ADDR":"2808 SING SONG WAY",
"_lat":36.19132518828868,"_lon":-115.1798476050579,"_layer":"archive"}`

func TestParseCurrent(t *testing.T) {
	obs, err := (&Source{}).Parse(source.Record{ID: "R23-17258", Payload: json.RawMessage(currentRow)})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Kind != "permit" || obs.Address != "2440 SAGE POINTE CIR" || obs.Parcel != "13816415069" ||
		obs.Zip != "89128" || obs.Category != "Res OTC" || obs.Status != "Closed" || obs.Name != "" {
		t.Errorf("obs = %+v", obs)
	}
	if obs.StartDate == nil || obs.StartDate.Format(time.DateOnly) != "2023-10-24" {
		t.Errorf("StartDate = %v", obs.StartDate)
	}
}

func TestParseArchive(t *testing.T) {
	obs, err := (&Source{}).Parse(source.Record{ID: "R26-00072", Payload: json.RawMessage(archiveRow)})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Address != "2808 SING SONG WAY" || obs.Parcel != "13920415030" ||
		obs.Category != "Residential Building Permit" || obs.EndDate != nil {
		t.Errorf("obs = %+v", obs)
	}
	if obs.StartDate == nil || obs.StartDate.Format(time.DateOnly) != "2026-01-05" {
		t.Errorf("StartDate = %v", obs.StartDate)
	}
	if obs.Lat == nil || *obs.Lat != 36.19132518828868 {
		t.Errorf("Lat = %v", obs.Lat)
	}
}

func TestParseRejectsUnknownLayer(t *testing.T) {
	if _, err := (&Source{}).Parse(source.Record{ID: "x", Payload: json.RawMessage(`{"_layer":"?"}`)}); err == nil {
		t.Fatal("want error")
	}
}

// serve answers the first query with feats and every later one with no features.
func serve(t *testing.T, feats ...map[string]any) arcgis.Layer {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		out := []map[string]any{}
		if calls == 1 {
			for _, a := range feats {
				out = append(out, map[string]any{"attributes": a})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"features": out})
	}))
	t.Cleanup(srv.Close)
	return arcgis.Layer{URL: srv.URL, OIDField: "OBJECTID", PageSize: 10, HTTP: srv.Client()}
}

func TestFetchPrefersCurrentOverArchive(t *testing.T) {
	s := &Source{
		Current: serve(t, map[string]any{"OBJECTID": 1, "APNO": "A-1"}),
		Archive: serve(t, map[string]any{"OBJECTID": 1, "APNO": "A-1"}, map[string]any{"OBJECTID": 2, "APNO": "A-2"}),
	}
	var got []string
	err := s.Fetch(context.Background(), func(r source.Record) error {
		f, _ := source.Decode(r)
		got = append(got, r.ID+"/"+f.Str("_layer"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "A-1/current,A-2/archive" {
		t.Errorf("got %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/clvpermit/`
Expected: build failure, `undefined: Source`.

- [ ] **Step 3: Write the source**

`internal/source/clvpermit/clvpermit.go`:

```go
// Package clvpermit reads City of Las Vegas building permits. The city publishes
// them as two layers: a small current layer and a large archive that stops in
// January 2026. Both use the same permit number (APNO); the current row wins.
package clvpermit

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/arcgis"
)

const (
	org        = "https://services1.arcgis.com/F1v0ufATbBQScMtY/arcgis/rest/services"
	currentURL = org + "/OpenData_Building_Permits_/FeatureServer/0"
	archiveURL = org + "/Bldg_Permits/FeatureServer/379"
)

type Source struct{ Current, Archive arcgis.Layer }

func New(c *http.Client) *Source {
	return &Source{
		Current: arcgis.Layer{URL: currentURL, OIDField: "ObjectId", PageSize: 1000, HTTP: c},
		// ~72k archive rows are blank placeholders with no permit number.
		Archive: arcgis.Layer{URL: archiveURL, OIDField: "OBJECTID", PageSize: 2000, Where: "APNO <> ''", HTTP: c},
	}
}

func (*Source) Name() string { return "clvpermit" }

func (s *Source) Fetch(ctx context.Context, emit func(source.Record) error) error {
	seen := make(map[string]bool)
	err := s.Current.Each(ctx, func(f arcgis.Feature) error {
		fields := f.Fields(s.Current.OIDField)
		id, _ := fields["APNO"].(string)
		seen[id] = true
		return emitLayer(emit, id, "current", fields)
	})
	if err != nil {
		return fmt.Errorf("current layer: %w", err)
	}
	err = s.Archive.Each(ctx, func(f arcgis.Feature) error {
		fields := f.Fields(s.Archive.OIDField)
		id, _ := fields["APNO"].(string)
		if seen[id] {
			return nil
		}
		return emitLayer(emit, id, "archive", fields)
	})
	if err != nil {
		return fmt.Errorf("archive layer: %w", err)
	}
	return nil
}

func emitLayer(emit func(source.Record) error, id, layer string, fields map[string]any) error {
	fields["_layer"] = layer
	r, err := source.NewRecord(id, fields)
	if err != nil {
		return err
	}
	return emit(r)
}

func (*Source) Parse(r source.Record) (source.Observation, error) {
	f, err := source.Decode(r)
	if err != nil {
		return source.Observation{}, err
	}
	obs := source.Observation{Kind: "permit", City: "Las Vegas", Parcel: source.Digits(f.Str("PRCLID"))}
	switch layer := f.Str("_layer"); layer {
	case "current":
		obs.Address = source.Clean(source.Join(f.Str("STNO"), f.Str("PREDIR"), f.Str("STNAME"),
			f.Str("SUFFIX"), f.Str("POSTDIR"), f.Str("STSUB")))
		obs.Zip = source.Zip5(f.Str("ZIP"))
		obs.Category = source.Join(f.Str("APTYPE"), f.Str("WORKTYPE"))
		obs.Status = f.Str("BLDGAPPLSTATUS")
		obs.StartDate = epochMillis(f.Str("ISSDTTM"))
	case "archive":
		obs.Address = source.Clean(source.Join(f.Str("ADDR"), f.Str("SUITE")))
		obs.Category = f.Str("APDESC")
		obs.Status = f.Str("STAT")
		obs.StartDate = source.Date("02-Jan-06", f.Str("ISSUE_DT"))
		obs.EndDate = source.Date("02-Jan-06", f.Str("COMP_D"))
		obs.Lat, obs.Lon = source.LatLon(f.Float("_lat"), f.Float("_lon"))
	default:
		return source.Observation{}, fmt.Errorf("unknown _layer %q", layer)
	}
	return obs, nil
}

// epochMillis converts an ArcGIS date field (milliseconds since 1970, UTC) to a time.
func epochMillis(s string) *time.Time {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms == 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/clvpermit/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/source/clvpermit
git commit -m "feat(clvpermit): City of Las Vegas building permits, current + archive"
```

---

### Task 7: SNHD restaurant inspections (`snhd`)

The district publishes a nightly ~9 MB zip of semicolon-separated tables. It is **not** RFC CSV: there is no quoting (`"A" Grade` is literal data), every line ends with `;`, and 54 establishment names contain a bare `;` that is repeated in the trailing `search_text` column. A line with an even number of extra fields is repaired; anything else is emitted as an error record that `Parse` rejects, so the runner stores it in `parse_errors`. Each inspection record carries the stable columns of its establishment; volatile ones (`current_grade` …) are excluded so a new grade does not re-version every past inspection.

**Files:**
- Create: `internal/source/snhd/snhd.go`
- Test: `internal/source/snhd/snhd_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/source/snhd/snhd_test.go` (the fixture lines are real, including the broken ones):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/snhd/`
Expected: build failure, `undefined: Source`.

- [ ] **Step 3: Write the source**

`internal/source/snhd/snhd.go`:

```go
// Package snhd reads Southern Nevada Health District restaurant inspections
// from the district's nightly zip of semicolon-separated tables.
package snhd

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jasonharker/city-pulse/internal/source"
)

const zipURL = "https://www.southernnevadahealthdistrict.org/restaurants/download/restaurants.zip"

// The zip is ~9 MB; anything far larger means the URL now serves something else.
const maxZip = 200 << 20

// Inspection tables: current system (2020-) and the legacy one (2005-2017).
var inspectionFiles = []string{"restaurant_inspections.csv", "restaurant_inspections_stanford.csv"}

// Establishment columns copied onto each inspection. Volatile columns such as
// current_grade are left out: they change with every new inspection and would
// otherwise create a new version of every past inspection of that restaurant.
var establishmentColumns = []string{
	"facility_id", "PE", "restaurant_name", "location_name", "address",
	"latitude", "longitude", "city_name", "zip_code",
}

type Source struct {
	URL  string
	HTTP *http.Client
}

func New(c *http.Client) *Source { return &Source{URL: zipURL, HTTP: c} }

func (*Source) Name() string { return "snhd" }

func (s *Source) Fetch(ctx context.Context, emit func(source.Record) error) error {
	zr, err := s.download(ctx)
	if err != nil {
		return err
	}
	types, err := inspectionTypes(zr)
	if err != nil {
		return err
	}
	estabs := make(map[string]map[string]string)
	err = eachRow(zr, "restaurant_establishments.csv", func(header, cols []string, line string) error {
		row, err := repairEstablishment(header, cols)
		if err != nil {
			return emitError(emit, "establishment:"+cols[0], err, line)
		}
		estabs[row["permit_number"]] = row
		return nil
	})
	if err != nil {
		return err
	}
	for _, name := range inspectionFiles {
		err := eachRow(zr, name, func(header, cols []string, line string) error {
			if len(cols) != len(header) {
				return emitError(emit, "inspection:"+cols[0],
					fmt.Errorf("%d fields, want %d", len(cols), len(header)), line)
			}
			fields := map[string]any{"_file": name}
			for i, h := range header {
				fields[h] = cols[i]
			}
			fields["inspection_type"] = types[fields["inspection_type_id"].(string)]
			if e, ok := estabs[cols[1]]; ok { // permit_number; absent for ~340 permits
				for _, c := range establishmentColumns {
					fields["est_"+c] = e[c]
				}
			}
			r, err := source.NewRecord(cols[0], fields)
			if err != nil {
				return err
			}
			return emit(r)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) download(ctx context.Context) (*zip.Reader, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", source.UserAgent)
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snhd: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxZip+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxZip {
		return nil, errors.New("snhd: zip larger than 200 MB")
	}
	return zip.NewReader(bytes.NewReader(b), int64(len(b)))
}

// eachRow calls fn for every data line of a table in the zip. The files are
// ';'-separated with a trailing ';' and no quoting: quotes are literal data.
func eachRow(zr *zip.Reader, name string, fn func(header, cols []string, line string) error) error {
	f, err := zr.Open(name)
	if err != nil {
		return fmt.Errorf("snhd: %s: %w", name, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var header []string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		cols := split(line)
		if header == nil {
			header = cols
			continue
		}
		if err := fn(header, cols, line); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("snhd: %s: %w", name, err)
	}
	if header == nil {
		return fmt.Errorf("snhd: %s is empty", name)
	}
	return nil
}

func split(line string) []string {
	return strings.Split(strings.TrimSuffix(line, ";"), ";")
}

// repairEstablishment maps an establishment line to its columns. Some names
// contain a bare ';', which also appears in the trailing search_text column,
// so a repairable line has an even number of extra fields: half belong to
// restaurant_name (column 4), half to search_text (the last column).
func repairEstablishment(header, cols []string) (map[string]string, error) {
	const nameCol = 4
	extra := len(cols) - len(header)
	if header[nameCol] != "restaurant_name" || header[len(header)-1] != "search_text" {
		return nil, fmt.Errorf("unexpected establishment header %v", header)
	}
	if extra < 0 || extra%2 != 0 {
		return nil, fmt.Errorf("%d fields, want %d", len(cols), len(header))
	}
	k := extra / 2
	fixed := make([]string, 0, len(header))
	fixed = append(fixed, cols[:nameCol]...)
	fixed = append(fixed, strings.Join(cols[nameCol:nameCol+k+1], ";"))
	fixed = append(fixed, cols[nameCol+k+1:len(cols)-k-1]...)
	fixed = append(fixed, strings.Join(cols[len(cols)-k-1:], ";"))
	row := make(map[string]string, len(header))
	for i, h := range header {
		row[h] = fixed[i]
	}
	return row, nil
}

func inspectionTypes(zr *zip.Reader) (map[string]string, error) {
	types := make(map[string]string)
	err := eachRow(zr, "restaurant_inspection_types.csv", func(_, cols []string, _ string) error {
		if len(cols) >= 2 {
			types[cols[0]] = cols[1]
		}
		return nil
	})
	return types, err
}

// emitError forwards an unparseable line as a record that Parse rejects, so the
// ingest runner stores it in parse_errors instead of it vanishing.
func emitError(emit func(source.Record) error, id string, cause error, line string) error {
	r, err := source.NewRecord(id, map[string]any{"_error": cause.Error(), "_raw": line})
	if err != nil {
		return err
	}
	return emit(r)
}

func (*Source) Parse(r source.Record) (source.Observation, error) {
	f, err := source.Decode(r)
	if err != nil {
		return source.Observation{}, err
	}
	if msg := f.Str("_error"); msg != "" {
		return source.Observation{}, errors.New(msg)
	}
	name := f.Str("est_restaurant_name")
	if name == "" {
		name = f.Str("est_location_name")
	}
	lat, lon := source.LatLon(f.Float("est_latitude"), f.Float("est_longitude"))
	return source.Observation{
		Kind:        "inspection",
		Name:        source.Clean(name),
		Address:     source.Clean(f.Str("est_address")),
		City:        f.Str("est_city_name"),
		Zip:         source.Zip5(f.Str("est_zip_code")),
		BusinessKey: "snhd:" + f.Str("permit_number"),
		Category:    f.Str("inspection_type"),
		Status:      f.Str("inspection_result"),
		Lat:         lat,
		Lon:         lon,
		StartDate:   source.Date("2006-01-02 15:04:05", f.Str("inspection_date")),
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/snhd/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/source/snhd
git commit -m "feat(snhd): SNHD restaurant inspections"
```

---

### Task 8: Local Postgres for development and tests (`tools/devdb`)

The store and ingest tests run against a real Postgres, never a mock. `tools/devdb` starts Postgres 16 from downloaded binaries (no root, Docker or system packages) and creates the `citypulse` database. It is its own module, so the app never depends on it.

**Files:**
- Create: `tools/devdb/main.go`, `tools/devdb/go.mod`, `tools/devdb/go.sum`

- [ ] **Step 1: Write the tool**

`tools/devdb/main.go`:

```go
// Command devdb runs a throwaway Postgres 16 for local development and tests,
// with no root, Docker or system packages. Binaries and data live in .devdb/.
// It lives in its own module so the app never depends on it.
//
//	cd tools/devdb && go run . [-port 55432]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	ep "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

func main() {
	port := flag.Uint("port", 55432, "port to listen on")
	dir := flag.String("dir", "../../.devdb", "directory for binaries and data")
	flag.Parse()
	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	pg := ep.NewDatabase(ep.DefaultConfig().Version(ep.V16).Port(uint32(*port)).
		RuntimePath(filepath.Join(abs, "runtime")).DataPath(filepath.Join(abs, "data")).
		BinariesPath(filepath.Join(abs, "bin")).CachePath(filepath.Join(abs, "cache")))
	if err := pg.Start(); err != nil {
		log.Fatal(err)
	}
	defer pg.Stop()

	base := fmt.Sprintf("postgres://postgres:postgres@localhost:%d", *port)
	if err := createDB(base+"/postgres?sslmode=disable", "citypulse"); err != nil {
		log.Print(err)
		return
	}
	fmt.Printf("DATABASE_URL=%s/citypulse?sslmode=disable\n", base)
	fmt.Printf("CITYPULSE_TEST_DATABASE_URL=%s/postgres?sslmode=disable\n", base)
	fmt.Println("Ctrl-C to stop.")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
}

func createDB(url, name string) error {
	ctx := context.Background()
	c, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	var exists bool
	if err := c.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = c.Exec(ctx, "CREATE DATABASE "+name)
	return err
}
```

- [ ] **Step 2: Create its module**

```bash
cd tools/devdb
go mod init github.com/jasonharker/city-pulse/tools/devdb
go get github.com/fergusstrange/embedded-postgres@v1.34.0 github.com/jackc/pgx/v5@v5.11.0
go mod tidy
cd ../..
```

- [ ] **Step 3: Start it and keep it running in a second terminal**

Run: `cd tools/devdb && go run .`
Expected (first run downloads Postgres, ~1 minute):

```
DATABASE_URL=postgres://postgres:postgres@localhost:55432/citypulse?sslmode=disable
CITYPULSE_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable
Ctrl-C to stop.
```

Export both variables in the terminal used for the remaining tasks.

- [ ] **Step 4: Commit**

```bash
git add tools/devdb
git commit -m "chore(devdb): local Postgres 16 for development and tests"
```

---

### Task 9: Store

Schema: `source_runs` (one row per source run), `observations` (one row per record version, unique on `(source, record_id, hash)`), `parse_errors`. Migrations are embedded SQL files applied in one transaction under an advisory lock.

The write path is a `store.Run` holding one pooled connection, because it stages through session temp tables:
- `Add` `COPY`s a batch into `run_batch`, inserts it with `ON CONFLICT DO NOTHING` (rows affected = new versions), clears withdrawal on any re-seen withdrawn version, and appends the keys to `run_seen`.
- `Withdraw` anti-joins the source's records against `run_seen`.
- `Finish` records the outcome and drops the temp tables; if the drop fails the connection is destroyed rather than returned to the pool with stale tables.

Two earlier designs failed in live runs and are why it looks like this. An upsert that stamped `last_run_id` on every seen row rewrote every row every night (Postgres MVCC writes a new tuple per update — ~2.3M dead tuples per run) and managed ~600 rows/s. And withdrawal planned on stale statistics after a bulk first insert ran for minutes over 90k rows, so `Withdraw` runs `ANALYZE` first. `TestUnchangedRunWritesNoRows` pins the first property by checking the row's `xmin` does not change.

**Files:**
- Create: `internal/store/migrations/001_observations.sql`, `internal/store/store.go`, `internal/store/observations.go`, `internal/store/storetest/storetest.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Add pgx**

```bash
go get github.com/jackc/pgx/v5@v5.11.0
```

- [ ] **Step 2: Write the test helper**

`internal/store/storetest/storetest.go`:

```go
// Package storetest gives tests a migrated store in a throwaway schema.
package storetest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jasonharker/city-pulse/internal/store"
)

// New returns a migrated store in a fresh schema of the database named by
// CITYPULSE_TEST_DATABASE_URL (a postgres:// URL), plus a connection on the
// same schema for assertions. The schema is dropped after the test.
// Without the variable, the test is skipped.
func New(t *testing.T) (*store.Store, *pgx.Conn) {
	t.Helper()
	base := os.Getenv("CITYPULSE_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("CITYPULSE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return st, conn
}
```

- [ ] **Step 3: Write the failing tests**

`internal/store/store_test.go`:

```go
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store"
	"github.com/jasonharker/city-pulse/internal/store/storetest"
)

func obs(t *testing.T, id string, fields map[string]any) source.Observation {
	t.Helper()
	r, err := source.NewRecord(id, fields)
	if err != nil {
		t.Fatal(err)
	}
	d := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	lat, lon := 36.1, -115.1
	return source.Observation{
		Source: "test", RecordID: id, Hash: r.Hash(), Payload: r.Payload,
		Kind: "license", Name: "Joe's", Address: "1 Main St", Lat: &lat, Lon: &lon, StartDate: &d,
	}
}

// run performs one complete successful run and returns (new, withdrawn).
func run(t *testing.T, st *store.Store, batches ...[]source.Observation) (int, int) {
	t.Helper()
	ctx := context.Background()
	r, err := st.BeginRun(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	var stats store.RunStats
	for _, b := range batches {
		n, err := r.Add(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		stats.New += n
	}
	if stats.Withdrawn, err = r.Withdraw(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, stats, nil); err != nil {
		t.Fatal(err)
	}
	return stats.New, stats.Withdrawn
}

func TestMigrateIsIdempotent(t *testing.T) {
	st, _ := storetest.New(t)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunsVersionWithdrawAndRestore(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	q := func(sql string) int {
		var n int
		if err := db.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Run 1: a, b, and an exact duplicate of b in another batch.
	n, w := run(t, st,
		[]source.Observation{obs(t, "a", map[string]any{"v": 1}), obs(t, "b", map[string]any{"v": 1})},
		[]source.Observation{obs(t, "b", map[string]any{"v": 1})})
	if n != 2 || w != 0 {
		t.Fatalf("run1 new=%d withdrawn=%d", n, w)
	}

	// Run 2: a changed, b gone.
	n, w = run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 2})})
	if n != 1 || w != 1 {
		t.Fatalf("run2 new=%d withdrawn=%d", n, w)
	}
	if got := q("SELECT count(*) FROM observations WHERE record_id = 'a'"); got != 2 {
		t.Errorf("versions of a = %d, want 2", got)
	}
	if got := q("SELECT count(*) FROM observations WHERE record_id = 'b' AND withdrawn_at IS NOT NULL"); got != 1 {
		t.Errorf("withdrawn b = %d, want 1", got)
	}

	// Run 3: b back unchanged → same version, withdrawal cleared, nothing new.
	n, _ = run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 2}), obs(t, "b", map[string]any{"v": 1})})
	if n != 0 {
		t.Errorf("run3 new=%d, want 0", n)
	}
	if got := q("SELECT count(*) FROM observations WHERE withdrawn_at IS NOT NULL"); got != 0 {
		t.Errorf("withdrawn after reappearance = %d, want 0", got)
	}
}

func TestUnchangedRunWritesNoRows(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	batch := []source.Observation{obs(t, "a", map[string]any{"v": 1})}
	run(t, st, batch)
	var before, after int64
	db.QueryRow(ctx, "SELECT xmin::text::bigint FROM observations").Scan(&before)
	run(t, st, batch)
	db.QueryRow(ctx, "SELECT xmin::text::bigint FROM observations").Scan(&after)
	if before != after {
		t.Errorf("unchanged row was rewritten (xmin %d -> %d)", before, after)
	}
}

func TestAddStoresParsedColumns(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	run(t, st, []source.Observation{obs(t, "a", map[string]any{"v": 1})})
	var name, payload, start string
	var lat float64
	err := db.QueryRow(ctx, "SELECT name, payload::text, start_date::text, lat FROM observations").Scan(&name, &payload, &start, &lat)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Joe's" || payload != `{"v": 1}` || start != "2024-01-02" || lat != 36.1 {
		t.Errorf("row = %q %q %q %v", name, payload, start, lat)
	}
}

func TestParseErrorsAndLastGoodRun(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	if _, ok, err := st.LastGoodRun(ctx, "test"); ok || err != nil {
		t.Fatalf("no runs yet: ok=%v err=%v", ok, err)
	}
	r1, err := st.BeginRun(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := source.NewRecord("x", map[string]any{"_raw": "a;b"})
	if err := r1.ParseError(ctx, rec, errors.New("bad row")); err != nil {
		t.Fatal(err)
	}
	if err := r1.Finish(ctx, store.RunStats{Seen: 10}, nil); err != nil {
		t.Fatal(err)
	}
	r2, _ := st.BeginRun(ctx, "test") // reuses the pooled connection: staging tables must be gone
	if err := r2.Finish(ctx, store.RunStats{Seen: 3}, errors.New("boom")); err != nil {
		t.Fatal(err)
	}

	seen, ok, err := st.LastGoodRun(ctx, "test")
	if err != nil || !ok || seen != 10 {
		t.Errorf("LastGoodRun = %d %v %v, want 10 (failed run ignored)", seen, ok, err)
	}
	var msg string
	db.QueryRow(ctx, "SELECT error FROM parse_errors").Scan(&msg)
	if msg != "bad row" {
		t.Errorf("parse error = %q", msg)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/store/...`
Expected: build failure, `undefined: store.Open` (from `storetest`).

- [ ] **Step 5: Write the migration**

`internal/store/migrations/001_observations.sql`:

```sql
CREATE TABLE source_runs (
    id           bigserial   PRIMARY KEY,
    source       text        NOT NULL,
    started_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz,
    rows_seen    integer     NOT NULL DEFAULT 0,
    rows_new     integer     NOT NULL DEFAULT 0,
    parse_errors integer     NOT NULL DEFAULT 0,
    withdrawn    integer     NOT NULL DEFAULT 0,
    error        text
);
CREATE INDEX source_runs_source_idx ON source_runs (source, id DESC);

-- One row per version of a source record. A row is written once; afterwards
-- only withdrawn_at changes. Unchanged records cost no writes on later runs.
CREATE TABLE observations (
    id           bigserial        PRIMARY KEY,
    source       text             NOT NULL,
    record_id    text             NOT NULL,
    hash         text             NOT NULL,
    payload      jsonb            NOT NULL,
    kind         text             NOT NULL,
    name         text             NOT NULL,
    address      text             NOT NULL,
    city         text             NOT NULL,
    zip          text             NOT NULL,
    parcel       text             NOT NULL,
    business_key text             NOT NULL,
    category     text             NOT NULL,
    status       text             NOT NULL,
    lat          double precision,
    lon          double precision,
    start_date   date,
    end_date     date,
    first_seen   timestamptz      NOT NULL DEFAULT now(),
    first_run_id bigint           NOT NULL REFERENCES source_runs (id),
    withdrawn_at timestamptz,
    UNIQUE (source, record_id, hash)
);
CREATE INDEX observations_record_idx ON observations (source, record_id);

CREATE TABLE parse_errors (
    id         bigserial   PRIMARY KEY,
    run_id     bigint      NOT NULL REFERENCES source_runs (id),
    source     text        NOT NULL,
    record_id  text        NOT NULL,
    payload    jsonb       NOT NULL,
    error      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

- [ ] **Step 6: Write Open and Migrate**

`internal/store/store.go`:

```go
// Package store is city-pulse's Postgres layer.
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrateLock is an arbitrary advisory-lock key: serve replicas and the
// nightly job may all start at once, and only one should migrate.
const migrateLock = 7203305

type Store struct{ pool *pgxpool.Pool }

// Open connects and pings. url is a postgres:// URL.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate applies every embedded migration not yet recorded, in filename order,
// in one transaction.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", migrateLock)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text        PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
```

- [ ] **Step 7: Write runs, upsert, parse errors and withdrawal**

`internal/store/observations.go`:

```go
package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jasonharker/city-pulse/internal/source"
)

// RunStats is what one ingest run of one source did.
type RunStats struct {
	Seen, New, ParseErrors, Withdrawn int
}

// LastGoodRun returns rows_seen of the source's most recent successful run.
func (s *Store) LastGoodRun(ctx context.Context, src string) (seen int, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT rows_seen FROM source_runs
		WHERE source = $1 AND finished_at IS NOT NULL AND error IS NULL
		ORDER BY id DESC LIMIT 1`, src).Scan(&seen)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return seen, err == nil, err
}

// Run is one ingest run of one source. It holds a single connection because
// its staging tables are session-local temp tables. Always call Finish.
type Run struct {
	ID     int64
	source string
	conn   *pgxpool.Conn
}

// Staging: each batch is COPYed into run_batch; every (record_id, hash) the
// run saw accumulates in run_seen, which withdrawal anti-joins against.
const stagingSQL = `
CREATE TEMP TABLE run_batch (LIKE observations INCLUDING DEFAULTS);
ALTER TABLE run_batch DROP COLUMN id, DROP COLUMN first_seen, DROP COLUMN withdrawn_at;
CREATE TEMP TABLE run_seen (record_id text NOT NULL, hash text NOT NULL);`

// batchColumns is the COPY column list for run_batch, in Observation order.
var batchColumns = []string{"source", "record_id", "hash", "payload", "kind", "name", "address", "city",
	"zip", "parcel", "business_key", "category", "status", "lat", "lon", "start_date", "end_date", "first_run_id"}

// BeginRun records a new run of src and prepares its staging tables.
func (s *Store) BeginRun(ctx context.Context, src string) (*Run, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	r := &Run{source: src, conn: conn}
	if err := conn.QueryRow(ctx, "INSERT INTO source_runs (source) VALUES ($1) RETURNING id", src).Scan(&r.ID); err != nil {
		conn.Release()
		return nil, err
	}
	if _, err := conn.Exec(ctx, stagingSQL); err != nil {
		conn.Release()
		return nil, err
	}
	return r, nil
}

// Add stores the new versions among obs and returns how many there were.
// Versions already stored are only noted as seen; if they had been withdrawn,
// the withdrawal is cleared.
func (r *Run) Add(ctx context.Context, obs []source.Observation) (int, error) {
	if len(obs) == 0 {
		return 0, nil
	}
	rows := make([][]any, len(obs))
	for i, o := range obs {
		rows[i] = []any{o.Source, o.RecordID, o.Hash, []byte(o.Payload), o.Kind, o.Name, o.Address,
			o.City, o.Zip, o.Parcel, o.BusinessKey, o.Category, o.Status, o.Lat, o.Lon,
			o.StartDate, o.EndDate, r.ID}
	}
	c := r.conn
	if _, err := c.CopyFrom(ctx, pgx.Identifier{"run_batch"}, batchColumns, pgx.CopyFromRows(rows)); err != nil {
		return 0, err
	}
	// DO NOTHING (unlike DO UPDATE) tolerates the same key twice in one statement,
	// which happens when a source publishes exact duplicate rows.
	tag, err := c.Exec(ctx, `INSERT INTO observations (`+columnList+`)
		SELECT `+columnList+` FROM run_batch
		ON CONFLICT (source, record_id, hash) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	if _, err := c.Exec(ctx, `UPDATE observations o SET withdrawn_at = NULL
		FROM run_batch b
		WHERE o.source = b.source AND o.record_id = b.record_id AND o.hash = b.hash
		  AND o.withdrawn_at IS NOT NULL`); err != nil {
		return 0, err
	}
	if _, err := c.Exec(ctx, "INSERT INTO run_seen SELECT record_id, hash FROM run_batch; TRUNCATE run_batch"); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

var columnList = strings.Join(batchColumns, ", ")

// ParseError keeps a record that failed to parse, with the reason.
func (r *Run) ParseError(ctx context.Context, rec source.Record, cause error) error {
	_, err := r.conn.Exec(ctx, `INSERT INTO parse_errors (run_id, source, record_id, payload, error)
		VALUES ($1, $2, $3, $4, $5)`, r.ID, r.source, rec.ID, []byte(rec.Payload), cause.Error())
	return err
}

// Withdraw marks every record of the source that this run did not see as withdrawn.
// Call it only after a complete, successful fetch.
func (r *Run) Withdraw(ctx context.Context) (int, error) {
	// Temp tables are never auto-analyzed, and a first run inserts rows faster
	// than autovacuum re-analyzes observations. On stale statistics the planner
	// picks a nested loop and 90k rows take minutes; fresh ones get a hash anti-join.
	if _, err := r.conn.Exec(ctx, "ANALYZE run_seen; ANALYZE observations"); err != nil {
		return 0, err
	}
	tag, err := r.conn.Exec(ctx, `UPDATE observations o SET withdrawn_at = now()
		WHERE o.source = $1 AND o.withdrawn_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM run_seen s WHERE s.record_id = o.record_id)`, r.source)
	return int(tag.RowsAffected()), err
}

// Finish records the run's outcome (nil runErr = success), drops the staging
// tables and returns the connection to the pool.
func (r *Run) Finish(ctx context.Context, st RunStats, runErr error) error {
	defer r.conn.Release()
	var msg *string
	if runErr != nil {
		m := runErr.Error()
		msg = &m
	}
	_, err := r.conn.Exec(ctx, `UPDATE source_runs
		SET finished_at = now(), rows_seen = $2, rows_new = $3, parse_errors = $4, withdrawn = $5, error = $6
		WHERE id = $1`, r.ID, st.Seen, st.New, st.ParseErrors, st.Withdrawn, msg)
	if _, dropErr := r.conn.Exec(ctx, "DROP TABLE IF EXISTS run_batch, run_seen"); dropErr != nil {
		// A pooled connection must not keep stale staging tables: the next
		// BeginRun on it would fail. Destroy it instead of returning it.
		r.conn.Hijack().Close(ctx)
		return errors.Join(err, dropErr)
	}
	return err
}
```

- [ ] **Step 8: Run tests against the dev database**

Run: `go test -count=1 -v ./internal/store/ | grep -E '^(--- |ok|FAIL)'`
Expected: five `--- PASS` lines and `ok`. A `--- SKIP` means `CITYPULSE_TEST_DATABASE_URL` is not exported; the store is not verified until these run.

- [ ] **Step 9: Commit**

```bash
go mod tidy
git add go.mod go.sum internal/store
git commit -m "feat(store): observations schema, versioned upserts, runs, withdrawal"
```

---

### Task 10: Ingest runner

Runs each source, parses records, batches writes in 2,000s, and records the run. A parse failure stores the record in `parse_errors` and continues. After a successful fetch, records the run did not see are withdrawn, unless the run saw fewer than half the rows of the last successful run: an upstream outage that returns an empty layer must not mark the city as gone. That case fails the run instead, which the Serve plan's alert picks up.

**Files:**
- Create: `internal/ingest/ingest.go`
- Test: `internal/ingest/ingest_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/ingest/ingest_test.go`:

```go
package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store/storetest"
)

// fake publishes rows as {"v": value} records; a value of "bad" fails Parse.
type fake struct {
	name     string
	rows     map[string]string
	fetchErr error
}

func (f *fake) Name() string { return f.name }

func (f *fake) Fetch(_ context.Context, emit func(source.Record) error) error {
	if f.fetchErr != nil {
		return f.fetchErr
	}
	for id, v := range f.rows {
		r, _ := source.NewRecord(id, map[string]any{"v": v})
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *fake) Parse(r source.Record) (source.Observation, error) {
	d, _ := source.Decode(r)
	if d.Str("v") == "bad" {
		return source.Observation{}, errors.New("bad value")
	}
	return source.Observation{Kind: "license", Name: d.Str("v")}, nil
}

func TestRunOneVersionsWithdrawsAndKeepsParseErrors(t *testing.T) {
	st, _ := storetest.New(t)
	ctx := context.Background()
	src := &fake{name: "fake", rows: map[string]string{"a": "1", "b": "1", "c": "bad", "": "1"}}

	stats, err := runOne(ctx, st, src)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Seen != 4 || stats.New != 2 || stats.ParseErrors != 2 || stats.Withdrawn != 0 {
		t.Errorf("run1 = %+v", stats)
	}

	src.rows = map[string]string{"a": "2", "c": "1", "d": "1"}
	stats, err = runOne(ctx, st, src)
	if err != nil {
		t.Fatal(err)
	}
	if stats.New != 3 || stats.Withdrawn != 1 { // a v2, c, d new; b withdrawn
		t.Errorf("run2 = %+v", stats)
	}
}

func TestRunOneRefusesToWithdrawOnCollapse(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	src := &fake{name: "fake", rows: map[string]string{"a": "1", "b": "1", "c": "1", "d": "1"}}
	if _, err := runOne(ctx, st, src); err != nil {
		t.Fatal(err)
	}
	src.rows = map[string]string{"a": "1"}
	stats, err := runOne(ctx, st, src)
	if err == nil {
		t.Fatal("want error when rows collapse")
	}
	var withdrawn int
	db.QueryRow(ctx, "SELECT count(*) FROM observations WHERE withdrawn_at IS NOT NULL").Scan(&withdrawn)
	if stats.Withdrawn != 0 || withdrawn != 0 {
		t.Errorf("withdrew %d/%d rows on a collapsed run", stats.Withdrawn, withdrawn)
	}
}

func TestRunContinuesPastFailingSource(t *testing.T) {
	st, db := storetest.New(t)
	ctx := context.Background()
	broken := &fake{name: "broken", fetchErr: errors.New("HTTP 503")}
	good := &fake{name: "good", rows: map[string]string{"a": "1"}}
	err := Run(ctx, st, []source.Source{broken, good}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("want joined error")
	}
	var n int
	db.QueryRow(ctx, "SELECT count(*) FROM observations WHERE source = 'good'").Scan(&n)
	var failed string
	db.QueryRow(ctx, "SELECT error FROM source_runs WHERE source = 'broken'").Scan(&failed)
	if n != 1 || failed != "HTTP 503" {
		t.Errorf("good rows=%d broken error=%q", n, failed)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ingest/`
Expected: build failure, `undefined: runOne`.

- [ ] **Step 3: Write the runner**

`internal/ingest/ingest.go`:

```go
// Package ingest runs sources and stores what they publish.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/store"
)

const batchSize = 2000

// Run ingests each source in turn. One source failing does not stop the rest;
// the returned error joins every failure.
func Run(ctx context.Context, st *store.Store, sources []source.Source, log *slog.Logger) error {
	var errs []error
	for _, s := range sources {
		stats, err := runOne(ctx, st, s)
		log.Info("ingest", "source", s.Name(), "seen", stats.Seen, "new", stats.New,
			"parse_errors", stats.ParseErrors, "withdrawn", stats.Withdrawn, "err", err)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	return errors.Join(errs...)
}

func runOne(ctx context.Context, st *store.Store, s source.Source) (store.RunStats, error) {
	var stats store.RunStats
	prevSeen, hasPrev, err := st.LastGoodRun(ctx, s.Name())
	if err != nil {
		return stats, err
	}
	run, err := st.BeginRun(ctx, s.Name())
	if err != nil {
		return stats, err
	}

	batch := make([]source.Observation, 0, batchSize)
	flush := func() error {
		n, err := run.Add(ctx, batch)
		stats.New += n
		batch = batch[:0]
		return err
	}
	runErr := s.Fetch(ctx, func(r source.Record) error {
		stats.Seen++
		obs, err := parse(s, r)
		if err != nil {
			stats.ParseErrors++
			return run.ParseError(ctx, r, err)
		}
		if batch = append(batch, obs); len(batch) == batchSize {
			return flush()
		}
		return nil
	})
	if runErr == nil {
		runErr = flush()
	}
	if runErr == nil {
		// An upstream outage can return an empty or truncated layer. Withdrawing
		// on that would mark most of the city as gone, so refuse and fail loudly.
		if hasPrev && stats.Seen < prevSeen/2 {
			runErr = fmt.Errorf("saw %d rows, last good run saw %d: withdrawal skipped", stats.Seen, prevSeen)
		} else {
			stats.Withdrawn, runErr = run.Withdraw(ctx)
		}
	}
	// Record the outcome even when ctx was cancelled mid-run.
	if err := run.Finish(context.WithoutCancel(ctx), stats, runErr); err != nil {
		return stats, errors.Join(runErr, err)
	}
	return stats, runErr
}

// parse runs the source's parser and fills in the record-level fields.
func parse(s source.Source, r source.Record) (source.Observation, error) {
	if strings.TrimSpace(r.ID) == "" {
		return source.Observation{}, errors.New("record has no id")
	}
	obs, err := s.Parse(r)
	if err != nil {
		return source.Observation{}, err
	}
	obs.Source, obs.RecordID, obs.Hash, obs.Payload = s.Name(), r.ID, r.Hash(), r.Payload
	return obs, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 -v ./internal/ingest/ | grep -E '^(--- |ok|FAIL)'`
Expected: three `--- PASS` lines and `ok` (not `SKIP`).

- [ ] **Step 5: Commit**

```bash
git add internal/ingest
git commit -m "feat(ingest): run sources with batching, parse-error capture, withdrawal guard"
```

---

### Task 11: Command and live run

**Files:**
- Create: `cmd/citypulse/main.go`

- [ ] **Step 1: Write the command**

`cmd/citypulse/main.go`:

```go
// Command citypulse ingests Las Vegas civic data.
//
//	citypulse migrate
//	citypulse ingest [-only snhd,ccbiz]
//
// DATABASE_URL must be a postgres:// URL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jasonharker/city-pulse/internal/ingest"
	"github.com/jasonharker/city-pulse/internal/source"
	"github.com/jasonharker/city-pulse/internal/source/ccbiz"
	"github.com/jasonharker/city-pulse/internal/source/clvlicense"
	"github.com/jasonharker/city-pulse/internal/source/clvpermit"
	"github.com/jasonharker/city-pulse/internal/source/snhd"
	"github.com/jasonharker/city-pulse/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: citypulse migrate | ingest [-only a,b]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "migrate":
		err = withStore(ctx, func(st *store.Store) error { return nil })
	case "ingest":
		err = runIngest(ctx, os.Args[2:], log)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Error("citypulse failed", "cmd", os.Args[1], "err", err)
		os.Exit(1)
	}
}

// withStore opens and migrates the database, then runs fn.
func withStore(ctx context.Context, fn func(*store.Store) error) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return fn(st)
}

func runIngest(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	only := fs.String("only", "", "comma-separated source names (default: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := &http.Client{Timeout: 2 * time.Minute}
	all := []source.Source{snhd.New(c), ccbiz.New(c), clvlicense.New(c), clvpermit.New(c)}
	sources := all
	if *only != "" {
		names := strings.Split(*only, ",")
		sources = slices.DeleteFunc(slices.Clone(all), func(s source.Source) bool { return !slices.Contains(names, s.Name()) })
		if len(sources) != len(names) {
			return fmt.Errorf("-only %q: unknown source name", *only)
		}
	}
	return withStore(ctx, func(st *store.Store) error { return ingest.Run(ctx, st, sources, log) })
}
```

- [ ] **Step 2: Build and check the error paths**

```bash
go vet ./... && go build -o citypulse ./cmd/citypulse
./citypulse ingest -only nope
DATABASE_URL= ./citypulse migrate
```

Expected: `unknown source name` and `DATABASE_URL is not set`, each with exit status 1.

- [ ] **Step 3: Run the full suite**

Run: `go test -count=1 ./...`
Expected: every package `ok`.

- [ ] **Step 4: Live ingest of all four sources**

With `DATABASE_URL` exported from Task 8:

```bash
time ./citypulse ingest
```

Expected log lines (counts as of 2026-10-14; they drift daily):

```
level=INFO msg=ingest source=snhd seen=331807 new=331806 parse_errors=1 withdrawn=0 err=<nil>
level=INFO msg=ingest source=ccbiz seen=90019 new=90019 parse_errors=0 withdrawn=0 err=<nil>
level=INFO msg=ingest source=clvlicense seen=149702 new=149601 parse_errors=101 withdrawn=0 err=<nil>
level=INFO msg=ingest source=clvpermit seen=1807596 new=534185 parse_errors=0 withdrawn=0 err=<nil>
```

About 15 minutes on a laptop, 13 of them in `clvpermit` (the archive repeats rows up to 24×; only distinct versions are written). Database size afterwards: ~1.4 GB.

- `snhd`'s one parse error is the establishment line `PR0134961` (`-1;;AS VEGAS`), unrepairable by design.
- `clvlicense`'s 101 are rows with no license number.
- `clvpermit` `new` is lower than `seen` because of the repeats; 534,185 versions cover 426,701 permits (lifecycle snapshots).

- [ ] **Step 5: Run it again to prove change detection**

Run: `./citypulse ingest -only snhd,ccbiz`
Expected: both sources report `new=0` (or a handful, if the agency changed rows in between) and `withdrawn=0`. Measured: `snhd seen=331807 new=0`, `ccbiz seen=90019 new=0`, 55 seconds.

- [ ] **Step 6: Commit and push**

```bash
git add cmd/citypulse
git commit -m "feat(cmd): citypulse migrate and ingest"
git push -u origin HEAD
```

---

## Done when

- `go test -count=1 ./...` passes with `CITYPULSE_TEST_DATABASE_URL` set (no skips in `store` or `ingest`).
- A live `citypulse ingest` stores all four sources, and an immediate second run reports near-zero new versions.

## Next

Plan 2 (Resolve) builds the `address` package against a golden corpus sampled from these observations, then places, businesses and the table swap.
