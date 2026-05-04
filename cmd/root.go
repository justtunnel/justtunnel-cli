package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"golang.org/x/term"

	"github.com/justtunnel/justtunnel-cli/internal/browser"
	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/display"
	"github.com/justtunnel/justtunnel-cli/internal/tui"
	"github.com/justtunnel/justtunnel-cli/internal/tunnel"
	"github.com/justtunnel/justtunnel-cli/internal/version"
)

var (
	cfgFile              string
	logLevel             string
	subdomain            string
	password             string
	maxReconnectAttempts int
	tunnelConfigFile     string
	localTimeout         time.Duration
	contextOverride      string
)

var rootCmd = &cobra.Command{
	Use:   "justtunnel [port]",
	Short: "Expose a local HTTP server to the internet",
	Long:  "justtunnel creates a public URL that tunnels traffic to a local port via a persistent WebSocket connection.",
	Args:  cobra.RangeArgs(0, 1),
	RunE:  runTunnel,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/justtunnel/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&contextOverride, "context", "", "context override for this invocation (e.g. personal, team:<slug>)")
	rootCmd.Flags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
	rootCmd.Flags().StringVarP(&subdomain, "subdomain", "s", "", "request a specific subdomain")
	rootCmd.Flags().IntVar(&maxReconnectAttempts, "max-reconnect-attempts", 50, "maximum number of reconnection attempts (0 = unlimited)")
	rootCmd.Flags().StringVar(&tunnelConfigFile, "config-file", "", "YAML config file with tunnel definitions")
	rootCmd.Flags().StringVar(&password, "password", "", "password-protect the tunnel (4-128 chars)")
	rootCmd.Flags().DurationVar(&localTimeout, "local-timeout", tunnel.DefaultLocalTimeout, "per-request timeout when proxying to the local target (e.g. 30s, 1m)")
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	// Wire `--version` flag and a custom template that matches the
	// existing `justtunnel version` subcommand format (3 lines).
	rootCmd.Version = version.Version
	rootCmd.SetVersionTemplate("justtunnel " + version.Version +
		"\n  commit: " + version.Commit +
		"\n  built:  " + version.Date + "\n")

	// Surface the version in `--help` output by appending it to the long
	// description. Cobra's default help template prints Long verbatim, so
	// this puts the version near the top of the output.
	rootCmd.Long = rootCmd.Long + "\n\nVersion: " + version.Version
}

func Execute() error {
	err := rootCmd.Execute()
	if err != nil {
		display.PrintError(err)
	}
	return err
}

func runTunnel(cmd *cobra.Command, args []string) error {
	// Parse port arg if provided
	var port int
	if len(args) > 0 {
		var parseErr error
		port, parseErr = strconv.Atoi(args[0])
		if parseErr != nil || port < 1 || port > 65535 {
			return display.InputError(fmt.Sprintf("invalid port: %s (must be 1-65535)", args[0]))
		}
	}

	// Validate password length if provided
	if password != "" {
		if len(password) < 4 || len(password) > 128 {
			return display.InputError("password must be between 4 and 128 characters")
		}
	}

	// Need at least a port arg or a config file
	if port == 0 && tunnelConfigFile == "" {
		return cmd.Help()
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Auto-authenticate on first run if no auth token is configured.
	if err := ensureAuthenticated(cfg, cmd); err != nil {
		return err
	}

	if !cmd.Flags().Changed("log-level") && cfg.LogLevel != "" {
		logLevel = cfg.LogLevel
	}

	level := parseLogLevel(logLevel)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	serverURL, err := buildServerURL(cfg.ServerURL, subdomain)
	if err != nil {
		return err
	}

	// Fork: TTY gets the Bubble Tea TUI; non-TTY gets the existing single-tunnel flow.
	// Check stdout specifically since Bubble Tea renders there — stderr may still be a TTY.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return runTUI(port, cfg, serverURL, logger, cmd)
	}

	// Non-TTY requires a port arg (single-tunnel mode)
	if port == 0 {
		return display.InputError("port argument is required in non-TTY mode")
	}
	return runNonTTY(port, cfg, serverURL, logger, cmd)
}

// runTUI launches the Bubble Tea TUI with the TunnelManager. This path is used
// when stdout is a terminal. It supports multiple tunnels via slash commands.
func runTUI(port int, cfg *config.Config, serverURL string, logger *slog.Logger, cmd *cobra.Command) error {
	// Fetch plan info to show tunnel limits in the header.
	// If it fails (e.g., no auth token), fall back to free plan defaults.
	planInfo := tui.PlanInfo{Name: "free", MaxTunnels: 1}
	if cfg.AuthToken != "" {
		fetchedPlan, fetchErr := tui.FetchPlanInfo(serverURL, cfg.AuthToken)
		if fetchErr == nil {
			planInfo = fetchedPlan
		} else {
			logger.Warn("could not fetch plan info, using free defaults", "error", fetchErr)
		}
	}

	// Create a factory that produces real tunnel.Tunnel instances
	factory := newTunnelFactory(serverURL, cfg.AuthToken, logger, cmd)

	// Create the tea.Program first, then the manager (which needs the program as sender)
	// We use a deferred setup: create model/program, then wire the manager.
	var program *tea.Program

	// programSender wraps *tea.Program to satisfy the MessageSender interface.
	// We use an indirection because the program isn't created until after the manager.
	sender := &programSender{}

	manager := tui.NewTunnelManager(factory, sender)
	model := tui.NewModelWithManager(manager, planInfo)

	// Load tunnel presets from config file if provided
	var tunnelPresets []tui.TunnelPreset
	if tunnelConfigFile != "" {
		presetConfig, loadErr := tui.LoadConfig(tunnelConfigFile)
		if loadErr != nil {
			return fmt.Errorf("load tunnel config: %w", loadErr)
		}
		tunnelPresets = presetConfig.Tunnels
	}

	// Add the initial port arg tunnel (if provided)
	if port > 0 {
		initialName := fmt.Sprintf(":%d", port)
		model.AddDisplayEntry(port, initialName)
	}

	// Add config file tunnels
	for _, preset := range tunnelPresets {
		// Skip if port arg already covers this port
		if preset.Port == port {
			continue
		}
		displayName := preset.Name
		if displayName == "" {
			displayName = fmt.Sprintf(":%d", preset.Port)
		}
		model.AddDisplayEntry(preset.Port, displayName)
	}

	program = tea.NewProgram(model, tea.WithAltScreen())
	sender.program = program

	// Start the initial tunnel via the manager
	if port > 0 {
		initialName := fmt.Sprintf(":%d", port)
		if addErr := manager.Add(port, initialName, subdomain, password); addErr != nil {
			return fmt.Errorf("start initial tunnel: %w", addErr)
		}
	}

	// Start config file tunnels
	for _, preset := range tunnelPresets {
		if preset.Port == port {
			continue
		}
		if addErr := manager.Add(preset.Port, preset.Name, preset.Subdomain, preset.Password); addErr != nil {
			logger.Warn("could not start config tunnel", "port", preset.Port, "error", addErr)
		}
	}

	// Start config file watcher for hot-reload
	if tunnelConfigFile != "" {
		watcher, watchErr := tui.NewConfigWatcher(tunnelConfigFile, manager, sender)
		if watchErr != nil {
			logger.Warn("could not start config watcher", "error", watchErr)
		} else {
			watcher.Start()
			defer watcher.Stop()
		}
	}

	// Run Bubble Tea — it handles Ctrl+C internally and delegates to manager.Shutdown()
	_, runErr := program.Run()
	if runErr != nil {
		// TUI initialization failed (e.g., terminal not supported, alt screen rejected).
		// Shut down the manager's tunnels so we can fall back to the non-interactive path.
		manager.Shutdown()
		logger.Warn("TUI could not start, falling back to non-interactive mode", "error", runErr)
		fmt.Fprintf(os.Stderr, "Warning: TUI failed to start (%v), falling back to single-tunnel mode\n", runErr)
		return runNonTTY(port, cfg, serverURL, logger, cmd)
	}

	return nil
}

// programSender wraps a *tea.Program to satisfy the tui.MessageSender interface.
// This indirection is needed because the program must be created after the model,
// but the manager (which needs the sender) is created before the model.
type programSender struct {
	program *tea.Program
}

func (ps *programSender) Send(msg tea.Msg) {
	if ps.program != nil {
		ps.program.Send(msg)
	}
}

// newTunnelFactory creates a tui.TunnelFactory that produces real tunnel.Tunnel
// instances wired to the TUI callback system.
func newTunnelFactory(serverURL, authToken string, logger *slog.Logger, cmd *cobra.Command) tui.TunnelFactory {
	return func(port int, name string, tunnelSubdomain string, tunnelPassword string, callbacks tui.TunnelCallbacks) tui.TunnelRunner {
		localTarget := fmt.Sprintf("http://localhost:%d", port)

		// Build the server URL with an optional subdomain for this tunnel
		dialURL := serverURL
		if tunnelSubdomain != "" {
			if built, buildErr := buildServerURL(serverURL, tunnelSubdomain); buildErr == nil {
				dialURL = built
			}
		}

		// Bridge TUI callbacks to tunnel.Callbacks
		tunnelCallbacks := tunnel.Callbacks{
			OnConnected: func(sub, tunnelURL, target string, passwordProtected bool) {
				if callbacks.OnConnected != nil {
					callbacks.OnConnected(sub, tunnelURL, target, passwordProtected)
				}
			},
			OnRequest: func(method, path string, status int, latency time.Duration) {
				if callbacks.OnRequest != nil {
					callbacks.OnRequest(method, path, status, latency)
				}
			},
			OnReconnecting: func(attempt int, backoff time.Duration) {
				if callbacks.OnReconnecting != nil {
					callbacks.OnReconnecting(attempt, backoff)
				}
			},
			OnDisconnected: func(timestamp time.Time) {
				if callbacks.OnDisconnected != nil {
					callbacks.OnDisconnected(timestamp)
				}
			},
			OnReconnected: func(info tunnel.ReconnectInfo) {
				if callbacks.OnReconnected != nil {
					callbacks.OnReconnected(
						info.Subdomain,
						info.PreviousSubdomain,
						info.TunnelURL,
						info.SubdomainChanged,
					)
				}
			},
		}

		tun := tunnel.New(dialURL, localTarget, authToken, logger, tunnelCallbacks)

		if tunnelPassword != "" {
			tun.SetPassword(tunnelPassword)
		}

		// Apply max reconnect attempts
		if cmd.Flags().Changed("max-reconnect-attempts") {
			tun.SetMaxReconnectAttempts(maxReconnectAttempts)
		}

		if cmd.Flags().Changed("local-timeout") {
			tun.SetLocalTimeout(localTimeout)
		}

		return tun
	}
}

// runNonTTY is the original single-tunnel flow for non-terminal output (pipes, etc.).
// This code path is UNCHANGED from the pre-TUI implementation to avoid regressions.
func runNonTTY(port int, cfg *config.Config, serverURL string, logger *slog.Logger, cmd *cobra.Command) error {
	localTarget := fmt.Sprintf("http://localhost:%d", port)

	var connectSpinner *display.Spinner
	var reconnectSpinner *display.Spinner

	callbacks := tunnel.Callbacks{
		OnConnecting: func() {
			connectSpinner = display.NewSpinner("Connecting...")
			connectSpinner.Start()
		},
		OnConnected: func(sub, tunnelURL, target string, passwordProtected bool) {
			if connectSpinner != nil {
				connectSpinner.Stop()
				connectSpinner = nil
			}
			display.PrintBanner(sub, tunnelURL, target)
		},
		OnRequest: func(method, path string, status int, latency time.Duration) {
			display.LogRequest(method, path, status, latency)
		},
		OnReconnecting: func(attempt int, backoff time.Duration) {
			if reconnectSpinner != nil {
				reconnectSpinner.Stop()
			}
			msg := fmt.Sprintf("Reconnecting (attempt %d, next try in %s)...", attempt, backoff.Round(time.Second))
			reconnectSpinner = display.NewSpinner(msg)
			reconnectSpinner.Start()
		},
		OnReconnectWait: func(attempt int, remaining time.Duration) {
			if reconnectSpinner != nil {
				msg := fmt.Sprintf("Reconnecting (attempt %d, next try in %s)...", attempt, remaining.Round(time.Second))
				reconnectSpinner.Update(msg)
			}
		},
		OnDisconnected: func(timestamp time.Time) {
			if reconnectSpinner != nil {
				reconnectSpinner.Stop()
				reconnectSpinner = nil
			}
			display.PrintDisconnected(timestamp)
		},
		OnReconnected: func(info tunnel.ReconnectInfo) {
			if reconnectSpinner != nil {
				reconnectSpinner.Stop()
				reconnectSpinner = nil
			}
			display.PrintReconnected(
				info.Subdomain,
				info.PreviousSubdomain,
				info.TunnelURL,
				info.LocalTarget,
				info.SubdomainChanged,
				info.DowntimeDuration,
			)
		},
	}

	tun := tunnel.New(serverURL, localTarget, cfg.AuthToken, logger, callbacks)

	if password != "" {
		tun.SetPassword(password)
	}

	// Apply max reconnect attempts: CLI flag takes precedence, then config file.
	// A config value of 0 means unlimited, which is valid and must not be ignored.
	if cmd.Flags().Changed("max-reconnect-attempts") {
		tun.SetMaxReconnectAttempts(maxReconnectAttempts)
	} else if cfg.MaxReconnectAttempts != nil {
		tun.SetMaxReconnectAttempts(*cfg.MaxReconnectAttempts)
	}

	if cmd.Flags().Changed("local-timeout") {
		tun.SetLocalTimeout(localTimeout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		errCh <- tun.Run(ctx)
	}()

	stopAllSpinners := func() {
		if connectSpinner != nil {
			connectSpinner.Stop()
			connectSpinner = nil
		}
		if reconnectSpinner != nil {
			reconnectSpinner.Stop()
			reconnectSpinner = nil
		}
	}

	select {
	case err := <-errCh:
		stopAllSpinners()
		return err
	case sig := <-sigCh:
		stopAllSpinners()
		logger.Info("received signal, shutting down", "signal", sig)
		cancel()
		tun.Shutdown(5 * time.Second)
		return nil
	}
}

func buildServerURL(baseURL, sub string) (string, error) {
	if sub == "" {
		return baseURL, nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}
	query := parsed.Query()
	query.Set("subdomain", sub)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ensureAuthenticated checks for an auth token and triggers device auth if missing.
// In interactive terminals, it auto-starts the browser-based sign-in flow so the user
// can go from `justtunnel 3000` to a working tunnel without running `auth` first.
func ensureAuthenticated(cfg *config.Config, cmd *cobra.Command) error {
	if cfg.AuthToken != "" {
		return nil
	}

	// Non-interactive: can't run device auth, tell the user what to do.
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return display.AuthError("not authenticated. Set JUSTTUNNEL_AUTH_TOKEN or run `justtunnel auth` first")
	}

	baseURL, err := apiBaseURL(cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\n  Welcome to justtunnel! Sign in with GitHub to get started.\n\n")

	deviceResp, err := createDeviceSession(http.DefaultClient, baseURL)
	if err != nil {
		return categorizeAuthError(err)
	}

	fmt.Fprintf(os.Stderr, "  Your code: %s\n\n", display.Bold(deviceResp.UserCode))

	verifyURL := deviceResp.VerificationURL + "?code=" + deviceResp.UserCode
	if openErr := browser.Open(verifyURL); openErr != nil {
		fmt.Fprintf(os.Stderr, "  Open this URL to authenticate:\n    %s\n\n", verifyURL)
	} else {
		fmt.Fprintf(os.Stderr, "  Opening browser to authenticate...\n\n")
	}

	authSpinner := display.NewSpinner("Waiting for approval...")
	authSpinner.Start()

	pollInterval := max(time.Duration(deviceResp.PollInterval)*time.Second, 5*time.Second)
	timeout := time.Duration(deviceResp.ExpiresIn) * time.Second

	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	sigCtx, sigCancel := signal.NotifyContext(ctx, os.Interrupt)
	defer sigCancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigCtx.Done():
			authSpinner.Stop()
			if ctx.Err() == context.DeadlineExceeded {
				return display.InputError("authentication timed out. Run 'justtunnel auth' to try again")
			}
			return display.InputError("authentication cancelled")
		case <-ticker.C:
			status, pollErr := pollDeviceStatus(http.DefaultClient, baseURL, deviceResp.DeviceCode)
			if pollErr != nil {
				continue
			}

			switch status.Status {
			case "pending":
				continue
			case "expired":
				authSpinner.Stop()
				return display.InputError("authentication timed out. Run 'justtunnel auth' to try again")
			case "approved":
				authSpinner.Stop()

				cfg.AuthToken = status.APIKey
				if saveErr := config.Save(cfg); saveErr != nil {
					return fmt.Errorf("save config: %w", saveErr)
				}

				result, verifyErr := verifyKey(http.DefaultClient, baseURL, status.APIKey)
				if verifyErr != nil {
					fmt.Fprintf(os.Stderr, "\n  Authenticated successfully. Starting tunnel...\n\n")
					return nil
				}
				displayName := result.GitHubUsername
				if displayName == "" {
					displayName = result.Email
				}
				fmt.Fprintf(os.Stderr, "\n  Authenticated as %s (%s plan). Starting tunnel...\n\n", displayName, result.Plan)
				return nil
			}
		}
	}
}
