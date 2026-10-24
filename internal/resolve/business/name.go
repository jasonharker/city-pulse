package business

import (
	"strings"
	"unicode"
)

// Trailing words that carry no identity: "JOE'S BAR LLC" and "Joes Bar" are one
// name. SNHD appends "- DELETED" to retired permits.
var legal = map[string]bool{"llc": true, "inc": true, "corp": true, "corporation": true, "co": true,
	"company": true, "ltd": true, "lp": true, "llp": true, "pllc": true, "pc": true, "incorporated": true,
	"deleted": true}

// NormName reduces a business name to its identifying words. A "dba" name wins
// over the legal name: "SMITH HOLDINGS LLC DBA JOES BAR" -> "joes bar".
func NormName(s string) string {
	s = strings.ToLower(s)
	for _, sep := range []string{" d/b/a ", " dba ", " d b a "} {
		if _, after, ok := strings.Cut(s, sep); ok {
			s = after
		}
	}
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.NewReplacer("'", "", "’", "").Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	toks := strings.Fields(s)
	for len(toks) > 1 && legal[toks[len(toks)-1]] {
		toks = toks[:len(toks)-1]
	}
	if len(toks) > 1 && toks[0] == "the" {
		toks = toks[1:]
	}
	return strings.Join(toks, " ")
}
