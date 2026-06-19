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

// guardTUIFallback is the decision that runTUI applies after program.Run() fails:
// a config-file-only invocation (port==0) must not fall back to runNonTTY, which
// would dial http://localhost:0. These tests cover that fallback decision directly
// so the wiring at the runTUI call site is exercised, not just the shared primitive.
func TestGuardTUIFallback_RejectsZeroPort(t *testing.T) {
	err := guardTUIFallback(0)
	if err == nil {
		t.Fatal("expected an error when falling back with port 0, got nil")
	}

	var cliErr *display.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *display.CLIError, got %T", err)
	}
	if cliErr.Category != display.CategoryInput {
		t.Errorf("expected CategoryInput, got %v", cliErr.Category)
	}
	if !strings.Contains(cliErr.Message, "fall back to single-tunnel mode") {
		t.Errorf("expected fallback-specific message, got %q", cliErr.Message)
	}
}

func TestGuardTUIFallback_AllowsNonZeroPort(t *testing.T) {
	if err := guardTUIFallback(8080); err != nil {
		t.Fatalf("expected no error for non-zero port, got %v", err)
	}
}

// guardNonTTYPort backs the direct non-TTY entry point. It must reject port==0
// with an input-category error before runNonTTY dials http://localhost:0.
func TestGuardNonTTYPort_RejectsZeroPort(t *testing.T) {
	err := guardNonTTYPort(0)
	if err == nil {
		t.Fatal("expected an error in non-TTY mode with port 0, got nil")
	}

	var cliErr *display.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *display.CLIError, got %T", err)
	}
	if cliErr.Category != display.CategoryInput {
		t.Errorf("expected CategoryInput, got %v", cliErr.Category)
	}
	if !strings.Contains(cliErr.Message, "non-TTY mode") {
		t.Errorf("expected non-TTY-specific message, got %q", cliErr.Message)
	}
}

func TestGuardNonTTYPort_AllowsNonZeroPort(t *testing.T) {
	if err := guardNonTTYPort(8080); err != nil {
		t.Fatalf("expected no error for non-zero port, got %v", err)
	}
}

// guardMissingTarget backs the no-arg entry point. With neither a port nor a
// --config-file it must return a non-nil typed error so Execute exits non-zero,
// instead of cmd.Help() which always returns nil and exits 0 — breaking
// `justtunnel || fallback` and `if justtunnel; then` shell/CI idioms.
func TestGuardMissingTarget_RejectsNoPortNoConfig(t *testing.T) {
	err := guardMissingTarget(0, "")
	if err == nil {
		t.Fatal("expected an error with no port and no config file, got nil")
	}

	var cliErr *display.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *display.CLIError, got %T", err)
	}
	if cliErr.Category != display.CategoryInput {
		t.Errorf("expected CategoryInput, got %v", cliErr.Category)
	}
}

func TestGuardMissingTarget_AllowsPort(t *testing.T) {
	if err := guardMissingTarget(8080, ""); err != nil {
		t.Fatalf("expected no error when a port is supplied, got %v", err)
	}
}

func TestGuardMissingTarget_AllowsConfigFile(t *testing.T) {
	if err := guardMissingTarget(0, "tunnels.yaml"); err != nil {
		t.Fatalf("expected no error when a config file is supplied, got %v", err)
	}
}
