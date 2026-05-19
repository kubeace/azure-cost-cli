package azure

import (
	"context"
	"fmt"
	"time"
)

// Cost Management /query API.
//
// API surface: POST /<scope>/providers/Microsoft.CostManagement/query?api-version=2023-11-01
//
// Scope can be a subscription (/subscriptions/<id>), resource group
// (/subscriptions/<id>/resourceGroups/<rg>), billing account, or enrollment.
// Per-subscription scope is preferred — see ARCHITECTURE.md for why.
//
// Responses come back as columns + rows: a [][]any where each row is indexed
// by the column list. Row.Float/String adapt this awkward shape to typed
// accessors. Pagination is via nextLink (URLs returned in the response body);
// Query() follows them transparently.
const costAPIVersion = "2023-11-01"

type queryBody struct {
	Type       string      `json:"type"`
	Timeframe  string      `json:"timeframe"`
	TimePeriod *timePeriod `json:"timePeriod,omitempty"`
	Dataset    dataset     `json:"dataset"`
}

type timePeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type dataset struct {
	Granularity string                 `json:"granularity"`
	Aggregation map[string]aggregation `json:"aggregation"`
	Grouping    []grouping             `json:"grouping,omitempty"`
	Filter      *filter                `json:"filter,omitempty"`
}

type aggregation struct {
	Name     string `json:"name"`
	Function string `json:"function"`
}

type grouping struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type filter struct {
	Dimensions *dimFilter `json:"dimensions,omitempty"`
	And        []filter   `json:"and,omitempty"`
}

type dimFilter struct {
	Name     string   `json:"name"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type queryResponse struct {
	Properties struct {
		NextLink string   `json:"nextLink"`
		Columns  []column `json:"columns"`
		Rows     [][]any  `json:"rows"`
	} `json:"properties"`
}

type column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Row is a typed accessor over a [][]any result row using the column index.
type Row struct {
	cols []column
	vals []any
}

func (r Row) Float(name string) float64 {
	for i, c := range r.cols {
		if c.Name == name {
			if v, ok := r.vals[i].(float64); ok {
				return v
			}
		}
	}
	return 0
}

func (r Row) String(name string) string {
	for i, c := range r.cols {
		if c.Name == name {
			if v, ok := r.vals[i].(string); ok {
				return v
			}
		}
	}
	return ""
}

// QueryOptions builds a Cost Management query. Scope determines the URL prefix.
type QueryOptions struct {
	Scope          string // e.g. "subscriptions/<id>" or "providers/Microsoft.Billing/billingAccounts/<id>"
	CostType       string // "ActualCost" (default) or "AmortizedCost"
	Timeframe      string // "MonthToDate" (default) or "Custom"
	From, To       time.Time
	Granularity    string // "None" (default) or "Daily" — daily adds a UsageDate column
	GroupBy        []string
	FilterService  string   // single ServiceName filter
	FilterResIDs   []string // ResourceId IN (...)
	FilterMeterCat string   // MeterCategory single value
}

func (c *Client) Query(ctx context.Context, opts QueryOptions) ([]Row, error) {
	if opts.Scope == "" {
		return nil, fmt.Errorf("scope required")
	}
	if opts.CostType == "" {
		opts.CostType = "ActualCost"
	}
	if opts.Timeframe == "" {
		opts.Timeframe = "MonthToDate"
	}

	gran := opts.Granularity
	if gran == "" {
		gran = "None"
	}
	body := queryBody{
		Type:      opts.CostType,
		Timeframe: opts.Timeframe,
		Dataset: dataset{
			Granularity: gran,
			Aggregation: map[string]aggregation{
				"totalCost": {Name: "Cost", Function: "Sum"},
			},
		},
	}
	if opts.Timeframe == "Custom" {
		body.TimePeriod = &timePeriod{
			From: opts.From.UTC().Format(time.RFC3339),
			To:   opts.To.UTC().Format(time.RFC3339),
		}
	}
	for _, g := range opts.GroupBy {
		body.Dataset.Grouping = append(body.Dataset.Grouping, grouping{Type: "Dimension", Name: g})
	}
	var filters []filter
	if opts.FilterService != "" {
		filters = append(filters, filter{Dimensions: &dimFilter{Name: "ServiceName", Operator: "In", Values: []string{opts.FilterService}}})
	}
	if len(opts.FilterResIDs) > 0 {
		filters = append(filters, filter{Dimensions: &dimFilter{Name: "ResourceId", Operator: "In", Values: opts.FilterResIDs}})
	}
	if opts.FilterMeterCat != "" {
		filters = append(filters, filter{Dimensions: &dimFilter{Name: "MeterCategory", Operator: "In", Values: []string{opts.FilterMeterCat}}})
	}
	if len(filters) == 1 {
		body.Dataset.Filter = &filters[0]
	} else if len(filters) > 1 {
		body.Dataset.Filter = &filter{And: filters}
	}

	url := fmt.Sprintf("https://management.azure.com/%s/providers/Microsoft.CostManagement/query?api-version=%s",
		opts.Scope, costAPIVersion)

	var all []Row
	for {
		var resp queryResponse
		if err := c.Post(ctx, url, body, &resp); err != nil {
			return nil, err
		}
		for _, raw := range resp.Properties.Rows {
			all = append(all, Row{cols: resp.Properties.Columns, vals: raw})
		}
		if resp.Properties.NextLink == "" {
			break
		}
		url = resp.Properties.NextLink
	}
	return all, nil
}
