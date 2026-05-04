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
		createdAt = time.Now().UTC()
	}

	if err := worker.Write(&worker.Config{
		WorkerID:       created.ID,
		Name:           created.Name,
		Context:        ctxName,
		Subdomain:      created.Subdomain,
		CreatedAt:      createdAt,
		ServiceBackend: "none",
	}); err != nil {
		return fmt.Errorf("persist local worker config: %w", err)
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
