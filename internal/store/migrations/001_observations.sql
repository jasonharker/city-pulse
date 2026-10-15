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
