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
			"worker uninstall is not supported on %s; remove the local config manually with `justtunnel worker rm %s`",
			runtime.GOOS, name,
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

	// Track whether anything actually changed so we can render the
	// "already uninstalled" message when re-running on a clean state.
	changed := false

	// Step 1: Service teardown. Idempotent in both per-OS impls (a
	// missing service is a successful no-op).
	unbootErr := svc.Unbootstrap(cmd.Context(), name)
	if unbootErr != nil {
		stepErr := &uninstallStepError{step: "service teardown", err: unbootErr}
		if !workerUninstallForce {
			return stepErr
		}
		stepErrors = append(stepErrors, stepErr)
	}
	// We can't distinguish "removed" from "was already gone" at the
	// installer layer (Unbootstrap is idempotent and reports neither),
	// so we let the on-disk config Read below be the authoritative
	// "anything to do?" signal.

	// Step 2: Local config removal. Idempotent — Read first so we can
	// honestly report "nothing to do" when there was nothing to delete.
	if _, readErr := worker.Read(name); readErr == nil {
		if delErr := worker.Delete(name); delErr != nil && !errors.Is(delErr, os.ErrNotExist) {
			stepErr := &uninstallStepError{step: "local config removal", err: delErr}
			if !workerUninstallForce {
				return stepErr
			}
			stepErrors = append(stepErrors, stepErr)
		} else {
			changed = true
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		// Read failed with something other than "not found" (e.g.
		// permission denied, malformed JSON). Try to delete anyway —
		// the operator's intent is to clean up — and surface the read
		// error only if delete also fails.
		if delErr := worker.Delete(name); delErr != nil && !errors.Is(delErr, os.ErrNotExist) {
			stepErr := &uninstallStepError{step: "local config removal", err: fmt.Errorf("read=%v delete=%w", readErr, delErr)}
			if !workerUninstallForce {
				return stepErr
			}
			stepErrors = append(stepErrors, stepErr)
		} else {
			changed = true
		}
	}

	// Step 3 (optional): server-side delete.
	if workerUninstallDeleteOnServer {
		if deleteErr := uninstallServerSide(baseURL, authToken, teamID, name, cmd.OutOrStdout()); deleteErr != nil {
			stepErr := &uninstallStepError{step: "server-side delete", err: deleteErr}
			if !workerUninstallForce {
				return stepErr
			}
			stepErrors = append(stepErrors, stepErr)
		} else {
			// We deliberately do NOT flip `changed` here on its own;
			// "deleted server-side but local was already gone" is
			// still a meaningful change, so consider it a change.
			changed = true
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

// uninstallServerSide mirrors the Mode-2 logic from worker_rm.go: GET the
// list, find by NAME (not by potentially-stale local ID), and DELETE by
// the server's authoritative ID. A 404 from DELETE — or a name that's
// already absent from the list — is treated as already-deleted and
// returns nil.
func uninstallServerSide(baseURL, authToken, teamID, name string, _ io.Writer) error {
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
	if _, err := deleteWorker(baseURL, authToken, teamID, workerID); err != nil {
		return err
	}
	return nil
}

// probePostUninstall runs a single supervisor probe and warns on stderr
// if the worker still appears managed/running. Failures (probe error,
// "still running" state) are informational only — see the plan note
// about supervisor teardown timing.
func probePostUninstall(ctx context.Context, name string, errOut io.Writer) {
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
