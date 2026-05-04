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

// mergeWorkers unions the two sources. Server entries are keyed by
// WorkerID (the server's source of truth) so a name collision in the
// server response surfaces as two distinct rows tagged with a
// "[duplicate-name]" marker rather than silently overwriting. Local
// entries are matched against server entries first by WorkerID, then by
// name as a fallback for local-only configs that have no server-assigned
// ID yet. Output is sorted by name for deterministic display.
func mergeWorkers(server []workerAPI, local []worker.Config, ctxName string) []workerRow {
	// Detect server-side name collisions in a first pass so we can mark
	// every offending row, not just the second one.
	nameCounts := make(map[string]int, len(server))
	for _, srv := range server {
		nameCounts[srv.Name]++
	}

	byID := make(map[string]*workerRow, len(server))
	rows := make([]*workerRow, 0, len(server)+len(local))
	for _, srv := range server {
		displayName := srv.Name
		if nameCounts[srv.Name] > 1 {
			displayName = srv.Name + " [duplicate-name]"
		}
		row := &workerRow{
			Name:      displayName,
			WorkerID:  srv.ID,
			Subdomain: srv.Subdomain,
			Status:    srv.Status,
			Presence:  "server-only",
		}
		// Only the first server entry for a given ID gets indexed. IDs
		// should be unique server-side; if they aren't, we still render
		// every row but only one wins the "synced" upgrade for a local
		// match (deterministic by iteration order).
		if _, exists := byID[srv.ID]; !exists && srv.ID != "" {
			byID[srv.ID] = row
		}
		rows = append(rows, row)
	}

	// Track local rows already attached to a server row so name-fallback
	// matching doesn't double-count.
	matched := make(map[string]bool, len(local))
	for _, loc := range local {
		// Local configs not bound to this context are noise — skip.
		if loc.Context != ctxName {
			continue
		}
		// Prefer ID match: this is robust against server-side renames
		// and is the dedup key the server itself uses.
		if loc.WorkerID != "" {
			if existing, ok := byID[loc.WorkerID]; ok {
				existing.Presence = "synced"
				matched[loc.Name] = true
				continue
			}
		}
		// Fall back to name match for local entries with no WorkerID
		// (legacy state, or local configs created before a server-side
		// create succeeded). This intentionally does NOT cross
		// duplicate-name server rows; a local config matching a name
		// collision is ambiguous and we leave it as local-only.
		if loc.WorkerID == "" && nameCounts[loc.Name] == 1 {
			for _, row := range rows {
				if row.Name == loc.Name {
					row.Presence = "synced"
					matched[loc.Name] = true
					break
				}
			}
			if matched[loc.Name] {
				continue
			}
		}
		rows = append(rows, &workerRow{
			Name:      loc.Name,
			WorkerID:  loc.WorkerID,
			Subdomain: loc.Subdomain,
			Status:    "",
			Presence:  "local-only",
		})
	}

	out := make([]workerRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(left, right int) bool { return out[left].Name < out[right].Name })
	return out
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
