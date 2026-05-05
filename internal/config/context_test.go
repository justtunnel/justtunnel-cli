package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/justtunnel/justtunnel-cli/internal/config"
)

func TestValidateContext(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"personal is valid", "personal", false},
		{"team with slug is valid", "team:acme", false},
		{"team with digits and dashes", "team:acme-corp-2", false},
		{"team with ULID id is valid", "team:01KQTJBVA6REFPMKT8MPKX8Z9N", false},
		{"team with mixed case identifier is valid", "team:Acme", false},
		{"team with empty identifier", "team:", true},
		{"team with underscore", "team:acme_corp", true},
		{"team with space", "team:acme corp", true},
		{"team with dot", "team:acme.corp", true},
		{"random string", "foobar", true},
		{"empty string", "", true},
		{"just colon", ":", true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := config.ValidateContext(testCase.input)
			if testCase.wantErr && err == nil {
				t.Errorf("ValidateContext(%q): expected error, got nil", testCase.input)
			}
			if !testCase.wantErr && err != nil {
				t.Errorf("ValidateContext(%q): unexpected error: %v", testCase.input, err)
			}
		})
	}
}

func TestCurrentContextRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	config.SetConfigPath(path)

	original := &config.Config{
		AuthToken:      "justtunnel_ctx_token",
		ServerURL:      "wss://example.com/ws",
		LogLevel:       "info",
		CurrentContext: "team:acme",
	}

	if err := config.Save(original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.CurrentContext != "team:acme" {
		t.Errorf("current_context: got %q, want %q", loaded.CurrentContext, "team:acme")
	}
	if loaded.AuthToken != original.AuthToken {
		t.Errorf("auth_token round trip broken: got %q, want %q", loaded.AuthToken, original.AuthToken)
	}
}

func TestSetCurrentContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	config.SetConfigPath(path)

	cfg := &config.Config{ServerURL: "wss://example.com/ws"}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	if err := config.SetCurrentContext(cfg, "team:acme"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if cfg.CurrentContext != "team:acme" {
		t.Errorf("in-memory not updated: got %q", cfg.CurrentContext)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.CurrentContext != "team:acme" {
		t.Errorf("persisted current_context: got %q, want %q", loaded.CurrentContext, "team:acme")
	}

	// Switching back to personal works
	if err := config.SetCurrentContext(cfg, "personal"); err != nil {
		t.Fatalf("set personal: %v", err)
	}
	loaded, err = config.Load(path)
	if err != nil {
		t.Fatalf("load 2: %v", err)
	}
	if loaded.CurrentContext != "personal" {
		t.Errorf("after switch to personal: got %q", loaded.CurrentContext)
	}
}

func TestSetCurrentContextRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	config.SetConfigPath(path)

	cfg := &config.Config{}
	err := config.SetCurrentContext(cfg, "team:")
	if err == nil {
		t.Fatal("expected error for invalid context, got nil")
	}
	if !strings.Contains(err.Error(), "team identifier is empty") {
		t.Errorf("error should mention empty identifier, got: %v", err)
	}
	if cfg.CurrentContext != "" {
		t.Errorf("invalid set should not mutate cfg, got %q", cfg.CurrentContext)
	}
}

func TestResolveContext(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		flagValue string
		want      string
	}{
		{"flag wins over config", &config.Config{CurrentContext: "team:b"}, "team:a", "team:a"},
		{"config wins over default", &config.Config{CurrentContext: "team:b"}, "", "team:b"},
		{"default when nothing set", &config.Config{}, "", "personal"},
		{"default when cfg is nil", nil, "", "personal"},
		{"flag wins when cfg is nil", nil, "team:a", "team:a"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := config.ResolveContext(testCase.cfg, testCase.flagValue)
			if got != testCase.want {
				t.Errorf("ResolveContext: got %q, want %q", got, testCase.want)
			}
		})
	}
}
