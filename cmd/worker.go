package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	// F-21: refuse early when the active team context is a stale slug the
	// user is no longer a member of. Without this check, every worker
	// subcommand fails with an opaque 403/404 from the team-scoped REST
	// route. stalenessAnnotation returns stale=false when verification is
	// not possible (offline, older server, ULID-shaped identifier), so we
	// preserve compatibility and offline workflows.
	if _, stale := stalenessAnnotation(cfg, baseURL, ctxName); stale {
		return nil, "", "", "", display.InputError(fmt.Sprintf(
			"active context %s is no longer valid — run `justtunnel context use personal` to clear",
			ctxName,
		))
	}
	return cfg, teamID, ctxName, baseURL, nil
}

// mapForbiddenError converts a 403 response from a team-scoped REST endpoint
// into a typed display.InputError with an actionable, server-shape-aware
// message. The server returns {"error":"..."} bodies; we detect a few known
// phrasings ("not a member", "admin", "only team admins") and fall back to
// a generic "forbidden: <body>" otherwise. Keeps the user from staring at
// an opaque "server returned 403" mid-flight after membership pre-check
// already passed (e.g. revoked between the two calls, or the action is
// admin-gated like ?include=quarantined).
func mapForbiddenError(body []byte) error {
	serverMsg := extractServerError(body)
	lowerMsg := strings.ToLower(serverMsg)
	switch {
	case strings.Contains(lowerMsg, "not a member"):
		return display.InputError(
			"your team membership is no longer valid — run `justtunnel context use personal` and retry",
		)
	case strings.Contains(lowerMsg, "only team admins") || strings.Contains(lowerMsg, "admin"):
		return display.InputError(
			"this action requires team admin role; re-run without admin-only options",
		)
	default:
		return display.InputError(fmt.Sprintf("forbidden: %s", serverMsg))
	}
}

// httpDo performs an authenticated request and returns body + status. The
// caller decides how to interpret status. Body reads are bounded so a
// hostile server cannot OOM the CLI.
//
// D1: ctx threads through to client.Do via req.WithContext so a SIGINT
// during a long-running HTTP call (slow server, hung TLS handshake)
// cancels the in-flight request rather than blocking until httpTimeout.
// Pass cmd.Context() from cobra handlers; pass context.Background() from
// non-cobra callers (rare).
func httpDo(ctx context.Context, method, url, authToken string, body io.Reader) (int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", config.AuthHeaderPrefix+authToken)
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
// worker. Non-2xx responses surface as errors with the server's message;
// 403 specifically routes through mapForbiddenError so the user gets a
// friendly recovery hint rather than "server returned 403".
func postWorker(ctx context.Context, baseURL, authToken, teamID, name string) (*workerAPI, error) {
	// json.Marshal on a map[string]string with a literal key cannot fail
	// (no unsupported types possible). Explicit `_ =` documents the
	// intentional discard.
	body, _ := json.Marshal(map[string]string{"name": name})
	requestURL := baseURL + "/api/teams/" + teamID + "/workers"
	status, raw, err := httpDo(ctx, http.MethodPost, requestURL, authToken, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden {
		return nil, mapForbiddenError(raw)
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

// FetchWorkersOptions controls optional behavior of fetchWorkers. The
// zero-value is the safe default (live workers only); set
// IncludeQuarantined to additionally surface soft-deleted rows. Callers
// pass empty options for the default path (status, install, uninstall,
// rm) so quota-counting consumers keep matching the dashboard view.
type FetchWorkersOptions struct {
	// IncludeQuarantined sends `?include=quarantined` so the server
	// returns workers in retired_quarantined alongside live ones — used by
	// `worker list --all` (justtunnel-cli#50, justtunnel-server F-20).
	// Note: the server admin-gates this on some plans; non-admin callers
	// receive a 403 which mapForbiddenError translates.
	IncludeQuarantined bool
}

// fetchWorkers calls GET /api/teams/{teamID}/workers. opts.IncludeQuarantined
// adds `?include=quarantined`. 403 responses route through mapForbiddenError
// so admin-gated server policies surface as actionable errors rather than
// "server returned 403".
func fetchWorkers(ctx context.Context, baseURL, authToken, teamID string, opts FetchWorkersOptions) ([]workerAPI, error) {
	requestURL := baseURL + "/api/teams/" + teamID + "/workers"
	if opts.IncludeQuarantined {
		// Use url.Values rather than bare string concatenation so adding
		// future params (e.g. ?status=...) doesn't require rewriting this
		// callsite for proper '&' joining and percent-encoding.
		queryValues := url.Values{}
		queryValues.Set("include", "quarantined")
		requestURL += "?" + queryValues.Encode()
	}
	status, raw, err := httpDo(ctx, http.MethodGet, requestURL, authToken, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden {
		return nil, mapForbiddenError(raw)
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
// already-deleted and continue local cleanup. 403 routes through
// mapForbiddenError for friendly admin/membership messaging.
func deleteWorker(ctx context.Context, baseURL, authToken, teamID, workerID string) (notFound bool, err error) {
	requestURL := baseURL + "/api/teams/" + teamID + "/workers/" + workerID
	status, raw, err := httpDo(ctx, http.MethodDelete, requestURL, authToken, nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return true, nil
	}
	if status == http.StatusForbidden {
		return false, mapForbiddenError(raw)
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("server returned %d: %s", status, extractServerError(raw))
	}
	return false, nil
}
