package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/justtunnel/justtunnel-cli/internal/display"
)

func TestBuildServerURL_Subdomain(t *testing.T) {
	result, err := buildServerURL("wss://api.justtunnel.dev/ws", "myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "subdomain=myapp") {
		t.Errorf("expected subdomain=myapp in URL, got %q", result)
	}
	if !strings.HasPrefix(result, "wss://api.justtunnel.dev/ws?") {
		t.Errorf("expected URL to start with wss://api.justtunnel.dev/ws?, got %q", result)
	}
}

func TestBuildServerURL_NoSubdomain(t *testing.T) {
	result, err := buildServerURL("wss://api.justtunnel.dev/ws", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "wss://api.justtunnel.dev/ws" {
		t.Errorf("expected unchanged URL, got %q", result)
	}
}

// requireSingleTunnelPort backs both the direct non-TTY path and the TUI
// fallback path. A zero port (only --config-file was supplied) must be rejected
// before runNonTTY builds http://localhost:0 and every proxied request 502s.
func TestRequireSingleTunnelPort_RejectsZeroPort(t *testing.T) {
	err := requireSingleTunnelPort(0, "port argument is required")
	if err == nil {
		t.Fatal("expected an error for port 0, got nil")
	}

	var cliErr *display.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *display.CLIError, got %T", err)
	}
	if cliErr.Category != display.CategoryInput {
		t.Errorf("expected CategoryInput, got %v", cliErr.Category)
	}
	if cliErr.Message != "port argument is required" {
		t.Errorf("expected supplied message, got %q", cliErr.Message)
	}
}

func TestRequireSingleTunnelPort_AllowsNonZeroPort(t *testing.T) {
	if err := requireSingleTunnelPort(8080, "port argument is required"); err != nil {
		t.Fatalf("expected no error for non-zero port, got %v", err)
	}
}
