// Package model holds city-pulse's domain types: what resolve reads, what it
// derives, and the rules that justify each link.
package model

import "time"

// Obs is the current version of one source record.
type Obs struct {
	ID          int64
	Name        string
	Address     string
	BusinessKey string
	Status      string
	Lat, Lon    *float64
	Date        time.Time  // start date, or when city-pulse first saw the record
	End         *time.Time // end date if the source has one
	Parcel      string     // Clark County APN, digits only, or ""
	Zip         string     // 5 digits or ""
	City        string
	Owners      []string // owners and officers; City of Las Vegas licenses only
}

// Place is one physical location. ID is its address key, stable across rebuilds.
type Place struct {
	ID       string
	Address  string // the most common published spelling
	Lat, Lon *float64
}

// Business is one operator at one place (or at no known place) over a span.
// ID is the place ID plus the smallest business key among its records.
type Business struct {
	ID, PlaceID, Name string
	First, Last       time.Time
	Closed            bool
}

// Link ties an observation to its place and business, naming the rule that did it.
type Link struct {
	ObservationID            int64
	PlaceID, PlaceRule       string
	BusinessID, BusinessRule string
}

// Place rules, most to least certain.
const (
	RuleExact  = "exact"  // the address key as published
	RuleDir    = "dir"    // direction filled in: the only one seen at that number and street
	RuleNear   = "near"   // unparseable address, nearest place by coordinates
	RuleKey    = "key"    // business: shares a source business key
	RuleName   = "name"   // business: same name at the same place, ignoring spacing and word order
	RulePrefix = "prefix" // business: its name extends another's at the same place
)
