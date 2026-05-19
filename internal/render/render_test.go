package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestShortResource(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"full ARM id",
			"/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg-prod/providers/Microsoft.Storage/storageAccounts/mystorage",
			"00000000/rg-prod/mystorage"},
		{"lowercase variant", "/subscriptions/abc/resourcegroups/rg1/providers/x/y/name1", "abc/rg1/name1"},
		{"empty", "", "//"},
		{"short sub", "/subscriptions/ab/resourceGroups/rg/providers/x/y/n", "ab/rg/n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShortResource(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestExtractSub(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/subscriptions/abc/resourceGroups/x", "abc"},
		{"/SUBSCRIPTIONS/XYZ/foo", "XYZ"},
		{"", ""},
		{"/subscriptions/", ""},
	}
	for _, tc := range cases {
		if got := ExtractSub(tc.in); got != tc.want {
			t.Errorf("ExtractSub(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestSafePrefix(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"abcdef", 3, "abc"},
		{"ab", 5, "ab"},
		{"", 5, ""},
		{"abcdefgh", 8, "abcdefgh"},
	} {
		if got := SafePrefix(tc.s, tc.n); got != tc.want {
			t.Errorf("SafePrefix(%q,%d)=%q want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

func TestAggregateSumsAndSorts(t *testing.T) {
	in := []CostRow{{Label: "A", INR: 10}, {Label: "B", INR: 50}, {Label: "A", INR: 5}, {Label: "C", INR: 20}}
	out := Aggregate(in, func(r CostRow) string { return r.Label })
	if len(out) != 3 || out[0].Label != "B" || out[0].INR != 50 || out[1].Label != "C" || out[2].Label != "A" || out[2].INR != 15 {
		t.Fatalf("got %+v", out)
	}
}

func TestAggregateFirstWriteWins(t *testing.T) {
	in := []CostRow{{Label: "A", Extra: "first", INR: 1}, {Label: "A", Extra: "second", INR: 2}}
	out := Aggregate(in, func(r CostRow) string { return r.Label })
	if out[0].Extra != "first" {
		t.Errorf("want first-write-wins for Extra, got %q", out[0].Extra)
	}
	if out[0].INR != 3 {
		t.Errorf("want summed INR=3, got %v", out[0].INR)
	}
}

func TestWriteFormats(t *testing.T) {
	rows := []CostRow{{Label: "svcA", Extra: "type1", INR: 100}, {Label: "svcB", INR: 50}}
	o := Options{Title: "T", Format: FormatTable, Rate: 10, Currency: "both"}

	t.Run("table", func(t *testing.T) {
		var b bytes.Buffer
		if err := Write(&b, rows, o); err != nil {
			t.Fatal(err)
		}
		s := b.String()
		if !strings.Contains(s, "T\n=") || !strings.Contains(s, "svcA") || !strings.Contains(s, "₹") {
			t.Errorf("unexpected table output:\n%s", s)
		}
	})

	t.Run("markdown", func(t *testing.T) {
		var b bytes.Buffer
		o2 := o
		o2.Format = FormatMarkdown
		o2.Headers = []string{"#", "Service", "Type", "INR", "USD"}
		if err := Write(&b, rows, o2); err != nil {
			t.Fatal(err)
		}
		s := b.String()
		if !strings.Contains(s, "## T") || !strings.Contains(s, "| #") || !strings.Contains(s, "| 1 | svcA") {
			t.Errorf("bad md:\n%s", s)
		}
	})

	t.Run("json", func(t *testing.T) {
		var b bytes.Buffer
		o2 := o
		o2.Format = FormatJSON
		if err := Write(&b, rows, o2); err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Title string    `json:"title"`
			Rate  float64   `json:"inr_per_usd"`
			Rows  []CostRow `json:"rows"`
		}
		if err := json.Unmarshal(b.Bytes(), &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Title != "T" || parsed.Rate != 10 || len(parsed.Rows) != 2 || parsed.Rows[0].Label != "svcA" {
			t.Errorf("bad json: %+v", parsed)
		}
	})

	t.Run("csv", func(t *testing.T) {
		var b bytes.Buffer
		o2 := o
		o2.Format = FormatCSV
		if err := Write(&b, rows, o2); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(b.String()), "\n")
		if len(lines) != 3 || !strings.HasPrefix(lines[0], "rank,label") || !strings.HasPrefix(lines[1], "1,svcA") {
			t.Errorf("bad csv:\n%s", b.String())
		}
	})
}

func TestWriteTop(t *testing.T) {
	rows := []CostRow{{Label: "A", INR: 3}, {Label: "B", INR: 2}, {Label: "C", INR: 1}}
	var b bytes.Buffer
	_ = Write(&b, rows, Options{Format: FormatCSV, Top: 2, Rate: 1})
	if strings.Count(b.String(), "\n") != 3 { // 1 header + 2 rows
		t.Errorf("want 2 data rows + header, got:\n%s", b.String())
	}
}

func TestLastSegment(t *testing.T) {
	if LastSegment("/a/b/c") != "c" || LastSegment("") != "" {
		t.Error("LastSegment broken")
	}
}
