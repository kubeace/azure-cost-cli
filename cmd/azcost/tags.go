package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubeace/azure-cost-cli/internal/azure"
	"github.com/kubeace/azure-cost-cli/internal/render"
)

// newTagsCmd: cross-subscription MTD cost grouped by a tag key from the local
// tag-map. Pulls per-resource costs from each sub in parallel, then re-
// aggregates by the chosen tag value.
func newTagsCmd() *cobra.Command {
	var key string
	c := &cobra.Command{
		Use:   "tags",
		Short: "MTD cost grouped by a tag key (uses local tag-map)",
		Long: `Tags re-aggregates per-resource cost across subscriptions by a tag value
declared in the local tagmap file (default ~/.config/azcost-tags.yaml).

Useful for answering "what does team=ai cost?" without first tagging every
resource in Azure. Resources matching no rule are bucketed under "<unset>".`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if key == "" {
				return fmt.Errorf("--key required (e.g. --key team)")
			}
			f, err := loadTagmap()
			if err != nil {
				return fmt.Errorf("load tagmap: %w", err)
			}
			if f == nil {
				return fmt.Errorf("no tagmap at %s — create one or pass --tagmap", g.tagmap)
			}

			subs, ctx, cancel, cl, from, to, tf, fT, tT, err := setupMulti()
			if err != nil {
				return err
			}
			defer cancel()

			rows := fanoutResources(ctx, cl, subs, tf, fT, tT)
			grouped := f.GroupBy(rows, key)
			title := fmt.Sprintf("MTD cost by tag/%s (%s → %s)",
				key, from.Format("2006-01-02"), to.Format("2006-01-02"))
			return render.Write(os.Stdout, grouped,
				renderOpts(title, []string{"#", key, "Resources", "INR", "USD"}, g.top, render.FormatTable))
		},
	}
	c.Flags().StringVar(&key, "key", "", "tag key to group by (required)")
	return c
}

// newTagCoverageCmd: report on what fraction of spend is matched by at least
// one tagmap rule. Lists the largest untagged resources so rule-writing
// effort can target the highest-impact gaps first.
func newTagCoverageCmd() *cobra.Command {
	var topN int
	c := &cobra.Command{
		Use:   "tag-coverage",
		Short: "Classified vs unclassified spend; lists largest untagged resources",
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := loadTagmap()
			if err != nil {
				return fmt.Errorf("load tagmap: %w", err)
			}
			if f == nil {
				return fmt.Errorf("no tagmap at %s", g.tagmap)
			}

			subs, ctx, cancel, cl, _, _, tf, fT, tT, err := setupMulti()
			if err != nil {
				return err
			}
			defer cancel()

			rows := fanoutResources(ctx, cl, subs, tf, fT, tT)
			cov := f.Coverage(rows)
			total := cov.CoveredINR + cov.UncoveredINR
			pct := 0.0
			if total > 0 {
				pct = cov.CoveredINR / total * 100
			}
			fmt.Printf("Coverage: %d/%d resources, ₹%.0f / ₹%.0f (%.1f%%)\n\n",
				cov.Covered, cov.Covered+cov.Uncovered, cov.CoveredINR, total, pct)
			if len(cov.Untagged) == 0 {
				return nil
			}
			return render.Write(os.Stdout, cov.Untagged,
				renderOpts(fmt.Sprintf("Largest %d untagged resources", topN),
					[]string{"#", "Resource", "Type", "INR", "USD"}, topN, render.FormatTable))
		},
	}
	c.Flags().IntVar(&topN, "top-untagged", 25, "rows to list under untagged")
	return c
}

// setupMulti resolves subscription list + context + client + time window
// shared by the multi-sub commands. Caller must defer cancel().
func setupMulti() (
	subs []string, ctx context.Context, cancel context.CancelFunc,
	cl *azure.Client, from, to time.Time, tf string, fT, tT time.Time, err error,
) {
	subs = g.subs
	if len(subs) == 0 {
		subs = discoverSubs()
	}
	if len(subs) == 0 {
		err = fmt.Errorf("no subscriptions; pass --subs or run 'az login'")
		return
	}
	ctx, cancel = ctxWithSignal()
	cl, err = newClient()
	if err != nil {
		cancel()
		return
	}
	from, to, err = parseRange(g.from, g.to)
	if err != nil {
		cancel()
		return
	}
	tf, fT, tT = customOrMTD(from, to)
	return
}

// fanoutResources fans out per-resource queries across the given subs and
// flattens results into a single []CostRow with short labels.
func fanoutResources(ctx context.Context, cl *azure.Client, subs []string, tf string, f, t time.Time) []render.CostRow {
	type result struct {
		rows []azure.Row
		err  error
		sub  string
	}
	ch := make(chan result, len(subs))
	var wg sync.WaitGroup
	for _, s := range subs {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := cl.Query(ctx, azure.QueryOptions{
				Scope: subScope(s), Timeframe: tf, From: f, To: t,
				GroupBy: []string{"ResourceId", "ResourceType"},
			})
			ch <- result{rows: rows, err: err, sub: s}
		}()
	}
	wg.Wait()
	close(ch)

	var out []render.CostRow
	for r := range ch {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "tags: sub %s: %v\n", r.sub, r.err)
			continue
		}
		for _, x := range r.rows {
			out = append(out, render.CostRow{
				Label: render.ShortResource(x.String("ResourceId")),
				Sub:   r.sub,
				Extra: x.String("ResourceType"),
				INR:   x.Float("Cost"),
			})
		}
	}
	return out
}
