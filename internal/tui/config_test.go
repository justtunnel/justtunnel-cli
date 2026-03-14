package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestConfig writes YAML content to a temp file and returns the path.
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	return configPath
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		yaml        string
		wantTunnels int
		wantErr     bool
	}{
		{
			name: "valid two tunnels with all fields",
			yaml: `tunnels:
  - port: 3000
    name: frontend
    subdomain: my-frontend
  - port: 8080
    name: api
`,
			wantTunnels: 2,
			wantErr:     false,
		},
		{
			name: "port only entries are valid",
			yaml: `tunnels:
  - port: 3000
  - port: 8080
  - port: 9090
`,
			wantTunnels: 3,
			wantErr:     false,
		},
		{
			name:        "empty tunnels list is valid",
			yaml:        "tunnels: []\n",
			wantTunnels: 0,
			wantErr:     false,
		},
		{
			name: "duplicate ports rejected",
			yaml: `tunnels:
  - port: 3000
    name: first
  - port: 3000
    name: second
`,
			wantTunnels: 0,
			wantErr:     true,
		},
		{
			name: "port zero rejected",
			yaml: `tunnels:
  - port: 0
`,
			wantTunnels: 0,
			wantErr:     true,
		},
		{
			name: "port above 65535 rejected",
			yaml: `tunnels:
  - port: 70000
`,
			wantTunnels: 0,
			wantErr:     true,
		},
		{
			name: "negative port rejected",
			yaml: `tunnels:
  - port: -1
`,
			wantTunnels: 0,
			wantErr:     true,
		},
		{
			name:        "invalid yaml rejected",
			yaml:        "tunnels:\n  - [invalid",
			wantTunnels: 0,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			configPath := writeTestConfig(t, tt.yaml)

			cfg, err := LoadConfig(configPath)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.Tunnels) != tt.wantTunnels {
				t.Errorf("got %d tunnels, want %d", len(cfg.Tunnels), tt.wantTunnels)
			}
		})
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_FieldValues(t *testing.T) {
	t.Parallel()

	yaml := `tunnels:
  - port: 3000
    name: frontend
    subdomain: my-frontend
  - port: 8080
    name: api
  - port: 9090
`
	configPath := writeTestConfig(t, yaml)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Tunnels) != 3 {
		t.Fatalf("got %d tunnels, want 3", len(cfg.Tunnels))
	}

	// First tunnel: all fields set
	first := cfg.Tunnels[0]
	if first.Port != 3000 {
		t.Errorf("first tunnel port = %d, want 3000", first.Port)
	}
	if first.Name != "frontend" {
		t.Errorf("first tunnel name = %q, want %q", first.Name, "frontend")
	}
	if first.Subdomain != "my-frontend" {
		t.Errorf("first tunnel subdomain = %q, want %q", first.Subdomain, "my-frontend")
	}

	// Second tunnel: name but no subdomain
	second := cfg.Tunnels[1]
	if second.Port != 8080 {
		t.Errorf("second tunnel port = %d, want 8080", second.Port)
	}
	if second.Name != "api" {
		t.Errorf("second tunnel name = %q, want %q", second.Name, "api")
	}
	if second.Subdomain != "" {
		t.Errorf("second tunnel subdomain = %q, want empty", second.Subdomain)
	}

	// Third tunnel: port only
	third := cfg.Tunnels[2]
	if third.Port != 9090 {
		t.Errorf("third tunnel port = %d, want 9090", third.Port)
	}
	if third.Name != "" {
		t.Errorf("third tunnel name = %q, want empty", third.Name)
	}
}

func TestDiffConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		current    []ManagedTunnel
		desired    []TunnelPreset
		wantAdd    int
		wantRemove int
	}{
		{
			name: "2 current + 3 desired = 1 to add, 0 to remove",
			current: []ManagedTunnel{
				{Port: 3000, Name: "frontend"},
				{Port: 8080, Name: "api"},
			},
			desired: []TunnelPreset{
				{Port: 3000, Name: "frontend"},
				{Port: 8080, Name: "api"},
				{Port: 9090, Name: "metrics"},
			},
			wantAdd:    1,
			wantRemove: 0,
		},
		{
			name: "3 current + 2 desired = 0 to add, 1 to remove",
			current: []ManagedTunnel{
				{Port: 3000, Name: "frontend"},
				{Port: 8080, Name: "api"},
				{Port: 9090, Name: "metrics"},
			},
			desired: []TunnelPreset{
				{Port: 3000, Name: "frontend"},
				{Port: 8080, Name: "api"},
			},
			wantAdd:    0,
			wantRemove: 1,
		},
		{
			name:    "0 current + 2 desired = 2 to add, 0 to remove",
			current: []ManagedTunnel{},
			desired: []TunnelPreset{
				{Port: 3000, Name: "frontend"},
				{Port: 8080, Name: "api"},
			},
			wantAdd:    2,
			wantRemove: 0,
		},
		{
			name: "2 current + 0 desired = 0 to add, 2 to remove",
			current: []ManagedTunnel{
				{Port: 3000, Name: "frontend"},
				{Port: 8080, Name: "api"},
			},
			desired:    []TunnelPreset{},
			wantAdd:    0,
			wantRemove: 2,
		},
		{
			name: "different ports = all swapped",
			current: []ManagedTunnel{
				{Port: 3000, Name: "frontend"},
			},
			desired: []TunnelPreset{
				{Port: 4000, Name: "new-frontend"},
			},
			wantAdd:    1,
			wantRemove: 1,
		},
		{
			name:       "both empty = no changes",
			current:    []ManagedTunnel{},
			desired:    []TunnelPreset{},
			wantAdd:    0,
			wantRemove: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Convert current to pointers as Tunnels() returns []*ManagedTunnel
			currentPtrs := make([]*ManagedTunnel, len(tt.current))
			for idx := range tt.current {
				currentPtrs[idx] = &tt.current[idx]
			}

			diff := DiffConfig(currentPtrs, tt.desired)

			if len(diff.ToAdd) != tt.wantAdd {
				t.Errorf("ToAdd count = %d, want %d", len(diff.ToAdd), tt.wantAdd)
			}
			if len(diff.ToRemove) != tt.wantRemove {
				t.Errorf("ToRemove count = %d, want %d", len(diff.ToRemove), tt.wantRemove)
			}
		})
	}
}

func TestDiffConfig_AddContents(t *testing.T) {
	t.Parallel()

	current := []*ManagedTunnel{
		{Port: 3000, Name: "frontend"},
	}
	desired := []TunnelPreset{
		{Port: 3000, Name: "frontend", Subdomain: "my-frontend"},
		{Port: 9090, Name: "metrics"},
	}

	diff := DiffConfig(current, desired)

	if len(diff.ToAdd) != 1 {
		t.Fatalf("ToAdd count = %d, want 1", len(diff.ToAdd))
	}
	added := diff.ToAdd[0]
	if added.Port != 9090 {
		t.Errorf("added port = %d, want 9090", added.Port)
	}
	if added.Name != "metrics" {
		t.Errorf("added name = %q, want %q", added.Name, "metrics")
	}
}

func TestDiffConfig_RemoveContents(t *testing.T) {
	t.Parallel()

	current := []*ManagedTunnel{
		{Port: 3000, Name: "frontend"},
		{Port: 8080, Name: "api"},
		{Port: 9090, Name: "metrics"},
	}
	desired := []TunnelPreset{
		{Port: 3000, Name: "frontend"},
	}

	diff := DiffConfig(current, desired)

	if len(diff.ToRemove) != 2 {
		t.Fatalf("ToRemove count = %d, want 2", len(diff.ToRemove))
	}

	// Verify the removed ports are 8080 and 9090
	removedPorts := make(map[int]bool)
	for _, port := range diff.ToRemove {
		removedPorts[port] = true
	}
	if !removedPorts[8080] {
		t.Error("expected port 8080 to be in ToRemove")
	}
	if !removedPorts[9090] {
		t.Error("expected port 9090 to be in ToRemove")
	}
}
