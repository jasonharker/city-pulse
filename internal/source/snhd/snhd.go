// Package snhd reads Southern Nevada Health District restaurant inspections
// from the district's nightly zip of semicolon-separated tables.
package snhd

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jasonharker/city-pulse/internal/source"
)

const zipURL = "https://www.southernnevadahealthdistrict.org/restaurants/download/restaurants.zip"

// The zip is ~9 MB; anything far larger means the URL now serves something else.
const maxZip = 200 << 20

// Inspection tables: current system (2020-) and the legacy one (2005-2017).
var inspectionFiles = []string{"restaurant_inspections.csv", "restaurant_inspections_stanford.csv"}

// Establishment columns copied onto each inspection. Volatile columns such as
// current_grade are left out: they change with every new inspection and would
// otherwise create a new version of every past inspection of that restaurant.
var establishmentColumns = []string{
	"facility_id", "PE", "restaurant_name", "location_name", "address",
	"latitude", "longitude", "city_name", "zip_code",
}

type Source struct {
	URL  string
	HTTP *http.Client
}

func New(c *http.Client) *Source { return &Source{URL: zipURL, HTTP: c} }

func (*Source) Name() string { return "snhd" }

func (s *Source) Fetch(ctx context.Context, emit func(source.Record) error) error {
	zr, err := s.download(ctx)
	if err != nil {
		return err
	}
	types, err := inspectionTypes(zr)
	if err != nil {
		return err
	}
	estabs := make(map[string]map[string]string)
	err = eachRow(zr, "restaurant_establishments.csv", func(header, cols []string, line string) error {
		row, err := repairEstablishment(header, cols)
		if err != nil {
			return emitError(emit, "establishment:"+cols[0], err, line)
		}
		estabs[row["permit_number"]] = row
		return nil
	})
	if err != nil {
		return err
	}
	for _, name := range inspectionFiles {
		err := eachRow(zr, name, func(header, cols []string, line string) error {
			if len(cols) != len(header) {
				return emitError(emit, "inspection:"+cols[0],
					fmt.Errorf("%d fields, want %d", len(cols), len(header)), line)
			}
			fields := map[string]any{"_file": name}
			for i, h := range header {
				fields[h] = cols[i]
			}
			fields["inspection_type"] = types[fields["inspection_type_id"].(string)]
			if e, ok := estabs[cols[1]]; ok { // permit_number; absent for ~340 permits
				for _, c := range establishmentColumns {
					fields["est_"+c] = e[c]
				}
			}
			r, err := source.NewRecord(cols[0], fields)
			if err != nil {
				return err
			}
			return emit(r)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) download(ctx context.Context) (*zip.Reader, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", source.UserAgent)
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snhd: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxZip+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxZip {
		return nil, errors.New("snhd: zip larger than 200 MB")
	}
	return zip.NewReader(bytes.NewReader(b), int64(len(b)))
}

// eachRow calls fn for every data line of a table in the zip. The files are
// ';'-separated with a trailing ';' and no quoting: quotes are literal data.
func eachRow(zr *zip.Reader, name string, fn func(header, cols []string, line string) error) error {
	f, err := zr.Open(name)
	if err != nil {
		return fmt.Errorf("snhd: %s: %w", name, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var header []string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		cols := split(line)
		if header == nil {
			header = cols
			continue
		}
		if err := fn(header, cols, line); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("snhd: %s: %w", name, err)
	}
	if header == nil {
		return fmt.Errorf("snhd: %s is empty", name)
	}
	return nil
}

func split(line string) []string {
	return strings.Split(strings.TrimSuffix(line, ";"), ";")
}

// repairEstablishment maps an establishment line to its columns. Some names
// contain a bare ';', which also appears in the trailing search_text column,
// so a repairable line has an even number of extra fields: half belong to
// restaurant_name (column 4), half to search_text (the last column).
func repairEstablishment(header, cols []string) (map[string]string, error) {
	const nameCol = 4
	extra := len(cols) - len(header)
	if header[nameCol] != "restaurant_name" || header[len(header)-1] != "search_text" {
		return nil, fmt.Errorf("unexpected establishment header %v", header)
	}
	if extra < 0 || extra%2 != 0 {
		return nil, fmt.Errorf("%d fields, want %d", len(cols), len(header))
	}
	k := extra / 2
	fixed := make([]string, 0, len(header))
	fixed = append(fixed, cols[:nameCol]...)
	fixed = append(fixed, strings.Join(cols[nameCol:nameCol+k+1], ";"))
	fixed = append(fixed, cols[nameCol+k+1:len(cols)-k-1]...)
	fixed = append(fixed, strings.Join(cols[len(cols)-k-1:], ";"))
	row := make(map[string]string, len(header))
	for i, h := range header {
		row[h] = fixed[i]
	}
	return row, nil
}

func inspectionTypes(zr *zip.Reader) (map[string]string, error) {
	types := make(map[string]string)
	err := eachRow(zr, "restaurant_inspection_types.csv", func(_, cols []string, _ string) error {
		if len(cols) >= 2 {
			types[cols[0]] = cols[1]
		}
		return nil
	})
	return types, err
}

// emitError forwards an unparseable line as a record that Parse rejects, so the
// ingest runner stores it in parse_errors instead of it vanishing.
func emitError(emit func(source.Record) error, id string, cause error, line string) error {
	r, err := source.NewRecord(id, map[string]any{"_error": cause.Error(), "_raw": line})
	if err != nil {
		return err
	}
	return emit(r)
}

func (*Source) Parse(r source.Record) (source.Observation, error) {
	f, err := source.Decode(r)
	if err != nil {
		return source.Observation{}, err
	}
	if msg := f.Str("_error"); msg != "" {
		return source.Observation{}, errors.New(msg)
	}
	name := f.Str("est_restaurant_name")
	if name == "" {
		name = f.Str("est_location_name")
	}
	lat, lon := source.LatLon(f.Float("est_latitude"), f.Float("est_longitude"))
	return source.Observation{
		Kind:        "inspection",
		Name:        source.Clean(name),
		Address:     source.Clean(f.Str("est_address")),
		City:        f.Str("est_city_name"),
		Zip:         source.Zip5(f.Str("est_zip_code")),
		BusinessKey: "snhd:" + f.Str("permit_number"),
		Category:    f.Str("inspection_type"),
		Status:      f.Str("inspection_result"),
		Lat:         lat,
		Lon:         lon,
		StartDate:   source.Date("2006-01-02 15:04:05", f.Str("inspection_date")),
	}, nil
}
