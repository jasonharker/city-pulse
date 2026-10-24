//go:build golden

package resolve_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/jasonharker/city-pulse/internal/resolve"
	"github.com/jasonharker/city-pulse/internal/resolve/legacy"
	"github.com/jasonharker/city-pulse/internal/store"
)

// TestGolden resolves every current observation in DATABASE_URL with both the
// frozen legacy resolver and the current one, and requires identical results:
// same places, businesses, links and IDs. Run it against a full ingest:
//
//	DATABASE_URL=... go test -tags golden -run Golden -v ./internal/resolve
func TestGolden(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	obs, err := st.CurrentObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) == 0 {
		t.Fatal("no observations: ingest first")
	}
	lobs := make([]legacy.Obs, len(obs))
	for i, o := range obs {
		lobs[i] = legacy.Obs{ID: o.ID, Name: o.Name, Address: o.Address, BusinessKey: o.BusinessKey,
			Status: o.Status, Lat: o.Lat, Lon: o.Lon, Date: o.Date, End: o.End}
	}

	want, got := canonical(t, legacy.Build(lobs)), canonical(t, resolve.Build(obs))
	if !reflect.DeepEqual(want, got) {
		diffResults(t, want, got)
	}
	sum := sha256.Sum256(must(json.Marshal(got)))
	t.Logf("%d observations, %d places, %d businesses, %d links, sha256 %x",
		len(obs), len(got.Places), len(got.Businesses), len(got.Links), sum[:8])
}

// result is a resolve result in plain types, so the legacy and current
// packages' structs compare field by field.
type result struct {
	Places     []map[string]any
	Businesses []map[string]any
	Links      []map[string]any
}

// canonical round-trips v through JSON: exported fields, by name.
func canonical(t *testing.T, v any) result {
	t.Helper()
	var r result
	if err := json.Unmarshal(must(json.Marshal(v)), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func diffResults(t *testing.T, want, got result) {
	t.Helper()
	for _, part := range []struct {
		name      string
		want, got []map[string]any
	}{{"places", want.Places, got.Places}, {"businesses", want.Businesses, got.Businesses}, {"links", want.Links, got.Links}} {
		if len(part.want) != len(part.got) {
			t.Errorf("%s: %d legacy, %d current", part.name, len(part.want), len(part.got))
		}
		shown := 0
		for i := 0; i < min(len(part.want), len(part.got)) && shown < 10; i++ {
			if !reflect.DeepEqual(part.want[i], part.got[i]) {
				t.Errorf("%s[%d]:\n legacy  %v\n current %v", part.name, i, part.want[i], part.got[i])
				shown++
			}
		}
	}
	t.FailNow()
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
