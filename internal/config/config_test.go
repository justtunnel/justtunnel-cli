package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justtunnel/justtunnel-cli/internal/config"
)

func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	config.SetConfigPath(path)

	original := &config.Config{
		AuthToken: "justtunnel_roundtrip_token",
		ServerURL: "wss://custom.example.com/ws",
		LogLevel:  "debug",
	}

	if err := config.Save(original); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file permissions: got %o, want 0600", perm)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.AuthToken != original.AuthToken {
		t.Errorf("auth_token: got %q, want %q", loaded.AuthToken, original.AuthToken)
	}
	if loaded.ServerURL != original.ServerURL {
		t.Errorf("server_url: got %q, want %q", loaded.ServerURL, original.ServerURL)
	}
	if loaded.LogLevel != original.LogLevel {
		t.Errorf("log_level: got %q, want %q", loaded.LogLevel, original.LogLevel)
	}
}

func TestConfigPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	config.SetConfigPath(path)

	if got := config.ConfigPath(); got != path {
		t.Errorf("ConfigPath: got %q, want %q", got, path)
	}
}

func TestDeleteAuthToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	config.SetConfigPath(path)

	cfg := &config.Config{
		AuthToken: "justtunnel_to_be_deleted",
		ServerURL: "wss://example.com/ws",
		LogLevel:  "info",
	}

	if err := config.Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := config.DeleteAuthToken(); err != nil {
		t.Fatalf("delete auth token: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}

	if loaded.AuthToken != "" {
		t.Errorf("auth_token should be empty after delete, got %q", loaded.AuthToken)
	}
	if loaded.ServerURL != cfg.ServerURL {
		t.Errorf("server_url should be preserved: got %q, want %q", loaded.ServerURL, cfg.ServerURL)
	}
}

func TestDeleteAuthTokenNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")

	config.SetConfigPath(path)

	if err := config.DeleteAuthToken(); err != nil {
		t.Fatalf("delete should succeed when no file exists: %v", err)
	}
}

func TestAPIBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		serverURL string
		want      string
		wantErr   bool
	}{
		{
			name:      "wss URL converts to https",
			serverURL: "wss://api.justtunnel.dev/ws",
			want:      "https://api.justtunnel.dev",
		},
		{
			name:      "ws URL converts to http",
			serverURL: "ws://localhost:8080/ws",
			want:      "http://localhost:8080",
		},
		{
			name:      "https URL stays https",
			serverURL: "https://api.justtunnel.dev",
			want:      "https://api.justtunnel.dev",
		},
		{
			name:      "http URL stays http",
			serverURL: "http://localhost:8080",
			want:      "http://localhost:8080",
		},
		{
			name:      "strips path from URL",
			serverURL: "wss://api.justtunnel.dev/ws/connect",
			want:      "https://api.justtunnel.dev",
		},
		{
			name:      "strips query string",
			serverURL: "wss://custom.domain.com/ws?foo=bar",
			want:      "https://custom.domain.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := config.APIBaseURL(tt.serverURL)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("APIBaseURL(%q) = %q, want %q", tt.serverURL, got, tt.want)
			}
		})
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	path := filepath.Join(nested, "config.yaml")

	config.SetConfigPath(path)

	cfg := &config.Config{AuthToken: "justtunnel_nested"}

	if err := config.Save(cfg); err != nil {
		t.Fatalf("save with nested dir: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("config file was not created")
	}

	dirInfo, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("directory permissions: got %o, want 0700", perm)
	}
}
