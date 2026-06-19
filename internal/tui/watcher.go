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

				// fsnotify drops the watch when the file is removed or renamed
				// (e.g. an editor's atomic save: write tmp, rename over original).
				// In that case we must re-add the watch after reloading, otherwise
				// the watcher fires once and then goes permanently silent.
				rewatch := event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename)
				debounceTimer = time.AfterFunc(debounceDelay, func() {
					w.handleReload(rewatch)
				})
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
// When rewatch is true the triggering event removed or renamed the file, which causes
// fsnotify to drop the watch; after a successful reload we re-add it so subsequent
// changes (such as repeated editor atomic saves) keep being picked up.
func (w *ConfigWatcher) handleReload(rewatch bool) {
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

	// The reload succeeded, so the path exists again. Re-add the watch that
	// fsnotify dropped on the remove/rename, ignoring "already watching" no-ops.
	if rewatch {
		if err := w.watcher.Add(w.configPath); err != nil {
			w.sender.Send(ConfigReloadErrorMsg{
				Error: fmt.Sprintf("re-watch config file %q: %v", w.configPath, err),
			})
		}
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
