package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyKeyValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/verify" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer justtunnel_validkey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(authVerifyResponse{
			Email: "user@test.com",
			Plan:  "pro",
		})
	}))
	defer server.Close()

	result, err := verifyKey(server.Client(), server.URL, "justtunnel_validkey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Email != "user@test.com" {
		t.Errorf("email: got %q, want %q", result.Email, "user@test.com")
	}
	if result.Plan != "pro" {
		t.Errorf("plan: got %q, want %q", result.Plan, "pro")
	}
}

func TestVerifyKeyInvalidKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := verifyKey(server.Client(), server.URL, "justtunnel_badkey")
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
	if err.Error() != "invalid API key" {
		t.Errorf("error message: got %q, want %q", err.Error(), "invalid API key")
	}
}

func TestVerifyKeyForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	_, err := verifyKey(server.Client(), server.URL, "justtunnel_revokedkey")
	if err == nil {
		t.Fatal("expected error for forbidden key")
	}
	if err.Error() != "invalid API key" {
		t.Errorf("error message: got %q, want %q", err.Error(), "invalid API key")
	}
}

func TestVerifyKeyNetworkError(t *testing.T) {
	_, err := verifyKey(http.DefaultClient, "http://127.0.0.1:1", "justtunnel_key")
	if err == nil {
		t.Fatal("expected error for network failure")
	}
	if err.Error() != "could not reach justtunnel server" {
		t.Errorf("error message: got %q, want %q", err.Error(), "could not reach justtunnel server")
	}
}

func TestValidateKeyFormat(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"justtunnel_validkey123", true},
		{"justtunnel_", true},
		{"notjusttunnel_key", false},
		{"JUSTTUNNEL_key", false},
		{"", false},
		{"justtunnel", false},
	}

	for _, tt := range tests {
		if got := validateKeyFormat(tt.key); got != tt.want {
			t.Errorf("validateKeyFormat(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestAPIBaseURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"wss://api.justtunnel.dev/ws", "https://api.justtunnel.dev"},
		{"ws://localhost:8080/ws", "http://localhost:8080"},
		{"wss://custom.domain.com/ws?foo=bar", "https://custom.domain.com"},
	}

	for _, tt := range tests {
		got, err := apiBaseURL(tt.input)
		if err != nil {
			t.Errorf("apiBaseURL(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("apiBaseURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
