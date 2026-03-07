package cmd

import (
	"strings"
	"testing"
)

func TestBuildServerURL_Subdomain(t *testing.T) {
	result, err := buildServerURL("wss://api.justtunnel.dev/ws", "myapp", "")
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

func TestBuildServerURL_Domain(t *testing.T) {
	result, err := buildServerURL("wss://api.justtunnel.dev/ws", "", "tunnel.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "domain=tunnel.example.com") {
		t.Errorf("expected domain=tunnel.example.com in URL, got %q", result)
	}
}

func TestBuildServerURL_MutuallyExclusive(t *testing.T) {
	_, err := buildServerURL("wss://api.justtunnel.dev/ws", "myapp", "tunnel.example.com")
	if err == nil {
		t.Fatal("expected error when both subdomain and domain are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention 'mutually exclusive', got %q", err.Error())
	}
}

func TestBuildServerURL_Neither(t *testing.T) {
	result, err := buildServerURL("wss://api.justtunnel.dev/ws", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "wss://api.justtunnel.dev/ws" {
		t.Errorf("expected unchanged URL, got %q", result)
	}
}
