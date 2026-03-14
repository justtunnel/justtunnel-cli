package tui

import (
	"fmt"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	// debounceDelay is how long to wait after the last file-change event
	// before reloading the config. Editors often trigger multiple writes
	// (write to temp, rename, chmod) so we coalesce them.
	debounceDelay = 100 * time.Millisecond
)

// ConfigWatcher watches a config file for changes using fsnotify and sends
// ConfigChangedMsg or ConfigReloadErrorMsg to the TUI via a MessageSender.
type ConfigWatcher struct {
	configPath string
	manager    *TunnelManager
	sender     MessageSender
	watcher    *fsnotify.Watcher
	stopCh     chan struct{}
	stopped    sync.Once
}

// NewConfigWatcher creates a ConfigWatcher for the given config file path.
// The manager is used to compute diffs between running tunnels and desired config.
// The sender receives ConfigChangedMsg or ConfigReloadErrorMsg messages.
func NewConfigWatcher(configPath string, manager *TunnelManager, sender MessageSender) (*ConfigWatcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	if err := fsWatcher.Add(configPath); err != nil {
		fsWatcher.Close()
		return nil, fmt.Errorf("watch config file %q: %w", configPath, err)
	}

	return &ConfigWatcher{
		configPath: configPath,
		manager:    manager,
		sender:     sender,
		watcher:    fsWatcher,
		stopCh:     make(chan struct{}),
	}, nil
}

// Start begins watching for file changes in a background goroutine.
// Call Stop() to clean up.
func (w *ConfigWatcher) Start() {
	go w.watchLoop()
}

// Stop shuts down the watcher and releases resources.
// Safe to call multiple times.
func (w *ConfigWatcher) Stop() {
	w.stopped.Do(func() {
		close(w.stopCh)
		w.watcher.Close()
	})
}

// watchLoop runs the event loop that listens for fsnotify events
// and debounces them before triggering a config reload.
func (w *ConfigWatcher) watchLoop() {
	var debounceTimer *time.Timer

	for {
		select {
		case <-w.stopCh:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// We care about writes, creates (some editors write to a new file then rename),
			// and removes (config deleted).
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				// Reset the debounce timer on each event
				if debounceTimer != nil {
					debounceTimer.Stop()
				}

				if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
					// File was removed or renamed. Send error and try to re-watch.
					// Use a short debounce since editors may recreate the file.
					debounceTimer = time.AfterFunc(debounceDelay, func() {
						w.handleReload()
					})
				} else {
					debounceTimer = time.AfterFunc(debounceDelay, func() {
						w.handleReload()
					})
				}
			}

		case watchErr, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.sender.Send(ConfigReloadErrorMsg{
				Error: fmt.Sprintf("file watcher error: %v", watchErr),
			})
		}
	}
}

// handleReload loads the config file, computes a diff, and sends the appropriate message.
func (w *ConfigWatcher) handleReload() {
	// Check if stopped before sending
	select {
	case <-w.stopCh:
		return
	default:
	}

	cfg, err := LoadConfig(w.configPath)
	if err != nil {
		w.sender.Send(ConfigReloadErrorMsg{
			Error: fmt.Sprintf("config reload failed: %v", err),
		})
		return
	}

	currentTunnels := w.manager.Tunnels()
	diff := DiffConfig(currentTunnels, cfg.Tunnels)

	// Only send a message if there are actual changes
	if len(diff.ToAdd) > 0 || len(diff.ToRemove) > 0 {
		w.sender.Send(ConfigChangedMsg{
			ToAdd:    diff.ToAdd,
			ToRemove: diff.ToRemove,
		})
	}
}
