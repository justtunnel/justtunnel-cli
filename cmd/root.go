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
	"github.com/justtunnel/justtunnel-cli/internal/tunnel"
)

var (
	cfgFile   string
	logLevel  string
	subdomain string
)

var rootCmd = &cobra.Command{
	Use:   "justtunnel [port]",
	Short: "Expose a local HTTP server to the internet",
	Long:  "justtunnel creates a public URL that tunnels traffic to a local port via a persistent WebSocket connection.",
	Args:  cobra.ExactArgs(1),
	RunE:  runTunnel,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/justtunnel/config.yaml)")
	rootCmd.Flags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
	rootCmd.Flags().StringVarP(&subdomain, "subdomain", "s", "", "request a specific subdomain")
}

func Execute() error {
	return rootCmd.Execute()
}

func runTunnel(cmd *cobra.Command, args []string) error {
	port, err := strconv.Atoi(args[0])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port: %s (must be 1-65535)", args[0])
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

	tun := tunnel.New(serverURL, localTarget, cfg.AuthToken, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		errCh <- tun.Run(ctx)
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
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
