package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/browser"
	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/display"
)

type authVerifyResponse struct {
	Email          string `json:"email"`
	GitHubUsername string `json:"github_username"`
	Plan           string `json:"plan"`
}

type deviceSessionResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	PollInterval    int    `json:"poll_interval"`
}

type deviceStatusResponse struct {
	Status string `json:"status"`
	APIKey string `json:"api_key,omitempty"`
}

var authCmd = &cobra.Command{
	Use:   "auth [key]",
	Short: "Authenticate with justtunnel",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAuth,
}

func init() {
	rootCmd.AddCommand(authCmd)
}

func runAuth(cmd *cobra.Command, args []string) error {
	// If key provided, use legacy flow
	if len(args) == 1 {
		return runKeyAuth(cmd, args[0])
	}
	// No args — device flow
	return runDeviceAuth(cmd)
}

func runKeyAuth(_ *cobra.Command, key string) error {
	if !validateKeyFormat(key) {
		return display.InputError("invalid API key format (must start with justtunnel_)")
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
		return categorizeAuthError(err)
	}

	cfg.AuthToken = key
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	displayName := result.GitHubUsername
	if displayName == "" {
		displayName = result.Email
	}
	fmt.Printf("Authenticated as %s (%s plan).\n", displayName, result.Plan)
	return nil
}

func runDeviceAuth(cmd *cobra.Command) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	baseURL, err := apiBaseURL(cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}

	// Check if already authenticated
	if cfg.AuthToken != "" {
		result, verifyErr := verifyKey(http.DefaultClient, baseURL, cfg.AuthToken)
		if verifyErr == nil {
			displayName := result.GitHubUsername
			if displayName == "" {
				displayName = result.Email
			}
			fmt.Printf("Already authenticated as %s. Re-authenticate? [y/N] ", displayName)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				return nil
			}
		}
	}

	// Create device session
	deviceResp, err := createDeviceSession(http.DefaultClient, baseURL)
	if err != nil {
		return categorizeAuthError(err)
	}

	// Print user code prominently
	fmt.Fprintf(os.Stderr, "\nYour code: %s\n\n", display.Bold(deviceResp.UserCode))

	// Try to open browser
	verifyURL := deviceResp.VerificationURL + "?code=" + deviceResp.UserCode
	if openErr := browser.Open(verifyURL); openErr != nil {
		fmt.Fprintf(os.Stderr, "Open this URL to authenticate:\n  %s\n\n", verifyURL)
	} else {
		fmt.Fprintf(os.Stderr, "Opening browser to authenticate...\n\n")
	}

	authSpinner := display.NewSpinner("Waiting for approval...")
	authSpinner.Start()

	// Poll for approval
	pollInterval := max(time.Duration(deviceResp.PollInterval)*time.Second, 5*time.Second)
	timeout := time.Duration(deviceResp.ExpiresIn) * time.Second

	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	// Handle Ctrl+C
	sigCtx, sigCancel := signal.NotifyContext(ctx, os.Interrupt)
	defer sigCancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigCtx.Done():
			authSpinner.Stop()
			if sigCtx.Err() == context.DeadlineExceeded {
				return display.InputError("authentication timed out. Please run 'justtunnel auth' again")
			}
			return display.InputError("authentication cancelled")
		case <-ticker.C:
			status, pollErr := pollDeviceStatus(http.DefaultClient, baseURL, deviceResp.DeviceCode)
			if pollErr != nil {
				continue // retry on network errors
			}

			switch status.Status {
			case "pending":
				continue
			case "expired":
				authSpinner.Stop()
				return display.InputError("authentication timed out. Please run 'justtunnel auth' again")
			case "approved":
				authSpinner.Stop()

				// Save the key
				cfg.AuthToken = status.APIKey
				if saveErr := config.Save(cfg); saveErr != nil {
					return fmt.Errorf("save config: %w", saveErr)
				}

				// Verify and display user info
				result, verifyErr := verifyKey(http.DefaultClient, baseURL, status.APIKey)
				if verifyErr != nil {
					// Key saved but verify failed — still OK
					fmt.Println("Authenticated successfully.")
					return nil
				}
				displayName := result.GitHubUsername
				if displayName == "" {
					displayName = result.Email
				}
				fmt.Printf("Authenticated as %s (%s plan).\n", displayName, result.Plan)
				return nil
			}
		}
	}
}

func createDeviceSession(client *http.Client, baseURL string) (*deviceSessionResponse, error) {
	req, err := http.NewRequest("POST", baseURL+"/api/auth/device", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach justtunnel server")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("unexpected response: %d", resp.StatusCode)
	}

	var result deviceSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

func pollDeviceStatus(client *http.Client, baseURL string, deviceCode string) (*deviceStatusResponse, error) {
	params := url.Values{"device_code": {deviceCode}}
	req, err := http.NewRequest("GET", baseURL+"/api/auth/device/status?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach justtunnel server")
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("polling too fast")
	}
	if resp.StatusCode == http.StatusNotFound {
		return &deviceStatusResponse{Status: "expired"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response: %d", resp.StatusCode)
	}

	var result deviceStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

func validateKeyFormat(key string) bool {
	return strings.HasPrefix(key, "justtunnel_")
}

func verifyKey(client *http.Client, baseURL, key string) (*authVerifyResponse, error) {
	req, err := http.NewRequest("GET", baseURL+"/api/me", nil)
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

// categorizeAuthError wraps raw errors from verifyKey/createDeviceSession into
// structured CLIError types based on message content.
func categorizeAuthError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "could not reach"):
		return display.NetworkError(msg)
	case strings.Contains(msg, "invalid API key"):
		return display.AuthError(msg)
	default:
		return err
	}
}

// apiBaseURL derives the REST API base URL from the WebSocket server URL.
// e.g. "wss://api.justtunnel.dev/ws" -> "https://api.justtunnel.dev"
func apiBaseURL(serverURL string) (string, error) {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	switch parsedURL.Scheme {
	case "wss":
		parsedURL.Scheme = "https"
	case "ws":
		parsedURL.Scheme = "http"
	}
	parsedURL.Path = ""
	parsedURL.RawQuery = ""
	return parsedURL.String(), nil
}
