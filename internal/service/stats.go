package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/stats"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/storage"
)

// minSamplesForPattern is how many measurements a cell of the hour matrix needs
// before it is treated as showing a pattern rather than an accident. Cells below
// it are reported but marked, so that one unlucky morning is not read as
// congestion.
const minSamplesForPattern = 3

// SegmentStats summarises one stretch of the route.
type SegmentStats struct {
	Seq   int    `json:"seq"`
	Label string `json:"label"`
	// DistanceM is the median measured length of the stretch, which makes the
	// durations of stretches of different length comparable.
	DistanceM float64       `json:"distance_m"`
	Duration  stats.Summary `json:"duration"`
	// CongestionIndex is the median duration divided by the fastest observed
	// one. A value near 1 means the stretch always runs freely; a larger value
	// means it is often slower than it can be.
	CongestionIndex float64 `json:"congestion_index"`
	// MedianSpeedKPH derives from the median duration and distance.
	MedianSpeedKPH float64 `json:"median_speed_kph"`
}

// HourCell is one stretch at one hour of the day.
type HourCell struct {
	Seq   int `json:"seq"`
	Hour  int `json:"hour"`
	Count int `json:"count"`
	// MedianS is the median duration in seconds.
	MedianS float64 `json:"median_s"`
	// RelativeToBest compares this hour with the fastest hour for the same
	// stretch: 1.0 is as good as it gets, 1.4 is forty percent slower.
	RelativeToBest float64 `json:"relative_to_best"`
	// Sparse marks a cell with too few measurements to read as a pattern.
	Sparse bool `json:"sparse"`
}

// WeekPoint is the median duration of one stretch in one calendar week.
type WeekPoint struct {
	Seq     int     `json:"seq"`
	Week    string  `json:"week"`
	MedianS float64 `json:"median_s"`
	Count   int     `json:"count"`
}

// TripPoint is a single measurement, for the scatter plot.
type TripPoint struct {
	Seq       int     `json:"seq"`
	TripID    string  `json:"trip_id"`
	Date      string  `json:"date"`
	Hour      int     `json:"hour"`
	Direction string  `json:"direction"`
	DurationS float64 `json:"duration_s"`
	// Outlier marks a measurement far from the typical one. Such measurements
	// are shown, not discarded: a journey that took twice as long really did
	// take twice as long.
	Outlier bool `json:"outlier"`
}

// Report is everything the statistics page and its charts need.
type Report struct {
	From      string         `json:"from"`
	To        string         `json:"to"`
	Direction string         `json:"direction"`
	Segments  []SegmentStats `json:"segments"`
	Hours     []HourCell     `json:"hours"`
	// HoursPresent lists the hours that actually contain measurements, so the
	// matrix can be drawn without a column for every hour of the day.
	HoursPresent []int       `json:"hours_present"`
	Weeks        []WeekPoint `json:"weeks"`
	Trips        []TripPoint `json:"trips"`
	TotalTrips   int         `json:"total_trips"`
}

// Stats summarises the measurements in a date range.
func (s *Service) Stats(ctx context.Context, from, to string, direction domain.Direction) (Report, error) {
	tmpl, err := s.store.ActiveTemplate(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return Report{From: from, To: to}, nil
	}
	if err != nil {
		return Report{}, err
	}

	segs, err := s.store.Segments(ctx, storage.SegmentQuery{
		TemplateID: tmpl.ID,
		From:       from,
		To:         to,
		Direction:  direction,
	})
	if err != nil {
		return Report{}, err
	}

	rep := Report{From: from, To: to, Direction: string(direction)}
	if len(segs) == 0 {
		return rep, nil
	}

	bySeq := make(map[int][]domain.Segment)
	trips := make(map[string]struct{})
	for _, sg := range segs {
		bySeq[sg.Seq] = append(bySeq[sg.Seq], sg)
		trips[sg.TripID] = struct{}{}
	}
	rep.TotalTrips = len(trips)

	seqs := make([]int, 0, len(bySeq))
	for seq := range bySeq {
		seqs = append(seqs, seq)
	}
	sort.Ints(seqs)

	hoursSeen := make(map[int]struct{})
	for _, seq := range seqs {
		group := bySeq[seq]
		summary := stats.Summarise(durations(group))

		rep.Segments = append(rep.Segments, SegmentStats{
			Seq:             seq,
			Label:           group[0].Label,
			DistanceM:       medianDistance(group),
			Duration:        summary,
			CongestionIndex: ratio(summary.Median, summary.Min),
			MedianSpeedKPH:  speed(medianDistance(group), summary.Median),
		})

		rep.Hours = append(rep.Hours, hourCells(seq, group, hoursSeen)...)
		rep.Weeks = append(rep.Weeks, weekPoints(seq, group, s.loc)...)
		rep.Trips = append(rep.Trips, tripPoints(seq, group, summary)...)
	}

	for h := range hoursSeen {
		rep.HoursPresent = append(rep.HoursPresent, h)
	}
	sort.Ints(rep.HoursPresent)

	return rep, nil
}

func hourCells(seq int, group []domain.Segment, hoursSeen map[int]struct{}) []HourCell {
	byHour := make(map[int][]float64)
	for _, sg := range group {
		if sg.DurationS != nil {
			byHour[sg.LocalHour] = append(byHour[sg.LocalHour], *sg.DurationS)
			hoursSeen[sg.LocalHour] = struct{}{}
		}
	}

	hours := make([]int, 0, len(byHour))
	for h := range byHour {
		hours = append(hours, h)
	}
	sort.Ints(hours)

	// The comparison baseline is the best hour that has enough measurements to
	// be believed, so a single fast run in an otherwise empty hour cannot make
	// every other hour look congested.
	best := 0.0
	for _, h := range hours {
		if len(byHour[h]) < minSamplesForPattern {
			continue
		}
		m := stats.Summarise(byHour[h]).Median
		if best == 0 || m < best {
			best = m
		}
	}

	cells := make([]HourCell, 0, len(hours))
	for _, h := range hours {
		values := byHour[h]
		median := stats.Summarise(values).Median
		cells = append(cells, HourCell{
			Seq:            seq,
			Hour:           h,
			Count:          len(values),
			MedianS:        median,
			RelativeToBest: ratio(median, best),
			Sparse:         len(values) < minSamplesForPattern,
		})
	}
	return cells
}

func weekPoints(seq int, group []domain.Segment, loc *time.Location) []WeekPoint {
	byWeek := make(map[string][]float64)
	for _, sg := range group {
		if sg.DurationS == nil || sg.StartedAt.IsZero() {
			continue
		}
		year, week := sg.StartedAt.In(loc).ISOWeek()
		key := fmt.Sprintf("%d-W%02d", year, week)
		byWeek[key] = append(byWeek[key], *sg.DurationS)
	}

	keys := make([]string, 0, len(byWeek))
	for k := range byWeek {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]WeekPoint, 0, len(keys))
	for _, k := range keys {
		out = append(out, WeekPoint{
			Seq:     seq,
			Week:    k,
			MedianS: stats.Summarise(byWeek[k]).Median,
			Count:   len(byWeek[k]),
		})
	}
	return out
}

func tripPoints(seq int, group []domain.Segment, summary stats.Summary) []TripPoint {
	out := make([]TripPoint, 0, len(group))
	for _, sg := range group {
		if sg.DurationS == nil {
			continue
		}
		out = append(out, TripPoint{
			Seq:       seq,
			TripID:    sg.TripID,
			Date:      sg.LocalDate,
			Hour:      sg.LocalHour,
			Direction: string(sg.Direction),
			DurationS: *sg.DurationS,
			Outlier:   stats.IsOutlier(*sg.DurationS, summary),
		})
	}
	return out
}

func durations(segs []domain.Segment) []float64 {
	out := make([]float64, 0, len(segs))
	for _, sg := range segs {
		if sg.DurationS != nil {
			out = append(out, *sg.DurationS)
		}
	}
	return out
}

func medianDistance(segs []domain.Segment) float64 {
	values := make([]float64, 0, len(segs))
	for _, sg := range segs {
		if sg.DistanceM != nil && *sg.DistanceM > 0 {
			values = append(values, *sg.DistanceM)
		}
	}
	return stats.Summarise(values).Median
}

// ratio guards against a zero denominator, which occurs before any measurement
// has been taken.
func ratio(value, base float64) float64 {
	if base <= 0 {
		return 0
	}
	return value / base
}

func speed(distanceM, durationS float64) float64 {
	if durationS <= 0 {
		return 0
	}
	return distanceM / durationS * 3.6
}
