package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"storysmith/internal/config"
)

// newMockOpenAIServer emulates Ollama's OpenAI-compatible endpoint:
// streaming chat completions + a model list.
func newMockOpenAIServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "Hola desde el mock"}}},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{"Hola ", "desde ", "el ", "mock"}
		for _, c := range chunks {
			payload := map[string]any{
				"choices": []map[string]any{{"delta": map[string]string{"content": c}}},
			}
			b, _ := json.Marshal(payload)
			w.Write([]byte("data: " + string(b) + "\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "qwen2.5:14b"}, {"id": "llama3.1:8b"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func mockCfg(srv *httptest.Server) *config.APIConfig {
	return &config.APIConfig{
		BaseURL:             srv.URL + "/v1",
		Model:               "qwen2.5:14b",
		HTTPTimeoutSeconds:  10,
		ContextBudgetTokens: 131072,
	}
}

func TestCallAPIMessagesNonStreaming(t *testing.T) {
	srv := newMockOpenAIServer(t)
	out, err := CallAPIMessages(context.Background(), mockCfg(srv), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Hola desde el mock") {
		t.Errorf("unexpected reply: %q", out)
	}
}

func TestCallAPIStreamMessagesAggregatesDeltas(t *testing.T) {
	srv := newMockOpenAIServer(t)
	var got strings.Builder
	res, err := CallAPIStreamMessages(context.Background(), mockCfg(srv), []Message{{Role: "user", Content: "hi"}}, func(delta string) {
		got.WriteString(delta)
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = res
	if got.String() != "Hola desde el mock" {
		t.Errorf("streamed text mismatch: %q", got.String())
	}
}

func TestStreamCancellationStopsEarly(t *testing.T) {
	srv := newMockOpenAIServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	_, err := CallAPIStreamMessages(ctx, mockCfg(srv), []Message{{Role: "user", Content: "hi"}}, func(string) {
		n++
		if n == 2 {
			cancel()
		}
	})
	if err != nil && ctx.Err() == nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
