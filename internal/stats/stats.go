// Package stats summarises segment durations.
//
// Aggregation happens here rather than in SQL. A decade of this route yields
// only a few tens of thousands of rows, so sorting a slice costs nothing, and
// keeping it in Go avoids depending on percentile functions that SQLite does
// not provide.
package stats

import (
	"math"
	"slices"
)

// Summary describes a set of measurements in seconds.
type Summary struct {
	Count  int     `json:"count"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P25    float64 `json:"p25"`
	P75    float64 `json:"p75"`
	P90    float64 `json:"p90"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	StdDev float64 `json:"stddev"`
	// MAD is the median absolute deviation, a spread measure that a single
	// exceptional journey cannot distort the way a standard deviation can.
	MAD float64 `json:"mad"`
}

// Summarise computes the summary of a set of values. The input is not modified.
// A nil or empty input yields a zero Summary with Count zero, which callers
// should treat as "no data" rather than as a measurement of zero.
func Summarise(values []float64) Summary {
	if len(values) == 0 {
		return Summary{}
	}

	sorted := slices.Clone(values)
	slices.Sort(sorted)

	s := Summary{
		Count:  len(sorted),
		Median: percentile(sorted, 0.5),
		P25:    percentile(sorted, 0.25),
		P75:    percentile(sorted, 0.75),
		P90:    percentile(sorted, 0.90),
		Min:    sorted[0],
		Max:    sorted[len(sorted)-1],
	}

	var sum float64
	for _, v := range sorted {
		sum += v
	}
	s.Mean = sum / float64(len(sorted))

	var sumSq float64
	for _, v := range sorted {
		d := v - s.Mean
		sumSq += d * d
	}
	s.StdDev = math.Sqrt(sumSq / float64(len(sorted)))

	deviations := make([]float64, len(sorted))
	for i, v := range sorted {
		deviations[i] = math.Abs(v - s.Median)
	}
	slices.Sort(deviations)
	s.MAD = percentile(deviations, 0.5)

	return s
}

// percentile interpolates within an already sorted slice. For the median of an
// even-sized sample this yields the mean of the two central values.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}

	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// IsOutlier reports whether a value sits far enough from the median, measured
// in median absolute deviations, to be worth drawing attention to.
//
// Outliers are flagged rather than discarded: a journey that took twice as long
// because of snow is real data, and dropping it would understate how bad the
// route can get.
func IsOutlier(value float64, s Summary) bool {
	if s.MAD == 0 {
		return false
	}
	// 1.4826 rescales the MAD so that it estimates the standard deviation of a
	// normal distribution, making the threshold comparable to a z-score.
	const scale = 1.4826
	return math.Abs(value-s.Median)/(scale*s.MAD) > 3.5
}
