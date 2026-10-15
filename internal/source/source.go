// Package source defines the contract every civic dataset connector implements.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// UserAgent identifies city-pulse to the agencies it fetches from.
const UserAgent = "city-pulse/0.1 (+https://citypulse.jasonharker.com)"

// Record is one row exactly as a source published it.
type Record struct {
	ID      string          // the source's own identifier for the row
	Payload json.RawMessage // canonical JSON: object keys sorted
}

// NewRecord marshals fields to canonical JSON. encoding/json sorts map keys,
// so an unchanged row always yields the same bytes and therefore the same hash.
func NewRecord(id string, fields map[string]any) (Record, error) {
	b, err := json.Marshal(fields)
	if err != nil {
		return Record{}, err
	}
	return Record{ID: id, Payload: b}, nil
}

// Hash identifies one version of a record.
func (r Record) Hash() string {
	sum := sha256.Sum256(r.Payload)
	return hex.EncodeToString(sum[:])
}

// Observation is the parsed, typed view of one record version.
// The ingest runner fills Source, RecordID, Hash and Payload; Parse fills the rest.
type Observation struct {
	Source   string
	RecordID string
	Hash     string
	Payload  json.RawMessage

	Kind        string // "license", "inspection" or "permit"
	Name        string
	Address     string // street line as published, not normalized
	City        string
	Zip         string // 5 digits or ""
	Parcel      string // Clark County APN, digits only, or ""
	BusinessKey string // source-scoped id naming one operator across records
	Category    string
	Status      string
	Lat, Lon    *float64
	StartDate   *time.Time
	EndDate     *time.Time
}

// Source is one civic dataset.
type Source interface {
	Name() string
	// Fetch streams a full snapshot of the dataset to emit.
	Fetch(ctx context.Context, emit func(Record) error) error
	// Parse maps one record to an observation. It must not do I/O.
	Parse(Record) (Observation, error)
}
