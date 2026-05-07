package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

// resetWorkerState wires the CLI into a per-test temp config + temp
// JUSTTUNNEL_HOME so worker.Read/Write/List/Delete operate against an
// isolated directory. It also zeroes ALL worker-command package-level
// flag state and installs a t.Cleanup to zero them again on test exit,
// mirroring resetContextState's pattern. Without this, a test that ran
// with e.g. --delete-on-server or --no-linger would leak the `true`
// value into the next test's cobra Execute() call. Returns the config
// path.
//
// When adding a new package-level cobra flag var to any worker_*.go,
// add it here too. Current flags reset:
//   - workerRmDeleteOnServer (worker_rm.go)
//   - workerInstallNoLinger  (worker_install.go)
//   - workerInstallNonInteractive (worker_install.go)
//   - workerUninstallDeleteOnServer (worker_uninstall.go)
//   - workerUninstallForce (worker_uninstall.go)
//   - workerListAll (worker_list.go)
func resetWorkerState(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path := resetContextState(t, cfg)
	tmpHome := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", tmpHome)
	workerRmDeleteOnServer = false
	workerInstallNoLinger = false
	workerInstallNonInteractive = false
	workerUninstallDeleteOnServer = false
	workerUninstallForce = false
	workerListAll = false
	// loadWorkerEnv now performs an F-21 staleness check by hitting
	// /api/memberships. The single-handler stubs used by worker tests
	// don't model that endpoint and would either decode garbage as an
	// empty membership list (false-positive stale) or return arbitrary
	// success bodies. Default to "endpoint unsupported" (supported=false)
	// so the staleness check fails open in worker tests; tests that
	// specifically want to exercise the stale path can override
	// fetchMemberships themselves.
	previousFetcher := fetchMemberships
	fetchMemberships = func(client *http.Client, baseURL, authToken string) ([]membership, bool, error) {
		return nil, false, nil
	}
	t.Cleanup(func() {
		workerRmDeleteOnServer = false
		workerInstallNoLinger = false
		workerInstallNonInteractive = false
		workerUninstallDeleteOnServer = false
		workerUninstallForce = false
		workerListAll = false
		fetchMemberships = previousFetcher
	})
	return path
}

// TestWorkerCommandRejectsStaleContext verifies F-21 on the worker
// command path: when the active context is a team the user is no longer a
// member of, worker subcommands fail fast with an actionable message
// referencing `context use personal`, instead of the opaque 403/404 from
// the team-scoped REST route.
func TestWorkerCommandRejectsStaleContext(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Worker REST calls should never run — loadWorkerEnv must abort
		// before reaching them.
		t.Errorf("unexpected request to %s; loadWorkerEnv should have aborted", request.URL.Path)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	// Override the fetcher installed by resetWorkerState so the membership
	// check actually runs and reports the active team as missing.
	previousFetcher := fetchMemberships
	t.Cleanup(func() { fetchMemberships = previousFetcher })
	fetchMemberships = func(client *http.Client, baseURL, authToken string) ([]membership, bool, error) {
		return []membership{{TeamSlug: "some-other-team"}}, true, nil
	}

	_, err := runCmd(t, "worker", "list")
	if err == nil {
		t.Fatal("expected loadWorkerEnv to reject stale team context, got nil error")
	}
	if !strings.Contains(err.Error(), "context use personal") {
		t.Errorf("error should reference recovery command; got %v", err)
	}
	if !strings.Contains(err.Error(), "team-alpha") {
		t.Errorf("error should mention the stale slug; got %v", err)
	}
}

func teamCfg(serverURL string) *config.Config {
	return &config.Config{
		AuthToken:      "justtunnel_test_token",
		ServerURL:      httpToWS(serverURL) + "/ws",
		CurrentContext: "team:team-alpha",
	}
}

func TestWorkerCreateHappyPath(t *testing.T) {
	var receivedBody []byte
	var receivedAuth string
	var receivedPath string
	var receivedMethod string

	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedMethod = request.Method
		receivedPath = request.URL.Path
		receivedAuth = request.Header.Get("Authorization")
		receivedBody, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		writer.Write([]byte(`{"id":"wkr_123","name":"alice","team_id":"team-alpha","subdomain":"alice-team-alpha","created_at":"2026-05-04T12:00:00Z"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	out, err := runCmd(t, "worker", "create", "alice")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("method: got %q, want POST", receivedMethod)
	}
	if receivedPath != "/api/teams/team-alpha/workers" {
		t.Errorf("path: got %q, want /api/teams/team-alpha/workers", receivedPath)
	}
	if receivedAuth != "Bearer justtunnel_test_token" {
		t.Errorf("auth header: got %q", receivedAuth)
	}
	var body map[string]string
	if err := json.Unmarshal(receivedBody, &body); err != nil {
		t.Fatalf("body json: %v", err)
	}
	if body["name"] != "alice" {
		t.Errorf("body name: got %q, want alice", body["name"])
	}

	// Local config should be persisted.
	loaded, err := worker.Read("alice")
	if err != nil {
		t.Fatalf("read local config: %v", err)
	}
	if loaded.WorkerID != "wkr_123" {
		t.Errorf("local WorkerID: got %q", loaded.WorkerID)
	}
	if loaded.Context != "team:team-alpha" {
		t.Errorf("local Context: got %q, want team:team-alpha", loaded.Context)
	}
	// A4: locally-derived subdomain (`<name>--<slug>`) is authoritative
	// over the server's value. The stub returns "alice-team-alpha" (one
	// dash) deliberately to exercise the disagreement path; the local
	// derivation `alice--team-alpha` (two dashes) is what we persist.
	if loaded.Subdomain != "alice--team-alpha" {
		t.Errorf("local Subdomain: got %q, want alice--team-alpha (locally-derived)", loaded.Subdomain)
	}
	if loaded.ServiceBackend != "none" {
		t.Errorf("local ServiceBackend: got %q, want none", loaded.ServiceBackend)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("expected output to mention alice, got: %s", out)
	}
}

func TestWorkerCreateRejectsPersonalContext(t *testing.T) {
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

	_, err := runCmd(t, "worker", "create", "alice")
	if err == nil {
		t.Fatal("expected error for personal context, got nil")
	}
	if !strings.Contains(err.Error(), "team context") {
		t.Errorf("error should mention team context, got: %v", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("server should not have been called, got %d calls", called)
	}
}

func TestWorkerCreateBadName400(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		writer.Write([]byte(`{"error":"invalid worker name"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	_, err := runCmd(t, "worker", "create", "BadName")
	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if !strings.Contains(err.Error(), "invalid worker name") {
		t.Errorf("expected server error message, got: %v", err)
	}
}

func TestWorkerCreateDuplicate409(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusConflict)
		writer.Write([]byte(`{"error":"worker name already exists"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	_, err := runCmd(t, "worker", "create", "alice")
	if err == nil {
		t.Fatal("expected error for 409, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		t.Errorf("expected already-exists error, got: %v", err)
	}
}

func TestWorkerListMergesServerAndLocal(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/teams/team-alpha/workers" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"workers":[
			{"id":"wkr_1","name":"alice","team_id":"team-alpha","subdomain":"alice-team-alpha","status":"active"},
			{"id":"wkr_2","name":"bob","team_id":"team-alpha","subdomain":"bob-team-alpha","status":"quarantined"}
		]}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	// Seed local-only worker "carol" and shared worker "alice" so we exercise
	// both dedup and "local-only" / "server-only" markers. The local
	// "alice" uses the SAME WorkerID as the server entry — dedup is now
	// keyed on ID (per worker_list.go mergeWorkers), so a stale local
	// row with a different ID would intentionally render as two rows.
	if err := worker.Write(&worker.Config{WorkerID: "wkr_1", Name: "alice", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	if err := worker.Write(&worker.Config{WorkerID: "wkr_local_carol", Name: "carol", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed carol: %v", err)
	}

	out, err := runCmd(t, "worker", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		if !strings.Contains(out, name) {
			t.Errorf("output should include %q, got: %s", name, out)
		}
	}
	// alice exists both places — should appear in exactly one row, marked
	// "synced" rather than as two separate rows.
	aliceRows := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "alice" {
			aliceRows++
		}
	}
	if aliceRows != 1 {
		t.Errorf("alice should appear in exactly one row, got %d: %s", aliceRows, out)
	}
	if !strings.Contains(out, "synced") {
		t.Errorf("alice should be marked synced, got: %s", out)
	}
	if !strings.Contains(out, "server-only") {
		t.Errorf("bob should be marked server-only, got: %s", out)
	}
	if !strings.Contains(out, "local-only") {
		t.Errorf("carol should be marked local-only, got: %s", out)
	}
}

func TestWorkerRmLocalOnlyDefault(t *testing.T) {
	var called int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&called, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := runCmd(t, "worker", "rm", "alice")
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("server should not be called for local-only rm, got %d calls", called)
	}
	if _, err := worker.Read("alice"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("local config should be gone, got err=%v", err)
	}
	if !strings.Contains(out, "may still be registered server-side") {
		t.Errorf("output should hint about server-side, got: %s", out)
	}
}

func TestWorkerRmDeleteOnServerHappyPath(t *testing.T) {
	var receivedMethod, receivedPath string
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Route 1: list endpoint to look up worker ID.
		if request.Method == http.MethodGet && request.URL.Path == "/api/teams/team-alpha/workers" {
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_x","name":"alice","team_id":"team-alpha"}]}`))
			return
		}
		// Route 2: the DELETE.
		receivedMethod = request.Method
		receivedPath = request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte(`{"status":"deleted"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := runCmd(t, "worker", "rm", "alice", "--delete-on-server"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if receivedMethod != http.MethodDelete {
		t.Errorf("method: got %q, want DELETE", receivedMethod)
	}
	if receivedPath != "/api/teams/team-alpha/workers/wkr_x" {
		t.Errorf("path: got %q", receivedPath)
	}
	if _, err := worker.Read("alice"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("local config should be deleted (want os.ErrNotExist), got err=%v", err)
	}
}

func TestWorkerRmDeleteOnServer404ProceedsLocally(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_x","name":"alice","team_id":"team-alpha"}]}`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := runCmd(t, "worker", "rm", "alice", "--delete-on-server"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if _, err := worker.Read("alice"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("local config should still be deleted on 404 (want os.ErrNotExist), got err=%v", err)
	}
}

func TestWorkerRmDeleteOnServer403LeavesLocal(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_x","name":"alice","team_id":"team-alpha"}]}`))
			return
		}
		writer.WriteHeader(http.StatusForbidden)
		writer.Write([]byte(`{"error":"only admins can delete workers"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := runCmd(t, "worker", "rm", "alice", "--delete-on-server")
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if _, err := worker.Read("alice"); err != nil {
		t.Errorf("local config should be preserved on 403, got read err: %v", err)
	}
}

// TestWorkerRmFlagDoesNotLeakBetweenInvocations exercises the regression
// behind blocker #1: cobra's bound flag value persists across Execute()
// calls in the same process, so a prior `--delete-on-server` would taint
// a subsequent local-only `rm`. resetWorkerState's t.Cleanup zeros the
// flag, but this test exercises the flag-reset path inside a single test
// to guarantee the contract.
func TestWorkerRmFlagDoesNotLeakBetweenInvocations(t *testing.T) {
	var deleteCalls int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_x","name":"alice","team_id":"team-alpha"}]}`))
			return
		}
		if request.Method == http.MethodDelete {
			atomic.AddInt32(&deleteCalls, 1)
			writer.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(writer, request)
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	// First invocation: with --delete-on-server. Should hit the DELETE.
	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	if _, err := runCmd(t, "worker", "rm", "alice", "--delete-on-server"); err != nil {
		t.Fatalf("first rm: %v", err)
	}
	if got := atomic.LoadInt32(&deleteCalls); got != 1 {
		t.Fatalf("first rm: expected 1 DELETE call, got %d", got)
	}

	// Manually clear the flag the way cobra would NOT — this simulates
	// the production fix: between user invocations the flag must reset.
	// We rely on the resetWorkerState contract here (cleanup + the
	// in-test re-zero below).
	workerRmDeleteOnServer = false

	// Second invocation: WITHOUT the flag. Local-only path; no DELETE.
	if err := worker.Write(&worker.Config{WorkerID: "wkr_y", Name: "bob", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed bob: %v", err)
	}
	if _, err := runCmd(t, "worker", "rm", "bob"); err != nil {
		t.Fatalf("second rm: %v", err)
	}
	if got := atomic.LoadInt32(&deleteCalls); got != 1 {
		t.Errorf("second rm should NOT have hit the server (flag leaked); DELETE count went from 1 to %d", got)
	}
	if _, err := worker.Read("bob"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bob local config should be gone, got err=%v", err)
	}
}

// TestWorkerRmLocalOnlyMissingConfigIsIdempotent covers blocker #3: a
// local-only `rm` against a name with no on-disk config previously printed
// the misleading "Removed local config" message. It should now print a
// "no local config found" message and exit 0.
func TestWorkerRmLocalOnlyMissingConfigIsIdempotent(t *testing.T) {
	var called int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&called, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	out, err := runCmd(t, "worker", "rm", "typo-name")
	if err != nil {
		t.Fatalf("rm of missing worker should succeed, got err=%v", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("local-only rm should not call the server, got %d calls", called)
	}
	if !strings.Contains(out, "No local config found") {
		t.Errorf("expected honest 'No local config found' message, got: %s", out)
	}
	if strings.Contains(out, "Removed local config") {
		t.Errorf("must NOT print false 'Removed local config' for missing worker, got: %s", out)
	}
}

// TestWorkerCreateRollsBackOnLocalWriteFailure covers blocker #2 happy
// rollback: local worker.Write fails, compensating DELETE succeeds. The
// CLI must surface a "rolled back, please retry" error.
func TestWorkerCreateRollsBackOnLocalWriteFailure(t *testing.T) {
	var deleteCalled int32
	var deletedID string
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			writer.Write([]byte(`{"id":"wkr_ghost","name":"alice","team_id":"team-alpha","subdomain":"alice-team-alpha","created_at":"2026-05-04T12:00:00Z"}`))
			return
		}
		if request.Method == http.MethodDelete {
			atomic.AddInt32(&deleteCalled, 1)
			// /api/teams/team-alpha/workers/wkr_ghost
			parts := strings.Split(request.URL.Path, "/")
			if len(parts) > 0 {
				deletedID = parts[len(parts)-1]
			}
			writer.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(writer, request)
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	// Force worker.Write to fail by pointing JUSTTUNNEL_HOME at a path
	// where the workers/ directory cannot be created — a regular file
	// occupying the spot, which makes os.MkdirAll fail with ENOTDIR.
	tmpHome := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", tmpHome)
	if err := os.WriteFile(tmpHome+"/workers", []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}

	_, err := runCmd(t, "worker", "create", "alice")
	if err == nil {
		t.Fatal("expected error when local write fails")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("expected error mentioning rollback, got: %v", err)
	}
	if atomic.LoadInt32(&deleteCalled) != 1 {
		t.Errorf("expected exactly one compensating DELETE, got %d", deleteCalled)
	}
	if deletedID != "wkr_ghost" {
		t.Errorf("compensating DELETE targeted wrong id: got %q want %q", deletedID, "wkr_ghost")
	}
}

// TestWorkerCreateGhostWarningWhenRollbackAlsoFails covers blocker #2 sad
// path: local write fails AND compensating DELETE fails. The CLI must
// surface the loud, actionable WARNING and instruct the user to clean up
// manually with `worker rm --delete-on-server`.
func TestWorkerCreateGhostWarningWhenRollbackAlsoFails(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			writer.Write([]byte(`{"id":"wkr_ghost","name":"alice","team_id":"team-alpha","subdomain":"alice-team-alpha","created_at":"2026-05-04T12:00:00Z"}`))
			return
		}
		// Compensating DELETE also fails.
		writer.WriteHeader(http.StatusInternalServerError)
		writer.Write([]byte(`{"error":"db down"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	tmpHome := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", tmpHome)
	if err := os.WriteFile(tmpHome+"/workers", []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}

	_, err := runCmd(t, "worker", "create", "alice")
	if err == nil {
		t.Fatal("expected error when both write and rollback fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "WARNING") {
		t.Errorf("error should start with loud WARNING, got: %v", err)
	}
	if !strings.Contains(msg, "wkr_ghost") {
		t.Errorf("error should include the ghost worker id, got: %v", err)
	}
	if !strings.Contains(msg, "--delete-on-server") {
		t.Errorf("error should tell user how to clean up, got: %v", err)
	}
}

// TestWorkerListFiltersLocalConfigsByContext covers warning #8: a local
// config seeded under a different context must not appear in `worker
// list` for the active context.
func TestWorkerListFiltersLocalConfigsByContext(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"workers":[]}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL)) // active context: team:team-alpha

	if err := worker.Write(&worker.Config{WorkerID: "wkr_a", Name: "in-foo", Context: "team:team-alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed in-foo: %v", err)
	}
	if err := worker.Write(&worker.Config{WorkerID: "wkr_b", Name: "in-bar", Context: "team:team-bar", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed in-bar: %v", err)
	}

	out, err := runCmd(t, "worker", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "in-foo") {
		t.Errorf("expected in-foo in output, got: %s", out)
	}
	if strings.Contains(out, "in-bar") {
		t.Errorf("in-bar belongs to team:team-bar and must be filtered out, got: %s", out)
	}
}

// TestWorkerListDedupsByWorkerID covers warning #6: when a server entry
// and a local entry share an ID but somehow have differing names, dedup
// must key on ID. Also covers the duplicate-name marker for the rare
// server bug where two server entries report the same name.
func TestWorkerListDedupsByWorkerID(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		// Two server entries with the same name "twin" but different IDs.
		writer.Write([]byte(`{"workers":[
			{"id":"wkr_1","name":"twin","team_id":"team-alpha"},
			{"id":"wkr_2","name":"twin","team_id":"team-alpha"}
		]}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	out, err := runCmd(t, "worker", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "duplicate-name") {
		t.Errorf("expected [duplicate-name] marker for colliding server entries, got: %s", out)
	}
	// Both rows must be present (we surface both rather than overwriting).
	wkr1Count := strings.Count(out, "wkr_1")
	wkr2Count := strings.Count(out, "wkr_2")
	if wkr1Count != 1 || wkr2Count != 1 {
		t.Errorf("expected both wkr_1 and wkr_2 each shown exactly once, got wkr_1=%d wkr_2=%d in: %s", wkr1Count, wkr2Count, out)
	}
}

// TestResolveTeamIDRejectsMalformedSlugs covers warning #5: a hand-edited
// config or --context flag with an invalid slug must be rejected before
// it gets baked into a REST URL.
func TestResolveTeamIDRejectsMalformedSlugs(t *testing.T) {
	tests := []struct {
		name    string
		context string
	}{
		{"colon in identifier", "team:foo:bar"},
		{"underscore in identifier", "team:foo_bar"},
		{"space in identifier", "team:foo bar"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			resetWorkerState(t, &config.Config{
				AuthToken:      "tok",
				ServerURL:      "wss://api.example.com/ws",
				CurrentContext: testCase.context,
			})
			_, err := runCmd(t, "worker", "list")
			if err == nil {
				t.Fatalf("expected error for context %q, got nil", testCase.context)
			}
			if !strings.Contains(err.Error(), "context") {
				t.Errorf("error should mention context, got: %v", err)
			}
		})
	}
}

// TestWorkerRmDeleteOnServerMessageReflectsQuarantine guards against the
// regression in justtunnel-cli#50 where `rm --delete-on-server` printed
// "Deleted worker (server + local)" even though the server only
// soft-deletes and the worker remains visible (as retired_quarantined)
// in `worker list --all` for 30 days.
func TestWorkerRmDeleteOnServerMessageReflectsQuarantine(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/api/teams/team-alpha/workers" {
			writer.Header().Set("Content-Type", "application/json")
			writer.Write([]byte(`{"workers":[{"id":"wkr_x","name":"alice","team_id":"team-alpha"}]}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte(`{"status":"deleted"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_x", Name: "alice", Context: "team:team-alpha", ServiceBackend: "none",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := runCmd(t, "worker", "rm", "alice", "--delete-on-server")
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	// New honest messaging — must NOT claim outright deletion.
	if strings.Contains(out, "Deleted worker") {
		t.Errorf("output still claims permanent deletion (#50 regression): %s", out)
	}
	if !strings.Contains(out, "Quarantined") || !strings.Contains(out, "30 days") {
		t.Errorf("output should describe quarantine + 30-day reaper window: %s", out)
	}
}

// TestWorkerListHidesQuarantinedByDefault guards justtunnel-cli#50: the
// default `worker list` view must NOT display rows the server has
// soft-deleted, otherwise a user who just ran `rm --delete-on-server`
// sees their "deleted" worker still listed.
func TestWorkerListHidesQuarantinedByDefault(t *testing.T) {
	var observedInclude string
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedInclude = request.URL.Query().Get("include")
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"workers":[
		    {"id":"wkr_a","name":"alice","team_id":"team-alpha","subdomain":"alice--team-alpha","status":"online"},
		    {"id":"wkr_z","name":"zombie","team_id":"team-alpha","subdomain":"zombie--team-alpha","status":"retired_quarantined"}
		]}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	out, err := runCmd(t, "worker", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("active worker should be listed: %s", out)
	}
	if strings.Contains(out, "zombie") {
		t.Errorf("quarantined worker should be hidden by default: %s", out)
	}
	// Default mode must NOT pass `?include=quarantined` so the server keeps
	// the billing-quota-accurate filtered view (justtunnel-server #170).
	if observedInclude != "" {
		t.Errorf("default list must not request quarantined; got include=%q", observedInclude)
	}
}

// TestWorkerListAllShowsQuarantined verifies the --all flag opts the
// quarantined rows back in.
func TestWorkerListAllShowsQuarantined(t *testing.T) {
	var observedInclude string
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedInclude = request.URL.Query().Get("include")
		writer.Header().Set("Content-Type", "application/json")
		// Server only echoes back quarantined rows when explicitly asked
		// (justtunnel-server F-20). If the CLI omits the query param the
		// stub returns nothing, so the assertions below also catch a
		// regression where --all silently drops the include flag.
		if observedInclude != "quarantined" {
			writer.Write([]byte(`{"workers":[]}`))
			return
		}
		writer.Write([]byte(`{"workers":[
		    {"id":"wkr_z","name":"zombie","team_id":"team-alpha","subdomain":"zombie--team-alpha","status":"retired_quarantined"}
		]}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))

	out, err := runCmd(t, "worker", "list", "--all")
	if err != nil {
		t.Fatalf("list --all: %v", err)
	}
	if observedInclude != "quarantined" {
		t.Fatalf("--all must send ?include=quarantined; got include=%q", observedInclude)
	}
	if !strings.Contains(out, "zombie") {
		t.Errorf("--all should reveal quarantined rows: %s", out)
	}
}

// TestWorkerListSubdomainFallsBackToLocal guards F-07 (#51): when the
// server omits subdomain in its list response, the row should fall back
// to the locally-derived value rather than rendering "-".
func TestWorkerListSubdomainFallsBackToLocal(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		// subdomain field intentionally omitted server-side.
		writer.Write([]byte(`{"workers":[{"id":"wkr_a","name":"alice","team_id":"team-alpha","status":"offline"}]}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	if err := worker.Write(&worker.Config{
		WorkerID: "wkr_a", Name: "alice", Context: "team:team-alpha",
		Subdomain: "alice--team-alpha", ServiceBackend: "none",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := runCmd(t, "worker", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "alice--team-alpha") {
		t.Errorf("subdomain should fall back to local value, got: %s", out)
	}
}
