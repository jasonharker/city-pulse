-- The change feed reads each source's newest versions by run, and the newest
-- withdrawals by when they happened.
CREATE INDEX observations_run_idx ON observations (source, first_run_id, id);
CREATE INDEX observations_withdrawn_idx ON observations (withdrawn_at, id) WHERE withdrawn_at IS NOT NULL;
