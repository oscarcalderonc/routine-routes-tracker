package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
)

// ErrNotFound reports that no row matched.
var ErrNotFound = errors.New("storage: not found")

// ActiveTemplate returns the active route template with its waypoints in
// canonical order. It reports ErrNotFound when no route has been defined.
func (s *Store) ActiveTemplate(ctx context.Context) (domain.Template, error) {
	const q = `SELECT id, name, active, version, created_at, updated_at
		FROM route_templates WHERE active = 1 ORDER BY created_at LIMIT 1`

	var t domain.Template
	var created, updated string
	err := s.db.QueryRowContext(ctx, q).Scan(&t.ID, &t.Name, &t.Active, &t.Version, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Template{}, ErrNotFound
	}
	if err != nil {
		return domain.Template{}, fmt.Errorf("query active template: %w", err)
	}
	t.CreatedAt, t.UpdatedAt = parseTime(created), parseTime(updated)

	t.Waypoints, err = s.waypoints(ctx, t.ID)
	if err != nil {
		return domain.Template{}, err
	}
	return t, nil
}

func (s *Store) waypoints(ctx context.Context, templateID string) ([]domain.Waypoint, error) {
	const q = `SELECT id, template_id, seq, label, lat, lon, radius_m, optional, created_at, updated_at
		FROM waypoints WHERE template_id = ? ORDER BY seq`

	rows, err := s.db.QueryContext(ctx, q, templateID)
	if err != nil {
		return nil, fmt.Errorf("query waypoints: %w", err)
	}
	defer rows.Close()

	var out []domain.Waypoint
	for rows.Next() {
		var w domain.Waypoint
		var created, updated string
		if err := rows.Scan(&w.ID, &w.TemplateID, &w.Seq, &w.Label, &w.Lat, &w.Lon,
			&w.RadiusM, &w.Optional, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan waypoint: %w", err)
		}
		w.CreatedAt, w.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, w)
	}
	return out, rows.Err()
}

// EnsureActiveTemplate returns the active route, creating it if there is none.
//
// Only one route may be active, which the database enforces. Two callers can
// therefore reach the creation step together and one will lose; losing means the
// other has already created the route, so the answer is simply to read it back.
func (s *Store) EnsureActiveTemplate(ctx context.Context, name string) (domain.Template, error) {
	t, err := s.ActiveTemplate(ctx)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return t, err
	}

	t, err = s.CreateTemplate(ctx, name)
	if err == nil {
		return t, nil
	}
	// Another request created the route first. Its version is the one to use.
	if created, readErr := s.ActiveTemplate(ctx); readErr == nil {
		return created, nil
	}
	return domain.Template{}, err
}

// CreateTemplate stores a new route template. It is used to seed the single
// route on first run.
func (s *Store) CreateTemplate(ctx context.Context, name string) (domain.Template, error) {
	now := time.Now().UTC()
	t := domain.Template{
		ID:        domain.NewID(),
		Name:      name,
		Active:    true,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	const q = `INSERT INTO route_templates (id, name, active, version, created_at, updated_at)
		VALUES (?, ?, 1, 1, ?, ?)`
	if _, err := s.db.ExecContext(ctx, q, t.ID, t.Name, formatTime(now), formatTime(now)); err != nil {
		return domain.Template{}, fmt.Errorf("insert template: %w", err)
	}
	return t, nil
}

// AddWaypoint appends a waypoint to the end of a route and bumps the template
// version so that existing trips are recomputed against the new geometry.
func (s *Store) AddWaypoint(ctx context.Context, templateID string, w domain.Waypoint) (domain.Waypoint, error) {
	now := time.Now().UTC()
	w.ID = domain.NewID()
	w.TemplateID = templateID
	w.CreatedAt, w.UpdatedAt = now, now

	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var next sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(seq) FROM waypoints WHERE template_id = ?`, templateID).Scan(&next); err != nil {
			return fmt.Errorf("find last waypoint: %w", err)
		}
		w.Seq = 0
		if next.Valid {
			w.Seq = int(next.Int64) + 1
		}

		const q = `INSERT INTO waypoints
			(id, template_id, seq, label, lat, lon, radius_m, optional, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, w.ID, w.TemplateID, w.Seq, w.Label, w.Lat, w.Lon,
			w.RadiusM, w.Optional, formatTime(now), formatTime(now)); err != nil {
			return fmt.Errorf("insert waypoint: %w", err)
		}
		return bumpVersion(ctx, tx, templateID, now)
	})
	if err != nil {
		return domain.Waypoint{}, err
	}
	return w, nil
}

// UpdateWaypoint changes a waypoint's label, position, radius or optionality.
// The waypoint identity is preserved so that historical crossings remain
// attached to it, and the template version is bumped to trigger recomputation.
func (s *Store) UpdateWaypoint(ctx context.Context, w domain.Waypoint) error {
	now := time.Now().UTC()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		const q = `UPDATE waypoints
			SET label = ?, lat = ?, lon = ?, radius_m = ?, optional = ?, updated_at = ?
			WHERE id = ?`
		res, err := tx.ExecContext(ctx, q, w.Label, w.Lat, w.Lon, w.RadiusM, w.Optional,
			formatTime(now), w.ID)
		if err != nil {
			return fmt.Errorf("update waypoint: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return bumpVersion(ctx, tx, w.TemplateID, now)
	})
}

// DeleteWaypoint removes a waypoint and closes the gap in the sequence.
func (s *Store) DeleteWaypoint(ctx context.Context, templateID, waypointID string) error {
	now := time.Now().UTC()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var seq int
		err := tx.QueryRowContext(ctx,
			`SELECT seq FROM waypoints WHERE id = ? AND template_id = ?`,
			waypointID, templateID).Scan(&seq)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find waypoint: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM waypoints WHERE id = ?`, waypointID); err != nil {
			return fmt.Errorf("delete waypoint: %w", err)
		}
		// Sequence numbers are unique per template, so the gap must be closed
		// in ascending order to avoid colliding with an existing row.
		if _, err := tx.ExecContext(ctx,
			`UPDATE waypoints SET seq = seq - 1 WHERE template_id = ? AND seq > ?`,
			templateID, seq); err != nil {
			return fmt.Errorf("renumber waypoints: %w", err)
		}
		return bumpVersion(ctx, tx, templateID, now)
	})
}

// MoveWaypoint shifts a waypoint one place earlier or later in the route.
func (s *Store) MoveWaypoint(ctx context.Context, templateID, waypointID string, delta int) error {
	if delta != 1 && delta != -1 {
		return fmt.Errorf("storage: move delta must be 1 or -1, got %d", delta)
	}
	now := time.Now().UTC()

	return s.inTx(ctx, func(tx *sql.Tx) error {
		var seq int
		err := tx.QueryRowContext(ctx,
			`SELECT seq FROM waypoints WHERE id = ? AND template_id = ?`,
			waypointID, templateID).Scan(&seq)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find waypoint: %w", err)
		}

		var otherID string
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM waypoints WHERE template_id = ? AND seq = ?`,
			templateID, seq+delta).Scan(&otherID)
		if errors.Is(err, sql.ErrNoRows) {
			// Already at one end of the route; nothing to do.
			return nil
		}
		if err != nil {
			return fmt.Errorf("find neighbouring waypoint: %w", err)
		}

		// SQLite cannot defer a uniqueness check to the end of a statement, so
		// the swap goes via a sequence number no row can hold.
		const park = -1
		if _, err := tx.ExecContext(ctx,
			`UPDATE waypoints SET seq = ? WHERE id = ?`, park, waypointID); err != nil {
			return fmt.Errorf("park waypoint: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE waypoints SET seq = ? WHERE id = ?`, seq, otherID); err != nil {
			return fmt.Errorf("move neighbour: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE waypoints SET seq = ? WHERE id = ?`, seq+delta, waypointID); err != nil {
			return fmt.Errorf("place waypoint: %w", err)
		}
		return bumpVersion(ctx, tx, templateID, now)
	})
}

// bumpVersion records that a route's geometry changed. Every trip carries the
// version it was measured against, so raising it is what marks existing trips
// for recomputation.
func bumpVersion(ctx context.Context, tx *sql.Tx, templateID string, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE route_templates SET version = version + 1, updated_at = ? WHERE id = ?`,
		formatTime(now), templateID)
	if err != nil {
		return fmt.Errorf("bump template version: %w", err)
	}
	return nil
}
