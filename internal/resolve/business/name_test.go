package business

import "testing"

func TestNormName(t *testing.T) {
	for in, want := range map[string]string{
		"JOE'S BAR & GRILL, LLC":          "joes bar and grill",
		"SMITH HOLDINGS INC DBA JOES BAR": "joes bar",
		"The Venetian Resort":             "venetian resort",
		"THE":                             "the",
		"BACON BAR PUB - DELETED":         "bacon bar pub",
		"GIANNA’S PIZZA":                  "giannas pizza",
	} {
		if got := NormName(in); got != want {
			t.Errorf("NormName(%q) = %q, want %q", in, got, want)
		}
	}
}
