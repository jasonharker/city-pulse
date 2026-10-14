# city-pulse — Design

**Date:** 2026-10-14
**Status:** Approved design, pre-implementation
**Public URL:** `https://citypulse.jasonharker.com`
**Module:** `github.com/jasonharker/city-pulse`

## Purpose

A public, read-only website where anyone can look up any Las Vegas place and see its history: every business license, building permit, health inspection and liquor license recorded there, merged across agencies into one timeline. A storefront that was a bar, then a vape shop, then a taco place shows that succession.

The project is also a deliberate programming exercise: a Go codebase whose complexity comes from the domain (messy multi-agency data, address normalization, entity resolution), not from infrastructure.

## Scope

**v1:** five structured sources, place and business pages, search, nightly refresh, public exposure.

**Not in v1:**
- Council/commission agendas (unstructured text — a separate sub-project; Clark County's Legistar Web API is the likely starting point).
- Nevada Secretary of State entity filings (bulk download is paid and currently disabled pending the state's ORION system).
- User accounts, watches, alerts.
- City-wide change feed.
- Other cities. The `Source` interface is designed so Chicago/NYC are new source packages, not a redesign.

## Data sources (v1)

| Source | Jurisdiction | Access |
|---|---|---|
| SNHD restaurant inspections | Southern Nevada (both) | Nightly-refreshed CSV from the SNHD developers page; 2009–present; summaries, not full reports |
| CLV business licenses | City of Las Vegas | ArcGIS open-data portal, ~214k records since 2013, daily updates |
| CLV building permits | City of Las Vegas | ArcGIS open-data portal, ~440k records, includes coordinates |
| Clark County business licenses | Unincorporated Clark County | ArcGIS REST `BusinessLicense/BusinessLicenseData` |
| Clark County liquor establishments | Unincorporated Clark County | ArcGIS REST `BusinessLicense/LiquorLicenseEstablishments` |

The Strip is unincorporated Clark County; downtown is City of Las Vegas. Two jurisdictions publish overlapping data in different shapes, which makes entity resolution a v1 problem.

**Verify per source at implementation time (not assumed here):** exact download URL, ArcGIS max page size, which field supports incremental `since` filtering, whether the SNHD CSV carries coordinates, and the source's terms of use.

## Domain model

- **Source** — one connector per dataset: fetch, detect new/changed rows, map a raw row to an observation.
- **Observation** — one immutable source record: source, source record id, content hash, raw payload (JSONB), plus parsed fields: name, address string, coordinates if present, event date, event type (`license.issued`, `license.closed`, `inspection`, `permit.issued`, …).
- **Address** — normalized canonical form (number, street, unit, city) and a point.
- **Place** — a physical location: one canonical address and point. Hosts many businesses over time.
- **Business** — a named operator at a place over a time span. "Joe's Bar LLC" and "JOES BAR" resolve to one business.
- **Link** — observation → business/place, with confidence and the rule that produced it. Rebuilt on every resolve run.

Places and businesses are derived, never hand-edited. Improving the matcher means re-running resolve, not migrating data.

## Components

One Go module, one binary, subcommands `ingest`, `resolve`, `serve`, `nightly` (ingest then resolve).

```
cmd/citypulse/            main: subcommand dispatch
internal/source/          Source interface + registry
  arcgis/                 shared ArcGIS FeatureServer client (paging, since-filter)
  snhd/                   SNHD inspections (CSV)
  clvlicense/             City of LV business licenses
  clvpermit/              City of LV building permits
  ccbiz/                  Clark County business licenses
  ccliquor/               Clark County liquor establishments
internal/address/         parse + normalize street addresses
internal/resolve/         blocking, name matching, clustering, business spans
internal/store/           Postgres via pgx, embedded SQL migrations
internal/web/             HTTP server, html/template, search, place/business pages
```

**Interfaces**
- `Source`: `Name() string`, `Fetch(ctx, since)` → stream of raw records, `Parse(raw) (Observation, error)`.
- `address.Normalize(string) (Address, error)` — pure, no I/O.
- `resolve` reads observations, writes places/businesses/links; shares nothing with `web` except the store.

**Decisions**
- Hand-written address parser, not libpostal (avoids cgo and a ~2 GB model; Vegas addresses are regular; better exercise; easier to debug).
- Go owns all matching logic; Postgres (with PostGIS and `pg_trgm`) owns storage, spatial indexing and search indexes.
- Frontend: server-rendered `html/template`, a small MapLibre or Leaflet map from a CDN on place pages. No SPA, no JS build step.

## Data flow

### Ingest (nightly)
1. Each source has a watermark (last successful fetch) in `source_runs`.
2. Fetch incrementally since the watermark where the source supports it. The Sunday run (and every run for a source with no usable incremental field) does a full snapshot instead, to catch edits and deletions incremental fetches miss.
3. Upsert keyed on `(source, source_record_id, content_hash)`: same hash → no-op; new hash → new version of the record (change detection); a record absent from a full snapshot → marked `withdrawn`, never deleted.
4. Sources fail independently; a failed source records its error and keeps its old watermark.

### Resolve (after ingest; full deterministic rebuild)
1. **Normalize** — address key (number + street + unit); name: lowercase, strip legal suffixes (LLC, INC, CORP, …), split `dba`, `&` → `and`, drop punctuation.
2. **Places** — group observations by address key. A unit-less record at an address with exactly one unit attaches to that unit with lower confidence. Unparseable address with coordinates → nearest place within a small radius.
3. **Businesses** — within a place: shared identifiers (license numbers) merge outright; otherwise name similarity (Jaro-Winkler or token-set over a threshold), clustered with union-find. The metric and threshold are chosen by measuring against the `resolve` fixture datasets, not fixed in advance. Each business gets an active span from first to last observation, closed early by a "closed" license status.
4. **Write** — build fresh `places`, `businesses`, `links` tables, swap in one transaction. The site never sees a partial state.

Full rebuild is expected to take minutes at ~1M observations. Incremental resolution is the upgrade path if it doesn't; not built until needed.

A manual overrides table (force-merge / force-split pins, applied last) is added once real mismatches appear on the public site, not before.

### Serve
Reads only the swapped-in tables: name/address search (`pg_trgm`), place pages (timeline + map), business pages.

## Deployment (THALES)

- One namespace: Postgres 16 + PostGIS (hand-rolled, `ceph-rbd` PVC, nightly `pg_dump` CronJob); `serve` Deployment, 2 replicas; one CronJob running `citypulse nightly`.
- Build: Jenkins → in-cluster `registry` → Flux image bump. Secrets via Vault/ESO.
- Public exposure: IngressRoute + cloudflared ingress entry + Cloudflare CNAME to the tunnel (both, per `L#118`). Traefik rate-limit middleware on the public search.
- World egress only for the CronJob pods; `serve` has none.
- Scaffold with `/thales-deploy` when the service is ready to ship.

## Failure handling

- One source failing never blocks the others.
- Parse failures keep the raw row and write to `parse_errors`; nothing is silently dropped. Per-source parse-error rate is a metric, so an upstream format change shows up as a spike.
- Resolve failure → no swap; the site serves the previous tables.
- Prometheus metrics per source: rows fetched, new versions, parse failures, last success time. One alert: any source without a success in 3 days → existing AlertManager → Discord.

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
