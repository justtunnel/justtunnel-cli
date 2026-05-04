package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
	"github.com/justtunnel/justtunnel-cli/internal/worker/installer"
)

// fakeServiceInstaller records Bootstrap calls so install tests can assert
// the wiring (right name passed, right number of invocations) without
// touching launchctl/systemctl. The serviceInstaller seam in
// worker_install.go is what we mock here.
type fakeServiceInstaller struct {
	bootstrapCalls int32
	gotName        string
	gotNoLinger    bool
	err            error
	result         installer.SystemdResult
}

func (f *fakeServiceInstaller) Bootstrap(_ context.Context, name string, opts installer.SystemdOptions) (installer.SystemdResult, error) {
	atomic.AddInt32(&f.bootstrapCalls, 1)
	f.gotName = name
	f.gotNoLinger = opts.NoLinger
	return f.result, f.err
}

// withFakeInstaller swaps the package-level newServiceInstaller factory for
// the duration of a single test, then restores it. Using a factory function
// rather than a global variable means tests can pin per-OS dispatch
// behavior without leaking state across the suite.
func withFakeInstaller(t *testing.T, fake *fakeServiceInstaller) {
	t.Helper()
	prev := newServiceInstaller
	newServiceInstaller = func(_ string) (serviceInstaller, error) {
		return fake, nil
	}
	t.Cleanup(func() { newServiceInstaller = prev })
}

// withUnsupportedOS pins the OS dispatcher to return the canonical "not
// supported" error so the windows-path test does not depend on the host
// runtime.GOOS.
func withUnsupportedOS(t *testing.T) {
	t.Helper()
	prev := newServiceInstaller
	newServiceInstaller = func(goos string) (serviceInstaller, error) {
		return nil, unsupportedOSError(goos)
	}
	t.Cleanup(func() { newServiceInstaller = prev })
}

func TestWorkerInstallCleanInstall(t *testing.T) {
	var postCount, getCount int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			atomic.AddInt32(&getCount, 1)
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[]}`))
		case http.MethodPost:
			atomic.AddInt32(&postCount, 1)
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), `"alpha"`) {
				t.Errorf("POST body should contain alpha, got %s", body)
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			writer.Write([]byte(`{"id":"wkr_1","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme","created_at":"2026-05-04T12:00:00Z"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:team-acme",
	})

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)

	out, err := runCmd(t, "worker", "install", "alpha")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if atomic.LoadInt32(&postCount) != 1 {
		t.Errorf("POST calls: got %d, want 1", postCount)
	}
	if atomic.LoadInt32(&fake.bootstrapCalls) != 1 {
		t.Errorf("Bootstrap calls: got %d, want 1", fake.bootstrapCalls)
	}
	if fake.gotName != "alpha" {
		t.Errorf("Bootstrap name: got %q, want alpha", fake.gotName)
	}
	loaded, readErr := worker.Read("alpha")
	if readErr != nil {
		t.Fatalf("local config not written: %v", readErr)
	}
	if loaded.WorkerID != "wkr_1" {
		t.Errorf("local WorkerID: got %q, want wkr_1", loaded.WorkerID)
	}
	if !strings.Contains(out, "alpha") {
		t.Errorf("output should mention worker name, got: %s", out)
	}
}

func TestWorkerInstallReinstallNoOpWhenBothExist(t *testing.T) {
	var postCount int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_1","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme"}]}`))
		case http.MethodPost:
			atomic.AddInt32(&postCount, 1)
			writer.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:team-acme",
	})

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_1", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "none",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)

	if _, err := runCmd(t, "worker", "install", "alpha"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if atomic.LoadInt32(&postCount) != 0 {
		t.Errorf("re-install should NOT POST when both sides have the worker, got %d POST calls", postCount)
	}
	if atomic.LoadInt32(&fake.bootstrapCalls) != 1 {
		t.Errorf("Bootstrap should still re-run (idempotent), got %d", fake.bootstrapCalls)
	}
}

func TestWorkerInstallReinstallLocalPresentServerMissing(t *testing.T) {
	var postCount int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[]}`))
		case http.MethodPost:
			atomic.AddInt32(&postCount, 1)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			writer.Write([]byte(`{"id":"wkr_new","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme","created_at":"2026-05-04T12:00:00Z"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:team-acme",
	})

	// Local seed has a stale ID; install should re-create on server and
	// overwrite local with the fresh server-side ID.
	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_stale", Name: "alpha", Context: "team:team-acme",
		Subdomain: "old", CreatedAt: time.Now().UTC(), ServiceBackend: "none",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)

	if _, err := runCmd(t, "worker", "install", "alpha"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if atomic.LoadInt32(&postCount) != 1 {
		t.Errorf("expected re-create POST, got %d", postCount)
	}
	loaded, readErr := worker.Read("alpha")
	if readErr != nil {
		t.Fatalf("read after reinstall: %v", readErr)
	}
	if loaded.WorkerID != "wkr_new" {
		t.Errorf("local WorkerID: got %q, want wkr_new", loaded.WorkerID)
	}
	if atomic.LoadInt32(&fake.bootstrapCalls) != 1 {
		t.Errorf("Bootstrap calls: got %d", fake.bootstrapCalls)
	}
}

func TestWorkerInstallReinstallServerPresentLocalMissing(t *testing.T) {
	var postCount int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_existing","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme","created_at":"2026-05-04T12:00:00Z"}]}`))
		case http.MethodPost:
			atomic.AddInt32(&postCount, 1)
			writer.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:team-acme",
	})

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)

	if _, err := runCmd(t, "worker", "install", "alpha"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if atomic.LoadInt32(&postCount) != 0 {
		t.Errorf("should NOT POST when server has the worker, got %d", postCount)
	}
	loaded, readErr := worker.Read("alpha")
	if readErr != nil {
		t.Fatalf("local config should be hydrated from server, got err=%v", readErr)
	}
	if loaded.WorkerID != "wkr_existing" {
		t.Errorf("WorkerID: got %q, want wkr_existing", loaded.WorkerID)
	}
	if atomic.LoadInt32(&fake.bootstrapCalls) != 1 {
		t.Errorf("Bootstrap: got %d", fake.bootstrapCalls)
	}
}

func TestWorkerInstallBootstrapFailureSurfacesError(t *testing.T) {
	var deleteCalls int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[]}`))
		case http.MethodPost:
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			writer.Write([]byte(`{"id":"wkr_1","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme","created_at":"2026-05-04T12:00:00Z"}`))
		case http.MethodDelete:
			atomic.AddInt32(&deleteCalls, 1)
			writer.WriteHeader(http.StatusOK)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:team-acme",
	})

	fake := &fakeServiceInstaller{err: errors.New("launchctl bootstrap exploded")}
	withFakeInstaller(t, fake)

	_, err := runCmd(t, "worker", "install", "alpha")
	if err == nil {
		t.Fatal("expected error when Bootstrap fails")
	}
	if !strings.Contains(err.Error(), "launchctl bootstrap exploded") {
		t.Errorf("expected wrapped Bootstrap error, got: %v", err)
	}
	// Server worker + local config remain — operator can re-run install.
	if _, readErr := worker.Read("alpha"); readErr != nil {
		t.Errorf("local config should remain after Bootstrap failure, got err=%v", readErr)
	}
	// Plan: "leave server worker in place" on Bootstrap failure. Lock
	// in zero compensating DELETE calls — auto-rolling back the server
	// record on a local-platform problem would make retry strictly
	// worse (operator has to re-create instead of re-running install).
	if got := atomic.LoadInt32(&deleteCalls); got != 0 {
		t.Errorf("Bootstrap failure must NOT trigger server-side DELETE; got %d", got)
	}
}

func TestWorkerInstallRejectsPersonalContext(t *testing.T) {
	var called int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&called, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "personal",
	})

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)

	_, err := runCmd(t, "worker", "install", "alpha")
	if err == nil {
		t.Fatal("expected personal-context error")
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("server should not be called, got %d", called)
	}
	if atomic.LoadInt32(&fake.bootstrapCalls) != 0 {
		t.Errorf("Bootstrap should not be called, got %d", fake.bootstrapCalls)
	}
}

func TestWorkerInstallUnsupportedOS(t *testing.T) {
	var called int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&called, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:team-acme",
	})
	withUnsupportedOS(t)

	_, err := runCmd(t, "worker", "install", "alpha")
	if err == nil {
		t.Fatal("expected unsupported-OS error")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("expected 'not supported' message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "worker start") {
		t.Errorf("error should suggest `worker start`, got: %v", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("no HTTP call should occur on unsupported OS, got %d", called)
	}
}

func TestWorkerInstallNoLingerFlagPropagates(t *testing.T) {
	// Mode 4 setup (clean install: empty server list, no local config)
	// so we exercise the path that actually builds `opts` from the flag
	// and threads it through to Bootstrap. Earlier the test used a
	// preseeded server record, which masked any regression in the opts-
	// build path because Mode 1/3 short-circuit before opts construction
	// was relevant to the test's intent.
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[]}`))
		case http.MethodPost:
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			writer.Write([]byte(`{"id":"wkr_1","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme","created_at":"2026-05-04T12:00:00Z"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:team-acme",
	})

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)

	if _, err := runCmd(t, "worker", "install", "alpha", "--no-linger"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !fake.gotNoLinger {
		t.Errorf("--no-linger should propagate to opts.NoLinger=true")
	}
}

// TestWorkerInstallNonInteractiveFlagPropagates exercises the
// --non-interactive flag in isolation (no --no-linger). On linux the
// flag is the non-aliased name for the same NoLinger semantic — either
// flag set OR'd should produce NoLinger=true.
func TestWorkerInstallNonInteractiveFlagPropagates(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[]}`))
		case http.MethodPost:
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			writer.Write([]byte(`{"id":"wkr_1","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme","created_at":"2026-05-04T12:00:00Z"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "team:team-acme",
	})

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)

	if _, err := runCmd(t, "worker", "install", "alpha", "--non-interactive"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !fake.gotNoLinger {
		t.Errorf("--non-interactive (alone) should propagate to opts.NoLinger=true")
	}
}

// TestWorkerInstallDarwinWarnsOnNoLingerFlags asserts that on macOS the
// linger-related flags emit a stderr warning rather than silently
// vanishing. The Bootstrap call is still issued (the launchd adapter
// ignores NoLinger), but scripted callers parsing stderr can detect
// the misuse.
func TestWorkerInstallDarwinWarnsOnNoLingerFlags(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only behavior; runtime.GOOS is " + runtime.GOOS)
	}
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[]}`))
		case http.MethodPost:
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			writer.Write([]byte(`{"id":"wkr_1","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme","created_at":"2026-05-04T12:00:00Z"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	cases := []struct {
		name     string
		flag     string
		wantText string
	}{
		{"no-linger on darwin", "--no-linger", "--no-linger has no effect on macOS"},
		{"non-interactive on darwin", "--non-interactive", "--non-interactive has no effect on macOS"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resetWorkerState(t, &config.Config{
				AuthToken:      "tok",
				ServerURL:      httpToWS(stub.URL) + "/ws",
				CurrentContext: "team:team-acme",
			})
			fake := &fakeServiceInstaller{}
			withFakeInstaller(t, fake)

			out, err := runCmd(t, "worker", "install", "alpha", testCase.flag)
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			// runCmd returns combined stdout+stderr — assert the warning
			// is present without being strict about exact byte position.
			if !strings.Contains(out, testCase.wantText) {
				t.Errorf("expected darwin warning %q in output, got: %s", testCase.wantText, out)
			}
		})
	}
}

// Sanity check the OS-dispatch factory at runtime for the host platform —
// keeps the production wiring covered without exercising real launchctl.
func TestNewServiceInstallerForCurrentOS(t *testing.T) {
	got, err := defaultNewServiceInstaller(runtime.GOOS)
	switch runtime.GOOS {
	case "darwin", "linux":
		if err != nil {
			t.Errorf("expected installer for %s, got err=%v", runtime.GOOS, err)
		}
		if got == nil {
			t.Errorf("expected non-nil installer for %s", runtime.GOOS)
		}
	default:
		if err == nil {
			t.Errorf("expected unsupported error for %s", runtime.GOOS)
		}
	}
}

func TestWorkerURLForConfigured(t *testing.T) {
	cases := []struct {
		name      string
		serverURL string
		subdomain string
		want      string
	}{
		{
			name:      "production wss api host",
			serverURL: "wss://api.justtunnel.dev/ws",
			subdomain: "build--acme",
			want:      "https://build--acme.justtunnel.dev",
		},
		{
			name:      "https api host without ws path",
			serverURL: "https://api.justtunnel.dev",
			subdomain: "alpha--team",
			want:      "https://alpha--team.justtunnel.dev",
		},
		{
			name:      "localhost dev fallback",
			serverURL: "ws://localhost:8080/ws",
			subdomain: "alpha--team",
			want:      "http://localhost:8080/alpha--team",
		},
		{
			name:      "custom domain without api prefix",
			serverURL: "wss://tunnels.example.com/ws",
			subdomain: "alpha--team",
			want:      "https://tunnels.example.com/alpha--team",
		},
		{
			// Dev/staging splits sometimes pin api.* on a non-default
			// port. We deliberately do NOT strip "api." in that case
			// because rewriting `api.example.com:8443` to
			// `<sub>.example.com:8443` would silently change the
			// host's intent. Fall back to /<subdomain>.
			name:      "api host with explicit port falls back to path form",
			serverURL: "https://api.example.com:8443/ws",
			subdomain: "build--acme",
			want:      "https://api.example.com:8443/build--acme",
		},
		{
			name:      "api host with explicit port wss scheme",
			serverURL: "wss://api.example.com:8443/ws",
			subdomain: "build--acme",
			want:      "https://api.example.com:8443/build--acme",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workerURL(tc.serverURL, tc.subdomain)
			if err != nil {
				t.Fatalf("workerURL: %v", err)
			}
			if got != tc.want {
				t.Errorf("workerURL(%q, %q) = %q, want %q", tc.serverURL, tc.subdomain, got, tc.want)
			}
		})
	}
}

// TestWorkerURLEmptySubdomain locks in the empty-subdomain guard. A
// caller that forgets to pass a subdomain should get a deterministic
// error instead of a malformed URL with a leading dot.
func TestWorkerURLEmptySubdomain(t *testing.T) {
	got, err := workerURL("wss://api.justtunnel.dev/ws", "")
	if err == nil {
		t.Fatalf("expected error for empty subdomain, got %q", got)
	}
	if !strings.Contains(err.Error(), "empty subdomain") {
		t.Errorf("error should mention empty subdomain, got: %v", err)
	}
}
