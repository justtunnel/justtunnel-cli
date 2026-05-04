package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

// workerRmDeleteOnServer is the bound flag value for `worker rm
// --delete-on-server`. Cobra does NOT reset bound flag values between
// successive Execute() calls, which means a test that sets the flag and a
// later test that omits it would inherit the stale `true` value. Tests
// must call resetWorkerState (in cmd/worker_test.go) which zeroes this
// before each run and registers a t.Cleanup to zero it again on exit.
var workerRmDeleteOnServer bool

var workerRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a worker (local config by default; --delete-on-server to also remove server-side)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkerRm,
}

func init() {
	workerRmCmd.Flags().BoolVar(&workerRmDeleteOnServer, "delete-on-server", false,
		"also delete the worker server-side (off by default; local-only removal is the safe default)")
	workerCmd.AddCommand(workerRmCmd)
}

func runWorkerRm(cmd *cobra.Command, args []string) error {
	name := args[0]

	if !workerRmDeleteOnServer {
		// Local-only path: no auth, no HTTP, no team context required.
		// This lets users clean up stale local state even if they've
		// already lost team membership.
		//
		// Probe with Read first so we can distinguish "removed something"
		// from "nothing to remove". worker.Delete is idempotent (silently
		// returns nil on missing files per #33), which previously caused
		// `worker rm typo-name` to print a misleading "Removed local
		// config" line. Idempotent success is still success — `rm` of a
		// non-existent thing is a no-op success in Unix — but we owe the
		// user an honest message.
		if _, readErr := worker.Read(name); errors.Is(readErr, os.ErrNotExist) {
			fmt.Fprintf(cmd.OutOrStdout(),
				"No local config found for %q.\n", name,
			)
			return nil
		}
		// Read may also fail with non-ENOENT errors (permissions, parse
		// failure). In that case still try to delete so the user has a
		// path forward; surface the read error only if delete also fails.
		if err := worker.Delete(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete local worker config: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"Removed local config for %q. The worker may still be registered server-side.\n"+
				"Use --delete-on-server to also delete server-side.\n",
			name,
		)
		return nil
	}

	cfg, teamID, _, baseURL, err := loadWorkerEnv()
	if err != nil {
		return err
	}

	// Resolve worker ID via the list endpoint. We could try local first,
	// but the server is the source of truth and a list call costs the same
	// as a per-id GET while letting us surface "not found" cleanly.
	//
	// TOCTOU NOTE: there is a small race window between this list and the
	// DELETE below. Another operator (or a concurrent tab) could delete
	// the worker, or worse, delete it and recreate one with the same name
	// but a different ID, so our DELETE could land on the wrong target.
	// For v1 the typical operator pattern is single-user / single-shell,
	// and the DELETE is keyed on the resolved ID (not the name) which
	// limits the worst case to "404 instead of 200" for the deleted-then-
	// recreated case rather than wrong-target deletion. Acceptable for
	// v1; revisit if we expose multi-admin team workflows.
	workers, err := fetchWorkers(baseURL, cfg.AuthToken, teamID)
	if err != nil {
		return err
	}

	var workerID string
	for _, candidate := range workers {
		if candidate.Name == name {
			workerID = candidate.ID
			break
		}
	}

	if workerID == "" {
		// Server doesn't know about it — treat as already-deleted and
		// proceed with local cleanup so the user can recover from
		// out-of-sync state.
		if err := worker.Delete(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete local worker config: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"Worker %q not found server-side; removed local config.\n", name,
		)
		return nil
	}

	notFound, err := deleteWorker(baseURL, cfg.AuthToken, teamID, workerID)
	if err != nil {
		// Server refused — leave local config in place so the user can retry.
		return err
	}

	if err := worker.Delete(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete local worker config: %w", err)
	}

	if notFound {
		// 404 here means the server lost track of the ID between our
		// list and our DELETE — most likely a concurrent operator, but
		// it could also be a server-side cleanup. See TOCTOU NOTE above.
		fmt.Fprintf(cmd.OutOrStdout(),
			"Worker %q with id %s not found server-side — possibly already deleted, or replaced by a concurrent operation. Removed local config.\n",
			name, workerID,
		)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(),
			"Deleted worker %q (server + local).\n", name,
		)
	}
	return nil
}
