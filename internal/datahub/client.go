// Package datahub pushes events to the DoiT DataHub ingestion API.
//
// Contract (verified against the DoiT implementation): max 50,000 events per
// request, max 255 metrics per event, event ids are the idempotency key with
// read-time last-write-wins dedup, validation is all-or-nothing per batch,
// and the API is fronted by Cloudflare which rejects generic User-Agents.
package datahub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"
)

type Dimension struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Metric struct {
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

type Event struct {
	Provider   string      `json:"provider"`
	ID         string      `json:"id"`
	Time       string      `json:"time"`
	Dimensions []Dimension `json:"dimensions"`
	Metrics    []Metric    `json:"metrics"`
}

type Client struct {
	baseURL   string
	apiKey    string
	userAgent string
	http      *http.Client
	log       *slog.Logger

	Quarantined int
}

func NewClient(baseURL, apiKey, userAgent string, log *slog.Logger) *Client {
	return &Client{
		baseURL:   baseURL,
		apiKey:    apiKey,
		userAgent: userAgent,
		http:      &http.Client{Timeout: 120 * time.Second},
		log:       log,
	}
}

// EnsureDataset creates the dataset with a description and preset logo
// unless it already has a metadata document. Create is NOT idempotent: the
// first POST on a dataset succeeds (even when events already exist), any
// later POST fails with a plain 500 — so existence is checked first, and
// the description is effectively immutable through the public API once set.
// logoName requires DoiT-side support (doiteng/omni#62391); servers without
// it ignore the field.
func (c *Client) EnsureDataset(ctx context.Context, name, description, logoName string) error {
	status, _, err := c.do(ctx, http.MethodGet, "/datahub/v1/datasets/"+url.PathEscape(name), nil)
	if err != nil {
		return fmt.Errorf("ensure dataset: get: %w", err)
	}

	if status == http.StatusOK {
		return nil
	}

	payload := map[string]string{"name": name, "description": description}
	if logoName != "" {
		payload["logoName"] = logoName
	}

	body, _ := json.Marshal(payload)

	status, respBody, err := c.post(ctx, "/datahub/v1/datasets", body)
	if err != nil {
		return err
	}

	if status != http.StatusCreated && status != http.StatusOK {
		if recheck, _, rerr := c.do(ctx, http.MethodGet, "/datahub/v1/datasets/"+url.PathEscape(name), nil); rerr == nil && recheck == http.StatusOK {
			return nil
		}

		return fmt.Errorf("ensure dataset: HTTP %d: %.300s", status, respBody)
	}

	return nil
}

// Push sends events in chunks of at most maxBatch, deduplicating ids within
// each request (a duplicate id inside one batch rejects the whole batch
// server-side). 429/5xx responses are retried with backoff; a 400 bisects
// the chunk to isolate and quarantine offending events.
func (c *Client) Push(ctx context.Context, events []Event, maxBatch int) error {
	events = dedupeByID(events)

	for start := 0; start < len(events); start += maxBatch {
		end := min(start+maxBatch, len(events))
		if err := c.pushChunk(ctx, events[start:end]); err != nil {
			return err
		}
	}

	return nil
}

func (c *Client) pushChunk(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	body, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		return err
	}

	var (
		status   int
		respBody string
	)

	for attempt := 0; ; attempt++ {
		status, respBody, err = c.post(ctx, "/datahub/v1/events", body)
		if err == nil && status < 500 && status != http.StatusTooManyRequests {
			break
		}

		if attempt >= 5 {
			if err != nil {
				return fmt.Errorf("push events: %w", err)
			}

			return fmt.Errorf("push events: HTTP %d after %d attempts: %.300s", status, attempt+1, respBody)
		}

		delay := time.Duration(1<<attempt)*time.Second + time.Duration(rand.IntN(1000))*time.Millisecond
		c.log.Warn("push retry", "attempt", attempt+1, "status", status, "err", err, "delay", delay)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	switch {
	case status == http.StatusCreated:
		return nil
	case status == http.StatusBadRequest && len(events) > 1:
		mid := len(events) / 2
		if err := c.pushChunk(ctx, events[:mid]); err != nil {
			return err
		}

		return c.pushChunk(ctx, events[mid:])
	case status == http.StatusBadRequest:
		c.Quarantined++
		c.log.Error("event rejected by validation, quarantined", "id", events[0].ID, "response", respBody)

		return nil
	default:
		return fmt.Errorf("push events: HTTP %d: %.300s", status, respBody)
	}
}

func (c *Client) post(ctx context.Context, path string, body []byte) (int, string, error) {
	return c.do(ctx, http.MethodPost, path, body)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (int, string, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, "", err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, "", err
	}

	return resp.StatusCode, string(respBody), nil
}

func dedupeByID(events []Event) []Event {
	seen := make(map[string]struct{}, len(events))
	out := events[:0]

	for _, e := range events {
		if _, dup := seen[e.ID]; dup {
			continue
		}

		seen[e.ID] = struct{}{}
		out = append(out, e)
	}

	return out
}
