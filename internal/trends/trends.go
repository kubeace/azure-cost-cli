// Package trends contains pure analytics over per-day cost series: rolling
// averages, spike anomaly detection, before/after diff math.
//
// No I/O, no Azure SDK, no time.Now (callers supply all times). This keeps
// the package fully unit-testable and decoupled from the Cost Management
// query layer — the same analytics could in principle run over AWS CUR data
// if we ever multi-cloud.
//
// Detector philosophy (see DetectAnomalies):
//
//   - Flags SUDDEN spikes, not gradual climbs. Each series's latest day is
//     compared against the rolling baseline of the prior min_days. Climbs
//     where each day is just barely above its baseline are intentionally
//     not flagged — those are the domain of Diff.
//
//   - The pivot day is excluded from the baseline. Without this, a 5x spike
//     pulls its own baseline up by 50% and registers as ~3.3x, masking the
//     magnitude.
//
//   - min_abs suppresses tiny services (₹500/day default) that swing wildly
//     off rounding noise.
//
//   - Series with less than min_days+1 history points are skipped (no
//     baseline to compare against). Alternative would be to treat them as
//     ratio=inf, but that fires on every new deploy.
package trends

import (
	"math"
	"sort"
	"time"
)

// DailySeries is an ordered (by Date) per-day cost series for one label.
// Date is normalized to midnight UTC.
type DailySeries struct {
	Label string
	Days  []DayPoint
}

type DayPoint struct {
	Date time.Time
	INR  float64
}

// Sorted returns Days sorted ascending by Date.
func (s DailySeries) Sorted() []DayPoint {
	out := make([]DayPoint, len(s.Days))
	copy(out, s.Days)
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// RollingAvg returns the average of the last n points strictly before pivot.
// Used to compute "what was the typical day before today" — pivot itself is
// excluded so today's value can be compared against history.
func RollingAvg(s DailySeries, pivot time.Time, n int) float64 {
	if n <= 0 {
		return 0
	}
	pts := s.Sorted()
	var vals []float64
	for i := len(pts) - 1; i >= 0; i-- {
		if !pts[i].Date.Before(pivot) {
			continue
		}
		vals = append(vals, pts[i].INR)
		if len(vals) >= n {
			break
		}
	}
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// Anomaly describes one flagged series: its latest value, its baseline
// (rolling avg), and the ratio that triggered the flag.
type Anomaly struct {
	Label    string
	Latest   time.Time
	LatestV  float64
	Baseline float64
	Ratio    float64 // LatestV / Baseline
}

// DetectAnomalies returns anomalies sorted by Ratio descending. The detector
// flags a series when:
//   - latest day value > minAbs (suppresses noisy tiny services), AND
//   - latest day value > ratioThreshold * rolling 7-day baseline.
//
// Series with fewer than minHistoryDays prior points are skipped to avoid
// false positives on newly-onboarded resources.
func DetectAnomalies(series []DailySeries, ratioThreshold, minAbs float64, minHistoryDays int) []Anomaly {
	var out []Anomaly
	for _, s := range series {
		pts := s.Sorted()
		if len(pts) < minHistoryDays+1 {
			continue
		}
		latest := pts[len(pts)-1]
		if latest.INR < minAbs {
			continue
		}
		baseline := RollingAvg(s, latest.Date, minHistoryDays)
		if baseline <= 0 {
			continue
		}
		ratio := latest.INR / baseline
		if ratio >= ratioThreshold {
			out = append(out, Anomaly{
				Label: s.Label, Latest: latest.Date,
				LatestV: latest.INR, Baseline: baseline, Ratio: ratio,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ratio > out[j].Ratio })
	return out
}

// Mover is a single label's cost delta between two windows.
type Mover struct {
	Label     string
	Before    float64
	After     float64
	Delta     float64 // After - Before
	PctChange float64 // (After - Before) / Before * 100, NaN when Before == 0
}

// Diff returns Movers sorted by absolute Delta descending. Labels present in
// only one map are kept (PctChange = +Inf when Before == 0 and After > 0).
func Diff(before, after map[string]float64) []Mover {
	keys := map[string]struct{}{}
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}
	out := make([]Mover, 0, len(keys))
	for k := range keys {
		b, a := before[k], after[k]
		m := Mover{Label: k, Before: b, After: a, Delta: a - b}
		if b > 0 {
			m.PctChange = (a - b) / b * 100
		} else if a > 0 {
			m.PctChange = math.Inf(1)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return math.Abs(out[i].Delta) > math.Abs(out[j].Delta)
	})
	return out
}
