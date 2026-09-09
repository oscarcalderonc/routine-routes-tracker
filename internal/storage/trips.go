package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
)

// SaveTrip writes a trip together with everything derived from it, replacing
// any previous derivation. Crossings and segments are rebuilt wholesale rather
// than amended, because inserting or removing a waypoint renumbers them and
// patching that in place is an easy way to corrupt old measurements.
func (s *Store) SaveTrip(ctx context.Context, t domain.Trip, sha string, track []byte, trackPoints int) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		const upsertTrip = `INSERT INTO trips (
			id, template_id, template_version, algo_version, source_filename, content_sha256,
			started_at, ended_at, local_date, local_weekday, local_hour, direction,
			point_count, kept_point_count, duration_s, distance_m,
			min_lat, min_lon, max_lat, max_lon,
			status, matched_waypoints, match_score, error_message, processed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			template_id = excluded.template_id,
			template_version = excluded.template_version,
			algo_version = excluded.algo_version,
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			local_date = excluded.local_date,
			local_weekday = excluded.local_weekday,
			local_hour = excluded.local_hour,
			direction = excluded.direction,
			point_count = excluded.point_count,
			kept_point_count = excluded.kept_point_count,
			duration_s = excluded.duration_s,
			distance_m = excluded.distance_m,
			min_lat = excluded.min_lat, min_lon = excluded.min_lon,
			max_lat = excluded.max_lat, max_lon = excluded.max_lon,
			status = excluded.status,
			matched_waypoints = excluded.matched_waypoints,
			match_score = excluded.match_score,
			error_message = excluded.error_message,
			processed_at = excluded.processed_at`

		_, err := tx.ExecContext(ctx, upsertTrip,
			t.ID, nullString(t.TemplateID), t.TemplateVersion, t.AlgoVersion, t.SourceFilename, sha,
			nullTime(t.StartedAt), nullTime(t.EndedAt), t.LocalDate, t.LocalWeekday, t.LocalHour,
			string(t.Direction), t.PointCount, t.KeptPointCount, t.DurationS, t.DistanceM,
			t.MinLat, t.MinLon, t.MaxLat, t.MaxLon,
			string(t.Status), t.MatchedWaypoints, t.MatchScore, t.ErrorMessage,
			formatTime(t.ProcessedAt))
		if err != nil {
			return fmt.Errorf("save trip: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM crossings WHERE trip_id = ?`, t.ID); err != nil {
			return fmt.Errorf("clear crossings: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM segments WHERE trip_id = ?`, t.ID); err != nil {
			return fmt.Errorf("clear segments: %w", err)
		}

		const insertCrossing = `INSERT INTO crossings (
			id, trip_id, waypoint_id, waypoint_seq, crossed_at, offset_s,
			exit_at, closest_at, closest_distance_m, method, enter_index
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		for _, c := range t.Crossings {
			if _, err := tx.ExecContext(ctx, insertCrossing,
				domain.NewID(), t.ID, c.WaypointID, c.WaypointSeq, formatTime(c.CrossedAt), c.OffsetS,
				nullTime(c.ExitAt), nullTime(c.ClosestAt), c.ClosestDistanceM,
				string(c.Method), c.EnterIndex); err != nil {
				return fmt.Errorf("insert crossing: %w", err)
			}
		}

		const insertSegment = `INSERT INTO segments (
			id, trip_id, template_id, seq, from_waypoint_id, to_waypoint_id, label, direction,
			started_at, ended_at, local_date, local_weekday, local_hour,
			duration_s, distance_m, avg_speed_kph, is_complete
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		for _, sg := range t.Segments {
			if _, err := tx.ExecContext(ctx, insertSegment,
				domain.NewID(), t.ID, sg.TemplateID, sg.Seq, sg.FromWaypointID, sg.ToWaypointID,
				sg.Label, string(sg.Direction), nullTime(sg.StartedAt), nullTime(sg.EndedAt),
				nullString(sg.LocalDate), sg.LocalWeekday, sg.LocalHour,
				nullFloat(sg.DurationS), nullFloat(sg.DistanceM), nullFloat(sg.AvgSpeedKPH),
				sg.IsComplete); err != nil {
				return fmt.Errorf("insert segment: %w", err)
			}
		}

		if track != nil {
			const upsertTrack = `INSERT INTO trip_tracks (trip_id, encoding, point_count, payload)
				VALUES (?, 'json+gzip', ?, ?)
				ON CONFLICT(trip_id) DO UPDATE SET
					point_count = excluded.point_count, payload = excluded.payload`
			if _, err := tx.ExecContext(ctx, upsertTrack, t.ID, trackPoints, track); err != nil {
				return fmt.Errorf("save track: %w", err)
			}
		}
		return nil
	})
}

// TripFilter narrows a trip listing.
type TripFilter struct {
	From      string
	To        string
	Direction string
	Limit     int
}

// ListTrips returns trips most recent first, without their crossings or
// segments.
func (s *Store) ListTrips(ctx context.Context, f TripFilter) ([]domain.Trip, error) {
	q := `SELECT id, COALESCE(template_id, ''), COALESCE(template_version, 0), COALESCE(algo_version, 0),
			source_filename, started_at, ended_at, COALESCE(local_date, ''),
			COALESCE(local_weekday, 0), COALESCE(local_hour, 0), COALESCE(direction, ''),
			point_count, kept_point_count, COALESCE(duration_s, 0), COALESCE(distance_m, 0),
			COALESCE(min_lat, 0), COALESCE(min_lon, 0), COALESCE(max_lat, 0), COALESCE(max_lon, 0),
			status, matched_waypoints, COALESCE(match_score, 0), COALESCE(error_message, ''),
			processed_at
		FROM trips WHERE 1 = 1`

	var args []any
	if f.From != "" {
		q += ` AND local_date >= ?`
		args = append(args, f.From)
	}
	if f.To != "" {
		q += ` AND local_date <= ?`
		args = append(args, f.To)
	}
	if f.Direction != "" {
		q += ` AND direction = ?`
		args = append(args, f.Direction)
	}
	q += ` ORDER BY COALESCE(started_at, processed_at) DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query trips: %w", err)
	}
	defer rows.Close()

	var out []domain.Trip
	for rows.Next() {
		t, err := scanTrip(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Trip returns one trip with its crossings and segments.
func (s *Store) Trip(ctx context.Context, id string) (domain.Trip, error) {
	const q = `SELECT id, COALESCE(template_id, ''), COALESCE(template_version, 0), COALESCE(algo_version, 0),
			source_filename, started_at, ended_at, COALESCE(local_date, ''),
			COALESCE(local_weekday, 0), COALESCE(local_hour, 0), COALESCE(direction, ''),
			point_count, kept_point_count, COALESCE(duration_s, 0), COALESCE(distance_m, 0),
			COALESCE(min_lat, 0), COALESCE(min_lon, 0), COALESCE(max_lat, 0), COALESCE(max_lon, 0),
			status, matched_waypoints, COALESCE(match_score, 0), COALESCE(error_message, ''),
			processed_at
		FROM trips WHERE id = ?`

	row := s.db.QueryRowContext(ctx, q, id)
	t, err := scanTrip(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Trip{}, ErrNotFound
	}
	if err != nil {
		return domain.Trip{}, err
	}

	if t.Crossings, err = s.crossings(ctx, id); err != nil {
		return domain.Trip{}, err
	}
	if t.Segments, err = s.tripSegments(ctx, id); err != nil {
		return domain.Trip{}, err
	}
	return t, nil
}

// scanner covers both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanTrip(sc scanner) (domain.Trip, error) {
	var t domain.Trip
	var started, ended sql.NullString
	var processed string
	var direction, status string

	err := sc.Scan(&t.ID, &t.TemplateID, &t.TemplateVersion, &t.AlgoVersion, &t.SourceFilename,
		&started, &ended, &t.LocalDate, &t.LocalWeekday, &t.LocalHour, &direction,
		&t.PointCount, &t.KeptPointCount, &t.DurationS, &t.DistanceM,
		&t.MinLat, &t.MinLon, &t.MaxLat, &t.MaxLon,
		&status, &t.MatchedWaypoints, &t.MatchScore, &t.ErrorMessage, &processed)
	if err != nil {
		return domain.Trip{}, err
	}

	t.StartedAt, t.EndedAt = timeFrom(started), timeFrom(ended)
	t.ProcessedAt = parseTime(processed)
	t.Direction = domain.Direction(direction)
	t.Status = domain.TripStatus(status)
	return t, nil
}

func (s *Store) crossings(ctx context.Context, tripID string) ([]domain.Crossing, error) {
	const q = `SELECT id, trip_id, waypoint_id, waypoint_seq, crossed_at, offset_s,
			exit_at, closest_at, COALESCE(closest_distance_m, 0), COALESCE(method, ''),
			COALESCE(enter_index, 0)
		FROM crossings WHERE trip_id = ? ORDER BY waypoint_seq`

	rows, err := s.db.QueryContext(ctx, q, tripID)
	if err != nil {
		return nil, fmt.Errorf("query crossings: %w", err)
	}
	defer rows.Close()

	var out []domain.Crossing
	for rows.Next() {
		var c domain.Crossing
		var crossed string
		var exit, closest sql.NullString
		var method string
		if err := rows.Scan(&c.ID, &c.TripID, &c.WaypointID, &c.WaypointSeq, &crossed, &c.OffsetS,
			&exit, &closest, &c.ClosestDistanceM, &method, &c.EnterIndex); err != nil {
			return nil, fmt.Errorf("scan crossing: %w", err)
		}
		c.CrossedAt = parseTime(crossed)
		c.ExitAt, c.ClosestAt = timeFrom(exit), timeFrom(closest)
		c.Method = domain.CrossingMethod(method)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) tripSegments(ctx context.Context, tripID string) ([]domain.Segment, error) {
	const q = `SELECT id, trip_id, template_id, seq, from_waypoint_id, to_waypoint_id, label,
			COALESCE(direction, ''), started_at, ended_at, COALESCE(local_date, ''),
			COALESCE(local_weekday, 0), COALESCE(local_hour, 0),
			duration_s, distance_m, avg_speed_kph, is_complete
		FROM segments WHERE trip_id = ? ORDER BY seq`

	rows, err := s.db.QueryContext(ctx, q, tripID)
	if err != nil {
		return nil, fmt.Errorf("query segments: %w", err)
	}
	defer rows.Close()

	var out []domain.Segment
	for rows.Next() {
		sg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

func scanSegment(sc scanner) (domain.Segment, error) {
	var sg domain.Segment
	var direction string
	var started, ended sql.NullString
	var dur, dist, speed sql.NullFloat64

	if err := sc.Scan(&sg.ID, &sg.TripID, &sg.TemplateID, &sg.Seq, &sg.FromWaypointID,
		&sg.ToWaypointID, &sg.Label, &direction, &started, &ended, &sg.LocalDate,
		&sg.LocalWeekday, &sg.LocalHour, &dur, &dist, &speed, &sg.IsComplete); err != nil {
		return domain.Segment{}, fmt.Errorf("scan segment: %w", err)
	}
	sg.Direction = domain.Direction(direction)
	sg.StartedAt, sg.EndedAt = timeFrom(started), timeFrom(ended)
	sg.DurationS, sg.DistanceM, sg.AvgSpeedKPH = floatPtr(dur), floatPtr(dist), floatPtr(speed)
	return sg, nil
}

// TrackPayload returns the simplified path stored for a trip.
func (s *Store) TrackPayload(ctx context.Context, tripID string) ([]byte, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM trip_tracks WHERE trip_id = ?`, tripID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query track: %w", err)
	}
	return payload, nil
}

// DeleteTrip removes a trip and everything derived from it. The record that its
// source file was processed is kept, so a deleted trip is not re-imported on
// the next refresh.
func (s *Store) DeleteTrip(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM trips WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete trip: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// StaleTripIDs returns trips that were measured against an older route version
// or an older matching algorithm and therefore need recomputing.
//
// This query is the work queue. Because it derives entirely from stored state
// it needs no bookkeeping of its own and is safe to re-run after a crash.
func (s *Store) StaleTripIDs(ctx context.Context, templateID string, version, algoVersion int) ([]string, error) {
	const q = `SELECT id FROM trips
		WHERE template_id = ? AND status <> ?
		  AND (template_version <> ? OR algo_version <> ?)
		ORDER BY started_at`

	rows, err := s.db.QueryContext(ctx, q, templateID, string(domain.StatusError), version, algoVersion)
	if err != nil {
		return nil, fmt.Errorf("query stale trips: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan trip id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// TripCounts summarises how many trips hold each status. The keys are plain
// strings so that templates can look a status up directly.
func (s *Store) TripCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM trips GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count trips: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("scan trip count: %w", err)
		}
		out[status] = n
	}
	return out, rows.Err()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TripSourceSHA returns the content hash of a trip's retained source file,
// which is how the file is located when a trip is recomputed.
func (s *Store) TripSourceSHA(ctx context.Context, tripID string) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx,
		`SELECT content_sha256 FROM trips WHERE id = ?`, tripID).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query trip source: %w", err)
	}
	return sha, nil
}
