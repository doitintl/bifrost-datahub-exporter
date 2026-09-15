// mock-provider is a minimal OpenAI/Anthropic-shaped upstream for the
// integration test. Bifrost treats it as the real provider and prices the
// fixed token counts from its own catalog — real cost computation, no real
// spend. Requests whose body contains "please fail" get a 500 to exercise
// error rows and fallbacks.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := ":18742"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, model, fail := readBody(r)
		_ = body

		if fail {
			http.Error(w, `{"error":{"message":"mock upstream failure","type":"server_error"}}`, http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]any{
			"id": fmt.Sprintf("msg_mock_%d", time.Now().UnixNano()), "type": "message", "role": "assistant", "model": model,
			"content":     []map[string]string{{"type": "text", "text": "mock anthropic reply CANARY-a7f3e9"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 100, "output_tokens": 200},
		})
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		body, model, fail := readBody(r)
		_ = body

		if fail {
			http.Error(w, `{"error":{"message":"mock upstream failure","type":"server_error"}}`, http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]any{
			"id": fmt.Sprintf("chatcmpl-mock%d", time.Now().UnixNano()), "object": "chat.completion",
			"created": time.Now().Unix(), "model": model,
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]string{"role": "assistant", "content": "mock openai reply CANARY-a7f3e9"}}},
			"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 200, "total_tokens": 300},
		})
	})

	log.Printf("mock provider on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func readBody(r *http.Request) (string, string, bool) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &body)

	return string(raw), body.Model, strings.Contains(string(raw), "please fail")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
