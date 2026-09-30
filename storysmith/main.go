// StorySmith — local-first novel generation studio.
//
// Built on the show-me-the-story architecture (single binary, web UI,
// OpenAI-compatible API → works out of the box with Ollama) and extended
// with NovelWriter-style "Novel Parameters" (genre catalogue, narrative
// structures, tuning knobs).
//
// Usage:
//
//	storysmith [--port :48090] [--data <dir>] [--no-browser]
//
// Data layout under --data (default: next to the executable):
//
//	api.json                 → API configuration (tab "Configuration")
//	current.txt              → active project name
//	storys/<project>/
//	  config.json            → story settings + Novel Parameters
//	  progress.json          → workflow phase
//	  outline.json           → generated outline (pending/confirmed)
//	  chapters/chNN.txt      → chapter prose
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"storysmith/internal/config"
	"storysmith/internal/httpapi"
	"storysmith/internal/llm"
	"storysmith/internal/story"
)

var version = "0.1.0-dev"

func main() {
	port := flag.String("port", ":48090", "listen address (e.g. :48090)")
	dataDir := flag.String("data", "", "data directory (default: alongside the executable)")
	noBrowser := flag.Bool("no-browser", false, "do not open a browser window on start")
	flag.Parse()

	progDir := *dataDir
	if progDir == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot resolve executable path: %v\n", err)
			os.Exit(1)
		}
		progDir = filepath.Dir(exe)
	}
	if err := os.MkdirAll(filepath.Join(progDir, "storys"), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create data dir: %v\n", err)
		os.Exit(1)
	}

	apiCfgPath := filepath.Join(progDir, "api.json")
	apiCfg, err := config.LoadAPIConfig(apiCfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot load api.json: %v\n", err)
		os.Exit(1)
	}
	// Best-effort: learn the model's real context window from the server
	// (works with Ollama / LM Studio / llama.cpp style endpoints).
	llm.EnsureContextBudget(apiCfg)

	// Restore last selected project if it still exists.
	h := httpapi.NewHandlersWithVersion(apiCfg, apiCfgPath, progDir, version)
	if last := story.ReadCurrentProject(progDir); last != "" {
		if err := h.SelectProjectSilent(last); err == nil {
			fmt.Printf(" [system] Project restored: %s\n", last)
		}
	}

	url := "http://localhost" + *port
	if !*noBrowser {
		httpapi.OpenBrowser(url)
	}
	httpapi.StartWebServerWithHandlers(h, *port, url)
}
