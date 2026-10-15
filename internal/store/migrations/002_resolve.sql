-- Derived by resolve and replaced wholesale on every run. Never edited by hand.
CREATE TABLE places (
    id      text PRIMARY KEY, -- address key: number|dir|street|unit
    address text NOT NULL,
    lat     double precision,
    lon     double precision
);

CREATE TABLE businesses (
    id         text    PRIMARY KEY,
    place_id   text    REFERENCES places (id), -- null: the source published no usable address
    name       text    NOT NULL,
    first_date date    NOT NULL,
    last_date  date    NOT NULL,
    closed     boolean NOT NULL
);
CREATE INDEX businesses_place_idx ON businesses (place_id);

-- One row per current record version; older versions stay in observations only.
CREATE TABLE links (
    observation_id bigint PRIMARY KEY REFERENCES observations (id),
    place_id       text   REFERENCES places (id),
    place_rule     text   NOT NULL,
    business_id    text   REFERENCES businesses (id),
    business_rule  text   NOT NULL
);
CREATE INDEX links_place_idx ON links (place_id);
CREATE INDEX links_business_idx ON links (business_id);
