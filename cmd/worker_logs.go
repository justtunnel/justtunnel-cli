package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"


	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/display"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

const (
	defaultTailLines        = 1000
	followPollInterval      = 250 * time.Millisecond
	followReadChunk         = 32 * 1024
)

var (
	workerLogsFollow bool
	workerLogsAll    bool
	workerLogsLines  int
)

var workerLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show logs for a worker tunnel",
	Long: "Reads ~/.justtunnel/logs/worker-<name>.log written by `worker start`.\n\n" +
		"Default mode prints the last 1000 lines from the active log file. Use\n" +
		"--all to concatenate all retained daily files (oldest first), -n to set\n" +
		"the line count, or -f to tail the active file (Ctrl-C to exit).\n\n" +
		"Daily rotation and retention (7 days / 100 MB) are enforced by the\n" +
		"writer side — this command does not delete files.",
	Args: cobra.ExactArgs(1),
	RunE: runWorkerLogs,
}

func init() {
	workerLogsCmd.Flags().BoolVarP(&workerLogsFollow, "follow", "f", false, "tail the active log file; exit on Ctrl-C")
	workerLogsCmd.Flags().BoolVar(&workerLogsAll, "all", false, "print all retained log files in chronological order")
	workerLogsCmd.Flags().IntVarP(&workerLogsLines, "lines", "n", defaultTailLines, "number of trailing lines to print from the active file")
	workerCmd.AddCommand(workerLogsCmd)
}

func runWorkerLogs(cmd *cobra.Command, args []string) error {
	name := args[0]

	historical, activePath, err := worker.ListLogsForReader(name)
	if err != nil {
		return err
	}

	// Existence check: an empty active file is fine (worker started but no
	// output yet), but a missing one means `worker start` was never run (or
	// the worker was started, rotated, and never wrote again on the new day).
	activeMissing := false
	if _, statErr := os.Stat(activePath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			if len(historical) == 0 {
				return display.InputError(fmt.Sprintf(
					"no log file for worker %q (has it been started?)", name,
				))
			}
			activeMissing = true
		}
	}

	out := cmd.OutOrStdout()

	switch {
	case workerLogsAll:
		return printAllLogs(out, historical, activePath)
	case workerLogsFollow:
		if activeMissing {
			return display.InputError(fmt.Sprintf(
				"no active log file for worker %q (start the worker first, or use --all to read historical logs)", name,
			))
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return followActive(ctx, out, activePath)
	default:
		if activeMissing {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"no active log file for worker %q; use --all to view %d historical log file(s)\n",
				name, len(historical))
			return nil
		}
		return printTail(out, activePath, workerLogsLines)
	}
}

// printAllLogs concatenates historical files (oldest first) followed by the
// active file. Each file is streamed without buffering the full contents in
// memory — daily files can run to hundreds of MB.
func printAllLogs(out io.Writer, historical []worker.HistoricalLog, activePath string) error {
	for _, entry := range historical {
		if err := streamFile(out, entry.Path); err != nil {
			return err
		}
	}
	if _, err := os.Stat(activePath); err == nil {
		if err := streamFile(out, activePath); err != nil {
			return err
		}
	}
	return nil
}

func streamFile(out io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if _, err := io.Copy(out, file); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// printTail prints the last `lines` lines of path. Uses a streaming ring
// buffer so it works on arbitrarily large files without loading them all.
func printTail(out io.Writer, path string, lines int) error {
	if lines <= 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // empty active file is OK
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	ring := make([][]byte, 0, lines)
	scanner := bufio.NewScanner(file)
	// 1 MiB max line; log lines can be slog text records with stack traces.
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		// scanner.Bytes() is reused on next Scan; copy before storing.
		line := append([]byte(nil), scanner.Bytes()...)
		if len(ring) < lines {
			ring = append(ring, line)
			continue
		}
		// Slide window: drop oldest, append newest. Pre-allocated cap means
		// no reslice cost beyond the copy.
		copy(ring, ring[1:])
		ring[lines-1] = line
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	for _, line := range ring {
		if _, err := out.Write(line); err != nil {
			return err
		}
		if _, err := out.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return nil
}

// followActive prints the active file's current contents, then polls for
// new bytes and rotation. Rotation is detected by inode change (the writer
// renames the active file to a historical name and creates a fresh one);
// on detection we close the old handle and reopen the path.
//
// We poll instead of using fsnotify to avoid pulling in a third-party dep
// for a feature whose 250ms latency target is already inode-poll friendly.
func followActive(ctx context.Context, out io.Writer, path string) error {
	file, info, err := openForFollow(path)
	if err != nil {
		return err
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()

	buffer := make([]byte, followReadChunk)
	ticker := time.NewTicker(followPollInterval)
	defer ticker.Stop()

	for {
		// Drain any unread bytes before checking for rotation, so we don't
		// lose the tail of the file we're about to swap out. drainInto
		// itself watches ctx so a high write throughput cannot starve the
		// outer ctx.Done() select.
		if file != nil {
			if err := drainInto(ctx, out, file, buffer); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		// Rotation detection: stat the path; if the on-disk file is no
		// longer the same as our open handle (different inode / file ID),
		// the writer rotated and we should reopen.
		newInfo, statErr := os.Stat(path)
		if statErr != nil {
			// File momentarily missing during rotation — keep polling.
			continue
		}
		if !os.SameFile(info, newInfo) {
			if file != nil {
				// Read any final bytes from the old (now historical) handle
				// before swapping. The rename does not invalidate our fd on
				// POSIX; we keep reading until EOF.
				if drainErr := drainInto(ctx, out, file, buffer); drainErr != nil &&
					!errors.Is(drainErr, context.Canceled) &&
					!errors.Is(drainErr, context.DeadlineExceeded) {
					fmt.Fprintf(os.Stderr, "worker logs: drain pre-swap: %v\n", drainErr)
				}
				_ = file.Close()
			}
			reopened, reopenedInfo, err := openForFollow(path)
			if err != nil {
				return err
			}
			file = reopened
			info = reopenedInfo
		}
	}
}

// openForFollow opens path read-only and returns the file plus its
// FileInfo (used by os.SameFile for rotation detection). The reader starts
// at offset 0 by design — `worker logs -f` is "show me what happened and
// keep showing me", matching `tail -f -n +1` semantics.
//
// The reader does NOT create the file: file lifecycle is owned by the
// writer (worker process). A missing active file means the worker has not
// started; the caller is expected to surface that as a usage error before
// invoking followActive.
func openForFollow(path string) (*os.File, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}
	return file, info, nil
}

// drainInto copies all currently-available bytes from file into out, using
// the supplied scratch buffer. Stops at EOF or when a read returns fewer
// bytes than requested (no more data ready right now). Returns ctx.Err()
// promptly when ctx is cancelled, so a high-throughput writer cannot starve
// shutdown by keeping every Read fully populated.
func drainInto(ctx context.Context, out io.Writer, file *os.File, scratch []byte) error {
	for {
		// Tight per-iteration ctx check so a steady stream of full-buffer
		// reads cannot pin this loop indefinitely.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		count, err := file.Read(scratch)
		if count > 0 {
			if _, writeErr := out.Write(scratch[:count]); writeErr != nil {
				return writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if count < len(scratch) {
			return nil
		}
		// Re-check ctx between reads as well — covers the case where a
		// short read came back but more data is queued behind it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
}
