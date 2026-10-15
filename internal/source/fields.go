package source

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Fields is a decoded record payload.
type Fields map[string]any

// Decode unmarshals a record payload, keeping numbers as json.Number so
// identifiers and coordinates survive without float rounding.
func Decode(r Record) (Fields, error) {
	d := json.NewDecoder(bytes.NewReader(r.Payload))
	d.UseNumber()
	var f Fields
	if err := d.Decode(&f); err != nil {
		return nil, err
	}
	return f, nil
}

// Str returns a field as trimmed text. Missing and null fields are "".
func (f Fields) Str(key string) string {
	switch v := f[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

// Float returns a numeric field, or nil if it is missing or not a number.
func (f Fields) Float(key string) *float64 {
	s := f.Str(key)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// Date parses s with layout. Empty or unparseable input returns nil:
// a missing date is normal in this data, not an error.
func Date(layout, s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return nil
	}
	return &t
}

// Zip5 returns the first five characters of a ZIP if they are digits, else "".
func Zip5(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 5 {
		return ""
	}
	for _, c := range s[:5] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return s[:5]
}

// Digits keeps only the ASCII digits of s.
func Digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r < '0' || r > '9' {
			return -1
		}
		return r
	}, s)
}

// Join joins the non-empty parts of ps with single spaces.
func Join(ps ...string) string {
	var out []string
	for _, p := range ps {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// Before returns s up to the first sep, or all of s if sep is absent.
func Before(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i]
	}
	return s
}

// Clean collapses whitespace runs to one space and trims.
func Clean(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// Southern Nevada, generously. Anything outside is a bad or swapped coordinate.
const (
	minLat, maxLat = 34.5, 37.5
	minLon, maxLon = -116.5, -113.5
)

// LatLon validates a claimed (lat, lon) pair for Southern Nevada. A pair that
// is valid only when swapped is returned swapped (SNHD publishes them reversed).
// Anything else, including (0, 0), returns nil, nil.
func LatLon(lat, lon *float64) (*float64, *float64) {
	if lat == nil || lon == nil {
		return nil, nil
	}
	in := func(a, b float64) bool { return a >= minLat && a <= maxLat && b >= minLon && b <= maxLon }
	switch {
	case in(*lat, *lon):
		return lat, lon
	case in(*lon, *lat):
		return lon, lat
	}
	return nil, nil
}
