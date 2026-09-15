// Package mapper turns Bifrost request logs into DataHub events.
//
// The mapping is the one specified (and lab-validated against DoiT's
// ingestion validator plus a real prod ingest) in the omni spec
// specs/CloudIntelligence/bifrost-datahub-spend-integration/TECH.md §4.
package mapper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/doitintl/bifrost-datahub-exporter/internal/bifrost"
	"github.com/doitintl/bifrost-datahub-exporter/internal/datahub"
)

type FeatureSource string

const (
	FeatureFromApp FeatureSource = "app"
	FeatureNone    FeatureSource = "none"
)

type Options struct {
	Dataset         string
	FeatureSource   FeatureSource
	EmitTraceLabels bool
}

// LogToEvent maps one finalized gateway log row to one event. Rows still in
// "processing" are skipped (they finalize asynchronously and are picked up
// by the next cycle's lookback); error rows are exported with zero cost so
// failed-request counts stay reportable.
func LogToEvent(r bifrost.Log, o Options) (datahub.Event, bool, error) {
	if r.Status != "success" && r.Status != "error" {
		return datahub.Event{}, false, nil
	}

	if r.ID == "" {
		return datahub.Event{}, false, fmt.Errorf("log row without id")
	}

	ts, err := time.Parse(time.RFC3339Nano, r.Timestamp)
	if err != nil {
		return datahub.Event{}, false, fmt.Errorf("log row %s: timestamp: %w", r.ID, err)
	}

	dims := []datahub.Dimension{
		{Key: "service_description", Type: "fixed", Value: r.Provider},
		{Key: "sku_description", Type: "fixed", Value: r.Model},
		{Key: "provider", Type: "label", Value: r.Provider},
		{Key: "model", Type: "label", Value: r.Model},
		{Key: "virtual_key", Type: "label", Value: coalesce(r.VirtualKeyName, r.VirtualKeyID)},
		{Key: "team", Type: "label", Value: coalesce(r.TeamName, r.TeamID)},
		{Key: "customer", Type: "label", Value: coalesce(r.CustomerName, r.CustomerID)},
		{Key: "business_unit", Type: "label", Value: coalesce(r.BusinessUnitName, r.BusinessUnitID)},
		{Key: "bifrost_project", Type: "label", Value: coalesce(r.ProjectName, r.ProjectID)},
		{Key: "feature", Type: "label", Value: feature(r, o)},
		{Key: "cost_basis", Type: "system_label", Value: "estimated"},
		{Key: "bifrost/object", Type: "system_label", Value: r.Object},
	}

	if r.Status == "error" {
		dims = append(dims, datahub.Dimension{Key: "bifrost/status", Type: "system_label", Value: "error"})
	}

	if r.FallbackIndex > 0 {
		dims = append(dims, datahub.Dimension{Key: "bifrost/fallback_index", Type: "system_label", Value: fmt.Sprint(r.FallbackIndex)})
	}

	dims = append(dims, genaiDimensions(
		r.Model,
		coalesce(r.UserName, r.UserID),
		"",
		r.VirtualKeyName,
		feature(r, o),
	)...)

	if o.EmitTraceLabels {
		dims = append(dims,
			datahub.Dimension{Key: "request_id", Type: "label", Value: r.ID},
			datahub.Dimension{Key: "parent_trace_id", Type: "label", Value: r.ParentRequestID},
			datahub.Dimension{Key: "session_id", Type: "label", Value: r.SessionID},
		)
	}

	metrics := []datahub.Metric{
		{Type: "cost", Value: deref(r.Cost)},
	}

	if tu := r.TokenUsage; tu != nil {
		metrics = append(metrics,
			datahub.Metric{Type: "usage", Value: tu.TotalTokens},
			datahub.Metric{Type: "Prompt Tokens", Value: tu.PromptTokens},
			datahub.Metric{Type: "Completion Tokens", Value: tu.CompletionTokens},
		)

		// The per-direction split lives in token_usage.cost (transports
		// >= v2.0.0); the row-level cost_breakdown is unreliable there
		// (observed reporting input_cost = total on v2.1.1).
		if c := tu.Cost; c != nil && c.InputCost != nil && c.OutputCost != nil {
			metrics = append(metrics,
				datahub.Metric{Type: "Input Cost", Value: *c.InputCost},
				datahub.Metric{Type: "Output Cost", Value: *c.OutputCost},
			)

			if additional := deref(r.Cost) - *c.InputCost - *c.OutputCost; additional > 1e-12 {
				metrics = append(metrics, datahub.Metric{Type: "Additional Cost", Value: additional})
			}
		}
	} else {
		metrics = append(metrics, datahub.Metric{Type: "usage", Value: 0})
	}

	return datahub.Event{
		Provider:   o.Dataset,
		ID:         "bifrost-" + r.ID,
		Time:       ts.UTC().Format("2006-01-02T15:04:05.000000Z"),
		Dimensions: dropEmpty(dims),
		Metrics:    metrics,
	}, true, nil
}

type dailyKey struct {
	date, provider, model, vk, team, customer string
}

type dailyCell struct {
	vkName, teamName, customerName  string
	cost, prompt, completion, total float64
	inputCost, outputCost           float64
	hasSplit                        bool
	requests, failed                float64
}

// DailyToEvents aggregates finalized rows client-side into one event per
// (UTC day x provider x model x virtual key x team x customer). Bifrost has
// no server-side aggregate export API, so daily mode trades DataHub row
// volume for reading every log row anyway.
func DailyToEvents(rows []bifrost.Log, o Options) []datahub.Event {
	cells := map[dailyKey]*dailyCell{}

	for _, r := range rows {
		if r.Status != "success" && r.Status != "error" {
			continue
		}

		ts, err := time.Parse(time.RFC3339Nano, r.Timestamp)
		if err != nil {
			continue
		}

		k := dailyKey{ts.UTC().Format("2006-01-02"), r.Provider, r.Model, coalesce(r.VirtualKeyID, r.VirtualKeyName), r.TeamID, r.CustomerID}

		cell, ok := cells[k]
		if !ok {
			cell = &dailyCell{vkName: coalesce(r.VirtualKeyName, r.VirtualKeyID), teamName: coalesce(r.TeamName, r.TeamID), customerName: coalesce(r.CustomerName, r.CustomerID)}
			cells[k] = cell
		}

		cell.requests++
		cell.cost += deref(r.Cost)

		if r.Status == "error" {
			cell.failed++
		}

		if tu := r.TokenUsage; tu != nil {
			cell.prompt += tu.PromptTokens
			cell.completion += tu.CompletionTokens
			cell.total += tu.TotalTokens

			if c := tu.Cost; c != nil && c.InputCost != nil && c.OutputCost != nil {
				cell.inputCost += *c.InputCost
				cell.outputCost += *c.OutputCost
				cell.hasSplit = true
			}
		}
	}

	keys := make([]dailyKey, 0, len(cells))
	for k := range cells {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j])
	})

	events := make([]datahub.Event, 0, len(keys))

	for _, k := range keys {
		cell := cells[k]
		sum := sha256.Sum256([]byte(k.date + "|" + k.provider + "|" + k.model + "|" + k.vk + "|" + k.team + "|" + k.customer))

		dims := []datahub.Dimension{
			{Key: "service_description", Type: "fixed", Value: k.provider},
			{Key: "sku_description", Type: "fixed", Value: k.model},
			{Key: "provider", Type: "label", Value: k.provider},
			{Key: "model", Type: "label", Value: k.model},
			{Key: "virtual_key", Type: "label", Value: cell.vkName},
			{Key: "team", Type: "label", Value: cell.teamName},
			{Key: "customer", Type: "label", Value: cell.customerName},
			{Key: "cost_basis", Type: "system_label", Value: "estimated"},
		}

		dims = append(dims, genaiDimensions(k.model, "", "", cell.vkName, "")...)

		metrics := []datahub.Metric{
			{Type: "cost", Value: cell.cost},
			{Type: "usage", Value: cell.total},
			{Type: "Prompt Tokens", Value: cell.prompt},
			{Type: "Completion Tokens", Value: cell.completion},
			{Type: "Requests", Value: cell.requests},
			{Type: "Failed Requests", Value: cell.failed},
		}

		if cell.hasSplit {
			metrics = append(metrics,
				datahub.Metric{Type: "Input Cost", Value: cell.inputCost},
				datahub.Metric{Type: "Output Cost", Value: cell.outputCost},
			)
		}

		events = append(events, datahub.Event{
			Provider:   o.Dataset,
			ID:         "bifrost-daily-" + hex.EncodeToString(sum[:])[:32],
			Time:       k.date + "T00:00:00Z",
			Dimensions: dropEmpty(dims),
			Metrics:    metrics,
		})
	}

	return events
}

// feature maps Bifrost's app attribution to the feature label. "Other" is
// the gateway's bucket for unrecognized user agents and carries no signal;
// customers register their own apps via the gateway's user-agent mappings.
func feature(r bifrost.Log, o Options) string {
	if o.FeatureSource != FeatureFromApp {
		return ""
	}

	if r.App == "Other" {
		return ""
	}

	return r.App
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}

	return *f
}

func dropEmpty(dims []datahub.Dimension) []datahub.Dimension {
	out := dims[:0]

	for _, d := range dims {
		if d.Value != "" {
			out = append(out, d)
		}
	}

	return out
}
