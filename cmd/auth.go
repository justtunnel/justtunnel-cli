package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/config"
)

type authVerifyResponse struct {
	Email string `json:"email"`
	Plan  string `json:"plan"`
}

var authCmd = &cobra.Command{
	Use:   "auth <key>",
	Short: "Authenticate with your justtunnel API key",
	Args:  cobra.ExactArgs(1),
	RunE:  runAuth,
}

func init() {
	rootCmd.AddCommand(authCmd)
}

func runAuth(cmd *cobra.Command, args []string) error {
	key := args[0]
	if !validateKeyFormat(key) {
		return fmt.Errorf("invalid API key format (must start with justtunnel_)")
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	baseURL, err := apiBaseURL(cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}

	result, err := verifyKey(http.DefaultClient, baseURL, key)
	if err != nil {
		return err
	}

	cfg.AuthToken = key
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Authenticated as %s (%s).\n", result.Email, result.Plan)
	return nil
}

func validateKeyFormat(key string) bool {
	return strings.HasPrefix(key, "justtunnel_")
}

func verifyKey(client *http.Client, baseURL, key string) (*authVerifyResponse, error) {
	req, err := http.NewRequest("GET", baseURL+"/api/v1/auth/verify", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach justtunnel server")
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("invalid API key")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response: %d", resp.StatusCode)
	}

	var result authVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode verify response: %w", err)
	}

	return &result, nil
}

// apiBaseURL derives the REST API base URL from the WebSocket server URL.
// e.g. "wss://api.justtunnel.dev/ws" → "https://api.justtunnel.dev"
func apiBaseURL(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	}
	u.Path = ""
	u.RawQuery = ""
	return u.String(), nil
}
