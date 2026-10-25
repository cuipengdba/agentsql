package chatbi

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/*
var uiFiles embed.FS

func uiHandler() http.Handler {
	files, _ := fs.Sub(uiFiles, "ui")
	server := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		server.ServeHTTP(w, r)
	})
}
