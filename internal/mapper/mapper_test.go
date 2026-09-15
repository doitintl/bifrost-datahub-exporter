package mapper

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/doitintl/bifrost-datahub-exporter/internal/bifrost"
	"github.com/doitintl/bifrost-datahub-exporter/internal/datahub"
)

// Captured from a live Bifrost transports/v2.1.1 GET /api/logs row (spike
// lab, 2026-09-15; mock upstream priced by the real catalog). The content
// fields (input_history, content_summary, ...) are intentionally present to
// prove the allowlist decode drops them.
const liveLogRow = `{
  "id": "d2ff438a-e903-43b5-a97c-f71057352e2b",
  "parent_request_id": null,
  "timestamp": "2026-09-14T23:03:55.873261635Z",
  "object": "chat_completion",
  "provider": "openai",
  "model": "gpt-4o-mini",
  "number_of_retries": 0,
  "fallback_index": 0,
  "selected_key_id": "key-mock-openai",
  "selected_key_name": "mock-openai",
  "virtual_key_id": "vk-growth",
  "virtual_key_name": "advice-chat-prod",
  "user_id": null,
  "user_name": null,
  "team_id": "team-growth",
  "team_name": "growth",
  "customer_id": "cust-acme",
  "customer_name": "acme-corp",
  "business_unit_id": null,
  "business_unit_name": null,
  "project_id": null,
  "project_name": null,
  "user_agent": "curl/8.7.1",
  "app": "Other",
  "latency": 58.76175,
  "upstream_latency": 45.310708,
  "overhead_latency": 13.451042,
  "cost": 0.000135,
  "cost_breakdown": {"input_cost": 0.000135, "total_cost": 0.000135},
  "status": "success",
  "stream": false,
  "content_summary": "SECRET PROMPT mock reply SECRET COMPLETION",
  "input_history": [{"role": "user", "content": "SECRET PROMPT"}],
  "token_usage": {
    "prompt_tokens": 100,
    "completion_tokens": 200,
    "total_tokens": 300,
    "cost": {
      "input_cost": 0.000015,
      "input_cost_details": {"text_cost": 0.000015},
      "output_cost": 0.00012,
      "output_cost_details": {"text_cost": 0.00012},
      "total_cost": 0.000135
    }
  }
}`

func opts() Options {
	return Options{Dataset: "Bifrost", FeatureSource: FeatureFromApp}
}

func decodeLiveRow(t *testing.T) bifrost.Log {
	t.Helper()

	var row bifrost.Log
	if err := json.Unmarshal([]byte(liveLogRow), &row); err != nil {
		t.Fatal(err)
	}

	return row
}

func labels(e datahub.Event) map[string]string {
	out := map[string]string{}
	for _, d := range e.Dimensions {
		out[d.Type+"/"+d.Key] = d.Value
	}

	return out
}

func metricsByType(e datahub.Event) map[string]float64 {
	out := map[string]float64{}
	for _, m := range e.Metrics {
		out[m.Type] = m.Value
	}

	return out
}

func TestLogToEventMapsLiveRow(t *testing.T) {
	event, ok, err := LogToEvent(decodeLiveRow(t), opts())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}

	if event.ID != "bifrost-d2ff438a-e903-43b5-a97c-f71057352e2b" {
		t.Errorf("id: %s", event.ID)
	}

	if event.Time != "2026-09-14T23:03:55.873261Z" {
		t.Errorf("time: %s", event.Time)
	}

	l := labels(event)
	for key, want := range map[string]string{
		"fixed/service_description":       "openai",
		"fixed/sku_description":           "gpt-4o-mini",
		"label/provider":                  "openai",
		"label/model":                     "gpt-4o-mini",
		"label/virtual_key":               "advice-chat-prod",
		"label/team":                      "growth",
		"label/customer":                  "acme-corp",
		"system_label/cost_basis":         "estimated",
		"system_label/bifrost/object":     "chat_completion",
		"system_label/genai/genai_spend":  "true",
		"system_label/genai/model":        "gpt-4o-mini",
		"system_label/genai/model_family": "GPT",
		"system_label/genai/api_key_name": "advice-chat-prod",
	} {
		if l[key] != want {
			t.Errorf("%s = %q, want %q", key, l[key], want)
		}
	}

	for _, absent := range []string{"label/feature", "label/business_unit", "label/bifrost_project", "label/request_id", "system_label/bifrost/status", "system_label/genai/user_email"} {
		if v, present := l[absent]; present {
			t.Errorf("%s should be absent, got %q", absent, v)
		}
	}

	m := metricsByType(event)
	for typ, want := range map[string]float64{
		"cost":              0.000135,
		"usage":             300,
		"Prompt Tokens":     100,
		"Completion Tokens": 200,
		"Input Cost":        0.000015,
		"Output Cost":       0.00012,
	} {
		if m[typ] != want {
			t.Errorf("metric %s = %v, want %v", typ, m[typ], want)
		}
	}

	if _, present := m["Additional Cost"]; present {
		t.Error("Additional Cost should be absent when the split sums to the total")
	}
}

func TestLogToEventNeverLeaksContent(t *testing.T) {
	event, _, err := LogToEvent(decodeLiveRow(t), Options{Dataset: "Bifrost", FeatureSource: FeatureFromApp, EmitTraceLabels: true})
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(payload), "SECRET") {
		t.Fatalf("prompt content leaked into the outbound event: %s", payload)
	}
}

func TestLogToEventSkipsProcessingRows(t *testing.T) {
	row := decodeLiveRow(t)
	row.Status = "processing"

	if _, ok, err := LogToEvent(row, opts()); ok || err != nil {
		t.Fatalf("processing row must be skipped without error, ok=%v err=%v", ok, err)
	}
}

func TestLogToEventErrorRow(t *testing.T) {
	row := decodeLiveRow(t)
	row.Status = "error"
	row.Cost = nil
	row.TokenUsage = nil

	event, ok, err := LogToEvent(row, opts())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}

	if labels(event)["system_label/bifrost/status"] != "error" {
		t.Error("error row must carry bifrost/status=error")
	}

	if m := metricsByType(event); m["cost"] != 0 {
		t.Errorf("error row cost = %v, want 0", m["cost"])
	}
}

func TestLogToEventFallbackChild(t *testing.T) {
	row := decodeLiveRow(t)
	row.FallbackIndex = 1
	row.ParentRequestID = "root-req-1"

	event, _, err := LogToEvent(row, Options{Dataset: "Bifrost", FeatureSource: FeatureFromApp, EmitTraceLabels: true})
	if err != nil {
		t.Fatal(err)
	}

	l := labels(event)
	if l["system_label/bifrost/fallback_index"] != "1" {
		t.Errorf("fallback_index label: %q", l["system_label/bifrost/fallback_index"])
	}

	if l["label/parent_trace_id"] != "root-req-1" {
		t.Errorf("parent_trace_id: %q", l["label/parent_trace_id"])
	}
}

func TestFeatureSource(t *testing.T) {
	row := decodeLiveRow(t)
	row.App = "claude-code"

	event, _, _ := LogToEvent(row, opts())
	if labels(event)["label/feature"] != "claude-code" {
		t.Error("recognized app must map to the feature label")
	}

	event, _, _ = LogToEvent(row, Options{Dataset: "Bifrost", FeatureSource: FeatureNone})
	if _, present := labels(event)["label/feature"]; present {
		t.Error("FEATURE_SOURCE=none must not emit feature")
	}
}

func TestDirectVirtualKeyRow(t *testing.T) {
	row := decodeLiveRow(t)
	row.TeamID, row.TeamName = "", ""
	row.VirtualKeyName = ""

	event, _, err := LogToEvent(row, opts())
	if err != nil {
		t.Fatal(err)
	}

	l := labels(event)
	if _, present := l["label/team"]; present {
		t.Error("direct-customer VK row must not emit a team label")
	}

	if l["label/virtual_key"] != "vk-growth" {
		t.Errorf("virtual_key must fall back to the id, got %q", l["label/virtual_key"])
	}
}

func TestDailyAggregation(t *testing.T) {
	rows := make([]bifrost.Log, 0, 4)

	for i := 0; i < 3; i++ {
		rows = append(rows, decodeLiveRow(t))
	}

	errRow := decodeLiveRow(t)
	errRow.Status = "error"
	errRow.Cost = nil
	errRow.TokenUsage = nil
	rows = append(rows, errRow)

	processing := decodeLiveRow(t)
	processing.Status = "processing"
	rows = append(rows, processing)

	events := DailyToEvents(rows, opts())
	if len(events) != 1 {
		t.Fatalf("expected 1 daily cell, got %d", len(events))
	}

	m := metricsByType(events[0])
	for typ, want := range map[string]float64{
		"cost":            0.000405,
		"usage":           900,
		"Requests":        4,
		"Failed Requests": 1,
		"Input Cost":      0.000045,
	} {
		if diff := m[typ] - want; diff > 1e-12 || diff < -1e-12 {
			t.Errorf("daily metric %s = %v, want %v", typ, m[typ], want)
		}
	}

	if events[0].Time != "2026-09-14T00:00:00Z" {
		t.Errorf("daily time: %s", events[0].Time)
	}

	rerun := DailyToEvents(rows, opts())
	if rerun[0].ID != events[0].ID {
		t.Error("daily ids must be deterministic across runs")
	}

	if !strings.HasPrefix(events[0].ID, "bifrost-daily-") {
		t.Errorf("daily id prefix: %s", events[0].ID)
	}
}
