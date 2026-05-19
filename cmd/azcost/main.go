// Command azcost is an Azure cost-management CLI.
//
// See README.md for usage. Subcommands:
//
//	subs services rgs resources service-split cogsvc report
//	daily diff anomaly snapshot tags tag-coverage
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/kubeace/azure-cost-cli/internal/azure"
	cfg "github.com/kubeace/azure-cost-cli/internal/config"
	"github.com/kubeace/azure-cost-cli/internal/render"
	"github.com/kubeace/azure-cost-cli/internal/tagmap"
)

// Global flags backed by viper.
type globalFlags struct {
	sub      string
	subs     []string
	from     string
	to       string
	rate     float64
	currency string
	format   string
	top      int
	rps      int
	tenant   string
	tagmap   string
	groupTag string
	auth     string
}

var g globalFlags

// Build-time metadata. Populated via -ldflags during `make build`/`make install`.
// Defaults are placeholders for `go build`/`go run` invocations.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	cfgPath := cfg.Init()

	root := &cobra.Command{
		Use:     "azcost",
		Short:   "Azure cost-management CLI",
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildDate),
		Long: `azcost queries Azure Cost Management + Monitor for MTD breakdowns,
per-resource service splits, and Cognitive Services per-deployment token usage.

Auth: defaults to your local 'az login' session. Workload identity, managed
identity and service-principal env vars also work via --auth (see README).
Config: ~/.config/azcost.yaml or AZCOST_* env vars.`,
		SilenceUsage: true,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&g.sub, "sub", "", "subscription id (sub-scoped commands)")
	pf.StringSliceVar(&g.subs, "subs", nil, "comma-separated subscriptions (report)")
	pf.StringVar(&g.from, "from", "", "start YYYY-MM-DD (default: first of current month UTC)")
	pf.StringVar(&g.to, "to", "", "end YYYY-MM-DD (default: today UTC)")
	pf.Float64Var(&g.rate, "rate", cfg.DefaultRate, "local-currency-to-USD divisor (1.0 = passthrough; set e.g. 89.985 for INR-billed)")
	pf.StringVar(&g.currency, "currency", "USD", "USD|local|both (local = your raw Azure billing currency)")
	pf.StringVar(&g.format, "format", "", "table|md|csv|json (default: table; md for report)")
	pf.IntVar(&g.top, "top", 0, "top-N rows (defaults vary by command)")
	pf.IntVar(&g.rps, "rps", cfg.DefaultRPS, "max ARM requests/sec")
	pf.StringVar(&g.tenant, "tenant", "", "Azure tenant ID (disambiguates multi-tenant az login)")
	pf.StringVar(&g.tagmap, "tagmap", tagmap.DefaultPath(), "path to tag-map YAML (loaded if file exists)")
	pf.StringVar(&g.groupTag, "group-tag", "", "re-aggregate output by this tag key (e.g. team, app, env)")
	pf.StringVar(&g.auth, "auth", "auto", "auth mode: auto (env > cli) | cli (force az login token) | sp (force AZURE_CLIENT_* env)")

	// bind to viper so AZCOST_* env vars and config file values feed in
	for _, name := range []string{"sub", "subs", "from", "to", "rate", "currency", "format", "top", "rps", "tenant", "tagmap", "group-tag", "auth"} {
		_ = viper.BindPFlag(name, pf.Lookup(name))
	}

	root.AddCommand(
		newSubsCmd(),
		newServicesCmd(),
		newRGsCmd(),
		newResourcesCmd(),
		newServiceSplitCmd(),
		newCogsvcCmd(),
		newReportCmd(),
		newDailyCmd(),
		newDiffCmd(),
		newAnomalyCmd(),
		newSnapshotCmd(),
		newTagsCmd(),
		newTagCoverageCmd(),
	)

	if cfgPath != "" && os.Getenv("AZCOST_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "azcost: loaded config %s\n", cfgPath)
	}

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// ===== shared helpers =====

func ctxWithSignal() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}

func parseRange(from, to string) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	p := func(s string, def time.Time) (time.Time, error) {
		if s == "" {
			return def, nil
		}
		return time.Parse("2006-01-02", s)
	}
	defFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	defTo := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
	f, err := p(from, defFrom)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("from: %w", err)
	}
	t, err := p(to, defTo)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("to: %w", err)
	}
	return f, t, nil
}

func customOrMTD(from, to time.Time) (tf string, f, t time.Time) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if from.Equal(monthStart) && to.Day() == now.Day() && to.Month() == now.Month() {
		return "MonthToDate", time.Time{}, time.Time{}
	}
	return "Custom", from, to
}

func subScope(id string) string { return "subscriptions/" + id }

func discoverSubs() []string {
	out, err := exec.Command("az", "account", "list", "--query", "[?state=='Enabled'].id", "-o", "tsv").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

func newClient() (*azure.Client, error) {
	return azure.NewClient(g.rps, g.tenant, azure.AuthMode(g.auth))
}

// pickFormat resolves the --format flag with the per-command default.
func pickFormat(def render.Format) render.Format {
	if g.format == "" {
		return def
	}
	return render.Format(g.format)
}

func renderOpts(title string, headers []string, top int, def render.Format) render.Options {
	return render.Options{
		Title:    title,
		Headers:  headers,
		Format:   pickFormat(def),
		Top:      top,
		Rate:     g.rate,
		Currency: g.currency,
	}
}

// ===== commands =====

func newSubsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "subs",
		Short: "MTD cost per subscription (auto-discovers via 'az account list')",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := ctxWithSignal()
			defer cancel()

			subList := g.subs
			if len(subList) == 0 {
				subList = discoverSubs()
			}
			if len(subList) == 0 {
				return fmt.Errorf("no subscriptions; pass --subs or run 'az login'")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			from, to, err := parseRange(g.from, g.to)
			if err != nil {
				return err
			}
			tf, f, t := customOrMTD(from, to)

			type sr struct {
				ID, Name string
				INR      float64
			}
			results := make([]sr, len(subList))
			var wg sync.WaitGroup
			for i, s := range subList {
				i, s := i, s
				wg.Add(1)
				go func() {
					defer wg.Done()
					rows, err := c.Query(ctx, azure.QueryOptions{
						Scope: subScope(s), Timeframe: tf, From: f, To: t,
						GroupBy: []string{"SubscriptionName"},
					})
					if err != nil {
						fmt.Fprintf(os.Stderr, "subs %s: %v\n", s, err)
						return
					}
					var tot float64
					var name string
					for _, r := range rows {
						tot += r.Float("Cost")
						if name == "" {
							name = r.String("SubscriptionName")
						}
					}
					results[i] = sr{ID: s, Name: name, INR: tot}
				}()
			}
			wg.Wait()

			var rows []render.CostRow
			for _, r := range results {
				if r.ID == "" {
					continue
				}
				rows = append(rows, render.CostRow{Label: r.ID, Extra: r.Name, INR: r.INR})
			}
			rows = render.Aggregate(rows, func(r render.CostRow) string { return r.Label })
			title := fmt.Sprintf("MTD cost by subscription (%s → %s)", from.Format("2006-01-02"), to.Format("2006-01-02"))
			return render.Write(os.Stdout, rows,
				renderOpts(title, []string{"#", "Subscription", "Name", "INR", "USD"}, g.top, render.FormatTable))
		},
	}
}

func newServicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "services",
		Short: "MTD cost by service name for one subscription",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.sub == "" {
				return fmt.Errorf("--sub required")
			}
			ctx, cancel := ctxWithSignal()
			defer cancel()
			c, err := newClient()
			if err != nil {
				return err
			}
			from, to, err := parseRange(g.from, g.to)
			if err != nil {
				return err
			}
			tf, f, t := customOrMTD(from, to)
			rows, err := c.Query(ctx, azure.QueryOptions{
				Scope: subScope(g.sub), Timeframe: tf, From: f, To: t,
				GroupBy: []string{"ServiceName"},
			})
			if err != nil {
				return err
			}
			cr := toCostRows(rows, "ServiceName", "")
			cr = render.Aggregate(cr, func(r render.CostRow) string { return r.Label })
			title := fmt.Sprintf("Services for %s (%s → %s)", g.sub, from.Format("2006-01-02"), to.Format("2006-01-02"))
			headers := []string{"#", "Service", "—", "INR", "USD"}
			cr, title, headers, err = maybeRegroup(cr, title, headers)
			if err != nil {
				return err
			}
			return render.Write(os.Stdout, cr,
				renderOpts(title, headers, g.top, render.FormatTable))
		},
	}
}

func newRGsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rgs",
		Short: "Top resource groups for one subscription",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.sub == "" {
				return fmt.Errorf("--sub required")
			}
			top := g.top
			if top == 0 {
				top = 25
			}
			ctx, cancel := ctxWithSignal()
			defer cancel()
			c, err := newClient()
			if err != nil {
				return err
			}
			from, to, err := parseRange(g.from, g.to)
			if err != nil {
				return err
			}
			tf, f, t := customOrMTD(from, to)
			rows, err := c.Query(ctx, azure.QueryOptions{
				Scope: subScope(g.sub), Timeframe: tf, From: f, To: t,
				GroupBy: []string{"ResourceGroupName"},
			})
			if err != nil {
				return err
			}
			cr := toCostRows(rows, "ResourceGroupName", "")
			cr = render.Aggregate(cr, func(r render.CostRow) string { return r.Label })
			title := fmt.Sprintf("Top %d resource groups for %s", top, g.sub)
			headers := []string{"#", "Resource Group", "—", "INR", "USD"}
			cr, title, headers, err = maybeRegroup(cr, title, headers)
			if err != nil {
				return err
			}
			return render.Write(os.Stdout, cr,
				renderOpts(title, headers, top, render.FormatTable))
		},
	}
}

func newResourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resources",
		Short: "Top resources for one subscription (with resource type)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.sub == "" {
				return fmt.Errorf("--sub required")
			}
			top := g.top
			if top == 0 {
				top = 30
			}
			ctx, cancel := ctxWithSignal()
			defer cancel()
			c, err := newClient()
			if err != nil {
				return err
			}
			from, to, err := parseRange(g.from, g.to)
			if err != nil {
				return err
			}
			tf, f, t := customOrMTD(from, to)
			rows, err := c.Query(ctx, azure.QueryOptions{
				Scope: subScope(g.sub), Timeframe: tf, From: f, To: t,
				GroupBy: []string{"ResourceId", "ResourceType"},
			})
			if err != nil {
				return err
			}
			cr := make([]render.CostRow, 0, len(rows))
			for _, r := range rows {
				cr = append(cr, render.CostRow{
					Label: render.ShortResource(r.String("ResourceId")),
					Extra: r.String("ResourceType"),
					INR:   r.Float("Cost"),
				})
			}
			cr = render.Aggregate(cr, func(r render.CostRow) string { return r.Label })
			title := fmt.Sprintf("Top %d resources for %s", top, g.sub)
			headers := []string{"#", "Resource", "Type", "INR", "USD"}
			cr, title, headers, err = maybeRegroup(cr, title, headers)
			if err != nil {
				return err
			}
			return render.Write(os.Stdout, cr,
				renderOpts(title, headers, top, render.FormatTable))
		},
	}
}

func newServiceSplitCmd() *cobra.Command {
	var service string
	c := &cobra.Command{
		Use:   "service-split",
		Short: "Per-resource cost split within a single service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.sub == "" {
				return fmt.Errorf("--sub required")
			}
			if service == "" {
				return fmt.Errorf("--service required")
			}
			top := g.top
			if top == 0 {
				top = 20
			}
			ctx, cancel := ctxWithSignal()
			defer cancel()
			cl, err := newClient()
			if err != nil {
				return err
			}
			from, to, err := parseRange(g.from, g.to)
			if err != nil {
				return err
			}
			tf, f, t := customOrMTD(from, to)
			rows, err := cl.Query(ctx, azure.QueryOptions{
				Scope: subScope(g.sub), Timeframe: tf, From: f, To: t,
				GroupBy:       []string{"ResourceId"},
				FilterService: service,
			})
			if err != nil {
				return err
			}
			cr := make([]render.CostRow, 0, len(rows))
			for _, r := range rows {
				cr = append(cr, render.CostRow{
					Label: render.ShortResource(r.String("ResourceId")),
					INR:   r.Float("Cost"),
				})
			}
			cr = render.Aggregate(cr, func(r render.CostRow) string { return r.Label })
			title := fmt.Sprintf("%s in %s — top %d resources", service, g.sub, top)
			return render.Write(os.Stdout, cr,
				renderOpts(title, []string{"#", "Resource", "—", "INR", "USD"}, top, render.FormatTable))
		},
	}
	c.Flags().StringVar(&service, "service", "", "ServiceName filter (required)")
	return c
}

func newCogsvcCmd() *cobra.Command {
	var rid string
	c := &cobra.Command{
		Use:   "cogsvc",
		Short: "Meter + per-deployment tokens/cache% for a Cognitive Services account",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rid == "" {
				return fmt.Errorf("--rid required")
			}
			subID := render.ExtractSub(rid)
			if subID == "" {
				return fmt.Errorf("could not parse subscription id from %s", rid)
			}
			ctx, cancel := ctxWithSignal()
			defer cancel()
			cl, err := newClient()
			if err != nil {
				return err
			}
			from, to, err := parseRange(g.from, g.to)
			if err != nil {
				return err
			}
			tf, f, t := customOrMTD(from, to)

			meterRows, err := cl.Query(ctx, azure.QueryOptions{
				Scope: subScope(subID), Timeframe: tf, From: f, To: t,
				GroupBy:      []string{"Meter", "MeterSubCategory"},
				FilterResIDs: []string{rid},
			})
			if err != nil {
				return fmt.Errorf("meter query: %w", err)
			}
			var meterCR []render.CostRow
			for _, r := range meterRows {
				meterCR = append(meterCR, render.CostRow{
					Label: r.String("Meter"),
					Extra: r.String("MeterSubCategory"),
					INR:   r.Float("Cost"),
				})
			}
			meterCR = render.Aggregate(meterCR, func(r render.CostRow) string { return r.Label })

			title := fmt.Sprintf("Meter split — %s", render.LastSegment(rid))
			if err := render.Write(os.Stdout, meterCR,
				renderOpts(title, []string{"#", "Meter", "SubCategory", "INR", "USD"}, 0, render.FormatTable)); err != nil {
				return err
			}

			prompt, _ := cl.Metric(ctx, azure.MetricOptions{
				ResourceID: rid, MetricName: "ProcessedPromptTokens", Aggregation: "Total",
				From: from, To: to, Interval: "P1D", SplitBy: "ModelDeploymentName",
			})
			gen, _ := cl.Metric(ctx, azure.MetricOptions{
				ResourceID: rid, MetricName: "GeneratedTokens", Aggregation: "Total",
				From: from, To: to, Interval: "P1D", SplitBy: "ModelDeploymentName",
			})
			cache, _ := cl.Metric(ctx, azure.MetricOptions{
				ResourceID: rid, MetricName: "AzureOpenAIContextTokensCacheMatchRate", Aggregation: "Average",
				From: from, To: to, Interval: "P1D", SplitBy: "ModelDeploymentName",
			})

			if len(prompt) == 0 && len(gen) == 0 {
				fmt.Println("(no deployment-level metrics — resource may not be a Cognitive Services account)")
				return nil
			}

			type drow struct {
				dep                     string
				input, output, cacheAvg float64
			}
			dm := map[string]drow{}
			for _, s := range prompt {
				e := dm[s.Dims["modeldeploymentname"]]
				e.dep = s.Dims["modeldeploymentname"]
				e.input = s.Total
				dm[e.dep] = e
			}
			for _, s := range gen {
				e := dm[s.Dims["modeldeploymentname"]]
				e.dep = s.Dims["modeldeploymentname"]
				e.output = s.Total
				dm[e.dep] = e
			}
			for _, s := range cache {
				e := dm[s.Dims["modeldeploymentname"]]
				e.dep = s.Dims["modeldeploymentname"]
				e.cacheAvg = s.Avg
				dm[e.dep] = e
			}
			deps := make([]drow, 0, len(dm))
			for _, v := range dm {
				deps = append(deps, v)
			}
			sort.Slice(deps, func(i, j int) bool { return deps[i].input > deps[j].input })

			fmt.Println("\nDeployment-level token usage")
			fmt.Println("============================")
			fmt.Printf("%-50s  %15s  %15s  %7s\n", "Deployment", "Input tokens", "Output tokens", "Cache%")
			for _, d := range deps {
				suffix := ""
				if d.cacheAvg < 10 && d.input > 1e9 {
					suffix = "  ← low cache hit; consider prompt-caching review"
				}
				fmt.Printf("%-50s  %15.0f  %15.0f  %6.2f%%%s\n",
					trim(d.dep, 50), d.input, d.output, d.cacheAvg, suffix)
			}
			fmt.Println()
			return nil
		},
	}
	c.Flags().StringVar(&rid, "rid", "", "Cognitive Services ARM resource ID (required)")
	return c
}

func newReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "Full MTD markdown report (subs + services + RGs + top resources + service splits)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			subList := g.subs
			if len(subList) == 0 {
				subList = discoverSubs()
			}
			if len(subList) == 0 {
				return fmt.Errorf("no subscriptions; pass --subs or run 'az login'")
			}
			ctx, cancel := ctxWithSignal()
			defer cancel()
			cl, err := newClient()
			if err != nil {
				return err
			}
			from, to, err := parseRange(g.from, g.to)
			if err != nil {
				return err
			}
			tf, f, t := customOrMTD(from, to)
			return runReport(ctx, cl, subList, from, to, tf, f, t, g.rate)
		},
	}
}

// runReport is exposed for future reuse (diff, snapshots).
func runReport(ctx context.Context, c *azure.Client, subs []string, from, to time.Time, tf string, f, t time.Time, rate float64) error {
	w := os.Stdout
	fmt.Fprintf(w, "# Azure MTD Cost Report — %s → %s\n\n", from.Format("2006-01-02"), to.Format("2006-01-02"))
	fmt.Fprintf(w, "- Subscriptions: %d\n- Conversion: ₹%.3f/USD\n\n", len(subs), rate)

	type subResult struct {
		ID                                string
		ServiceRows, RGRows, ResourceRows []render.CostRow
	}
	results := make([]subResult, len(subs))
	var wg sync.WaitGroup
	for i, s := range subs {
		i, s := i, s
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc, err := c.Query(ctx, azure.QueryOptions{Scope: subScope(s), Timeframe: tf, From: f, To: t, GroupBy: []string{"ServiceName"}})
			if err != nil {
				fmt.Fprintf(os.Stderr, "report: sub %s services: %v\n", s, err)
			}
			rg, err := c.Query(ctx, azure.QueryOptions{Scope: subScope(s), Timeframe: tf, From: f, To: t, GroupBy: []string{"ResourceGroupName"}})
			if err != nil {
				fmt.Fprintf(os.Stderr, "report: sub %s rgs: %v\n", s, err)
			}
			res, err := c.Query(ctx, azure.QueryOptions{Scope: subScope(s), Timeframe: tf, From: f, To: t, GroupBy: []string{"ResourceId", "ResourceType"}})
			if err != nil {
				fmt.Fprintf(os.Stderr, "report: sub %s resources: %v\n", s, err)
			}
			r := subResult{ID: s}
			for _, x := range svc {
				r.ServiceRows = append(r.ServiceRows, render.CostRow{Label: x.String("ServiceName"), INR: x.Float("Cost")})
			}
			for _, x := range rg {
				r.RGRows = append(r.RGRows, render.CostRow{Label: x.String("ResourceGroupName"), Sub: s, INR: x.Float("Cost")})
			}
			for _, x := range res {
				r.ResourceRows = append(r.ResourceRows, render.CostRow{
					Label: render.ShortResource(x.String("ResourceId")),
					Sub:   s, Extra: x.String("ResourceType"),
					INR: x.Float("Cost"),
				})
			}
			results[i] = r
		}()
	}
	wg.Wait()

	var subRows []render.CostRow
	var totalINR float64
	for _, r := range results {
		var tot float64
		for _, s := range r.ServiceRows {
			tot += s.INR
		}
		subRows = append(subRows, render.CostRow{Label: r.ID, INR: tot})
		totalINR += tot
	}
	subRows = render.Aggregate(subRows, func(r render.CostRow) string { return r.Label })
	_ = render.Write(w, subRows, render.Options{Title: "By Subscription", Headers: []string{"#", "Sub ID", "—", "INR", "USD"}, Format: render.FormatMarkdown, Rate: rate, Currency: "both"})
	fmt.Fprintf(w, "**Total: ₹%.0f ≈ $%.0f**\n\n", totalINR, totalINR/rate)

	var allSvc []render.CostRow
	for _, r := range results {
		allSvc = append(allSvc, r.ServiceRows...)
	}
	allSvc = render.Aggregate(allSvc, func(r render.CostRow) string { return r.Label })
	_ = render.Write(w, allSvc, render.Options{Title: "Top Services (global)", Headers: []string{"#", "Service", "—", "INR", "USD"}, Format: render.FormatMarkdown, Top: 10, Rate: rate, Currency: "both"})

	var allRG []render.CostRow
	for _, r := range results {
		for _, x := range r.RGRows {
			allRG = append(allRG, render.CostRow{Label: x.Label, Extra: render.SafePrefix(x.Sub, 8), INR: x.INR})
		}
	}
	_ = render.Write(w, allRG, render.Options{Title: "Top Resource Groups", Headers: []string{"#", "RG", "Sub", "INR", "USD"}, Format: render.FormatMarkdown, Top: 25, Rate: rate, Currency: "both"})

	var allRes []render.CostRow
	for _, r := range results {
		allRes = append(allRes, r.ResourceRows...)
	}
	allRes = render.Aggregate(allRes, func(r render.CostRow) string { return r.Label })
	_ = render.Write(w, allRes, render.Options{Title: "Top 30 Resources", Headers: []string{"#", "Resource", "Type", "INR", "USD"}, Format: render.FormatMarkdown, Top: 30, Rate: rate, Currency: "both"})

	if len(subRows) > 0 && len(allSvc) > 0 {
		topSvc := allSvc
		if len(topSvc) > 5 {
			topSvc = topSvc[:5]
		}
		fmt.Fprintln(w, "## Top-5 Service Splits (largest subscription only)")
		bigSub := subRows[0].Label
		for _, s := range topSvc {
			rows, err := c.Query(ctx, azure.QueryOptions{
				Scope: subScope(bigSub), Timeframe: tf, From: f, To: t,
				GroupBy:       []string{"ResourceId"},
				FilterService: s.Label,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "split %s: %v\n", s.Label, err)
				continue
			}
			var cr []render.CostRow
			for _, x := range rows {
				cr = append(cr, render.CostRow{Label: render.ShortResource(x.String("ResourceId")), INR: x.Float("Cost")})
			}
			cr = render.Aggregate(cr, func(r render.CostRow) string { return r.Label })
			_ = render.Write(w, cr, render.Options{Title: s.Label, Headers: []string{"#", "Resource", "—", "INR", "USD"}, Format: render.FormatMarkdown, Top: 10, Rate: rate, Currency: "both"})
		}
	}
	return nil
}

func toCostRows(rows []azure.Row, labelCol, extraCol string) []render.CostRow {
	out := make([]render.CostRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, render.CostRow{
			Label: r.String(labelCol),
			Extra: r.String(extraCol),
			INR:   r.Float("Cost"),
		})
	}
	return out
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// loadTagmap loads the tagmap if --tagmap points at an existing file. A
// missing file is not an error (returns nil, nil) so the flag's default path
// is harmless when no map is configured.
func loadTagmap() (*tagmap.File, error) {
	path := g.tagmap
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	return tagmap.Load(path)
}

// maybeRegroup returns rows re-aggregated by tag value when --group-tag is
// set; otherwise returns rows unchanged. headers and title are also updated
// to reflect the regrouping.
func maybeRegroup(rows []render.CostRow, title string, headers []string) ([]render.CostRow, string, []string, error) {
	if g.groupTag == "" {
		return rows, title, headers, nil
	}
	f, err := loadTagmap()
	if err != nil {
		return nil, "", nil, fmt.Errorf("load tagmap: %w", err)
	}
	if f == nil {
		return nil, "", nil, fmt.Errorf("--group-tag set but no tagmap at %s", g.tagmap)
	}
	out := f.GroupBy(rows, g.groupTag)
	newTitle := fmt.Sprintf("%s — grouped by tag/%s", title, g.groupTag)
	newHeaders := []string{"#", g.groupTag, "Resources", "INR", "USD"}
	_ = headers
	return out, newTitle, newHeaders, nil
}
