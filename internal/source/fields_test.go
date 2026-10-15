package source

import (
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func TestRecordHashIsStableAcrossKeyOrder(t *testing.T) {
	a, err := NewRecord("1", map[string]any{"b": 2, "a": "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRecord("1", map[string]any{"a": "x", "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() != b.Hash() {
		t.Fatalf("hash differs: %s vs %s", a.Hash(), b.Hash())
	}
	if string(a.Payload) != `{"a":"x","b":2}` {
		t.Fatalf("payload = %s", a.Payload)
	}
}

func TestDecodeKeepsNumbersExact(t *testing.T) {
	r, _ := NewRecord("1", map[string]any{"id": 13816415069, "name": "  Joe's  ", "nothing": nil})
	f, err := Decode(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Str("id"); got != "13816415069" {
		t.Errorf("Str(id) = %q", got)
	}
	if got := f.Str("name"); got != "Joe's" {
		t.Errorf("Str(name) = %q", got)
	}
	if got := f.Str("nothing"); got != "" {
		t.Errorf("Str(nothing) = %q", got)
	}
	if got := f.Float("name"); got != nil {
		t.Errorf("Float(name) = %v, want nil", *got)
	}
}

func TestDate(t *testing.T) {
	cases := []struct {
		layout, in string
		want       string // "" means nil
	}{
		{"20060102", "20220214", "2022-02-14"},
		{"2006-01-02 15:04:05", "2023-08-16 00:00:00", "2023-08-16"},
		{"02-Jan-06", "05-JAN-26", "2026-01-05"}, // month match is case-insensitive
		{"02-Jan-06", "15-MAR-85", "1985-03-15"}, // two-digit years >= 69 are 19xx
		{"20060102", "", ""},
		{"20060102", "garbage", ""},
	}
	for _, c := range cases {
		got := Date(c.layout, c.in)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("Date(%q, %q) = %v, want nil", c.layout, c.in, got)
		case c.want != "" && (got == nil || got.Format(time.DateOnly) != c.want):
			t.Errorf("Date(%q, %q) = %v, want %s", c.layout, c.in, got, c.want)
		}
	}
}

func TestZip5DigitsJoinBeforeClean(t *testing.T) {
	if got := Zip5("89101-5600"); got != "89101" {
		t.Errorf("Zip5 = %q", got)
	}
	if got := Zip5("AS VEGAS"); got != "" {
		t.Errorf("Zip5 = %q", got)
	}
	if got := Digits("178-06-101-019"); got != "17806101019" {
		t.Errorf("Digits = %q", got)
	}
	if got := Join(" 4161 S Eastern Ave ", "", "#A-3"); got != "4161 S Eastern Ave #A-3" {
		t.Errorf("Join = %q", got)
	}
	if got := Before("1001277.LIQ-107", "."); got != "1001277" {
		t.Errorf("Before = %q", got)
	}
	if got := Clean("  9110 Las Vegas  Blvd S \t"); got != "9110 Las Vegas Blvd S" {
		t.Errorf("Clean = %q", got)
	}
}

func TestLatLon(t *testing.T) {
	lat, lon := LatLon(f64(36.16939030), f64(-115.14308440))
	if lat == nil || *lat != 36.16939030 || *lon != -115.14308440 {
		t.Errorf("valid pair changed: %v %v", lat, lon)
	}
	lat, lon = LatLon(f64(-115.14308440), f64(36.16939030)) // SNHD order
	if lat == nil || *lat != 36.16939030 || *lon != -115.14308440 {
		t.Errorf("swapped pair not repaired: %v %v", lat, lon)
	}
	if lat, lon = LatLon(f64(0), f64(0)); lat != nil || lon != nil {
		t.Errorf("(0,0) accepted")
	}
	if lat, lon = LatLon(nil, f64(1)); lat != nil || lon != nil {
		t.Errorf("nil accepted")
	}
}
