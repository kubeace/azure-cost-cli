package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSparklineEmpty(t *testing.T) {
	if Sparkline(nil) != "" {
		t.Error("nil should be empty")
	}
}

func TestSparklineAllZero(t *testing.T) {
	got := Sparkline([]float64{0, 0, 0})
	if got != "   " {
		t.Errorf("all-zero want 3 spaces, got %q", got)
	}
}

func TestSparklineMonotonic(t *testing.T) {
	got := Sparkline([]float64{1, 2, 3, 4, 5, 6, 7, 8})
	// Rune count should equal input length
	if utf8.RuneCountInString(got) != 8 {
		t.Errorf("want 8 runes, got %d (%q)", utf8.RuneCountInString(got), got)
	}
	// Last rune should be the densest block
	last := []rune(got)[7]
	if last != '█' {
		t.Errorf("max should be █, got %c", last)
	}
}

func TestSparklineZerosBecomeSpaces(t *testing.T) {
	got := Sparkline([]float64{0, 10, 0, 10})
	runes := []rune(got)
	if len(runes) != 4 {
		t.Fatalf("len=%d", len(runes))
	}
	if runes[0] != ' ' || runes[2] != ' ' {
		t.Errorf("zeros should be spaces: %q", got)
	}
	if !strings.ContainsRune(got, '█') {
		t.Errorf("10s should hit max block: %q", got)
	}
}
