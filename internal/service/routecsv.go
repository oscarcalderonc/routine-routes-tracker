package service

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
)

// routeCSVHeader names the columns of a route backup. The route's name is
// repeated on every row so that the file stays a plain table, which is what
// makes it editable in a spreadsheet.
var routeCSVHeader = []string{"route", "seq", "label", "lat", "lon", "radius_m", "optional"}

// WriteRouteCSV writes a route and its waypoints as CSV.
//
// Only the definition is written. Trips are derived from the recordings, which
// are kept in the cloud folder, so after a restore a refresh measures them all
// again against the restored route.
func WriteRouteCSV(w io.Writer, t domain.Template) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(routeCSVHeader); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	for _, wp := range t.Waypoints {
		// Coordinates are written with as many digits as needed to read back
		// exactly, so a restore does not move a waypoint.
		if err := cw.Write([]string{
			t.Name,
			strconv.Itoa(wp.Seq + 1),
			wp.Label,
			strconv.FormatFloat(wp.Lat, 'f', -1, 64),
			strconv.FormatFloat(wp.Lon, 'f', -1, 64),
			strconv.FormatFloat(wp.RadiusM, 'f', -1, 64),
			strconv.FormatBool(wp.Optional),
		}); err != nil {
			return fmt.Errorf("write waypoint: %w", err)
		}
	}
	cw.Flush()
	return cw.Error()
}

// ReadRouteCSV parses a file written by WriteRouteCSV, returning the route's
// name and its waypoints in route order.
//
// Columns are found by name and rows are ordered by their seq column, so a
// backup that has been reordered or had columns moved in a spreadsheet still
// reads back as the same route.
func ReadRouteCSV(r io.Reader) (string, []domain.Waypoint, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return "", nil, errors.New("the file is empty")
	}
	if err != nil {
		return "", nil, fmt.Errorf("read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		// A spreadsheet may save a byte-order mark before the first heading.
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	for _, name := range routeCSVHeader {
		if _, ok := col[name]; !ok {
			return "", nil, fmt.Errorf("missing column %q; expected %s", name, strings.Join(routeCSVHeader, ","))
		}
	}

	type row struct {
		seq int
		wp  domain.Waypoint
	}
	var (
		name string
		rows []row
	)
	seen := make(map[int]bool)
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("read line %d: %w", line, err)
		}
		field := func(name string) string { return strings.TrimSpace(rec[col[name]]) }

		if name == "" {
			name = field("route")
		}
		seq, err := strconv.Atoi(field("seq"))
		if err != nil {
			return "", nil, fmt.Errorf("line %d: seq must be a whole number", line)
		}
		if seen[seq] {
			return "", nil, fmt.Errorf("line %d: seq %d appears more than once", line, seq)
		}
		seen[seq] = true

		wp, err := waypointFromRecord(field)
		if err != nil {
			return "", nil, fmt.Errorf("line %d: %w", line, err)
		}
		rows = append(rows, row{seq: seq, wp: wp})
	}

	// Fewer than two waypoints cannot measure anything, and restoring such a
	// file would silently empty a working route.
	if len(rows) < 2 {
		return "", nil, fmt.Errorf("the file has %d waypoint(s); a route needs at least two", len(rows))
	}
	if name == "" {
		name = "Daily route"
	}

	slices.SortFunc(rows, func(a, b row) int { return a.seq - b.seq })
	wps := make([]domain.Waypoint, len(rows))
	for i, r := range rows {
		wps[i] = r.wp
		wps[i].Seq = i
	}
	return name, wps, nil
}

func waypointFromRecord(field func(string) string) (domain.Waypoint, error) {
	label := field("label")
	if label == "" {
		return domain.Waypoint{}, errors.New("label is required")
	}
	lat, err := strconv.ParseFloat(field("lat"), 64)
	if err != nil || lat < -90 || lat > 90 {
		return domain.Waypoint{}, errors.New("lat must be a number between -90 and 90")
	}
	lon, err := strconv.ParseFloat(field("lon"), 64)
	if err != nil || lon < -180 || lon > 180 {
		return domain.Waypoint{}, errors.New("lon must be a number between -180 and 180")
	}
	radius, err := strconv.ParseFloat(field("radius_m"), 64)
	if err != nil || radius <= 0 {
		return domain.Waypoint{}, errors.New("radius_m must be a positive number")
	}
	optional := false
	if v := field("optional"); v != "" {
		if optional, err = strconv.ParseBool(v); err != nil {
			return domain.Waypoint{}, errors.New("optional must be true or false")
		}
	}
	return domain.Waypoint{Label: label, Lat: lat, Lon: lon, RadiusM: radius, Optional: optional}, nil
}
