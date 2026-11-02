package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebPageRoute(t *testing.T) {
	handler := routes(nil, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") || !strings.Contains(response.Body.String(), "Stacks by node") {
		t.Fatalf("web page response: %d %q", response.Code, response.Header().Get("Content-Type"))
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown route: got %d", response.Code)
	}
}
