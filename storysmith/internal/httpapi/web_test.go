package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"storysmith/internal/config"
)

func newTestServer(t *testing.T) (*Handlers, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	apiPath := filepath.Join(dir, "api.json")
	apiCfg := config.DefaultAPIConfig()
	if err := os.WriteFile(apiPath, []byte(`{"base_url":"http://localhost:11434/v1","model":"test-model"}`), 0644); err != nil {
		t.Fatal(err)
	}
	h := NewHandlersWithVersion(apiCfg, apiPath, dir, "test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/version":
			h.GetVersion(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/config/api/models"):
			h.GetOllamaModels(w, r)
		case r.URL.Path == "/api/config/api/test":
			h.PostAPITest(w, r)
		case r.URL.Path == "/api/config/api":
			switch r.Method {
			case "GET":
				h.GetAPIConfig(w, r)
			case "PUT":
				h.PutAPIConfig(w, r)
			}
		case r.URL.Path == "/api/parameters/options":
			h.GetParameterOptions(w, r)
		case r.URL.Path == "/api/parameters":
			switch r.Method {
			case "GET":
				h.GetParameters(w, r)
			case "PUT":
				h.PutParameters(w, r)
			}
		case r.URL.Path == "/api/projects":
			switch r.Method {
			case "GET":
				h.GetProjects(w, r)
			case "POST":
				h.PostProject(w, r)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	return h, srv, dir
}

func httpDoJSON(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func TestParameterOptionsCatalogue(t *testing.T) {
	_, srv, _ := newTestServer(t)
	defer srv.Close()
	resp, data := httpDoJSON(t, "GET", srv.URL+"/api/parameters/options", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var opts map[string]any
	json.Unmarshal(data, &opts)
	for _, key := range []string{"genres", "subgenres", "length_options", "structure_map", "default_structure", "structure_sections", "gender_bias"} {
		if _, ok := opts[key]; !ok {
			t.Errorf("options missing key %q", key)
		}
	}
	if g, ok := opts["genres"].([]any); !ok || len(g) != 8 {
		t.Errorf("expected 8 genres, got %v", opts["genres"])
	}
}

func TestProjectCreateAndParametersRoundTrip(t *testing.T) {
	_, srv, _ := newTestServer(t)
	defer srv.Close()

	body := map[string]any{
		"name": "demo",
		"story": map[string]any{
			"genre": "Sci-Fi", "subgenre": "Cyberpunk",
			"novel_length": "Novella", "structure": "3-Act Structure",
			"title": "Neon", "target_words_per_chapter": 4000,
			"tone": "Cínico", "darkness_level": 4,
		},
	}
	resp, data := httpDoJSON(t, "POST", srv.URL+"/api/projects", body)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}

	resp, data = httpDoJSON(t, "GET", srv.URL+"/api/parameters", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get params: %d %s", resp.StatusCode, data)
	}
	var story map[string]any
	json.Unmarshal(data, &story)
	if story["genre"] != "Sci-Fi" || story["subgenre"] != "Cyberpunk" {
		t.Errorf("params not persisted: %s", data)
	}
	if secs, ok := story["sections"].([]any); !ok || len(secs) != 3 {
		t.Errorf("expected 3 sections for 3-Act, got %v", story["sections"])
	}

	// Update structure to Hero's Journey (Simplified) (valid for Novella).
	resp, data = httpDoJSON(t, "PUT", srv.URL+"/api/parameters", map[string]any{"structure": "Hero's Journey (Simplified)"})
	if resp.StatusCode != 200 {
		t.Fatalf("put params: %d %s", resp.StatusCode, data)
	}
	json.Unmarshal(data, &story)
	if story["structure"] != "Hero's Journey (Simplified)" {
		t.Errorf("structure not updated: %s", data)
	}

	// Invalid genre rejected.
	resp, data = httpDoJSON(t, "PUT", srv.URL+"/api/parameters", map[string]any{"genre": "Space-Western"})
	if resp.StatusCode != 400 {
		t.Errorf("invalid genre should be 400, got %d %s", resp.StatusCode, data)
	}

	// Structure invalid for the project's length rejected (Freytag is Short Story only).
	resp, data = httpDoJSON(t, "PUT", srv.URL+"/api/parameters", map[string]any{"structure": "Freytag's Pyramid"})
	if resp.StatusCode != 400 {
		t.Errorf("mismatched structure should be 400, got %d %s", resp.StatusCode, data)
	}
}

func TestAPIConfigMaskingAndKeyPreservation(t *testing.T) {
	_, srv, dir := newTestServer(t)
	defer srv.Close()

	// Save a config with a real key.
	resp, data := httpDoJSON(t, "PUT", srv.URL+"/api/config/api", map[string]any{
		"base_url": "http://localhost:11434/v1", "model": "qwen2.5:14b",
		"api_key": "sk-supersecret-1234567890", "temperature": 0.8,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("put api cfg: %d %s", resp.StatusCode, data)
	}
	if strings.Contains(string(data), "supersecret") {
		t.Errorf("response leaked the API key: %s", data)
	}

	// GET returns masked key.
	resp, data = httpDoJSON(t, "GET", srv.URL+"/api/config/api", nil)
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	if k, _ := cfg["api_key"].(string); !strings.Contains(k, "...") {
		t.Errorf("expected masked key, got %q", k)
	}

	// PUT echoing the masked value keeps the stored key.
	httpDoJSON(t, "PUT", srv.URL+"/api/config/api", map[string]any{
		"base_url": "http://localhost:11434/v1", "model": "llama3.1",
		"api_key": cfg["api_key"], "temperature": 0.9,
	})
	_, data2 := httpDoJSON(t, "GET", srv.URL+"/api/config/api", nil)
	var cfg2 map[string]any
	json.Unmarshal(data2, &cfg2)
	if k, _ := cfg2["api_key"].(string); k != cfg["api_key"] {
		t.Errorf("masked echo changed stored key: %q vs %q", k, cfg["api_key"])
	}

	// The on-disk file must still hold the REAL key.
	raw, err := os.ReadFile(filepath.Join(dir, "api.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "sk-supersecret-1234567890") {
		t.Errorf("real key not persisted on disk: %s", raw)
	}
}

func TestGetOllamaModelsOfflineIsGraceful(t *testing.T) {
	_, srv, _ := newTestServer(t)
	defer srv.Close()
	// Connection refused → must return empty list, not an error.
	resp, data := httpDoJSON(t, "GET", srv.URL+"/api/config/api/models?base_url=http://localhost:1/v1", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("offline models should degrade gracefully, got %d %s", resp.StatusCode, data)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if m["ollama"] != false {
		t.Errorf("expected ollama=false, got %v", m)
	}
}

func TestParametersRequireProject(t *testing.T) {
	_, srv, _ := newTestServer(t)
	defer srv.Close()
	resp, data := httpDoJSON(t, "GET", srv.URL+"/api/parameters", nil)
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 select-project error, got %d %s", resp.StatusCode, data)
	}
}
