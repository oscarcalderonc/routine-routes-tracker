package web

import (
	"errors"
	"net/http"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/storage"
)

// defaultRadiusM is the tolerance offered for a new waypoint. It is wide enough
// to absorb ordinary GPS error near buildings without being so wide that two
// waypoints on the same street become ambiguous.
const defaultRadiusM = 25

// staleRoute sends the browser back to a freshly loaded route page.
//
// It answers an action naming a waypoint that no longer exists, which is what a
// page left open in a tab produces after the waypoint has been removed
// elsewhere. Reporting the missing row as a server error told the reader nothing
// and left the out-of-date page on screen; reloading it both explains the
// situation and fixes it.
func (s *Server) staleRoute(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, "/route?stale=1")
}

func (s *Server) showRoute(w http.ResponseWriter, r *http.Request) {
	route, err := s.svc.Store().ActiveTemplate(r.Context())
	if errors.Is(err, storage.ErrNotFound) {
		route = domain.Template{Name: "No route defined"}
	} else if err != nil {
		s.fail(w, r, err)
		return
	}

	trips, err := s.svc.Store().ListTrips(r.Context(), storage.TripFilter{Limit: 20})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render.page(w, r, "route.gohtml", map[string]any{
		"Title": "Route",
		"Nav":   "route",
		"Route": route,
		// Set when the page was reloaded because an action referred to a
		// waypoint that had already gone.
		"Stale": r.URL.Query().Get("stale") != "",
		// Recent trips are offered as a backdrop for the editor: placing a
		// waypoint on a road you actually drove is far more reliable than
		// judging it from map tiles alone.
		"Trips":         trips,
		"DefaultRadius": defaultRadiusM,
	})
}

func (s *Server) addWaypoint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	// The first waypoint implies the route, so it is created on demand rather
	// than asking for a separate setup step.
	route, err := s.svc.Store().EnsureActiveTemplate(ctx, "Daily route")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	wp, err := waypointFromForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.svc.Store().AddWaypoint(ctx, route.ID, wp); err != nil {
		s.fail(w, r, err)
		return
	}
	s.afterRouteChange(w, r)
}

func (s *Server) updateWaypoint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	route, err := s.svc.Store().ActiveTemplate(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		s.staleRoute(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	wp, err := waypointFromForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	wp.ID = r.PathValue("id")
	wp.TemplateID = route.ID

	if err := s.svc.Store().UpdateWaypoint(ctx, wp); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.staleRoute(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.afterRouteChange(w, r)
}

func (s *Server) deleteWaypoint(w http.ResponseWriter, r *http.Request) {
	route, err := s.svc.Store().ActiveTemplate(r.Context())
	if errors.Is(err, storage.ErrNotFound) {
		s.staleRoute(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.Store().DeleteWaypoint(r.Context(), route.ID, r.PathValue("id")); err != nil {
		// Already gone is the outcome that was asked for, so it is not an error.
		if errors.Is(err, storage.ErrNotFound) {
			s.staleRoute(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.afterRouteChange(w, r)
}

func (s *Server) moveWaypoint(w http.ResponseWriter, r *http.Request) {
	delta := -1
	if r.URL.Query().Get("dir") == "down" {
		delta = 1
	}
	route, err := s.svc.Store().ActiveTemplate(r.Context())
	if errors.Is(err, storage.ErrNotFound) {
		s.staleRoute(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.Store().MoveWaypoint(r.Context(), route.ID, r.PathValue("id"), delta); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.staleRoute(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.afterRouteChange(w, r)
}

// afterRouteChange recomputes the affected trips and returns to the editor.
//
// Recomputation runs in the background because the caller should not wait on
// it, and it is safe to interrupt: the set of stale trips is derived from
// stored state, so whatever is missed is picked up next time.
func (s *Server) afterRouteChange(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := s.svc.Reprocess(s.background); err != nil {
			s.log.Error("recompute after route change failed", "error", err)
		}
	}()
	redirect(w, r, "/route")
}

func waypointFromForm(r *http.Request) (domain.Waypoint, error) {
	lat, err := parseFloat(r.FormValue("lat"))
	if err != nil {
		return domain.Waypoint{}, errors.New("latitude must be a number")
	}
	lon, err := parseFloat(r.FormValue("lon"))
	if err != nil {
		return domain.Waypoint{}, errors.New("longitude must be a number")
	}
	radius, err := parseFloat(r.FormValue("radius_m"))
	if err != nil || radius <= 0 {
		return domain.Waypoint{}, errors.New("radius must be a positive number")
	}

	label := r.FormValue("label")
	if label == "" {
		return domain.Waypoint{}, errors.New("label is required")
	}

	return domain.Waypoint{
		Label:    label,
		Lat:      lat,
		Lon:      lon,
		RadiusM:  radius,
		Optional: r.FormValue("optional") != "",
	}, nil
}
