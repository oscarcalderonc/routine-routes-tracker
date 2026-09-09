package geo

import (
	"math"
	"testing"
)

func TestHaversine_KnownDistance(t *testing.T) {
	// One degree of latitude is close to 111 km anywhere on the globe.
	got := Haversine(52.0, 21.0, 53.0, 21.0)
	if math.Abs(got-111195) > 500 {
		t.Errorf("Haversine over one degree of latitude = %.0f m, want ~111195 m", got)
	}
}

func TestFrame_ProjectRoundTrip(t *testing.T) {
	f := NewFrame(52.2297, 21.0122)
	x, y := f.Project(52.2297, 21.0122)
	if x != 0 || y != 0 {
		t.Errorf("Project at origin = (%v, %v), want (0, 0)", x, y)
	}

	// A point due north should project to a positive y and a negligible x, and
	// its magnitude should agree with the spherical distance.
	x, y = f.Project(52.2397, 21.0122)
	if math.Abs(x) > 0.001 {
		t.Errorf("Project due north gave x = %v, want ~0", x)
	}
	want := Haversine(52.2297, 21.0122, 52.2397, 21.0122)
	if math.Abs(y-want) > 1 {
		t.Errorf("Project due north gave y = %.2f m, want %.2f m", y, want)
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
