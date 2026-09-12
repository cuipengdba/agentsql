package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesEmbeddedIndex(t *testing.T) {
	handler, err := Handler()
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", contentType)
	}
	if recorder.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", recorder.Header().Get("Cache-Control"))
	}
}

func TestHandlerFallsBackToIndexForFrontendRoute(t *testing.T) {
	handler, err := Handler()
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	rootRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRecorder := httptest.NewRecorder()
	handler.ServeHTTP(rootRecorder, rootRequest)

	routeRequest := httptest.NewRequest(http.MethodGet, "/audit", nil)
	routeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(routeRecorder, routeRequest)
	if routeRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", routeRecorder.Code, http.StatusOK)
	}
	if routeRecorder.Body.String() != rootRecorder.Body.String() {
		t.Fatal("frontend route did not fall back to index.html")
	}
}
