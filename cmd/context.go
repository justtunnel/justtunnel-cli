package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/display"
)

// membership represents a single team membership returned by the server.
// The CLI only needs the team slug to construct a context name.
type membership struct {
	TeamSlug string `json:"team_slug"`
	TeamName string `json:"team_name,omitempty"`
	Role     string `json:"role,omitempty"`
}

// membershipFetcher fetches team memberships for the authenticated user.
// Production wiring uses fetchMembershipsHTTP; tests inject stubs.
// Returns (memberships, supported). supported=false means the server does
// not yet implement the endpoint and the CLI should fall back to a hint.
type membershipFetcher func(client *http.Client, baseURL, authToken string) ([]membership, bool, error)

// fetchMemberships is the package-level fetcher; tests may swap it.
var fetchMemberships membershipFetcher = fetchMembershipsHTTP

var contextCmd = &cobra.Command{
	Use:   "context",
	Short: "Manage active context (personal or team)",
	Long: "Manage which context the CLI uses for tunnel operations.\n" +
		"Contexts are either 'personal' or 'team:<slug>'.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var contextListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available contexts",
	Args:  cobra.NoArgs,
	RunE:  runContextList,
}

var contextUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Set the active context",
	Args:  cobra.ExactArgs(1),
	RunE:  runContextUse,
}

var contextShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the active context",
	Args:  cobra.NoArgs,
	RunE:  runContextShow,
}

func init() {
	contextCmd.AddCommand(contextListCmd)
	contextCmd.AddCommand(contextUseCmd)
	contextCmd.AddCommand(contextShowCmd)
	rootCmd.AddCommand(contextCmd)
}

func runContextList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	active := config.ResolveContext(cfg, contextOverride)

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Available contexts:")

	marker := func(name string) string {
		if name == active {
			return "* "
		}
		return "  "
	}

	fmt.Fprintf(out, "%s%s\n", marker(config.PersonalContext), config.PersonalContext)

	if cfg.AuthToken == "" {
		fmt.Fprintln(out, "\n(sign in with `justtunnel auth` to list team memberships)")
		return nil
	}

	baseURL, err := apiBaseURL(cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}

	memberships, supported, err := fetchMemberships(http.DefaultClient, baseURL, cfg.AuthToken)
	if err != nil {
		fmt.Fprintf(out, "\n(could not list team memberships: %v)\n", err)
		return nil
	}
	if !supported {
		fmt.Fprintln(out, "\n(team membership listing not yet supported by this server;")
		fmt.Fprintln(out, " use `justtunnel context use team:<slug>` if you know your team slug)")
		return nil
	}

	for _, mem := range memberships {
		name := config.TeamContextPrefix + mem.TeamSlug
		fmt.Fprintf(out, "%s%s", marker(name), name)
		if mem.TeamName != "" {
			fmt.Fprintf(out, "  (%s)", mem.TeamName)
		}
		fmt.Fprintln(out)
	}
	return nil
}

func runContextUse(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := config.ValidateContext(name); err != nil {
		return display.InputError(err.Error())
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := config.SetCurrentContext(cfg, name); err != nil {
		return fmt.Errorf("set context: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Active context set to %s.\n", name)
	return nil
}

func runContextShow(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	active := config.ResolveContext(cfg, contextOverride)
	fmt.Fprintln(cmd.OutOrStdout(), active)
	return nil
}

// fetchMembershipsHTTP calls GET /api/memberships on the server. If the server
// returns 404, it reports supported=false so the caller can degrade gracefully.
func fetchMembershipsHTTP(client *http.Client, baseURL, authToken string) ([]membership, bool, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/memberships", nil)
	if err != nil {
		return nil, false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, true, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Memberships []membership `json:"memberships"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, true, fmt.Errorf("decode response: %w", err)
	}
	return payload.Memberships, true, nil
}
