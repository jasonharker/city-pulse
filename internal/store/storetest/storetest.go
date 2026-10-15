// Package storetest gives tests a migrated store in a throwaway schema.
package storetest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jasonharker/city-pulse/internal/store"
)

// New returns a migrated store in a fresh schema of the database named by
// CITYPULSE_TEST_DATABASE_URL (a postgres:// URL), plus a connection on the
// same schema for assertions. The schema is dropped after the test.
// Without the variable, the test is skipped.
func New(t *testing.T) (*store.Store, *pgx.Conn) {
	t.Helper()
	base := os.Getenv("CITYPULSE_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("CITYPULSE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public") // public holds extensions (pg_trgm)
	u.RawQuery = q.Encode()
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return st, conn
}
