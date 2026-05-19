package trends

import (
	"math"
	"testing"
	"time"
)

func d(y, m, day int) time.Time {
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
}

func TestRollingAvgExcludesPivot(t *testing.T) {
	s := DailySeries{Days: []DayPoint{
		{d(2026, 5, 10), 100}, {d(2026, 5, 11), 200}, {d(2026, 5, 12), 300},
		{d(2026, 5, 13), 1000}, // pivot, should NOT be in avg
	}}
	avg := RollingAvg(s, d(2026, 5, 13), 3)
	want := (100.0 + 200 + 300) / 3
	if avg != want {
		t.Errorf("avg=%v want %v", avg, want)
	}
}

func TestRollingAvgInsufficientHistory(t *testing.T) {
	s := DailySeries{Days: []DayPoint{{d(2026, 5, 12), 500}}}
	avg := RollingAvg(s, d(2026, 5, 13), 7)
	if avg != 500 {
		t.Errorf("with only 1 pt, want it back; got %v", avg)
	}
}

func TestDetectAnomaliesFiresOnSpike(t *testing.T) {
	series := []DailySeries{
		{Label: "spike", Days: []DayPoint{
			{d(2026, 5, 10), 100}, {d(2026, 5, 11), 100}, {d(2026, 5, 12), 100},
			{d(2026, 5, 13), 100}, {d(2026, 5, 14), 100}, {d(2026, 5, 15), 100},
			{d(2026, 5, 16), 100}, {d(2026, 5, 17), 300}, // 3x baseline
		}},
		{Label: "flat", Days: []DayPoint{
			{d(2026, 5, 10), 100}, {d(2026, 5, 11), 100}, {d(2026, 5, 12), 100},
			{d(2026, 5, 13), 100}, {d(2026, 5, 14), 100}, {d(2026, 5, 15), 100},
			{d(2026, 5, 16), 100}, {d(2026, 5, 17), 105},
		}},
	}
	got := DetectAnomalies(series, 1.5, 50, 7)
	if len(got) != 1 || got[0].Label != "spike" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Ratio != 3.0 {
		t.Errorf("ratio=%v want 3.0", got[0].Ratio)
	}
}

func TestDetectAnomaliesSuppressesSmall(t *testing.T) {
	series := []DailySeries{{Label: "tiny", Days: []DayPoint{
		{d(2026, 5, 10), 1}, {d(2026, 5, 11), 1}, {d(2026, 5, 12), 1},
		{d(2026, 5, 13), 1}, {d(2026, 5, 14), 1}, {d(2026, 5, 15), 1},
		{d(2026, 5, 16), 1}, {d(2026, 5, 17), 10},
	}}}
	got := DetectAnomalies(series, 1.5, 50, 7)
	if len(got) != 0 {
		t.Errorf("min-abs should suppress: %+v", got)
	}
}

func TestDetectAnomaliesSkipsNewSeries(t *testing.T) {
	series := []DailySeries{{Label: "new", Days: []DayPoint{
		{d(2026, 5, 16), 100}, {d(2026, 5, 17), 1000},
	}}}
	got := DetectAnomalies(series, 1.5, 50, 7)
	if len(got) != 0 {
		t.Errorf("insufficient history should skip, got %+v", got)
	}
}

func TestDiffMath(t *testing.T) {
	before := map[string]float64{"A": 100, "B": 50, "C": 200}
	after := map[string]float64{"A": 150, "B": 0, "D": 300}
	got := Diff(before, after)

	// Sorted by |Delta|: D +300, C -200, A +50, B -50  → ties broken by map order so check by label
	byLabel := map[string]Mover{}
	for _, m := range got {
		byLabel[m.Label] = m
	}
	if byLabel["A"].Delta != 50 || byLabel["A"].PctChange != 50 {
		t.Errorf("A: %+v", byLabel["A"])
	}
	if byLabel["B"].After != 0 || byLabel["B"].Delta != -50 {
		t.Errorf("B: %+v", byLabel["B"])
	}
	if byLabel["C"].After != 0 || byLabel["C"].PctChange != -100 {
		t.Errorf("C: %+v", byLabel["C"])
	}
	if byLabel["D"].Before != 0 || !math.IsInf(byLabel["D"].PctChange, 1) {
		t.Errorf("D: %+v", byLabel["D"])
	}
	// First entry should be biggest |delta|: D
	if got[0].Label != "D" {
		t.Errorf("top mover should be D, got %s", got[0].Label)
	}
}

func TestDiffEmpty(t *testing.T) {
	got := Diff(map[string]float64{}, map[string]float64{})
	if len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}
