package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/display"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

var workerStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Run the worker WebSocket attach loop in the foreground",
	Long: "Runs a worker tunnel in the foreground with auto-reconnect.\n" +
		"This is the same code path the supervisor uses (#34/#35); use it directly\n" +
		"to debug a worker, or wire it into your own process supervisor.\n\n" +
		"The command blocks until SIGINT/SIGTERM (graceful shutdown) or until the\n" +
		"server closes the session with a terminal status (suspended / already attached).",
	Args: cobra.ExactArgs(1),
	RunE: runWorkerStart,
}

func init() {
	workerCmd.AddCommand(workerStartCmd)
}

func runWorkerStart(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.AuthToken == "" {
		return display.AuthError("not signed in — run `justtunnel auth` first")
	}

	// Load the per-worker config written by `worker create` (#29). If it's
	// missing we refuse to dial — auto-creating here would silently bypass
	// the team's worker quota and the server-side validation that runs
	// during create.
	workerCfg, err := worker.Read(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return display.InputError(fmt.Sprintf(
				"no local config for worker %q — run `justtunnel worker create %s` first",
				name, name,
			))
		}
		return err
	}

	subdomain, err := worker.DeriveSubdomain(workerCfg.Name, workerCfg.Context)
	if err != nil {
		return err
	}

	// Logs go to stderr only. The platform supervisor (launchd on macOS,
	// systemd-user on Linux) is configured to redirect stderr to the
	// per-worker log file at ~/.justtunnel/logs/worker-<name>.log, which
	// is what `worker logs` reads. Writing to the file in-process AND
	// having the supervisor capture stderr to the same path produced
	// duplicate lines in the log file (F-10) — see justtunnel-cli#51.
	//
	// Foreground use without a supervisor (`justtunnel worker start
	// <name>` directly) emits to stderr only; that's intentional, since
	// the operator is watching the terminal in that mode.
	logLevel := parseLogLevel(cfg.LogLevel)
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
		Level: logLevel,
	}))

	// signal.NotifyContext gives us a context that's cancelled on the first
	// SIGINT/SIGTERM. The returned `stop` re-arms default signal handling so
	// a SECOND signal during the runner's shutdown produces a hard exit
	// (Go default behavior for SIGINT is exit; for SIGTERM the OS kills the
	// process).
	//
	// Note re: tech spec "wait up to 5s for in-flight requests" — this does
	// not apply to workers. Workers are pure WebSocket attach with no
	// port-forwarded HTTP traffic to drain; that wording is borrowed from
	// the tunnel-mode spec and is N/A here.
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := worker.NewRunner(
		worker.RunnerIdentity{
			WorkerName: workerCfg.Name,
			WorkerID:   workerCfg.WorkerID,
			Subdomain:  subdomain,
			ServerURL:  cfg.ServerURL,
		},
		worker.RunnerDeps{
			Logger: logger,
			Dialer: worker.NewRealDialer(cfg.AuthToken),
		},
	)

	logger.Info("worker starting",
		"worker", workerCfg.Name,
		"context", workerCfg.Context,
		"subdomain", subdomain,
	)

	runErr := runner.Run(ctx)

	// Disambiguate the four exit modes for the operator and the caller:
	//   * context cancelled (SIGINT/SIGTERM) → exit 0
	//   * 403 forbidden                      → display.ForbiddenError (no re-auth misdirection)
	//   * 401 / terminal close code          → return error so cobra exits 1
	//   * any other error                    → return error
	switch {
	case runErr == nil:
		return nil
	case errors.Is(runErr, context.Canceled):
		logger.Info("worker stopped (signal received)", "worker", workerCfg.Name)
		return nil
	case errors.Is(runErr, worker.ErrForbidden):
		// Most common cause for a worker 403 is creating the worker via
		// `worker create` (no service token) and then trying to start
		// it directly. Steer the operator there instead of suggesting
		// re-auth, which won't help. See justtunnel-cli#47.
		return display.ForbiddenError(
			fmt.Sprintf("worker %q rejected by server (403)", workerCfg.Name),
			fmt.Sprintf("This is not an authentication problem. If %[1]q was created with `worker create`, run `justtunnel worker install %[1]s` to provision a service token. Other causes: the team is suspended or your membership was revoked.", workerCfg.Name),
		)
	default:
		return runErr
	}
}
