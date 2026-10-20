-- The change feed reads the versions recent runs stored, per source, and
-- withdrawals by when they happened.
CREATE INDEX observations_run_idx ON observations (source, first_run_id);
CREATE INDEX observations_withdrawn_idx ON observations (withdrawn_at) WHERE withdrawn_at IS NOT NULL;
