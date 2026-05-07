package cmd

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

// workerListAll controls whether `worker list` includes quarantined
// rows. Default false hides them so a freshly-deleted worker doesn't
// linger in the listing for 30 days; pass --all to see the soft-deleted
// rows (e.g. before they're permanently removed). See justtunnel-cli#50.
var workerListAll bool

var workerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workers for the active team context",
	Args:  cobra.NoArgs,
	RunE:  runWorkerList,
}

func init() {
	workerListCmd.Flags().BoolVar(&workerListAll, "all", false,
		"include quarantined workers (soft-deleted; permanently removed after 30 days)")
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

	// When --all is set, ask the server to include quarantined workers.
	// The default endpoint strips them entirely (justtunnel-server #170 /
	// F-16) so the billing quota stays accurate; without the explicit
	// query param the --all flag would be a dead toggle (F-20).
	serverWorkers, err := fetchWorkersWithOptions(cmd.Context(), baseURL, cfg.AuthToken, teamID, workerListAll)
	if err != nil {
		return err
	}

	localWorkers, err := worker.List()
	if err != nil {
		return fmt.Errorf("list local workers: %w", err)
	}

	rows := mergeWorkers(serverWorkers, localWorkers, ctxName)
	if !workerListAll {
		// Defense-in-depth: the server already strips quarantined rows for
		// the default listing, but keep the client-side filter so a stale
		// CLI talking to a future server that surfaces quarantined rows
		// without an explicit opt-in still hides them by default.
		rows = filterQuarantined(rows)
	}

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
				// F-07: when the server omits subdomain in its list
				// response, fall back to the locally-derived value
				// rather than rendering "-" for every row. See
				// justtunnel-cli#51.
				if existing.Subdomain == "" {
					existing.Subdomain = loc.Subdomain
				}
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
					if row.Subdomain == "" {
						row.Subdomain = loc.Subdomain
					}
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

// filterQuarantined drops rows that the server has already soft-deleted
// (status=retired_quarantined). The default `worker list` view hides
// these so a freshly-deleted worker doesn't linger for 30 days; pass
// --all to see them. See justtunnel-cli#50.
func filterQuarantined(rows []workerRow) []workerRow {
	out := rows[:0]
	for _, row := range rows {
		if row.Status == "retired_quarantined" {
			continue
		}
		out = append(out, row)
	}
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
