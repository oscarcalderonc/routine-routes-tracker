package stats

import (
	"math"
	"testing"
)

func TestSummarise_Empty(t *testing.T) {
	if got := Summarise(nil); got.Count != 0 {
		t.Errorf("Count = %d, want 0", got.Count)
	}
}

func TestSummarise_OddAndEvenMedian(t *testing.T) {
	if got := Summarise([]float64{3, 1, 2}); got.Median != 2 {
		t.Errorf("median of an odd sample = %v, want 2", got.Median)
	}
	if got := Summarise([]float64{4, 1, 3, 2}); got.Median != 2.5 {
		t.Errorf("median of an even sample = %v, want 2.5", got.Median)
	}
}

func TestSummarise_DoesNotModifyInput(t *testing.T) {
	in := []float64{5, 1, 3}
	Summarise(in)
	if in[0] != 5 || in[1] != 1 || in[2] != 3 {
		t.Errorf("input was reordered: %v", in)
	}
}

func TestSummarise_Values(t *testing.T) {
	got := Summarise([]float64{10, 20, 30, 40, 50})
	if got.Mean != 30 {
		t.Errorf("Mean = %v, want 30", got.Mean)
	}
	if got.Min != 10 || got.Max != 50 {
		t.Errorf("range = [%v, %v], want [10, 50]", got.Min, got.Max)
	}
	if math.Abs(got.StdDev-14.142) > 0.01 {
		t.Errorf("StdDev = %v, want ~14.142", got.StdDev)
	}
	if got.MAD != 10 {
		t.Errorf("MAD = %v, want 10", got.MAD)
	}
}

func TestIsOutlier(t *testing.T) {
	// A typical commute clustered near 300 s, with one snowbound journey.
	values := []float64{295, 300, 305, 298, 302, 301, 299, 700}
	s := Summarise(values)

	if !IsOutlier(700, s) {
		t.Error("the exceptional journey should be flagged as an outlier")
	}
	if IsOutlier(300, s) {
		t.Error("a typical journey should not be flagged as an outlier")
	}
}

func TestIsOutlier_ZeroSpread(t *testing.T) {
	s := Summarise([]float64{300, 300, 300})
	if IsOutlier(305, s) {
		t.Error("with no spread to measure against, nothing should be called an outlier")
	}
}
