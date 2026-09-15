// Package metrics exposes exporter counters in Prometheus text format
// without pulling in the Prometheus client library.
package metrics

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

var (
	RowsRead        atomic.Int64
	EventsPushed    atomic.Int64
	Cycles          atomic.Int64
	CycleErrors     atomic.Int64
	Quarantined     atomic.Int64
	lastSuccessUnix atomic.Int64
)

func SetLastSuccess(t time.Time) { lastSuccessUnix.Store(t.Unix()) }

func Serve(addr string) {
	if addr == "" {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")

		var lag float64
		if last := lastSuccessUnix.Load(); last > 0 {
			lag = time.Since(time.Unix(last, 0)).Seconds()
		}

		fmt.Fprintf(w, "# TYPE bifrost_exporter_rows_read_total counter\nbifrost_exporter_rows_read_total %d\n", RowsRead.Load())
		fmt.Fprintf(w, "# TYPE bifrost_exporter_events_pushed_total counter\nbifrost_exporter_events_pushed_total %d\n", EventsPushed.Load())
		fmt.Fprintf(w, "# TYPE bifrost_exporter_cycles_total counter\nbifrost_exporter_cycles_total %d\n", Cycles.Load())
		fmt.Fprintf(w, "# TYPE bifrost_exporter_cycle_errors_total counter\nbifrost_exporter_cycle_errors_total %d\n", CycleErrors.Load())
		fmt.Fprintf(w, "# TYPE bifrost_exporter_events_quarantined_total counter\nbifrost_exporter_events_quarantined_total %d\n", Quarantined.Load())
		fmt.Fprintf(w, "# TYPE bifrost_exporter_last_success_lag_seconds gauge\nbifrost_exporter_last_success_lag_seconds %f\n", lag)
	})

	go func() {
		server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		_ = server.ListenAndServe()
	}()
}
