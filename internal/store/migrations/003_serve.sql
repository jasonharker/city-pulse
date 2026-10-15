-- Name search: substring matches on a punctuation-free copy of the name
-- ("joes" finds "JOE'S BAR"), ranked by trigram similarity.
-- Extensions are database-wide; pinning it to public keeps it out of the
-- per-test schemas, which are dropped.
CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;
ALTER TABLE businesses ADD COLUMN search text NOT NULL DEFAULT '';
CREATE INDEX businesses_search_trgm_idx ON businesses USING gin (search gin_trgm_ops);
-- Address search and "units in this building" are prefix matches on the place key.
CREATE INDEX places_id_prefix_idx ON places (id text_pattern_ops);
