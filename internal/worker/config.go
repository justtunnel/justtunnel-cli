// Package worker provides per-worker configuration persistence for the
// JustTunnel CLI. Each worker stores its identity and service-installation
// state as a JSON document under ~/.justtunnel/workers/<name>.json (or
// $JUSTTUNNEL_HOME/workers/<name>.json when the env var is set, primarily
// for tests).
//
// The schema mirrors the team-plan-and-worker-tunnels tech spec §6.2 and is
// intentionally separate from the user-global YAML config so that per-worker
// state is isolated and trivially manageable as discrete files.
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Config is the persisted state for a single worker tunnel.
type Config struct {
	WorkerID        string    `json:"worker_id"`
	Name            string    `json:"name"`
	Context         string    `json:"context"`
	Subdomain       string    `json:"subdomain"`
	CreatedAt       time.Time `json:"created_at"`
	ServiceBackend  string    `json:"service_backend"` // launchd | systemd | none
	ServiceUnitPath string    `json:"service_unit_path,omitempty"`
}

// nameRegexp matches the strict worker-name pattern used server-side. This
// is the only thing standing between an attacker-supplied name and a path
// traversal write under the workers directory, so do NOT loosen it without
// also coordinating with the server validation.
var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// homeEnvVar lets tests redirect the storage root to a temp directory.
const homeEnvVar = "JUSTTUNNEL_HOME"

// validateName returns an error if name does not match the strict pattern.
func validateName(name string) error {
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("worker: invalid name %q (must match %s)", name, nameRegexp)
	}
	return nil
}

// home returns the JustTunnel home directory: $JUSTTUNNEL_HOME if set,
// otherwise ~/.justtunnel. The directory is NOT created here.
func home() (string, error) {
	if override := os.Getenv(homeEnvVar); override != "" {
		return override, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("worker: locate home dir: %w", err)
	}
	return filepath.Join(userHome, ".justtunnel"), nil
}

// WorkerDir returns the workers subdirectory, creating it (and any missing
// parents) with 0700 permissions. Safe to call repeatedly.
func WorkerDir() (string, error) {
	root, err := home()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "workers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("worker: create workers dir: %w", err)
	}
	// MkdirAll won't tighten an existing looser permission; do it explicitly
	// so a pre-existing 0755 ~/.justtunnel/workers gets locked down.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("worker: chmod workers dir: %w", err)
	}
	return dir, nil
}

// ConfigPath returns the absolute path to the JSON file for the given worker
// name. It validates name first to prevent path traversal.
func ConfigPath(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	dir, err := WorkerDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// Read loads the config for the given worker. Returns an error wrapping
// os.ErrNotExist if the file is missing (use errors.Is to detect).
func Read(name string) (*Config, error) {
	path, err := ConfigPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Preserve os.ErrNotExist semantics for the caller.
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("worker: parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Write persists cfg atomically. The on-disk representation is identical
// for identical inputs, so re-writing the same Config is a no-op as far as
// downstream observers are concerned (idempotent re-init).
func Write(cfg *Config) error {
	if cfg == nil {
		return errors.New("worker: nil config")
	}
	path, err := ConfigPath(cfg.Name)
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("worker: marshal config: %w", err)
	}
	// Trailing newline is conventional and keeps editors happy.
	payload = append(payload, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("worker: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if anything below fails before rename.
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("worker: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("worker: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("worker: close temp file: %w", err)
	}
	// Tighten perms before rename so the final file is never world-readable.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("worker: chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("worker: rename temp -> final: %w", err)
	}
	cleanupTmp = false
	return nil
}

// List enumerates every worker config in the directory. Non-JSON files and
// the temp files generated during atomic writes are skipped. Results are
// sorted by name for deterministic output.
func List() ([]Config, error) {
	dir, err := WorkerDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("worker: read workers dir: %w", err)
	}
	configs := make([]Config, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("worker: read %s: %w", path, err)
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			// One bad file shouldn't poison the whole list; surface it
			// loudly instead of silently dropping so users can fix it.
			return nil, fmt.Errorf("worker: parse %s: %w", path, err)
		}
		configs = append(configs, cfg)
	}
	sort.Slice(configs, func(left, right int) bool {
		return configs[left].Name < configs[right].Name
	})
	return configs, nil
}

// Delete removes the config file for the given worker. Missing files are
// treated as success so callers can use Delete as part of an unconditional
// cleanup path.
func Delete(name string) error {
	path, err := ConfigPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("worker: remove %s: %w", path, err)
	}
	return nil
}