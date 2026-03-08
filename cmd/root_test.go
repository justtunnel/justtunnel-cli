package cmd

import (
	"strings"
	"testing"
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
