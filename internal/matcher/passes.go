// Package matcher decides when a recorded track reached each waypoint of a
// route and how long it took to travel between them.
//
// The unit of detection is the straight line between consecutive fixes rather
// than the fixes themselves. At driving speed a logger sampling every few
// seconds places its fixes 40 to 70 metres apart, so a pass through a 20 metre
// radius frequently contains no fix at all; testing only whether a fix lies
// inside the circle would miss most crossings.
package matcher

import (
	"math"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/geo"
)

// AlgoVersion identifies the matching algorithm. Increment it whenever a change
// would alter results for an existing recording; trips recorded under an older
// version are then recomputed automatically on the next run.
const AlgoVersion = 1

// Tuning constants for pass detection.
const (
	// exitRadiusFactor widens the radius at which a pass is considered over,
	// so that a track grazing the boundary is read as one pass rather than
	// several. The hysteresis is what makes boundary jitter harmless.
	exitRadiusFactor = 1.6
	// exitRadiusMarginM is the minimum absolute widening, for small radii.
	exitRadiusMarginM = 10.0
	// minChordFraction is the share of the radius a track must cut through for
	// a pass with no interior fix to be believed. A genuine drive-through cuts
	// a substantial chord; a single stray fix produces a sliver.
	minChordFraction = 0.5
	// maxGapS is the longest interval between fixes across which a crossing may
	// be interpolated. Beyond it the straight line is a fiction.
	maxGapS = 60.0
	// maxSpeedKPH bounds the implied speed of a chord used for interpolation.
	maxSpeedKPH = 150.0
)

// Pass is one continuous approach of a track to a waypoint.
type Pass struct {
	// EnterAt is the moment the track first came within the radius.
	EnterAt time.Time
	// ExitAt is the moment it last left the widened radius.
	ExitAt time.Time
	// ClosestAt and MinDistanceM describe the point of closest approach. Unlike
	// EnterAt they do not shift when the waypoint radius is edited.
	ClosestAt    time.Time
	MinDistanceM float64
	// EnterIndex is the index into the track at which the pass began.
	EnterIndex int
	Method     domain.CrossingMethod
}

// findPasses returns every credible approach of the track to one waypoint, in
// chronological order.
func findPasses(track domain.Track, wp domain.Waypoint) []Pass {
	pts := track.Points
	if len(pts) == 0 {
		return nil
	}

	frame := geo.NewFrame(wp.Lat, wp.Lon)
	xs := make([]float64, len(pts))
	ys := make([]float64, len(pts))
	dists := make([]float64, len(pts))
	for i, p := range pts {
		xs[i], ys[i] = frame.Project(p.Lat, p.Lon)
		dists[i] = geo.DistanceToOrigin(xs[i], ys[i])
	}

	rEnter := wp.RadiusM
	rExit := math.Max(rEnter*exitRadiusFactor, rEnter+exitRadiusMarginM)

	var passes []Pass
	var cur *Pass
	var curInsideChord float64
	var curHasInsideFix bool

	// Close the open pass, keeping it only if the track really travelled
	// through the circle rather than merely reporting one suspect fix there.
	closePass := func(exitAt time.Time) {
		if cur == nil {
			return
		}
		if curHasInsideFix || curInsideChord >= minChordFraction*rEnter {
			cur.ExitAt = exitAt
			passes = append(passes, *cur)
		}
		cur, curInsideChord, curHasInsideFix = nil, 0, false
	}

	openPass := func(at time.Time, idx int, method domain.CrossingMethod) {
		cur = &Pass{
			EnterAt:      at,
			ExitAt:       at,
			ClosestAt:    at,
			MinDistanceM: math.Inf(1),
			EnterIndex:   idx,
			Method:       method,
		}
		curInsideChord, curHasInsideFix = 0, false
	}

	// A track that begins inside the radius has no entry to solve for; its
	// crossing time reflects when recording started, which is worth recording
	// distinctly so the UI can flag it.
	if dists[0] <= rEnter {
		openPass(pts[0].Time, 0, domain.MethodStartInside)
		curHasInsideFix = true
		cur.MinDistanceM = dists[0]
		cur.ClosestAt = pts[0].Time
	}

	for i := 0; i < len(pts)-1; i++ {
		a, b := pts[i], pts[i+1]

		var ch geo.Chord
		if usableChord(a, b) {
			ch = geo.IntersectCircle(xs[i], ys[i], xs[i+1], ys[i+1], rEnter)
		}

		if cur == nil && ch.Intersects {
			method := domain.MethodInterpolated
			if dists[i] <= rEnter || dists[i+1] <= rEnter {
				method = domain.MethodPointInside
			}
			openPass(lerpTime(a.Time, b.Time, ch.EnterFrac), i, method)
		}

		if cur == nil {
			continue
		}

		if ch.Intersects {
			curInsideChord = math.Max(curInsideChord, ch.InsideLengthM)
			cur.ExitAt = lerpTime(a.Time, b.Time, ch.ExitFrac)
			if d := ch.MinDistanceM; d < cur.MinDistanceM {
				cur.MinDistanceM = d
				cur.ClosestAt = lerpTime(a.Time, b.Time, ch.ClosestFrac)
			}
		}
		if dists[i] <= rEnter || dists[i+1] <= rEnter {
			curHasInsideFix = true
		}
		// Fall back to the endpoint distance when the chord missed entirely, so
		// that the closest approach is still recorded for a near miss.
		if !ch.Intersects && dists[i+1] < cur.MinDistanceM {
			cur.MinDistanceM = dists[i+1]
			cur.ClosestAt = b.Time
		}

		// Leaving the widened radius, or a break in recording, ends the pass.
		if dists[i+1] > rExit || b.SegmentStart {
			closePass(b.Time)
		}
	}
	closePass(pts[len(pts)-1].Time)

	return passes
}

// usableChord reports whether the straight line between two fixes represents
// real travel. Recording pauses and implausible jumps do not, and interpolating
// a crossing across one would invent a time that never happened.
func usableChord(a, b domain.Point) bool {
	if b.SegmentStart {
		return false
	}
	dt := b.Time.Sub(a.Time).Seconds()
	if dt <= 0 || dt > maxGapS {
		return false
	}
	dist := geo.Distance(a.Lat, a.Lon, b.Lat, b.Lon)
	return (dist/dt)*3.6 <= maxSpeedKPH
}

// lerpTime returns the instant a given fraction of the way from a to b.
func lerpTime(a, b time.Time, frac float64) time.Time {
	return a.Add(time.Duration(float64(b.Sub(a)) * frac))
}
