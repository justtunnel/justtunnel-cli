package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

var workerCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a worker in the active team context",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkerCreate,
}

func init() {
	workerCmd.AddCommand(workerCreateCmd)
}

func runWorkerCreate(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, teamID, ctxName, baseURL, err := loadWorkerEnv()
	if err != nil {
		return err
	}

	created, err := postWorker(baseURL, cfg.AuthToken, teamID, name)
	if err != nil {
		return err
	}

	createdAt, parseErr := time.Parse(time.RFC3339, created.CreatedAt)
	if parseErr != nil {
		// Server response missing/malformed timestamp — fall back to now.
		// Local config is a CLI-side convenience, not a source of truth.
		// Surface the parse failure on stderr so a server-side regression
		// doesn't go silently unnoticed.
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: server returned unparseable created_at %q (%v); using local time\n",
			created.CreatedAt, parseErr,
		)
		createdAt = time.Now().UTC()
	}

	if writeErr := worker.Write(&worker.Config{
		WorkerID:       created.ID,
		Name:           created.Name,
		Context:        ctxName,
		Subdomain:      created.Subdomain,
		CreatedAt:      createdAt,
		ServiceBackend: "none",
	}); writeErr != nil {
		// Local persistence failed AFTER a successful server-side create.
		// Without compensation, the user is left with a "ghost" worker
		// they cannot see in `worker list` (server-only with no local
		// row will show as server-only, but the CLI still failed). Try
		// to roll back so retrying create won't 409.
		_, deleteErr := deleteWorker(baseURL, cfg.AuthToken, teamID, created.ID)
		if deleteErr != nil {
			return fmt.Errorf(
				"WARNING: server-side worker %q (id %s) created but local config write failed AND rollback also failed.\n"+
					"Run `justtunnel worker rm %s --delete-on-server` to clean up before retrying create.\n"+
					"  local write error: %v\n"+
					"  rollback error:    %v",
				created.Name, created.ID, created.Name, writeErr, deleteErr,
			)
		}
		return fmt.Errorf(
			"local config write failed; rolled back server-side create — please retry: %w",
			writeErr,
		)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Created worker %q (id=%s) in %s\n",
		created.Name, created.ID, ctxName,
	)
	if created.Subdomain != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  subdomain: %s\n", created.Subdomain)
	}
	return nil
}
