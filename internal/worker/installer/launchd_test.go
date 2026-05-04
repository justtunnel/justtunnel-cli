package installer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner records every Run call and replays a queue of canned results.
// If a call exceeds the queue, it falls back to the default result. This
// shape lets a single test express both the call-sequence assertion and
// the "what does launchctl return for call N" stub in one place.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []runCall
	results  []runResult
	fallback runResult
}

type runCall struct {
	Name string
	Args []string
}

type runResult struct {
	Output []byte
	Err    error
}

func (fake *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, runCall{Name: name, Args: append([]string{}, args...)})
	if len(fake.results) > 0 {
		head := fake.results[0]
		fake.results = fake.results[1:]
		return head.Output, head.Err
	}
	return fake.fallback.Output, fake.fallback.Err
}

func (fake *fakeRunner) Calls() []runCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	out := make([]runCall, len(fake.calls))
	copy(out, fake.calls)
	return out
}

// newTestInstaller wires a fake runner + pinned uid/exe/home onto a fresh
// temp dir and returns the installer plus the fake.
func newTestInstaller(t *testing.T) (*LaunchdInstaller, *fakeRunner, string) {
	t.Helper()
	homeDir := t.TempDir()
	fake := &fakeRunner{}
	installer := &LaunchdInstaller{
		Runner:     fake,
		Geteuid:    func() int { return 501 },
		Executable: func() (string, error) { return "/usr/local/bin/justtunnel", nil },
		HomeDir:    func() (string, error) { return homeDir, nil },
	}
	return installer, fake, homeDir
}

func TestRenderPlist_Golden(t *testing.T) {
	installer, _, _ := newTestInstaller(t)
	got, err := installer.RenderPlist("alpha", "/usr/local/bin/justtunnel", "/Users/me/.justtunnel/logs/worker-alpha.log")
	if err != nil {
		t.Fatalf("RenderPlist: %v", err)
	}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>dev.justtunnel.worker.alpha</string>
  <key>ProgramArguments</key> <array>
    <string>/usr/local/bin/justtunnel</string>
    <string>worker</string>
    <string>start</string>
    <string>alpha</string>
  </array>
  <key>KeepAlive</key>        <true/>
  <key>RunAtLoad</key>        <true/>
  <key>ThrottleInterval</key> <integer>60</integer>
  <key>StandardOutPath</key>  <string>/Users/me/.justtunnel/logs/worker-alpha.log</string>
  <key>StandardErrorPath</key><string>/Users/me/.justtunnel/logs/worker-alpha.log</string>
</dict>
</plist>
`
	if string(got) != want {
		t.Fatalf("plist mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderPlist_RejectsBadInputs(t *testing.T) {
	installer, _, _ := newTestInstaller(t)
	cases := []struct {
		label                              string
		name, binaryPath, logPath, wantSub string
	}{
		{"bad name", "Bad_Name", "/bin/x", "/tmp/x.log", "invalid name"},
		{"empty name", "", "/bin/x", "/tmp/x.log", "invalid name"},
		{"xml in binary path", "ok", "/bin/x<evil>", "/tmp/x.log", "binary path"},
		{"xml in log path", "ok", "/bin/x", "/tmp/x&y.log", "log path"},
		{"empty binary path", "ok", "", "/tmp/x.log", "empty binary path"},
		// C2: parity with validateUnitPath — newline / CR / NUL must be rejected.
		{"newline in binary path", "ok", "/bin/x\nevil", "/tmp/x.log", "newline or NUL"},
		{"cr in log path", "ok", "/bin/x", "/tmp/x\rinjected", "newline or NUL"},
		{"nul in binary path", "ok", "/bin/x\x00", "/tmp/x.log", "newline or NUL"},
	}
	for _, testCase := range cases {
		t.Run(testCase.label, func(subTest *testing.T) {
			_, err := installer.RenderPlist(testCase.name, testCase.binaryPath, testCase.logPath)
			if err == nil {
				subTest.Fatalf("want error containing %q, got nil", testCase.wantSub)
			}
			if !strings.Contains(err.Error(), testCase.wantSub) {
				subTest.Fatalf("error %q does not contain %q", err, testCase.wantSub)
			}
		})
	}
}

func TestPlistPath(t *testing.T) {
	installer, _, homeDir := newTestInstaller(t)
	got, err := installer.PlistPath("alpha")
	if err != nil {
		t.Fatalf("PlistPath: %v", err)
	}
	want := filepath.Join(homeDir, "Library", "LaunchAgents", "dev.justtunnel.worker.alpha.plist")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// withWorkerHome points worker.home() at a temp dir for the duration of the
// test so worker.Read / worker.LogFilePath don't touch the real ~/.justtunnel.
func withWorkerHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", dir)
	return dir
}

// seedWorkerConfig writes a minimal worker config file so worker.Read
// succeeds. We don't import the worker package's Write here to keep this
// test from getting tangled in worker's own validation contract; a raw
// JSON file matching the schema is enough.
func seedWorkerConfig(t *testing.T, name string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("JUSTTUNNEL_HOME"), "workers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Build the JSON via encoding/json rather than string concatenation so
	// any future schema field added to worker.Config gets a typed touchpoint
	// here (and so a name containing JSON metacharacters can never inject).
	cfg := struct {
		WorkerID       string `json:"worker_id"`
		Name           string `json:"name"`
		Context        string `json:"context"`
		Subdomain      string `json:"subdomain"`
		CreatedAt      string `json:"created_at"`
		ServiceBackend string `json:"service_backend"`
	}{
		WorkerID:       "w_abc",
		Name:           name,
		Context:        "personal",
		Subdomain:      "foo",
		CreatedAt:      "2026-01-01T00:00:00Z",
		ServiceBackend: "launchd",
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(filepath.Join(dir, name+".json"), payload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestBootstrap_HappyPath(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, homeDir := newTestInstaller(t)

	if err := installer.Bootstrap(context.Background(), "alpha"); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", "dev.justtunnel.worker.alpha.plist")
	plistBytes, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	if !strings.Contains(string(plistBytes), "<string>/usr/local/bin/justtunnel</string>") {
		t.Fatalf("plist missing binary path; content: %s", plistBytes)
	}
	info, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("stat plist: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Fatalf("plist mode = %v, want 0644", mode)
	}

	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("want 2 launchctl calls, got %d: %+v", len(calls), calls)
	}
	wantBootstrap := []string{"bootstrap", "gui/501", plistPath}
	if calls[0].Name != "launchctl" || !equalArgs(calls[0].Args, wantBootstrap) {
		t.Fatalf("call[0] = %+v, want launchctl %v", calls[0], wantBootstrap)
	}
	wantEnable := []string{"enable", "gui/501/dev.justtunnel.worker.alpha"}
	if calls[1].Name != "launchctl" || !equalArgs(calls[1].Args, wantEnable) {
		t.Fatalf("call[1] = %+v, want launchctl %v", calls[1], wantEnable)
	}
}

func TestBootstrap_AlreadyLoaded_Retries(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, _ := newTestInstaller(t)

	// First bootstrap fails with "already loaded"; bootout succeeds;
	// second bootstrap succeeds; enable succeeds.
	fake.results = []runResult{
		{Output: []byte("Bootstrap failed: 37: Service already loaded\n"), Err: &fakeExitError{Code: 37}},
		{Output: nil, Err: nil}, // bootout
		{Output: nil, Err: nil}, // bootstrap retry
		{Output: nil, Err: nil}, // enable
	}

	if err := installer.Bootstrap(context.Background(), "alpha"); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 4 {
		t.Fatalf("want 4 calls, got %d: %+v", len(calls), calls)
	}
	if got := calls[0].Args[0]; got != "bootstrap" {
		t.Fatalf("call[0] arg0 = %q, want bootstrap", got)
	}
	if got := calls[1].Args[0]; got != "bootout" {
		t.Fatalf("call[1] arg0 = %q, want bootout", got)
	}
	if got := calls[1].Args[1]; got != "gui/501/dev.justtunnel.worker.alpha" {
		t.Fatalf("call[1] arg1 = %q", got)
	}
	if got := calls[2].Args[0]; got != "bootstrap" {
		t.Fatalf("call[2] arg0 = %q, want bootstrap (retry)", got)
	}
	if got := calls[3].Args[0]; got != "enable" {
		t.Fatalf("call[3] arg0 = %q, want enable", got)
	}
}

func TestBootstrap_RequiresWorkerConfig(t *testing.T) {
	withWorkerHome(t)
	// Deliberately do NOT seed a config.
	installer, fake, _ := newTestInstaller(t)

	err := installer.Bootstrap(context.Background(), "alpha")
	if err == nil {
		t.Fatal("want error for missing worker config, got nil")
	}
	if !strings.Contains(err.Error(), "worker create") {
		t.Fatalf("error %q should hint at `worker create`", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("launchctl should not be called when config missing; got %+v", calls)
	}
}

func TestUnbootstrap_HappyPath(t *testing.T) {
	withWorkerHome(t)
	installer, fake, homeDir := newTestInstaller(t)
	// Pre-create the plist so we can verify removal.
	plistDir := filepath.Join(homeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	plistPath := filepath.Join(plistDir, "dev.justtunnel.worker.alpha.plist")
	if err := os.WriteFile(plistPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := installer.Unbootstrap(context.Background(), "alpha"); err != nil {
		t.Fatalf("Unbootstrap: %v", err)
	}
	if _, err := os.Stat(plistPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist should be removed; stat err = %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 launchctl call, got %d", len(calls))
	}
	if got := calls[0].Args; len(got) != 2 || got[0] != "bootout" || got[1] != "gui/501/dev.justtunnel.worker.alpha" {
		t.Fatalf("call[0].Args = %v", got)
	}
}

func TestUnbootstrap_Idempotent_NotLoaded(t *testing.T) {
	withWorkerHome(t)
	installer, fake, _ := newTestInstaller(t)
	fake.results = []runResult{
		{Output: []byte("Boot-out failed: 113: Could not find specified service\n"), Err: &fakeExitError{Code: 113}},
	}
	if err := installer.Unbootstrap(context.Background(), "alpha"); err != nil {
		t.Fatalf("Unbootstrap should swallow not-loaded error, got %v", err)
	}
}

func TestUnbootstrap_PropagatesUnexpectedError(t *testing.T) {
	withWorkerHome(t)
	installer, fake, _ := newTestInstaller(t)
	fake.results = []runResult{
		{Output: []byte("permission denied\n"), Err: &fakeExitError{Code: 1}},
	}
	err := installer.Unbootstrap(context.Background(), "alpha")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "bootout") {
		t.Fatalf("error %q should mention bootout", err)
	}
}

// TestBootstrap_ContextCancellation verifies the installer threads ctx
// through to the runner. We spin a runner that blocks until ctx is done,
// then cancel.
func TestBootstrap_ContextCancellation(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	homeDir := t.TempDir()
	blockingRunner := &ctxAwareRunner{}
	installer := &LaunchdInstaller{
		Runner:     blockingRunner,
		Geteuid:    func() int { return 501 },
		Executable: func() (string, error) { return "/usr/local/bin/justtunnel", nil },
		HomeDir:    func() (string, error) { return homeDir, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- installer.Bootstrap(ctx, "alpha") }()
	// Give the goroutine a chance to enter Run.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Bootstrap did not return after cancel")
	}
}

type ctxAwareRunner struct{}

func (ctxAwareRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// fakeExitError is a stand-in for *exec.ExitError. We can't construct a
// real *exec.ExitError outside of os/exec, so the production code falls
// back to a duck-typed `interface{ ExitCode() int }` check that this fake
// satisfies. The launchctlError records both the exit code and the textual
// output, and the classifiers (isAlreadyLoaded / isNotLoaded) consult both.
type fakeExitError struct{ Code int }

func (fakeExitError) Error() string         { return "exit status" }
func (exit fakeExitError) ExitCode() int    { return exit.Code }

func equalArgs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestIsAlreadyLoaded_Variants(t *testing.T) {
	cases := []struct {
		output string
		want   bool
	}{
		{"Bootstrap failed: 37: Service already loaded", true},
		{"already loaded", true},
		{"already bootstrapped", true},
		{"Bootstrap succeeded", false},
		{"Could not find specified service", false},
	}
	for _, testCase := range cases {
		err := &launchctlError{Args: []string{"bootstrap"}, Output: testCase.output, Err: errors.New("x")}
		if got := isAlreadyLoaded(err); got != testCase.want {
			t.Fatalf("isAlreadyLoaded(%q) = %v, want %v", testCase.output, got, testCase.want)
		}
	}
	if isAlreadyLoaded(errors.New("not a launchctl error")) {
		t.Fatal("non-launchctl error should not match")
	}
}

// TestIsAlreadyLoaded_ExitCodeOnly proves the classifier matches purely on
// exit code 37, with empty output text. This is the path that mattered to
// the BLOCKER fix: previous code only inspected the textual output, so a
// macOS release/wrapper that emitted a different message but kept the exit
// code would have caused Bootstrap to surface a real error instead of
// retrying via bootout.
func TestIsAlreadyLoaded_ExitCodeOnly(t *testing.T) {
	err := &launchctlError{
		Args:     []string{"bootstrap"},
		Output:   "", // no diagnostic text at all
		ExitCode: 37,
		Err:      errors.New("exit status 37"),
	}
	if !isAlreadyLoaded(err) {
		t.Fatal("isAlreadyLoaded should match exit code 37 even without text")
	}
}

// TestIsNotLoaded_ExitCodeOnly is the bootout-side mirror: empty output but
// exit code 113 must still classify as not-loaded so Unbootstrap stays
// idempotent.
func TestIsNotLoaded_ExitCodeOnly(t *testing.T) {
	err := &launchctlError{
		Args:     []string{"bootout"},
		Output:   "",
		ExitCode: 113,
		Err:      errors.New("exit status 113"),
	}
	if !isNotLoaded(err) {
		t.Fatal("isNotLoaded should match exit code 113 even without text")
	}
}

// TestRunLaunchctl_ExtractsExitCode confirms the production code threads
// the duck-typed exit code from the runner's error into launchctlError.
// Without this, the new exit-code-first classification path is unreachable.
func TestRunLaunchctl_ExtractsExitCode(t *testing.T) {
	fake := &fakeRunner{}
	fake.fallback = runResult{Output: []byte(""), Err: &fakeExitError{Code: 113}}
	installer := &LaunchdInstaller{Runner: fake}
	err := installer.runLaunchctl(context.Background(), "bootout", "gui/501/dev.justtunnel.worker.alpha")
	var launchctlErr *launchctlError
	if !errors.As(err, &launchctlErr) {
		t.Fatalf("want *launchctlError, got %T: %v", err, err)
	}
	if launchctlErr.ExitCode != 113 {
		t.Fatalf("ExitCode = %d, want 113", launchctlErr.ExitCode)
	}
}

// TestLaunchctlError_NoDoubleErrText exercises D5: the .Error() format
// must use e.Err.Error() (not %v) so wrapping does not produce a doubled
// error chain when the outer formatter expands %w.
func TestLaunchctlError_NoDoubleErrText(t *testing.T) {
	inner := errors.New("inner-fail-marker")
	wrapped := &launchctlError{
		Args:     []string{"bootstrap", "gui/501"},
		Output:   "some-output",
		ExitCode: 5,
		Err:      inner,
	}
	got := wrapped.Error()
	// `inner-fail-marker` should appear exactly once.
	count := strings.Count(got, "inner-fail-marker")
	if count != 1 {
		t.Fatalf("inner error appears %d times in %q; want exactly 1", count, got)
	}
	// Unwrap chain still functions.
	if !errors.Is(wrapped, inner) {
		t.Fatalf("Unwrap chain broken: errors.Is failed for %q", got)
	}
}

func TestIsNotLoaded_Variants(t *testing.T) {
	cases := []struct {
		output string
		want   bool
	}{
		{"Could not find specified service", true},
		{"No such process", true},
		{"not loaded", true},
		{"Service already loaded", false},
		{"Bootstrap succeeded", false},
	}
	for _, testCase := range cases {
		err := &launchctlError{Args: []string{"bootout"}, Output: testCase.output, Err: errors.New("x")}
		if got := isNotLoaded(err); got != testCase.want {
			t.Fatalf("isNotLoaded(%q) = %v, want %v", testCase.output, got, testCase.want)
		}
	}
}
