package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/display"
)

// workerAPI mirrors the JSON shape returned by the server for a single
// worker. Fields the CLI does not consume are deliberately omitted to keep
// the contract minimal and tolerant of additive server changes.
type workerAPI struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TeamID    string `json:"team_id"`
	Subdomain string `json:"subdomain,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	// LastSeenAt is RFC3339; the server may not yet emit it (see
	// server/internal/db/workers.go #43 follow-up). When absent the CLI
	// renders "-" so missing telemetry doesn't look like an error.
	LastSeenAt string `json:"last_seen_at,omitempty"`
}

// workerListResponse matches GET /api/teams/{teamID}/workers.
type workerListResponse struct {
	Workers []workerAPI `json:"workers"`
}

// httpTimeout matches the 10s timeout used by other subcommands so behavior
// is consistent against an unresponsive server. Declared as a var (not const)
// so tests can shrink it to keep timeout cases fast.
var httpTimeout = 10 * time.Second

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Manage worker tunnels (team contexts only)",
	Long: "Manage long-lived worker tunnels owned by a team.\n" +
		"Worker commands require a team context — switch with `justtunnel context use team:<slug>`.\n\n" +
		"On Windows, see docs/windows-recipe.md for the Task Scheduler recipe.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(workerCmd)
}

// resolveTeamID returns the team slug from the active context, or an error
// if the context is personal/invalid. The server's Team.ID is the slug, so
// no further translation is needed.
func resolveTeamID(cfg *config.Config) (string, string, error) {
	active := config.ResolveContext(cfg, contextOverride)
	if active == config.PersonalContext {
		return "", "", display.InputError(
			"worker commands require a team context — switch with `justtunnel context use team:<slug>`",
		)
	}
	if !strings.HasPrefix(active, config.TeamContextPrefix) {
		return "", "", display.InputError(fmt.Sprintf("unsupported context %q for worker commands", active))
	}
	slug := strings.TrimPrefix(active, config.TeamContextPrefix)
	if slug == "" {
		return "", "", display.InputError("team context has empty slug")
	}
	// Re-validate the resolved context before we use the slug to construct
	// REST URLs. ResolveContext can return values that came from the
	// --context flag or a hand-edited config file; without this check a
	// malformed slug like "foo:bar" or "UPPER" would land verbatim in the
	// path. Use ValidateContext on the full "team:<slug>" form so the
	// regex/charset rules stay in one place.
	if err := config.ValidateContext(active); err != nil {
		return "", "", display.InputError(fmt.Sprintf(
			"invalid team context %q: %v — switch with `justtunnel context use team:<slug>`",
			active, err,
		))
	}
	return slug, active, nil
}

// loadWorkerEnv consolidates the boilerplate every worker subcommand needs:
// load config, resolve team, derive REST base URL, and require auth.
func loadWorkerEnv() (cfg *config.Config, teamID, ctxName, baseURL string, err error) {
	cfg, err = config.Load(cfgFile)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("load config: %w", err)
	}
	if cfg.AuthToken == "" {
		return nil, "", "", "", display.AuthError("not signed in")
	}
	teamID, ctxName, err = resolveTeamID(cfg)
	if err != nil {
		return nil, "", "", "", err
	}
	baseURL, err = apiBaseURL(cfg.ServerURL)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("parse server URL: %w", err)
	}
	return cfg, teamID, ctxName, baseURL, nil
}

// httpDo performs an authenticated request and returns body + status. The
// caller decides how to interpret status. Body reads are bounded so a
// hostile server cannot OOM the CLI.
func httpDo(method, url, authToken string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	// Cap response body at 64 KiB. The CLI only consumes small JSON
	// envelopes (worker create/list/delete); a hostile or buggy server
	// shipping a multi-megabyte body should not be able to inflate CLI
	// memory.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, data, nil
}

// extractServerError pulls a human-readable message out of a server error
// body of the form {"error":"..."}; falls back to the raw body when JSON
// parsing fails.
func extractServerError(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error != "" {
		return payload.Error
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "(empty response body)"
	}
	return trimmed
}

// postWorker calls POST /api/teams/{teamID}/workers and returns the parsed
// worker. Non-2xx responses surface as errors with the server's message.
func postWorker(baseURL, authToken, teamID, name string) (*workerAPI, error) {
	// json.Marshal on a map[string]string with a literal key cannot fail
	// (no unsupported types possible). Explicit `_ =` documents the
	// intentional discard.
	body, _ := json.Marshal(map[string]string{"name": name})
	url := baseURL + "/api/teams/" + teamID + "/workers"
	status, raw, err := httpDo(http.MethodPost, url, authToken, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("server returned %d: %s", status, extractServerError(raw))
	}
	var worker workerAPI
	if err := json.Unmarshal(raw, &worker); err != nil {
		return nil, fmt.Errorf("decode worker: %w", err)
	}
	return &worker, nil
}

// fetchWorkers calls GET /api/teams/{teamID}/workers.
func fetchWorkers(baseURL, authToken, teamID string) ([]workerAPI, error) {
	url := baseURL + "/api/teams/" + teamID + "/workers"
	status, raw, err := httpDo(http.MethodGet, url, authToken, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("server returned %d: %s", status, extractServerError(raw))
	}
	var resp workerListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode workers: %w", err)
	}
	return resp.Workers, nil
}

// deleteWorker calls DELETE /api/teams/{teamID}/workers/{workerID}.
// Returns (notFound=true, nil) on 404 so the caller can treat it as
// already-deleted and continue local cleanup.
func deleteWorker(baseURL, authToken, teamID, workerID string) (notFound bool, err error) {
	url := baseURL + "/api/teams/" + teamID + "/workers/" + workerID
	status, raw, err := httpDo(http.MethodDelete, url, authToken, nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return true, nil
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("server returned %d: %s", status, extractServerError(raw))
	}
	return false, nil
}
