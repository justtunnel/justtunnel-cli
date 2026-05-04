package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetWorkerLogsState ensures the workerLogs* package-level cobra flag vars
// are zeroed before AND after each test. Without this, a test that ran with
// -f leaks `workerLogsFollow=true` into the next test's invocation.
func resetWorkerLogsState(t *testing.T) {
	t.Helper()
	workerLogsFollow = false
	workerLogsAll = false
	workerLogsLines = defaultTailLines
	t.Cleanup(func() {
		workerLogsFollow = false
		workerLogsAll = false
		workerLogsLines = defaultTailLines
	})
}

// seedLogFile writes a file under JUSTTUNNEL_HOME/logs. Returns the full path.
func seedLogFile(t *testing.T, name, content string) string {
	t.Helper()
	home := os.Getenv("JUSTTUNNEL_HOME")
	if home == "" {
		t.Fatal("JUSTTUNNEL_HOME not set; call resetWorkerState first")
	}
	dir := filepath.Join(home, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestWorkerLogsRegistered(t *testing.T) {
	for _, child := range workerCmd.Commands() {
		if child.Name() == "logs" {
			return
		}
	}
	t.Fatal("worker logs subcommand is not registered under workerCmd")
}

func TestWorkerLogsErrorWhenNoLogs(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	_, err := runCmd(t, "worker", "logs", "ghost")
	if err == nil {
		t.Fatal("expected error for missing log file, got nil")
	}
	if !strings.Contains(err.Error(), "no log file") {
		t.Errorf("error message should mention missing log file; got: %v", err)
	}
}

func TestWorkerLogsDefaultPrintsActiveTail(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	// Build 5 lines so default-1000 trivially returns all of them.
	lines := []string{"line-1", "line-2", "line-3", "line-4", "line-5"}
	seedLogFile(t, "worker-alpha.log", strings.Join(lines, "\n")+"\n")

	out, err := runCmd(t, "worker", "logs", "alpha")
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	for _, want := range lines {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\noutput=%q", want, out)
		}
	}
}

func TestWorkerLogsLinesFlag(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	var builder strings.Builder
	for index := 1; index <= 50; index++ {
		fmt.Fprintf(&builder, "line-%02d\n", index)
	}
	seedLogFile(t, "worker-alpha.log", builder.String())

	out, err := runCmd(t, "worker", "logs", "alpha", "-n", "3")
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	gotLines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(gotLines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(gotLines), out)
	}
	want := []string{"line-48", "line-49", "line-50"}
	for index, line := range gotLines {
		if line != want[index] {
			t.Errorf("line %d: got %q, want %q", index, line, want[index])
		}
	}
}

func TestWorkerLogsAllConcatenatesChronologically(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	seedLogFile(t, "worker-alpha.2026-05-01.log", "first-day\n")
	seedLogFile(t, "worker-alpha.2026-05-02.log", "second-day\n")
	seedLogFile(t, "worker-alpha.2026-05-03.log", "third-day\n")
	seedLogFile(t, "worker-alpha.log", "today\n")

	out, err := runCmd(t, "worker", "logs", "alpha", "--all")
	if err != nil {
		t.Fatalf("logs --all: %v", err)
	}
	want := "first-day\nsecond-day\nthird-day\ntoday\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

// TestWorkerLogsFollowSeesNewWritesAndRotation drives the follow-mode reader
// in-process: a goroutine writes via the rotating writer, advances the fake
// clock to force rotation, writes again. The reader must see both pre- and
// post-rotation lines in stdout.
//
// Uses a real os.Pipe to avoid the test depending on an unbounded buffer
// growing during a tight poll loop.
func TestWorkerLogsFollowSeesNewWritesAndRotation(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	// Use the worker package directly to drive deterministic rotation.
	// Importing it would create a cycle in the test file's existing
	// imports, so we set up the file by hand using os.WriteFile + Rename
	// to mimic the writer's behavior.
	home := os.Getenv("JUSTTUNNEL_HOME")
	logsDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	activePath := filepath.Join(logsDir, "worker-alpha.log")
	if err := os.WriteFile(activePath, []byte("pre\n"), 0o600); err != nil {
		t.Fatalf("seed active: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pipeReader.Close()

	var followErr error
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		defer pipeWriter.Close()
		followErr = followActive(ctx, pipeWriter, io.Discard, activePath)
	}()

	// Read from the pipe in another goroutine into a buffer the test inspects.
	var captured strings.Builder
	var captureMu sync.Mutex
	var captureWG sync.WaitGroup
	captureWG.Add(1)
	go func() {
		defer captureWG.Done()
		buffer := make([]byte, 1024)
		for {
			count, err := pipeReader.Read(buffer)
			if count > 0 {
				captureMu.Lock()
				captured.Write(buffer[:count])
				captureMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	// Helper: poll captured for substring.
	waitFor := func(needle string) bool {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			captureMu.Lock()
			haveIt := strings.Contains(captured.String(), needle)
			captureMu.Unlock()
			if haveIt {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}

	if !waitFor("pre") {
		t.Fatalf("never saw pre-rotation content; captured=%q", captured.String())
	}

	// Append more to the active file — reader should pick it up on next poll.
	appendFile, err := os.OpenFile(activePath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := appendFile.WriteString("mid\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	appendFile.Close()
	if !waitFor("mid") {
		t.Fatalf("never saw mid-stream append; captured=%q", captured.String())
	}

	// Append a final line to the active file IMMEDIATELY before rename, so
	// it lands between the reader's last poll and the rotation. The
	// drain-before-swap path in followActive is the only thing that
	// captures it — if drain were deleted, this assertion would fail.
	preRenameAppend, err := os.OpenFile(activePath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open pre-rename append: %v", err)
	}
	if _, err := preRenameAppend.WriteString("BEFORE-RENAME-SHOULD-BE-CAPTURED\n"); err != nil {
		t.Fatalf("pre-rename append: %v", err)
	}
	preRenameAppend.Close()

	// Simulate rotation: rename active to historical, create new active.
	historical := filepath.Join(logsDir, "worker-alpha.2026-05-04.log")
	if err := os.Rename(activePath, historical); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.WriteFile(activePath, []byte("post\n"), 0o600); err != nil {
		t.Fatalf("post-rotation write: %v", err)
	}
	if !waitFor("post") {
		t.Fatalf("never saw post-rotation content; captured=%q", captured.String())
	}
	// Verify drain-before-swap captured the pre-rename append.
	captureMu.Lock()
	finalCapture := captured.String()
	captureMu.Unlock()
	if !strings.Contains(finalCapture, "BEFORE-RENAME-SHOULD-BE-CAPTURED") {
		t.Fatalf("drain-before-swap missed pre-rename bytes; captured=%q", finalCapture)
	}

	cancel()
	done.Wait()
	captureWG.Wait()
	if followErr != nil {
		t.Errorf("followActive returned error: %v", followErr)
	}
}

// TestWorkerLogsFollowMissingActiveErrors verifies `worker logs -f` returns
// a clear error when the active file does not exist. The reader must NOT
// create the file (file lifecycle is the writer's job).
func TestWorkerLogsFollowMissingActiveErrors(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	// Seed a historical file so we exercise the "active missing, historical
	// present" branch — this is the case that previously created-and-tailed.
	seedLogFile(t, "worker-ghost.2026-05-01.log", "old\n")

	_, err := runCmd(t, "worker", "logs", "-f", "ghost")
	if err == nil {
		t.Fatal("expected error following nonexistent active log, got nil")
	}
	if !strings.Contains(err.Error(), "no active log file") {
		t.Errorf("error should mention missing active log; got: %v", err)
	}
	// Crucial: the reader must NOT have created the active file.
	home := os.Getenv("JUSTTUNNEL_HOME")
	activePath := filepath.Join(home, "logs", "worker-ghost.log")
	if _, statErr := os.Stat(activePath); statErr == nil {
		t.Fatalf("reader created the active file at %s; reader must be read-only", activePath)
	}
}

// TestWorkerLogsDefaultActiveMissingHistoricalPresent verifies that the
// default (non-follow, non-all) mode prints a stderr hint and exits 0 when
// the active file is missing but historical files exist.
func TestWorkerLogsDefaultActiveMissingHistoricalPresent(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	seedLogFile(t, "worker-ghost.2026-05-01.log", "old1\n")
	seedLogFile(t, "worker-ghost.2026-05-02.log", "old2\n")

	// runCmd captures stdout+stderr (see context_test.go), so we can grep
	// the combined output for the hint.
	out, err := runCmd(t, "worker", "logs", "ghost")
	if err != nil {
		t.Fatalf("expected nil error (active missing + historical present is OK), got: %v", err)
	}
	if !strings.Contains(out, "no active log file") {
		t.Errorf("output should mention missing active log; got: %q", out)
	}
	if !strings.Contains(out, "2 historical") {
		t.Errorf("output should mention historical count; got: %q", out)
	}
}

// TestWorkerLogsLinesFlagBeyondTotal verifies -n N where N exceeds the
// file's line count returns all lines (not an error, no padding).
func TestWorkerLogsLinesFlagBeyondTotal(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	seedLogFile(t, "worker-alpha.log", "only-line-1\nonly-line-2\nonly-line-3\n")

	out, err := runCmd(t, "worker", "logs", "alpha", "-n", "100")
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	gotLines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(gotLines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(gotLines), out)
	}
}

// TestWorkerLogsAllNoHistoricalActiveOnly verifies --all with no historical
// files prints just the active file (and does not error).
func TestWorkerLogsAllNoHistoricalActiveOnly(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	seedLogFile(t, "worker-alpha.log", "active-only\n")

	out, err := runCmd(t, "worker", "logs", "alpha", "--all")
	if err != nil {
		t.Fatalf("logs --all: %v", err)
	}
	if out != "active-only\n" {
		t.Fatalf("output = %q, want %q", out, "active-only\n")
	}
}

// TestWorkerLogsFollowExitsPromptlyUnderHighWriteThroughput verifies that a
// reader stuck inside drainInto's read loop (busy writer pushing >32KB per
// poll interval) still returns within 100ms of ctx cancellation. This
// regression test guards against ctx.Done starvation in the drain path.
func TestWorkerLogsFollowExitsPromptlyUnderHighWriteThroughput(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))
	resetWorkerLogsState(t)

	home := os.Getenv("JUSTTUNNEL_HOME")
	logsDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	activePath := filepath.Join(logsDir, "worker-alpha.log")
	// Pre-seed > followReadChunk so the very first drainInto iteration
	// loops at least once before EOF.
	if err := os.WriteFile(activePath, make([]byte, 64*1024), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pipeReader.Close()

	// Drain the pipe so writes don't block.
	var drainWG sync.WaitGroup
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		buffer := make([]byte, 32*1024)
		for {
			if _, readErr := pipeReader.Read(buffer); readErr != nil {
				return
			}
		}
	}()

	// Background writer keeps pushing bytes into the active file so the
	// reader's drain loop never sees EOF on its own.
	stopWriter := make(chan struct{})
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		appender, openErr := os.OpenFile(activePath, os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			return
		}
		defer appender.Close()
		chunk := make([]byte, 64*1024)
		for {
			select {
			case <-stopWriter:
				return
			default:
			}
			_, _ = appender.Write(chunk)
		}
	}()

	followDone := make(chan error, 1)
	go func() {
		defer pipeWriter.Close()
		followDone <- followActive(ctx, pipeWriter, io.Discard, activePath)
	}()

	// Let the follow goroutine spin up and start draining.
	time.Sleep(50 * time.Millisecond)
	cancelStart := time.Now()
	cancel()

	select {
	case <-followDone:
		elapsed := time.Since(cancelStart)
		if elapsed > 100*time.Millisecond {
			t.Fatalf("followActive took %s to exit after ctx cancel; want <100ms", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("followActive did not exit within 2s of ctx cancel — drain loop is starving ctx.Done")
	}
	close(stopWriter)
	writerWG.Wait()
	drainWG.Wait()
}
