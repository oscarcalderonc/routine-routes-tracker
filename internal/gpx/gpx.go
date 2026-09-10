// Package gpx parses GPX recordings into tracks and removes the fixes that are
// obviously wrong.
//
// Only the handful of elements the tracker needs are decoded. Go's XML decoder
// matches on local names when no namespace is given, so GPX 1.0 and 1.1 files
// are handled identically without special cases.
package gpx

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/geo"
)

// Filter bounds on what counts as a usable fix. The defaults suit a car in an
// urban setting with a phone-grade receiver.
const (
	// MaxHDOP is the largest horizontal dilution of precision accepted.
	MaxHDOP = 8.0
	// MaxAccuracyM is the largest reported horizontal accuracy accepted.
	MaxAccuracyM = 25.0
	// MaxSpeedKPH is the speed above which a fix is treated as a multipath
	// artefact rather than travel.
	MaxSpeedKPH = 150.0
)

// ErrNoTimestamps reports a recording whose fixes carry no times. Such a file
// cannot yield durations and is rejected rather than partially imported.
var ErrNoTimestamps = errors.New("gpx: track points have no timestamps")

// ErrNoPoints reports a recording that contained no track points at all.
var ErrNoPoints = errors.New("gpx: no track points found")

// ErrDoctype reports a document carrying a DTD. Go's decoder does not fetch
// external entities but will expand internal ones, so such documents are
// refused outright rather than parsed.
var ErrDoctype = errors.New("gpx: document type declarations are not accepted")

type gpxFile struct {
	Tracks []struct {
		Segments []struct {
			Points []trackPoint `xml:"trkpt"`
		} `xml:"trkseg"`
	} `xml:"trk"`
}

type trackPoint struct {
	Lat        float64 `xml:"lat,attr"`
	Lon        float64 `xml:"lon,attr"`
	Time       string  `xml:"time"`
	Ele        float64 `xml:"ele"`
	HDOP       float64 `xml:"hdop"`
	Extensions struct {
		Inner []byte `xml:",innerxml"`
	} `xml:"extensions"`
}

// Parse reads a GPX document and returns the track it contains, with noisy
// fixes already removed. All track segments are concatenated in order; the
// boundaries between them are preserved on the resulting points so that
// crossings are never interpolated across paused recording.
func Parse(r io.Reader, maxBytes int64) (domain.Track, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes))
	if err != nil {
		return domain.Track{}, fmt.Errorf("read gpx: %w", err)
	}
	if int64(len(data)) >= maxBytes {
		return domain.Track{}, fmt.Errorf("gpx: input exceeds %d bytes", maxBytes)
	}
	if bytes.Contains(data, []byte("<!DOCTYPE")) {
		return domain.Track{}, ErrDoctype
	}

	var doc gpxFile
	if err := xml.Unmarshal(data, &doc); err != nil {
		return domain.Track{}, fmt.Errorf("parse gpx: %w", err)
	}

	var raw []domain.Point
	var missingTime int
	for _, trk := range doc.Tracks {
		for _, seg := range trk.Segments {
			for i, p := range seg.Points {
				ts, err := parseTime(p.Time)
				if err != nil {
					missingTime++
					continue
				}
				raw = append(raw, domain.Point{
					Lat:          p.Lat,
					Lon:          p.Lon,
					Time:         ts,
					Ele:          p.Ele,
					HDOP:         p.HDOP,
					Accuracy:     accuracyFrom(p.Extensions.Inner),
					SegmentStart: i == 0,
				})
			}
		}
	}

	if len(raw) == 0 {
		if missingTime > 0 {
			return domain.Track{}, ErrNoTimestamps
		}
		return domain.Track{}, ErrNoPoints
	}

	return domain.Track{Points: filter(raw), RawCount: len(raw)}, nil
}

// filter discards fixes that report poor precision or imply impossible travel,
// and puts the remainder in time order with duplicates removed.
func filter(pts []domain.Point) []domain.Point {
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].Time.Before(pts[j].Time) })

	out := make([]domain.Point, 0, len(pts))
	// A discarded fix must not hide a recording gap, so a pending segment
	// boundary is carried forward to the next fix that survives filtering.
	pendingBoundary := false

	for _, p := range pts {
		if p.SegmentStart {
			pendingBoundary = true
		}
		if p.HDOP > MaxHDOP || p.Accuracy > MaxAccuracyM {
			continue
		}
		if len(out) > 0 {
			prev := out[len(out)-1]
			if !p.Time.After(prev.Time) {
				continue
			}
			// Reject fixes implying a speed no car on this route could reach;
			// they are almost always multipath in an urban canyon. A fix
			// opening a new segment is exempt, because the apparent speed
			// across a recording pause is meaningless.
			dt := p.Time.Sub(prev.Time).Seconds()
			dist := geo.Distance(prev.Lat, prev.Lon, p.Lat, p.Lon)
			if !pendingBoundary && dt > 0 && (dist/dt)*3.6 > MaxSpeedKPH {
				continue
			}
		}
		p.SegmentStart = pendingBoundary
		pendingBoundary = false
		out = append(out, p)
	}

	if len(out) > 0 {
		out[0].SegmentStart = true
	}
	return out
}

// parseTime accepts the RFC 3339 timestamps GPX mandates, tolerating the
// fractional seconds and space separator some loggers emit.
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty time")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time %q", s)
}

// accuracyFrom scans a trkpt's extensions for a horizontal accuracy value.
// Logging apps disagree on the element name, so any local name mentioning
// horizontal accuracy is accepted and other extension content is ignored.
func accuracyFrom(inner []byte) float64 {
	if len(inner) == 0 {
		return 0
	}
	dec := xml.NewDecoder(bytes.NewReader(inner))
	for {
		tok, err := dec.Token()
		if err != nil {
			return 0
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		name := strings.ToLower(start.Name.Local)
		if name != "horizontalaccuracy" && name != "hacc" && name != "accuracy" {
			continue
		}
		var v string
		if err := dec.DecodeElement(&v, &start); err != nil {
			return 0
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return f
	}
}
