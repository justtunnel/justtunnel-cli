package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	// Tee logs to both stderr (for foreground operators) and the per-worker
	// log file (for `worker logs` in #31). The file is opened in append mode
	// with 0600 perms — see worker.OpenLogFile.
	logFile, err := worker.OpenLogFile(name)
	if err != nil {
		return err
	}
	defer logFile.Close()

	logLevel := parseLogLevel(cfg.LogLevel)
	multiWriter := io.MultiWriter(cmd.ErrOrStderr(), logFile)
	logger := slog.New(slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
		Level: logLevel,
	}))

	// signal.NotifyContext gives us a context that's cancelled on the first
	// SIGINT/SIGTERM. The returned `stop` re-arms default signal handling so
	// a SECOND signal during the runner's shutdown produces a hard exit
	// (Go default behavior for SIGINT is exit; for SIGTERM the OS kills the
	// process).
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := &worker.Runner{
		WorkerName: workerCfg.Name,
		WorkerID:   workerCfg.WorkerID,
		Subdomain:  subdomain,
		ServerURL:  cfg.ServerURL,
		AuthToken:  cfg.AuthToken,
		Logger:     logger,
		Dialer:     worker.NewRealDialer(cfg.AuthToken),
	}

	logger.Info("worker starting",
		"worker", workerCfg.Name,
		"context", workerCfg.Context,
		"subdomain", subdomain,
	)

	runErr := runner.Run(ctx)

	// Disambiguate the three exit modes for the operator and the caller:
	//   * context cancelled (SIGINT/SIGTERM) → exit 0
	//   * terminal close code (4403/4409)    → return error so cobra exits 1
	//   * any other error                    → return error
	switch {
	case runErr == nil:
		return nil
	case errors.Is(runErr, context.Canceled):
		logger.Info("worker stopped (signal received)", "worker", workerCfg.Name)
		return nil
	default:
		return runErr
	}
}
