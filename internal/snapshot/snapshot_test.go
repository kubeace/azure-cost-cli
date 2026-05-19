package snapshot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kubeace/azure-cost-cli/internal/render"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	captured := time.Date(2026, 5, 18, 10, 30, 0, 0, time.UTC)
	want := Snapshot{
		CapturedAt: captured,
		WindowFrom: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		WindowTo:   time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC),
		Subs:       []string{"sub-a", "sub-b"},
		Services: []render.CostRow{
			{Label: "Bandwidth", INR: 1200},
			{Label: "Cognitive Services", INR: 1471654},
		},
	}
	path, err := Save(dir, want)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "2026-05-18.json" {
		t.Errorf("path=%s", path)
	}

	got, err := Load(dir, captured)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CapturedAt.Equal(want.CapturedAt) {
		t.Errorf("captured: got %v want %v", got.CapturedAt, want.CapturedAt)
	}
	if len(got.Services) != 2 || got.Services[1].INR != 1471654 {
		t.Errorf("services lost: %+v", got.Services)
	}
	if len(got.Subs) != 2 || got.Subs[0] != "sub-a" {
		t.Errorf("subs: %+v", got.Subs)
	}
}

func TestListSortedAndIgnoresJunk(t *testing.T) {
	dir := t.TempDir()
	for _, day := range []int{18, 1, 10} {
		s := Snapshot{CapturedAt: time.Date(2026, 5, day, 0, 0, 0, 0, time.UTC)}
		if _, err := Save(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	// Some junk that should be ignored
	if _, err := Save(dir, Snapshot{CapturedAt: time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	// Should NOT include directories or non-date filenames
	dates, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dates) != 4 || dates[0].Day() != 1 || dates[3].Day() != 18 {
		t.Errorf("dates=%v", dates)
	}
}

func TestListMissingDir(t *testing.T) {
	dates, err := List(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(dates) != 0 {
		t.Errorf("missing dir: dates=%v err=%v", dates, err)
	}
}
