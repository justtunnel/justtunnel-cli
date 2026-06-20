package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justtunnel/justtunnel-cli/internal/httpclient"
)

func TestFetchPlanInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		responseBody   map[string]any
		statusCode     int
		wantPlanName   string
		wantMaxTunnels int
		wantErr        bool
	}{
		{
			name:           "free plan returns max 1 tunnel",
			responseBody:   map[string]any{"plan": "free"},
			statusCode:     http.StatusOK,
			wantPlanName:   "free",
			wantMaxTunnels: 1,
			wantErr:        false,
		},
		{
			name:           "starter plan returns max 2 tunnels",
			responseBody:   map[string]any{"plan": "starter"},
			statusCode:     http.StatusOK,
			wantPlanName:   "starter",
			wantMaxTunnels: 2,
			wantErr:        false,
		},
		{
			name:           "pro plan returns max 5 tunnels",
			responseBody:   map[string]any{"plan": "pro"},
			statusCode:     http.StatusOK,
			wantPlanName:   "pro",
			wantMaxTunnels: 5,
			wantErr:        false,
		},
		{
			name:           "unknown plan defaults to free limits",
			responseBody:   map[string]any{"plan": "unknown_plan"},
			statusCode:     http.StatusOK,
			wantPlanName:   "unknown_plan",
			wantMaxTunnels: 1,
			wantErr:        false,
		},
		{
			name:           "empty plan defaults to free limits",
			responseBody:   map[string]any{"plan": ""},
			statusCode:     http.StatusOK,
			wantPlanName:   "",
			wantMaxTunnels: 1,
			wantErr:        false,
		},
		{
			name:         "HTTP 401 returns error",
			responseBody: map[string]any{"error": "unauthorized"},
			statusCode:   http.StatusUnauthorized,
			wantErr:      true,
		},
		{
			name:         "HTTP 500 returns error",
			responseBody: map[string]any{"error": "internal server error"},
			statusCode:   http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/api/me" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				if req.Header.Get("Authorization") != "Bearer test-token" {
					writer.WriteHeader(http.StatusUnauthorized)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(tt.statusCode)
				json.NewEncoder(writer).Encode(tt.responseBody)
			}))
			defer server.Close()

			planInfo, err := FetchPlanInfo(server.URL, "test-token")

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if planInfo.Name != tt.wantPlanName {
				t.Errorf("plan name = %q, want %q", planInfo.Name, tt.wantPlanName)
			}
			if planInfo.MaxTunnels != tt.wantMaxTunnels {
				t.Errorf("max tunnels = %d, want %d", planInfo.MaxTunnels, tt.wantMaxTunnels)
			}
		})
	}
}

func TestFetchPlanInfo_NetworkError(t *testing.T) {
	t.Parallel()

	// Use a URL that will fail to connect
	_, err := FetchPlanInfo("http://127.0.0.1:1", "test-token")
	if err == nil {
		t.Fatal("expected error for network failure, got nil")
	}
}

// TestFetchPlanInfo_ServerStall is the CLI-2 regression for TUI startup: a
// server that accepts the connection then never responds must not block the
// TUI indefinitely. httpclient.Timeout bounds the call; we shrink it so the
// test is fast. The handler blocks on the request context so it unblocks
// cleanly when the client cancels.
func TestFetchPlanInfo_ServerStall(t *testing.T) {
	prev := httpclient.Timeout
	httpclient.Timeout = 100 * time.Millisecond
	t.Cleanup(func() { httpclient.Timeout = prev })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	}))
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		_, err := FetchPlanInfo(server.URL, "test-token")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout error from stalling server, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FetchPlanInfo did not return within 5s — timeout did not bound the stalling server")
	}
}

func TestFetchPlanInfo_AuthorizationHeaderSent(t *testing.T) {
	t.Parallel()

	var capturedAuthHeader string

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		capturedAuthHeader = req.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		json.NewEncoder(writer).Encode(map[string]string{"plan": "free"})
	}))
	defer server.Close()

	_, err := FetchPlanInfo(server.URL, "my-api-key-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedHeader := "Bearer my-api-key-123"
	if capturedAuthHeader != expectedHeader {
		t.Errorf("authorization header = %q, want %q", capturedAuthHeader, expectedHeader)
	}
}

func TestFetchPlanInfo_WSSURLConversion(t *testing.T) {
	t.Parallel()

	// Start an HTTPS-like test server (httptest uses http://)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		json.NewEncoder(writer).Encode(map[string]string{"plan": "pro"})
	}))
	defer server.Close()

	// The function should handle raw http:// URLs directly since httptest returns them.
	// The important thing is that wss:// -> https:// and ws:// -> http:// conversion works.
	// The conversion itself is covered by config.TestAPIBaseURL.
	planInfo, err := FetchPlanInfo(server.URL, "test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if planInfo.Name != "pro" {
		t.Errorf("plan name = %q, want %q", planInfo.Name, "pro")
	}
}

func TestPlanLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		planName       string
		wantMaxTunnels int
	}{
		{"free plan", "free", 1},
		{"starter plan", "starter", 2},
		{"pro plan", "pro", 5},
		{"unknown plan defaults to free", "enterprise", 1},
		{"empty plan defaults to free", "", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			maxTunnels := maxTunnelsForPlan(tt.planName)
			if maxTunnels != tt.wantMaxTunnels {
				t.Errorf("maxTunnelsForPlan(%q) = %d, want %d", tt.planName, maxTunnels, tt.wantMaxTunnels)
			}
		})
	}
}
