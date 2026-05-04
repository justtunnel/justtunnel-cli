package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

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
		if err := worker.Delete(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete local worker config: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"Removed local config for %q. The worker is still registered server-side.\n"+
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
		fmt.Fprintf(cmd.OutOrStdout(),
			"Worker %q already gone server-side; removed local config.\n", name,
		)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(),
			"Deleted worker %q (server + local).\n", name,
		)
	}
	return nil
}
