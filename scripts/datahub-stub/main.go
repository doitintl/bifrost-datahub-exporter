// datahub-stub is a minimal stand-in for the DoiT DataHub API used by the
// integration test: it accepts event batches, enforces the contract limits
// the real API enforces (batch size, provider pattern, within-batch id
// uniqueness), and reports what it received on GET /received.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
)

var providerPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+( [a-zA-Z0-9_-]+)*$`)

func main() {
	addr := ":8181"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	var (
		mu       sync.Mutex
		eventIDs = map[string]int{}
		datasets = map[string]bool{}
		rawBody  []byte
	)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /datahub/v1/datasets/{name}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if !datasets[r.PathValue("name")] {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"name": r.PathValue("name")})
	})

	mux.HandleFunc("POST /datahub/v1/datasets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		defer mu.Unlock()

		if datasets[body.Name] {
			http.Error(w, `{"error":"Failed to create dataset"}`, http.StatusInternalServerError)
			return
		}

		datasets[body.Name] = true
		w.WriteHeader(http.StatusCreated)
	})

	mux.HandleFunc("POST /datahub/v1/events", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		var body struct {
			Events []struct {
				Provider string `json:"provider"`
				ID       string `json:"id"`
				Metrics  []any  `json:"metrics"`
			} `json:"events"`
		}

		if err := json.Unmarshal(raw, &body); err != nil || len(body.Events) == 0 || len(body.Events) > 50000 {
			http.Error(w, `{"error":"invalid batch"}`, http.StatusBadRequest)
			return
		}

		seen := map[string]bool{}

		for _, e := range body.Events {
			if !providerPattern.MatchString(e.Provider) || seen[e.ID] || len(e.Metrics) > 255 {
				http.Error(w, `{"error":"validation"}`, http.StatusBadRequest)
				return
			}

			seen[e.ID] = true
		}

		mu.Lock()
		for _, e := range body.Events {
			eventIDs[e.ID]++
		}
		rawBody = append(rawBody, raw...)
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":"Ingestion success"}`))
	})

	mux.HandleFunc("GET /received", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{"unique_events": len(eventIDs)})
	})

	mux.HandleFunc("GET /received-full", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		_, _ = w.Write(rawBody)
	})

	log.Fatal(http.ListenAndServe(addr, mux))
}
