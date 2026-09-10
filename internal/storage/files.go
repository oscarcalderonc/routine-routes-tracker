package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
)

// IsProcessed reports whether a source file has already been ingested.
// Deduplication is by filename because the recorder names each file after the
// UTC instant the journey began, which identifies it uniquely.
func (s *Store) IsProcessed(ctx context.Context, filename string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM processed_files WHERE filename = ?`, filename).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check processed file: %w", err)
	}
	return n > 0, nil
}

// ProcessedNames returns the set of filenames already ingested, for filtering a
// directory listing in one query rather than one per file.
func (s *Store) ProcessedNames(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT filename FROM processed_files`)
	if err != nil {
		return nil, fmt.Errorf("list processed files: %w", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan filename: %w", err)
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// MarkProcessed records the outcome of ingesting a file. Failures are recorded
// as well as successes so that an unreadable file is not retried on every
// refresh; the interface offers an explicit retry instead.
func (s *Store) MarkProcessed(ctx context.Context, f domain.ProcessedFile) error {
	const q = `INSERT INTO processed_files
		(filename, trip_id, sha256, file_time_utc, processed_at, status, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(filename) DO UPDATE SET
			trip_id = excluded.trip_id,
			sha256 = excluded.sha256,
			file_time_utc = excluded.file_time_utc,
			processed_at = excluded.processed_at,
			status = excluded.status,
			error_message = excluded.error_message`

	_, err := s.db.ExecContext(ctx, q, f.Filename, nullString(f.TripID), nullString(f.SHA256),
		nullTime(f.FileTimeUTC), formatTime(f.ProcessedAt), f.Status, f.ErrorMessage)
	if err != nil {
		return fmt.Errorf("mark file processed: %w", err)
	}
	return nil
}

// Forget removes the record that a file was processed, so that the next refresh
// imports it again.
func (s *Store) Forget(ctx context.Context, filename string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM processed_files WHERE filename = ?`, filename); err != nil {
		return fmt.Errorf("forget file: %w", err)
	}
	return nil
}

// SkippedFiles returns files that produced no trip, most recent first, whether
// because they could not be read or because they did not follow the route.
//
// They are listed so that a recording skipped while the route was still being
// set up can be reconsidered afterwards, rather than being invisible because it
// is recorded as seen.
func (s *Store) SkippedFiles(ctx context.Context, limit int) ([]domain.ProcessedFile, error) {
	const q = `SELECT filename, COALESCE(trip_id, ''), COALESCE(sha256, ''), file_time_utc,
			processed_at, status, COALESCE(error_message, '')
		FROM processed_files WHERE status <> ? ORDER BY processed_at DESC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, q, domain.FileStatusOK, limit)
	if err != nil {
		return nil, fmt.Errorf("query skipped files: %w", err)
	}
	defer rows.Close()

	var out []domain.ProcessedFile
	for rows.Next() {
		var f domain.ProcessedFile
		var fileTime sql.NullString
		var processed string
		if err := rows.Scan(&f.Filename, &f.TripID, &f.SHA256, &fileTime, &processed,
			&f.Status, &f.ErrorMessage); err != nil {
			return nil, fmt.Errorf("scan processed file: %w", err)
		}
		f.FileTimeUTC = timeFrom(fileTime)
		f.ProcessedAt = parseTime(processed)
		out = append(out, f)
	}
	return out, rows.Err()
}

// LastRefresh reports when a file was most recently ingested.
func (s *Store) LastRefresh(ctx context.Context) (time.Time, error) {
	var at sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT MAX(processed_at) FROM processed_files`).Scan(&at)
	if err != nil {
		return time.Time{}, fmt.Errorf("query last refresh: %w", err)
	}
	return timeFrom(at), nil
}
