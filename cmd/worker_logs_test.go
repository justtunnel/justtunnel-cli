package cmd

import (
	"context"
	"fmt"
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
		followErr = followActive(ctx, pipeWriter, activePath)
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

	cancel()
	done.Wait()
	captureWG.Wait()
	if followErr != nil {
		t.Errorf("followActive returned error: %v", followErr)
	}
}
