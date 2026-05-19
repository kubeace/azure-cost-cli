package azure

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// Azure Monitor /metrics API.
//
// API: GET <resourceID>/providers/microsoft.insights/metrics
//   ?api-version=2021-05-01
//   &metricnames=<metric>
//   &aggregation=Total|Average|...
//   &interval=P1D|PT1H|...
//   &timespan=<start>/<end>
//   &$filter=<Dimension> eq '*'    # to split by that dimension
//
// Used by `azcost cogsvc` to pull per-deployment token counts and cache
// hit rates. Cost Management can only attribute cost at the Cogsvc account
// level — per-deployment cost is *allocated* from the meter cost by token
// share in the caller.
//
// Quirks worth remembering:
//   - ProcessedPromptTokens supports only these split dims: ApiName,
//     ModelDeploymentName, FeatureName, UsageChannel, Region, ModelVersion.
//     A two-dim split (e.g. ModelDeploymentName + ModelName) returns 400.
//   - There is no ProcessedCompletionTokens metric. Use GeneratedTokens.
//   - Cache% is averaged across only the days where Average > 0 — days with
//     zero cache match would otherwise dilute the average misleadingly low.

type metricsResponse struct {
	Value []struct {
		Name struct {
			Value string `json:"value"`
		} `json:"name"`
		Timeseries []struct {
			MetadataValues []struct {
				Name struct {
					Value string `json:"value"`
				} `json:"name"`
				Value string `json:"value"`
			} `json:"metadatavalues"`
			Data []struct {
				TimeStamp string  `json:"timeStamp"`
				Total     float64 `json:"total"`
				Average   float64 `json:"average"`
			} `json:"data"`
		} `json:"timeseries"`
	} `json:"value"`
}

// MetricSeries is a single (dimension-tuple, totals) result.
type MetricSeries struct {
	Dims  map[string]string
	Total float64
	Avg   float64
}

// MetricOptions describes a single Azure Monitor metric query split by one dimension.
type MetricOptions struct {
	ResourceID  string
	MetricName  string
	Aggregation string // "Total" or "Average"
	From, To    time.Time
	Interval    string // ISO8601, e.g. "P1D"
	SplitBy     string // dimension name, e.g. "ModelDeploymentName"
}

func (c *Client) Metric(ctx context.Context, opts MetricOptions) ([]MetricSeries, error) {
	if opts.Aggregation == "" {
		opts.Aggregation = "Total"
	}
	if opts.Interval == "" {
		opts.Interval = "P1D"
	}
	q := url.Values{}
	q.Set("api-version", "2021-05-01")
	q.Set("metricnames", opts.MetricName)
	q.Set("aggregation", opts.Aggregation)
	q.Set("interval", opts.Interval)
	q.Set("timespan", fmt.Sprintf("%s/%s",
		opts.From.UTC().Format("2006-01-02T15:04:05Z"),
		opts.To.UTC().Format("2006-01-02T15:04:05Z")))
	if opts.SplitBy != "" {
		q.Set("$filter", fmt.Sprintf("%s eq '*'", opts.SplitBy))
	}
	u := fmt.Sprintf("https://management.azure.com%s/providers/microsoft.insights/metrics?%s",
		opts.ResourceID, q.Encode())

	var resp metricsResponse
	if err := c.Get(ctx, u, &resp); err != nil {
		return nil, err
	}
	if len(resp.Value) == 0 {
		return nil, nil
	}
	var out []MetricSeries
	for _, ts := range resp.Value[0].Timeseries {
		dims := map[string]string{}
		for _, m := range ts.MetadataValues {
			dims[m.Name.Value] = m.Value
		}
		var total, sumAvg float64
		nAvg := 0
		for _, d := range ts.Data {
			total += d.Total
			if d.Average > 0 {
				sumAvg += d.Average
				nAvg++
			}
		}
		avg := 0.0
		if nAvg > 0 {
			avg = sumAvg / float64(nAvg)
		}
		out = append(out, MetricSeries{Dims: dims, Total: total, Avg: avg})
	}
	return out, nil
}
