package service

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/drive"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/geo"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/matcher"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/storage"
)

const (
	testLat = 13.6929
	testLon = -89.2182
)

func degLon(m float64) float64 {
	f := geo.NewFrame(testLat, testLon)
	perDeg, _ := f.Project(testLat, testLon+1)
	return m / perDeg
}

func degLat(m float64) float64 {
	f := geo.NewFrame(testLat, testLon)
	_, perDeg := f.Project(testLat+1, testLon)
	return m / perDeg
}

// writeGPX creates a recording that drives east from fromM to toM, sampled at a
// realistic 5 s interval at 50 km/h, and names it the way the recorder does.
func writeGPX(t *testing.T, dir string, start time.Time, fromM, toM float64) string {
	t.Helper()

	const spacingM = 70.0
	const speedMS = 50.0 / 3.6
	dir1 := 1.0
	if toM < fromM {
		dir1 = -1
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n")
	b.WriteString(`<gpx version="1.1" creator="test"><trk><trkseg>` + "\n")
	elapsed := 0.0
	for d := fromM; (toM-d)*dir1 >= 0; d += spacingM * dir1 {
		ts := start.Add(time.Duration(elapsed * float64(time.Second)))
		fmt.Fprintf(&b, `<trkpt lat="%.7f" lon="%.7f"><time>%s</time><hdop>1.2</hdop></trkpt>`+"\n",
			testLat+degLat(5), testLon+degLon(d), ts.UTC().Format(time.RFC3339))
		elapsed += spacingM / speedMS
	}
	b.WriteString(`</trkseg></trk></gpx>` + "\n")

	name := start.UTC().Format("20060102150405") + ".gpx"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}
	return name
}

// writeGPXWithTail records a drive along the route and then keeps recording:
// five minutes stationary at the destination followed by a slow loop back
// through it, as happens when the recorder is not stopped on arrival.
func writeGPXWithTail(t *testing.T, dir string, start time.Time) string {
	t.Helper()

	const spacingM = 70.0
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n")
	b.WriteString(`<gpx version="1.1" creator="test"><trk><trkseg>` + "\n")

	elapsed := 0.0
	write := func(offsetM float64) {
		ts := start.Add(time.Duration(elapsed * float64(time.Second)))
		fmt.Fprintf(&b, `<trkpt lat="%.7f" lon="%.7f"><time>%s</time><hdop>1.2</hdop></trkpt>`+"\n",
			testLat+degLat(5), testLon+degLon(offsetM), ts.UTC().Format(time.RFC3339))
	}

	// The drive: -100 m to 1100 m at 50 km/h.
	for d := -100.0; d <= 1100; d += spacingM {
		write(d)
		elapsed += spacingM / (50.0 / 3.6)
	}
	// Sitting at the destination with the recorder still on.
	for range 10 {
		write(1080)
		elapsed += 30
	}
	// Then a slow loop that carries the track back through the destination.
	for d := 1080.0; d >= 900; d -= spacingM {
		write(d)
		elapsed += spacingM / (15.0 / 3.6)
	}
	for d := 900.0; d <= 1100; d += spacingM {
		write(d)
		elapsed += spacingM / (15.0 / 3.6)
	}
	b.WriteString(`</trkseg></trk></gpx>` + "\n")

	name := start.UTC().Format("20060102150405") + ".gpx"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}
	return name
}

func newTestService(t *testing.T) (*Service, *storage.Store) {
	t.Helper()

	root := t.TempDir()
	store, err := storage.Open(t.Context(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	svc, err := New(Options{
		Store:    store,
		Puller:   drive.Puller{}, // Not configured: files are read straight from the inbox.
		Location: time.UTC,
		Anchor:   matcher.AnchorEntry,
		BlobDir:  filepath.Join(root, "gpx"),
		InboxDir: filepath.Join(root, "inbox"),
		MaxBytes: 1 << 20,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, store
}

func seedRoute(t *testing.T, store *storage.Store, offsets ...float64) domain.Template {
	t.Helper()

	tmpl, err := store.CreateTemplate(t.Context(), "Daily route")
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	for i, off := range offsets {
		_, err := store.AddWaypoint(t.Context(), tmpl.ID, domain.Waypoint{
			Label:   fmt.Sprintf("WP%d", i),
			Lat:     testLat,
			Lon:     testLon + degLon(off),
			RadiusM: 20,
		})
		if err != nil {
			t.Fatalf("add waypoint: %v", err)
		}
	}
	got, err := store.ActiveTemplate(t.Context())
	if err != nil {
		t.Fatalf("load route: %v", err)
	}
	return got
}

func TestRefresh_ImportsMeasuresAndDeduplicates(t *testing.T) {
	svc, store := newTestService(t)
	seedRoute(t, store, 0, 500, 1000, 1500)
	ctx := t.Context()

	start := time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC)
	name := writeGPX(t, svc.inboxDir, start, -100, 1600)

	svc.Refresh(ctx)

	progress := svc.Progress()
	if progress.Running {
		t.Fatal("Refresh returned while still running")
	}
	if progress.Total != 1 || progress.Done != 1 {
		t.Fatalf("progress = %d of %d, want 1 of 1", progress.Done, progress.Total)
	}
	if len(progress.Outcomes) != 1 || progress.Outcomes[0].Status != OutcomeImported {
		t.Fatalf("outcomes = %+v, want one import", progress.Outcomes)
	}

	trips, err := store.ListTrips(ctx, storage.TripFilter{})
	if err != nil {
		t.Fatalf("list trips: %v", err)
	}
	if len(trips) != 1 {
		t.Fatalf("got %d trips, want 1", len(trips))
	}

	trip, err := store.Trip(ctx, trips[0].ID)
	if err != nil {
		t.Fatalf("load trip: %v", err)
	}
	if trip.Status != domain.StatusMatched {
		t.Errorf("Status = %q, want matched (%d waypoints reached)", trip.Status, trip.MatchedWaypoints)
	}
	if trip.SourceFilename != name {
		t.Errorf("SourceFilename = %q, want %q", trip.SourceFilename, name)
	}
	if trip.Direction != domain.DirectionForward {
		t.Errorf("Direction = %q, want forward", trip.Direction)
	}
	if len(trip.Segments) != 3 {
		t.Fatalf("got %d stretches, want 3", len(trip.Segments))
	}
	for i, sg := range trip.Segments {
		if sg.DurationS == nil {
			t.Fatalf("stretch %d was not measured", i)
		}
		// 500 m at 50 km/h is 36 s.
		if *sg.DurationS < 30 || *sg.DurationS > 42 {
			t.Errorf("stretch %d took %.0f s, want about 36 s", i, *sg.DurationS)
		}
	}

	// The drawn path must have been stored for the map.
	if _, err := store.TrackPayload(ctx, trip.ID); err != nil {
		t.Errorf("TrackPayload returned %v", err)
	}

	// A second refresh must not import the same recording again: the filename
	// is the deduplication key.
	svc.Refresh(ctx)
	if p := svc.Progress(); p.Total != 0 {
		t.Errorf("second refresh found %d files, want 0", p.Total)
	}
	trips, _ = store.ListTrips(ctx, storage.TripFilter{})
	if len(trips) != 1 {
		t.Errorf("got %d trips after a second refresh, want 1", len(trips))
	}
}

func TestRefresh_RecordsUnreadableFilesWithoutRetrying(t *testing.T) {
	svc, store := newTestService(t)
	seedRoute(t, store, 0, 500)
	ctx := t.Context()

	path := filepath.Join(svc.inboxDir, "20260909063000.gpx")
	if err := os.WriteFile(path, []byte("this is not gpx"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	svc.Refresh(ctx)

	p := svc.Progress()
	if len(p.Outcomes) != 1 || p.Outcomes[0].Status != OutcomeFailed {
		t.Fatalf("outcomes = %+v, want one failure", p.Outcomes)
	}

	failed, err := store.SkippedFiles(ctx, 10)
	if err != nil || len(failed) != 1 {
		t.Fatalf("SkippedFiles = %v, %v; want one record", failed, err)
	}

	// The failure is remembered, so the next refresh does not try again.
	svc.Refresh(ctx)
	if p := svc.Progress(); p.Total != 0 {
		t.Errorf("a failed file was retried: %d files considered", p.Total)
	}
}

func TestRefresh_RecognisesTheReturnJourney(t *testing.T) {
	svc, store := newTestService(t)
	seedRoute(t, store, 0, 500, 1000)
	ctx := t.Context()

	writeGPX(t, svc.inboxDir, time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC), -100, 1100)
	writeGPX(t, svc.inboxDir, time.Date(2026, 9, 9, 10, 15, 0, 0, time.UTC), 1100, -100)
	svc.Refresh(ctx)

	trips, err := store.ListTrips(ctx, storage.TripFilter{})
	if err != nil {
		t.Fatalf("list trips: %v", err)
	}
	if len(trips) != 2 {
		t.Fatalf("got %d trips, want 2", len(trips))
	}

	directions := map[domain.Direction]int{}
	for _, tr := range trips {
		directions[tr.Direction]++
		if tr.Status != domain.StatusMatched {
			t.Errorf("trip %s status = %q, want matched", tr.LocalDate, tr.Status)
		}
	}
	if directions[domain.DirectionForward] != 1 || directions[domain.DirectionReverse] != 1 {
		t.Errorf("directions = %v, want one of each", directions)
	}

	// Both directions must land in the same buckets, since a stretch of road is
	// the same stretch whichever way it is driven.
	report, err := svc.Stats(ctx, "2026-01-01", "2027-01-01", "")
	if err != nil {
		t.Fatalf("Stats returned %v", err)
	}
	if len(report.Segments) != 2 {
		t.Fatalf("got %d stretches, want 2", len(report.Segments))
	}
	for _, seg := range report.Segments {
		if seg.Duration.Count != 2 {
			t.Errorf("stretch %q has %d measurements, want 2 (both directions pooled)",
				seg.Label, seg.Duration.Count)
		}
	}
	if report.TotalTrips != 2 {
		t.Errorf("TotalTrips = %d, want 2", report.TotalTrips)
	}
}

func TestReprocess_RecomputesAfterAWaypointMoves(t *testing.T) {
	svc, store := newTestService(t)
	tmpl := seedRoute(t, store, 0, 500, 1000)
	ctx := t.Context()

	writeGPX(t, svc.inboxDir, time.Date(2026, 9, 9, 6, 30, 0, 0, time.UTC), -100, 1100)
	svc.Refresh(ctx)

	before, _ := store.ListTrips(ctx, storage.TripFilter{})
	if len(before) != 1 {
		t.Fatalf("got %d trips, want 1", len(before))
	}
	original, _ := store.Trip(ctx, before[0].ID)
	if original.Status != domain.StatusMatched {
		t.Fatalf("Status = %q, want matched before the edit", original.Status)
	}

	// Move the middle waypoint far off the road so it can no longer be reached.
	mid := tmpl.Waypoints[1]
	mid.Lat = testLat + degLat(300)
	if err := store.UpdateWaypoint(ctx, mid); err != nil {
		t.Fatalf("update waypoint: %v", err)
	}

	stale, err := svc.StaleCount(ctx)
	if err != nil || stale != 1 {
		t.Fatalf("StaleCount = %d, %v; want 1", stale, err)
	}

	if err := svc.Reprocess(ctx); err != nil {
		t.Fatalf("Reprocess returned %v", err)
	}

	after, err := store.Trip(ctx, original.ID)
	if err != nil {
		t.Fatalf("load trip: %v", err)
	}
	if after.ID != original.ID {
		t.Error("recomputing must keep the trip's identity")
	}
	if after.Status != domain.StatusPartial {
		t.Errorf("Status = %q, want partial once a waypoint is unreachable", after.Status)
	}
	for _, sg := range after.Segments {
		if sg.DurationS != nil {
			t.Errorf("stretch %d still has a duration though its waypoint is unreachable", sg.Seq)
		}
	}

	if stale, _ := svc.StaleCount(ctx); stale != 0 {
		t.Errorf("StaleCount = %d after recomputing, want 0", stale)
	}
}

func TestFileTime(t *testing.T) {
	got, ok := FileTime("20260909164908.gpx")
	if !ok {
		t.Fatal("a well-formed name was not recognised")
	}
	want := time.Date(2026, 9, 9, 16, 49, 8, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("FileTime = %v, want %v", got, want)
	}

	if _, ok := FileTime("holiday-drive.gpx"); ok {
		t.Error("an arbitrary name should not yield a time")
	}
}

func TestRefresh_IgnoresRecordingsThatDoNotFollowTheRoute(t *testing.T) {
	svc, store := newTestService(t)
	seedRoute(t, store, 0, 500, 1000, 1500)
	ctx := t.Context()

	// A short demo recording that only ever reaches the first waypoint.
	writeGPX(t, svc.inboxDir, time.Date(2026, 9, 10, 6, 30, 0, 0, time.UTC), -100, 150)
	// And a real drive, to show the two are told apart.
	writeGPX(t, svc.inboxDir, time.Date(2026, 9, 10, 13, 0, 0, 0, time.UTC), -100, 1600)

	svc.Refresh(ctx)

	outcomes := map[string]int{}
	for _, o := range svc.Progress().Outcomes {
		outcomes[o.Status]++
	}
	if outcomes[OutcomeIgnored] != 1 || outcomes[OutcomeImported] != 1 {
		t.Fatalf("outcomes = %v, want one ignored and one imported", outcomes)
	}

	trips, err := store.ListTrips(ctx, storage.TripFilter{})
	if err != nil {
		t.Fatalf("list trips: %v", err)
	}
	if len(trips) != 1 {
		t.Fatalf("got %d trips, want 1: the demo recording should not have become a trip", len(trips))
	}
	if trips[0].Status != domain.StatusMatched {
		t.Errorf("the stored trip has status %q, want matched", trips[0].Status)
	}

	// The ignored file is still recorded as seen, so it is not reconsidered.
	svc.Refresh(ctx)
	if p := svc.Progress(); p.Total != 0 {
		t.Errorf("an ignored file was reconsidered: %d files", p.Total)
	}

	skipped, err := store.SkippedFiles(ctx, 10)
	if err != nil || len(skipped) != 1 {
		t.Fatalf("SkippedFiles = %v, %v; want the one ignored file", skipped, err)
	}
	if skipped[0].Status != domain.FileStatusIgnored {
		t.Errorf("status = %q, want %q", skipped[0].Status, domain.FileStatusIgnored)
	}
}

func TestRetry_ReconsidersAnIgnoredRecording(t *testing.T) {
	svc, store := newTestService(t)
	ctx := t.Context()

	// A route whose waypoints are nowhere near the recording, as it would be
	// before the waypoints have been placed properly.
	tmpl := seedRoute(t, store, 0, 500)
	for _, w := range tmpl.Waypoints {
		w.Lat = testLat + degLat(5000)
		if err := store.UpdateWaypoint(ctx, w); err != nil {
			t.Fatalf("update waypoint: %v", err)
		}
	}

	name := writeGPX(t, svc.inboxDir, time.Date(2026, 9, 10, 6, 30, 0, 0, time.UTC), -100, 600)
	svc.Refresh(ctx)

	if trips, _ := store.ListTrips(ctx, storage.TripFilter{}); len(trips) != 0 {
		t.Fatalf("got %d trips, want 0 while the route is misplaced", len(trips))
	}

	// Put the waypoints where the road actually is, then reconsider the file.
	current, _ := store.ActiveTemplate(ctx)
	for i, w := range current.Waypoints {
		w.Lat = testLat
		w.Lon = testLon + degLon(float64(i)*500)
		if err := store.UpdateWaypoint(ctx, w); err != nil {
			t.Fatalf("update waypoint: %v", err)
		}
	}

	if err := svc.Retry(ctx, name); err != nil {
		t.Fatalf("Retry returned %v", err)
	}

	trips, err := store.ListTrips(ctx, storage.TripFilter{})
	if err != nil {
		t.Fatalf("list trips: %v", err)
	}
	if len(trips) != 1 {
		t.Fatalf("got %d trips after retrying, want 1", len(trips))
	}
	if trips[0].Status != domain.StatusMatched {
		t.Errorf("status = %q, want matched once the waypoints are in place", trips[0].Status)
	}
}

// TestRefresh_TrimsFixesRecordedAfterArrival is the end-to-end form of leaving
// the recorder running: the extra fixes must not appear in the trip's duration,
// its distance, or the path drawn on the map.
func TestRefresh_TrimsFixesRecordedAfterArrival(t *testing.T) {
	svc, store := newTestService(t)
	seedRoute(t, store, 0, 500, 1000)
	ctx := t.Context()

	start := time.Date(2026, 9, 10, 6, 30, 0, 0, time.UTC)
	writeGPXWithTail(t, svc.inboxDir, start)
	svc.Refresh(ctx)

	trips, err := store.ListTrips(ctx, storage.TripFilter{})
	if err != nil || len(trips) != 1 {
		t.Fatalf("ListTrips = %v, %v; want one trip", trips, err)
	}
	trip, err := store.Trip(ctx, trips[0].ID)
	if err != nil {
		t.Fatalf("load trip: %v", err)
	}

	// The drive itself is 1200 m at 50 km/h, about 86 s. The recording runs for
	// a further ten minutes.
	if trip.DurationS > 180 {
		t.Errorf("trip duration = %.0f s; the idle tail was counted", trip.DurationS)
	}
	if trip.EndedAt.After(start.Add(3 * time.Minute)) {
		t.Errorf("trip ends at %v, long after the drive did", trip.EndedAt)
	}
	for _, sg := range trip.Segments {
		if sg.DurationS == nil {
			t.Fatalf("stretch %d was not measured", sg.Seq)
		}
		if *sg.DurationS > 60 {
			t.Errorf("stretch %d took %.0f s, want about 36 s", sg.Seq, *sg.DurationS)
		}
	}
}
