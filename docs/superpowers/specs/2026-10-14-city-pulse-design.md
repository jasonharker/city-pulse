# city-pulse — Design

**Date:** 2026-10-14
**Status:** Approved design, pre-implementation
**Public URL:** `https://citypulse.jasonharker.com`
**Module:** `github.com/jasonharker/city-pulse`

## Purpose

A public, read-only website where anyone can look up any Las Vegas place and see its history: every business license, building permit, health inspection and liquor license recorded there, merged across agencies into one timeline. A storefront that was a bar, then a vape shop, then a taco place shows that succession.

The project is also a deliberate programming exercise: a Go codebase whose complexity comes from the domain (messy multi-agency data, address normalization, entity resolution), not from infrastructure.

## Scope

**v1:** four structured sources, place and business pages, search, nightly refresh, public exposure.

**Not in v1:**
- Council/commission agendas (unstructured text — a separate sub-project; Clark County's Legistar Web API is the likely starting point).
- Nevada Secretary of State entity filings (bulk download is paid and currently disabled pending the state's ORION system).
- User accounts and email alerts. Following a place, a business or the city is an Atom feed.
- Ranking places by turnover. Measured: business counts surface registered-agent suites (977 undated City licenses at one), and restaurant-only succession surfaces ghost kitchens; real storefronts top out at 3 successions.
- Other cities. The `Source` interface is designed so Chicago/NYC are new source packages, not a redesign.

## Data sources (v1)

| Source id | Dataset | Endpoint | Rows (Oct 2026) |
|---|---|---|---|
| `snhd` | SNHD restaurant inspections | `https://www.southernnevadahealthdistrict.org/restaurants/download/restaurants.zip` (refreshed nightly) | 35,904 establishments; 127,627 current + 204,179 legacy inspections |
| `ccbiz` | Clark County business licenses | `https://maps.clarkcountynv.gov/arcgis/rest/services/BusinessLicense/BusinessLicenseData/FeatureServer/1` | 90,019 |
| `clvlicense` | City of LV business licenses | `https://services1.arcgis.com/F1v0ufATbBQScMtY/arcgis/rest/services/Business_Licenses_OpenData/FeatureServer/0` | 214,049 |
| `clvpermit` | City of LV building permits | current: `…/OpenData_Building_Permits_/FeatureServer/0`; archive: `…/Bldg_Permits/FeatureServer/379` (same `services1` org) | 26,000 current; 1,807,326 archive |

The Strip is unincorporated Clark County; downtown is City of Las Vegas. Two jurisdictions publish overlapping data in different shapes, which makes entity resolution a v1 problem.

**Verified source facts (2026-10-14) that shape the code:**
- No v1 source has a usable last-edited field, so **every run is a full snapshot**. Watermarks and incremental fetch are dropped from v1.
- ArcGIS geometry is projected (Nevada State Plane); queries request `outSR=4326`. Page sizes: Clark County 10,000, CLV 1,000/2,000. Paging is keyset on the object-id field, not offsets. Object ids are not stable identities and are excluded from payloads.
- `ccbiz`: credential numbers are unique (`1001277.LIQ-107`); the part before the `.` is shared by all of one business's licenses. Dates are `YYYYMMDD`. Has parcel numbers and geometry; some rows are out-of-state mailing addresses.
- Clark County's separate liquor layer is dropped: 504 of its 518 license numbers are already in `ccbiz`.
- `clvlicense`: no geometry, no issue date. Its timeline events are dated by when city-pulse first saw them. One row per owner/officer, so 214,049 rows fold into 149,601 license records with an `owners` list; 101 rows have no license number.
- `clvpermit`: the archive layer stops in January 2026; current permits are the separate layer. Same `APNO` namespace — current wins on overlap. Archive dates are `DD-MON-YY`; current dates are epoch milliseconds. Both carry parcel ids (`PRCLID`). Archive rows with empty `APNO` (~72k) are blank and skipped by query. The archive repeats identical rows up to 24× (content hashing collapses them) and holds several lifecycle snapshots of one permit (`Accepted` → `Issued` → `Closed`): 1,807,596 rows → 534,185 versions of 426,701 permits. Ingest keeps every distinct version; resolve picks the most advanced one per permit.
- `snhd`: semicolon-separated with a trailing `;`, not RFC CSV — quotes are literal data. 54 establishment names contain bare `;` (mirrored in the trailing `search_text` column), repairable when the extra field count is even. The `latitude` column holds longitude and vice versa. Inspections from March 2017 to September 2020 are missing from the published data. 342 inspected permits have no establishment row.

## Domain model

- **Source** — one connector per dataset: fetch, detect new/changed rows, map a raw row to an observation.
- **Observation** — one immutable version of a source record: source, source record id, content hash, raw payload (JSONB), plus parsed fields: kind (`license`, `inspection`, `permit`), name, street address as published, city, zip, parcel number, business key (a source-scoped id naming one operator across records), category, status, coordinates if present, start date and end date.
- **Address** — normalized canonical form (number, street, unit, city) and a point.
- **Place** — a physical location: one canonical address and point. Hosts many businesses over time.
- **Business** — a named operator at a place over a time span. "Joe's Bar LLC" and "JOES BAR" resolve to one business.
- **Link** — observation → business/place, with the rule that produced each (`exact`, `dir`, `near`; `key`, `name`, `prefix`); the rule is the confidence. Rebuilt on every resolve run.

Places and businesses are derived, never hand-edited. Improving the matcher means re-running resolve, not migrating data.

## Components

One Go module, one binary, subcommands `ingest`, `resolve`, `serve`, `nightly` (ingest then resolve).

```
cmd/citypulse/            main: subcommand dispatch
internal/source/          Source interface + registry
  arcgis/                 shared ArcGIS FeatureServer client (keyset paging)
  snhd/                   SNHD inspections (zip of semicolon files)
  clvlicense/             City of LV business licenses
  clvpermit/              City of LV building permits (current + archive)
  ccbiz/                  Clark County business licenses
internal/ingest/          runs sources, batches upserts, records runs and errors
internal/address/         parse + normalize street addresses
internal/resolve/         blocking, name matching, clustering, business spans
internal/store/           Postgres via pgx, embedded SQL migrations
internal/web/             HTTP server, html/template, search, place/business pages
```

**Interfaces**
- `Source`: `Name() string`, `Fetch(ctx, emit func(Record) error) error` (full snapshot), `Parse(Record) (Observation, error)`.
- `address.Normalize(string) (Address, error)` — pure, no I/O.
- `resolve` reads observations, writes places/businesses/links; shares nothing with `web` except the store.

**Decisions**
- Hand-written address parser, not libpostal (avoids cgo and a ~2 GB model; Vegas addresses are regular; better exercise; easier to debug).
- Go owns all matching logic; Postgres (with PostGIS and `pg_trgm`) owns storage, spatial indexing and search indexes.
- Frontend: server-rendered `html/template`, a Leaflet map vendored into the binary on the front, place and business pages. No SPA, no JS build step.

## Data flow

### Ingest (nightly)
1. Each source run is a row in `source_runs` (rows seen, new versions, parse errors, error text).
2. Every run fetches a full snapshot.
3. Each batch is `COPY`'d into a session temp table, then inserted keyed on `(source, record_id, hash)` with `ON CONFLICT DO NOTHING`: same hash → no write at all; new hash → new version of the record (change detection). Seen keys collect in a second temp table; after a successful fetch, records the run did not see → marked `withdrawn`, never deleted. Unchanged rows are never rewritten, so a nightly full snapshot does not bloat the table.
4. Withdrawal guard: if a run sees fewer than half the rows of the source's last successful run, withdrawal is skipped and the run is marked failed — an upstream outage that returns an empty layer must not withdraw everything.
5. Sources fail independently.

### Resolve (after ingest; full deterministic rebuild)
Input is the **current version** of every source record: the one from the latest run, and among versions first seen in the same run (the permit archive's lifecycle snapshots) the most advanced status. Older versions stay in `observations`.

1. **Normalize** — address key `number|dir|street|unit`, street type left out because agencies omit it as often as they publish it. Name: lowercase, strip legal suffixes (LLC, INC, CORP, …) and SNHD's `- DELETED`, a `dba` name wins, `&` → `and`, drop punctuation.
2. **Places** — group observations by address key. A missing direction is filled when exactly one is seen at that number and street. A missing unit is never guessed: a unit-less record belongs to the whole building, whose units share its key as a prefix (measured: guessing pulled whole buildings into junk units). Unparseable address with coordinates → nearest place within 60 m.
3. **Businesses** — within a place, union-find over: a shared source business key; names equal ignoring spacing or word order (`SPORTSBAR`/`SPORTS BAR`, 156 such pairs); one name a whole-word prefix of another, shorter name ≥ 5 characters (`SUBWAY`/`SUBWAY #27601`, SNHD's per-station permits; ~4,100 pairs). Records with no address cluster by business key alone. Each business spans its first to last observation; it is closed when every record carries a closed status or an end date.
4. **Write** — `TRUNCATE` and `COPY` `places`, `businesses`, `links` in one transaction. The site never sees a partial state. IDs are derived from content (place ID = address key; business ID = place ID + smallest business key), so URLs survive rebuilds.

A full rebuild takes about a minute at ~1M current records. Incremental resolution is the upgrade path if it stops being enough; not built until needed. PostGIS is not needed for resolve (the 60 m lookup is an in-memory grid); it arrives with serve if the map needs it.

A manual overrides table (force-merge / force-split pins, applied last) is added once real mismatches appear on the public site, not before.

### Serve
Reads only the swapped-in tables. `citypulse serve [-addr :8080]`; one embedded `html/template` page, plus Leaflet 1.9.4 and two OFL fonts vendored into the binary (no CDN, no build step) and served under content-hashed `/static/` URLs.
- **Front page** — a clickable map of every building with records. `pins` is a materialized view, refreshed by the resolve write, with one pin per building (its units share it). `/map.json?z&s&w&n&e` clusters pins into ~64 px grid cells below zoom 17 (a dense zoom-15 view holds ~17,000 buildings) and returns them one by one, with their place, from 17 up (≤ ~1,500 in the densest view). The valley-wide cluster query is ~90 ms; clusters carry no text, because text aggregates tripled it. Search works without JavaScript.
- **Search** — a query that parses as an address matches every unit at that number and street (prefix match on the place key, any direction if omitted); every query also matches business names as substrings of a punctuation-free `search` column (`joes` finds `JOE'S BAR`), ranked by `pg_trgm` similarity. 50 results each.
- **Place page** — the same map, inset at the place with its neighbours clickable; businesses here with spans, the building it is a unit of or the units it contains, a succession strip (the newest 30 *dated* businesses as bars on one time axis, oldest first, none with fewer than two; undated City licenses only span the night city-pulse first saw them), and the newest 300 records, each linking its record page.
- **Business page** — span, place, newest 300 records.
- **Record page** — `/record/{source}/{id}`: every stored version newest first, dated by the night it was first seen, with the payload fields that changed from the version before (the first version lists them all), and whether the source has stopped publishing it. A record with neither name nor address on its newest version is headed by its record id (843 in production).
- **What changed** — `/changes`: up to 500 changes from the last 7 days of runs, by night; its last night is marked partial when the cap cuts through it. `/changes.atom` the newest 100; `/feed/place/{id}` and `/feed/business/{id}` the newest 50. A change is a version stored by a run after its source's first successful run (the initial load is not a change), new or updated by whether an earlier version exists, or a withdrawal. Versions are windowed by run (`observations (source, first_run_id)`), because a new source's initial load is also recent; withdrawals by `withdrawn_at` time. The citywide query reads observations through a `LATERAL` subquery with `OFFSET 0` as an optimization fence: without it, once `source_runs` has planner statistics, Postgres hash-joins and sequentially scans all of `observations` (970 ms against 28 ms with an analyzed `source_runs`). Entries link absolute URLs and have `tag:` ids on the observation id. The front page advertises `/changes.atom` for feed autodiscovery; place and business pages advertise their own feeds.
- **Sources** — `/sources`: each source's coverage, known gaps and latest run.
- `Cache-Control: public, max-age=3600` (data changes nightly); strict CSP (`default-src 'none'`; scripts, fonts and fetches same-origin only; OpenStreetMap tiles the only third-party content); 5 s query timeouts.
- `/metrics` (per-source last success, last run ok, rows seen/new, parse errors) and `/healthz` (database ping).

Measured on the full data: search 5–12 ms, the busiest place page (3,660 records) 65 ms; `/changes` ≈ 40 ms, its feed ≈ 27 ms, a place or business feed ≤ 42 ms, a record page 2 ms.

## Deployment (THALES)

Manifests: `apps/city-pulse/` plus shared-infrastructure entries in `thales-kubernetes` (PR #24).
- Namespace `city-pulse`: Postgres 16 (`postgres:16-alpine`, pg_trgm; no PostGIS needed) on a 10Gi `ceph-rbd` PVC with `pg_dump` at 02:15; `serve` Deployment ×2 with a ServiceMonitor; CronJob `citypulse nightly` at 03:00 America/Los_Angeles (768Mi request / 1536Mi limit, from a measured 880 MB resolve peak).
- Build: `Dockerfile` (distroless static, uid 65532) and `Jenkinsfile` (go test, kaniko → `10.10.20.31:5000/city-pulse:<sha>`, trivy, then rewrite the two `# city-pulse-image` lines on `thales-kubernetes` main). Secrets via Vault/ESO at `secret/city-pulse/postgres`.
- Public exposure: IngressRoute for `citypulse.jasonharker.com` and `citypulse.thales.local` with a 60/min-per-client rate limit, a cloudflared entry, and a Cloudflare CNAME to the tunnel (both, per `L#118`).
- Network: ingress from traefik and monitoring on 8080 only; egress DNS and same-namespace for all pods, world :443 only for pods labelled `app: citypulse-nightly`.
- Alert `CityPulseSourceStale` (in `monitoring`): a source without a successful ingest for 3 days.

## Failure handling

- One source failing never blocks the others.
- Parse failures keep the raw row and write to `parse_errors`; nothing is silently dropped. Per-source parse-error rate is a metric, so an upstream format change shows up as a spike.
- Resolve failure → no swap; the site serves the previous tables.
- Prometheus metrics per source: rows fetched, new versions, parse failures, last success time. A CronJob can't be scraped, so `serve` exposes these on `/metrics` by reading `source_runs`. One alert, `CityPulseSourceStale`: any source without a success in 3 days → existing AlertManager → Discord.

## Testing

- `address` — golden corpus of real addresses sampled from every source with expected normalized output. The most important test file in the repo.
- `source/*` — parse tests against recorded fixture payloads; no network in tests.
- `resolve` — small fixture datasets with expected place/business clusters, including storefront succession and strip-mall units.
- `store` / `web` — tests run against a real Postgres + PostGIS in CI, not mocks.

## Research sources

- SNHD developers page: https://www.southernnevadahealthdistrict.org/permits-and-regulations/restaurant-inspections/developers/
- SNHD inspections on the CLV portal: https://opendataportal-lasvegas.opendata.arcgis.com/datasets/c630117f44774db8814ab88c8ec97853_0/about
- CLV building permits: https://opendataportal-lasvegas.opendata.arcgis.com/datasets/building-permits-
- CLV business licenses: https://opendataportal-lasvegas.opendata.arcgis.com/datasets/clv-business-licenses
- Clark County BusinessLicense services: https://maps.clarkcountynv.gov/arcgis/rest/services/BusinessLicense
- Clark County Legistar: https://clark.legistar.com/
- NV SOS bulk data status: https://www.nvsos.gov/sosservices/AnonymousAccess/HelpGuides/DataDownloadUserGuide.aspx

## Build sequence

One implementation plan per subsystem, each ending in working, tested software:

1. **Ingest** — module skeleton, store and migrations, ArcGIS client, the four sources, `citypulse migrate` / `citypulse ingest`. Done when a live run fills `observations` from all four sources.
2. **Resolve** — `address` package and golden corpus, name normalization, place/business clustering, table swap, `citypulse resolve` / `nightly`.
3. **Serve** — search, place and business pages, map, `/metrics`.
4. **Deploy** — THALES manifests via `/thales-deploy`, Jenkins pipeline, public exposure.
