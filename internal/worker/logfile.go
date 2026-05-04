package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Log retention policy. Both bounds are enforced together on every rotation:
// historical files older than maxLogAge are pruned regardless of byte total,
// AND historical files in excess of maxLogTotalBytes are pruned oldest-first.
// The active file is NEVER pruned, even if it alone exceeds the byte cap —
// truncating a file the runner currently has open would lose live log data
// and confuse readers tailing the file.
const (
	maxLogAge        = 7 * 24 * time.Hour
	maxLogTotalBytes = 100 * 1024 * 1024 // 100 MB
	logDateLayout    = "2006-01-02"
)

// historicalLogPattern matches the rotated-file naming scheme. Used by
// listHistoricalLogs to filter directory entries; the capture group extracts
// the YYYY-MM-DD stamp for sorting.
var historicalLogPattern = regexp.MustCompile(`^worker-(.+)\.(\d{4}-\d{2}-\d{2})\.log$`)

// RotatingWriter is an io.WriteCloser that owns a per-worker active log file
// and rotates it on date boundaries. Rotation runs lazily on Write — there is
// no background goroutine and no ticker to leak. All access to the underlying
// file is mutex-guarded so the worker's slog handler can call Write from
// multiple goroutines safely.
//
// Construct via NewRotatingWriter; the zero value is not usable.
type RotatingWriter struct {
	dir        string             // directory containing log files
	workerName string             // base name (validated; safe for path joining)
	now        func() time.Time   // injectable clock for tests
	mu         sync.Mutex         // serializes Write/Close/rotate
	file       *os.File           // current active file handle; nil after Close
	openedDate time.Time          // calendar date the active file was opened on
}

// NewRotatingWriter opens (or creates) the active log file for workerName
// under ~/.justtunnel/logs and returns a writer that will rotate on date
// changes. clock may be nil to use time.Now (production); tests inject a
// deterministic clock to drive rotation without sleeping.
func NewRotatingWriter(workerName string, clock func() time.Time) (*RotatingWriter, error) {
	if err := validateName(workerName); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	root, err := home()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("worker: create logs dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("worker: chmod logs dir: %w", err)
	}
	writer := &RotatingWriter{
		dir:        dir,
		workerName: workerName,
		now:        clock,
	}
	if err := writer.openActive(clock()); err != nil {
		return nil, err
	}
	return writer, nil
}

// activePath returns the path to the active (un-rotated) log file.
func (w *RotatingWriter) activePath() string {
	return filepath.Join(w.dir, "worker-"+w.workerName+".log")
}

// historicalPath returns the path a rotated file should take when the active
// file was last written on `date`.
func (w *RotatingWriter) historicalPath(date time.Time) string {
	return filepath.Join(w.dir, "worker-"+w.workerName+"."+date.Format(logDateLayout)+".log")
}

// openActive opens (or creates) the active file with append-only 0600 perms.
// The caller is responsible for holding the mutex (or being in the
// constructor where no other reference exists yet).
func (w *RotatingWriter) openActive(at time.Time) error {
	path := w.activePath()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("worker: open log file %s: %w", path, err)
	}
	// Defense-in-depth: tighten perms in case the file pre-existed with a
	// looser mode (e.g. created by a buggy earlier version).
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("worker: chmod log file %s: %w", path, err)
	}
	w.file = file
	w.openedDate = startOfDay(at)
	return nil
}

// startOfDay drops the time-of-day so two timestamps on the same calendar day
// compare equal. Operates in the wall-clock zone the timestamp was generated
// in (no UTC normalization) — log files are local artifacts inspected by
// local operators, so local-day boundaries match expectations.
func startOfDay(timestamp time.Time) time.Time {
	year, month, day := timestamp.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, timestamp.Location())
}

// Write appends data to the active log file, rotating first if the calendar
// date has advanced since the file was opened. Rotation failures (rename,
// re-open, prune) are handled best-effort: if rename fails we keep writing
// to the existing file rather than crashing the worker over a logging issue.
func (w *RotatingWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, errors.New("worker: write to closed log writer")
	}
	w.maybeRotateLocked()
	return w.file.Write(payload)
}

// maybeRotateLocked checks whether the calendar date has advanced and, if so,
// renames the active file to its historical name and opens a fresh active
// file. Any failure is logged-and-swallowed in the sense that the existing
// file handle remains usable — losing rotation is preferable to dropping log
// lines or crashing the worker.
func (w *RotatingWriter) maybeRotateLocked() {
	currentDay := startOfDay(w.now())
	if !currentDay.After(w.openedDate) {
		return
	}
	openedDay := w.openedDate
	// Close the current handle before rename; on Windows rename of an open
	// file fails, and on Unix it works but the historical file would still
	// have a writer attached briefly. Closing first keeps semantics simple.
	if err := w.file.Close(); err != nil {
		// Not fatal — the file might already be closed by a prior partial
		// rotation. Continue: if the rename succeeds we'll reopen below.
		_ = err
	}
	w.file = nil

	from := w.activePath()
	to := w.historicalPath(openedDay)
	if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Rename failed (e.g. disk full, permissions changed). Re-open the
		// active file and keep writing — rotation will retry on the next
		// write. This intentionally trades on-time rotation for log durability.
		_ = w.openActive(w.now())
		return
	}
	if err := w.openActive(w.now()); err != nil {
		// Active file could not be reopened. Leave w.file == nil; the next
		// Write will return an error. This is unlikely (we just renamed
		// out from under it) but the alternative is silently losing logs.
		return
	}
	w.pruneLocked()
}

// pruneLocked enforces the retention policy. Best-effort: any deletion error
// is ignored (e.g. file vanished between listing and unlink) so a transient
// FS hiccup does not block writes.
func (w *RotatingWriter) pruneLocked() {
	historical, err := w.listHistoricalLocked()
	if err != nil {
		return
	}
	now := w.now()
	// Sort newest-first so we can keep the head and prune the tail.
	sort.Slice(historical, func(left, right int) bool {
		return historical[left].date.After(historical[right].date)
	})
	// Pass 1: prune anything older than maxLogAge.
	cutoff := now.Add(-maxLogAge)
	kept := historical[:0]
	for _, entry := range historical {
		if entry.date.Before(cutoff) {
			_ = os.Remove(entry.path)
			continue
		}
		kept = append(kept, entry)
	}
	// Pass 2: enforce 100 MB cap, keeping the newest files.
	var total int64
	for index, entry := range kept {
		total += entry.size
		if total > maxLogTotalBytes {
			// Delete this and everything older.
			for _, drop := range kept[index:] {
				_ = os.Remove(drop.path)
			}
			break
		}
	}
}

// historicalEntry is a parsed historical log file plus its on-disk metadata,
// used by pruning and the reader's --all mode.
type historicalEntry struct {
	path string
	date time.Time
	size int64
}

// listHistoricalLocked enumerates rotated files for this worker. Entries
// whose name does not match the pattern (e.g. the active file, files for
// other workers) are skipped silently.
func (w *RotatingWriter) listHistoricalLocked() ([]historicalEntry, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return nil, fmt.Errorf("worker: read logs dir: %w", err)
	}
	out := make([]historicalEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := historicalLogPattern.FindStringSubmatch(entry.Name())
		if match == nil || match[1] != w.workerName {
			continue
		}
		date, err := time.Parse(logDateLayout, match[2])
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, historicalEntry{
			path: filepath.Join(w.dir, entry.Name()),
			date: date,
			size: info.Size(),
		})
	}
	return out, nil
}

// Close releases the active file handle. Idempotent. After Close further
// Writes return an error.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// HistoricalLog identifies a rotated log file on disk. Exposed for callers
// (e.g. the `worker logs --all` command) that need to enumerate retained
// files without owning a RotatingWriter.
type HistoricalLog struct {
	Path string
	Date time.Time
}

// ListLogsForReader returns the historical rotated files for workerName in
// chronological order (oldest first) plus the path the active file would
// take. Either may be missing on disk; callers should stat before reading.
// Returns an error only when the worker name is invalid or the logs
// directory cannot be read.
func ListLogsForReader(workerName string) (historical []HistoricalLog, activePath string, err error) {
	if err := validateName(workerName); err != nil {
		return nil, "", err
	}
	root, err := home()
	if err != nil {
		return nil, "", err
	}
	dir := filepath.Join(root, "logs")
	// Logs dir may not exist yet (worker never started). Treat ENOENT as
	// "no historical, no active" — caller distinguishes the two cases.
	if _, statErr := os.Stat(dir); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return nil, filepath.Join(dir, "worker-"+workerName+".log"), nil
		}
	}
	tempWriter := &RotatingWriter{dir: dir, workerName: workerName, now: time.Now}
	entries, err := tempWriter.listHistoricalLocked()
	if err != nil {
		return nil, "", err
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].date.Before(entries[right].date)
	})
	out := make([]HistoricalLog, 0, len(entries))
	for _, entry := range entries {
		out = append(out, HistoricalLog{Path: entry.path, Date: entry.date})
	}
	return out, tempWriter.activePath(), nil
}
