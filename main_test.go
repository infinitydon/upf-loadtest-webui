package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidName(t *testing.T) {
	valid := []string{"upf-loadtest", "a", "test-123"}
	invalid := []string{"", "UPF", "-test", "test-", "with_underscore"}
	for _, value := range valid {
		if !validName(value) {
			t.Errorf("expected %q to be valid", value)
		}
	}
	for _, value := range invalid {
		if validName(value) {
			t.Errorf("expected %q to be invalid", value)
		}
	}
}

func TestHealth(t *testing.T) {
	s := &server{staticDir: t.TempDir()}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestAuth(t *testing.T) {
	s := &server{staticDir: t.TempDir(), token: "secret"}
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestFilterRunEvents(t *testing.T) {
	raw := map[string]interface{}{"items": []interface{}{
		map[string]interface{}{"involvedObject": map[string]interface{}{"name": "trex-123-abc"}},
		map[string]interface{}{"involvedObject": map[string]interface{}{"name": "another-pod"}},
	}}
	events := filterRunEvents(raw, "trex-123")
	if len(events) != 1 {
		t.Fatalf("expected one matching event, got %d", len(events))
	}
}
