// Package service holds the tracker's application logic: importing recordings,
// measuring them against the route, and recomputing them when the route
// changes.
package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/geo"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/gpx"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/matcher"
)

// filenameLayout is the shape of a recording's name: the UTC instant the
// journey began. The name is the deduplication key, so a recording is imported
// exactly once however many times the folder is synced.
const filenameLayout = "20060102150405"

// simplifyToleranceM is how far the drawn path may stray from the recorded one.
// It only affects the map; every measurement uses the full track.
const simplifyToleranceM = 4

// Ingest imports one recording and measures it against the route.
//
// The source bytes are retained before anything is derived from them. Every
// crossing, segment and drawn path can be rebuilt from that file, which is what
// makes it safe to recompute the whole history after a waypoint is moved.
func (s *Service) Ingest(ctx context.Context, filename string, data []byte) (domain.Trip, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	if err := s.storeBlob(sha, data); err != nil {
		return domain.Trip{}, err
	}

	tmpl, err := s.store.ActiveTemplate(ctx)
	if err != nil {
		return domain.Trip{}, fmt.Errorf("load route: %w", err)
	}

	track, err := gpx.Parse(bytes.NewReader(data), s.maxBytes)
	if err != nil {
		return domain.Trip{}, err
	}

	trip := s.measure(tmpl, track, filename, sha)
	payload, points, err := encodeTrack(track)
	if err != nil {
		return domain.Trip{}, err
	}
	if err := s.store.SaveTrip(ctx, trip, sha, payload, points); err != nil {
		return domain.Trip{}, err
	}
	return trip, nil
}

// measure runs the matcher over a track and assembles the trip record. It does
// not touch the database, so it is also what recomputation uses.
func (s *Service) measure(tmpl domain.Template, track domain.Track, filename, sha string) domain.Trip {
	res := matcher.Match(tmpl, track, s.anchor)
	segments := matcher.BuildSegments(tmpl, res, track, s.loc)

	start := track.Points[0].Time
	end := track.Points[len(track.Points)-1].Time
	local := start.In(s.loc)

	trip := domain.Trip{
		ID:               domain.NewID(),
		TemplateID:       tmpl.ID,
		TemplateVersion:  tmpl.Version,
		AlgoVersion:      matcher.AlgoVersion,
		SourceFilename:   filename,
		StartedAt:        start,
		EndedAt:          end,
		LocalDate:        local.Format(time.DateOnly),
		LocalWeekday:     int(local.Weekday()),
		LocalHour:        local.Hour(),
		Direction:        res.Direction,
		PointCount:       track.RawCount,
		KeptPointCount:   len(track.Points),
		DurationS:        end.Sub(start).Seconds(),
		Status:           res.Status,
		MatchedWaypoints: res.MatchedCount,
		MatchScore:       res.Score,
		Crossings:        res.Crossings,
		Segments:         segments,
		ProcessedAt:      time.Now().UTC(),
	}
	trip.DistanceM, trip.MinLat, trip.MinLon, trip.MaxLat, trip.MaxLon = extent(track)

	for i := range trip.Crossings {
		trip.Crossings[i].TripID = trip.ID
	}
	for i := range trip.Segments {
		trip.Segments[i].TripID = trip.ID
	}
	return trip
}

// extent returns the length of a track and its bounding box.
func extent(track domain.Track) (distanceM, minLat, minLon, maxLat, maxLon float64) {
	pts := track.Points
	minLat, minLon = math.Inf(1), math.Inf(1)
	maxLat, maxLon = math.Inf(-1), math.Inf(-1)

	for i, p := range pts {
		minLat, maxLat = math.Min(minLat, p.Lat), math.Max(maxLat, p.Lat)
		minLon, maxLon = math.Min(minLon, p.Lon), math.Max(maxLon, p.Lon)
		if i > 0 {
			distanceM += geo.Haversine(pts[i-1].Lat, pts[i-1].Lon, p.Lat, p.Lon)
		}
	}
	return distanceM, minLat, minLon, maxLat, maxLon
}

// trackPoint is the compact form the map consumes: latitude, longitude and
// seconds since the start of the recording.
type trackPoint [3]float64

// encodeTrack simplifies a track for display and compresses it. The result is a
// derived cache; the retained source file remains authoritative.
func encodeTrack(track domain.Track) ([]byte, int, error) {
	coords := make([]geo.LatLon, len(track.Points))
	for i, p := range track.Points {
		coords[i] = geo.LatLon{Lat: p.Lat, Lon: p.Lon}
	}

	// Working in indices keeps each retained coordinate paired with its own
	// timestamp; matching on coordinate values would confuse points the track
	// visited more than once.
	start := track.Points[0].Time
	kept := geo.SimplifyIndices(coords, simplifyToleranceM)
	out := make([]trackPoint, len(kept))
	for i, j := range kept {
		p := track.Points[j]
		out[i] = trackPoint{round(p.Lat, 6), round(p.Lon, 6), round(p.Time.Sub(start).Seconds(), 1)}
	}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(out); err != nil {
		return nil, 0, fmt.Errorf("encode track: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, 0, fmt.Errorf("compress track: %w", err)
	}
	return buf.Bytes(), len(out), nil
}

func round(v float64, places int) float64 {
	f := math.Pow(10, float64(places))
	return math.Round(v*f) / f
}

// FileTime reads the instant encoded in a recording's name. Recordings are
// named for the UTC time the journey began; a name that does not follow that
// shape is accepted but carries no time.
func FileTime(filename string) (time.Time, bool) {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	t, err := time.ParseInLocation(filenameLayout, base, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// blobPath is where a source file is retained, derived from its content hash so
// that no path needs storing alongside the trip.
func (s *Service) blobPath(sha string) string {
	return filepath.Join(s.blobDir, sha[:2], sha+".gpx.gz")
}

func (s *Service) storeBlob(sha string, data []byte) error {
	path := s.blobPath(sha)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create blob directory: %w", err)
	}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return fmt.Errorf("compress source file: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("compress source file: %w", err)
	}

	// Write to a temporary name first so that an interrupted write cannot leave
	// a truncated file under a hash that claims to describe it.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write source file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit source file: %w", err)
	}
	return nil
}

// readBlob returns the original bytes of a retained source file.
func (s *Service) readBlob(sha string) ([]byte, error) {
	f, err := os.Open(s.blobPath(sha))
	if err != nil {
		return nil, fmt.Errorf("open source file: %w", err)
	}
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("read source file: %w", err)
	}
	defer zr.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(zr); err != nil {
		return nil, fmt.Errorf("decompress source file: %w", err)
	}
	return buf.Bytes(), nil
}

// parseBytes parses a retained source file back into a track.
func parseBytes(data []byte, maxBytes int64) (domain.Track, error) {
	return gpx.Parse(bytes.NewReader(data), maxBytes)
}
