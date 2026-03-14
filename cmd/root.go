package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/display"
	"github.com/justtunnel/justtunnel-cli/internal/tunnel"
)

var (
	cfgFile              string
	logLevel             string
	subdomain            string
	maxReconnectAttempts int
)

var rootCmd = &cobra.Command{
	Use:   "justtunnel [port]",
	Short: "Expose a local HTTP server to the internet",
	Long:  "justtunnel creates a public URL that tunnels traffic to a local port via a persistent WebSocket connection.",
	Args: cobra.MaximumNArgs(1),
	RunE: runTunnel,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/justtunnel/config.yaml)")
	rootCmd.Flags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
	rootCmd.Flags().StringVarP(&subdomain, "subdomain", "s", "", "request a specific subdomain")
	rootCmd.Flags().IntVar(&maxReconnectAttempts, "max-reconnect-attempts", 50, "maximum number of reconnection attempts (0 = unlimited)")
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	rootCmd.CompletionOptions.DisableDefaultCmd = true
}

func Execute() error {
	err := rootCmd.Execute()
	if err != nil {
		display.PrintError(err)
	}
	return err
}

func runTunnel(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}

	port, err := strconv.Atoi(args[0])
	if err != nil || port < 1 || port > 65535 {
		return display.InputError(fmt.Sprintf("invalid port: %s (must be 1-65535)", args[0]))
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
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

	localTarget := fmt.Sprintf("http://localhost:%d", port)

	var connectSpinner *display.Spinner
	var reconnectSpinner *display.Spinner

	callbacks := tunnel.Callbacks{
		OnConnecting: func() {
			connectSpinner = display.NewSpinner("Connecting...")
			connectSpinner.Start()
		},
		OnConnected: func(sub, tunnelURL, target string) {
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

	// Apply max reconnect attempts: CLI flag takes precedence, then config file.
	// A config value of 0 means unlimited, which is valid and must not be ignored.
	if cmd.Flags().Changed("max-reconnect-attempts") {
		tun.SetMaxReconnectAttempts(maxReconnectAttempts)
	} else if cfg.MaxReconnectAttempts != nil {
		tun.SetMaxReconnectAttempts(*cfg.MaxReconnectAttempts)
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
