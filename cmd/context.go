package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
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
// Returns (memberships, supported, definitelyNotMember, err). supported=false
// means the server does not yet implement the endpoint and the CLI should
// fall back to a hint. definitelyNotMember=true means the server returned 403
// (e.g. token revoked, team membership no longer valid) — callers should
// treat this as a definitive "not a member" signal rather than failing open.
type membershipFetcher func(client *http.Client, baseURL, authToken string) ([]membership, bool, bool, error)

// fetchMemberships is the package-level fetcher; tests may swap it.
var fetchMemberships membershipFetcher = fetchMembershipsHTTP

// membershipCache provides a process-lifetime in-memory cache of the
// /api/memberships response, keyed on (baseURL, authToken). It exists to
// avoid doubling latency on every worker subcommand: loadWorkerEnv calls
// stalenessAnnotation which fetches memberships, then the actual REST call
// runs immediately afterwards. A 30s TTL means a token rotation or
// membership revocation is reflected within a single trivial wait.
type membershipCacheEntry struct {
	memberships         []membership
	supported           bool
	definitelyNotMember bool
	fetchedAt           time.Time
}

var (
	membershipCacheMu  sync.Mutex
	membershipCacheMap = map[string]membershipCacheEntry{}
)

// membershipCacheTTL is the cache window. Declared as a var so tests can
// shrink it; at runtime 30s is short enough that revocations are reflected
// quickly while still de-duplicating the back-to-back calls inside a single
// worker subcommand invocation.
var membershipCacheTTL = 30 * time.Second

// fetchMembershipsCached wraps fetchMemberships with the (baseURL,authToken)
// keyed cache. Tests that inject fetchMemberships still see every call (the
// fetcher swap happens above this layer when tests assign to
// fetchMemberships); production code paths benefit from de-duplication.
// Errors are NOT cached so a transient 5xx doesn't poison the next call.
func fetchMembershipsCached(client *http.Client, baseURL, authToken string) ([]membership, bool, bool, error) {
	cacheKey := baseURL + "\x00" + authToken
	membershipCacheMu.Lock()
	entry, ok := membershipCacheMap[cacheKey]
	membershipCacheMu.Unlock()
	if ok && time.Since(entry.fetchedAt) < membershipCacheTTL {
		return entry.memberships, entry.supported, entry.definitelyNotMember, nil
	}
	memberships, supported, definitelyNotMember, err := fetchMemberships(client, baseURL, authToken)
	if err != nil {
		return memberships, supported, definitelyNotMember, err
	}
	membershipCacheMu.Lock()
	membershipCacheMap[cacheKey] = membershipCacheEntry{
		memberships:         memberships,
		supported:           supported,
		definitelyNotMember: definitelyNotMember,
		fetchedAt:           time.Now(),
	}
	membershipCacheMu.Unlock()
	return memberships, supported, definitelyNotMember, nil
}

// resetMembershipCache clears the cache. Tests call this between cases so a
// stub change in one test doesn't leak into the next.
func resetMembershipCache() {
	membershipCacheMu.Lock()
	membershipCacheMap = map[string]membershipCacheEntry{}
	membershipCacheMu.Unlock()
}

// activeContextPusher syncs the user's active context to the server so the
// WS worker handshake can resolve the team without requiring team_slug on
// every dial. Production wiring uses pushActiveContextHTTP; tests inject
// stubs.
type activeContextPusher func(client *http.Client, baseURL, authToken, contextName string) error

// pushActiveContext is the package-level pusher; tests may swap it.
var pushActiveContext activeContextPusher = pushActiveContextHTTP

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

	// Pass nil so fetchMembershipsHTTP builds its own client with a 10s
	// timeout. Passing http.DefaultClient would skip the timeout and risk a
	// hang against an unresponsive server.
	memberships, supported, _, err := fetchMembershipsCached(nil, baseURL, cfg.AuthToken)
	if err != nil {
		// Network/timeout errors degrade gracefully (warning to stderr) so
		// the user still sees their personal context. Other non-2xx
		// responses (401/403/500/...) are loud failures with non-zero exit.
		if isNetworkError(err) {
			fmt.Fprintf(os.Stderr, "warning: could not reach server: %v\n", err)
			return nil
		}
		return fmt.Errorf("list team memberships: %w", err)
	}
	if !supported {
		fmt.Fprintln(out, "\n(team membership listing not yet supported by this server;")
		fmt.Fprintln(out, " use `justtunnel context use team:<slug>` if you know your team slug)")
		return nil
	}

	for _, mem := range memberships {
		name := config.TeamContextPrefix + mem.TeamSlug
		// Defensive: if the server returns a syntactically invalid slug,
		// skip it with a stderr warning rather than emitting garbage.
		if validateErr := config.ValidateContext(name); validateErr != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping invalid membership %q: %v\n", name, validateErr)
			continue
		}
		fmt.Fprintf(out, "%s%s", marker(name), name)
		if mem.TeamName != "" {
			fmt.Fprintf(out, "  (%s)", mem.TeamName)
		}
		fmt.Fprintln(out)
	}
	return nil
}

// isNetworkError reports whether err looks like a transient transport-layer
// failure (DNS, connection refused, timeout) as opposed to a structured HTTP
// error response. Network failures degrade gracefully; structured errors
// surface to the user.
//
// We unwrap *url.Error to inspect the inner cause: a TLS handshake or
// redirect-loop failure is wrapped in url.Error too, but it is NOT a
// transient connectivity issue and should surface differently. Only inner
// net.Error (timeout, refused, DNS) qualifies as "network".
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		var inner net.Error
		if errors.As(urlErr.Err, &inner) {
			return true
		}
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
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

	// Membership validation for team contexts. Without this, `context use
	// team:<bogus>` silently succeeds against a server that has never
	// heard of the team, and subsequent commands fail with downstream
	// 403/404s instead of the real cause. See justtunnel-cli#49.
	//
	// We only validate when the user is authenticated AND the server
	// supports the memberships endpoint. If it doesn't, fall through to
	// the previous behavior so older servers still work.
	//
	// offlineDetected is set when the validation step already determined
	// the server is unreachable. The downstream best-effort push then
	// skips its own attempt to avoid emitting a second redundant warning.
	offlineDetected := false
	baseURL, parseErr := apiBaseURL(cfg.ServerURL)
	if cfg.AuthToken != "" && strings.HasPrefix(name, config.TeamContextPrefix) && parseErr == nil {
		memberships, supported, definitelyNotMember, fetchErr := fetchMembershipsCached(nil, baseURL, cfg.AuthToken)
		switch {
		case fetchErr != nil && isNetworkError(fetchErr):
			// Offline / unreachable server: warn and proceed so the
			// user can keep working without connectivity.
			offlineDetected = true
			fmt.Fprintf(os.Stderr,
				"warning: could not reach server to verify team membership: %v\n", fetchErr)
		case fetchErr != nil:
			// Structured server error (4xx/5xx other than 404):
			// surface so the user knows validation failed.
			return fmt.Errorf("verify team membership: %w", fetchErr)
		case definitelyNotMember:
			// 403 from /api/memberships: token is valid but the server
			// considers the user not a member (e.g. revoked).
			return display.InputError(fmt.Sprintf(
				"server rejected your team membership for %s — re-run `justtunnel auth` or `justtunnel context use personal`",
				name,
			))
		case supported:
			slug := strings.TrimPrefix(name, config.TeamContextPrefix)
			found := false
			for _, mem := range memberships {
				if mem.TeamSlug == slug {
					found = true
					break
				}
			}
			if !found {
				return display.InputError(fmt.Sprintf(
					"you are not a member of %s — run `justtunnel context list` to see available teams",
					name,
				))
			}
		}
		// supported=false (older server): fall through; we cannot
		// verify and forcing a hard error would break compatibility.
	}

	if err := config.SetCurrentContext(cfg, name); err != nil {
		return fmt.Errorf("set context: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Active context set to %s.\n", name)

	// Best-effort sync to the server so the WS worker handshake can resolve
	// the team without team_slug on the dial URL. Local config remains the
	// source of truth — push failures degrade to a stderr warning so the
	// user can keep working offline. See justtunnel-cli#44.
	if cfg.AuthToken != "" {
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not sync context to server: %v\n", parseErr)
			return nil
		}
		if offlineDetected {
			// Already warned about unreachability above; don't double-warn.
			return nil
		}
		if pushErr := pushActiveContext(nil, baseURL, cfg.AuthToken, name); pushErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not sync context to server: %v\n", pushErr)
		}
	}
	return nil
}

func runContextShow(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	active := config.ResolveContext(cfg, contextOverride)

	// F-21: detect a stale team context — a slug that was set in an older
	// binary (before write-side validation in justtunnel-cli#49) and has
	// since been deleted or had the user removed. We annotate but do not
	// mutate config: the user clears it explicitly with `context use
	// personal`. Failures to verify (offline, unsupported endpoint, network)
	// fall through silently so `context show` keeps working without a
	// reachable server.
	//
	// Compute the REST base URL once and pass it down so stalenessAnnotation
	// doesn't repeat the work the caller already did.
	baseURL, baseURLErr := apiBaseURL(cfg.ServerURL)
	if baseURLErr != nil {
		fmt.Fprintln(cmd.OutOrStdout(), active)
		return nil
	}
	annotation, stale := stalenessAnnotation(cfg, baseURL, active)
	if stale {
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", active, annotation)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), active)
	return nil
}

// stalenessAnnotation returns (annotation, stale). When stale=true the
// caller should append annotation to the printed context. stale=false
// means the context is either valid, personal, or cannot be verified
// (offline, older server, ULID-shaped identifier we cannot resolve via
// the slug-keyed memberships endpoint).
//
// Callers compute the REST baseURL once and pass it in to avoid two
// successive apiBaseURL calls.
func stalenessAnnotation(cfg *config.Config, baseURL, active string) (string, bool) {
	if cfg == nil || cfg.AuthToken == "" {
		return "", false
	}
	if !strings.HasPrefix(active, config.TeamContextPrefix) {
		return "", false
	}
	identifier := strings.TrimPrefix(active, config.TeamContextPrefix)
	if identifier == "" || looksLikeULID(identifier) {
		// ULIDs are addressed by Team.ID, not slug — the memberships
		// endpoint returns slugs, so we cannot prove staleness here.
		// Fail open rather than annotate a valid ULID-shaped context.
		return "", false
	}

	memberships, supported, definitelyNotMember, err := fetchMembershipsCached(nil, baseURL, cfg.AuthToken)
	if definitelyNotMember {
		// 403 from /api/memberships: server says the token holder isn't a
		// member of any team. Treat the active team context as stale.
		return "(invalid — not a member; run `justtunnel context use personal` to clear)", true
	}
	if err != nil || !supported {
		// Unsupported endpoint, network blip, or 5xx: fail open. `context
		// show` is a read-only diagnostic and shouldn't fail because of an
		// auth blip on a sibling endpoint.
		return "", false
	}
	for _, mem := range memberships {
		if mem.TeamSlug == identifier {
			return "", false
		}
	}
	return "(invalid — not a member; run `justtunnel context use personal` to clear)", true
}

// looksLikeULID reports whether identifier has the shape of a Crockford
// base32 ULID (26 uppercase alphanumerics, no I/L/O/U). The CLI accepts
// either a slug or a ULID in `team:<id-or-slug>` form (see
// config.ValidateContext); the memberships endpoint only returns slugs, so
// ULID-shaped identifiers cannot be cross-checked and should not be flagged.
func looksLikeULID(identifier string) bool {
	if len(identifier) != 26 {
		return false
	}
	for _, character := range identifier {
		isUpper := character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		if !isUpper && !isDigit {
			return false
		}
		// Crockford base32 explicitly excludes I, L, O, U to avoid
		// visual ambiguity with 1, 1, 0, and V respectively. Reject
		// any identifier that contains one of those letters even if
		// the rest of the alphanumeric check would have accepted it.
		if character == 'I' || character == 'L' || character == 'O' || character == 'U' {
			return false
		}
	}
	return true
}

// fetchMembershipsHTTP calls GET /api/memberships on the server. If the server
// returns 404, it reports supported=false so the caller can degrade gracefully.
// If the server returns 403, it reports definitelyNotMember=true so callers
// can treat that as a definitive "not a member" signal (vs a transient 5xx).
func fetchMembershipsHTTP(client *http.Client, baseURL, authToken string) ([]membership, bool, bool, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/memberships", nil)
	if err != nil {
		return nil, false, false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", config.AuthHeaderPrefix+authToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, false, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, false, nil
	}
	if resp.StatusCode == http.StatusForbidden {
		// 403 means the token is valid in shape but the server says the
		// user isn't a member of any team. Surface the signal so callers
		// can annotate context as stale rather than failing open.
		return nil, true, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Bound the read so a malicious or misconfigured server cannot OOM
		// the CLI by streaming an enormous error body.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, true, false, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Memberships []membership `json:"memberships"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, true, false, fmt.Errorf("decode response: %w", err)
	}
	return payload.Memberships, true, false, nil
}

// pushActiveContextHTTP POSTs the active context name to the server so the
// user.active_context column reflects the CLI's local choice. The endpoint
// is /api/me/preferences/active-context with body
// {"kind":"personal"|"team","slug":"<slug>"}. Empty input is rejected here
// (caller validates first via config.ValidateContext).
func pushActiveContextHTTP(client *http.Client, baseURL, authToken, contextName string) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	var body struct {
		Kind string `json:"kind"`
		Slug string `json:"slug,omitempty"`
	}
	switch {
	case contextName == config.PersonalContext:
		body.Kind = "personal"
	case strings.HasPrefix(contextName, config.TeamContextPrefix):
		body.Kind = "team"
		body.Slug = strings.TrimPrefix(contextName, config.TeamContextPrefix)
		if body.Slug == "" {
			return fmt.Errorf("team context missing slug")
		}
	default:
		return fmt.Errorf("unknown context shape: %s", contextName)
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/me/preferences/active-context", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", config.AuthHeaderPrefix+authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	// 404 from older servers without the route is treated as a soft failure
	// so the CLI doesn't surface a confusing warning every time someone
	// runs against a not-yet-upgraded server.
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(errorBody)))
	}
	return nil
}
