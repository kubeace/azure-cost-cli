package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubeace/azure-cost-cli/internal/azure"
	"github.com/kubeace/azure-cost-cli/internal/render"
	"github.com/kubeace/azure-cost-cli/internal/snapshot"
	"github.com/kubeace/azure-cost-cli/internal/trends"
)

// queryDaily fetches Daily-granularity rows for one sub and reshapes them into
// label-keyed time series. Group is the dimension to break down on.
func queryDaily(ctx context.Context, c *azure.Client, sub string, from, to time.Time, group string) (map[string][]trends.DayPoint, error) {
	tf, f, t := customOrMTD(from, to)
	rows, err := c.Query(ctx, azure.QueryOptions{
		Scope: subScope(sub), Timeframe: tf, From: f, To: t,
		Granularity: "Daily",
		GroupBy:     []string{group},
	})
	if err != nil {
		return nil, err
	}
	// UsageDate comes back as a number like 20260518.
	out := map[string][]trends.DayPoint{}
	for _, r := range rows {
		date := parseAzureDate(r.Float("UsageDate"))
		if date.IsZero() {
			continue
		}
		label := r.String(group)
		if label == "" {
			label = "(unattributed)"
		}
		out[label] = append(out[label], trends.DayPoint{Date: date, INR: r.Float("Cost")})
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].Date.Before(out[k][j].Date) })
	}
	return out, nil
}

// parseAzureDate handles Cost Management's UsageDate format (an int like
// 20260518 representing 2026-05-18).
func parseAzureDate(f float64) time.Time {
	n := int(f)
	if n < 19700101 || n > 99991231 {
		return time.Time{}
	}
	y, m, d := n/10000, (n/100)%100, n%100
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}

// ===== daily =====

func newDailyCmd() *cobra.Command {
	var group string
	c := &cobra.Command{
		Use:   "daily",
		Short: "Per-day cost breakdown with sparkline (one subscription)",
		Long: `Queries Cost Management with daily granularity, groups by ServiceName
(or --group <dim>), and prints one row per series with a 30-day spark.

Example:
  azcost daily --sub <id> --top 15
  azcost daily --sub <id> --group ResourceGroupName --top 10`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.sub == "" {
				return fmt.Errorf("--sub required")
			}
			top := g.top
			if top == 0 {
				top = 15
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
			series, err := queryDaily(ctx, cl, g.sub, from, to, group)
			if err != nil {
				return err
			}

			// Build sorted slice by total INR descending
			type entry struct {
				label string
				total float64
				days  []trends.DayPoint
			}
			entries := make([]entry, 0, len(series))
			for label, days := range series {
				var tot float64
				for _, d := range days {
					tot += d.INR
				}
				entries = append(entries, entry{label, tot, days})
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].total > entries[j].total })
			if len(entries) > top {
				entries = entries[:top]
			}

			fmt.Printf("Daily %s for %s (%s → %s)\n", group, g.sub, from.Format("2006-01-02"), to.Format("2006-01-02"))
			fmt.Println(strings.Repeat("=", 60))
			fmt.Printf("%-40s  %12s  %12s  %s\n", group, "Total ₹", "Avg/day ₹", "Trend")
			for _, e := range entries {
				values := make([]float64, len(e.days))
				for i, d := range e.days {
					values[i] = d.INR
				}
				avg := e.total
				if len(e.days) > 0 {
					avg /= float64(len(e.days))
				}
				fmt.Printf("%-40s  %12.0f  %12.0f  %s\n",
					trim(e.label, 40), e.total, avg, render.Sparkline(values))
			}
			return nil
		},
	}
	c.Flags().StringVar(&group, "group", "ServiceName", "groupBy dimension (ServiceName, ResourceGroupName, ResourceId, MeterCategory)")
	return c
}

// ===== diff =====

func newDiffCmd() *cobra.Command {
	var (
		baseFrom, baseTo string
		group            string
	)
	c := &cobra.Command{
		Use:   "diff",
		Short: "Compare costs between two windows (top movers by INR delta)",
		Long: `Two windows. Default: current MTD vs same length window immediately before
it. Override with --base-from / --base-to.

Examples:
  azcost diff --sub <id>                                # this MTD vs prior MTD-length window
  azcost diff --sub <id> --from 2026-05-01 --to 2026-05-17 --base-from 2026-04-01 --base-to 2026-04-17
  azcost diff --sub <id> --group ResourceGroupName --top 10`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.sub == "" {
				return fmt.Errorf("--sub required")
			}
			ctx, cancel := ctxWithSignal()
			defer cancel()
			cl, err := newClient()
			if err != nil {
				return err
			}
			aFrom, aTo, err := parseRange(g.from, g.to)
			if err != nil {
				return err
			}
			// Default base = same-length window immediately before aFrom
			bFrom, bTo, err := resolveBaseWindow(baseFrom, baseTo, aFrom, aTo)
			if err != nil {
				return err
			}

			top := g.top
			if top == 0 {
				top = 15
			}

			afterMap, err := queryGrouped(ctx, cl, g.sub, aFrom, aTo, group)
			if err != nil {
				return fmt.Errorf("after window: %w", err)
			}
			beforeMap, err := queryGrouped(ctx, cl, g.sub, bFrom, bTo, group)
			if err != nil {
				return fmt.Errorf("before window: %w", err)
			}
			movers := trends.Diff(beforeMap, afterMap)

			var totBefore, totAfter float64
			for _, v := range beforeMap {
				totBefore += v
			}
			for _, v := range afterMap {
				totAfter += v
			}

			fmt.Printf("Diff by %s for %s\n", group, g.sub)
			fmt.Printf("  Before: %s → %s  ₹%.0f\n", bFrom.Format("2006-01-02"), bTo.Format("2006-01-02"), totBefore)
			fmt.Printf("  After:  %s → %s  ₹%.0f\n", aFrom.Format("2006-01-02"), aTo.Format("2006-01-02"), totAfter)
			delta := totAfter - totBefore
			fmt.Printf("  Delta:  ₹%+.0f (%+.1f%%)\n\n", delta, safePct(totBefore, totAfter))

			n := top
			if n > len(movers) {
				n = len(movers)
			}
			fmt.Printf("%-40s  %12s  %12s  %12s  %10s\n", group, "Before ₹", "After ₹", "Δ ₹", "Δ %")
			for i := 0; i < n; i++ {
				m := movers[i]
				pct := fmt.Sprintf("%+.1f%%", m.PctChange)
				if math.IsInf(m.PctChange, 1) {
					pct = "new"
				} else if m.After == 0 && m.Before > 0 {
					pct = "removed"
				}
				fmt.Printf("%-40s  %12.0f  %12.0f  %+12.0f  %10s\n",
					trim(m.Label, 40), m.Before, m.After, m.Delta, pct)
			}
			return nil
		},
	}
	c.Flags().StringVar(&group, "group", "ServiceName", "groupBy dimension")
	c.Flags().StringVar(&baseFrom, "base-from", "", "base window start (default: same length before --from)")
	c.Flags().StringVar(&baseTo, "base-to", "", "base window end (default: day before --from)")
	return c
}

func resolveBaseWindow(baseFrom, baseTo string, aFrom, aTo time.Time) (time.Time, time.Time, error) {
	if baseFrom != "" || baseTo != "" {
		f, err := time.Parse("2006-01-02", baseFrom)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("base-from: %w", err)
		}
		t, err := time.Parse("2006-01-02", baseTo)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("base-to: %w", err)
		}
		return f, t, nil
	}
	length := aTo.Sub(aFrom)
	bTo := aFrom.Add(-time.Hour * 24)
	bFrom := bTo.Add(-length)
	return bFrom, bTo, nil
}

func safePct(before, after float64) float64 {
	if before == 0 {
		if after == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return (after - before) / before * 100
}

// queryGrouped returns a label->total map for one sub+window using normal
// (non-daily) granularity — much cheaper than daily for diff use.
func queryGrouped(ctx context.Context, c *azure.Client, sub string, from, to time.Time, group string) (map[string]float64, error) {
	tf, f, t := customOrMTD(from, to)
	if tf == "MonthToDate" && (from != time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC) || to.Day() != time.Now().UTC().Day()) {
		tf, f, t = "Custom", from, to
	}
	rows, err := c.Query(ctx, azure.QueryOptions{
		Scope: subScope(sub), Timeframe: tf, From: f, To: t,
		GroupBy: []string{group},
	})
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, r := range rows {
		label := r.String(group)
		if label == "" {
			label = "(unattributed)"
		}
		out[label] += r.Float("Cost")
	}
	return out, nil
}

// ===== anomaly =====

func newAnomalyCmd() *cobra.Command {
	var (
		group   string
		ratio   float64
		minAbs  float64
		minDays int
		days    int
	)
	c := &cobra.Command{
		Use:   "anomaly",
		Short: "Flag services whose latest day cost exceeds a rolling baseline",
		Long: `Pulls --days of daily-granularity cost for the subscription and reports
any series where the most-recent day is >= --ratio × the prior-day rolling
average over --min-days.

Defaults: 14 days of history, 7-day rolling avg, ratio 1.5×, latest > ₹500.
Tune --min-abs to suppress noisy tiny services.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.sub == "" {
				return fmt.Errorf("--sub required")
			}
			ctx, cancel := ctxWithSignal()
			defer cancel()
			cl, err := newClient()
			if err != nil {
				return err
			}
			to := time.Now().UTC()
			from := to.AddDate(0, 0, -days+1)
			from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
			seriesMap, err := queryDaily(ctx, cl, g.sub, from, to, group)
			if err != nil {
				return err
			}

			series := make([]trends.DailySeries, 0, len(seriesMap))
			for label, pts := range seriesMap {
				series = append(series, trends.DailySeries{Label: label, Days: pts})
			}

			anomalies := trends.DetectAnomalies(series, ratio, minAbs, minDays)

			fmt.Printf("Anomaly scan for %s — last %d days, ratio≥%.1fx, baseline=%dd rolling\n",
				g.sub, days, ratio, minDays)
			fmt.Println(strings.Repeat("=", 80))
			if len(anomalies) == 0 {
				fmt.Println("(no anomalies)")
				return nil
			}
			fmt.Printf("%-40s  %10s  %12s  %12s  %8s\n", group, "Latest", "Latest ₹", "Baseline ₹", "Ratio")
			for _, a := range anomalies {
				fmt.Printf("%-40s  %10s  %12.0f  %12.0f  %7.2fx\n",
					trim(a.Label, 40), a.Latest.Format("2006-01-02"), a.LatestV, a.Baseline, a.Ratio)
			}
			return nil
		},
	}
	c.Flags().StringVar(&group, "group", "ServiceName", "groupBy dimension")
	c.Flags().Float64Var(&ratio, "ratio", 1.5, "flag when latest day >= ratio × baseline")
	c.Flags().Float64Var(&minAbs, "min-abs", 500, "suppress series whose latest day < this INR amount")
	c.Flags().IntVar(&minDays, "min-days", 7, "rolling baseline window length")
	c.Flags().IntVar(&days, "days", 14, "history window in days")
	return c
}

// ===== snapshot save/list (utility commands) =====

func newSnapshotCmd() *cobra.Command {
	c := &cobra.Command{Use: "snapshot", Short: "Save/list cost snapshots under ~/.cache/azcost"}
	c.AddCommand(newSnapshotSaveCmd(), newSnapshotListCmd())
	return c
}

func newSnapshotSaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save",
		Short: "Capture per-sub service+RG+resource costs for the current window into a snapshot",
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

			var svc, rgs, res []render.CostRow
			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, s := range subList {
				s := s
				wg.Add(1)
				go func() {
					defer wg.Done()
					sRows, _ := cl.Query(ctx, azure.QueryOptions{Scope: subScope(s), Timeframe: tf, From: f, To: t, GroupBy: []string{"ServiceName"}})
					rRows, _ := cl.Query(ctx, azure.QueryOptions{Scope: subScope(s), Timeframe: tf, From: f, To: t, GroupBy: []string{"ResourceGroupName"}})
					resRows, _ := cl.Query(ctx, azure.QueryOptions{Scope: subScope(s), Timeframe: tf, From: f, To: t, GroupBy: []string{"ResourceId", "ResourceType"}})
					mu.Lock()
					defer mu.Unlock()
					for _, x := range sRows {
						svc = append(svc, render.CostRow{Label: x.String("ServiceName"), Sub: s, INR: x.Float("Cost")})
					}
					for _, x := range rRows {
						rgs = append(rgs, render.CostRow{Label: x.String("ResourceGroupName"), Sub: s, INR: x.Float("Cost")})
					}
					for _, x := range resRows {
						res = append(res, render.CostRow{
							Label: render.ShortResource(x.String("ResourceId")),
							Sub:   s, Extra: x.String("ResourceType"),
							INR: x.Float("Cost"),
						})
					}
				}()
			}
			wg.Wait()

			snap := snapshot.Snapshot{
				CapturedAt: time.Now().UTC(),
				WindowFrom: from, WindowTo: to,
				Subs:      subList,
				Services:  render.Aggregate(svc, func(r render.CostRow) string { return r.Label }),
				RGs:       render.Aggregate(rgs, func(r render.CostRow) string { return r.Label }),
				Resources: render.Aggregate(res, func(r render.CostRow) string { return r.Label }),
			}
			path, err := snapshot.Save(snapshot.DefaultDir(), snap)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "wrote %s (%d services, %d RGs, %d resources)\n",
				path, len(snap.Services), len(snap.RGs), len(snap.Resources))
			return nil
		},
	}
}

func newSnapshotListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List snapshot dates under ~/.cache/azcost",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dates, err := snapshot.List(snapshot.DefaultDir())
			if err != nil {
				return err
			}
			if len(dates) == 0 {
				fmt.Println("(no snapshots)")
				return nil
			}
			for _, d := range dates {
				fmt.Println(d.Format("2006-01-02"))
			}
			return nil
		},
	}
}
