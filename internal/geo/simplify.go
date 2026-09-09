package geo

import "math"

// LatLon is a coordinate pair used when simplifying a path for display.
type LatLon struct {
	Lat, Lon float64
}

// Simplify reduces a path with the Douglas-Peucker algorithm, discarding points
// that lie within toleranceM of the line they fall on. It is used only to
// shrink the payload sent to the map; analysis always runs on the full track.
func Simplify(pts []LatLon, toleranceM float64) []LatLon {
	idx := SimplifyIndices(pts, toleranceM)
	out := make([]LatLon, len(idx))
	for i, j := range idx {
		out[i] = pts[j]
	}
	return out
}

// SimplifyIndices is Simplify expressed as the positions of the points that
// survive, so that callers holding data alongside each point can select the
// matching entries.
func SimplifyIndices(pts []LatLon, toleranceM float64) []int {
	if len(pts) == 0 {
		return nil
	}
	if len(pts) < 3 || toleranceM <= 0 {
		idx := make([]int, len(pts))
		for i := range pts {
			idx[i] = i
		}
		return idx
	}

	keep := make([]bool, len(pts))
	keep[0] = true
	keep[len(pts)-1] = true
	simplify(pts, 0, len(pts)-1, toleranceM, keep)

	idx := make([]int, 0, len(pts))
	for i, k := range keep {
		if k {
			idx = append(idx, i)
		}
	}
	return idx
}

// simplify marks the points between first and last that must be retained. It
// recurses on the two halves either side of the furthest outlier.
func simplify(pts []LatLon, first, last int, toleranceM float64, keep []bool) {
	if last <= first+1 {
		return
	}

	// Work in a frame centred on the start of the span so that the
	// perpendicular distance is a plain planar computation.
	f := NewFrame(pts[first].Lat, pts[first].Lon)
	ax, ay := f.Project(pts[first].Lat, pts[first].Lon)
	bx, by := f.Project(pts[last].Lat, pts[last].Lon)
	dx, dy := bx-ax, by-ay
	lenSq := dx*dx + dy*dy

	maxDist, maxIdx := 0.0, first
	for i := first + 1; i < last; i++ {
		px, py := f.Project(pts[i].Lat, pts[i].Lon)

		var dist float64
		if lenSq < 1e-12 {
			dist = math.Hypot(px-ax, py-ay)
		} else {
			t := clamp01(((px-ax)*dx + (py-ay)*dy) / lenSq)
			dist = math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
		}
		if dist > maxDist {
			maxDist, maxIdx = dist, i
		}
	}

	if maxDist <= toleranceM {
		return
	}
	keep[maxIdx] = true
	simplify(pts, first, maxIdx, toleranceM, keep)
	simplify(pts, maxIdx, last, toleranceM, keep)
}
