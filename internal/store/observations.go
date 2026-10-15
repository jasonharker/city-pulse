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
