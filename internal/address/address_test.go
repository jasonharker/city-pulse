package address

import "testing"

// Golden corpus: real addresses as the sources published them (2026-10-14).
func TestNormalize(t *testing.T) {
	for _, c := range []struct{ in, key, typ string }{
		// snhd
		{"3725 W RUSSELL", "3725|W|RUSSELL|", ""},
		{"9484 W FLAMINGO RD STE 165", "9484|W|FLAMINGO|165", "RD"},
		{"3650 S DECATUR BLVD STE 6-7", "3650|S|DECATUR|6-7", "BLVD"},
		{"2225 VILLAGE WALK #181", "2225||VILLAGE WALK|181", ""},
		{"1300 W SUNSET # 2433", "1300|W|SUNSET|2433", ""},
		{"1717 S DECATUR D61", "1717|S|DECATUR|D61", ""},
		{"2605 S DECATUR 120 -122", "2605|S|DECATUR|120-122", ""},
		{"11357 N Decatur BLVD BLDG SP04", "11357|N|DECATUR|SP04", "BLVD"},
		{"8665 W FLAMINGO RD S114 -117", "8665|W|FLAMINGO|S114-117", "RD"},
		{"3500 S LAS VEGAS BLVD L27 A4", "3500|S|LAS VEGAS|L27A4", "BLVD"},
		{"5875 S RAINBOW BLVD 105- 107", "5875|S|RAINBOW|105-107", "BLVD"},
		{"2660 W Centennial Parkway", "2660|W|CENTENNIAL|", "PKWY"},
		{"1830 N MARTIN LUTHER KING", "1830|N|MLK|", ""},
		{"1017- 1025 S 1ST ST STE 180", "1017|S|1ST|180", "ST"},
		{"4190-B S RAINBOW", "4190|S|RAINBOW|B", ""},
		{"18000 E Hwy 93", "18000|E|HWY 93|", ""},
		{"3440 SAINT ROSE PKWY STE 5 & 6", "3440||ST ROSE|5&6", "PKWY"},
		// clvlicense
		{"515 S 3rd St 110", "515|S|3RD|110", "ST"},
		{"917 Fremont St 927 Fremont St", "917||FREMONT|", "ST"},
		{"2411 W Sahara Ave PALACE STATION (KIOSK)", "2411|W|SAHARA|", "AVE"},
		{"1600 N Decatur BLVD OUTSIDE OF 7-ELEVEN", "1600|N|DECATUR|", "BLVD"},
		{"300 E 2nd Street 15th Floor", "300|E|2ND|", "ST"},
		{"2477 E TROPICANA AVE B/C", "2477|E|TROPICANA|B/C", "AVE"},
		{"875 S Grand Central Pkwy Kiosk C2 (by Tommy Hilfiger)", "875|S|GRAND CENTRAL|C2", "PKWY"},
		{"401 Ryland St Ste 0200A", "401||RYLAND|200A", "ST"},
		// clvpermit
		{"10800 SPACE ODYSSEY AVE", "10800||SPACE ODYSSEY|", "AVE"},
		{"5000 CAMINO DEL RANCHO None", "5000||CAMINO DEL RANCHO|", ""},
		{"3570 LAS VEGAS BLVD SOUTH", "3570|S|LAS VEGAS|", "BLVD"},
	} {
		a, err := Normalize(c.in)
		if err != nil || a.Key() != c.key || a.Type != c.typ {
			t.Errorf("Normalize(%q) = %q type %q, %v; want %q type %q", c.in, a.Key(), a.Type, err, c.key, c.typ)
		}
	}
	for _, in := range []string{"", "PO Box 750247", "Echo Bay", "HIGHWAY 93", "unknown Unknown"} {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q): want ErrUnparseable", in)
		}
	}
}
