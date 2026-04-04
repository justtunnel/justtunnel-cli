package tui

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// TunnelPresetConfig is the top-level structure of a tunnel preset YAML file.
// This is separate from the auth config (which lives at ~/.config/justtunnel/config.yaml).
type TunnelPresetConfig struct {
	Tunnels []TunnelPreset `yaml:"tunnels"`
}

// ConfigDiff describes the changes needed to reconcile current tunnels with
// a desired preset config. Used by hot-reload (Phase 4).
type ConfigDiff struct {
	ToAdd    []TunnelPreset // tunnels in desired but not in current (by port)
	ToRemove []int          // ports in current but not in desired
}

// LoadConfig reads and validates a tunnel preset YAML file from the given path.
// It returns a validated TunnelPresetConfig or an error.
func LoadConfig(path string) (*TunnelPresetConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}

	var cfg TunnelPresetConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file %q: %w", path, err)
	}

	if err := validateConfig(&cfg); err != nil {
		return nil, fmt.Errorf("validate config file %q: %w", path, err)
	}

	return &cfg, nil
}

// validateConfig checks that all tunnel presets have valid ports and no duplicates.
func validateConfig(cfg *TunnelPresetConfig) error {
	seenPorts := make(map[int]bool, len(cfg.Tunnels))

	for idx, preset := range cfg.Tunnels {
		if preset.Port < minPort || preset.Port > maxPort {
			return fmt.Errorf("tunnel[%d]: invalid port %d (must be %d-%d)", idx, preset.Port, minPort, maxPort)
		}

		if seenPorts[preset.Port] {
			return fmt.Errorf("tunnel[%d]: duplicate port %d", idx, preset.Port)
		}
		seenPorts[preset.Port] = true

		if preset.Password != "" && (len(preset.Password) < 4 || len(preset.Password) > 128) {
			name := preset.Name
			if name == "" {
				name = fmt.Sprintf("port %d", preset.Port)
			}
			return fmt.Errorf("tunnel %q password must be between 4 and 128 characters", name)
		}
	}

	return nil
}

// DiffConfig compares current running tunnels against desired preset tunnels
// and returns which tunnels need to be added or removed. Matching is by port number.
func DiffConfig(current []*ManagedTunnel, desired []TunnelPreset) ConfigDiff {
	currentPorts := make(map[int]bool, len(current))
	for _, managed := range current {
		currentPorts[managed.Port] = true
	}

	desiredPorts := make(map[int]bool, len(desired))
	for _, preset := range desired {
		desiredPorts[preset.Port] = true
	}

	var diff ConfigDiff

	// Find tunnels to add: in desired but not in current
	for _, preset := range desired {
		if !currentPorts[preset.Port] {
			diff.ToAdd = append(diff.ToAdd, preset)
		}
	}

	// Find tunnels to remove: in current but not in desired
	for _, managed := range current {
		if !desiredPorts[managed.Port] {
			diff.ToRemove = append(diff.ToRemove, managed.Port)
		}
	}

	return diff
}
