// Package geo provides the geometric primitives used to decide when a recorded
// track passed within a waypoint's radius.
//
// Distances are computed in a local east-north tangent plane re-originated at
// the point of interest rather than on the sphere. At the scale of a waypoint
// radius the difference from a geodesic is far below GPS noise, and the flat
// frame admits a closed form for the distance from a point to a line segment,
// which the spherical formulation does not.
package geo

import "math"

// Frame converts geographic coordinates to metres east and north of a fixed
// origin. It is valid for offsets of a few kilometres, which comfortably covers
// any waypoint radius.
type Frame struct {
	lat0, lon0             float64
	mPerDegLat, mPerDegLon float64
}

// NewFrame returns a Frame originated at the given coordinate.
func NewFrame(lat, lon float64) Frame {
	phi := lat * math.Pi / 180
	return Frame{
		lat0: lat,
		lon0: lon,
		// Meridional and normal radii expanded as a series in latitude; the
		// residual error is well under a millimetre per degree.
		mPerDegLat: 111132.92 - 559.82*math.Cos(2*phi) + 1.175*math.Cos(4*phi),
		mPerDegLon: 111412.84*math.Cos(phi) - 93.5*math.Cos(3*phi),
	}
}

// Project returns the offset of a coordinate from the frame origin, in metres
// east (x) and north (y).
func (f Frame) Project(lat, lon float64) (x, y float64) {
	return (lon - f.lon0) * f.mPerDegLon, (lat - f.lat0) * f.mPerDegLat
}

// Distance returns the distance in metres between two nearby coordinates.
//
// It uses the same local frame as the rest of this package, so distances and
// the crossing geometry agree. That consistency is the point: a spherical
// formula such as haversine differs from the ellipsoidal frame by around half a
// percent at tropical latitudes, and having two models of the Earth in one
// program means a stretch's reported length disagrees slightly with the
// geometry used to find its endpoints.
//
// The flat approximation costs nothing here because every distance the tracker
// measures is a hop between consecutive fixes, tens of metres apart. Error
// grows with the square of the separation and only becomes noticeable over
// hundreds of kilometres.
func Distance(lat1, lon1, lat2, lon2 float64) float64 {
	x, y := NewFrame(lat1, lon1).Project(lat2, lon2)
	return math.Hypot(x, y)
}

// Chord describes how the straight line between two consecutive fixes relates
// to a circle of a given radius about the frame origin.
type Chord struct {
	// Intersects reports whether any part of the line lies within the radius.
	Intersects bool
	// EnterFrac and ExitFrac are positions along the line, in [0,1], at which
	// it enters and leaves the circle.
	EnterFrac float64
	ExitFrac  float64
	// ClosestFrac is the position of closest approach and MinDistanceM the
	// distance there. These are defined even when Intersects is false.
	ClosestFrac  float64
	MinDistanceM float64
	// StartedInside reports that the line's first endpoint already lay within
	// the radius, so EnterFrac was clamped to zero rather than solved for.
	StartedInside bool
	// InsideLengthM is the length of the portion of the line lying within the
	// circle. A genuine drive-through cuts a substantial chord, whereas a
	// single noisy fix produces a degenerate sliver.
	InsideLengthM float64
}

// IntersectCircle solves the intersection of the segment from (ax,ay) to
// (bx,by) with a circle of radius r centred on the origin. Coordinates are in
// metres, as returned by Frame.Project.
func IntersectCircle(ax, ay, bx, by, r float64) Chord {
	dx, dy := bx-ax, by-ay
	a := dx*dx + dy*dy

	// Coincident fixes carry no direction, so degenerate to a point test.
	if a < 1e-12 {
		d := math.Hypot(ax, ay)
		return Chord{
			Intersects:    d <= r,
			MinDistanceM:  d,
			StartedInside: d <= r,
			InsideLengthM: 0,
		}
	}

	closest := clamp01(-(ax*dx + ay*dy) / a)
	cx, cy := ax+closest*dx, ay+closest*dy
	c := Chord{
		ClosestFrac:  closest,
		MinDistanceM: math.Hypot(cx, cy),
	}

	// |A + s*d|^2 = r^2 expands to the quadratic a*s^2 + b*s + cc = 0.
	b := 2 * (ax*dx + ay*dy)
	cc := ax*ax + ay*ay - r*r
	disc := b*b - 4*a*cc
	if disc < 0 {
		return c
	}

	sqrtDisc := math.Sqrt(disc)
	s1 := (-b - sqrtDisc) / (2 * a)
	s2 := (-b + sqrtDisc) / (2 * a)
	if s1 > 1 || s2 < 0 {
		return c
	}

	c.Intersects = true
	c.StartedInside = s1 < 0
	c.EnterFrac = clamp01(s1)
	c.ExitFrac = clamp01(s2)
	c.InsideLengthM = (c.ExitFrac - c.EnterFrac) * math.Sqrt(a)
	return c
}

// DistanceToOrigin returns the distance in metres from a projected point to the
// frame origin.
func DistanceToOrigin(x, y float64) float64 { return math.Hypot(x, y) }

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
