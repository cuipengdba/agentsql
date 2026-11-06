package chatbi

import (
	"io/fs"
	"net/http"
	"strings"
)

// AssistantOnlyHandler is intended for a dedicated listener and origin. It
// never serves the administration page or management API, even when a normal
// account token is presented. Deploy it on a different origin from the main
// ChatBI listener; the original SDK loads its iframe at this origin's root.
func AssistantOnlyHandler(base http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		path := r.URL.Path
		if r.Method == http.MethodGet && path == "/" {
			serveAssistantAsset(w, "assistant.html", "text/html; charset=utf-8")
			return
		}
		if r.Method == http.MethodGet {
			switch path {
			case "/ui/assistant.js":
				serveAssistantAsset(w, "assistant.js", "text/javascript; charset=utf-8")
				return
			case "/ui/assistant.css":
				serveAssistantAsset(w, "assistant.css", "text/css; charset=utf-8")
				return
			case "/ui/mark.svg":
				serveAssistantAsset(w, "mark.svg", "image/svg+xml")
				return
			}
		}
		if !assistantOnlyRoute(r) {
			http.NotFound(w, r)
			return
		}
		if assistantOnlyProtected(r) {
			values := r.Header.Values("X-SQLBOT-ASSISTANT-TOKEN")
			if len(values) != 1 || len(strings.Fields(values[0])) != 2 ||
				strings.Fields(values[0])[0] != "Assistant" ||
				len(r.Header.Values("X-SQLBOT-TOKEN")) != 0 ||
				len(r.Header.Values("Authorization")) != 0 {
				failure(w, ErrUnauthenticated)
				return
			}
		}
		base.ServeHTTP(w, r)
	})
}

func serveAssistantAsset(w http.ResponseWriter, name, contentType string) {
	data, err := fs.ReadFile(uiFiles, "ui/"+name)
	if err != nil {
		http.Error(w, "Asset unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if name == "assistant.html" {
		// This broad frame allowance applies only to the isolated assistant
		// listener. The administration listener retains frame-ancestors 'none'.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors https: http:; base-uri 'none'; form-action 'none'")
	} else {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	}
	_, _ = w.Write(data)
}

func assistantOnlyRoute(r *http.Request) bool {
	path := r.URL.Path
	if r.Method == http.MethodGet && (path == "/healthz" || path == "/api/v1/system/config/key" ||
		path == "/api/v1/system/assistant/ds" || path == "/api/v1/agentsql/assistant/context" ||
		path == "/api/v1/chat/list") {
		return true
	}
	if r.Method == http.MethodGet || r.Method == http.MethodOptions {
		for _, prefix := range []string{"/api/v1/system/assistant/info/", "/api/v1/system/assistant/validate/"} {
			if strings.HasPrefix(path, prefix) && ID(strings.TrimPrefix(path, prefix)).Valid() {
				return true
			}
		}
	}
	if r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v1/system/assistant/picture/") {
		return len(strings.TrimPrefix(path, "/api/v1/system/assistant/picture/")) <= 128
	}
	if r.Method == http.MethodPost && (path == "/api/v1/agentsql/assistant/guest-session" ||
		path == "/api/v1/agentsql/assistant/guest-session/resume" ||
		path == "/api/v1/chat/assistant/start" || path == "/api/v1/chat/question") {
		return true
	}
	if r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v1/chat/") {
		rest := strings.TrimPrefix(path, "/api/v1/chat/")
		if ID(rest).Valid() {
			return true
		}
		parts := strings.Split(rest, "/")
		return len(parts) == 2 && ID(parts[0]).Valid() && parts[1] == "with_data" ||
			len(parts) == 3 && parts[0] == "record" && ID(parts[1]).Valid() && (parts[2] == "data" || parts[2] == "usage")
	}
	return false
}

func assistantOnlyProtected(r *http.Request) bool {
	if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/api/v1/system/config/key") {
		return false
	}
	return !strings.HasPrefix(r.URL.Path, "/api/v1/system/assistant/info/") &&
		!strings.HasPrefix(r.URL.Path, "/api/v1/system/assistant/validate/") &&
		!strings.HasPrefix(r.URL.Path, "/api/v1/system/assistant/picture/")
}
