package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justtunnel/justtunnel-cli/internal/config"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
)

// fakeSupervisor is a test double for worker.Supervisor. It records every
// probed name and returns a per-name canned ProbeResult / error map.
type fakeSupervisor struct {
	probed  []string
	results map[string]worker.ProbeResult
	errs    map[string]error
}

func newFakeSupervisor() *fakeSupervisor {
	return &fakeSupervisor{
		results: map[string]worker.ProbeResult{},
		errs:    map[string]error{},
	}
}

func (fake *fakeSupervisor) Probe(ctx context.Context, workerName string) (worker.ProbeResult, error) {
	fake.probed = append(fake.probed, workerName)
	if err, ok := fake.errs[workerName]; ok {
		return worker.ProbeResult{}, err
	}
	if result, ok := fake.results[workerName]; ok {
		return result, nil
	}
	return worker.ProbeResult{ServiceBackend: "launchd", Detail: "probe not yet implemented"}, nil
}

// useFakeSupervisor swaps the package-level factory for the duration of
// the test. The cleanup restores the production factory so no other test
// is affected.
func useFakeSupervisor(t *testing.T, fake *fakeSupervisor) {
	t.Helper()
	prev := supervisorFactory
	supervisorFactory = func() worker.Supervisor { return fake }
	t.Cleanup(func() { supervisorFactory = prev })
}

func seedLocalWorker(t *testing.T, cfg worker.Config) {
	t.Helper()
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	}
	if err := worker.Write(&cfg); err != nil {
		t.Fatalf("seed local worker %q: %v", cfg.Name, err)
	}
}

// stubWorkersServer returns an httptest.Server that responds to GET
// /api/teams/<teamID>/workers with the supplied JSON body.
func stubWorkersServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || !strings.HasSuffix(request.URL.Path, "/workers") {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(writer, `{"error":"test stub: unhandled %s %s"}`, request.Method, request.URL.Path)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(body))
	}))
}

func TestWorkerStatusListHappyPath(t *testing.T) {
	stub := stubWorkersServer(`{"workers":[
		{"id":"wkr_1","name":"build","team_id":"team-alpha","status":"online","last_seen_at":"2026-05-04T12:34:56Z"},
		{"id":"wkr_2","name":"deploy","team_id":"team-alpha","status":"offline"}
	]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	seedLocalWorker(t, worker.Config{
		WorkerID:       "wkr_1",
		Name:           "build",
		Context:        "team:team-alpha",
		Subdomain:      "build-team-alpha",
		ServiceBackend: "launchd",
	})

	fake := newFakeSupervisor()
	fake.results["build"] = worker.ProbeResult{ServiceBackend: "launchd", ManagedByUs: true, Running: true}
	useFakeSupervisor(t, fake)

	out, err := runCmd(t, "worker", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "build") || !strings.Contains(out, "deploy") {
		t.Fatalf("expected both workers in output, got:\n%s", out)
	}
	if !strings.Contains(out, "online") || !strings.Contains(out, "offline") {
		t.Errorf("expected both server statuses in output, got:\n%s", out)
	}
	if !strings.Contains(out, "launchd:running") {
		t.Errorf("expected launchd:running for build, got:\n%s", out)
	}
	if !strings.Contains(out, "2026-05-04 12:34:56Z") {
		t.Errorf("expected formatted last seen, got:\n%s", out)
	}
	// deploy has no local config, so Local must say "none".
	deployLine := findLine(t, out, "deploy")
	if !strings.Contains(deployLine, "none") {
		t.Errorf("expected deploy row to show local=none, got: %q", deployLine)
	}
	// build was probed exactly once.
	if len(fake.probed) != 1 || fake.probed[0] != "build" {
		t.Errorf("supervisor probes: got %v, want [build]", fake.probed)
	}
}

func TestWorkerStatusRejectsPersonalContext(t *testing.T) {
	var hits int32
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&hits, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	resetWorkerState(t, &config.Config{
		AuthToken:      "tok",
		ServerURL:      httpToWS(stub.URL) + "/ws",
		CurrentContext: "personal",
	})
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "status")
	if err == nil {
		t.Fatalf("expected error for personal context, got nil")
	}
	if !strings.Contains(err.Error(), "team context") {
		t.Errorf("expected message about team context, got: %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("server should not be called for personal context, hits=%d", hits)
	}
}

func TestWorkerStatusSingleNameDetail(t *testing.T) {
	stub := stubWorkersServer(`{"workers":[
		{"id":"wkr_1","name":"build","team_id":"team-alpha","status":"online","last_seen_at":"2026-05-04T12:34:56Z"}
	]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	seedLocalWorker(t, worker.Config{
		WorkerID:       "wkr_1",
		Name:           "build",
		Context:        "team:team-alpha",
		ServiceBackend: "launchd",
	})

	fake := newFakeSupervisor()
	fake.results["build"] = worker.ProbeResult{ServiceBackend: "launchd", ManagedByUs: true, Running: true}
	useFakeSupervisor(t, fake)

	out, err := runCmd(t, "worker", "status", "build")
	if err != nil {
		t.Fatalf("status build: %v", err)
	}
	for _, want := range []string{"Worker:", "build", "Server:", "online", "Local:", "launchd:running", "Last Seen:"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail output missing %q:\n%s", want, out)
		}
	}
}

func TestWorkerStatusSingleNameNotFound(t *testing.T) {
	stub := stubWorkersServer(`{"workers":[]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "status", "ghost")
	if err == nil {
		t.Fatalf("expected not-found error, got nil")
	}
	if !strings.Contains(err.Error(), `"ghost"`) || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected helpful not-found message, got: %v", err)
	}
}

func TestWorkerStatusLocalOnlyShowsMissing(t *testing.T) {
	stub := stubWorkersServer(`{"workers":[]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	seedLocalWorker(t, worker.Config{
		Name:           "ghost",
		Context:        "team:team-alpha",
		ServiceBackend: "launchd",
	})
	fake := newFakeSupervisor()
	fake.results["ghost"] = worker.ProbeResult{ServiceBackend: "launchd", ManagedByUs: true, Running: false, Detail: "stopped"}
	useFakeSupervisor(t, fake)

	out, err := runCmd(t, "worker", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	ghostLine := findLine(t, out, "ghost")
	if !strings.Contains(ghostLine, "<missing>") {
		t.Errorf("expected <missing> server status for local-only worker, got: %q", ghostLine)
	}
	if !strings.Contains(ghostLine, "launchd:stopped") {
		t.Errorf("expected launchd:stopped for local-only worker, got: %q", ghostLine)
	}
}

func TestWorkerStatusServerOnlyShowsLocalNone(t *testing.T) {
	stub := stubWorkersServer(`{"workers":[
		{"id":"wkr_1","name":"build","team_id":"team-alpha","status":"online"}
	]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	fake := newFakeSupervisor()
	useFakeSupervisor(t, fake)

	out, err := runCmd(t, "worker", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	buildLine := findLine(t, out, "build")
	if !strings.Contains(buildLine, "none") {
		t.Errorf("expected local=none for server-only worker, got: %q", buildLine)
	}
	if len(fake.probed) != 0 {
		t.Errorf("server-only worker should not be probed, got probes: %v", fake.probed)
	}
}

func TestWorkerStatusStubProbeRendersWithoutCrash(t *testing.T) {
	stub := stubWorkersServer(`{"workers":[
		{"id":"wkr_1","name":"build","team_id":"team-alpha","status":"online"}
	]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	seedLocalWorker(t, worker.Config{
		WorkerID:       "wkr_1",
		Name:           "build",
		Context:        "team:team-alpha",
		ServiceBackend: "launchd",
	})
	// Default fakeSupervisor returns a "probe not yet implemented"
	// stub for any unknown name — exactly the production stub behavior.
	useFakeSupervisor(t, newFakeSupervisor())

	out, err := runCmd(t, "worker", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "probe not yet implemented") {
		t.Errorf("expected stub probe text, got:\n%s", out)
	}
}

func TestWorkerStatusServer5xxErrors(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		writer.Write([]byte(`{"error":"boom"}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "status")
	if err == nil {
		t.Fatalf("expected error from 500 response")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected 500/boom in error, got: %v", err)
	}
}

func TestWorkerStatusOtherContextLocalIgnored(t *testing.T) {
	// Local config bound to a DIFFERENT context must not leak into
	// this team's status output.
	stub := stubWorkersServer(`{"workers":[]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	seedLocalWorker(t, worker.Config{
		Name:           "elsewhere",
		Context:        "team:other-team",
		ServiceBackend: "launchd",
	})
	useFakeSupervisor(t, newFakeSupervisor())

	out, err := runCmd(t, "worker", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(out, "elsewhere") {
		t.Errorf("local config from other context leaked into status: %s", out)
	}
}

func TestWorkerStatusDuplicateNamesPreservesAllRows(t *testing.T) {
	stub := stubWorkersServer(`{"workers":[
		{"id":"wkr_1","name":"build","team_id":"team-alpha","status":"online","last_seen_at":"2026-05-04T12:34:56Z"},
		{"id":"wkr_2","name":"build","team_id":"team-alpha","status":"offline","last_seen_at":"2026-05-04T13:00:00Z"}
	]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	useFakeSupervisor(t, newFakeSupervisor())

	out, err := runCmd(t, "worker", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	// Both rows must be visible with their own status data.
	if !strings.Contains(out, "online") {
		t.Errorf("expected first duplicate's status (online) in output:\n%s", out)
	}
	if !strings.Contains(out, "offline") {
		t.Errorf("expected second duplicate's status (offline) in output:\n%s", out)
	}
	// Both rows carry a DUP marker so the operator sees the collision.
	if strings.Count(out, "DUP-") < 2 {
		t.Errorf("expected DUP markers on both rows, got:\n%s", out)
	}
	if !strings.Contains(out, "DUP-1/2") || !strings.Contains(out, "DUP-2/2") {
		t.Errorf("expected DUP-1/2 and DUP-2/2 markers, got:\n%s", out)
	}
	// Both timestamps preserved.
	if !strings.Contains(out, "2026-05-04 12:34:56Z") || !strings.Contains(out, "2026-05-04 13:00:00Z") {
		t.Errorf("expected both last-seen timestamps preserved, got:\n%s", out)
	}
}

func TestWorkerStatusServerTimeout(t *testing.T) {
	// Block the response indefinitely until the test ends. The handler
	// uses request context to unblock cleanly when the client cancels.
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer stub.Close()

	prev := httpTimeout
	httpTimeout = 100 * time.Millisecond
	t.Cleanup(func() { httpTimeout = prev })

	resetWorkerState(t, teamCfg(stub.URL))
	useFakeSupervisor(t, newFakeSupervisor())

	_, err := runCmd(t, "worker", "status")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "deadline exceeded") && !strings.Contains(msg, "Client.Timeout") && !strings.Contains(msg, "context deadline") {
		t.Errorf("expected timeout-flavored error, got: %v", err)
	}
}

func TestWorkerStatusLocalOnlyDistinguishedFromMissing(t *testing.T) {
	// Two local workers, no server entries. One has ServiceBackend=""
	// (foreground / local-only), the other has a real backend (server
	// entry has gone missing).
	stub := stubWorkersServer(`{"workers":[]}`)
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	seedLocalWorker(t, worker.Config{
		Name:           "foreground",
		Context:        "team:team-alpha",
		ServiceBackend: "",
	})
	seedLocalWorker(t, worker.Config{
		Name:           "orphan",
		Context:        "team:team-alpha",
		ServiceBackend: "launchd",
	})
	fake := newFakeSupervisor()
	fake.results["orphan"] = worker.ProbeResult{ServiceBackend: "launchd", ManagedByUs: true, Running: false}
	useFakeSupervisor(t, fake)

	out, err := runCmd(t, "worker", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	foregroundLine := findLine(t, out, "foreground")
	if !strings.Contains(foregroundLine, "<local-only>") {
		t.Errorf("expected <local-only> for foreground worker, got: %q", foregroundLine)
	}
	orphanLine := findLine(t, out, "orphan")
	if !strings.Contains(orphanLine, "<missing>") {
		t.Errorf("expected <missing> for orphan worker, got: %q", orphanLine)
	}
}

func TestWorkerStatusSendsAuthHeader(t *testing.T) {
	var sawAuth atomic.Value
	stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sawAuth.Store(request.Header.Get("Authorization"))
		if !strings.HasSuffix(request.URL.Path, "/workers") {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"workers":[]}`))
	}))
	defer stub.Close()

	resetWorkerState(t, teamCfg(stub.URL))
	useFakeSupervisor(t, newFakeSupervisor())

	if _, err := runCmd(t, "worker", "status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	got, _ := sawAuth.Load().(string)
	if got != "Bearer justtunnel_test_token" {
		t.Errorf("expected Authorization=Bearer justtunnel_test_token, got %q", got)
	}
}

// findLine returns the first line in output containing substring, or
// fails the test.
func findLine(t *testing.T, output, substring string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, substring) {
			return line
		}
	}
	t.Fatalf("no line containing %q in:\n%s", substring, output)
	return ""
}
