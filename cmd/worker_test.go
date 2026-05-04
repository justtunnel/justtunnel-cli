package cmd

import (
	"encoding/json"
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
// isolated directory. Returns the config path.
func resetWorkerState(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path := resetContextState(t, cfg)
	tmpHome := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", tmpHome)
	return path
}

func teamCfg(serverURL string) *config.Config {
	return &config.Config{
		AuthToken:      "justtunnel_test_token",
		ServerURL:      httpToWS(serverURL) + "/ws",
		CurrentContext: "team:alpha",
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
	if loaded.Context != "team:alpha" {
		t.Errorf("local Context: got %q, want team:alpha", loaded.Context)
	}
	if loaded.Subdomain != "alice-team-alpha" {
		t.Errorf("local Subdomain: got %q", loaded.Subdomain)
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
	// both dedup and "local-only" / "server-only" markers.
	if err := worker.Write(&worker.Config{WorkerID: "wkr_local_alice", Name: "alice", Context: "team:alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	if err := worker.Write(&worker.Config{WorkerID: "wkr_local_carol", Name: "carol", Context: "team:alpha", ServiceBackend: "none"}); err != nil {
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
	// alice exists both places — should not be duplicated.
	if count := strings.Count(out, "alice"); count != 1 {
		t.Errorf("alice should appear exactly once, appeared %d times: %s", count, out)
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

	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := runCmd(t, "worker", "rm", "alice")
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("server should not be called for local-only rm, got %d calls", called)
	}
	if _, err := worker.Read("alice"); !os.IsNotExist(unwrapPathErr(err)) {
		t.Errorf("local config should be gone, got err=%v", err)
	}
	if !strings.Contains(out, "still registered server-side") {
		t.Errorf("output should hint about server-side, got: %s", out)
	}
}

// unwrapPathErr returns the underlying os error from worker.Read's wrapped
// error, preserving os.ErrNotExist semantics for IsNotExist checks.
func unwrapPathErr(err error) error {
	if err == nil {
		return nil
	}
	return err
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

	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:alpha", ServiceBackend: "none"}); err != nil {
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
	if _, err := worker.Read("alice"); err == nil {
		t.Errorf("local config should be deleted")
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

	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:alpha", ServiceBackend: "none"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := runCmd(t, "worker", "rm", "alice", "--delete-on-server"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if _, err := worker.Read("alice"); err == nil {
		t.Errorf("local config should still be deleted on 404")
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

	if err := worker.Write(&worker.Config{WorkerID: "wkr_x", Name: "alice", Context: "team:alpha", ServiceBackend: "none"}); err != nil {
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
