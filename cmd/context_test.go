package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justtunnel/justtunnel-cli/internal/config"
)

// resetContextState reinitializes process-level state that the context
// commands touch so each test starts from a clean slate. It also installs
// a t.Cleanup that resets contextOverride so a test failure (e.g. t.Fatal
// before a manual reset) cannot poison subsequent tests with stale flag
// state.
func resetContextState(t *testing.T, cfg *config.Config) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	config.SetConfigPath(path)
	cfgFile = path
	contextOverride = ""
	resetMembershipCache()
	t.Cleanup(func() { contextOverride = ""; resetMembershipCache() })

	if cfg == nil {
		cfg = &config.Config{ServerURL: "wss://api.example.com/ws"}
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return path
}

// httpToWS rewrites an httptest.Server URL (http://...) into the ws:// form
// that the CLI's config layer expects for ServerURL.
func httpToWS(serverURL string) string {
	return strings.Replace(serverURL, "http://", "ws://", 1)
}

// runCmd executes the named subcommand in isolation, capturing stdout.
func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	rootCmd.SetOut(out)
	rootCmd.SetErr(out)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	// Always restore default args so other tests aren't affected.
	rootCmd.SetArgs(nil)
	return out.String(), err
}

func TestContextShowDefaultsToPersonal(t *testing.T) {
	resetContextState(t, nil)

	out, err := runCmd(t, "context", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.TrimSpace(out) != "personal" {
		t.Errorf("show output: got %q, want %q", strings.TrimSpace(out), "personal")
	}
}

func TestContextUseSetsAndPersists(t *testing.T) {
	path := resetContextState(t, nil)

	if _, err := runCmd(t, "context", "use", "team:acme"); err != nil {
		t.Fatalf("use: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.CurrentContext != "team:acme" {
		t.Errorf("persisted context: got %q, want %q", loaded.CurrentContext, "team:acme")
	}

	out, err := runCmd(t, "context", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.TrimSpace(out) != "team:acme" {
		t.Errorf("show after use: got %q, want %q", strings.TrimSpace(out), "team:acme")
	}
}

func TestContextUseRejectsInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty team identifier", "team:"},
		{"bad name", "garbage"},
		{"underscore in identifier", "team:acme_corp"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			resetContextState(t, nil)
			_, err := runCmd(t, "context", "use", testCase.input)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", testCase.input)
			}
		})
	}
}

func TestContextFlagOverridesShow(t *testing.T) {
	resetContextState(t, &config.Config{
		ServerURL:      "wss://api.example.com/ws",
		CurrentContext: "team:base",
	})

	out, err := runCmd(t, "--context", "team:override", "context", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.TrimSpace(out) != "team:override" {
		t.Errorf("show with --context override: got %q, want %q", strings.TrimSpace(out), "team:override")
	}
	// Cleanup is handled by resetContextState's t.Cleanup hook.
}

// TestContextFlagRejectsInvalid verifies the rootCmd PersistentPreRunE hook
// short-circuits with an error when --context is set to a syntactically
// invalid value, rather than letting it propagate via ResolveContext.
func TestContextFlagRejectsInvalid(t *testing.T) {
	resetContextState(t, nil)

	_, err := runCmd(t, "--context", "garbage", "context", "show")
	if err == nil {
		t.Fatal("expected error for --context garbage, got nil")
	}
}

// TestContextUsePushesToServer verifies that `context use team:<slug>` calls
// the active-context sync endpoint with the right body when an auth token
// is present. Regression guard for justtunnel-cli#44.
func TestContextUsePushesToServer(t *testing.T) {
	resetContextState(t, &config.Config{
		ServerURL: "wss://api.example.com/ws",
		AuthToken: "tok_test",
	})

	// Stub the membership fetcher so the validation step doesn't actually
	// hit the unreachable api.example.com (which would otherwise mark the
	// command as "offline" and skip the push entirely).
	previousFetcher := fetchMemberships
	t.Cleanup(func() { fetchMemberships = previousFetcher; resetMembershipCache() })
	fetchMemberships = func(client *http.Client, baseURL, authToken string) ([]membership, bool, bool, error) {
		return []membership{{TeamSlug: "acme"}}, true, false, nil
	}
	resetMembershipCache()

	previousPusher := pushActiveContext
	t.Cleanup(func() { pushActiveContext = previousPusher })

	var got struct {
		called      bool
		baseURL     string
		authToken   string
		contextName string
	}
	pushActiveContext = func(client *http.Client, baseURL, authToken, contextName string) error {
		got.called = true
		got.baseURL = baseURL
		got.authToken = authToken
		got.contextName = contextName
		return nil
	}

	if _, err := runCmd(t, "context", "use", "team:acme"); err != nil {
		t.Fatalf("use: %v", err)
	}
	if !got.called {
		t.Fatal("pushActiveContext was not called")
	}
	if got.contextName != "team:acme" {
		t.Errorf("contextName: got %q want %q", got.contextName, "team:acme")
	}
	if got.authToken != "tok_test" {
		t.Errorf("authToken: got %q want %q", got.authToken, "tok_test")
	}
	if !strings.HasPrefix(got.baseURL, "https://") {
		t.Errorf("baseURL should be https-derived: got %q", got.baseURL)
	}
}

// TestContextUseSkipsServerSyncWhenLoggedOut verifies that the push is a
// no-op when no auth token is present — the user can still configure the
// CLI offline.
func TestContextUseSkipsServerSyncWhenLoggedOut(t *testing.T) {
	resetContextState(t, &config.Config{ServerURL: "wss://api.example.com/ws"})

	previousPusher := pushActiveContext
	t.Cleanup(func() { pushActiveContext = previousPusher })

	called := false
	pushActiveContext = func(client *http.Client, baseURL, authToken, contextName string) error {
		called = true
		return nil
	}

	if _, err := runCmd(t, "context", "use", "team:acme"); err != nil {
		t.Fatalf("use: %v", err)
	}
	if called {
		t.Fatal("pushActiveContext should not be called without auth token")
	}
}

// TestPushActiveContextHTTP_TolerantOf404 verifies that pushActiveContextHTTP
// returns nil when the server returns 404, so older servers without the
// route don't surface a confusing warning.
func TestPushActiveContextHTTP_TolerantOf404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	if err := pushActiveContextHTTP(server.Client(), server.URL, "tok", "personal"); err != nil {
		t.Fatalf("404 should be soft: got %v", err)
	}
}

// TestPushActiveContextHTTP_BodyShape verifies the wire format the server
// expects: {"kind":"personal"} and {"kind":"team","slug":"<s>"}.
func TestPushActiveContextHTTP_BodyShape(t *testing.T) {
	tests := []struct {
		contextName string
		wantKind    string
		wantSlug    string
	}{
		{"personal", "personal", ""},
		{"team:acme", "team", "acme"},
	}
	for _, testCase := range tests {
		t.Run(testCase.contextName, func(t *testing.T) {
			var receivedBody struct {
				Kind string `json:"kind"`
				Slug string `json:"slug,omitempty"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/me/preferences/active-context" {
					t.Errorf("path: got %q want %q", request.URL.Path, "/api/me/preferences/active-context")
				}
				if got := request.Header.Get("Authorization"); got != "Bearer tok" {
					t.Errorf("auth header: got %q", got)
				}
				if err := json.NewDecoder(request.Body).Decode(&receivedBody); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				rw.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)

			if err := pushActiveContextHTTP(server.Client(), server.URL, "tok", testCase.contextName); err != nil {
				t.Fatalf("push: %v", err)
			}
			if receivedBody.Kind != testCase.wantKind {
				t.Errorf("kind: got %q want %q", receivedBody.Kind, testCase.wantKind)
			}
			if receivedBody.Slug != testCase.wantSlug {
				t.Errorf("slug: got %q want %q", receivedBody.Slug, testCase.wantSlug)
			}
		})
	}
}

func TestContextListWithoutAuthShowsPersonalOnly(t *testing.T) {
	resetContextState(t, nil) // no AuthToken set

	out, err := runCmd(t, "context", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "personal") {
		t.Errorf("output should include personal: %s", out)
	}
	if !strings.Contains(out, "sign in") {
		t.Errorf("output should hint at sign-in: %s", out)
	}
}

func TestContextListWithMembershipsEndpoint(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memberships" {
			http.NotFound(writer, request)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer justtunnel_test_token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"memberships":[{"team_slug":"acme","team_name":"Acme Inc"},{"team_slug":"globex"}]}`))
	}))
	defer stubServer.Close()

	resetContextState(t, &config.Config{
		AuthToken: "justtunnel_test_token",
		ServerURL: httpToWS(stubServer.URL) + "/ws",
	})

	out, err := runCmd(t, "context", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "team:acme") {
		t.Errorf("output should include team:acme, got: %s", out)
	}
	if !strings.Contains(out, "team:globex") {
		t.Errorf("output should include team:globex, got: %s", out)
	}
	if !strings.Contains(out, "Acme Inc") {
		t.Errorf("output should include team display name, got: %s", out)
	}
}

func TestContextListFallsBackWhenServerLacksEndpoint(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	}))
	defer stubServer.Close()

	resetContextState(t, &config.Config{
		AuthToken: "justtunnel_test_token",
		ServerURL: httpToWS(stubServer.URL) + "/ws",
	})

	out, err := runCmd(t, "context", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "personal") {
		t.Errorf("output should include personal: %s", out)
	}
	if !strings.Contains(out, "not yet supported") {
		t.Errorf("output should include fallback hint, got: %s", out)
	}
}

func TestContextListMarksActive(t *testing.T) {
	resetContextState(t, &config.Config{
		ServerURL:      "wss://api.example.com/ws",
		CurrentContext: "personal",
	})

	out, err := runCmd(t, "context", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "* personal") {
		t.Errorf("active context should be marked with *, got: %s", out)
	}
}

func TestFetchMembershipsHTTPError(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		writer.Write([]byte("boom"))
	}))
	defer stubServer.Close()

	_, supported, _, err := fetchMembershipsHTTP(stubServer.Client(), stubServer.URL, "tok")
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if !supported {
		t.Errorf("supported should be true for non-404 errors")
	}
}

func TestFetchMembershipsHTTP404(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	}))
	defer stubServer.Close()

	memberships, supported, definitelyNotMember, err := fetchMembershipsHTTP(stubServer.Client(), stubServer.URL, "tok")
	if err != nil {
		t.Fatalf("404 should not surface as error: %v", err)
	}
	if supported {
		t.Errorf("supported should be false on 404")
	}
	if definitelyNotMember {
		t.Errorf("definitelyNotMember should be false on 404")
	}
	if memberships != nil {
		t.Errorf("memberships should be nil on 404, got %v", memberships)
	}
}

// TestFetchMembershipsHTTP403 verifies the tri-state extension: a 403 must
// be reported as definitelyNotMember=true rather than as an opaque error,
// so callers (stalenessAnnotation, runContextUse) can treat the response
// as a definitive "not a member" signal.
func TestFetchMembershipsHTTP403(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		writer.Write([]byte(`{"error":"not a member"}`))
	}))
	defer stubServer.Close()

	memberships, supported, definitelyNotMember, err := fetchMembershipsHTTP(stubServer.Client(), stubServer.URL, "tok")
	if err != nil {
		t.Fatalf("403 should not surface as error: %v", err)
	}
	if !supported {
		t.Errorf("supported should be true on 403")
	}
	if !definitelyNotMember {
		t.Errorf("definitelyNotMember should be true on 403")
	}
	if memberships != nil {
		t.Errorf("memberships should be nil on 403, got %v", memberships)
	}
}

// TestContextUseRejectsNonMemberTeam verifies the membership validation
// added for justtunnel-cli#49: `context use team:<bogus>` must error
// (non-zero exit) when the server's memberships endpoint reports the
// user is not a member, instead of silently accepting the slug.
func TestContextUseRejectsNonMemberTeam(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memberships" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"memberships":[{"team_slug":"acme"}]}`))
	}))
	defer stubServer.Close()

	resetContextState(t, &config.Config{
		AuthToken: "tok",
		ServerURL: httpToWS(stubServer.URL) + "/ws",
	})

	_, err := runCmd(t, "context", "use", "team:does-not-exist")
	if err == nil {
		t.Fatal("expected error for non-member team, got nil")
	}
	if !strings.Contains(err.Error(), "not a member") {
		t.Errorf("error should mention membership: got %v", err)
	}

	// Config must be unchanged — the user did not switch.
	loaded, loadErr := config.Load(cfgFile)
	if loadErr != nil {
		t.Fatalf("load: %v", loadErr)
	}
	if loaded.CurrentContext == "team:does-not-exist" {
		t.Errorf("config should not have been mutated; got CurrentContext=%q",
			loaded.CurrentContext)
	}
}

// TestContextUseAcceptsMemberTeam complements TestContextUseRejectsNonMemberTeam
// by verifying the happy path still works after membership validation.
func TestContextUseAcceptsMemberTeam(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/memberships":
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"memberships":[{"team_slug":"acme"}]}`))
		case "/api/me/preferences/active-context":
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stubServer.Close()

	resetContextState(t, &config.Config{
		AuthToken: "tok",
		ServerURL: httpToWS(stubServer.URL) + "/ws",
	})

	if _, err := runCmd(t, "context", "use", "team:acme"); err != nil {
		t.Fatalf("expected success for member team: %v", err)
	}
	loaded, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.CurrentContext != "team:acme" {
		t.Errorf("CurrentContext: got %q want %q", loaded.CurrentContext, "team:acme")
	}
}

// TestContextShowAnnotatesStaleSlug verifies F-21: when the active context
// is a team slug the user is no longer a member of (e.g. team was deleted
// or membership revoked while a stale slug lingered in local config),
// `context show` prints the slug with an "(invalid — ...)" annotation
// rather than emitting it verbatim. The local config is left untouched so
// the user can clear it explicitly with `context use personal`.
func TestContextShowAnnotatesStaleSlug(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memberships" {
			http.NotFound(writer, request)
			return
		}
		// C-7: assert the CLI is sending the right Authorization header
		// rather than treating the stub as a black box.
		if got := request.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization header: got %q, want %q", got, "Bearer tok")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"memberships":[{"team_slug":"acme"}]}`))
	}))
	defer stubServer.Close()

	resetContextState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stubServer.URL) + "/ws",
		CurrentContext: "team:does-not-exist-xyz",
	})

	out, err := runCmd(t, "context", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "team:does-not-exist-xyz") {
		t.Errorf("output should still include the stale slug for visibility, got: %q", out)
	}
	if !strings.Contains(out, "invalid") {
		t.Errorf("output should annotate the slug as invalid, got: %q", out)
	}
	if !strings.Contains(out, "context use personal") {
		t.Errorf("output should hint at the recovery command, got: %q", out)
	}

	// Read-path must not mutate config: a subsequent `context use personal`
	// is the only way to clear the stale value.
	loaded, loadErr := config.Load(cfgFile)
	if loadErr != nil {
		t.Fatalf("load: %v", loadErr)
	}
	if loaded.CurrentContext != "team:does-not-exist-xyz" {
		t.Errorf("show must not mutate config; got CurrentContext=%q", loaded.CurrentContext)
	}
}

// TestContextShowDoesNotAnnotateValidTeam verifies the no-false-positive
// case: when the active slug IS in the membership list, `context show`
// prints it verbatim with no annotation.
func TestContextShowDoesNotAnnotateValidTeam(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memberships" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"memberships":[{"team_slug":"acme"}]}`))
	}))
	defer stubServer.Close()

	resetContextState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stubServer.URL) + "/ws",
		CurrentContext: "team:acme",
	})

	out, err := runCmd(t, "context", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.TrimSpace(out) != "team:acme" {
		t.Errorf("valid team should print without annotation; got %q", strings.TrimSpace(out))
	}
}

// TestContextShowFailsOpenWhenServerUnsupported verifies that older servers
// (no /api/memberships route) do not cause `context show` to annotate
// every team context as invalid. The CLI cannot prove staleness, so it
// must emit the slug verbatim.
func TestContextShowFailsOpenWhenServerUnsupported(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	}))
	defer stubServer.Close()

	resetContextState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stubServer.URL) + "/ws",
		CurrentContext: "team:acme",
	})

	out, err := runCmd(t, "context", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.TrimSpace(out) != "team:acme" {
		t.Errorf("unsupported server must fail open; got %q", strings.TrimSpace(out))
	}
}

// TestContextShowFailsOpenWhenLoggedOut verifies that without an auth token
// the CLI never annotates — there is no way to verify, and a logged-out
// user shouldn't get a confusing warning on a read-only command.
func TestContextShowFailsOpenWhenLoggedOut(t *testing.T) {
	resetContextState(t, &config.Config{
		ServerURL:      "wss://api.example.com/ws",
		CurrentContext: "team:acme",
	})

	out, err := runCmd(t, "context", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.TrimSpace(out) != "team:acme" {
		t.Errorf("logged-out show must not annotate; got %q", strings.TrimSpace(out))
	}
}

// TestContextShowFailsOpenForULIDIdentifier verifies the ULID escape
// hatch: the memberships endpoint returns slugs, so a ULID-shaped
// identifier cannot be cross-checked and must NOT be flagged as stale.
func TestContextShowFailsOpenForULIDIdentifier(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memberships" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"memberships":[{"team_slug":"acme"}]}`))
	}))
	defer stubServer.Close()

	const ulid = "01KQTJBVA6REFPMKT8MPKX8Z9N"
	resetContextState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stubServer.URL) + "/ws",
		CurrentContext: "team:" + ulid,
	})

	out, err := runCmd(t, "context", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.TrimSpace(out) != "team:"+ulid {
		t.Errorf("ULID-shaped id must not be annotated; got %q", strings.TrimSpace(out))
	}
}

// TestContextUseTolerates404FromOlderServer verifies older servers (no
// /api/memberships route) still allow `context use` so the CLI doesn't
// break compatibility. The previous behavior is preserved when supported
// is false.
func TestContextUseTolerates404FromOlderServer(t *testing.T) {
	stubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Every endpoint returns 404 — simulating an older server.
		http.NotFound(writer, request)
	}))
	defer stubServer.Close()

	resetContextState(t, &config.Config{
		AuthToken: "tok",
		ServerURL: httpToWS(stubServer.URL) + "/ws",
	})

	if _, err := runCmd(t, "context", "use", "team:acme"); err != nil {
		t.Fatalf("404 from /api/memberships should not block context use: %v", err)
	}
}

// TestLooksLikeULID_ExcludesCrockfordInvalid verifies that I, L, O, and U
// (which Crockford base32 explicitly excludes to avoid visual ambiguity
// with 1, 1, 0, V) are rejected in any position. Without this, the
// stalenessAnnotation ULID escape hatch silently treats invalid
// identifiers as ULID-shaped and skips the membership cross-check.
func TestLooksLikeULID_ExcludesCrockfordInvalid(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"contains I (start)", "I1KQTJBVA6REFPMKT8MPKX8Z9N"},
		{"contains I (mid)", "01KQTJBVA6IEFPMKT8MPKX8Z9N"},
		{"contains L (mid)", "01KQTJBVA6REFPMKT8LPKX8Z9N"},
		{"contains O (start)", "O1KQTJBVA6REFPMKT8MPKX8Z9N"},
		{"contains U (mid)", "01KQTJBVA6REFPMKT8UPKX8Z9N"},
		{"contains O (end)", "01KQTJBVA6REFPMKT8MPKX8Z9O"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if len(testCase.id) != 26 {
				t.Fatalf("test setup error: id length = %d, want 26", len(testCase.id))
			}
			if looksLikeULID(testCase.id) {
				t.Errorf("looksLikeULID(%q) = true, want false (contains Crockford-invalid char)", testCase.id)
			}
		})
	}
}

// TestLooksLikeULID_AcceptsValidCrockford complements the negative cases:
// a real ULID using only Crockford-valid characters must still pass.
func TestLooksLikeULID_AcceptsValidCrockford(t *testing.T) {
	const validULID = "01KQTJBVA6REFPMKT8MPKX8Z9N"
	if len(validULID) != 26 {
		t.Fatalf("test setup error: id length = %d, want 26", len(validULID))
	}
	if !looksLikeULID(validULID) {
		t.Errorf("looksLikeULID(%q) = false, want true (valid Crockford ULID)", validULID)
	}
}

// TestStalenessAnnotationCachesMembershipFetch verifies C-3: back-to-back
// staleness checks (e.g. context show then a worker subcommand within the
// 30s TTL) reuse the cached /api/memberships response instead of hitting
// the server twice.
func TestStalenessAnnotationCachesMembershipFetch(t *testing.T) {
	var requestCount int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memberships" {
			http.NotFound(writer, request)
			return
		}
		atomic.AddInt32(&requestCount, 1)
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"memberships":[{"team_slug":"acme"}]}`))
	}))
	defer stub.Close()

	resetContextState(t, &config.Config{
		AuthToken: "tok",
		ServerURL: httpToWS(stub.URL) + "/ws",
	})

	// Two successive calls to stalenessAnnotation in quick succession.
	cfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	baseURL, err := config.APIBaseURL(cfg.ServerURL)
	if err != nil {
		t.Fatalf("config.APIBaseURL: %v", err)
	}
	if _, stale := stalenessAnnotation(cfg, baseURL, "team:acme"); stale {
		t.Fatalf("first call: should not be stale for valid membership")
	}
	if _, stale := stalenessAnnotation(cfg, baseURL, "team:acme"); stale {
		t.Fatalf("second call: should not be stale for valid membership")
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Errorf("expected 1 HTTP request to /api/memberships (cached on second call); got %d", got)
	}
}

// TestStalenessAnnotation403MarksStale verifies C-4: a 403 from
// /api/memberships is treated as "definitely not a member" rather than
// failing open. This catches the case where membership was revoked
// server-side while a stale slug lingered locally.
func TestStalenessAnnotation403MarksStale(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memberships" {
			http.NotFound(writer, request)
			return
		}
		writer.WriteHeader(http.StatusForbidden)
		writer.Write([]byte(`{"error":"not a member"}`))
	}))
	defer stub.Close()

	resetContextState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:acme",
	})

	cfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	baseURL, err := config.APIBaseURL(cfg.ServerURL)
	if err != nil {
		t.Fatalf("config.APIBaseURL: %v", err)
	}
	annotation, stale := stalenessAnnotation(cfg, baseURL, "team:acme")
	if !stale {
		t.Fatalf("expected stale=true on 403, got false (annotation=%q)", annotation)
	}
	if !strings.Contains(annotation, "not a member") {
		t.Errorf("annotation should mention not-a-member; got %q", annotation)
	}
}

// TestStalenessAnnotationFailsOpenOn5xx verifies the C-4 contract: a
// transient 5xx must NOT be treated as definitive. The CLI fails open so
// `context show` keeps working through a brief server blip.
func TestStalenessAnnotationFailsOpenOn5xx(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memberships" {
			http.NotFound(writer, request)
			return
		}
		writer.WriteHeader(http.StatusInternalServerError)
		writer.Write([]byte(`{"error":"db down"}`))
	}))
	defer stub.Close()

	resetContextState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:acme",
	})

	cfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	baseURL, err := config.APIBaseURL(cfg.ServerURL)
	if err != nil {
		t.Fatalf("config.APIBaseURL: %v", err)
	}
	if _, stale := stalenessAnnotation(cfg, baseURL, "team:acme"); stale {
		t.Errorf("5xx must fail open, not annotate as stale")
	}
}
