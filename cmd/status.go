package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/display"
)

type tunnelInfo struct {
	ID        string `json:"id"`
	Subdomain string `json:"subdomain"`
	CreatedAt string `json:"created_at"`
}

type meResponse struct {
	Email              string       `json:"email"`
	GithubUsername     string       `json:"github_username"`
	Plan               string       `json:"plan"`
	IsPlatformAdmin    bool         `json:"is_platform_admin"`
	Tunnels            []tunnelInfo `json:"tunnels"`
	ReservedSubdomains []string     `json:"reserved_subdomains"`
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
		return display.AuthError("not authenticated")
	}

	baseURL, err := apiBaseURL(cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}

	req, err := http.NewRequest("GET", baseURL+"/api/me", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return display.NetworkError("could not reach justtunnel server")
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return display.AuthError(fmt.Sprintf("authentication failed (HTTP %d)", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return display.ServerError(fmt.Sprintf("server error (HTTP %d)", resp.StatusCode))
	}

	var account meResponse
	if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Printf("Email:          %s\n", account.Email)
	fmt.Printf("Plan:           %s\n", account.Plan)
	fmt.Printf("Active tunnels: %d\n", len(account.Tunnels))
	return nil
}
