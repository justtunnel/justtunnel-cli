package cmd

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

var workerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workers for the active team context",
	Args:  cobra.NoArgs,
	RunE:  runWorkerList,
}

func init() {
	workerCmd.AddCommand(workerListCmd)
}

// workerRow is a unified view across server and local sources, with a
// presence marker for divergence detection.
type workerRow struct {
	Name      string
	WorkerID  string
	Subdomain string
	Status    string
	Presence  string // "synced" | "server-only" | "local-only"
}

func runWorkerList(cmd *cobra.Command, args []string) error {
	cfg, teamID, ctxName, baseURL, err := loadWorkerEnv()
	if err != nil {
		return err
	}

	serverWorkers, err := fetchWorkers(baseURL, cfg.AuthToken, teamID)
	if err != nil {
		return err
	}

	localWorkers, err := worker.List()
	if err != nil {
		return fmt.Errorf("list local workers: %w", err)
	}

	rows := mergeWorkers(serverWorkers, localWorkers, ctxName)

	out := cmd.OutOrStdout()
	if len(rows) == 0 {
		fmt.Fprintf(out, "No workers in %s.\n", ctxName)
		return nil
	}

	fmt.Fprintf(out, "Workers in %s:\n", ctxName)
	writeWorkerTable(out, rows)
	return nil
}

// mergeWorkers unions the two sources by name. Server entries take
// precedence for status/id/subdomain when both sides exist; the local-only
// case still surfaces what we know locally so the user has something to
// reference. Output is sorted by name for deterministic display.
func mergeWorkers(server []workerAPI, local []worker.Config, ctxName string) []workerRow {
	byName := make(map[string]*workerRow)

	for _, srv := range server {
		row := &workerRow{
			Name:      srv.Name,
			WorkerID:  srv.ID,
			Subdomain: srv.Subdomain,
			Status:    srv.Status,
			Presence:  "server-only",
		}
		byName[srv.Name] = row
	}

	for _, loc := range local {
		// Local configs not bound to this context are noise — skip.
		if loc.Context != ctxName {
			continue
		}
		if existing, ok := byName[loc.Name]; ok {
			existing.Presence = "synced"
			continue
		}
		byName[loc.Name] = &workerRow{
			Name:      loc.Name,
			WorkerID:  loc.WorkerID,
			Subdomain: loc.Subdomain,
			Status:    "",
			Presence:  "local-only",
		}
	}

	rows := make([]workerRow, 0, len(byName))
	for _, row := range byName {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].Name < rows[right].Name })
	return rows
}

func writeWorkerTable(out io.Writer, rows []workerRow) {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tID\tSUBDOMAIN\tSTATUS\tPRESENCE")
	for _, row := range rows {
		status := row.Status
		if status == "" {
			status = "-"
		}
		subdomain := row.Subdomain
		if subdomain == "" {
			subdomain = "-"
		}
		workerID := row.WorkerID
		if workerID == "" {
			workerID = "-"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", row.Name, workerID, subdomain, status, row.Presence)
	}
	writer.Flush()
}
