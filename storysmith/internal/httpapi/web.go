// Package httpapi wires the local web server. Route groups:
//   - /api/config/api*   → API configuration tab (base URL, model, test)
//   - /api/parameters*   → Novel Parameters tab (genre catalogue + per-project params)
//   - /api/projects*     → project management
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"storysmith/internal/config"
	"storysmith/internal/fsutil"
	"storysmith/internal/genres"
	"storysmith/internal/i18n"
	"storysmith/internal/llm"
	"storysmith/internal/sse"
	"storysmith/internal/story"
	"storysmith/internal/task"
)

type Handlers struct {
	apiCfg     *config.APIConfig
	apiCfgPath string
	logger     *sse.LogBroadcaster
	version    string

	progDir     string
	projectName string
	projectMu   sync.RWMutex

	cfg     *config.Config
	cfgPath string

	runner *task.Runner

	taskMu       sync.Mutex
	taskRunning  bool
	lastTaskName string
}

func (h *Handlers) setTaskRunning(running bool, name string) {
	h.taskMu.Lock()
	h.taskRunning = running
	if running {
		h.lastTaskName = name
	}
	h.taskMu.Unlock()
}

func NewHandlers(apiCfg *config.APIConfig, apiCfgPath string, logger *sse.LogBroadcaster, progDir, version string) *Handlers {
	return &Handlers{
		apiCfg:     apiCfg,
		apiCfgPath: apiCfgPath,
		logger:     logger,
		version:    version,
		progDir:    progDir,
		cfg:        config.DefaultConfig(),
		runner:     task.NewRunner(),
	}
}

// NewHandlersWithVersion builds a Handlers with its own log broadcaster.
func NewHandlersWithVersion(apiCfg *config.APIConfig, apiCfgPath, progDir, version string) *Handlers {
	return NewHandlers(apiCfg, apiCfgPath, sse.NewLogBroadcaster(), progDir, version)
}

// SelectProjectSilent restores a project at startup without HTTP plumbing.
func (h *Handlers) SelectProjectSilent(name string) error { return h.switchProject(name) }

func (h *Handlers) storysDir() string { return filepath.Join(h.progDir, "storys") }

func (h *Handlers) writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (h *Handlers) writeErrorReq(w http.ResponseWriter, r *http.Request, code int, key string, args ...any) {
	lang := i18n.FromRequest(r)
	h.writeJSON(w, code, map[string]interface{}{"error": i18n.T(lang, key, args...)})
}

func (h *Handlers) isTaskRunning() bool {
	h.taskMu.Lock()
	defer h.taskMu.Unlock()
	return h.taskRunning
}

// ---------------------------------------------------------------------------
// Server bootstrap
// ---------------------------------------------------------------------------

// StartWebServer keeps the original signature for callers that build the
// config themselves.
func StartWebServer(apiCfg *config.APIConfig, apiCfgPath, port, progDir, version string) {
	StartWebServerWithHandlers(NewHandlersWithVersion(apiCfg, apiCfgPath, progDir, version), port, "http://localhost"+port)
}

// StartWebServerWithHandlers serves an already-built handler set (used by
// main.go so it can restore the last project before listening).
func StartWebServerWithHandlers(h *Handlers, port, url string) {
	mux := http.NewServeMux()

	// Version / events
	mux.HandleFunc("GET /api/version", h.GetVersion)
	mux.HandleFunc("GET /api/events", h.SSEHandler)

	// --- Tab: API Configuration (split out of the old settings page) ---
	mux.HandleFunc("GET /api/config/api", h.GetAPIConfig)
	mux.HandleFunc("PUT /api/config/api", h.PutAPIConfig)
	mux.HandleFunc("POST /api/config/api/test", h.PostAPITest)
	mux.HandleFunc("GET /api/config/api/models", h.GetOllamaModels)

	// --- Tab: Novel Parameters (NovelWriter integration) ---
	mux.HandleFunc("GET /api/parameters/options", h.GetParameterOptions)
	mux.HandleFunc("GET /api/parameters", h.GetParameters)
	mux.HandleFunc("PUT /api/parameters", h.PutParameters)

	// --- Generation workflow (outline / chapters) ---
	mux.HandleFunc("GET /api/status", h.GetStatus)
	mux.HandleFunc("POST /api/task/cancel", h.PostTaskCancel)
	mux.HandleFunc("POST /api/outline/generate", h.PostOutlineGenerate)
	mux.HandleFunc("GET /api/outline", h.GetOutline)
	mux.HandleFunc("POST /api/outline/confirm", h.PostOutlineConfirm)
	mux.HandleFunc("POST /api/chapters/write", h.PostChapterWrite)
	mux.HandleFunc("GET /api/chapters", h.GetChapters)
	mux.HandleFunc("GET /api/chapters/{n}", h.GetChapterN)

	// --- Projects ---
	mux.HandleFunc("GET /api/projects", h.GetProjects)
	mux.HandleFunc("POST /api/projects", h.PostProject)
	mux.HandleFunc("GET /api/projects/current", h.GetProjectCurrent)
	mux.HandleFunc("POST /api/projects/select", h.PostProjectSelect)
	mux.HandleFunc("DELETE /api/projects/{name}", h.DeleteProject)

	handler := recoveryMiddleware(corsMiddleware(loggingMiddleware(mux)))

	srv := &http.Server{
		Addr:         port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	fmt.Printf(" [system] StorySmith web UI starting...\n")
	fmt.Printf(" [system] URL: %s\n", url)
	fmt.Printf(" [system] Data dir: %s\n", h.progDir)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, " [error] server failed: %v\n", err)
		os.Exit(1)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-UI-Locale, Accept-Language")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" {
			start := time.Now()
			next.ServeHTTP(w, r)
			fmt.Printf(" %s %s (%v)\n", r.Method, r.URL.Path, time.Since(start))
		} else {
			next.ServeHTTP(w, r)
		}
	})
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				fmt.Printf("[PANIC] %s %s: %v\n", r.Method, r.URL.Path, err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// ---------------------------------------------------------------------------
// Misc
// ---------------------------------------------------------------------------

func (h *Handlers) GetVersion(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]string{"version": h.version})
}

func (h *Handlers) SSEHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := h.logger.Subscribe()
	defer h.logger.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(sse.Format(msg)); err != nil {
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Tab: API Configuration
// ---------------------------------------------------------------------------

func (h *Handlers) GetAPIConfig(w http.ResponseWriter, r *http.Request) {
	h.taskMu.Lock()
	cfg := *h.apiCfg
	h.taskMu.Unlock()
	// Never leak the key to the browser beyond a masked hint.
	cfg.APIKey = maskKey(cfg.APIKey)
	h.writeJSON(w, http.StatusOK, cfg)
}

func maskKey(k string) string {
	if len(k) <= 8 {
		if k == "" {
			return ""
		}
		return "****"
	}
	return k[:4] + "..." + k[len(k)-4:]
}

func (h *Handlers) PutAPIConfig(w http.ResponseWriter, r *http.Request) {
	if h.isTaskRunning() {
		h.writeErrorReq(w, r, http.StatusConflict, "task_running_locked")
		return
	}
	var newCfg config.APIConfig
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if newCfg.HTTPTimeoutSeconds <= 0 {
		newCfg.HTTPTimeoutSeconds = config.DefaultHTTPTimeoutSeconds
	}

	h.taskMu.Lock()
	oldKey := h.apiCfg.APIKey
	h.taskMu.Unlock()
	// Keep the stored key when the client echoed back a masked value.
	if strings.Contains(newCfg.APIKey, "...") || newCfg.APIKey == "" {
		newCfg.APIKey = oldKey
	}

	data, err := json.MarshalIndent(newCfg, "", "  ")
	if err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "serialize_api_config_failed", err.Error())
		return
	}
	if err := fsutil.WriteFileAtomic(h.apiCfgPath, data); err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "save_api_config_failed", err)
		return
	}
	h.taskMu.Lock()
	h.apiCfg = &newCfg
	h.taskMu.Unlock()

	h.logger.InfoBilingual("API 配置已保存", "API configuration saved")
	resp := newCfg
	resp.APIKey = maskKey(resp.APIKey)
	h.writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) PostAPITest(w http.ResponseWriter, r *http.Request) {
	var testCfg config.APIConfig
	if err := json.NewDecoder(r.Body).Decode(&testCfg); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	// A masked key in the payload means "use the stored one".
	if strings.Contains(testCfg.APIKey, "...") || testCfg.APIKey == "" {
		h.taskMu.Lock()
		testCfg.APIKey = h.apiCfg.APIKey
		h.taskMu.Unlock()
	}
	if err := llm.ValidateConfig(&testCfg); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	resp, err := llm.CallAPIMessages(ctx, &testCfg, []llm.Message{{Role: "user", Content: "Hi"}})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			h.writeErrorReq(w, r, http.StatusGatewayTimeout, "api_test_timeout")
			return
		}
		h.writeErrorReq(w, r, http.StatusBadGateway, "api_test_failed", err.Error())
		return
	}
	sample := resp
	if len(sample) > 100 {
		sample = sample[:100] + "..."
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "OK",
		"model":   testCfg.Model,
		"sample":  sample,
	})
}

// GetOllamaModels proxies Ollama's native /api/tags endpoint so the
// Configuration tab can offer a model dropdown for local setups.
func (h *Handlers) GetOllamaModels(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("base_url")), "/")
	if base == "" {
		h.taskMu.Lock()
		base = h.apiCfg.BaseURL
		h.taskMu.Unlock()
	}
	if base == "" {
		base = "http://localhost:11434/v1"
	}
	// Derive the native endpoint from an OpenAI-compat base URL.
	native := strings.TrimSuffix(base, "/v1")
	if !strings.HasPrefix(native, "http") {
		native = "http://" + native
	}
	if !strings.Contains(strings.TrimPrefix(native, "http://"), ":") && !strings.HasPrefix(native, "https://") {
		native += ":11434"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, native+"/api/tags", nil)
	if err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Not an Ollama server (or offline): return empty list, not an error.
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"models": []string{}, "ollama": false})
		return
	}
	defer resp.Body.Close()
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	models := []string{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil {
		for _, m := range payload.Models {
			models = append(models, m.Name)
		}
		sort.Strings(models)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"models": models, "ollama": true})
}

// ---------------------------------------------------------------------------
// Tab: Novel Parameters
// ---------------------------------------------------------------------------

// GetParameterOptions returns the full option catalogue (genres/subgenres,
// lengths, structures + their beats, bias presets) for rendering the form.
func (h *Handlers) GetParameterOptions(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, genres.GetOptions())
}

// GetParameters returns the current project's novel parameters.
func (h *Handlers) GetParameters(w http.ResponseWriter, r *http.Request) {
	h.projectMu.RLock()
	defer h.projectMu.RUnlock()
	if h.projectName == "" {
		h.writeErrorReq(w, r, http.StatusBadRequest, "select_project_first")
		return
	}
	h.writeJSON(w, http.StatusOK, h.cfg.Story)
}

// PutParameters validates and saves the novel parameters for the current
// project. Structure changes re-resolve the section beats.
func (h *Handlers) PutParameters(w http.ResponseWriter, r *http.Request) {
	if h.isTaskRunning() {
		h.writeErrorReq(w, r, http.StatusConflict, "task_running_locked")
		return
	}
	h.projectMu.RLock()
	defer h.projectMu.RUnlock()
	if h.projectName == "" {
		h.writeErrorReq(w, r, http.StatusBadRequest, "select_project_first")
		return
	}

	var incoming config.StoryConfig
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	story := h.cfg.Story

	if incoming.Genre != "" {
		if !genres.IsGenre(incoming.Genre) {
			h.writeErrorReq(w, r, http.StatusBadRequest, "unknown_genre", incoming.Genre)
			return
		}
		story.Genre = incoming.Genre
	}
	if incoming.Subgenre != "" {
		if _, err := genres.Config(story.Genre, incoming.Subgenre); err != nil {
			h.writeErrorReq(w, r, http.StatusBadRequest, "unknown_subgenre", err.Error())
			return
		}
		story.Subgenre = incoming.Subgenre
	}
	if incoming.NovelLength != "" {
		if !genres.IsLength(incoming.NovelLength) {
			h.writeErrorReq(w, r, http.StatusBadRequest, "unknown_length", incoming.NovelLength)
			return
		}
		story.NovelLength = incoming.NovelLength
	}
	if incoming.Structure != "" {
		if !genres.IsValidStructure(story.NovelLength, incoming.Structure) {
			h.writeErrorReq(w, r, http.StatusBadRequest, "unknown_structure", incoming.Structure)
			return
		}
		story.Structure = incoming.Structure
		story.Sections = genres.StructureSections[incoming.Structure]
	}
	if incoming.Title != "" {
		story.Title = incoming.Title
	}
	if incoming.WritingStyle != "" {
		story.WritingStyle = incoming.WritingStyle
	}
	if incoming.WritingPOV != "" {
		story.WritingPOV = incoming.WritingPOV
	}
	if incoming.TargetWordsPerChapter > 0 {
		story.TargetWordsPerChapter = incoming.TargetWordsPerChapter
	}
	// Free-form / scalar hints: accepted verbatim with light bounds.
	story.ProtagonistType = clampStr(incoming.ProtagonistType, story.ProtagonistType)
	story.ConflictScale = clampStr(incoming.ConflictScale, story.ConflictScale)
	story.Tone = clampStr(incoming.Tone, story.Tone)
	story.Timeframe = clampStr(incoming.Timeframe, story.Timeframe)
	story.Locations = clampStr(incoming.Locations, story.Locations)
	story.GenderRatio = clampStr(incoming.GenderRatio, story.GenderRatio)
	story.RomanceLevel = clampStr(incoming.RomanceLevel, story.RomanceLevel)
	story.SexualContent = clampStr(incoming.SexualContent, story.SexualContent)
	if incoming.DarknessLevel >= 0 {
		story.DarknessLevel = clampInt(incoming.DarknessLevel, 1, 5, story.DarknessLevel)
	}
	if incoming.SpeculativeDensity >= 0 {
		story.SpeculativeDensity = clampInt(incoming.SpeculativeDensity, 1, 5, story.SpeculativeDensity)
	}

	h.cfg.Story = story
	if err := config.SaveConfig(h.cfgPath, h.cfg); err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "save_config_failed", err)
		return
	}
	h.logger.InfoBilingual("小说参数已更新", "Novel parameters updated")
	h.writeJSON(w, http.StatusOK, h.cfg.Story)
}

func clampStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func clampInt(v, lo, hi, fallback int) int {
	if v == 0 {
		return fallback
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// Generation workflow (outline / chapters)
// ---------------------------------------------------------------------------

func (h *Handlers) GetStatus(w http.ResponseWriter, r *http.Request) {
	h.projectMu.RLock()
	proj := h.projectName
	h.projectMu.RUnlock()

	phase, title, cur := "", "", 0
	outlineChapters, confirmed := 0, false
	written := []int{}
	if proj != "" {
		dir := filepath.Join(h.storysDir(), proj)
		if p, err := story.LoadProgress(filepath.Join(dir, "progress.json")); err == nil && p != nil {
			phase, title, cur = p.Phase, p.Title, p.CurrentChapterIdx
		}
		if o, err := task.LoadOutline(dir); err == nil && o != nil {
			outlineChapters, confirmed = len(o.Chapters), o.Confirmed
		}
		if nums, err := task.ListChapters(dir); err == nil {
			written = nums
		}
	}

	h.taskMu.Lock()
	running, last := h.taskRunning, h.lastTaskName
	h.taskMu.Unlock()
	taskName := ""
	if running {
		taskName = h.runner.TaskName()
		if taskName == "" {
			taskName = last
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"project":           proj,
		"phase":             phase,
		"title":             title,
		"current_chapter":   cur,
		"task_running":      running,
		"task_name":         taskName,
		"outline_chapters":  outlineChapters,
		"outline_confirmed": confirmed,
		"chapters_written":  written,
	})
}

func (h *Handlers) PostTaskCancel(w http.ResponseWriter, r *http.Request) {
	if !h.runner.Cancel() {
		h.writeErrorReq(w, r, http.StatusNotFound, "no_task_running")
		return
	}
	h.logger.WarnBilingual("收到取消请求，正在停止当前任务...", "Cancel requested; stopping current task...")
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

// currentProjDir returns the active project directory or an empty string.
func (h *Handlers) currentProjDir() string {
	h.projectMu.RLock()
	defer h.projectMu.RUnlock()
	if h.projectName == "" {
		return ""
	}
	return filepath.Join(h.storysDir(), h.projectName)
}

func (h *Handlers) PostOutlineGenerate(w http.ResponseWriter, r *http.Request) {
	dir := h.currentProjDir()
	if dir == "" {
		h.writeErrorReq(w, r, http.StatusBadRequest, "select_project_first")
		return
	}
	var req task.OutlineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	h.setTaskRunning(true, "outline")
	apiCfg := h.snapshotAPIConfig()
	h.projectMu.RLock()
	cfg := h.cfg
	h.projectMu.RUnlock()

	go func() {
		defer h.setTaskRunning(false, "")
		if err := h.runner.GenerateOutline(apiCfg, cfg, dir, req, h.logger); err != nil {
			h.logger.ErrorBilingual(
				fmt.Sprintf("大纲生成失败：%v", err),
				fmt.Sprintf("Outline generation failed: %v", err))
		}
	}()
	h.writeJSON(w, http.StatusAccepted, map[string]string{"status": "started", "task": "outline"})
}

func (h *Handlers) GetOutline(w http.ResponseWriter, r *http.Request) {
	dir := h.currentProjDir()
	if dir == "" {
		h.writeErrorReq(w, r, http.StatusBadRequest, "select_project_first")
		return
	}
	o, err := task.LoadOutline(dir)
	if err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "load_outline_failed", err.Error())
		return
	}
	if o == nil {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"exists": false})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"exists": true, "outline": o})
}

func (h *Handlers) PostOutlineConfirm(w http.ResponseWriter, r *http.Request) {
	dir := h.currentProjDir()
	if dir == "" {
		h.writeErrorReq(w, r, http.StatusBadRequest, "select_project_first")
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	o, err := task.ConfirmOutline(dir, req.Confirm)
	if err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "outline_confirm_failed", err.Error())
		return
	}
	if req.Confirm {
		h.logger.InfoBilingual("大纲已确认，进入写作阶段。", "Outline confirmed. Entering writing phase.")
	} else {
		h.logger.InfoBilingual("大纲确认已撤销。", "Outline confirmation revoked.")
	}
	h.writeJSON(w, http.StatusOK, o)
}

type chapterWriteRequest struct {
	Chapter  int    `json:"chapter"` // 1-based
	Feedback string `json:"feedback,omitempty"`
}

func (h *Handlers) PostChapterWrite(w http.ResponseWriter, r *http.Request) {
	dir := h.currentProjDir()
	if dir == "" {
		h.writeErrorReq(w, r, http.StatusBadRequest, "select_project_first")
		return
	}
	var req chapterWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Chapter < 1 {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_chapter_index", strconv.Itoa(req.Chapter))
		return
	}

	h.setTaskRunning(true, "write")
	apiCfg := h.snapshotAPIConfig()
	h.projectMu.RLock()
	cfg := h.cfg
	h.projectMu.RUnlock()

	go func() {
		defer h.setTaskRunning(false, "")
		if err := h.runner.WriteChapter(apiCfg, cfg, dir, req.Chapter, req.Feedback, h.logger); err != nil {
			if err == context.Canceled {
				h.logger.WarnBilingual("写作任务已取消", "Writing task cancelled")
			} else {
				h.logger.ErrorBilingual(
					fmt.Sprintf("第 %d 章写作失败：%v", req.Chapter, err),
					fmt.Sprintf("Chapter %d failed: %v", req.Chapter, err))
			}
		}
	}()
	h.writeJSON(w, http.StatusAccepted, map[string]interface{}{"status": "started", "task": "write", "chapter": req.Chapter})
}

func (h *Handlers) GetChapters(w http.ResponseWriter, r *http.Request) {
	dir := h.currentProjDir()
	if dir == "" {
		h.writeErrorReq(w, r, http.StatusBadRequest, "select_project_first")
		return
	}
	nums, err := task.ListChapters(dir)
	if err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "list_chapters_failed", err.Error())
		return
	}
	type chMeta struct {
		Index  int  `json:"index"`
		Exists bool `json:"exists"`
		Words  int  `json:"words"`
	}
	items := make([]chMeta, 0, len(nums))
	for _, n := range nums {
		text, _ := task.GetChapter(dir, n)
		words := len(strings.Fields(text))
		items = append(items, chMeta{Index: n, Exists: text != "", Words: words})
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"chapters": items})
}

func (h *Handlers) GetChapterN(w http.ResponseWriter, r *http.Request) {
	dir := h.currentProjDir()
	if dir == "" {
		h.writeErrorReq(w, r, http.StatusBadRequest, "select_project_first")
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_chapter_index", r.PathValue("n"))
		return
	}
	text, err := task.GetChapter(dir, n)
	if err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "read_chapter_failed", err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"chapter": n, "text": text, "exists": text != ""})
}

// snapshotAPIConfig copies the live API config under lock so async tasks are
// unaffected by later edits from the Configuration tab.
func (h *Handlers) snapshotAPIConfig() *config.APIConfig {
	h.taskMu.Lock()
	defer h.taskMu.Unlock()
	c := *h.apiCfg
	return &c
}

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

func validProjectName(name string) bool {
	if name == "" || name == "." || name == ".." || name != strings.TrimSpace(name) ||
		strings.HasSuffix(name, ".") || !filepath.IsLocal(name) {
		return false
	}
	for _, c := range name {
		if c < 32 {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
		return false
	}
	return !strings.ContainsAny(name, `/\\:*?"<>|`)
}

func (h *Handlers) GetProjects(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(h.storysDir())
	names := []string{}
	if err == nil {
		for _, e := range entries {
			if e.IsDir() && validProjectName(e.Name()) {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	h.projectMu.RLock()
	cur := h.projectName
	h.projectMu.RUnlock()
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"projects": names, "current": cur})
}

type createProjectRequest struct {
	Name  string             `json:"name"`
	Story config.StoryConfig `json:"story"`
}

// PostProject creates a project directory whose config.json carries both the
// classic story settings and the validated Novel Parameters.
func (h *Handlers) PostProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !validProjectName(req.Name) {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_project_name")
		return
	}
	dir := filepath.Join(h.storysDir(), req.Name)
	if _, err := os.Stat(dir); err == nil {
		h.writeErrorReq(w, r, http.StatusConflict, "project_exists")
		return
	}

	cfg := config.DefaultConfigForLang(i18n.FromRequest(r))
	s := req.Story
	if s.Genre != "" {
		if !genres.IsGenre(s.Genre) {
			h.writeErrorReq(w, r, http.StatusBadRequest, "unknown_genre", s.Genre)
			return
		}
		if s.Subgenre != "" {
			if _, err := genres.Config(s.Genre, s.Subgenre); err != nil {
				h.writeErrorReq(w, r, http.StatusBadRequest, "unknown_subgenre", err.Error())
				return
			}
		}
	}
	if s.NovelLength != "" && !genres.IsLength(s.NovelLength) {
		h.writeErrorReq(w, r, http.StatusBadRequest, "unknown_length", s.NovelLength)
		return
	}
	if s.Structure != "" {
		length := s.NovelLength
		if length == "" {
			length = "Novel (Standard)"
		}
		if !genres.IsValidStructure(length, s.Structure) {
			h.writeErrorReq(w, r, http.StatusBadRequest, "unknown_structure", s.Structure)
			return
		}
		s.Sections = genres.StructureSections[s.Structure]
	}
	if s.TargetWordsPerChapter <= 0 {
		s.TargetWordsPerChapter = 5000
	}
	cfg.Story = s

	if err := os.MkdirAll(dir, 0755); err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "create_project_failed", err)
		return
	}
	if err := config.SaveConfig(filepath.Join(dir, "config.json"), cfg); err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "save_config_failed", err)
		return
	}
	if err := story.SaveProgress(filepath.Join(dir, "progress.json"), &story.Progress{Phase: "setup", Title: s.Title}); err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "save_config_failed", err)
		return
	}
	if err := h.switchProject(req.Name); err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "create_project_failed", err)
		return
	}
	h.logger.SuccessBilingual(fmt.Sprintf("项目「%s」已创建", req.Name), fmt.Sprintf("Project \"%s\" created", req.Name))
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"status": "created", "name": req.Name})
}

func (h *Handlers) GetProjectCurrent(w http.ResponseWriter, r *http.Request) {
	h.projectMu.RLock()
	name := h.projectName
	h.projectMu.RUnlock()
	if name == "" {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"name": ""})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"name": name, "story": h.cfg.Story})
}

func (h *Handlers) PostProjectSelect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := h.switchProject(req.Name); err != nil {
		h.writeErrorReq(w, r, http.StatusBadRequest, "project_not_found", err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "selected", "name": req.Name})
}

func (h *Handlers) DeleteProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validProjectName(name) {
		h.writeErrorReq(w, r, http.StatusBadRequest, "invalid_project_name")
		return
	}
	dir := filepath.Join(h.storysDir(), name)
	if err := os.RemoveAll(dir); err != nil {
		h.writeErrorReq(w, r, http.StatusInternalServerError, "delete_project_failed", err)
		return
	}
	h.projectMu.Lock()
	if h.projectName == name {
		h.projectName = ""
		h.cfg = config.DefaultConfig()
	}
	h.projectMu.Unlock()
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handlers) switchProject(name string) error {
	if !validProjectName(name) {
		return fmt.Errorf("invalid project name")
	}
	dir := filepath.Join(h.storysDir(), name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("project directory missing: %s", name)
	}
	cfg, err := config.LoadConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		return err
	}
	h.projectMu.Lock()
	h.projectName = name
	h.cfg = cfg
	h.cfgPath = filepath.Join(dir, "config.json")
	h.projectMu.Unlock()
	return story.WriteCurrentProject(h.progDir, name)
}
