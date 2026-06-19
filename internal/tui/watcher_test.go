package tui

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// watcherMsgCollector collects tea.Msg values sent by the ConfigWatcher.
// It implements the MessageSender interface.
type watcherMsgCollector struct {
	mu       sync.Mutex
	messages []tea.Msg
}

func newWatcherMsgCollector() *watcherMsgCollector {
	return &watcherMsgCollector{
		messages: make([]tea.Msg, 0),
	}
}

func (c *watcherMsgCollector) Send(msg tea.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, msg)
}

func (c *watcherMsgCollector) Messages() []tea.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]tea.Msg, len(c.messages))
	copy(result, c.messages)
	return result
}

func (c *watcherMsgCollector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = c.messages[:0]
}

// waitForMessage waits up to the given duration for a message matching the predicate.
// Returns true if found.
func (c *watcherMsgCollector) waitForMessage(timeout time.Duration, predicate func(tea.Msg) bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, msg := range c.messages {
			if predicate(msg) {
				c.mu.Unlock()
				return true
			}
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// writeWatcherConfig writes YAML content to the given path, creating the file if needed.
func writeWatcherConfig(t *testing.T, configPath string, content string) {
	t.Helper()
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
}

func TestConfigWatcher_FileChangeProducesMessage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")

	// Write initial config with one tunnel
	initialYAML := "tunnels:\n  - port: 3000\n    name: frontend\n"
	writeWatcherConfig(t, configPath, initialYAML)

	collector := newWatcherMsgCollector()

	// Start the watcher with a manager that has no tunnels yet.
	// The watcher compares desired config against manager's current tunnels.
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)
	watcher, err := NewConfigWatcher(configPath, mgr, collector)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	watcher.Start()

	// Now write an updated config that adds a second tunnel
	updatedYAML := "tunnels:\n  - port: 3000\n    name: frontend\n  - port: 8080\n    name: api\n"
	writeWatcherConfig(t, configPath, updatedYAML)

	// Should receive a ConfigChangedMsg within 500ms
	found := collector.waitForMessage(500*time.Millisecond, func(msg tea.Msg) bool {
		changed, ok := msg.(ConfigChangedMsg)
		if !ok {
			return false
		}
		// Expect 1 tunnel to add (8080) since manager has no running tunnels
		// and 0 to remove (3000 not running either)
		return len(changed.ToAdd) > 0
	})

	if !found {
		t.Fatal("expected ConfigChangedMsg within 500ms after file write, got none")
	}
}

func TestConfigWatcher_DebounceRapidWrites(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")

	initialYAML := "tunnels:\n  - port: 3000\n"
	writeWatcherConfig(t, configPath, initialYAML)

	collector := newWatcherMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)
	watcher, err := NewConfigWatcher(configPath, mgr, collector)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	watcher.Start()

	// Rapid-fire 3 writes within 50ms each
	for writeIdx := 0; writeIdx < 3; writeIdx++ {
		yaml := "tunnels:\n  - port: 3000\n  - port: 8080\n"
		writeWatcherConfig(t, configPath, yaml)
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for debounce to settle (100ms debounce + some margin)
	time.Sleep(300 * time.Millisecond)

	// Count ConfigChangedMsg messages
	messages := collector.Messages()
	configChangedCount := 0
	for _, msg := range messages {
		if _, ok := msg.(ConfigChangedMsg); ok {
			configChangedCount++
		}
	}

	// Debounce should collapse rapid writes into a single reload
	if configChangedCount != 1 {
		t.Errorf("expected exactly 1 ConfigChangedMsg after rapid writes, got %d", configChangedCount)
	}
}

func TestConfigWatcher_InvalidYAMLSendsError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")

	validYAML := "tunnels:\n  - port: 3000\n"
	writeWatcherConfig(t, configPath, validYAML)

	collector := newWatcherMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)
	watcher, err := NewConfigWatcher(configPath, mgr, collector)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	watcher.Start()

	// Write invalid YAML
	writeWatcherConfig(t, configPath, "tunnels:\n  - [broken yaml")

	// Should receive a ConfigReloadErrorMsg, NOT a ConfigChangedMsg
	foundError := collector.waitForMessage(500*time.Millisecond, func(msg tea.Msg) bool {
		_, ok := msg.(ConfigReloadErrorMsg)
		return ok
	})

	if !foundError {
		t.Fatal("expected ConfigReloadErrorMsg for invalid YAML, got none")
	}

	// Should NOT have received a ConfigChangedMsg for this write
	messages := collector.Messages()
	for _, msg := range messages {
		if _, ok := msg.(ConfigChangedMsg); ok {
			t.Error("should not receive ConfigChangedMsg for invalid YAML")
		}
	}
}

func TestConfigWatcher_StopPreventsEvents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")

	initialYAML := "tunnels:\n  - port: 3000\n"
	writeWatcherConfig(t, configPath, initialYAML)

	collector := newWatcherMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)
	watcher, err := NewConfigWatcher(configPath, mgr, collector)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}

	watcher.Start()

	// Stop the watcher
	watcher.Stop()

	// Clear any messages from the start phase
	collector.Reset()

	// Write a new config
	writeWatcherConfig(t, configPath, "tunnels:\n  - port: 3000\n  - port: 9090\n")

	// Wait a bit and verify no messages were sent
	time.Sleep(300 * time.Millisecond)

	messages := collector.Messages()
	for _, msg := range messages {
		switch msg.(type) {
		case ConfigChangedMsg, ConfigReloadErrorMsg:
			t.Error("should not receive any config messages after Stop()")
		}
	}
}

func TestConfigWatcher_DeletedConfigSendsError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")

	validYAML := "tunnels:\n  - port: 3000\n"
	writeWatcherConfig(t, configPath, validYAML)

	collector := newWatcherMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)
	watcher, err := NewConfigWatcher(configPath, mgr, collector)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	watcher.Start()

	// Delete the config file
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("failed to remove config file: %v", err)
	}

	// Should receive an error message
	foundError := collector.waitForMessage(500*time.Millisecond, func(msg tea.Msg) bool {
		_, ok := msg.(ConfigReloadErrorMsg)
		return ok
	})

	if !foundError {
		t.Fatal("expected ConfigReloadErrorMsg when config file is deleted")
	}
}

// writeWatcherConfigAtomic mimics how editors save: write to a temp file in the
// same directory, then rename it over the target. This triggers an fsnotify
// Remove/Rename event on the watched path and drops the underlying watch.
func writeWatcherConfigAtomic(t *testing.T, configPath string, content string) {
	t.Helper()
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		t.Fatalf("failed to rename temp config: %v", err)
	}
}

// TestConfigWatcher_ReWatchesAfterRenameReplace proves the fix deterministically
// across platforms: after a remove/rename drops the underlying fsnotify watch,
// a successful reload re-adds the watch so later changes keep being delivered.
// inotify (Linux) drops the watch on rename-replace; we simulate that dropped
// state directly so the assertion holds regardless of the host's fsnotify backend.
func TestConfigWatcher_ReWatchesAfterRenameReplace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")

	initialYAML := "tunnels:\n  - port: 3000\n    name: frontend\n"
	writeWatcherConfig(t, configPath, initialYAML)

	collector := newWatcherMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)
	watcher, err := NewConfigWatcher(configPath, mgr, collector)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	// Simulate the inotify behavior on rename-replace: the watch is gone.
	if err := watcher.watcher.Remove(configPath); err != nil {
		t.Fatalf("failed to drop watch to simulate rename: %v", err)
	}
	for _, watched := range watcher.watcher.WatchList() {
		if watched == configPath {
			t.Fatalf("precondition failed: %q still watched after Remove", configPath)
		}
	}

	// A remove/rename-triggered reload must re-add the watch.
	writeWatcherConfig(t, configPath, "tunnels:\n  - port: 3000\n    name: frontend\n  - port: 8080\n    name: api\n")
	watcher.handleReload(true)

	rewatched := false
	for _, watched := range watcher.watcher.WatchList() {
		if watched == configPath {
			rewatched = true
			break
		}
	}
	if !rewatched {
		t.Fatalf("expected %q to be re-watched after rename-replace reload; watcher would go silent", configPath)
	}
}

// TestConfigWatcher_KeepsWatchingAfterRenameReplace is an end-to-end check that
// repeated editor atomic saves (write tmp + rename) keep producing change
// messages instead of the watcher going silent after the first rename.
func TestConfigWatcher_KeepsWatchingAfterRenameReplace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")

	initialYAML := "tunnels:\n  - port: 3000\n    name: frontend\n"
	writeWatcherConfig(t, configPath, initialYAML)

	collector := newWatcherMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)
	watcher, err := NewConfigWatcher(configPath, mgr, collector)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	watcher.Start()

	// First atomic save (rename-replace) adds port 8080. fsnotify drops the
	// watch on the rename; handleReload must re-add it.
	firstYAML := "tunnels:\n  - port: 3000\n    name: frontend\n  - port: 8080\n    name: api\n"
	writeWatcherConfigAtomic(t, configPath, firstYAML)

	foundFirst := collector.waitForMessage(500*time.Millisecond, func(msg tea.Msg) bool {
		changed, ok := msg.(ConfigChangedMsg)
		return ok && len(changed.ToAdd) > 0
	})
	if !foundFirst {
		t.Fatal("expected ConfigChangedMsg after first rename-replace, got none")
	}

	collector.Reset()

	// Second atomic save adds port 9090. Before the fix the watcher was silent
	// after the first rename, so this change would never be picked up.
	secondYAML := "tunnels:\n  - port: 3000\n    name: frontend\n  - port: 8080\n    name: api\n  - port: 9090\n    name: admin\n"
	writeWatcherConfigAtomic(t, configPath, secondYAML)

	foundSecond := collector.waitForMessage(1*time.Second, func(msg tea.Msg) bool {
		changed, ok := msg.(ConfigChangedMsg)
		if !ok {
			return false
		}
		for _, preset := range changed.ToAdd {
			if preset.Port == 9090 {
				return true
			}
		}
		return false
	})
	if !foundSecond {
		t.Fatal("expected ConfigChangedMsg for port 9090 after second rename-replace; watcher went silent after the first rename")
	}
}

func TestModelHandlesConfigChangedMsg(t *testing.T) {
	t.Parallel()

	t.Run("adds and removes tunnels from config change", func(t *testing.T) {
		t.Parallel()
		mocks := make(map[int]*mockTunnel)
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)
		model := NewModelWithManager(mgr, PlanInfo{Name: "Pro", MaxTunnels: 5})

		// Pre-add a tunnel that will be removed
		addErr := mgr.Add(3000, "frontend", "", "")
		if addErr != nil {
			t.Fatalf("Add(3000) failed: %v", addErr)
		}
		model.AddDisplayEntry(3000, "frontend")

		// Send a ConfigChangedMsg that removes 3000 and adds 8080
		msg := ConfigChangedMsg{
			ToAdd:    []TunnelPreset{{Port: 8080, Name: "api"}},
			ToRemove: []int{3000},
		}
		updatedModel, _ := model.Update(msg)
		model = updatedModel.(Model)

		// Should have 1 tunnel (8080), not 3000
		if len(model.tunnels) != 1 {
			t.Fatalf("expected 1 tunnel, got %d", len(model.tunnels))
		}
		if model.tunnels[0].Port != 8080 {
			t.Errorf("tunnel port = %d, want 8080", model.tunnels[0].Port)
		}
		if model.tunnels[0].Name != "api" {
			t.Errorf("tunnel name = %q, want %q", model.tunnels[0].Name, "api")
		}
		if model.tunnels[0].State != StateConnecting {
			t.Errorf("tunnel state = %v, want StateConnecting", model.tunnels[0].State)
		}
	})

	t.Run("no-op when diff is empty", func(t *testing.T) {
		t.Parallel()
		mocks := make(map[int]*mockTunnel)
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)
		model := NewModelWithManager(mgr, PlanInfo{Name: "Pro", MaxTunnels: 5})

		addErr := mgr.Add(3000, "frontend", "", "")
		if addErr != nil {
			t.Fatalf("Add(3000) failed: %v", addErr)
		}
		model.AddDisplayEntry(3000, "frontend")

		// Empty diff
		msg := ConfigChangedMsg{
			ToAdd:    nil,
			ToRemove: nil,
		}
		updatedModel, _ := model.Update(msg)
		model = updatedModel.(Model)

		if len(model.tunnels) != 1 {
			t.Errorf("expected 1 tunnel unchanged, got %d", len(model.tunnels))
		}
	})

	t.Run("without manager shows error", func(t *testing.T) {
		t.Parallel()
		model := NewModel(nil, PlanInfo{Name: "Pro", MaxTunnels: 5})

		msg := ConfigChangedMsg{
			ToAdd: []TunnelPreset{{Port: 8080, Name: "api"}},
		}
		updatedModel, _ := model.Update(msg)
		model = updatedModel.(Model)

		if model.errorMessage == "" {
			t.Error("expected error when manager is nil")
		}
	})
}

func TestModelHandlesConfigReloadErrorMsg(t *testing.T) {
	t.Parallel()

	model := NewModel(nil, PlanInfo{Name: "Pro", MaxTunnels: 5})
	model.tunnels = []TunnelDisplayEntry{
		{ID: 1, Port: 3000, Name: "frontend", State: StateConnected},
	}

	msg := ConfigReloadErrorMsg{Error: "config reload failed: invalid YAML"}
	updatedModel, _ := model.Update(msg)
	model = updatedModel.(Model)

	// Error message should be set
	if model.errorMessage != "config reload failed: invalid YAML" {
		t.Errorf("errorMessage = %q, want %q", model.errorMessage, "config reload failed: invalid YAML")
	}

	// Existing tunnels should be untouched
	if len(model.tunnels) != 1 {
		t.Errorf("expected 1 tunnel unchanged, got %d", len(model.tunnels))
	}
	if model.tunnels[0].State != StateConnected {
		t.Errorf("tunnel state changed to %v, want StateConnected", model.tunnels[0].State)
	}
}

func TestConfigWatcher_DiffComputedCorrectly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "tunnels.yaml")

	// Start with a config that has one tunnel
	initialYAML := "tunnels:\n  - port: 3000\n    name: frontend\n"
	writeWatcherConfig(t, configPath, initialYAML)

	collector := newWatcherMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

	// Add port 3000 to manager so it's already running
	addErr := mgr.Add(3000, "frontend", "", "")
	if addErr != nil {
		t.Fatalf("failed to add initial tunnel: %v", addErr)
	}

	watcher, err := NewConfigWatcher(configPath, mgr, collector)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	watcher.Start()

	// Clear any messages from tunnel add
	collector.Reset()

	// Update config: remove 3000, add 8080
	updatedYAML := "tunnels:\n  - port: 8080\n    name: api\n"
	writeWatcherConfig(t, configPath, updatedYAML)

	// Should get ConfigChangedMsg with correct diff
	found := collector.waitForMessage(500*time.Millisecond, func(msg tea.Msg) bool {
		changed, ok := msg.(ConfigChangedMsg)
		if !ok {
			return false
		}
		// Should have 1 to add (8080) and 1 to remove (3000)
		hasAdd := len(changed.ToAdd) == 1 && changed.ToAdd[0].Port == 8080
		hasRemove := len(changed.ToRemove) == 1 && changed.ToRemove[0] == 3000
		return hasAdd && hasRemove
	})

	if !found {
		messages := collector.Messages()
		t.Fatalf("expected ConfigChangedMsg with ToAdd=[8080], ToRemove=[3000]; got %d messages: %v", len(messages), messages)
	}
}
