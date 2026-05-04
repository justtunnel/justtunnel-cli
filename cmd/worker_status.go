package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/display"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

// supervisorFactory is package-level indirection so tests can inject a
// fake Supervisor without shelling out to launchctl/systemctl. Production
// code calls worker.NewSupervisorForOS, which is build-tagged per OS.
var supervisorFactory = worker.NewSupervisorForOS

// probeTimeout bounds each per-worker supervisor probe. The probes
// themselves are stubs for #32 (instant return), but #34/#35 will replace
// them with shell-outs to launchctl/systemctl; capping individual probes
// keeps the table responsive even if one supervisor hangs.
const probeTimeout = 3 * time.Second

var workerStatusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show server-side and local status for workers in the active team context",
	Long: "Without [name], lists every worker in the active team context with " +
		"server-side status and local supervisor state.\n\n" +
		"With [name], prints a verbose key:value detail view for that worker.\n\n" +
		"Local supervisor probes (launchd/systemd --user) are stubbed in this " +
		"build; full probe support lands with #34 (macOS) and #35 (Linux).",
	Args: cobra.MaximumNArgs(1),
	RunE: runWorkerStatus,
}

func init() {
	workerCmd.AddCommand(workerStatusCmd)
}

// statusRow is the unified server+local view rendered as one table row.
type statusRow struct {
	Name       string
	Server     string // server.Status, "<missing>", "<local-only>"
	Local      string // e.g. "launchd:running", "none", "systemd:probe not yet implemented"
	LastSeenAt string // formatted UTC or "-"
}

// duplicateMarker is appended to Server cells when the server returns more
// than one entry with the same name. Kept short to avoid inflating tabwriter
// column widths.
const duplicateMarker = " DUP"

func runWorkerStatus(cmd *cobra.Command, args []string) error {
	cfg, teamID, ctxName, baseURL, err := loadWorkerEnv()
	if err != nil {
		return err
	}

	serverWorkers, err := fetchWorkers(cmd.Context(), baseURL, cfg.AuthToken, teamID)
	if err != nil {
		return err
	}

	localWorkers, err := worker.List()
	if err != nil {
		return fmt.Errorf("list local workers: %w", err)
	}

	supervisor := supervisorFactory()
	rows := buildStatusRows(cmd.Context(), supervisor, serverWorkers, localWorkers, ctxName)

	out := cmd.OutOrStdout()

	if len(args) == 1 {
		name := args[0]
		for _, row := range rows {
			if row.Name == name {
				writeStatusDetail(out, ctxName, row)
				return nil
			}
		}
		return display.InputError(fmt.Sprintf("worker %q not found in context %q", name, ctxName))
	}

	if len(rows) == 0 {
		fmt.Fprintf(out, "No workers in %s.\n", ctxName)
		return nil
	}
	fmt.Fprintf(out, "Workers in %s:\n", ctxName)
	writeStatusTable(out, rows)
	return nil
}

// buildStatusRows merges server and local sources into a single sorted
// slice keyed by worker name. The merge intentionally uses Name (not
// WorkerID) as the join key because users address workers by name in
// every other CLI surface; collisions are surfaced as a "<duplicate>"
// suffix on the Server cell so they are not silently flattened.
func buildStatusRows(
	ctx context.Context,
	supervisor worker.Supervisor,
	server []workerAPI,
	local []worker.Config,
	ctxName string,
) []statusRow {
	if ctx == nil {
		ctx = context.Background()
	}

	// Count duplicates first so each duplicate row can carry its own
	// "DUP-N/M" suffix without reaching back into a shared row.
	nameCounts := make(map[string]int, len(server))
	for _, srv := range server {
		nameCounts[srv.Name]++
	}

	// uniqueRows holds the single row per non-duplicated name so local
	// configs can attach to it. Duplicate rows are written directly to
	// `allRows` and never mutated again, preserving each entry's own
	// status data so the operator can see the divergence.
	uniqueRows := make(map[string]*statusRow)
	allRows := make([]*statusRow, 0, len(server)+len(local))
	dupSeen := make(map[string]int, len(server))

	for _, srv := range server {
		serverCell := srv.Status
		if serverCell == "" {
			serverCell = "unknown"
		}
		if nameCounts[srv.Name] > 1 {
			dupSeen[srv.Name]++
			serverCell = fmt.Sprintf("%s%s-%d/%d", serverCell, duplicateMarker, dupSeen[srv.Name], nameCounts[srv.Name])
			row := &statusRow{
				Name:       srv.Name,
				Server:     serverCell,
				LastSeenAt: formatLastSeen(srv.LastSeenAt),
			}
			allRows = append(allRows, row)
			continue
		}
		row := &statusRow{
			Name:       srv.Name,
			Server:     serverCell,
			LastSeenAt: formatLastSeen(srv.LastSeenAt),
		}
		uniqueRows[srv.Name] = row
		allRows = append(allRows, row)
	}

	// Attach local configs. For names with duplicate server entries we
	// cannot pick which row "owns" the local probe, so we leave the
	// duplicate rows' Local cells as the default "none" — the operator
	// is expected to resolve the duplication first via the server side.
	for _, loc := range local {
		if loc.Context != ctxName {
			continue
		}
		if existing, ok := uniqueRows[loc.Name]; ok {
			existing.Local = describeLocal(ctx, supervisor, loc)
			continue
		}
		// Local config exists with no matching server entry. Distinguish
		// between an explicitly local-only worker (ServiceBackend empty —
		// foreground via `worker start`) and one whose server entry has
		// gone missing.
		serverCell := "<missing>"
		if loc.ServiceBackend == "" {
			serverCell = "<local-only>"
		}
		row := &statusRow{
			Name:       loc.Name,
			Server:     serverCell,
			Local:      describeLocal(ctx, supervisor, loc),
			LastSeenAt: "-",
		}
		uniqueRows[loc.Name] = row
		allRows = append(allRows, row)
	}

	// Server-only rows still need a Local cell. Without a local config
	// we cannot probe (we don't know which name the supervisor would
	// have used), so report "none".
	for _, row := range allRows {
		if row.Local == "" {
			row.Local = "none"
		}
		if row.Server == "" {
			// Safety net: a row with no server status falls back to
			// "<unknown>" rather than rendering as a blank cell.
			row.Server = "<unknown>"
		}
	}

	out := make([]statusRow, 0, len(allRows))
	for _, row := range allRows {
		out = append(out, *row)
	}
	sort.SliceStable(out, func(left, right int) bool {
		if out[left].Name != out[right].Name {
			return out[left].Name < out[right].Name
		}
		// Preserve insertion order for duplicates by sorting on Server
		// suffix (DUP-1/N before DUP-2/N).
		return out[left].Server < out[right].Server
	})
	return out
}

// describeLocal returns the formatted Local cell for a single local
// config entry. Configs whose ServiceBackend is "none" short-circuit
// without invoking the supervisor (saves the cost of probing for a
// worker the user explicitly started in foreground via `worker start`).
func describeLocal(ctx context.Context, supervisor worker.Supervisor, loc worker.Config) string {
	if loc.ServiceBackend == "none" || loc.ServiceBackend == "" {
		return "none"
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	result, err := supervisor.Probe(probeCtx, loc.Name)
	if err != nil {
		// Probe failure should not crash the table — surface inline.
		return fmt.Sprintf("%s:probe error: %v", loc.ServiceBackend, err)
	}
	backend := result.ServiceBackend
	if backend == "" {
		backend = loc.ServiceBackend
	}
	// Build the local cell from the boolean state first, then optionally
	// append Detail when it adds signal. Ordering matters: a running probe
	// that also carries a Detail string ("started 12s ago") must still
	// render as ":running"; previously the Detail branch shadowed both
	// :running and :stopped.
	var state string
	switch {
	case result.Running:
		state = "running"
	case result.ManagedByUs:
		state = "stopped"
	default:
		state = "not loaded"
	}
	cell := backend + ":" + state
	// Skip detail strings that are pure noise (the stub probe in #32 emits
	// "probe not yet implemented" — keep that one for the existing
	// stub-renders test, but suppress on real states).
	if result.Detail != "" && shouldShowDetail(result.Detail, result.Running, result.ManagedByUs) {
		cell += " (" + result.Detail + ")"
	} else if result.Detail != "" && !result.Running && !result.ManagedByUs {
		// Stub case: no Running, no ManagedByUs — surface Detail directly
		// so "launchd:probe not yet implemented" stays visible until the
		// real probes land.
		cell = backend + ":" + result.Detail
	}
	return cell
}

// shouldShowDetail returns true when the supervisor's Detail string adds
// information on top of the resolved running/stopped state. Used to suppress
// "probe not yet implemented" noise on rows that already report a real state.
func shouldShowDetail(detail string, running, managed bool) bool {
	if detail == "probe not yet implemented" {
		return false
	}
	// Only annotate concrete states (running/stopped). Pure stub rows take
	// the alternative branch in describeLocal.
	return running || managed
}

// formatLastSeen normalizes the server's RFC3339 timestamp to UTC for
// log-grep friendliness. Returns "-" when the field is empty.
func formatLastSeen(raw string) string {
	if raw == "" {
		return "-"
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// Don't drop the data — show the raw value so a malformed
		// server response is still actionable.
		return raw
	}
	return parsed.UTC().Format("2006-01-02 15:04:05Z")
}

func writeStatusTable(out io.Writer, rows []statusRow) {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tSERVER\tLOCAL\tLAST SEEN")
	for _, row := range rows {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", row.Name, row.Server, row.Local, row.LastSeenAt)
	}
	writer.Flush()
}

func writeStatusDetail(out io.Writer, ctxName string, row statusRow) {
	fmt.Fprintf(out, "Worker:    %s\n", row.Name)
	fmt.Fprintf(out, "Context:   %s\n", ctxName)
	fmt.Fprintf(out, "Server:    %s\n", row.Server)
	fmt.Fprintf(out, "Local:     %s\n", row.Local)
	fmt.Fprintf(out, "Last Seen: %s\n", row.LastSeenAt)
}

