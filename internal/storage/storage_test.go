package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open returned %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedRoute(t *testing.T, s *Store, labels ...string) domain.Template {
	t.Helper()
	tmpl, err := s.CreateTemplate(t.Context(), "Test route")
	if err != nil {
		t.Fatalf("CreateTemplate returned %v", err)
	}
	for i, label := range labels {
		_, err := s.AddWaypoint(t.Context(), tmpl.ID, domain.Waypoint{
			Label:   label,
			Lat:     52.0 + float64(i)/1000,
			Lon:     21.0,
			RadiusM: 20,
		})
		if err != nil {
			t.Fatalf("AddWaypoint returned %v", err)
		}
	}
	got, err := s.ActiveTemplate(t.Context())
	if err != nil {
		t.Fatalf("ActiveTemplate returned %v", err)
	}
	return got
}

func TestOpen_MigratesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for i := range 2 {
		s, err := Open(t.Context(), path)
		if err != nil {
			t.Fatalf("Open %d returned %v", i, err)
		}
		s.Close()
	}
}

func TestActiveTemplate_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ActiveTemplate(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Errorf("ActiveTemplate returned %v, want ErrNotFound", err)
	}
}

func TestWaypoints_AppendAndOrder(t *testing.T) {
	s := newTestStore(t)
	tmpl := seedRoute(t, s, "Home", "Bridge", "School")

	if len(tmpl.Waypoints) != 3 {
		t.Fatalf("got %d waypoints, want 3", len(tmpl.Waypoints))
	}
	for i, w := range tmpl.Waypoints {
		if w.Seq != i {
			t.Errorf("waypoint %q has Seq %d, want %d", w.Label, w.Seq, i)
		}
	}
	// Each waypoint change must raise the version so that stored trips are
	// recomputed against the new geometry.
	if tmpl.Version != 4 {
		t.Errorf("Version = %d, want 4 after three waypoint additions", tmpl.Version)
	}
}

func TestDeleteWaypoint_ClosesTheGap(t *testing.T) {
	s := newTestStore(t)
	tmpl := seedRoute(t, s, "Home", "Bridge", "School")

	if err := s.DeleteWaypoint(t.Context(), tmpl.ID, tmpl.Waypoints[1].ID); err != nil {
		t.Fatalf("DeleteWaypoint returned %v", err)
	}

	got, err := s.ActiveTemplate(t.Context())
	if err != nil {
		t.Fatalf("ActiveTemplate returned %v", err)
	}
	if len(got.Waypoints) != 2 {
		t.Fatalf("got %d waypoints, want 2", len(got.Waypoints))
	}
	if got.Waypoints[0].Label != "Home" || got.Waypoints[1].Label != "School" {
		t.Errorf("waypoints are %q and %q, want Home and School",
			got.Waypoints[0].Label, got.Waypoints[1].Label)
	}
	for i, w := range got.Waypoints {
		if w.Seq != i {
			t.Errorf("waypoint %q has Seq %d, want %d after renumbering", w.Label, w.Seq, i)
		}
	}
}

func TestMoveWaypoint(t *testing.T) {
	s := newTestStore(t)
	tmpl := seedRoute(t, s, "Home", "Bridge", "School")

	if err := s.MoveWaypoint(t.Context(), tmpl.ID, tmpl.Waypoints[2].ID, -1); err != nil {
		t.Fatalf("MoveWaypoint returned %v", err)
	}
	got, _ := s.ActiveTemplate(t.Context())
	if got.Waypoints[1].Label != "School" || got.Waypoints[2].Label != "Bridge" {
		t.Errorf("order is %q then %q, want School then Bridge",
			got.Waypoints[1].Label, got.Waypoints[2].Label)
	}

	// Moving the first waypoint earlier is a no-op rather than an error.
	if err := s.MoveWaypoint(t.Context(), tmpl.ID, got.Waypoints[0].ID, -1); err != nil {
		t.Errorf("MoveWaypoint past the start returned %v, want nil", err)
	}
}

func TestSaveTrip_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	tmpl := seedRoute(t, s, "Home", "Bridge")
	ctx := t.Context()

	start := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	dur := 210.0
	trip := domain.Trip{
		ID:               domain.NewID(),
		TemplateID:       tmpl.ID,
		TemplateVersion:  tmpl.Version,
		AlgoVersion:      1,
		SourceFilename:   "20260909063000.gpx",
		StartedAt:        start,
		EndedAt:          start.Add(10 * time.Minute),
		LocalDate:        "2026-09-09",
		LocalWeekday:     3,
		LocalHour:        8,
		Direction:        domain.DirectionForward,
		Status:           domain.StatusMatched,
		MatchedWaypoints: 2,
		ProcessedAt:      time.Now().UTC(),
		Crossings: []domain.Crossing{
			{WaypointID: tmpl.Waypoints[0].ID, WaypointSeq: 0, CrossedAt: start, Method: domain.MethodInterpolated},
			{WaypointID: tmpl.Waypoints[1].ID, WaypointSeq: 1, CrossedAt: start.Add(210 * time.Second)},
		},
		Segments: []domain.Segment{{
			TemplateID:     tmpl.ID,
			Seq:            0,
			FromWaypointID: tmpl.Waypoints[0].ID,
			ToWaypointID:   tmpl.Waypoints[1].ID,
			Label:          "Home → Bridge",
			Direction:      domain.DirectionForward,
			StartedAt:      start,
			EndedAt:        start.Add(210 * time.Second),
			LocalDate:      "2026-09-09",
			LocalHour:      8,
			DurationS:      &dur,
			IsComplete:     true,
		}},
	}

	if err := s.SaveTrip(ctx, trip, "abc123", []byte("payload"), 12); err != nil {
		t.Fatalf("SaveTrip returned %v", err)
	}

	got, err := s.Trip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("Trip returned %v", err)
	}
	if got.Status != domain.StatusMatched || got.Direction != domain.DirectionForward {
		t.Errorf("status/direction = %q/%q, want matched/forward", got.Status, got.Direction)
	}
	if !got.StartedAt.Equal(start) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, start)
	}
	if len(got.Crossings) != 2 || len(got.Segments) != 1 {
		t.Fatalf("got %d crossings and %d segments, want 2 and 1", len(got.Crossings), len(got.Segments))
	}
	if got.Segments[0].DurationS == nil || *got.Segments[0].DurationS != dur {
		t.Errorf("segment duration did not round-trip: %v", got.Segments[0].DurationS)
	}

	// Saving again must replace the derived rows rather than duplicate them.
	if err := s.SaveTrip(ctx, trip, "abc123", []byte("payload"), 12); err != nil {
		t.Fatalf("second SaveTrip returned %v", err)
	}
	got, _ = s.Trip(ctx, trip.ID)
	if len(got.Crossings) != 2 || len(got.Segments) != 1 {
		t.Errorf("re-saving duplicated derived rows: %d crossings, %d segments",
			len(got.Crossings), len(got.Segments))
	}

	payload, err := s.TrackPayload(ctx, trip.ID)
	if err != nil || string(payload) != "payload" {
		t.Errorf("TrackPayload = %q, %v; want \"payload\", nil", payload, err)
	}
}

func TestSegments_ExcludesUnmeasuredStretches(t *testing.T) {
	s := newTestStore(t)
	tmpl := seedRoute(t, s, "Home", "Bridge", "School")
	ctx := t.Context()

	dur := 180.0
	start := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	trip := domain.Trip{
		ID: domain.NewID(), TemplateID: tmpl.ID, TemplateVersion: tmpl.Version, AlgoVersion: 1,
		SourceFilename: "x.gpx", StartedAt: start, LocalDate: "2026-09-09", LocalHour: 8,
		Status: domain.StatusPartial, ProcessedAt: time.Now().UTC(),
		Segments: []domain.Segment{
			{TemplateID: tmpl.ID, Seq: 0, FromWaypointID: tmpl.Waypoints[0].ID,
				ToWaypointID: tmpl.Waypoints[1].ID, Label: "a", LocalDate: "2026-09-09",
				LocalHour: 8, DurationS: &dur, IsComplete: true},
			// The second stretch was not measured; it must be stored but must
			// never appear as a measurement.
			{TemplateID: tmpl.ID, Seq: 1, FromWaypointID: tmpl.Waypoints[1].ID,
				ToWaypointID: tmpl.Waypoints[2].ID, Label: "b", IsComplete: false},
		},
	}
	if err := s.SaveTrip(ctx, trip, "sha", nil, 0); err != nil {
		t.Fatalf("SaveTrip returned %v", err)
	}

	segs, err := s.Segments(ctx, SegmentQuery{TemplateID: tmpl.ID})
	if err != nil {
		t.Fatalf("Segments returned %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("got %d measurements, want 1 (the unmeasured stretch must be excluded)", len(segs))
	}
	if segs[0].Seq != 0 {
		t.Errorf("got measurement for stretch %d, want 0", segs[0].Seq)
	}

	// The unmeasured stretch is still recorded against the trip itself.
	full, _ := s.Trip(ctx, trip.ID)
	if len(full.Segments) != 2 {
		t.Errorf("trip holds %d stretches, want 2", len(full.Segments))
	}
}

func TestStaleTripIDs(t *testing.T) {
	s := newTestStore(t)
	tmpl := seedRoute(t, s, "Home", "Bridge")
	ctx := t.Context()

	trip := domain.Trip{
		ID: domain.NewID(), TemplateID: tmpl.ID, TemplateVersion: tmpl.Version, AlgoVersion: 1,
		SourceFilename: "x.gpx", Status: domain.StatusMatched, ProcessedAt: time.Now().UTC(),
	}
	if err := s.SaveTrip(ctx, trip, "sha", nil, 0); err != nil {
		t.Fatalf("SaveTrip returned %v", err)
	}

	stale, err := s.StaleTripIDs(ctx, tmpl.ID, tmpl.Version, 1)
	if err != nil {
		t.Fatalf("StaleTripIDs returned %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("a freshly measured trip should not be stale, got %v", stale)
	}

	// Changing the route must mark the trip for recomputation.
	if err := s.UpdateWaypoint(ctx, domain.Waypoint{
		ID: tmpl.Waypoints[0].ID, TemplateID: tmpl.ID, Label: "Home", Lat: 52.0, Lon: 21.0, RadiusM: 40,
	}); err != nil {
		t.Fatalf("UpdateWaypoint returned %v", err)
	}
	updated, _ := s.ActiveTemplate(ctx)
	stale, err = s.StaleTripIDs(ctx, tmpl.ID, updated.Version, 1)
	if err != nil {
		t.Fatalf("StaleTripIDs returned %v", err)
	}
	if len(stale) != 1 || stale[0] != trip.ID {
		t.Errorf("stale trips = %v, want [%s] after a waypoint change", stale, trip.ID)
	}
}

func TestProcessedFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	ok, err := s.IsProcessed(ctx, "20260909063000.gpx")
	if err != nil || ok {
		t.Fatalf("IsProcessed on an unknown file = %v, %v; want false, nil", ok, err)
	}

	err = s.MarkProcessed(ctx, domain.ProcessedFile{
		Filename: "20260909063000.gpx", SHA256: "abc",
		FileTimeUTC: time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC),
		ProcessedAt: time.Now().UTC(), Status: domain.FileStatusOK,
	})
	if err != nil {
		t.Fatalf("MarkProcessed returned %v", err)
	}

	if ok, _ := s.IsProcessed(ctx, "20260909063000.gpx"); !ok {
		t.Error("IsProcessed should report a recorded file")
	}

	names, err := s.ProcessedNames(ctx)
	if err != nil {
		t.Fatalf("ProcessedNames returned %v", err)
	}
	if _, found := names["20260909063000.gpx"]; !found {
		t.Error("ProcessedNames omitted a recorded file")
	}

	// A failure is recorded too, so that a broken file is not retried on every
	// refresh, and can be cleared explicitly.
	if err := s.MarkProcessed(ctx, domain.ProcessedFile{
		Filename: "broken.gpx", ProcessedAt: time.Now().UTC(),
		Status: domain.FileStatusError, ErrorMessage: "no timestamps",
	}); err != nil {
		t.Fatalf("MarkProcessed for a failure returned %v", err)
	}
	failed, err := s.FailedFiles(ctx, 10)
	if err != nil || len(failed) != 1 || failed[0].Filename != "broken.gpx" {
		t.Fatalf("FailedFiles = %v, %v; want the one broken file", failed, err)
	}

	if err := s.Forget(ctx, "broken.gpx"); err != nil {
		t.Fatalf("Forget returned %v", err)
	}
	if ok, _ := s.IsProcessed(ctx, "broken.gpx"); ok {
		t.Error("a forgotten file should be eligible for import again")
	}
}
