package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/config"
)

type accountResponse struct {
	Email         string `json:"email"`
	Plan          string `json:"plan"`
	ActiveTunnels int    `json:"active_tunnels"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show account status",
	Args:  cobra.NoArgs,
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.AuthToken == "" {
		fmt.Println("Not authenticated. Run: justtunnel auth <key>")
		return nil
	}

	baseURL, err := apiBaseURL(cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}

	req, err := http.NewRequest("GET", baseURL+"/api/v1/account", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach justtunnel server")
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("authentication failed (HTTP %d). Try: justtunnel auth <key>", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server error (HTTP %d)", resp.StatusCode)
	}

	var account accountResponse
	if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Printf("Email:          %s\n", account.Email)
	fmt.Printf("Plan:           %s\n", account.Plan)
	fmt.Printf("Active tunnels: %d\n", account.ActiveTunnels)
	return nil
}
