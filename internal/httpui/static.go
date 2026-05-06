package httpui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed assets
var assetsFS embed.FS

// staticHandler serves the embedded HTML/JS/CSS bundle.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/")
	if rel == "" {
		rel = "index.html"
	}
	rel = path.Clean(rel)
	full := path.Join("assets", rel)
	data, err := fs.ReadFile(assetsFS, full)
	if err != nil {
		// Fallback: serve index.html so the SPA can route client-side.
		if rel != "index.html" {
			data, err = fs.ReadFile(assetsFS, "assets/index.html")
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		full = "assets/index.html"
	}
	w.Header().Set("Content-Type", contentTypeFor(full))
	// SPA assets are tied to the running binary — once the operator
	// upgrades, the new index.html / app.js MUST be fetched. Without
	// `no-store` the browser can pin a stale (potentially-vulnerable)
	// version against the operator's intent. Embedded assets are tiny;
	// the bandwidth cost is negligible compared to the serve-stale-XSS
	// risk on a binary that never advertises a versioned URL.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func contentTypeFor(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
