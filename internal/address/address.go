// Package address turns a street address as an agency published it into a
// canonical form whose Key is equal for every spelling of the same place.
package address

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

// Address is a parsed street address. All fields are upper case.
type Address struct {
	Number string // "3570"
	Dir    string // "N", "S", "E", "W" or ""
	Street string // street name without type or direction: "LAS VEGAS"
	Type   string // canonical street type: "BLVD"; not part of Key
	Unit   string // "165", "A", ""
}

// Key identifies the place. Street type is left out because agencies omit it
// as often as they publish it ("3725 W RUSSELL" vs "3725 W RUSSELL RD").
func (a Address) Key() string {
	return a.Number + "|" + a.Dir + "|" + a.Street + "|" + a.Unit
}

// ErrUnparseable means the text has no house number and street name.
var ErrUnparseable = errors.New("address: no house number and street")

var (
	dirs = map[string]string{"N": "N", "S": "S", "E": "E", "W": "W",
		"NORTH": "N", "SOUTH": "S", "EAST": "E", "WEST": "W"}
	types = map[string]string{
		"AVE": "AVE", "AV": "AVE", "AVENUE": "AVE",
		"BLVD": "BLVD", "BL": "BLVD", "BOULEVARD": "BLVD",
		"CIR": "CIR", "CIRCLE": "CIR",
		"CT": "CT", "COURT": "CT",
		"DR": "DR", "DRIVE": "DR",
		"HWY": "HWY", "HIGHWAY": "HWY",
		"LN": "LN", "LANE": "LN",
		"LOOP": "LOOP",
		"PKWY": "PKWY", "PKY": "PKWY", "PARKWAY": "PKWY",
		"PL": "PL", "PLACE": "PL",
		"RD": "RD", "ROAD": "RD",
		"ST": "ST", "STREET": "ST",
		"TER": "TER", "TERRACE": "TER",
		"TRL": "TRL", "TRAIL": "TRL",
		"WAY": "WAY",
	}
	// Words that start the unit part. Everything after the first one is the unit.
	unitWords = map[string]bool{"#": true, "STE": true, "SUITE": true, "UNIT": true, "APT": true,
		"SPC": true, "SPACE": true, "SP": true, "BLDG": true, "RM": true, "ROOM": true, "LOT": true, "NO": true, "KIOSK": true}
	words = map[string]string{"SAINT": "ST", "MOUNT": "MT", "FORT": "FT"}
	// Whole street names that agencies spell several ways.
	aliases = map[string]string{
		"MARTIN LUTHER KING": "MLK", "M L KING": "MLK", "ML KING": "MLK",
	}
	number   = regexp.MustCompile(`^(\d+)(?:-?([A-Z]))?$`) // "4190", "4190B", "4190-B"
	paren    = regexp.MustCompile(`\([^)]*\)?`)            // "(KIOSK)", "(by Tommy Hilfiger)", or truncated "(by Tom"
	digits   = regexp.MustCompile(`^\d+$`)
	ordinal  = regexp.MustCompile(`^\d+(ST|ND|RD|TH)$`)
	hasDigit = regexp.MustCompile(`\d`)
	punct    = strings.NewReplacer(".", " ", ",", " ", "#", " # ", "|", " ") // "|" separates Key parts
)

// Normalize parses s. It is pure and never does I/O.
func Normalize(s string) (Address, error) {
	toks := slices.DeleteFunc(strings.Fields(punct.Replace(paren.ReplaceAllString(strings.ToUpper(s), " "))),
		func(t string) bool { return t == "NONE" }) // the current permits layer writes empty parts as "None"
	if len(toks) < 2 {
		return Address{}, ErrUnparseable
	}
	// A number range keeps its first number: "1017-1025 S 1ST", "1017- 1025 S 1ST".
	if first, ok := strings.CutSuffix(toks[0], "-"); ok && digits.MatchString(toks[1]) {
		toks = append([]string{first}, toks[2:]...)
	} else if first, last, ok := strings.Cut(toks[0], "-"); ok && digits.MatchString(last) {
		toks[0] = first
	}
	if len(toks) < 2 {
		return Address{}, ErrUnparseable
	}
	m := number.FindStringSubmatch(toks[0])
	if m == nil {
		return Address{}, ErrUnparseable
	}
	a := Address{Number: m[1], Unit: m[2]}
	rest := toks[1:]

	for i, t := range rest {
		if i > 0 && unitWords[t] { // "SPACE ODYSSEY AVE" is a street
			if u := unit(rest[i+1:]); u != "" {
				a.Unit = u
			}
			rest = rest[:i]
			break
		}
	}
	if len(rest) > 1 {
		if d, ok := dirs[rest[0]]; ok {
			a.Dir = d
			rest = rest[1:]
		}
	}
	// After the street type comes an optional direction, then a bare unit:
	// "LAS VEGAS BLVD SOUTH", "FLAMINGO RD S114 -117", "LAS VEGAS BLVD L27 A4".
	for i := 1; i < len(rest); i++ {
		t, ok := types[rest[i]]
		if !ok {
			continue
		}
		tail := rest[i+1:]
		if d, ok := dirs[first(tail)]; ok && a.Dir == "" {
			a.Dir = d
			tail = tail[1:]
		}
		// Only a tail that looks like a unit is one. Prose ("OUTSIDE OF 7-ELEVEN",
		// "10TH FLOOR") and second addresses ("917 FREMONT ST 927 FREMONT ST") are dropped.
		if len(tail) > 0 && !slices.ContainsFunc(tail, notUnit) {
			a.Unit = unit(tail)
		}
		a.Type, rest = t, rest[:i]
		break
	}
	// No street type: the unit starts at the first later token with a digit
	// ("1717 S DECATUR D61", "2605 S DECATUR 120 -122"). Ordinals and highway numbers are names.
	for i := 1; a.Type == "" && i < len(rest); i++ {
		if hasDigit.MatchString(rest[i]) && !ordinal.MatchString(rest[i]) &&
			rest[i-1] != "HWY" && rest[i-1] != "HIGHWAY" && rest[i-1] != "ROUTE" {
			a.Unit = unit(rest[i:])
			rest = rest[:i]
			break
		}
	}
	if n := len(rest); n > 1 {
		if d, ok := dirs[rest[n-1]]; ok && a.Dir == "" { // "LAS VEGAS SOUTH", "LAS VEGAS S BLVD"
			a.Dir = d
			rest = rest[:n-1]
		}
	}
	if len(rest) == 0 {
		return Address{}, ErrUnparseable
	}
	for i, t := range rest {
		if v, ok := words[t]; ok {
			rest[i] = v
		}
	}
	a.Street = strings.Join(rest, " ")
	if v, ok := aliases[a.Street]; ok {
		a.Street = v
	}
	return a, nil
}

// notUnit reports whether a bare token after the street type can't be part of a unit.
// Units are "S114", "-117", "A", "&", "B/C"; words are not.
func notUnit(t string) bool {
	if hasDigit.MatchString(t) {
		return false
	}
	return slices.ContainsFunc(strings.FieldsFunc(t, func(r rune) bool { return r == '&' || r == '/' || r == '-' }),
		func(p string) bool { return len(p) > 2 })
}

func first(toks []string) string {
	if len(toks) == 0 {
		return ""
	}
	return toks[0]
}

// unit joins unit tokens, dropping designators, and strips leading zeros.
func unit(toks []string) string {
	var parts []string
	for _, t := range toks {
		if !unitWords[t] {
			parts = append(parts, t)
		}
	}
	u := strings.Join(parts, "")
	if t := strings.TrimLeft(u, "0"); t != "" {
		u = t
	}
	return u
}
