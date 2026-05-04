package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/display"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

// workerUninstallDeleteOnServer is the bound flag value for `worker
// uninstall --delete-on-server`. Cobra preserves bound flag values
// across Execute() calls in the same process, so resetWorkerState
// (cmd/worker_test.go) zeroes this between tests.
var workerUninstallDeleteOnServer bool

// workerUninstallForce is the bound flag value for `worker uninstall
// --force`. With --force each step's error is collected, all steps
// run to completion, errors are printed to stderr but the command
// exits 0 (best-effort cleanup; useful when local config is corrupt).
var workerUninstallForce bool

var workerUninstallCmd = &cobra.Command{
	Use:   "uninstall <name>",
	Short: "Uninstall a managed worker (stops the service and removes local config)",
	Long: "Reverses `worker install`: stops the platform service, removes the local\n" +
		"service definition, and deletes the local worker config. With\n" +
		"--delete-on-server, also deletes the server-side worker record.\n\n" +
		"Idempotent: re-running on an already-uninstalled worker is a no-op.\n" +
		"Use --force to continue past individual step failures (best-effort cleanup).",
	Args: cobra.ExactArgs(1),
	RunE: runWorkerUninstall,
}

func init() {
	workerUninstallCmd.Flags().BoolVar(&workerUninstallDeleteOnServer, "delete-on-server", false,
		"also delete the server-side worker record (off by default)")
	workerUninstallCmd.Flags().BoolVar(&workerUninstallForce, "force", false,
		"continue past per-step failures and print warnings to stderr (best-effort cleanup)")
	workerCmd.AddCommand(workerUninstallCmd)
}

// uninstallStepError tags an error with the step name so --force callers
// can render a readable summary on stderr.
type uninstallStepError struct {
	step string
	err  error
}

func (e *uninstallStepError) Error() string {
	return fmt.Sprintf("%s: %v", e.step, e.err)
}

func (e *uninstallStepError) Unwrap() error { return e.err }

func runWorkerUninstall(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := worker.ValidateName(name); err != nil {
		return display.InputError(err.Error())
	}

	// OS dispatch FIRST. Windows has no managed-service path so
	// `worker uninstall` cannot do anything useful — direct the user
	// at `worker rm` for local cleanup. We fail before any HTTP call
	// so a Windows operator never makes a pointless server round-trip.
	svc, err := newServiceInstaller(runtime.GOOS)
	if err != nil {
		// newServiceInstaller's only failure mode is "this OS has no
		// supervisor adapter" (Windows + anything non-darwin/linux).
		// The install-flavored message it returns recommends `worker
		// start`; uninstall's analogous recommendation is `worker rm`,
		// so we substitute a tailored error rather than passing the
		// install message through.
		return display.InputError(fmt.Sprintf(
			"worker uninstall is not supported on %s; remove the local config with `justtunnel worker rm %s` (and `justtunnel worker rm %s --delete-on-server` to also remove the server record)",
			runtime.GOOS, name, name,
		))
	}

	// If --delete-on-server is set we need a team context + auth. Without
	// it, only local cleanup is needed and we deliberately skip the env
	// load so an operator can uninstall after losing team membership.
	// loadWorkerEnv also rejects personal contexts up-front, satisfying
	// the personal-context guard for the optional HTTP path.
	var authToken, teamID, baseURL string
	if workerUninstallDeleteOnServer {
		cfg, slug, _, apiBase, envErr := loadWorkerEnv()
		if envErr != nil {
			return envErr
		}
		authToken = cfg.AuthToken
		teamID = slug
		baseURL = apiBase
	}

	// Track per-step errors so --force can summarize at the end.
	var stepErrors []error

	// `changed` is true if any step (service uninstall, local delete,
	// server delete) actually mutated state. We use this to render
	// "already uninstalled" when re-running on a clean state. Note:
	// service Unbootstrap is idempotent and reports neither "removed"
	// nor "was already gone", so it does not flip `changed` on its own;
	// the local Read+Delete and the server-side delete are the
	// authoritative mutation signals.
	changed := false

	// Branch on --delete-on-server: when set (and NOT --force), we run
	// the server-side preflight + DELETE FIRST so a 403 (or other
	// server-side failure) aborts BEFORE we touch local state. This
	// preserves the operator's pointer to the worker so they can re-run
	// with a permitted account. With --force we keep best-effort
	// behavior — every step runs and errors are collected for a stderr
	// summary. Without --delete-on-server we run service teardown +
	// local delete only (server is not in play).
	if workerUninstallDeleteOnServer && !workerUninstallForce {
		// Server FIRST so a 403 leaves local state intact.
		if serverErr := uninstallServerSide(baseURL, authToken, teamID, name, cmd.ErrOrStderr()); serverErr != nil {
			return &uninstallStepError{step: "server-side delete", err: serverErr}
		}
		// Server delete (or already-absent) succeeded; mark changed so
		// "deleted server-side, local was already gone" still reports
		// success rather than "already uninstalled".
		changed = true

		// Now safe to tear down local state.
		if unbootErr := svc.Unbootstrap(cmd.Context(), name); unbootErr != nil {
			return &uninstallStepError{
				step: "service teardown",
				err:  fmt.Errorf("stop supervisor: %w (retry with --force to continue and clean up local state)", unbootErr),
			}
		}
		if localChanged, localErr := removeLocalConfig(name); localErr != nil {
			return &uninstallStepError{step: "local config removal", err: localErr}
		} else if localChanged {
			changed = true
		}
	} else {
		// Two cases land here:
		//   1. --delete-on-server NOT set → local-only path.
		//   2. --delete-on-server set AND --force → best-effort: run
		//      every step, collect errors, exit 0.
		// In both cases we run service teardown → local delete in
		// order. Under --force the server step (if requested) runs
		// last so a server failure can be reported alongside the
		// local-cleanup outcome.

		// Step 1: Service teardown. Idempotent in both per-OS impls (a
		// missing service is a successful no-op).
		if unbootErr := svc.Unbootstrap(cmd.Context(), name); unbootErr != nil {
			stepErr := &uninstallStepError{
				step: "service teardown",
				err:  fmt.Errorf("stop supervisor: %w (retry with --force to continue and clean up local state)", unbootErr),
			}
			if !workerUninstallForce {
				return stepErr
			}
			stepErrors = append(stepErrors, stepErr)
		}

		// Step 2: Local config removal. Idempotent — Read first so we
		// can honestly report "nothing to do" when there was nothing
		// to delete.
		if localChanged, localErr := removeLocalConfig(name); localErr != nil {
			stepErr := &uninstallStepError{step: "local config removal", err: localErr}
			if !workerUninstallForce {
				return stepErr
			}
			stepErrors = append(stepErrors, stepErr)
		} else if localChanged {
			changed = true
		}

		// Step 3 (optional, --force only here since the no-force
		// server path is handled in the branch above): server-side
		// delete.
		if workerUninstallDeleteOnServer {
			if deleteErr := uninstallServerSide(baseURL, authToken, teamID, name, cmd.ErrOrStderr()); deleteErr != nil {
				stepErr := &uninstallStepError{step: "server-side delete", err: deleteErr}
				// We only reach this branch under --force, so collect
				// rather than return.
				stepErrors = append(stepErrors, stepErr)
			} else {
				changed = true
			}
		}
	}

	// Step 4: post-uninstall probe. Best-effort warning only — the
	// supervisor may take a beat to fully tear down. We intentionally
	// do NOT fail on a still-running probe; an operator who needs a
	// hard guarantee can re-run with --force after a moment.
	probePostUninstall(cmd.Context(), name, cmd.ErrOrStderr())

	// --force summary: print each collected error to stderr and
	// continue exiting 0 so callers can drive cleanup from scripts.
	if workerUninstallForce && len(stepErrors) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: --force completed uninstall with %d step error(s):\n", len(stepErrors))
		for _, stepErr := range stepErrors {
			fmt.Fprintf(cmd.ErrOrStderr(), "  - %v\n", stepErr)
		}
	}

	if !changed {
		fmt.Fprintf(cmd.OutOrStdout(), "worker %q already uninstalled\n", name)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Uninstalled worker %q\n", name)
	return nil
}

// removeLocalConfig encapsulates the Read-then-Delete dance so both
// branches of runWorkerUninstall can share the same idempotent
// semantics. Returns (changed=true, nil) if it actually deleted
// something, (changed=false, nil) if there was nothing to delete, or
// (false, err) on failure.
func removeLocalConfig(name string) (bool, error) {
	if _, readErr := worker.Read(name); readErr == nil {
		if delErr := worker.Delete(name); delErr != nil && !errors.Is(delErr, os.ErrNotExist) {
			return false, delErr
		}
		return true, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		// Read failed with something other than "not found" (e.g.
		// permission denied, malformed JSON). Try to delete anyway —
		// the operator's intent is to clean up — and surface the read
		// error only if delete also fails.
		if delErr := worker.Delete(name); delErr != nil && !errors.Is(delErr, os.ErrNotExist) {
			return false, fmt.Errorf("read=%v delete=%w", readErr, delErr)
		}
		return true, nil
	}
	// Read returned os.ErrNotExist → nothing to do.
	return false, nil
}

// uninstallServerSide mirrors the Mode-2 logic from worker_rm.go: GET the
// list, find by NAME (not by potentially-stale local ID), and DELETE by
// the server's authoritative ID. A 404 from DELETE — or a name that's
// already absent from the list — is treated as already-deleted; in the
// 404 case we log to stderr so the operator knows what happened.
func uninstallServerSide(baseURL, authToken, teamID, name string, warnOut io.Writer) error {
	workers, err := fetchWorkers(baseURL, authToken, teamID)
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
		// Server does not know about this worker; treat as already-deleted.
		return nil
	}
	if warnOut != nil {
		fmt.Fprintf(warnOut, "deleting server-side worker %q (id %s)\n", name, workerID)
	}
	notFound, err := deleteWorker(baseURL, authToken, teamID, workerID)
	if err != nil {
		return err
	}
	if notFound && warnOut != nil {
		// Race: GET saw the worker, DELETE returned 404. Surface it so
		// the operator understands why local cleanup proceeded.
		fmt.Fprintf(warnOut, "worker %q already deleted server-side; cleaning up locally\n", name)
	}
	return nil
}

// probePostUninstall runs a single supervisor probe and warns on stderr
// if the worker still appears managed/running. Failures (probe error,
// "still running" state) are informational only — see the plan note
// about supervisor teardown timing.
func probePostUninstall(ctx context.Context, name string, errOut io.Writer) {
	// If the parent context is already cancelled (e.g. clean Ctrl-C),
	// skip the probe so we don't print a confusing "probe failed: context
	// canceled" line after an intentional shutdown.
	if ctx.Err() != nil {
		return
	}
	supervisor := supervisorFactory()
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	result, err := supervisor.Probe(probeCtx, name)
	if err != nil {
		// Probe failure is not actionable in the uninstall flow —
		// the operator's intent has already been carried out — so
		// downgrade to a stderr note instead of failing the command.
		fmt.Fprintf(errOut,
			"note: post-uninstall probe failed (%v); supervisor may still be tearing down\n", err)
		return
	}
	if result.ManagedByUs || result.Running {
		fmt.Fprintf(errOut,
			"warning: worker %q still appears %s after uninstall (backend=%s); the supervisor may need a moment to fully tear down — re-check with `justtunnel worker status`\n",
			name, postUninstallState(result), result.ServiceBackend)
	}
}

// postUninstallState renders a short label describing the residual
// state seen by the post-uninstall probe.
func postUninstallState(result worker.ProbeResult) string {
	switch {
	case result.Running:
		return "running"
	case result.ManagedByUs:
		return "loaded"
	default:
		return "present"
	}
}
