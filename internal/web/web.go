// Package web serves the tracker's interface.
//
// Pages are rendered on the server and refreshed in place with htmx, so there
// is no separate front-end build and the whole tracker ships as one binary.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/service"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/storage"
)

// Server holds the dependencies the handlers need.
type Server struct {
	svc    *service.Service
	render *renderer
	log    *slog.Logger
	// background runs work started by a request but outliving it, such as a
	// refresh, with a context that is not cancelled when the client
	// disconnects.
	background context.Context
}

// NewServer builds the HTTP interface.
func NewServer(ctx context.Context, svc *service.Service, log *slog.Logger) (*Server, error) {
	r, err := newRenderer(svc.Location())
	if err != nil {
		return nil, err
	}
	return &Server{svc: svc, render: r, log: log, background: ctx}, nil
}

// Handler returns the routes the server exposes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("POST /refresh", s.startRefresh)
	mux.HandleFunc("GET /refresh/status", s.refreshStatus)
	mux.HandleFunc("POST /files/{name}/retry", s.retryFile)

	mux.HandleFunc("GET /trips", s.listTrips)
	mux.HandleFunc("GET /trips/{id}", s.showTrip)
	mux.HandleFunc("POST /trips/{id}/reprocess", s.reprocessTrip)
	mux.HandleFunc("POST /trips/{id}/delete", s.deleteTrip)
	mux.HandleFunc("GET /api/trips/{id}/track.json", s.tripTrack)

	mux.HandleFunc("GET /route", s.showRoute)
	mux.HandleFunc("POST /route/waypoints", s.addWaypoint)
	mux.HandleFunc("POST /route/waypoints/{id}", s.updateWaypoint)
	mux.HandleFunc("POST /route/waypoints/{id}/delete", s.deleteWaypoint)
	mux.HandleFunc("POST /route/waypoints/{id}/move", s.moveWaypoint)

	mux.HandleFunc("GET /stats", s.showStats)
	mux.HandleFunc("GET /api/stats.json", s.statsJSON)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	})

	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))

	return logRequests(s.log, mux)
}

// dashboard shows the headline figures and the control to pull new recordings.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	from, to := dateRange(r)
	report, err := s.svc.Stats(ctx, from, to, direction(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	trips, err := s.svc.Store().ListTrips(ctx, storage.TripFilter{Limit: 10})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	counts, err := s.svc.Store().TripCounts(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	stale, err := s.svc.StaleCount(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	lastRefresh, err := s.svc.Store().LastRefresh(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	skipped, err := s.svc.SkippedFiles(ctx, 20)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	route, routeErr := s.svc.Store().ActiveTemplate(ctx)

	s.render.page(w, r, "dashboard.gohtml", map[string]any{
		"Title":           "Dashboard",
		"Nav":             "dashboard",
		"Report":          report,
		"Trips":           trips,
		"Counts":          counts,
		"Stale":           stale,
		"Skipped":         skipped,
		"LastRefresh":     lastRefresh,
		"Route":           route,
		"RouteDefined":    routeErr == nil && len(route.Waypoints) >= 2,
		"DriveConfigured": s.svc.DriveConfigured(),
		"From":            from,
		"To":              to,
		"Progress":        s.svc.Progress(),
		"Location":        s.svc.Location().String(),
	})
}

// startRefresh begins a pull and returns immediately; the page then polls for
// progress. The work uses a background context so that navigating away does not
// abandon a half-finished import.
func (s *Server) startRefresh(w http.ResponseWriter, r *http.Request) {
	go s.svc.Refresh(s.background)

	// Give a fast refresh a moment to finish so the first fragment is usually
	// the final one, which avoids a visible flash of "starting".
	time.Sleep(150 * time.Millisecond)
	s.render.partial(w, "refresh-status", s.progressData())
}

func (s *Server) refreshStatus(w http.ResponseWriter, r *http.Request) {
	s.render.partial(w, "refresh-status", s.progressData())
}

func (s *Server) progressData() map[string]any {
	p := s.svc.Progress()
	return map[string]any{
		"Progress": p,
		// Polling stops once the run is over, so an idle page makes no
		// requests at all.
		"Poll": p.Running,
	}
}

// retryFile reconsiders a recording that produced no trip. It is how a file
// skipped while the route was still being set up gets a second look, which is
// otherwise impossible because the file is recorded as seen.
func (s *Server) retryFile(w http.ResponseWriter, r *http.Request) {
	// The name only ever addresses a file in the inbox, so anything that looks
	// like a path is refused rather than interpreted.
	name := r.PathValue("name")
	if name == "" || name != filepath.Base(name) {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	go func() {
		if err := s.svc.Retry(s.background, name); err != nil {
			s.log.Error("could not reconsider recording", "file", name, "error", err)
		}
	}()
	redirect(w, r, "/")
}

func (s *Server) listTrips(w http.ResponseWriter, r *http.Request) {
	from, to := dateRange(r)
	trips, err := s.svc.Store().ListTrips(r.Context(), storage.TripFilter{
		From:      from,
		To:        to,
		Direction: string(direction(r)),
		Limit:     500,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render.page(w, r, "trips.gohtml", map[string]any{
		"Title":     "Trips",
		"Nav":       "trips",
		"Trips":     trips,
		"From":      from,
		"To":        to,
		"Direction": string(direction(r)),
	})
}

func (s *Server) showTrip(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	trip, err := s.svc.Store().Trip(ctx, r.PathValue("id"))
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	route, _ := s.svc.Store().ActiveTemplate(ctx)

	s.render.page(w, r, "trip.gohtml", map[string]any{
		"Title": "Trip " + trip.LocalDate,
		"Nav":   "trips",
		"Trip":  trip,
		"Route": route,
	})
}

func (s *Server) tripTrack(w http.ResponseWriter, r *http.Request) {
	payload, err := s.svc.Store().TrackPayload(r.Context(), r.PathValue("id"))
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The payload is stored gzipped, which is also how it travels.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.Write(payload)
}

func (s *Server) reprocessTrip(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.ReprocessTrip(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/trips/"+r.PathValue("id"))
}

func (s *Server) deleteTrip(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Store().DeleteTrip(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/trips")
}

func (s *Server) showStats(w http.ResponseWriter, r *http.Request) {
	from, to := dateRange(r)
	report, err := s.svc.Stats(r.Context(), from, to, direction(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render.page(w, r, "stats.gohtml", map[string]any{
		"Title":     "Statistics",
		"Nav":       "stats",
		"Report":    report,
		"From":      from,
		"To":        to,
		"Direction": string(direction(r)),
	})
}

func (s *Server) statsJSON(w http.ResponseWriter, r *http.Request) {
	from, to := dateRange(r)
	report, err := s.svc.Stats(r.Context(), from, to, direction(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, report)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent by this point, so the only useful
		// action is to stop writing.
		return
	}
}

// dateRange reads the reporting window from the query, defaulting to the last
// ninety days.
func dateRange(r *http.Request) (from, to string) {
	from = r.URL.Query().Get("from")
	to = r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().AddDate(0, 0, -90).Format(time.DateOnly)
	}
	if to == "" {
		to = time.Now().AddDate(0, 0, 1).Format(time.DateOnly)
	}
	return from, to
}

func direction(r *http.Request) domain.Direction {
	switch strings.ToLower(r.URL.Query().Get("direction")) {
	case string(domain.DirectionForward):
		return domain.DirectionForward
	case string(domain.DirectionReverse):
		return domain.DirectionReverse
	default:
		return ""
	}
}

// redirect sends the browser to a page, using htmx's header when the request
// came from htmx so that the address bar keeps up.
func redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "error", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// logRequests records each request once it has been served.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path == "/refresh/status" || strings.HasPrefix(r.URL.Path, "/static/") {
			// Polling and asset requests would drown out everything else.
			return
		}
		log.Info("request", "method", r.Method, "path", r.URL.Path,
			"duration", time.Since(start).Round(time.Millisecond))
	})
}
