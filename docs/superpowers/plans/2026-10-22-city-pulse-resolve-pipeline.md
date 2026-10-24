# city-pulse Resolve Pipeline, Evaluation and Overrides Plan (Plan 6)

**Goal:** Make the codebase structurally rich for domain reasons. `resolve.Build` becomes a pipeline of stages over a shared graph; an evaluation harness scores it against hand-labeled pairs; new signals (parcel, owners) land only when the harness says they hold precision; manual merges and splits become the last stage.

**Architecture:** `internal/model` holds the domain types. `internal/resolve` splits into `graph`, `stage`, `place`, `business`, `cluster` and an ordered pipeline. Stages record `Edge`s with their rule; only `cluster` merges. `internal/eval` scores a pipeline against `testdata/pairs.csv` plus the observations behind them (`testdata/obs.json`), so it runs in CI without Postgres. `internal/override` applies stored corrections. Spec: `docs/superpowers/specs/2026-10-14-city-pulse-design.md`.

**Out of scope:** the JSON API and TypeScript frontend (next plan, on this model); the parse-error-as-withdrawal bug (separate PR).

**Decided with Jason, 2026-10-22:** the project's goal is a structurally rich codebase; the structure must come from features that genuinely interact, not wrappers. Order: pipeline and evaluation before API and frontend.

---

## Patterns to mirror

| Category | Source | Pattern |
|---|---|---|
| Naming | `internal/resolve/resolve.go:51` | Rule constants, commented most to least certain; the rule is the confidence |
| Purity | `internal/resolve/resolve.go:79` | No I/O; same input, same output, stable IDs |
| Determinism | `internal/resolve/resolve.go:335` | Union-find keeps the smaller root |
| Errors | `cmd/citypulse/main.go:132` | Wrap with the stage: `fmt.Errorf("resolve: load: %w", err)` |
| Logging | `cmd/citypulse/main.go:138` | One `slog` Info per stage with counts and `took` |
| Data access | `internal/store/resolve.go:21` | One query, `pgx.CollectRows`; writes by COPY in one transaction |
| Migrations | `internal/store/migrations/` | Numbered, embedded, advisory-locked |
| Tests | `internal/resolve/resolve_test.go:8` | Narrative fixture rows, expected `map[id]Link`; DB tests via `storetest.New(t)` |

## Phase 0 — freeze today's behaviour

- [x] Full ingest of the public sources into devdb (998,984 current records). A production copy was the first choice; reading production was not permitted, and the public sources give the same data.
- [x] `internal/resolve/golden_test.go` (`-tags golden`): load current observations from `DATABASE_URL`, run the frozen legacy `Build` and the current entry point, require identical `Result`s, and log the result's SHA-256.
- **Validate:** `go test -tags golden ./internal/resolve` passes before the refactor. Done: sha256 `ad784ecb0b8e7497`; a planted `minPrefix` change fails it.

## Phase 1 — domain model

- [x] `internal/model`: `Obs`, `Place`, `Business`, `Link`, rule constants. `Edge` lives in `resolve/graph`, the only place it is used.
- [x] `resolve.Result` keeps its shape; `ReplaceResolved` unchanged.
- Changed: `Obs` does not carry parcel, zip, city or owners. Added in phase 1 they cost 72 bytes a record (~90 MB at 1M records) and no stage read them yet; phase 4 loads them as side data for the records that have them.
- **Validate:** `go vet ./... && go test ./...`; golden identical.

## Phase 2 — pipeline

- [x] `resolve/graph`: observations, place assignment and rule, edges, and `Stage` (a package of its own would hold one interface). No parsed addresses: `dir` reads the exact placements' IDs, and `address.Normalize` now reads "|" as a space so IDs split cleanly.
- [x] `resolve/place`: `exact`, `dir`, `near` (the 60 m grid moves here).
- [x] `resolve/business`: `key`, `name` (spacing and word order), `prefix`.
- [x] `resolve/cluster`: clusters, spans, business IDs.
- [x] `resolve/pipeline.go`: ordered stages, per-stage timings logged by `citypulse resolve`.
- Changed: the legacy copy stays until phase 4 ends; phase 4 reviews its golden diffs against it.
- **Validate:** golden identical; `TestBuild` unchanged; resolve ≤ 1.2× current time, peak ≤ 1 GB (limit 1536Mi). Done: identical; peak heap ~700 MB against ~760 MB before, peak live ~380 MB against ~400 MB; stages total ~8 s of resolve's ~80 s, the rest is load and write.

## Phase 3 — evaluation harness

- [ ] `citypulse label -n 400`: stratified candidate pairs (per rule, plus near-misses) → `internal/eval/testdata/pairs.csv` with an empty `same` column; the observations behind them → `obs.json`.
- [ ] Jason labels the pairs.
- [ ] `internal/eval`: precision and recall per rule against `baseline.json`; test fails below baseline.
- **Validate:** `go test ./internal/eval` passes in Jenkins and fails when a deliberately broken stage is injected.

## Phase 4 — new stages, gated by eval

- [ ] Parcel stage: merges places that normalize differently but share a parcel and lie within N m. Never invents a unit.
- [ ] Owner stage + `Operator`: migration `006_operators.sql` (`operators`, `businesses.operator_id`); links businesses across places through shared clvlicense owners, with a data-derived stop list of registered agents.
- [ ] Each ships only if no rule's precision drops below baseline; otherwise flagged off.
- **Validate:** eval report per stage; golden diff reviewed.

## Phase 5 — overrides

- [ ] Migration `007_overrides.sql`: `overrides(kind, a, b, note, created_at)`, keyed by `(source, record_id)`.
- [ ] `internal/override` as the last stage; `citypulse override merge|split|list`.
- [ ] Override pairs join the eval labels.
- **Validate:** an override survives two resolve runs; override pairs are labeled.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Refactor drifts business IDs (shared URLs break) | Medium | Golden compares IDs, not just clusters |
| Edges push resolve past 1536Mi (880 MB peak today) | Medium | Interned rules, small evidence; measure in Phase 2 |
| Owner linking over-merges through registered agents | High | Stop list, precision gate, off by default |
| Labeling 400 pairs is hours of Jason's time | Certain | Stratified sampling; start smaller |
| Parcel merging contradicts "no unit guessing" | Medium | Merges whole addresses only; eval is the new data |
