package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/service"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/stats"
)

// TestNewRenderer_ParsesEveryTemplate catches a malformed template at test time
// rather than when a page is first opened.
func TestNewRenderer_ParsesEveryTemplate(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Fatalf("newRenderer returned %v", err)
	}
	for _, name := range []string{"dashboard.gohtml", "trips.gohtml", "trip.gohtml", "route.gohtml", "stats.gohtml"} {
		if _, ok := r.pages[name]; !ok {
			t.Errorf("template %s was not parsed", name)
		}
	}
}

// TestRenderPages exercises each page with data shaped like the real thing, so
// that a field renamed in the domain shows up here instead of in the browser.
func TestRenderPages(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Fatalf("newRenderer returned %v", err)
	}

	dur := 212.0
	dist := 1450.0
	route := domain.Template{
		ID: "t1", Name: "Daily route", Version: 3,
		Waypoints: []domain.Waypoint{
			{ID: "w0", Seq: 0, Label: "Street end", Lat: 13.6929, Lon: -89.2182, RadiusM: 25},
			{ID: "w1", Seq: 1, Label: "Bridge", Lat: 52.2317, Lon: 21.0222, RadiusM: 25},
		},
	}
	trip := domain.Trip{
		ID: "trip1", LocalDate: "2026-09-09", LocalHour: 8, Direction: domain.DirectionForward,
		Status: domain.StatusPartial, MatchedWaypoints: 1, DurationS: 600, DistanceM: 4200,
		PointCount: 500, KeptPointCount: 494, StartedAt: time.Now(),
		Crossings: []domain.Crossing{{
			WaypointSeq: 0, CrossedAt: time.Now(), ClosestAt: time.Now(),
			ClosestDistanceM: 6.2, Method: domain.MethodInterpolated,
		}},
		Segments: []domain.Segment{
			{Seq: 0, Label: "Street end → Bridge", DurationS: &dur, DistanceM: &dist, IsComplete: true},
			{Seq: 1, Label: "Bridge → Gate"},
		},
	}
	report := service.Report{
		From: "2026-06-01", To: "2026-09-09", TotalTrips: 10,
		HoursPresent: []int{8, 12},
		Segments: []service.SegmentStats{{
			Seq: 0, Label: "Street end → Bridge", DistanceM: 1450,
			Duration:        stats.Summarise([]float64{200, 212, 230}),
			CongestionIndex: 1.15, MedianSpeedKPH: 24.6,
		}},
		Hours: []service.HourCell{
			{Seq: 0, Hour: 8, Count: 5, MedianS: 230, RelativeToBest: 1.4},
			{Seq: 0, Hour: 12, Count: 2, MedianS: 200, RelativeToBest: 1.0, Sparse: true},
		},
		Weeks: []service.WeekPoint{{Seq: 0, Week: "2026-W37", MedianS: 212, Count: 4}},
		Trips: []service.TripPoint{{Seq: 0, TripID: "trip1", Date: "2026-09-09", DurationS: 212}},
	}

	pages := map[string]map[string]any{
		"dashboard.gohtml": {
			"Title": "Dashboard", "Nav": "dashboard", "Report": report,
			"Trips": []domain.Trip{trip}, "Counts": map[string]int{string(domain.StatusMatched): 8},
			"Stale": 2, "LastRefresh": time.Now(), "Route": route, "RouteDefined": true,
			"DriveConfigured": true, "From": "2026-06-01", "To": "2026-09-09",
			"Progress": service.Progress{}, "Location": "America/El_Salvador",
		},
		"trips.gohtml": {
			"Title": "Trips", "Nav": "trips", "Trips": []domain.Trip{trip},
			"From": "2026-06-01", "To": "2026-09-09", "Direction": "",
		},
		"trip.gohtml": {"Title": "Trip", "Nav": "trips", "Trip": trip, "Route": route},
		"route.gohtml": {
			"Title": "Route", "Nav": "route", "Route": route,
			"Trips": []domain.Trip{trip}, "DefaultRadius": 25,
		},
		"stats.gohtml": {
			"Title": "Statistics", "Nav": "stats", "Report": report,
			"From": "2026-06-01", "To": "2026-09-09", "Direction": "",
		},
	}

	for name, data := range pages {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.page(w, httptest.NewRequest(http.MethodGet, "/", nil), name, data)

			body := w.Body.String()
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if strings.Contains(body, "render error") {
				t.Errorf("rendering failed: %s", body[strings.Index(body, "render error"):])
			}
			if !strings.Contains(body, "</html>") {
				t.Error("page was truncated")
			}
		})
	}
}

func TestRenderPartial_RefreshStatus(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Fatalf("newRenderer returned %v", err)
	}

	// A finished run must not ask the page to keep polling.
	w := httptest.NewRecorder()
	r.partial(w, "refresh-status", map[string]any{
		"Progress": service.Progress{
			StartedAt: time.Now(), FinishedAt: time.Now(), Total: 2, Done: 2,
			Outcomes: []service.FileOutcome{{
				Filename: "20260909064908.gpx", TripID: "trip1",
				Status: service.OutcomeImported, Detail: "matched, 4 of 4 waypoints",
			}},
		},
		"Poll": false,
	})

	body := w.Body.String()
	if strings.Contains(body, "render error") {
		t.Fatalf("rendering failed: %s", body)
	}
	if strings.Contains(body, "hx-trigger") {
		t.Error("a finished refresh should not keep polling")
	}
	if !strings.Contains(body, "20260909064908.gpx") {
		t.Error("the imported file was not listed")
	}

	// A running one must.
	w = httptest.NewRecorder()
	r.partial(w, "refresh-status", map[string]any{
		"Progress": service.Progress{Running: true, StartedAt: time.Now(), Total: 3, Done: 1},
		"Poll":     true,
	})
	if !strings.Contains(w.Body.String(), "hx-trigger") {
		t.Error("a running refresh should poll for progress")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "—"},
		{-5, "—"},
		{45, "45s"},
		{212, "3m 32s"},
		{3600, "60m 00s"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.seconds); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestHeatColour(t *testing.T) {
	// The fastest observed time is green; anything at or beyond twice that is
	// fully red, and the scale must not run past it.
	if got := string(heatColour(1.0)); !strings.Contains(got, "hsl(140") {
		t.Errorf("heatColour(1.0) = %q, want a green hue", got)
	}
	if got := string(heatColour(2.5)); !strings.Contains(got, "hsl(0") {
		t.Errorf("heatColour(2.5) = %q, want a red hue", got)
	}
	if got := string(heatColour(0)); !strings.Contains(got, "surface") {
		t.Errorf("heatColour(0) = %q, want the neutral surface colour", got)
	}
}
