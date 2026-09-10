package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/matcher"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/storage"
)

// Source copies newly recorded files into the inbox directory.
//
// It is declared here, where it is used, rather than alongside an
// implementation. A nil Source means no cloud folder is in use, which is a
// supported way to run: recordings put into the inbox by any other means are
// imported just the same.
type Source interface {
	// Pull copies anything new into the inbox.
	Pull(ctx context.Context) error
	// Configured reports whether a folder has actually been set up.
	Configured() bool
}

// Service coordinates importing recordings and keeping their measurements up to
// date with the route definition.
type Service struct {
	store    *storage.Store
	source   Source
	loc      *time.Location
	anchor   matcher.Anchor
	blobDir  string
	inboxDir string
	maxBytes int64
	log      *slog.Logger

	// refreshMu makes a refresh single-flight: a second request while one is
	// running observes the first rather than starting a competing scan.
	refreshMu  sync.Mutex
	progress   Progress
	progressMu sync.RWMutex
}

// Options configures a Service.
type Options struct {
	Store    *storage.Store
	Source   Source
	Location *time.Location
	Anchor   matcher.Anchor
	BlobDir  string
	InboxDir string
	MaxBytes int64
	Logger   *slog.Logger
}

// New creates a Service and ensures its working directories exist.
func New(opts Options) (*Service, error) {
	for _, dir := range []string{opts.BlobDir, opts.InboxDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:    opts.Store,
		source:   opts.Source,
		loc:      opts.Location,
		anchor:   opts.Anchor,
		blobDir:  opts.BlobDir,
		inboxDir: opts.InboxDir,
		maxBytes: opts.MaxBytes,
		log:      log,
	}, nil
}

// Store exposes the database for read paths that need no application logic.
func (s *Service) Store() *storage.Store { return s.store }

// Location is the timezone measurements are reported in.
func (s *Service) Location() *time.Location { return s.loc }

// DriveConfigured reports whether a cloud folder has been set up.
func (s *Service) DriveConfigured() bool { return s.source != nil && s.source.Configured() }

// FileOutcome is what happened to one file during a refresh.
type FileOutcome struct {
	Filename string
	TripID   string
	Status   string
	Detail   string
}

// Outcomes a file may have during a refresh.
const (
	// OutcomeImported means a new trip was created.
	OutcomeImported = "imported"
	// OutcomeSkipped means the file had already been imported.
	OutcomeSkipped = "skipped"
	// OutcomeFailed means the file could not be read.
	OutcomeFailed = "failed"
	// OutcomeIgnored means the file was readable but did not follow the route,
	// so it produced no trip.
	OutcomeIgnored = "ignored"
)

// Progress describes a refresh, whether running or finished.
type Progress struct {
	Running    bool
	StartedAt  time.Time
	FinishedAt time.Time
	Total      int
	Done       int
	Outcomes   []FileOutcome
	Err        string
}

// Progress returns a snapshot of the most recent refresh.
func (s *Service) Progress() Progress {
	s.progressMu.RLock()
	defer s.progressMu.RUnlock()

	p := s.progress
	p.Outcomes = append([]FileOutcome(nil), s.progress.Outcomes...)
	return p
}

func (s *Service) setProgress(fn func(*Progress)) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	fn(&s.progress)
}

// Refresh pulls any new recordings from the configured remote and imports them.
//
// It is deliberately manual rather than scheduled: the recordings arrive twice
// a day and a button gives a clear moment at which to look at what happened,
// without a background timer to reason about.
func (s *Service) Refresh(ctx context.Context) {
	if !s.refreshMu.TryLock() {
		// A refresh is already under way; the caller will see its progress.
		return
	}
	defer s.refreshMu.Unlock()

	s.setProgress(func(p *Progress) {
		*p = Progress{Running: true, StartedAt: time.Now().UTC()}
	})

	err := s.refresh(ctx)

	s.setProgress(func(p *Progress) {
		p.Running = false
		p.FinishedAt = time.Now().UTC()
		if err != nil {
			p.Err = err.Error()
		}
	})
}

func (s *Service) refresh(ctx context.Context) error {
	if s.source != nil {
		if err := s.source.Pull(ctx); err != nil {
			// A failed pull is reported but does not stop the import: files
			// already in the inbox are still worth processing.
			s.log.Error("pull from cloud folder failed", "error", err)
			s.setProgress(func(p *Progress) { p.Err = err.Error() })
		}
	}

	names, err := s.newFiles(ctx)
	if err != nil {
		return err
	}
	s.setProgress(func(p *Progress) { p.Total = len(names) })

	for _, name := range names {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		outcome := s.ingestFile(ctx, name)
		s.setProgress(func(p *Progress) {
			p.Done++
			p.Outcomes = append(p.Outcomes, outcome)
		})
	}

	// New recordings may have arrived for a route that has since been edited,
	// so bring everything up to date in the same pass.
	if err := s.Reprocess(ctx); err != nil {
		s.log.Error("recompute after refresh failed", "error", err)
	}
	return nil
}

// newFiles lists recordings in the inbox that have not been imported, oldest
// first so that a backlog is imported in the order it was recorded.
func (s *Service) newFiles(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.inboxDir)
	if err != nil {
		return nil, fmt.Errorf("read inbox: %w", err)
	}
	seen, err := s.store.ProcessedNames(ctx)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".gpx") {
			continue
		}
		if _, done := seen[e.Name()]; done {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// ingestFile imports one file and records the outcome, including failures, so
// that an unreadable recording is not retried on every subsequent refresh.
func (s *Service) ingestFile(ctx context.Context, name string) FileOutcome {
	fileTime, _ := FileTime(name)

	data, err := os.ReadFile(filepath.Join(s.inboxDir, name))
	if err == nil && int64(len(data)) > s.maxBytes {
		err = fmt.Errorf("file exceeds %d bytes", s.maxBytes)
	}

	var trip domain.Trip
	if err == nil {
		trip, err = s.Ingest(ctx, name, data)
	}

	record := domain.ProcessedFile{
		Filename:    name,
		FileTimeUTC: fileTime,
		ProcessedAt: time.Now().UTC(),
	}
	outcome := FileOutcome{Filename: name}

	switch {
	case errors.Is(err, ErrNotOnRoute):
		// Not a failure: the recording is readable but describes something other
		// than the route, such as a test recording. No trip is created, and the
		// file is recorded as seen so that it is not considered again.
		record.Status = domain.FileStatusIgnored
		record.ErrorMessage = err.Error()
		outcome.Status, outcome.Detail = OutcomeIgnored, err.Error()
		s.log.Info("ignoring recording that does not follow the route", "file", name, "reason", err)
	case err != nil:
		record.Status = domain.FileStatusError
		record.ErrorMessage = err.Error()
		outcome.Status, outcome.Detail = OutcomeFailed, err.Error()
		s.log.Warn("could not import recording", "file", name, "error", err)
	default:
		record.Status = domain.FileStatusOK
		record.TripID = trip.ID
		outcome.Status = OutcomeImported
		outcome.TripID = trip.ID
		outcome.Detail = fmt.Sprintf("%s, %d of %d waypoints",
			trip.Status, trip.MatchedWaypoints, trip.MatchedWaypoints+missing(trip))
	}

	if err := s.store.MarkProcessed(ctx, record); err != nil {
		s.log.Error("could not record import outcome", "file", name, "error", err)
	}
	return outcome
}

func missing(t domain.Trip) int {
	var n int
	for _, sg := range t.Segments {
		if !sg.IsComplete {
			n++
		}
	}
	return n
}

// Reprocess recomputes every trip whose measurements were taken against an
// older route definition or an older matching algorithm.
//
// The set of stale trips is derived from stored state rather than tracked in a
// queue, so nothing needs recovering after a restart and running it twice is
// harmless. Recomputing the whole history takes a couple of seconds.
func (s *Service) Reprocess(ctx context.Context) error {
	tmpl, err := s.store.ActiveTemplate(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load route: %w", err)
	}

	ids, err := s.store.StaleTripIDs(ctx, tmpl.ID, tmpl.Version, matcher.AlgoVersion)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.reprocessTrip(ctx, tmpl, id); err != nil {
			s.log.Error("could not recompute trip", "trip", id, "error", err)
		}
	}
	if len(ids) > 0 {
		s.log.Info("recomputed trips", "count", len(ids), "route_version", tmpl.Version)
	}
	return nil
}

// ReprocessTrip recomputes a single trip against the current route.
func (s *Service) ReprocessTrip(ctx context.Context, tripID string) error {
	tmpl, err := s.store.ActiveTemplate(ctx)
	if err != nil {
		return fmt.Errorf("load route: %w", err)
	}
	return s.reprocessTrip(ctx, tmpl, tripID)
}

func (s *Service) reprocessTrip(ctx context.Context, tmpl domain.Template, tripID string) error {
	existing, err := s.store.Trip(ctx, tripID)
	if err != nil {
		return err
	}
	sha, err := s.store.TripSourceSHA(ctx, tripID)
	if err != nil {
		return err
	}
	data, err := s.readBlob(sha)
	if err != nil {
		return err
	}

	track, err := parseBytes(data, s.maxBytes)
	if err != nil {
		return err
	}

	trip, journey := s.measure(tmpl, track, existing.SourceFilename, sha)
	if trip.Status == domain.StatusUnmatched {
		// The route has been edited to the point where this recording no longer
		// follows it. Keeping it would leave a trip that measures nothing, so it
		// goes; the source file stays on disk and recorded as seen.
		s.log.Info("removing trip that no longer follows the route",
			"trip", existing.ID, "file", existing.SourceFilename)
		return s.store.DeleteTrip(ctx, existing.ID)
	}

	// Keep the identity of the existing trip so that anything referring to it
	// stays valid; only the measurements are replaced.
	trip.ID = existing.ID
	for i := range trip.Crossings {
		trip.Crossings[i].TripID = trip.ID
	}
	for i := range trip.Segments {
		trip.Segments[i].TripID = trip.ID
	}

	payload, points, err := encodeTrack(journey)
	if err != nil {
		return err
	}
	return s.store.SaveTrip(ctx, trip, sha, payload, points)
}

// StaleCount reports how many trips are awaiting recomputation.
func (s *Service) StaleCount(ctx context.Context) (int, error) {
	tmpl, err := s.store.ActiveTemplate(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	ids, err := s.store.StaleTripIDs(ctx, tmpl.ID, tmpl.Version, matcher.AlgoVersion)
	return len(ids), err
}

// SkippedFiles returns the recordings that produced no trip.
func (s *Service) SkippedFiles(ctx context.Context, limit int) ([]domain.ProcessedFile, error) {
	return s.store.SkippedFiles(ctx, limit)
}

// Retry forgets that a file was seen and imports it again.
//
// It exists because a recording skipped for not following the route is still
// recorded as seen, which is what stops it being reconsidered on every refresh.
// That is the right default, but while the route is still being set up a
// recording may have been skipped only because the waypoints were not yet in the
// right place, and it has to be possible to change one's mind.
func (s *Service) Retry(ctx context.Context, filename string) error {
	if err := s.store.Forget(ctx, filename); err != nil {
		return err
	}
	s.Refresh(ctx)
	return nil
}
