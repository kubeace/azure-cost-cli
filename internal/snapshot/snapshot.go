// Package snapshot persists cost summaries to ~/.cache/azcost (or
// $XDG_CACHE_HOME/azcost) as one JSON file per UTC date. Designed so future
// diff/anomaly commands can compare against historical state without
// re-querying ARM (which is throttled and slow).
//
// File naming: <YYYY-MM-DD>.json. Same-day saves overwrite. Other filenames
// in the directory are silently ignored by List().
//
// JSON over SQLite because:
//   - one file per day is git-able / rsync-able
//   - jq is sufficient for ad-hoc analysis
//   - no migration risk if the underlying types evolve (just re-snapshot)
//
// The Snapshot struct is treated as a contract; remove fields cautiously
// (older saved files may still be read by newer code via JSON's missing-
// field tolerance, but adding required fields will break round-trips).
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kubeace/azure-cost-cli/internal/render"
)

// Snapshot is a serializable record of cost results captured at one moment.
// Window describes the source date range; CapturedAt is the wall time the
// snapshot was written.
type Snapshot struct {
	CapturedAt time.Time        `json:"captured_at"`
	WindowFrom time.Time        `json:"window_from"`
	WindowTo   time.Time        `json:"window_to"`
	Subs       []string         `json:"subs,omitempty"`
	Services   []render.CostRow `json:"services,omitempty"`
	RGs        []render.CostRow `json:"rgs,omitempty"`
	Resources  []render.CostRow `json:"resources,omitempty"`
	Daily      map[string][]Day `json:"daily,omitempty"` // label -> series
}

type Day struct {
	Date time.Time `json:"date"`
	INR  float64   `json:"inr"`
}

// DefaultDir returns ~/.cache/azcost (XDG_CACHE_HOME respected).
func DefaultDir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "azcost")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".azcost-cache"
	}
	return filepath.Join(home, ".cache", "azcost")
}

// Save writes s to <dir>/<YYYY-MM-DD>.json (overwriting any existing file for
// that date). dir is created if absent.
func Save(dir string, s Snapshot) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	name := s.CapturedAt.UTC().Format("2006-01-02") + ".json"
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// Load reads a snapshot for the given date. date must match an existing
// "<YYYY-MM-DD>.json" file in dir.
func Load(dir string, date time.Time) (Snapshot, error) {
	path := filepath.Join(dir, date.UTC().Format("2006-01-02")+".json")
	var s Snapshot
	data, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("decode %s: %w", path, err)
	}
	return s, nil
}

// List returns the dates of every snapshot file in dir, sorted ascending.
func List(dir string) ([]time.Time, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		t, err := time.Parse("2006-01-02", base)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}
