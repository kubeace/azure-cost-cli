// Package render turns CostRow slices into one of four output formats:
// table (default human terminal), md (markdown for monthly reports), csv
// (stable schema for spreadsheets), or json (for pipelines).
//
// Two design rules:
//
//   - CostRow is the only inter-layer type. Command handlers produce []CostRow
//     and pass to Write() with an Options{Format, Top, Rate, Currency}. There
//     is no per-format type — formats agree on the same input.
//
//   - CSV uses a stable schema regardless of command: rank,label,extra,sub,
//     inr,usd. Caller-supplied headers (Options.Headers) only affect table
//     and markdown. This means downstream pipelines can parse `azcost
//     services --format csv` the same way as `azcost rgs --format csv`.
package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CostRow is the canonical row type produced by command handlers and consumed
// by all renderers. Sub/Extra are optional context columns.
type CostRow struct {
	Label string  `json:"label"`
	Sub   string  `json:"sub,omitempty"`
	Extra string  `json:"extra,omitempty"`
	INR   float64 `json:"inr"`
}

func (cr CostRow) USD(rate float64) float64 { return cr.INR / rate }

// Aggregate sums rows by key and returns them sorted by INR descending.
// First-write-wins semantics for Label/Sub/Extra: if rows for the same key
// disagree on those fields, the first observation is kept.
func Aggregate(rows []CostRow, key func(CostRow) string) []CostRow {
	m := map[string]CostRow{}
	for _, r := range rows {
		k := key(r)
		cur, ok := m[k]
		if !ok {
			cur = CostRow{Label: r.Label, Sub: r.Sub, Extra: r.Extra}
		}
		cur.INR += r.INR
		m[k] = cur
	}
	out := make([]CostRow, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].INR > out[j].INR })
	return out
}

// Format selects the output format.
type Format string

const (
	FormatTable    Format = "table"
	FormatMarkdown Format = "md"
	FormatJSON     Format = "json"
	FormatCSV      Format = "csv"
)

// Options controls a single render call. Headers labels apply to markdown/CSV;
// Title is shown by table/markdown only.
type Options struct {
	Title    string
	Headers  []string // ["#","Label","Extra","INR","USD"] convention
	Format   Format
	Top      int     // 0 = all
	Rate     float64 // INR/USD
	Currency string  // "INR"|"USD"|"both"
}

// Write renders rows to w using opts.
func Write(w io.Writer, rows []CostRow, opts Options) error {
	switch opts.Format {
	case FormatMarkdown:
		return writeMarkdown(w, rows, opts)
	case FormatJSON:
		return writeJSON(w, rows, opts)
	case FormatCSV:
		return writeCSV(w, rows, opts)
	default:
		return writeTable(w, rows, opts)
	}
}

func limit(rows []CostRow, top int) []CostRow {
	if top <= 0 || top >= len(rows) {
		return rows
	}
	return rows[:top]
}

func writeTable(w io.Writer, rows []CostRow, o Options) error {
	if o.Title != "" {
		fmt.Fprintln(w, o.Title)
		fmt.Fprintln(w, strings.Repeat("=", len(o.Title)))
	}
	for i, r := range limit(rows, o.Top) {
		switch o.Currency {
		case "USD":
			fmt.Fprintf(w, "%3d  $%10.2f  %-42s  %s\n", i+1, r.USD(o.Rate), trim(r.Extra, 42), r.Label)
		case "INR":
			fmt.Fprintf(w, "%3d  ₹%12.0f  %-42s  %s\n", i+1, r.INR, trim(r.Extra, 42), r.Label)
		default:
			fmt.Fprintf(w, "%3d  ₹%12.0f  $%9.0f  %-42s  %s\n", i+1, r.INR, r.USD(o.Rate), trim(r.Extra, 42), r.Label)
		}
	}
	fmt.Fprintln(w)
	return nil
}

func writeMarkdown(w io.Writer, rows []CostRow, o Options) error {
	if o.Title != "" {
		fmt.Fprintf(w, "## %s\n\n", o.Title)
	}
	headers := o.Headers
	if len(headers) == 0 {
		headers = []string{"#", "Label", "Extra", "INR", "USD"}
	}
	fmt.Fprintln(w, "| "+strings.Join(headers, " | ")+" |")
	sep := make([]string, len(headers))
	for i := range sep {
		sep[i] = "---"
	}
	fmt.Fprintln(w, "| "+strings.Join(sep, " | ")+" |")
	for i, r := range limit(rows, o.Top) {
		fmt.Fprintf(w, "| %d | %s | %s | %.0f | %.0f |\n", i+1, r.Label, r.Extra, r.INR, r.USD(o.Rate))
	}
	fmt.Fprintln(w)
	return nil
}

func writeJSON(w io.Writer, rows []CostRow, o Options) error {
	type out struct {
		Title string    `json:"title,omitempty"`
		Rate  float64   `json:"inr_per_usd"`
		Rows  []CostRow `json:"rows"`
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out{Title: o.Title, Rate: o.Rate, Rows: limit(rows, o.Top)})
}

// writeCSV uses a stable machine-readable schema and ignores Options.Headers
// (which are meant for human-facing layouts in table/markdown). Pipelines
// downstream can rely on these column names not changing per command.
func writeCSV(w io.Writer, rows []CostRow, o Options) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"rank", "label", "extra", "sub", "inr", "usd"}); err != nil {
		return err
	}
	for i, r := range limit(rows, o.Top) {
		row := []string{
			fmt.Sprintf("%d", i+1),
			r.Label,
			r.Extra,
			r.Sub,
			fmt.Sprintf("%.4f", r.INR),
			fmt.Sprintf("%.4f", r.USD(o.Rate)),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	return cw.Error()
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ShortResource collapses a full ARM resource ID to "<sub8>/<rg>/<name>".
func ShortResource(rid string) string {
	parts := strings.Split(rid, "/")
	var sub, rg, name string
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "subscriptions":
			if i+1 < len(parts) {
				v := parts[i+1]
				if len(v) > 8 {
					v = v[:8]
				}
				sub = v
			}
		case "resourcegroups":
			if i+1 < len(parts) {
				rg = parts[i+1]
			}
		}
	}
	if len(parts) > 0 {
		name = parts[len(parts)-1]
	}
	return fmt.Sprintf("%s/%s/%s", sub, rg, name)
}

// ExtractSub returns the subscription ID from an ARM resource ID, or "".
func ExtractSub(rid string) string {
	parts := strings.Split(rid, "/")
	for i, p := range parts {
		if strings.ToLower(p) == "subscriptions" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// LastSegment returns the final "/"-separated segment of s.
func LastSegment(s string) string {
	parts := strings.Split(s, "/")
	if len(parts) == 0 {
		return s
	}
	return parts[len(parts)-1]
}

// SafePrefix returns s[:n] when len(s) > n, else s unchanged.
func SafePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
