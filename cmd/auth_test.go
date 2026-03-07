package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyKeyValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/me" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("Authorization") != "Bearer justtunnel_validkey" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(writer).Encode(authVerifyResponse{
			Email:          "user@test.com",
			GitHubUsername: "testuser",
			Plan:           "pro",
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
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
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
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
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

func TestCreateDeviceSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/auth/device" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		json.NewEncoder(writer).Encode(deviceSessionResponse{
			DeviceCode:      "testdevicecode",
			UserCode:        "ABCD-5678",
			VerificationURL: "https://example.com/auth/cli",
			ExpiresIn:       900,
			PollInterval:    5,
		})
	}))
	defer server.Close()

	result, err := createDeviceSession(server.Client(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.UserCode != "ABCD-5678" {
		t.Errorf("user_code: got %q, want %q", result.UserCode, "ABCD-5678")
	}
	if result.DeviceCode != "testdevicecode" {
		t.Errorf("device_code: got %q, want %q", result.DeviceCode, "testdevicecode")
	}
}

func TestPollDeviceStatusPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(deviceStatusResponse{Status: "pending"})
	}))
	defer server.Close()

	result, err := pollDeviceStatus(server.Client(), server.URL, "testcode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "pending" {
		t.Errorf("status: got %q, want %q", result.Status, "pending")
	}
}

func TestPollDeviceStatusApproved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(deviceStatusResponse{
			Status: "approved",
			APIKey: "justtunnel_testkey123",
		})
	}))
	defer server.Close()

	result, err := pollDeviceStatus(server.Client(), server.URL, "testcode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "approved" {
		t.Errorf("status: got %q, want %q", result.Status, "approved")
	}
	if result.APIKey != "justtunnel_testkey123" {
		t.Errorf("api_key: got %q, want %q", result.APIKey, "justtunnel_testkey123")
	}
}

func TestPollDeviceStatusExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(deviceStatusResponse{Status: "expired"})
	}))
	defer server.Close()

	result, err := pollDeviceStatus(server.Client(), server.URL, "testcode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "expired" {
		t.Errorf("status: got %q, want %q", result.Status, "expired")
	}
}

func TestPollDeviceStatusRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "5")
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := pollDeviceStatus(server.Client(), server.URL, "testcode")
	if err == nil {
		t.Fatal("expected error for rate limited response")
	}
}

func TestPollDeviceStatusNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	result, err := pollDeviceStatus(server.Client(), server.URL, "testcode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "expired" {
		t.Errorf("status: got %q, want %q", result.Status, "expired")
	}
}
