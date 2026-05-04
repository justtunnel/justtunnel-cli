package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
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

// runWorkerCreate is the bare "POST + persist" flow. Idempotency reconciliation
// (the four modes implemented in `worker install`) is intentionally NOT done
// here — `create` is the explicit-intent verb that always issues a POST. A5:
// the actual POST + write + rollback dance lives in createServerSideAndPersist
// (worker_install.go) so the two commands cannot drift on rollback semantics
// or unparseable-timestamp warnings.
func runWorkerCreate(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, teamID, ctxName, baseURL, err := loadWorkerEnv()
	if err != nil {
		return err
	}

	created, err := createServerSideAndPersist(baseURL, cfg.AuthToken, teamID, ctxName, name, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Created worker %q (id=%s) in %s\n",
		created.Name, created.WorkerID, ctxName,
	)
	if created.Subdomain != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  subdomain: %s\n", created.Subdomain)
	}
	return nil
}
