// Command devdb runs a throwaway Postgres 16 for local development and tests,
// with no root, Docker or system packages. Binaries and data live in .devdb/.
// It lives in its own module so the app never depends on it.
//
//	cd tools/devdb && go run . [-port 55432]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	ep "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

func main() {
	port := flag.Uint("port", 55432, "port to listen on")
	dir := flag.String("dir", "../../.devdb", "directory for binaries and data")
	flag.Parse()
	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	pg := ep.NewDatabase(ep.DefaultConfig().Version(ep.V16).Port(uint32(*port)).
		RuntimePath(filepath.Join(abs, "runtime")).DataPath(filepath.Join(abs, "data")).
		BinariesPath(filepath.Join(abs, "bin")).CachePath(filepath.Join(abs, "cache")))
	if err := pg.Start(); err != nil {
		log.Fatal(err)
	}
	defer pg.Stop()

	base := fmt.Sprintf("postgres://postgres:postgres@localhost:%d", *port)
	if err := createDB(base+"/postgres?sslmode=disable", "citypulse"); err != nil {
		log.Print(err)
		return
	}
	fmt.Printf("DATABASE_URL=%s/citypulse?sslmode=disable\n", base)
	fmt.Printf("CITYPULSE_TEST_DATABASE_URL=%s/postgres?sslmode=disable\n", base)
	fmt.Println("Ctrl-C to stop.")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
}

func createDB(url, name string) error {
	ctx := context.Background()
	c, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	var exists bool
	if err := c.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = c.Exec(ctx, "CREATE DATABASE "+name)
	return err
}
