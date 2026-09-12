// Package webui serves the embedded AgentSQL console.
package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var embeddedAssets embed.FS

// Handler returns a static handler with single-page application fallback.
func Handler() (http.Handler, error) {
	dist, err := fs.Sub(embeddedAssets, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded web console: %w", err)
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded web console index: %w", err)
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		candidate := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if candidate == "." || candidate == "" || candidate == "index.html" {
			serveIndex(writer, request, index)
			return
		}
		if isBackendPath(candidate) {
			http.NotFound(writer, request)
			return
		}
		if fs.ValidPath(candidate) {
			if info, statError := fs.Stat(dist, candidate); statError == nil && !info.IsDir() {
				if strings.HasPrefix(candidate, "assets/") {
					writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(writer, request)
				return
			}
		}
		serveIndex(writer, request, index)
	}), nil
}

func serveIndex(writer http.ResponseWriter, request *http.Request, index []byte) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(index)
}

func isBackendPath(candidate string) bool {
	return candidate == "mcp" || strings.HasPrefix(candidate, "mcp/") ||
		candidate == "api" || strings.HasPrefix(candidate, "api/")
}
