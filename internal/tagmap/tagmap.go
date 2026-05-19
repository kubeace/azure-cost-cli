// Package tagmap loads a YAML rule file that assigns tag values (app, team,
// env, owner, …) to Azure resources based on substring or regex matches
// against the row label. It then re-aggregates CostRow slices by any tag key.
//
// Why client-side instead of querying Cost Management with a TagKey grouping?
//   - In most organisations many resources don't carry the tags you'd want
//     to filter by; a local map lets you answer "what does the AI team cost"
//     without first touching every resource in the portal.
//   - Rules can use regex to cover ephemeral resources (PVCs, VMSS instances)
//     that get created/destroyed automatically.
//   - The same map drives both the cost views AND a coverage report that
//     surfaces resources we haven't classified yet.
//
// Rule order matters: first match wins. Put the most specific rules first.
// Resources that match no rule receive the `defaults` tags (if any) and are
// also reported by Coverage as "uncovered".
package tagmap

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/kubeace/azure-cost-cli/internal/render"
	"go.yaml.in/yaml/v3"
)

// File is the on-disk schema. A nil File acts as an empty map (every row is
// uncovered, no tags assigned).
type File struct {
	Rules    []Rule            `yaml:"rules"`
	Defaults map[string]string `yaml:"defaults,omitempty"`
}

// Rule is a single classification entry. Match wins via substring search on
// the row label (case-insensitive); MatchRegex is an alternative — supply one
// or the other, not both. Tags are the key/value pairs applied on match.
type Rule struct {
	Match      string            `yaml:"match,omitempty"`
	MatchRegex string            `yaml:"match_regex,omitempty"`
	Tags       map[string]string `yaml:"tags"`

	rx *regexp.Regexp // compiled once on Load
}

// Load reads and validates a tagmap file. Returns (nil, nil) when path is
// empty so callers can treat "no tagmap" as a no-op.
func Load(path string) (*File, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tagmap %s: %w", path, err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse tagmap %s: %w", path, err)
	}
	for i := range f.Rules {
		r := &f.Rules[i]
		if r.Match == "" && r.MatchRegex == "" {
			return nil, fmt.Errorf("tagmap rule %d: must set match or match_regex", i)
		}
		if r.Match != "" && r.MatchRegex != "" {
			return nil, fmt.Errorf("tagmap rule %d: set only one of match/match_regex", i)
		}
		if r.MatchRegex != "" {
			rx, err := regexp.Compile("(?i)" + r.MatchRegex)
			if err != nil {
				return nil, fmt.Errorf("tagmap rule %d: bad regex %q: %w", i, r.MatchRegex, err)
			}
			r.rx = rx
		}
		if len(r.Tags) == 0 {
			return nil, fmt.Errorf("tagmap rule %d: tags is empty", i)
		}
	}
	return &f, nil
}

// Lookup returns the tag value for key on the given label, plus whether a rule
// matched. Defaults are consulted only when no rule matches. Empty string is
// returned when neither a rule nor a default supplies the key.
func (f *File) Lookup(label, key string) (val string, matched bool) {
	if f == nil {
		return "", false
	}
	lower := strings.ToLower(label)
	for i := range f.Rules {
		r := &f.Rules[i]
		hit := false
		switch {
		case r.rx != nil:
			hit = r.rx.MatchString(label)
		case r.Match != "":
			hit = strings.Contains(lower, strings.ToLower(r.Match))
		}
		if hit {
			if v, ok := r.Tags[key]; ok {
				return v, true
			}
			return f.Defaults[key], true
		}
	}
	return f.Defaults[key], false
}

// Matched reports whether any rule (not just a default) matched the label.
func (f *File) Matched(label string) bool {
	if f == nil {
		return false
	}
	lower := strings.ToLower(label)
	for i := range f.Rules {
		r := &f.Rules[i]
		switch {
		case r.rx != nil:
			if r.rx.MatchString(label) {
				return true
			}
		case r.Match != "":
			if strings.Contains(lower, strings.ToLower(r.Match)) {
				return true
			}
		}
	}
	return false
}

// GroupBy re-aggregates rows by tag value for the given key. Rows where the
// key resolves to "" are bucketed under "<unset>". The Sub column is dropped
// (aggregation crosses subscriptions); Extra is set to the source row count
// for that bucket.
func (f *File) GroupBy(rows []render.CostRow, key string) []render.CostRow {
	type bucket struct {
		inr   float64
		count int
	}
	m := map[string]*bucket{}
	for _, r := range rows {
		val, _ := f.Lookup(r.Label, key)
		if val == "" {
			val = "<unset>"
		}
		b, ok := m[val]
		if !ok {
			b = &bucket{}
			m[val] = b
		}
		b.inr += r.INR
		b.count++
	}
	out := make([]render.CostRow, 0, len(m))
	for k, b := range m {
		out = append(out, render.CostRow{
			Label: k,
			Extra: fmt.Sprintf("%d resources", b.count),
			INR:   b.inr,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].INR > out[j].INR })
	return out
}

// Coverage describes which rows were classified by a rule and which fell
// through. Untagged are sorted by INR descending so the biggest unknowns
// surface first.
type Coverage struct {
	Covered      int
	Uncovered    int
	CoveredINR   float64
	UncoveredINR float64
	Untagged     []render.CostRow
}

// Coverage tallies how much of the input is matched by at least one rule.
func (f *File) Coverage(rows []render.CostRow) Coverage {
	var c Coverage
	for _, r := range rows {
		if f.Matched(r.Label) {
			c.Covered++
			c.CoveredINR += r.INR
		} else {
			c.Uncovered++
			c.UncoveredINR += r.INR
			c.Untagged = append(c.Untagged, r)
		}
	}
	sort.Slice(c.Untagged, func(i, j int) bool { return c.Untagged[i].INR > c.Untagged[j].INR })
	return c
}

// DefaultPath returns the conventional config location.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/.config/azcost-tags.yaml"
}
