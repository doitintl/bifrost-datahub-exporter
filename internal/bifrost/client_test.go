package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func serveLogs(t *testing.T, total int, wantAuth bool) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantAuth {
			if user, pass, ok := r.BasicAuth(); !ok || user != "admin" || pass != "secret" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
		}

		q := r.URL.Query()
		if q.Get("sort_by") != "timestamp" || q.Get("order") != "asc" {
			t.Errorf("unexpected sort params: %v", q)
		}

		if _, err := time.Parse(time.RFC3339Nano, q.Get("start_time")); err != nil {
			t.Errorf("start_time not RFC3339: %v", err)
		}

		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))

		if limit > 1000 {
			http.Error(w, `{"error":{"message":"limit cannot exceed 1000"}}`, http.StatusBadRequest)
			return
		}

		n := min(limit, total-offset)
		n = max(n, 0)

		logs := make([]Log, n)
		for i := range logs {
			logs[i] = Log{ID: fmt.Sprintf("row-%d", offset+i), Status: "success", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
		}

		resp := logsResponse{Logs: logs}
		resp.Pagination.Limit = limit
		resp.Pagination.Offset = offset
		resp.Pagination.TotalCount = total

		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestLogsWalksPages(t *testing.T) {
	srv := serveLogs(t, 2350, false)
	defer srv.Close()

	rows, err := NewClient(srv.URL, "", "").Logs(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if len(rows) != 2350 {
		t.Fatalf("rows: %d, want 2350", len(rows))
	}

	if rows[0].ID != "row-0" || rows[2349].ID != "row-2349" {
		t.Errorf("pagination order broken: first=%s last=%s", rows[0].ID, rows[2349].ID)
	}
}

func TestBasicAuth(t *testing.T) {
	srv := serveLogs(t, 1, true)
	defer srv.Close()

	if _, err := NewClient(srv.URL, "", "").Logs(context.Background(), time.Now().Add(-time.Hour), time.Now()); err == nil {
		t.Fatal("expected an auth error without credentials")
	}

	rows, err := NewClient(srv.URL, "admin", "secret").Logs(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}

func TestAllowlistDropsContentFields(t *testing.T) {
	raw := `{"id":"x","status":"success","timestamp":"2026-09-14T23:03:55Z",
	  "input_history":[{"role":"user","content":"SECRET"}],
	  "content_summary":"SECRET","raw_request":"SECRET","output_message":{"content":"SECRET"}}`

	var row Log
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}

	reencoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(reencoded), "SECRET") {
		t.Fatalf("content survived the allowlist decode: %s", reencoded)
	}
}

func TestProbeCostSplit(t *testing.T) {
	cost := 0.1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := logsResponse{Logs: []Log{{ID: "x", Status: "success", TokenUsage: &TokenUsage{Cost: &TokenCost{InputCost: &cost}}}}}
		resp.Pagination.TotalCount = 42
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	caps, err := NewClient(srv.URL, "", "").Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !caps.CostSplit || caps.TotalLogs != 42 {
		t.Fatalf("caps: %+v", caps)
	}
}
