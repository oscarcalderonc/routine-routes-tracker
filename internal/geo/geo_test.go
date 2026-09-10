package geo

import (
	"math"
	"testing"
)

// The tracker runs in El Salvador; the projection is latitude-dependent, so the
// tests exercise it there rather than at some arbitrary latitude.
const (
	testLat = 13.6929
	testLon = -89.2182
)

// TestDistance_ShortHop checks a separation of the size the tracker actually
// measures: consecutive fixes from a phone logger at driving speed.
func TestDistance_ShortHop(t *testing.T) {
	// 70 m east of the origin, the spacing of fixes at around 50 km/h.
	const eastM = 70.0
	f := NewFrame(testLat, testLon)
	perDegLon, _ := f.Project(testLat, testLon+1)

	got := Distance(testLat, testLon, testLat, testLon+eastM/perDegLon)
	if math.Abs(got-eastM) > 0.01 {
		t.Errorf("Distance over %v m east = %.3f m", eastM, got)
	}

	if got := Distance(testLat, testLon, testLat, testLon); got != 0 {
		t.Errorf("Distance between identical coordinates = %v, want 0", got)
	}
}

// TestFrame_DegreeLengths pins the projection against the ellipsoidal lengths
// of a degree at the latitude this tracker runs at. A spherical model would put
// the meridional figure near 111195 m, about half a percent adrift, which is
// exactly the discrepancy this package exists to avoid.
func TestFrame_DegreeLengths(t *testing.T) {
	f := NewFrame(testLat, testLon)

	_, perDegLat := f.Project(testLat+1, testLon)
	if math.Abs(perDegLat-110636) > 20 {
		t.Errorf("one degree of latitude = %.0f m, want about 110636 m", perDegLat)
	}

	perDegLon, _ := f.Project(testLat, testLon+1)
	if math.Abs(perDegLon-108175) > 20 {
		t.Errorf("one degree of longitude = %.0f m, want about 108175 m", perDegLon)
	}
}

func TestIntersectCircle(t *testing.T) {
	tests := []struct {
		name           string
		ax, ay, bx, by float64
		r              float64
		wantIntersects bool
		wantEnterFrac  float64
		wantMinDist    float64
	}{
		{
			name: "passes through centre",
			ax:   -50, ay: 0, bx: 50, by: 0, r: 20,
			wantIntersects: true, wantEnterFrac: 0.3, wantMinDist: 0,
		},
		{
			name: "offset pass still intersects",
			ax:   -50, ay: 5, bx: 50, by: 5, r: 20,
			wantIntersects: true, wantMinDist: 5,
		},
		{
			name: "misses entirely",
			ax:   -50, ay: 40, bx: 50, by: 40, r: 20,
			wantIntersects: false, wantMinDist: 40,
		},
		{
			name: "segment stops short of the circle",
			ax:   -100, ay: 0, bx: -50, by: 0, r: 20,
			wantIntersects: false, wantMinDist: 50,
		},
		{
			name: "starts inside",
			ax:   0, ay: 0, bx: 100, by: 0, r: 20,
			wantIntersects: true, wantEnterFrac: 0, wantMinDist: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IntersectCircle(tt.ax, tt.ay, tt.bx, tt.by, tt.r)
			if got.Intersects != tt.wantIntersects {
				t.Fatalf("Intersects = %v, want %v", got.Intersects, tt.wantIntersects)
			}
			if math.Abs(got.MinDistanceM-tt.wantMinDist) > 0.01 {
				t.Errorf("MinDistanceM = %v, want %v", got.MinDistanceM, tt.wantMinDist)
			}
			if tt.wantIntersects && math.Abs(got.EnterFrac-tt.wantEnterFrac) > 0.01 && tt.wantEnterFrac != 0 {
				t.Errorf("EnterFrac = %v, want %v", got.EnterFrac, tt.wantEnterFrac)
			}
		})
	}
}

// TestIntersectCircle_SparseSampling is the case the whole design turns on: at
// driving speed the logger may place no fix at all inside a 20 m radius, yet
// the crossing must still be detected from the chord between the two fixes.
func TestIntersectCircle_SparseSampling(t *testing.T) {
	// Two fixes 70 m apart, straddling the waypoint with a 5 m lateral offset.
	got := IntersectCircle(-35, 5, 35, 5, 20)
	if !got.Intersects {
		t.Fatal("a 70 m chord straddling the waypoint was not detected as a crossing")
	}
	if got.MinDistanceM != 5 {
		t.Errorf("MinDistanceM = %v, want 5", got.MinDistanceM)
	}
	// The chord inside a 20 m circle at 5 m offset is 2*sqrt(400-25) ~= 38.7 m.
	if math.Abs(got.InsideLengthM-38.73) > 0.1 {
		t.Errorf("InsideLengthM = %.2f, want ~38.73", got.InsideLengthM)
	}
	if math.Abs(got.ClosestFrac-0.5) > 0.001 {
		t.Errorf("ClosestFrac = %v, want 0.5", got.ClosestFrac)
	}
}

func TestIntersectCircle_CoincidentPoints(t *testing.T) {
	got := IntersectCircle(3, 4, 3, 4, 20)
	if !got.Intersects {
		t.Error("a duplicate fix inside the radius should count as a crossing")
	}
	if math.Abs(got.MinDistanceM-5) > 0.001 {
		t.Errorf("MinDistanceM = %v, want 5", got.MinDistanceM)
	}
}

func TestSimplify(t *testing.T) {
	// A straight line with a jitter-sized wobble collapses to its endpoints.
	pts := []LatLon{
		{52.0000, 21.0000},
		{52.0010, 21.0000},
		{52.0020, 21.0000},
		{52.0030, 21.0000},
	}
	if got := Simplify(pts, 4); len(got) != 2 {
		t.Errorf("Simplify of a straight line kept %d points, want 2", len(got))
	}

	// A genuine corner must be retained.
	pts = []LatLon{
		{52.0000, 21.0000},
		{52.0010, 21.0010},
		{52.0000, 21.0020},
	}
	if got := Simplify(pts, 4); len(got) != 3 {
		t.Errorf("Simplify of a corner kept %d points, want 3", len(got))
	}
}
