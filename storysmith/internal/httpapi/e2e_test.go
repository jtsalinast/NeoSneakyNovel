// End-to-end test of the full workflow against a mock Ollama-compatible
// server: create project -> set novel parameters -> generate outline ->
// confirm -> write chapter (streamed) -> read it back. Also verifies the
// embedded SPA is served at "/" and API routes still win over the fallback.
package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"storysmith/internal/config"
)

// startMockLLM mimics POST /v1/chat/completions for both streaming and
// non-streaming calls, plus GET /api/tags (Ollama native model list).
func startMockLLM(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"name":"mock-llm:1b"},{"name":"qwen2.5:14b"}]}`)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		all := body.Messages[len(body.Messages)-1].Content + "\n" + body.Messages[0].Content
		isOutline := strings.Contains(all, "outline") || strings.Contains(all, "大纲")

		w.Header().Set("Content-Type", "application/json")
		if !body.Stream {
			if isOutline {
				fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"title\":\"T\",\"synopsis\":\"s\",\"chapters\":[{\"index\":1,\"title\":\"Un\",\"summary\":\"a\"},{\"index\":2,\"title\":\"Dos\",\"summary\":\"b\"}]}"}}]}`)
			} else {
				fmt.Fprint(w, `{"choices":[{"message":{"content":"Capítulo completo en texto plano."}}]}`)
			}
			return
		}
		fl, _ := w.(http.Flusher)
		var chunks []string
		if isOutline {
			chunks = []string{`{"title":"T","synopsis":"s","chapters":[{"index":1,"title":"Uno","summary":"a","section":"Act 1"},{"index":2,"title":"Dos","summary":"b","section":"Act 2"}]}`}
		} else {
			chunks = []string{"Érase ", "una vez ", "un faro.", " Fin."}
		}
		for _, c := range chunks {
			payload, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": c}}}})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newTestHandlers(t *testing.T, baseURL string) (*Handlers, string) {
	t.Helper()
	dir := t.TempDir()
	apiCfg := config.DefaultAPIConfig()
	apiCfg.BaseURL = baseURL
	apiCfg.Model = "mock-llm:1b"
	apiCfg.MaxTokens = 4096
	apiCfg.ContextBudgetTokens = 32768
	apiCfg.HTTPTimeoutSeconds = 30
	h := NewHandlersWithVersion(apiCfg, filepath.Join(dir, "api.json"), dir, "test")
	return h, dir
}

func doJSON(t *testing.T, h *Handlers, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr = strings.NewReader("")
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	withWebUI(buildMuxForTest(h)).ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func buildMuxForTest(h *Handlers) *http.ServeMux {
	mux := newTestMux(h)
	return mux
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

func TestE2EFullWorkflow(t *testing.T) {
	llmSrv, hits := startMockLLM(t)
	h, _ := newTestHandlers(t, llmSrv.URL+"/v1")

	// --- SPA served at "/" ---
	rec := httptest.NewRecorder()
	withWebUI(buildMuxForTest(h)).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "StorySmith") {
		t.Fatalf("expected SPA at /, got code %d len %d", rec.Code, rec.Len())
	}
	// Unknown non-API path falls back to the SPA too.
	rec = httptest.NewRecorder()
	withWebUI(buildMuxForTest(h)).ServeHTTP(rec, httptest.NewRequest("GET", "/some/spa/route", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("SPA fallback broken: code %d", rec.Code)
	}

	// --- create project with Novel Parameters ---
	rec, out := doJSON(t, h, "POST", "/api/projects", map[string]any{
		"name": "e2e-novel",
		"story": map[string]any{
			"title": "El faro de vidrio", "genre": "Fantasy", "subgenre": "High Fantasy",
			"novel_length": "Novel (Standard)", "structure": "Three-Act Structure",
		},
	})
	if rec.Code != 200 {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}
	_ = out
	rec, out = doJSON(t, h, "GET", "/api/parameters", nil)
	story := out
	if story["genre"] != "Fantasy" || story["structure"] != "Three-Act Structure" {
		t.Fatalf("params not persisted: %+v", story)
	}
	if secs, ok := story["sections"].([]any); !ok || len(secs) != 3 {
		t.Fatalf("sections should resolve to 3 acts, got %+v", story["sections"])
	}

	// --- cascading hints endpoint ---
	rec, out = doJSON(t, h, "GET", "/api/parameters/hints?genre=Fantasy&subgenre=High+Fantasy", nil)
	if rec.Code != 200 {
		t.Fatalf("hints: %d", rec.Code)
	}
	if pt, ok := out["protagonist_types"].([]any); !ok || len(pt) == 0 {
		t.Fatalf("hints missing protagonist_types: %+v", out)
	}

	// --- generate outline (async) ---
	before := hits.Load()
	rec, _ = doJSON(t, h, "POST", "/api/outline/generate", map[string]any{"chapter_count": 2, "idea": "un faro que vaticina naufragios"})
	if rec.Code != 202 {
		t.Fatalf("outline generate: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return !h.isTaskRunning() && hits.Load() > before }, "outline task to finish")

	rec, out = doJSON(t, h, "GET", "/api/outline", nil)
	o, ok := out["outline"].(map[string]any)
	if !ok || out["exists"] != true {
		t.Fatalf("outline not found: %s", rec.Body.String())
	}
	if chs, _ := o["chapters"].([]any); len(chs) != 2 {
		t.Fatalf("want 2 chapters, got %v", o["chapters"])
	}
	if o["confirmed"] == true {
		t.Fatal("freshly generated outline must be pending confirmation")
	}

	// --- confirm outline ---
	rec, _ = doJSON(t, h, "POST", "/api/outline/confirm", map[string]any{"confirm": true})
	if rec.Code != 200 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}

	// --- write chapter 1 (streamed into content_chunk events) ---
	sub := h.logger.Subscribe()
	defer h.logger.Unsubscribe(sub)
	before = hits.Load()
	rec, _ = doJSON(t, h, "POST", "/api/chapters/write", map[string]any{"chapter": 1})
	if rec.Code != 202 {
		t.Fatalf("write: %d %s", rec.Code, rec.Body.String())
	}
	go func() {
		for range sub { // drain so the broadcaster never blocks
		}
	}()
	waitFor(t, func() bool { return !h.isTaskRunning() && hits.Load() > before }, "chapter write to finish")

	rec, out = doJSON(t, h, "GET", "/api/chapters/1", nil)
	txt, _ := out["text"].(string)
	if !strings.Contains(txt, "faro") {
		t.Fatalf("chapter text missing streamed content: %q", txt)
	}

	// --- status reflects everything ---
	rec, out = doJSON(t, h, "GET", "/api/status", nil)
	if out["project"] != "e2e-novel" {
		t.Fatalf("status project: %+v", out)
	}
	if written, _ := out["chapters_written"].([]any); len(written) != 1 {
		t.Fatalf("chapters_written: %+v", out["chapters_written"])
	}
	if out["task_running"] == true {
		t.Fatal("task should have finished")
	}
}

func TestE2EOllamaModelProxy(t *testing.T) {
	llmSrv, _ := startMockLLM(t)
	h, _ := newTestHandlers(t, llmSrv.URL+"/v1")
	rec, out := doJSON(t, h, "GET", "/api/config/api/models", nil)
	if rec.Code != 200 {
		t.Fatalf("models proxy: %d", rec.Code)
	}
	if out["ollama"] != true {
		t.Fatalf("expected ollama=true, got %+v", out)
	}
	models, _ := out["models"].([]any)
	if len(models) != 2 || models[0] != "mock-llm:1b" {
		t.Fatalf("unexpected models: %+v", out["models"])
	}
}

func TestE2EAPITestButton(t *testing.T) {
	llmSrv, _ := startMockLLM(t)
	h, _ := newTestHandlers(t, llmSrv.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := httptest.NewRequest("POST", "/api/config/api/test",
		strings.NewReader(`{"base_url":"`+llmSrv.URL+`/v1","model":"mock-llm:1b","max_tokens":1024,"context_budget_tokens":8192,"http_timeout_seconds":30}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.PostAPITest(rec, req)
	if rec.Code != 200 {
		t.Fatalf("api test: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["success"] != true {
		t.Fatalf("expected success: %+v", out)
	}
}

var _ = bufio.ScanLines // keep import if helpers evolve
