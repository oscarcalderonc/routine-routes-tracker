// Package domain defines the core types shared across the tracker. It depends
// on nothing outside the standard library so that it may be imported freely.
package domain

import "time"

// Direction records which way along the canonical waypoint order a trip ran.
type Direction string

// The directions a trip may be driven in.
const (
	// DirectionForward means the trip visited waypoints in canonical order.
	DirectionForward Direction = "forward"
	// DirectionReverse means the trip visited waypoints in the opposite order.
	DirectionReverse Direction = "reverse"
)

// TripStatus describes how completely a trip matched its route template.
type TripStatus string

// The statuses a trip may hold.
const (
	// StatusMatched means every required waypoint was crossed.
	StatusMatched TripStatus = "matched"
	// StatusPartial means at least two but not all waypoints were crossed.
	StatusPartial TripStatus = "partial"
	// StatusUnmatched means at most one waypoint was crossed.
	StatusUnmatched TripStatus = "unmatched"
	// StatusError means the file could not be parsed into a usable track.
	StatusError TripStatus = "error"
)

// CrossingMethod records how a crossing time was derived, which is useful when
// judging how much to trust an individual measurement.
type CrossingMethod string

// The ways a crossing time may be established.
const (
	// MethodPointInside means at least one recorded fix lay inside the radius.
	MethodPointInside CrossingMethod = "point_inside"
	// MethodInterpolated means the crossing was derived from the chord between
	// two fixes, neither of which lay inside the radius.
	MethodInterpolated CrossingMethod = "interpolated"
	// MethodStartInside means the track already lay inside the radius at its
	// first fix, so the crossing time reflects when recording began.
	MethodStartInside CrossingMethod = "start_inside"
)

// Point is a single recorded GPS fix.
type Point struct {
	Lat  float64
	Lon  float64
	Time time.Time
	// Ele is the elevation in metres. It is zero when the source omitted it.
	Ele float64
	// HDOP is the horizontal dilution of precision. It is zero when unknown.
	HDOP float64
	// Accuracy is the reported horizontal accuracy in metres, or zero when the
	// source did not provide one.
	Accuracy float64
	// SegmentStart reports whether this point opens a new GPX track segment.
	// Crossings are never interpolated across such a boundary because the gap
	// represents paused recording rather than travel.
	SegmentStart bool
}

// Track is an ordered, time-sorted series of fixes from one recording.
type Track struct {
	Points []Point
	// RawCount is the number of fixes before filtering, retained so the UI can
	// show how much of a recording was discarded as noise.
	RawCount int
}

// Waypoint is a user-defined circle on the route. A trip is deemed to have
// reached the waypoint when its path passes within RadiusM of the centre.
type Waypoint struct {
	ID         string
	TemplateID string
	Seq        int
	Label      string
	Lat        float64
	Lon        float64
	RadiusM    float64
	// Optional waypoints may be missed without downgrading a trip to partial.
	Optional  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Template is the reusable ordered sequence of waypoints describing the route.
type Template struct {
	ID     string
	Name   string
	Active bool
	// Version increments on every waypoint mutation and drives reprocessing.
	Version   int
	Waypoints []Waypoint
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Crossing is the moment a trip's path reached one waypoint.
type Crossing struct {
	ID          string
	TripID      string
	WaypointID  string
	WaypointSeq int
	// CrossedAt is the first entry into the radius.
	CrossedAt time.Time
	// OffsetS is CrossedAt expressed as seconds since the start of the track,
	// which is convenient for the map and immune to timezone handling.
	OffsetS float64
	ExitAt  time.Time
	// ClosestAt is the moment of closest approach. Unlike CrossedAt it does not
	// move when the waypoint radius is edited.
	ClosestAt        time.Time
	ClosestDistanceM float64
	Method           CrossingMethod
	// EnterIndex is the index into the filtered track at which entry occurred.
	EnterIndex int
}

// Segment is the stretch between two consecutive waypoints on one trip. Seq is
// always expressed in canonical template order, so the same value denotes the
// same physical stretch of road whichever direction it was driven.
type Segment struct {
	ID             string
	TripID         string
	TemplateID     string
	Seq            int
	FromWaypointID string
	ToWaypointID   string
	Label          string
	Direction      Direction
	StartedAt      time.Time
	EndedAt        time.Time
	LocalDate      string
	LocalWeekday   int
	LocalHour      int
	// DurationS is nil when either bounding waypoint was not crossed. A missing
	// segment is not a zero-length one and must never be treated as such.
	DurationS   *float64
	DistanceM   *float64
	AvgSpeedKPH *float64
	IsComplete  bool
}

// Trip is one recorded drive together with the result of matching it against
// the route template.
type Trip struct {
	ID               string
	TemplateID       string
	TemplateVersion  int
	AlgoVersion      int
	SourceFilename   string
	StartedAt        time.Time
	EndedAt          time.Time
	LocalDate        string
	LocalWeekday     int
	LocalHour        int
	Direction        Direction
	PointCount       int
	KeptPointCount   int
	DurationS        float64
	DistanceM        float64
	MinLat           float64
	MinLon           float64
	MaxLat           float64
	MaxLon           float64
	Status           TripStatus
	MatchedWaypoints int
	MatchScore       float64
	ErrorMessage     string
	ProcessedAt      time.Time
	Crossings        []Crossing
	Segments         []Segment
}

// ProcessedFile records that a source file has been ingested. The filename is
// the deduplication key: the same name is never processed twice.
type ProcessedFile struct {
	Filename     string
	TripID       string
	SHA256       string
	FileTimeUTC  time.Time
	ProcessedAt  time.Time
	Status       string
	ErrorMessage string
}

// Statuses a ProcessedFile may hold.
const (
	// FileStatusOK means the file produced a trip.
	FileStatusOK = "ok"
	// FileStatusError means the file could not be ingested and will not be
	// retried automatically.
	FileStatusError = "error"
	// FileStatusIgnored means the file parsed but did not describe a journey
	// along the route, so no trip was created. It is recorded all the same so
	// that it is not considered again on the next refresh.
	FileStatusIgnored = "ignored"
)
