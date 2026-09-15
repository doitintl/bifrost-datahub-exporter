// Package bifrost reads request logs from a Bifrost gateway's HTTP API.
//
// The source is GET /api/logs — the same documented management API the
// gateway's own dashboard polls (OpenAPI: docs/openapi/paths/management/
// logging.yaml in maximhq/bifrost). Contract pinned against transports
// v2.0.0 and v2.1.1 (2026-09-15): limit is capped at 1000, pagination is
// limit/offset, sorting is sort_by/order, time bounds are start_time/
// end_time in RFC3339, and rows are written asynchronously a few seconds
// after the response — the caller's lookback window absorbs both the late
// writes and later status transitions.
//
// Privacy contract: Log below is a strict field allowlist. Rows also carry
// input_history, content_summary, output_message, params, tools, raw
// request/response and similar content fields; they have no struct fields
// here, so they are dropped at JSON decode time and can never be
// serialized onward.
package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const pageLimit = 1000

type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

func NewClient(baseURL, username, password string) *Client {
	return &Client{baseURL: baseURL, username: username, password: password, http: &http.Client{Timeout: 120 * time.Second}}
}

type TokenCost struct {
	InputCost  *float64 `json:"input_cost"`
	OutputCost *float64 `json:"output_cost"`
	TotalCost  *float64 `json:"total_cost"`
}

type TokenUsage struct {
	PromptTokens     float64    `json:"prompt_tokens"`
	CompletionTokens float64    `json:"completion_tokens"`
	TotalTokens      float64    `json:"total_tokens"`
	Cost             *TokenCost `json:"cost"`
}

type Log struct {
	ID               string      `json:"id"`
	ParentRequestID  string      `json:"parent_request_id"`
	Timestamp        string      `json:"timestamp"`
	Object           string      `json:"object"`
	Provider         string      `json:"provider"`
	Model            string      `json:"model"`
	Status           string      `json:"status"`
	Stream           bool        `json:"stream"`
	FallbackIndex    int         `json:"fallback_index"`
	VirtualKeyID     string      `json:"virtual_key_id"`
	VirtualKeyName   string      `json:"virtual_key_name"`
	TeamID           string      `json:"team_id"`
	TeamName         string      `json:"team_name"`
	CustomerID       string      `json:"customer_id"`
	CustomerName     string      `json:"customer_name"`
	UserID           string      `json:"user_id"`
	UserName         string      `json:"user_name"`
	BusinessUnitID   string      `json:"business_unit_id"`
	BusinessUnitName string      `json:"business_unit_name"`
	ProjectID        string      `json:"project_id"`
	ProjectName      string      `json:"project_name"`
	SessionID        string      `json:"session_id"`
	App              string      `json:"app"`
	Cost             *float64    `json:"cost"`
	TokenUsage       *TokenUsage `json:"token_usage"`
}

type logsResponse struct {
	Logs       []Log `json:"logs"`
	Pagination struct {
		Limit      int `json:"limit"`
		Offset     int `json:"offset"`
		TotalCount int `json:"total_count"`
	} `json:"pagination"`
}

// Logs walks GET /api/logs from `from` to `to`, oldest first, page by page.
func (c *Client) Logs(ctx context.Context, from, to time.Time) ([]Log, error) {
	var all []Log

	for offset := 0; ; {
		q := url.Values{
			"limit":      {fmt.Sprint(pageLimit)},
			"offset":     {fmt.Sprint(offset)},
			"sort_by":    {"timestamp"},
			"order":      {"asc"},
			"start_time": {from.UTC().Format(time.RFC3339Nano)},
			"end_time":   {to.UTC().Format(time.RFC3339Nano)},
		}

		var resp logsResponse
		if err := c.get(ctx, "/api/logs?"+q.Encode(), &resp); err != nil {
			return nil, err
		}

		all = append(all, resp.Logs...)
		offset += len(resp.Logs)

		if len(resp.Logs) < pageLimit || offset >= resp.Pagination.TotalCount {
			return all, nil
		}
	}
}

type Capabilities struct {
	// CostSplit is true when log rows carry token_usage.cost — the
	// per-direction cost breakdown introduced with transports v2.0.0.
	CostSplit bool
	// AuthRequired reports whether the gateway rejected an unauthenticated
	// probe (governance admin auth is enabled and no credentials were given).
	TotalLogs int
}

// Probe fetches a page of recent rows to verify reachability, credentials,
// and whether the gateway serves the v2 cost split (error rows carry no
// token_usage, so a single row is not enough). An empty logstore is fine.
func (c *Client) Probe(ctx context.Context) (Capabilities, error) {
	var resp logsResponse
	if err := c.get(ctx, "/api/logs?limit=25&status=success", &resp); err != nil {
		return Capabilities{}, err
	}

	caps := Capabilities{TotalLogs: resp.Pagination.TotalCount}

	for _, l := range resp.Logs {
		if l.TokenUsage != nil && l.TokenUsage.Cost != nil {
			caps.CostSplit = true
			break
		}
	}

	return caps, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}

	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bifrost GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return fmt.Errorf("bifrost GET %s: read: %w", path, err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("bifrost GET %s: HTTP %d — the gateway requires admin credentials (governance.auth_config); set BIFROST_ADMIN_USERNAME/BIFROST_ADMIN_PASSWORD", path, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bifrost GET %s: HTTP %d: %.300s", path, resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("bifrost GET %s: decode: %w", path, err)
	}

	return nil
}
