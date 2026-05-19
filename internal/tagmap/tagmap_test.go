package tagmap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kubeace/azure-cost-cli/internal/render"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "tags.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadEmptyPath(t *testing.T) {
	f, err := Load("")
	if err != nil || f != nil {
		t.Fatalf("want (nil, nil); got (%v, %v)", f, err)
	}
}

func TestLoadAndLookup(t *testing.T) {
	p := writeYAML(t, `
rules:
  - match: "k8s-prod-applications"
    tags: {app: cogsvc-prod, team: ai, env: prod}
  - match_regex: "aks-spotworker.*"
    tags: {team: platform, env: prod, tier: spot}
defaults:
  env: unknown
`)
	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		label, key, want string
		matched          bool
	}{
		{"core-weu/k8s-prod-applications", "team", "ai", true},
		{"mc_x/aks-spotworker05-x-vmss", "tier", "spot", true},
		{"some/random-thing", "env", "unknown", false}, // defaults
		{"some/random-thing", "team", "", false},
	}
	for _, c := range cases {
		got, matched := f.Lookup(c.label, c.key)
		if got != c.want || matched != c.matched {
			t.Errorf("Lookup(%q,%q)=(%q,%v) want (%q,%v)", c.label, c.key, got, matched, c.want, c.matched)
		}
	}
}

func TestLoadValidation(t *testing.T) {
	cases := map[string]string{
		"no match clause":  `rules: [{tags: {a: b}}]`,
		"both match types": `rules: [{match: x, match_regex: y, tags: {a: b}}]`,
		"empty tags":       `rules: [{match: x, tags: {}}]`,
		"bad regex":        `rules: [{match_regex: "[", tags: {a: b}}]`,
	}
	for name, body := range cases {
		p := writeYAML(t, body)
		if _, err := Load(p); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestGroupByAndCoverage(t *testing.T) {
	p := writeYAML(t, `
rules:
  - match: "openai"
    tags: {team: ai}
  - match: "postgres"
    tags: {team: data}
`)
	f, _ := Load(p)
	rows := []render.CostRow{
		{Label: "a/tv-azure-openai-01", INR: 100},
		{Label: "a/tv-au-openai", INR: 50},
		{Label: "a/tv-postgres-07", INR: 200},
		{Label: "a/something-else", INR: 30},
	}

	g := f.GroupBy(rows, "team")
	if len(g) != 3 {
		t.Fatalf("want 3 buckets, got %d", len(g))
	}
	if g[0].Label != "data" || g[0].INR != 200 {
		t.Errorf("top bucket %+v", g[0])
	}

	cov := f.Coverage(rows)
	if cov.Covered != 3 || cov.Uncovered != 1 {
		t.Errorf("coverage %+v", cov)
	}
	if cov.UncoveredINR != 30 {
		t.Errorf("uncovered INR = %f", cov.UncoveredINR)
	}
	if len(cov.Untagged) != 1 || cov.Untagged[0].Label != "a/something-else" {
		t.Errorf("untagged %+v", cov.Untagged)
	}
}

func TestNilFileBehavesAsEmpty(t *testing.T) {
	var f *File
	if v, m := f.Lookup("x", "k"); v != "" || m {
		t.Errorf("nil Lookup = (%q,%v)", v, m)
	}
	if f.Matched("x") {
		t.Errorf("nil Matched true")
	}
	rows := []render.CostRow{{Label: "a", INR: 1}}
	g := f.GroupBy(rows, "team")
	if len(g) != 1 || g[0].Label != "<unset>" || g[0].INR != 1 {
		t.Errorf("nil GroupBy %+v", g)
	}
	c := f.Coverage(rows)
	if c.Covered != 0 || c.Uncovered != 1 {
		t.Errorf("nil Coverage %+v", c)
	}
}
