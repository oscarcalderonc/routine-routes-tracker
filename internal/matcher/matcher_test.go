package matcher

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/geo"
)

const (
	testLat = 52.0
	testLon = 21.0
)

// metresToDegLon converts an eastward offset in metres to a longitude delta at
// the latitude the tests use.
func metresToDegLon(m float64) float64 {
	f := geo.NewFrame(testLat, testLon)
	perDeg, _ := f.Project(testLat, testLon+1)
	return m / perDeg
}

func metresToDegLat(m float64) float64 {
	f := geo.NewFrame(testLat, testLon)
	_, perDeg := f.Project(testLat+1, testLon)
	return m / perDeg
}

// lineTemplate builds a route whose waypoints sit at the given eastward offsets
// in metres from the test origin.
func lineTemplate(offsetsM []float64, radiusM float64) domain.Template {
	tmpl := domain.Template{ID: "tmpl", Name: "Test route", Active: true, Version: 1}
	for i, off := range offsetsM {
		tmpl.Waypoints = append(tmpl.Waypoints, domain.Waypoint{
			ID:      fmt.Sprintf("wp%d", i),
			Seq:     i,
			Label:   fmt.Sprintf("WP%d", i),
			Lat:     testLat,
			Lon:     testLon + metresToDegLon(off),
			RadiusM: radiusM,
		})
	}
	return tmpl
}

// eastTrack drives in a straight line from fromM to toM (eastward offsets in
// metres), sampling every spacingM at the given speed. lateralM offsets the
// whole track sideways, standing in for the fact that a road never passes
// exactly through the point you marked on a map.
func eastTrack(t0 time.Time, fromM, toM, spacingM, speedKPH, lateralM float64) domain.Track {
	speedMS := speedKPH / 3.6
	dir := 1.0
	if toM < fromM {
		dir = -1
	}

	var pts []domain.Point
	elapsed := 0.0
	for d := fromM; (toM-d)*dir >= 0; d += spacingM * dir {
		pts = append(pts, domain.Point{
			Lat:  testLat + metresToDegLat(lateralM),
			Lon:  testLon + metresToDegLon(d),
			Time: t0.Add(time.Duration(elapsed * float64(time.Second))),
		})
		elapsed += spacingM / speedMS
	}
	if len(pts) > 0 {
		pts[0].SegmentStart = true
	}
	return domain.Track{Points: pts, RawCount: len(pts)}
}

func crossingFor(res Result, waypointID string) (domain.Crossing, bool) {
	for _, c := range res.Crossings {
		if c.WaypointID == waypointID {
			return c, true
		}
	}
	return domain.Crossing{}, false
}

// TestMatch_SparseFastPass is the case the design turns on. At 50 km/h with a
// 5 s sample interval the fixes are 70 m apart, so a 20 m circle usually
// contains none of them. Every waypoint must still be matched.
func TestMatch_SparseFastPass(t *testing.T) {
	tmpl := lineTemplate([]float64{0, 500, 1000, 1500}, 20)
	t0 := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	track := eastTrack(t0, -100, 1600, 70, 50, 5)

	res := Match(tmpl, track, AnchorEntry)

	if res.Status != domain.StatusMatched {
		t.Fatalf("Status = %q, want %q (matched %d of 4)", res.Status, domain.StatusMatched, res.MatchedCount)
	}
	if res.Direction != domain.DirectionForward {
		t.Errorf("Direction = %q, want forward", res.Direction)
	}
	if res.MatchedCount != 4 {
		t.Fatalf("MatchedCount = %d, want 4", res.MatchedCount)
	}

	// At this spacing most waypoints are passed without any fix landing inside
	// the radius, so most crossings must have been interpolated from the chord
	// between two fixes. A design that only tested whether a fix lies inside
	// the circle would miss them entirely.
	var interpolated int
	for _, c := range res.Crossings {
		if c.Method == domain.MethodInterpolated {
			interpolated++
		}
	}
	if interpolated == 0 {
		t.Error("no crossing was interpolated; the sparse-sampling case is untested")
	}

	// Crossings must be ordered and 500 m apart, i.e. 36 s at 50 km/h.
	for i := 1; i < len(res.Crossings); i++ {
		gap := res.Crossings[i].CrossedAt.Sub(res.Crossings[i-1].CrossedAt).Seconds()
		if math.Abs(gap-36) > 2 {
			t.Errorf("gap between waypoints %d and %d = %.1f s, want ~36 s", i-1, i, gap)
		}
	}
}

func TestMatch_ReverseDirection(t *testing.T) {
	tmpl := lineTemplate([]float64{0, 500, 1000, 1500}, 20)
	t0 := time.Date(2026, 9, 9, 12, 15, 0, 0, time.UTC)
	track := eastTrack(t0, 1600, -100, 70, 50, 5)

	res := Match(tmpl, track, AnchorEntry)

	if res.Direction != domain.DirectionReverse {
		t.Errorf("Direction = %q, want reverse", res.Direction)
	}
	if res.Status != domain.StatusMatched {
		t.Fatalf("Status = %q, want matched (matched %d of 4)", res.Status, res.MatchedCount)
	}

	// Crossings are reported in canonical waypoint order, so on a reverse run
	// their timestamps decrease.
	for i := 1; i < len(res.Crossings); i++ {
		if !res.Crossings[i].CrossedAt.Before(res.Crossings[i-1].CrossedAt) {
			t.Errorf("crossing %d should precede crossing %d on a reverse run", i, i-1)
		}
	}

	segs := BuildSegments(tmpl, res, track, time.UTC)
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3", len(segs))
	}
	for i, seg := range segs {
		if seg.Seq != i {
			t.Errorf("segment %d has Seq %d; numbering must stay canonical", i, seg.Seq)
		}
		if !seg.IsComplete || seg.DurationS == nil {
			t.Fatalf("segment %d is incomplete on a fully matched trip", i)
		}
		if *seg.DurationS <= 0 {
			t.Errorf("segment %d duration = %v, want positive on a reverse run", i, *seg.DurationS)
		}
		if math.Abs(*seg.DurationS-36) > 2 {
			t.Errorf("segment %d duration = %.1f s, want ~36 s", i, *seg.DurationS)
		}
	}
}

// TestMatch_DirectionsAgreeOnDurations checks that the same stretch of road
// driven each way lands in the same segment bucket with a comparable duration,
// which is what lets the two directions be pooled.
func TestMatch_DirectionsAgreeOnDurations(t *testing.T) {
	tmpl := lineTemplate([]float64{0, 500, 1000}, 20)
	t0 := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)

	out := eastTrack(t0, -100, 1100, 70, 50, 5)
	back := eastTrack(t0.Add(6*time.Hour), 1100, -100, 70, 50, 5)

	outSegs := BuildSegments(tmpl, Match(tmpl, out, AnchorEntry), out, time.UTC)
	backSegs := BuildSegments(tmpl, Match(tmpl, back, AnchorEntry), back, time.UTC)

	if len(outSegs) != len(backSegs) {
		t.Fatalf("segment counts differ: %d vs %d", len(outSegs), len(backSegs))
	}
	for i := range outSegs {
		if outSegs[i].Seq != backSegs[i].Seq {
			t.Errorf("segment %d numbering differs between directions", i)
		}
		if outSegs[i].DurationS == nil || backSegs[i].DurationS == nil {
			t.Fatalf("segment %d incomplete in one direction", i)
		}
		if diff := math.Abs(*outSegs[i].DurationS - *backSegs[i].DurationS); diff > 3 {
			t.Errorf("segment %d durations differ by %.1f s between directions", i, diff)
		}
	}
	if outSegs[0].Direction != domain.DirectionForward || backSegs[0].Direction != domain.DirectionReverse {
		t.Error("segments should record the direction they were driven in")
	}
}

func TestMatch_MissedWaypointYieldsPartialTrip(t *testing.T) {
	tmpl := lineTemplate([]float64{0, 500, 1000, 1500}, 20)
	// Move the third waypoint 200 m off the road; it can never be reached.
	tmpl.Waypoints[2].Lat = testLat + metresToDegLat(200)

	t0 := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	track := eastTrack(t0, -100, 1600, 70, 50, 5)

	res := Match(tmpl, track, AnchorEntry)
	if res.Status != domain.StatusPartial {
		t.Fatalf("Status = %q, want partial", res.Status)
	}
	if res.MatchedCount != 3 {
		t.Errorf("MatchedCount = %d, want 3", res.MatchedCount)
	}
	if _, ok := crossingFor(res, "wp2"); ok {
		t.Error("the unreachable waypoint should have no crossing")
	}

	segs := BuildSegments(tmpl, res, track, time.UTC)
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3 even with a waypoint missing", len(segs))
	}
	// Segments 1 and 2 both touch the missing waypoint and must carry no
	// duration; segment 0 is unaffected and must still be measured.
	if segs[0].DurationS == nil {
		t.Error("segment 0 should still be measured")
	}
	for _, i := range []int{1, 2} {
		if segs[i].DurationS != nil {
			t.Errorf("segment %d touches a missing waypoint and must have no duration", i)
		}
		if segs[i].IsComplete {
			t.Errorf("segment %d should not be marked complete", i)
		}
	}
}

func TestMatch_DoesNotInterpolateAcrossRecordingGap(t *testing.T) {
	tmpl := lineTemplate([]float64{0, 500}, 20)
	t0 := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	track := eastTrack(t0, -100, 600, 70, 50, 5)

	// Mark the fix just past the second waypoint as opening a new segment and
	// push it far into the future, as a pause in recording would.
	for i, p := range track.Points {
		off := (p.Lon - testLon) / metresToDegLon(1)
		if off > 500 {
			track.Points[i].SegmentStart = true
			for j := i; j < len(track.Points); j++ {
				track.Points[j].Time = track.Points[j].Time.Add(20 * time.Minute)
			}
			break
		}
	}

	res := Match(tmpl, track, AnchorEntry)
	if _, ok := crossingFor(res, "wp1"); ok {
		t.Error("a crossing was invented across a recording pause")
	}
	if res.Status != domain.StatusUnmatched {
		t.Errorf("Status = %q, want unmatched with only one waypoint reached", res.Status)
	}
}

func TestMatch_TrackStartingInsideRadius(t *testing.T) {
	tmpl := lineTemplate([]float64{0, 500}, 20)
	t0 := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	// Begin recording right on the first waypoint.
	track := eastTrack(t0, 0, 600, 70, 50, 2)

	res := Match(tmpl, track, AnchorEntry)
	c, ok := crossingFor(res, "wp0")
	if !ok {
		t.Fatal("the waypoint the track started inside was not matched")
	}
	if c.Method != domain.MethodStartInside {
		t.Errorf("Method = %q, want start_inside", c.Method)
	}
	if !c.CrossedAt.Equal(t0) {
		t.Errorf("CrossedAt = %v, want the first fix time %v", c.CrossedAt, t0)
	}
}

// TestMatch_OutAndBackPassesWaypointTwice covers the ambiguity a greedy scan
// handles badly: the track passes every waypoint twice, and the assignment must
// still produce one consistent ordered set.
func TestMatch_OutAndBackPassesWaypointTwice(t *testing.T) {
	tmpl := lineTemplate([]float64{0, 500, 1000}, 20)
	t0 := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)

	out := eastTrack(t0, -100, 1100, 70, 50, 5)
	last := out.Points[len(out.Points)-1].Time
	back := eastTrack(last.Add(5*time.Minute), 1100, -100, 70, 50, 5)
	combined := domain.Track{Points: append(out.Points, back.Points[1:]...)}
	combined.RawCount = len(combined.Points)

	res := Match(tmpl, combined, AnchorEntry)
	if res.MatchedCount != 3 {
		t.Fatalf("MatchedCount = %d, want 3", res.MatchedCount)
	}
	for i := 1; i < len(res.Crossings); i++ {
		prev, cur := res.Crossings[i-1], res.Crossings[i]
		ordered := cur.CrossedAt.After(prev.CrossedAt)
		if res.Direction == domain.DirectionReverse {
			ordered = cur.CrossedAt.Before(prev.CrossedAt)
		}
		if !ordered {
			t.Errorf("crossings %d and %d are not consistently ordered for direction %q", i-1, i, res.Direction)
		}
	}

	segs := BuildSegments(tmpl, res, combined, time.UTC)
	for i, seg := range segs {
		if seg.DurationS == nil {
			t.Fatalf("segment %d has no duration", i)
		}
		// The chosen passes must come from a single traverse, not one from the
		// outbound leg and one from the return.
		if *seg.DurationS > 120 {
			t.Errorf("segment %d duration = %.0f s; passes were mixed across legs", i, *seg.DurationS)
		}
	}
}

func TestMatch_AnchorClosestIsRadiusInvariant(t *testing.T) {
	t0 := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	track := eastTrack(t0, -100, 1100, 70, 50, 5)

	narrow := Match(lineTemplate([]float64{0, 500, 1000}, 20), track, AnchorClosest)
	wide := Match(lineTemplate([]float64{0, 500, 1000}, 40), track, AnchorClosest)

	if narrow.MatchedCount != 3 || wide.MatchedCount != 3 {
		t.Fatalf("both radii should match all waypoints, got %d and %d", narrow.MatchedCount, wide.MatchedCount)
	}
	for i := range narrow.Crossings {
		diff := narrow.Crossings[i].CrossedAt.Sub(wide.Crossings[i].CrossedAt).Seconds()
		if math.Abs(diff) > 0.001 {
			t.Errorf("closest-approach anchor for waypoint %d moved by %.3f s when the radius changed", i, diff)
		}
	}

	// The entry anchor, by contrast, is expected to shift earlier as the radius
	// grows. This documents why the anchor is configurable.
	narrowEntry := Match(lineTemplate([]float64{0, 500, 1000}, 20), track, AnchorEntry)
	wideEntry := Match(lineTemplate([]float64{0, 500, 1000}, 40), track, AnchorEntry)
	if !wideEntry.Crossings[1].CrossedAt.Before(narrowEntry.Crossings[1].CrossedAt) {
		t.Error("a wider radius should bring the entry anchor forward in time")
	}
}

func TestMatch_EmptyTemplate(t *testing.T) {
	t0 := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	track := eastTrack(t0, 0, 500, 70, 50, 0)
	res := Match(domain.Template{ID: "empty"}, track, AnchorEntry)
	if res.Status != domain.StatusUnmatched {
		t.Errorf("Status = %q, want unmatched for a template with no waypoints", res.Status)
	}
}
