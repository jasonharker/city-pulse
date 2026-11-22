# city-pulse

Look up any Las Vegas address and see its history: every business license, building permit, health inspection and liquor license recorded there, merged across agencies into one timeline. A storefront that was a bar, then a vape shop, then a taco place shows that succession.

**Live:** [citypulse.jasonharker.com](https://citypulse.jasonharker.com)

Most of the difficulty is in the data, not the infrastructure. Four public datasets from three agencies describe the same buildings and businesses in different shapes: the Strip is unincorporated Clark County, downtown is the City of Las Vegas, and the health district inspects both. city-pulse normalizes their addresses, resolves records into places and businesses, and tracks every change night over night.

## Features

- **Map** of every building with records, clustered by zoom level.
- **Search** by address (every unit at a number and street) or by business name (fuzzy, punctuation-insensitive).
- **Place pages**: the businesses that have operated there, a succession strip on one time axis, and the building's units.
- **Record pages**: every stored version of a source record, with the fields that changed between versions.
- **What changed**: new, updated and withdrawn records by night, with Atom feeds for the whole city, a place, or a business.
- **Sources page**: each dataset's coverage, known gaps and latest run.

## Data sources

| Source | Publisher | Records |
|---|---|---|
| `snhd` | Southern Nevada Health District: restaurant inspections | ~36k establishments, ~330k inspections |
| `ccbiz` | Clark County: business licenses | ~90k |
| `clvlicense` | City of Las Vegas: business licenses | ~150k licenses |
| `clvpermit` | City of Las Vegas: building permits, current and archive | ~430k permits |

Every source is a full snapshot each night; none publishes a usable last-edited field. Some of what the parsers handle:

- The SNHD export is semicolon-separated with a trailing `;`, quotes are literal data, and its `latitude` column holds longitude.
- The permit archive repeats identical rows up to 24 times and holds several lifecycle snapshots of one permit (`Accepted` → `Issued` → `Closed`). 1.8M rows collapse to 534k versions of 427k permits.
- City licenses are one row per owner or officer, with no issue date and no geometry.

## How it works

One Go binary, one Postgres database, three stages:

1. **Ingest.** Each source streams a full snapshot into Postgres. Rows are content-hashed: an unchanged row is never rewritten, a changed one becomes a new version, and a record the source stops publishing is marked withdrawn, never deleted. If a run sees fewer than half the rows of the last good run, withdrawal is skipped and the run fails, so an upstream outage can't empty the site. Sources fail independently; parse failures are kept with their raw row and counted as a metric.
2. **Resolve.** A deterministic rebuild over the current version of every record (~1M), in about a minute. Addresses are normalized by a hand-written parser and grouped into places; a missing direction is filled only when it is unambiguous, and a missing unit is never guessed. Businesses within a place are merged with union-find over shared license keys, names equal up to spacing or word order, and whole-word name prefixes. Each stage is a step in an ordered pipeline, and each match records the rule that made it. Results swap in within one transaction, and IDs are derived from content, so URLs survive rebuilds.
3. **Serve.** Server-rendered `html/template`, a vendored Leaflet map, no JavaScript build step and no CDN. Search works without JavaScript. Strict CSP, `/metrics` for Prometheus and `/healthz`.

```
cmd/citypulse/        subcommands: migrate, ingest, resolve, serve, nightly
internal/source/      Source interface; one package per dataset, shared ArcGIS client
internal/ingest/      runs sources, batches upserts, records runs and errors
internal/address/     street address parser and normalizer (pure, no I/O)
internal/resolve/     the pipeline: place stages, business stages, clustering
internal/store/       Postgres via pgx, embedded SQL migrations
internal/web/         HTTP server, templates, map data, feeds
tools/devdb/          throwaway local Postgres for development and tests
```

## Running locally

`tools/devdb` runs a throwaway Postgres with no root, Docker or system packages, and prints the two connection strings to export:

```sh
cd tools/devdb && go run .          # leave running; data lives in .devdb/
```

In another shell, from the repo root:

```sh
export DATABASE_URL=...                 # printed by devdb
go run ./cmd/citypulse ingest           # all sources; -only snhd,ccbiz for a subset
go run ./cmd/citypulse resolve
go run ./cmd/citypulse serve            # http://localhost:8080
```

`nightly` runs ingest then resolve, and still resolves if a source failed. Every subcommand applies pending migrations first.

Tests:

```sh
go test ./...                           # database tests skip without the next line
export CITYPULSE_TEST_DATABASE_URL=...  # printed by devdb; tests use throwaway schemas
```

## Deployment

city-pulse runs on my homelab Kubernetes cluster. Jenkins vets and tests each new `main`, builds a distroless image with kaniko, scans it with Trivy, and bumps the image tag in the GitOps repo, which Flux applies. In the cluster, two `serve` replicas sit behind Traefik and a Cloudflare tunnel with per-client rate limiting, a nightly CronJob runs ingest and resolve, Postgres is backed up daily, and an alert fires if any source goes three days without a successful ingest. The manifests are in [`apps/city-pulse/`](https://github.com/jasonharker/thales-kubernetes-public/tree/main/apps/city-pulse) of the public mirror of that repo.

## Design docs

The design spec and implementation plans, including the measurements behind each matching rule, are in [`docs/superpowers/`](docs/superpowers/).
