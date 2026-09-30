// Embedded single-page web UI for StorySmith. The whole app lives in
// web/index.html (tabs: Projects / Novel Parameters / API Configuration /
// Outline / Writing) and is served from the binary — no external assets.
package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:web
var webFS embed.FS

// withWebUI wraps an API mux so that everything that is not under /api/
// serves the embedded SPA (with SPA fallback to index.html).
func withWebUI(api *http.ServeMux) http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("embedded web assets missing: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		// Serve real files when they exist; otherwise fall back to the SPA.
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
