package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	request := httptest.NewRequest("GET", "/health", nil)
	response := httptest.NewRecorder()
	handler().ServeHTTP(response, request)
	if response.Code != 200 || strings.TrimSpace(response.Body.String()) != "ok" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}

func TestGreeting(t *testing.T) {
	t.Setenv("GREETING", "hello test")
	request := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()
	handler().ServeHTTP(response, request)
	if response.Code != 200 || strings.TrimSpace(response.Body.String()) != "hello test" {
		t.Fatalf("greeting response = %d %q", response.Code, response.Body.String())
	}
}
