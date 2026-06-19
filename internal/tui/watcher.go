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

// isRelevantEvent reports whether an fsnotify event should trigger a debounced
// reload. We care about writes, creates (editors that write a new file then
// rename), removes (config deleted), and renames (atomic save).
func isRelevantEvent(event fsnotify.Event) bool {
	return event.Has(fsnotify.Write) ||
		event.Has(fsnotify.Create) ||
		event.Has(fsnotify.Remove) ||
		event.Has(fsnotify.Rename)
}

// accumulateRewatch carries the "watch was dropped" state across a debounce
// window. A Remove/Rename drops the underlying fsnotify watch and must trigger
// a re-watch; a trailing Write/Create within the same window must NOT clear that
// need. It is therefore sticky: once true it stays true for the window, which is
// reset by the caller when a new window opens. Kept pure so the order-independent
// behavior can be tested without timing.
func accumulateRewatch(pending bool, event fsnotify.Event) bool {
	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		return true
	}
	return pending
}

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

	// pendingRewatch accumulates across the debounce window. A remove/rename
	// drops the underlying watch, and editor atomic saves commonly emit a
	// Rename/Remove followed by a trailing Create/Write within the window.
	// The debounce timer only ever fires the last event's closure, so we must
	// remember that a rewatch is needed regardless of which event arrives last;
	// otherwise the trailing event would reset it to false and the re-watch is
	// skipped, silently dropping the watch on the next save. The flag lives only
	// in this goroutine; it is cleared when a new window opens after the previous
	// debounce already fired (see the Stop()==false check below).
	pendingRewatch := false

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
			if isRelevantEvent(event) {
				// Reset the debounce timer on each event. Timer.Stop reports false
				// when the previous timer already fired, which means the previous
				// debounce window completed (its reload ran) — so this event opens
				// a fresh window and the accumulated rewatch flag must be cleared.
				if debounceTimer == nil || !debounceTimer.Stop() {
					pendingRewatch = false
				}

				// fsnotify drops the watch when the file is removed or renamed
				// (e.g. an editor's atomic save: write tmp, rename over original).
				// In that case we must re-add the watch after reloading, otherwise
				// the watcher fires once and then goes permanently silent. The flag
				// is sticky across the whole debounce window so a trailing Write/Create
				// after the rename does not flip it back to false.
				pendingRewatch = accumulateRewatch(pendingRewatch, event)
				rewatch := pendingRewatch
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
	// rewatchErr is reported after the config change below so the valid diff is
	// still applied — the reload genuinely succeeded; only live-reload degraded.
	var rewatchErr error
	if rewatch {
		// Re-check stop: Stop() closes the watcher, and calling Add on a closed
		// watcher races with that close. The second check shrinks the window
		// between the AfterFunc firing and a concurrent Stop().
		select {
		case <-w.stopCh:
			return
		default:
		}
		if addErr := w.watcher.Add(w.configPath); addErr != nil {
			rewatchErr = addErr
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

	// Re-watching failed after a successful reload: the change above was applied,
	// but fsnotify is no longer watching the file, so live-reload is now dead until
	// the watcher restarts. Report this distinctly so it does not read as a reload
	// failure (the config loaded fine) — it is a degraded-watch warning.
	if rewatchErr != nil {
		w.sender.Send(ConfigReloadErrorMsg{
			Error: fmt.Sprintf("config applied, but live-reload stopped (could not re-watch %q): %v", w.configPath, rewatchErr),
		})
	}
}
