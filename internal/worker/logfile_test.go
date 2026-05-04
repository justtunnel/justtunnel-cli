package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock returns a controllable time source. Tests advance the clock
// directly to drive rotation without sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(at time.Time) *fakeClock {
	return &fakeClock{now: at}
}

func (fc *fakeClock) Now() time.Time {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.now
}

func (fc *fakeClock) Set(at time.Time) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.now = at
}

func setupLogsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)
	return home
}

// readFile is a tiny helper that fails the test on read errors so call sites
// can stay flat.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestRotatingWriter_SameDayDoesNotRotate(t *testing.T) {
	setupLogsHome(t)
	clock := newFakeClock(time.Date(2026, 5, 4, 23, 59, 0, 0, time.UTC))
	writer, err := NewRotatingWriter("foo", clock.Now)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()

	if _, err := writer.Write([]byte("first\n")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Same calendar day, later in the evening.
	clock.Set(time.Date(2026, 5, 4, 23, 59, 30, 0, time.UTC))
	if _, err := writer.Write([]byte("second\n")); err != nil {
		t.Fatalf("second write: %v", err)
	}

	active := writer.activePath()
	got := readFile(t, active)
	if got != "first\nsecond\n" {
		t.Fatalf("active file content = %q, want %q", got, "first\nsecond\n")
	}
	// No historical files should exist.
	historical, _, err := ListLogsForReader("foo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(historical) != 0 {
		t.Fatalf("expected no historical files, got %d", len(historical))
	}
}

func TestRotatingWriter_DateBoundaryRotates(t *testing.T) {
	setupLogsHome(t)
	clock := newFakeClock(time.Date(2026, 5, 4, 23, 59, 0, 0, time.UTC))
	writer, err := NewRotatingWriter("foo", clock.Now)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()

	if _, err := writer.Write([]byte("day1\n")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	clock.Set(time.Date(2026, 5, 5, 0, 1, 0, 0, time.UTC))
	if _, err := writer.Write([]byte("day2\n")); err != nil {
		t.Fatalf("second write: %v", err)
	}

	active := writer.activePath()
	if got := readFile(t, active); got != "day2\n" {
		t.Fatalf("active = %q, want %q", got, "day2\n")
	}
	historical := writer.historicalPath(time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC))
	if got := readFile(t, historical); got != "day1\n" {
		t.Fatalf("historical = %q, want %q", got, "day1\n")
	}
}

func TestRotatingWriter_PrunesByAge(t *testing.T) {
	home := setupLogsHome(t)
	logsDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed 10 historical files spanning 12 days back. Anything > 7 days old
	// must be pruned by the next rotation.
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	for daysAgo := 1; daysAgo <= 12; daysAgo++ {
		date := now.AddDate(0, 0, -daysAgo)
		name := fmt.Sprintf("worker-foo.%s.log", date.Format(logDateLayout))
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte("seed"), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	clock := newFakeClock(now)
	writer, err := NewRotatingWriter("foo", clock.Now)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte("today\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Force rotation by jumping a day.
	clock.Set(now.AddDate(0, 0, 1))
	if _, err := writer.Write([]byte("tomorrow\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	historical, _, err := ListLogsForReader("foo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cutoff := clock.Now().Add(-maxLogAge)
	for _, entry := range historical {
		if entry.Date.Before(cutoff) {
			t.Fatalf("file %s is older than retention cutoff %s", entry.Path, cutoff)
		}
	}
}

func TestRotatingWriter_PrunesByTotalSize(t *testing.T) {
	home := setupLogsHome(t)
	logsDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed five 30 MB files (150 MB total) — newer than 7 days so age
	// pruning leaves them alone, forcing the size pass to drop oldest.
	chunk := make([]byte, 30*1024*1024)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	for daysAgo := 1; daysAgo <= 5; daysAgo++ {
		date := now.AddDate(0, 0, -daysAgo)
		name := fmt.Sprintf("worker-foo.%s.log", date.Format(logDateLayout))
		if err := os.WriteFile(filepath.Join(logsDir, name), chunk, 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	clock := newFakeClock(now)
	writer, err := NewRotatingWriter("foo", clock.Now)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte("today\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	clock.Set(now.AddDate(0, 0, 1))
	if _, err := writer.Write([]byte("tomorrow\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	historical, _, err := ListLogsForReader("foo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var total int64
	for _, entry := range historical {
		info, err := os.Stat(entry.Path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		total += info.Size()
	}
	if total > maxLogTotalBytes {
		t.Fatalf("retained historical bytes = %d, want <= %d", total, maxLogTotalBytes)
	}
}

func TestRotatingWriter_NeverPrunesActive(t *testing.T) {
	home := setupLogsHome(t)
	logsDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed enough historical files to trigger the size cap …
	chunk := make([]byte, 30*1024*1024)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	for daysAgo := 1; daysAgo <= 4; daysAgo++ {
		date := now.AddDate(0, 0, -daysAgo)
		name := fmt.Sprintf("worker-foo.%s.log", date.Format(logDateLayout))
		if err := os.WriteFile(filepath.Join(logsDir, name), chunk, 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	clock := newFakeClock(now)
	writer, err := NewRotatingWriter("foo", clock.Now)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()
	// Write a large amount to the active file so it dwarfs the cap …
	big := make([]byte, 50*1024*1024)
	if _, err := writer.Write(big); err != nil {
		t.Fatalf("write: %v", err)
	}
	clock.Set(now.AddDate(0, 0, 1))
	if _, err := writer.Write([]byte("rotate\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// After rotation, the previously-active file becomes today's historical
	// (large). It MAY survive if it is the newest historical and would only
	// be dropped to satisfy the cap. The important invariant: the new
	// active file must exist and be tiny.
	info, err := os.Stat(writer.activePath())
	if err != nil {
		t.Fatalf("active path missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("active file is empty; expected the rotate-day write to land in it")
	}
}

func TestRotatingWriter_ConcurrentWritesSerialize(t *testing.T) {
	setupLogsHome(t)
	clock := newFakeClock(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC))
	writer, err := NewRotatingWriter("foo", clock.Now)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()

	var wg sync.WaitGroup
	const writers = 8
	const iterations = 50
	for goroutine := 0; goroutine < writers; goroutine++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			line := []byte(fmt.Sprintf("g%d\n", id))
			for iter := 0; iter < iterations; iter++ {
				if _, err := writer.Write(line); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(goroutine)
	}
	wg.Wait()

	got := readFile(t, writer.activePath())
	gotLines := strings.Count(got, "\n")
	if gotLines != writers*iterations {
		t.Fatalf("line count = %d, want %d", gotLines, writers*iterations)
	}
}

func TestRotatingWriter_WriteAfterCloseFails(t *testing.T) {
	setupLogsHome(t)
	clock := newFakeClock(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC))
	writer, err := NewRotatingWriter("foo", clock.Now)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := writer.Write([]byte("after")); err == nil {
		t.Fatal("expected error writing after Close, got nil")
	}
	// Close is idempotent.
	if err := writer.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestRotatingWriter_ListLogsChronological(t *testing.T) {
	home := setupLogsHome(t)
	logsDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dates := []string{"2026-05-01", "2026-05-03", "2026-05-02"}
	for _, date := range dates {
		name := fmt.Sprintf("worker-foo.%s.log", date)
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte(date), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	historical, activePath, err := ListLogsForReader("foo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(historical) != 3 {
		t.Fatalf("len = %d, want 3", len(historical))
	}
	if !sort.SliceIsSorted(historical, func(left, right int) bool {
		return historical[left].Date.Before(historical[right].Date)
	}) {
		t.Fatal("historical entries are not sorted oldest-first")
	}
	if !strings.HasSuffix(activePath, "worker-foo.log") {
		t.Fatalf("active path = %s, want suffix worker-foo.log", activePath)
	}
}

func TestRotatingWriter_ListLogsMissingDirNoError(t *testing.T) {
	setupLogsHome(t)
	// No mkdir — listing must not error when the worker has never run.
	historical, activePath, err := ListLogsForReader("foo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(historical) != 0 {
		t.Fatalf("expected no historical, got %d", len(historical))
	}
	if !strings.HasSuffix(activePath, "worker-foo.log") {
		t.Fatalf("active path = %s, want suffix worker-foo.log", activePath)
	}
}

func TestRotatingWriter_InvalidNameRejected(t *testing.T) {
	setupLogsHome(t)
	if _, err := NewRotatingWriter("../escape", nil); err == nil {
		t.Fatal("expected error for path-traversal name, got nil")
	}
	if _, _, err := ListLogsForReader(""); err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

// TestRotatingWriter_RotationProducesNewInode verifies the reader-side
// rotation-detection contract: after rotation, the active file path resolves
// to a different on-disk file than the pre-rotation handle. os.SameFile is
// the cross-platform identity check used by the follow-mode reader.
func TestRotatingWriter_RotationProducesNewInode(t *testing.T) {
	setupLogsHome(t)
	clock := newFakeClock(time.Date(2026, 5, 4, 23, 0, 0, 0, time.UTC))
	writer, err := NewRotatingWriter("foo", clock.Now)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()

	if _, err := writer.Write([]byte("pre\n")); err != nil {
		t.Fatalf("pre write: %v", err)
	}
	preInfo, err := os.Stat(writer.activePath())
	if err != nil {
		t.Fatalf("pre stat: %v", err)
	}

	clock.Set(time.Date(2026, 5, 5, 0, 1, 0, 0, time.UTC))
	if _, err := writer.Write([]byte("post\n")); err != nil {
		t.Fatalf("post write: %v", err)
	}
	postInfo, err := os.Stat(writer.activePath())
	if err != nil {
		t.Fatalf("post stat: %v", err)
	}
	if os.SameFile(preInfo, postInfo) {
		t.Fatal("expected active file to be a new inode after rotation; reader follow-mode would miss the swap")
	}
}

