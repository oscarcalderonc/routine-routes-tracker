package storage

import (
	"context"
	"fmt"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
)

// SegmentQuery selects the measurements to report on.
type SegmentQuery struct {
	TemplateID string
	// From and To are inclusive local dates in YYYY-MM-DD form.
	From string
	To   string
	// Direction restricts to one direction of travel. Empty pools both, which
	// is the default because a stretch of road is the same stretch either way.
	Direction domain.Direction
}

// Segments returns the completed measurements matching a query, ordered by
// stretch and then by date.
//
// Only the rows are fetched; the summarising happens in Go. At this data volume
// there is nothing to gain from aggregating in SQL, and doing it in Go keeps
// percentiles available, which SQLite does not offer.
func (s *Store) Segments(ctx context.Context, q SegmentQuery) ([]domain.Segment, error) {
	sql := `SELECT id, trip_id, template_id, seq, from_waypoint_id, to_waypoint_id, label,
			COALESCE(direction, ''), started_at, ended_at, COALESCE(local_date, ''),
			COALESCE(local_weekday, 0), COALESCE(local_hour, 0),
			duration_s, distance_m, avg_speed_kph, is_complete
		FROM segments
		WHERE template_id = ? AND duration_s IS NOT NULL`

	args := []any{q.TemplateID}
	if q.From != "" {
		sql += ` AND local_date >= ?`
		args = append(args, q.From)
	}
	if q.To != "" {
		sql += ` AND local_date <= ?`
		args = append(args, q.To)
	}
	if q.Direction != "" {
		sql += ` AND direction = ?`
		args = append(args, string(q.Direction))
	}
	sql += ` ORDER BY seq, local_date`

	rows, err := s.db.QueryContext(ctx, sql, args...)
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
