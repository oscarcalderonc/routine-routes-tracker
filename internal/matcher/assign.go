package matcher

import (
	"math"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/geo"
)

// Anchor selects which instant of a pass anchors a segment boundary.
type Anchor string

// The available segment anchors.
const (
	// AnchorEntry uses first entry into the radius. This is the natural reading
	// of "when I reached the waypoint" but shifts if the radius is later
	// widened, changing historical durations for non-driving reasons.
	AnchorEntry Anchor = "entry"
	// AnchorClosest uses the moment of closest approach, which is invariant
	// under radius edits and slightly lower variance.
	AnchorClosest Anchor = "closest"
)

// Result is the outcome of matching one track against one route template.
type Result struct {
	// Direction reports which way along the canonical waypoint order the track
	// ran. It is derived from the ordering of the crossings, never configured.
	Direction domain.Direction
	// Crossings holds one entry per waypoint that was reached, in canonical
	// waypoint order.
	Crossings []domain.Crossing
	// MatchedCount is len(Crossings), kept for convenience in queries.
	MatchedCount int
	// Score is the mean distance of closest approach across matched waypoints;
	// lower is a better fit.
	Score  float64
	Status domain.TripStatus
}

// Match finds the best interpretation of a track against a route template.
//
// The waypoint order is tried both ways and the better fit wins, so a drive out
// and the corresponding drive back are both recognised without the caller
// having to say which one it holds.
func Match(tmpl domain.Template, track domain.Track, anchor Anchor) Result {
	wps := tmpl.Waypoints
	if len(wps) == 0 || len(track.Points) == 0 {
		return Result{Direction: domain.DirectionForward, Status: domain.StatusUnmatched}
	}

	// Passes depend only on the waypoint, not on the order it is visited in,
	// so they are found once and reused for both directions.
	passes := make([][]Pass, len(wps))
	for i, wp := range wps {
		passes[i] = findPasses(track, wp)
	}

	forward := assign(passes)
	reverse := assign(reversed(passes))
	unreverse(reverse, len(wps))

	best, dir := forward, domain.DirectionForward
	if better(reverse, forward) {
		best, dir = reverse, domain.DirectionReverse
	}

	return buildResult(wps, track, passes, best, dir, anchor)
}

// choice records, for each waypoint, the index of the pass assigned to it, or
// -1 where the waypoint was not reached.
type choice struct {
	picks   []int
	matched int
	dist    float64
}

// assign selects at most one pass per waypoint such that the chosen entry times
// increase with waypoint order.
//
// A greedy scan that takes the first plausible pass and moves on is the obvious
// implementation, but it cannot revise a choice, and this route passes near
// some waypoints twice. One wrong commitment there corrupts every subsequent
// segment. With at most a handful of waypoints the exact answer is cheap, so it
// is computed by dynamic programming instead: maximise the number of waypoints
// matched, then prefer the passes that came closest to the waypoint centres.
func assign(passes [][]Pass) choice {
	n := len(passes)

	// best[k][j] is the best chain ending with waypoint k taking pass j.
	type state struct {
		matched  int
		dist     float64
		prevWP   int
		prevPass int
	}
	best := make([][]state, n)
	for k := range passes {
		best[k] = make([]state, len(passes[k]))
		for j, p := range passes[k] {
			best[k][j] = state{matched: 1, dist: p.MinDistanceM, prevWP: -1, prevPass: -1}
			for pk := 0; pk < k; pk++ {
				for pj, pp := range passes[pk] {
					if best[pk][pj].matched == 0 {
						continue
					}
					// A later waypoint must be reached after the earlier one
					// was left, which is what rules out an out-and-back track
					// matching the order it did not drive.
					if pp.ExitAt.After(p.EnterAt) {
						continue
					}
					cand := state{
						matched:  best[pk][pj].matched + 1,
						dist:     best[pk][pj].dist + p.MinDistanceM,
						prevWP:   pk,
						prevPass: pj,
					}
					if cand.matched > best[k][j].matched ||
						(cand.matched == best[k][j].matched && cand.dist < best[k][j].dist) {
						best[k][j] = cand
					}
				}
			}
		}
	}

	result := choice{picks: make([]int, n), matched: 0, dist: 0}
	for i := range result.picks {
		result.picks[i] = -1
	}

	endWP, endPass := -1, -1
	for k := range best {
		for j := range best[k] {
			s := best[k][j]
			if s.matched > result.matched ||
				(s.matched == result.matched && s.matched > 0 && s.dist < result.dist) {
				result.matched, result.dist = s.matched, s.dist
				endWP, endPass = k, j
			}
		}
	}

	for k, j := endWP, endPass; k >= 0; {
		result.picks[k] = j
		s := best[k][j]
		k, j = s.prevWP, s.prevPass
	}
	return result
}

// better reports whether a beats b: more waypoints matched wins, and a closer
// overall fit breaks the tie.
func better(a, b choice) bool {
	if a.matched != b.matched {
		return a.matched > b.matched
	}
	return a.matched > 0 && a.dist < b.dist
}

func reversed(passes [][]Pass) [][]Pass {
	out := make([][]Pass, len(passes))
	for i := range passes {
		out[i] = passes[len(passes)-1-i]
	}
	return out
}

// unreverse maps picks made against a reversed waypoint order back onto
// canonical waypoint indices.
func unreverse(c choice, n int) {
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		c.picks[i], c.picks[j] = c.picks[j], c.picks[i]
	}
}

func buildResult(wps []domain.Waypoint, track domain.Track, passes [][]Pass, c choice, dir domain.Direction, anchor Anchor) Result {
	start := track.Points[0].Time

	res := Result{Direction: dir}
	var missedRequired int
	for i, wp := range wps {
		j := c.picks[i]
		if j < 0 {
			if !wp.Optional {
				missedRequired++
			}
			continue
		}

		p := passes[i][j]
		at := p.EnterAt
		if anchor == AnchorClosest {
			at = p.ClosestAt
		}
		res.Crossings = append(res.Crossings, domain.Crossing{
			WaypointID:       wp.ID,
			WaypointSeq:      wp.Seq,
			CrossedAt:        at,
			OffsetS:          at.Sub(start).Seconds(),
			ExitAt:           p.ExitAt,
			ClosestAt:        p.ClosestAt,
			ClosestDistanceM: p.MinDistanceM,
			Method:           p.Method,
			EnterIndex:       p.EnterIndex,
		})
	}

	res.MatchedCount = len(res.Crossings)
	if res.MatchedCount > 0 {
		res.Score = c.dist / float64(res.MatchedCount)
	}

	switch {
	case res.MatchedCount <= 1:
		res.Status = domain.StatusUnmatched
	case missedRequired > 0:
		res.Status = domain.StatusPartial
	default:
		res.Status = domain.StatusMatched
	}
	return res
}

// BuildSegments converts crossings into the stretches between consecutive
// waypoints.
//
// Segments are always numbered in canonical template order, so a given sequence
// number denotes the same physical stretch of road whichever way it was driven.
// Every stretch gets a row: one whose bounding waypoints were not both reached
// carries no duration rather than being omitted, because a missing measurement
// is not a short one and must not be silently folded into its neighbour.
func BuildSegments(tmpl domain.Template, res Result, track domain.Track, loc *time.Location) []domain.Segment {
	wps := tmpl.Waypoints
	if len(wps) < 2 {
		return nil
	}

	byWaypoint := make(map[string]domain.Crossing, len(res.Crossings))
	for _, c := range res.Crossings {
		byWaypoint[c.WaypointID] = c
	}

	segs := make([]domain.Segment, 0, len(wps)-1)
	for i := 0; i < len(wps)-1; i++ {
		from, to := wps[i], wps[i+1]
		seg := domain.Segment{
			TemplateID:     tmpl.ID,
			Seq:            i,
			FromWaypointID: from.ID,
			ToWaypointID:   to.ID,
			Label:          from.Label + " → " + to.Label,
			Direction:      res.Direction,
		}

		a, aOK := byWaypoint[from.ID]
		b, bOK := byWaypoint[to.ID]
		if aOK && bOK {
			// In reverse the later waypoint is reached first, so the earlier
			// timestamp opens the segment either way.
			startAt, endAt := a.CrossedAt, b.CrossedAt
			if endAt.Before(startAt) {
				startAt, endAt = endAt, startAt
			}
			dur := endAt.Sub(startAt).Seconds()
			dist := trackDistance(track, a.EnterIndex, b.EnterIndex)

			seg.StartedAt, seg.EndedAt = startAt, endAt
			seg.DurationS = &dur
			seg.DistanceM = &dist
			seg.IsComplete = true
			if dur > 0 {
				speed := dist / dur * 3.6
				seg.AvgSpeedKPH = &speed
			}
			local := startAt.In(loc)
			seg.LocalDate = local.Format("2006-01-02")
			seg.LocalWeekday = int(local.Weekday())
			seg.LocalHour = local.Hour()
		}
		segs = append(segs, seg)
	}
	return segs
}

// trackDistance sums the along-track distance between two indices, in metres.
func trackDistance(track domain.Track, from, to int) float64 {
	if from > to {
		from, to = to, from
	}
	from = int(math.Max(0, float64(from)))
	if to >= len(track.Points) {
		to = len(track.Points) - 1
	}

	var total float64
	for i := from; i < to; i++ {
		a, b := track.Points[i], track.Points[i+1]
		total += geo.Distance(a.Lat, a.Lon, b.Lat, b.Lon)
	}
	return total
}
