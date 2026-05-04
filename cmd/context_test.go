package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	t.Cleanup(func() { contextOverride = "" })

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
		{"empty team slug", "team:"},
		{"bad name", "garbage"},
		{"uppercase slug", "team:Acme"},
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

	_, supported, err := fetchMembershipsHTTP(stubServer.Client(), stubServer.URL, "tok")
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

	memberships, supported, err := fetchMembershipsHTTP(stubServer.Client(), stubServer.URL, "tok")
	if err != nil {
		t.Fatalf("404 should not surface as error: %v", err)
	}
	if supported {
		t.Errorf("supported should be false on 404")
	}
	if memberships != nil {
		t.Errorf("memberships should be nil on 404, got %v", memberships)
	}
}
