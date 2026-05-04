package cmd

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

// runCmdSplit executes a subcommand with separate stdout and stderr
// buffers so tests can assert on which stream a message landed on.
// The default runCmd helper merges both streams into one buffer.
func runCmdSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	stdoutBuf := &bytes.Buffer{}
	stderrBuf := &bytes.Buffer{}
	rootCmd.SetOut(stdoutBuf)
	rootCmd.SetErr(stderrBuf)
	rootCmd.SetArgs(args)
	err = rootCmd.Execute()
	rootCmd.SetArgs(nil)
	return stdoutBuf.String(), stderrBuf.String(), err
}

// teamCfgWithToken is a thin re-shape of teamCfg so uninstall tests can
// share the same auth-token + team-context defaults the install/rm tests
// already lock in.
func uninstallTeamCfg(serverURL string) *config.Config {
	return &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(serverURL) + "/ws",
		CurrentContext: "team:team-acme",
	}
}

// TestWorkerUninstallDefaultLocalOnly: without --delete-on-server the
// command must run Unbootstrap + worker.Delete and NEVER touch the HTTP
// server. Verifies the contract that operators who lost team membership
// can still tear down local state.
func TestWorkerUninstallDefaultLocalOnly(t *testing.T) {
	var httpCalls int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&httpCalls, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_1", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "launchd",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)
	useFakeSupervisor(t, newFakeSupervisor())

	out, err := runCmd(t, "worker", "uninstall", "alpha")
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := atomic.LoadInt32(&fake.unbootstrapCalls); got != 1 {
		t.Errorf("Unbootstrap calls: got %d, want 1", got)
	}
	if got := fake.readGotUnbootName(); got != "alpha" {
		t.Errorf("Unbootstrap name: got %q, want alpha", got)
	}
	if got := atomic.LoadInt32(&httpCalls); got != 0 {
		t.Errorf("HTTP must NOT be called without --delete-on-server, got %d", got)
	}
	if _, readErr := worker.Read("alpha"); !errors.Is(readErr, os.ErrNotExist) {
		t.Errorf("local config should be deleted, got err=%v", readErr)
	}
	if !strings.Contains(out, "Uninstalled") || !strings.Contains(out, "alpha") {
		t.Errorf("expected success line mentioning alpha, got: %s", out)
	}
}

// TestWorkerUninstallDeleteOnServerHappyPath: with --delete-on-server,
// command must GET the worker list, find by name, and DELETE by ID, in
// addition to the local steps.
func TestWorkerUninstallDeleteOnServerHappyPath(t *testing.T) {
	var deletePath string
	var deleteCount int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_1","name":"alpha","team_id":"team-acme","subdomain":"alpha--acme"}]}`))
		case http.MethodDelete:
			atomic.AddInt32(&deleteCount, 1)
			deletePath = request.URL.Path
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(`{"status":"deleted"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_1", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "launchd",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)
	useFakeSupervisor(t, newFakeSupervisor())

	if _, err := runCmd(t, "worker", "uninstall", "alpha", "--delete-on-server"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := atomic.LoadInt32(&fake.unbootstrapCalls); got != 1 {
		t.Errorf("Unbootstrap calls: got %d, want 1", got)
	}
	if got := atomic.LoadInt32(&deleteCount); got != 1 {
		t.Errorf("DELETE calls: got %d, want 1", got)
	}
	if deletePath != "/api/teams/team-acme/workers/wkr_1" {
		t.Errorf("DELETE path: got %q", deletePath)
	}
	if _, readErr := worker.Read("alpha"); !errors.Is(readErr, os.ErrNotExist) {
		t.Errorf("local config should be deleted, got err=%v", readErr)
	}
}

// TestWorkerUninstallIdempotent: re-running on a fully-cleaned state
// must succeed and emit the "already uninstalled" line. Unbootstrap is
// still invoked (it's idempotent in the per-OS impls), but no HTTP
// calls are made since --delete-on-server was not set.
func TestWorkerUninstallIdempotent(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)
	useFakeSupervisor(t, newFakeSupervisor())

	// First call: nothing on disk. Should succeed and report "already
	// uninstalled" because no local-config change occurred.
	out, err := runCmd(t, "worker", "uninstall", "alpha")
	if err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	if !strings.Contains(out, "already uninstalled") {
		t.Errorf("first uninstall should report 'already uninstalled' on clean state, got: %s", out)
	}
	// Unbootstrap must still be invoked (idempotent contract).
	if got := atomic.LoadInt32(&fake.unbootstrapCalls); got != 1 {
		t.Errorf("Unbootstrap calls after first run: got %d, want 1", got)
	}

	// Second call: still nothing on disk. Same behavior — no error.
	out, err = runCmd(t, "worker", "uninstall", "alpha")
	if err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if !strings.Contains(out, "already uninstalled") {
		t.Errorf("second uninstall should still report 'already uninstalled', got: %s", out)
	}
	if got := atomic.LoadInt32(&fake.unbootstrapCalls); got != 2 {
		t.Errorf("Unbootstrap calls after second run: got %d, want 2", got)
	}
}

// TestWorkerUninstallDeleteOnServer404ProceedsLocally: when
// --delete-on-server is requested but the server has no record of the
// worker, the local cleanup must still complete and the command must
// exit 0 (the server's view IS the desired end state).
func TestWorkerUninstallDeleteOnServer404ProceedsLocally(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[]}`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_stale", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "launchd",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)
	useFakeSupervisor(t, newFakeSupervisor())

	if _, err := runCmd(t, "worker", "uninstall", "alpha", "--delete-on-server"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, readErr := worker.Read("alpha"); !errors.Is(readErr, os.ErrNotExist) {
		t.Errorf("local config should be deleted on 'server already gone', got err=%v", readErr)
	}
}

// TestWorkerUninstallDeleteOnServer403LeavesLocal: a 403 from the
// server-side DELETE must abort the command BEFORE any local mutation,
// so operators can retry with a permitted account without having lost
// the local pointer to the worker.
func TestWorkerUninstallDeleteOnServer403LeavesLocal(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_1","name":"alpha","team_id":"team-acme"}]}`))
			return
		}
		writer.WriteHeader(http.StatusForbidden)
		writer.Write([]byte(`{"error":"only admins can delete workers"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_1", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "launchd",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "uninstall", "alpha", "--delete-on-server")
	if err == nil {
		t.Fatal("expected error on 403 without --force")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "server-side") &&
		!strings.Contains(err.Error(), "403") {
		t.Errorf("expected error to surface server-side failure, got: %v", err)
	}
	// Local config MUST remain so the operator can re-run with a
	// permitted account.
	if _, readErr := worker.Read("alpha"); readErr != nil {
		t.Errorf("local config must remain after 403, got err=%v", readErr)
	}
	// Service teardown must NOT have run either — server delete is
	// the gate for any local mutation under --delete-on-server.
	if got := atomic.LoadInt32(&fake.unbootstrapCalls); got != 0 {
		t.Errorf("Unbootstrap must NOT run before successful server delete, got %d calls", got)
	}
}

// TestWorkerUninstallDeleteOnServer404OnDelete: when GET returns the
// worker but DELETE races with another deletion and returns 404, the
// command must treat it as already-deleted and proceed with local
// cleanup (exit 0).
func TestWorkerUninstallDeleteOnServer404OnDelete(t *testing.T) {
	var deleteCount int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_1","name":"alpha","team_id":"team-acme"}]}`))
		case http.MethodDelete:
			atomic.AddInt32(&deleteCount, 1)
			writer.WriteHeader(http.StatusNotFound)
			writer.Write([]byte(`{"error":"not found"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_1", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "launchd",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "uninstall", "alpha", "--delete-on-server")
	if err != nil {
		t.Fatalf("404 on DELETE must not fail the command, got: %v", err)
	}
	if got := atomic.LoadInt32(&deleteCount); got != 1 {
		t.Errorf("DELETE should have been attempted once, got %d", got)
	}
	if got := atomic.LoadInt32(&fake.unbootstrapCalls); got != 1 {
		t.Errorf("local cleanup should still run after 404, Unbootstrap calls: got %d, want 1", got)
	}
	if _, readErr := worker.Read("alpha"); !errors.Is(readErr, os.ErrNotExist) {
		t.Errorf("local config should be deleted after 404 on server, got err=%v", readErr)
	}
}

// TestWorkerUninstallForceContinuesPastUnbootstrapFailure: with --force,
// an Unbootstrap error must NOT abort the command. Local cleanup runs,
// the warning lands on stderr, and the command exits 0.
func TestWorkerUninstallForceContinuesPastUnbootstrapFailure(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_1", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "launchd",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{unbootstrapErr: errors.New("launchctl wedged")}
	withFakeInstaller(t, fake)
	useFakeSupervisor(t, newFakeSupervisor())

	stdout, stderr, err := runCmdSplit(t, "worker", "uninstall", "alpha", "--force")
	if err != nil {
		t.Fatalf("--force should not return an error, got: %v", err)
	}
	if _, readErr := worker.Read("alpha"); !errors.Is(readErr, os.ErrNotExist) {
		t.Errorf("local cleanup should still run under --force, got err=%v", readErr)
	}
	// The original error and step label belong on stderr only.
	if !strings.Contains(stderr, "launchctl wedged") {
		t.Errorf("--force should surface the original error on stderr, got stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "service teardown") {
		t.Errorf("--force summary should label the failing step on stderr, got stderr: %s", stderr)
	}
	if strings.Contains(stdout, "launchctl wedged") || strings.Contains(stdout, "service teardown") {
		t.Errorf("error summary must NOT leak onto stdout, got stdout: %s", stdout)
	}
	// The success line belongs on stdout only.
	if !strings.Contains(stdout, "Uninstalled") {
		t.Errorf("success line should appear on stdout, got stdout: %s", stdout)
	}
	if strings.Contains(stderr, "Uninstalled") {
		t.Errorf("success line must NOT leak onto stderr, got stderr: %s", stderr)
	}
}

// TestWorkerUninstallNoForceUnbootstrapFailureAborts: without --force,
// an Unbootstrap error must abort BEFORE local cleanup so the operator
// can investigate without losing the local pointer to the worker.
func TestWorkerUninstallNoForceUnbootstrapFailureAborts(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_1", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "launchd",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{unbootstrapErr: errors.New("launchctl wedged")}
	withFakeInstaller(t, fake)
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "uninstall", "alpha")
	if err == nil {
		t.Fatal("expected error without --force when Unbootstrap fails")
	}
	if !strings.Contains(err.Error(), "launchctl wedged") {
		t.Errorf("expected wrapped Unbootstrap error, got: %v", err)
	}
	// Local config must remain so the operator has a path forward.
	if _, readErr := worker.Read("alpha"); readErr != nil {
		t.Errorf("local config must remain after aborted uninstall, got err=%v", readErr)
	}
}

// TestWorkerUninstallRejectsPersonalContext: --delete-on-server requires
// a team context. We must fail BEFORE any HTTP call so a personal-context
// operator never inadvertently probes the worker API.
func TestWorkerUninstallRejectsPersonalContext(t *testing.T) {
	var called int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
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
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "uninstall", "alpha", "--delete-on-server")
	if err == nil {
		t.Fatal("expected personal-context error")
	}
	if !strings.Contains(err.Error(), "team context") {
		t.Errorf("error should mention team context, got: %v", err)
	}
	if got := atomic.LoadInt32(&called); got != 0 {
		t.Errorf("HTTP must not be called for personal-context uninstall, got %d", got)
	}
}

// TestWorkerUninstallUnsupportedOS: on a non-darwin/non-linux build the
// command must fail with a friendly error pointing at `worker rm`. No
// HTTP calls and no Unbootstrap.
func TestWorkerUninstallUnsupportedOS(t *testing.T) {
	var called int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&called, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))
	withUnsupportedOS(t)
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "uninstall", "alpha")
	if err == nil {
		t.Fatal("expected unsupported-OS error")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("expected 'not supported' message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "worker rm") {
		t.Errorf("error should suggest `worker rm`, got: %v", err)
	}
	if got := atomic.LoadInt32(&called); got != 0 {
		t.Errorf("HTTP must not be called on unsupported OS, got %d", got)
	}
}

// TestWorkerUninstallProbeWarnsWhenStillRunning: the post-uninstall
// probe must surface a stderr warning if the supervisor still reports
// the worker as managed/running, but it must NOT fail the command.
func TestWorkerUninstallProbeWarnsWhenStillRunning(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, uninstallTeamCfg(stub.URL))

	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_1", Name: "alpha", Context: "team:team-acme",
		Subdomain: "alpha--acme", CreatedAt: time.Now().UTC(), ServiceBackend: "launchd",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	fake := &fakeServiceInstaller{}
	withFakeInstaller(t, fake)

	supervisor := newFakeSupervisor()
	supervisor.results["alpha"] = worker.ProbeResult{
		ServiceBackend: "launchd",
		ManagedByUs:    true,
		Running:        true,
		Detail:         "pid 4242",
	}
	useFakeSupervisor(t, supervisor)

	out, err := runCmd(t, "worker", "uninstall", "alpha")
	if err != nil {
		t.Fatalf("probe-still-running must NOT fail the command, got: %v", err)
	}
	if !strings.Contains(out, "still appears") {
		t.Errorf("expected stderr warning about residual state, got: %s", out)
	}
}
