package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePrompter records every Prompt call and replays a queued response.
// answers is consumed FIFO; callsBeyondQueue uses fallback.
type fakePrompter struct {
	answers  []bool
	errs     []error
	calls    int
	fallback bool
}

func (prompter *fakePrompter) Prompt(_ context.Context) (bool, error) {
	prompter.calls++
	idx := prompter.calls - 1
	var answer bool
	var err error
	if idx < len(prompter.answers) {
		answer = prompter.answers[idx]
	} else {
		answer = prompter.fallback
	}
	if idx < len(prompter.errs) {
		err = prompter.errs[idx]
	}
	return answer, err
}

// newTestSystemdInstaller wires a fake runner + pinned exe/home/user/detector
// onto a fresh temp dir and returns the installer plus the fake.
func newTestSystemdInstaller(t *testing.T) (*SystemdInstaller, *fakeRunner, *fakePrompter, string) {
	t.Helper()
	homeDir := t.TempDir()
	fake := &fakeRunner{}
	prompter := &fakePrompter{}
	installer := &SystemdInstaller{
		Runner:          fake,
		Prompter:        prompter,
		Executable:      func() (string, error) { return "/usr/local/bin/justtunnel", nil },
		HomeDir:         func() (string, error) { return homeDir, nil },
		CurrentUser:     func() (string, error) { return "alice", nil },
		SystemdDetector: func() error { return nil },
	}
	return installer, fake, prompter, homeDir
}

func TestRenderUnit_Golden(t *testing.T) {
	installer, _, _, _ := newTestSystemdInstaller(t)
	got, err := installer.RenderUnit("alpha", "/usr/local/bin/justtunnel", "/home/alice/.justtunnel/logs/worker-alpha.log")
	if err != nil {
		t.Fatalf("RenderUnit: %v", err)
	}
	want := `[Unit]
Description=JustTunnel Worker alpha
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/justtunnel worker start alpha
Restart=on-failure
RestartSec=5
StandardOutput=append:/home/alice/.justtunnel/logs/worker-alpha.log
StandardError=append:/home/alice/.justtunnel/logs/worker-alpha.log

[Install]
WantedBy=default.target
`
	if string(got) != want {
		t.Fatalf("unit mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderUnit_RejectsBadInputs(t *testing.T) {
	installer, _, _, _ := newTestSystemdInstaller(t)
	cases := []struct {
		label                              string
		name, binaryPath, logPath, wantSub string
	}{
		{"bad name", "Bad_Name", "/bin/x", "/tmp/x.log", "invalid name"},
		{"empty name", "", "/bin/x", "/tmp/x.log", "invalid name"},
		{"newline in binary path", "ok", "/bin/x\nExecStart=evil", "/tmp/x.log", "binary path"},
		{"newline in log path", "ok", "/bin/x", "/tmp/x\ny.log", "log path"},
		{"empty binary path", "ok", "", "/tmp/x.log", "empty binary path"},
		{"NUL in binary path", "ok", "/bin/x\x00evil", "/tmp/x.log", "binary path"},
		{"percent in log path", "ok", "/bin/x", "/log/%n.log", "log path"},
		{"percent in binary path", "ok", "/bin/just%h", "/tmp/x.log", "binary path"},
	}
	for _, testCase := range cases {
		t.Run(testCase.label, func(subTest *testing.T) {
			_, err := installer.RenderUnit(testCase.name, testCase.binaryPath, testCase.logPath)
			if err == nil {
				subTest.Fatalf("want error containing %q, got nil", testCase.wantSub)
			}
			if !strings.Contains(err.Error(), testCase.wantSub) {
				subTest.Fatalf("error %q does not contain %q", err, testCase.wantSub)
			}
		})
	}
}

func TestUnitPath(t *testing.T) {
	installer, _, _, homeDir := newTestSystemdInstaller(t)
	got, err := installer.UnitPath("alpha")
	if err != nil {
		t.Fatalf("UnitPath: %v", err)
	}
	want := filepath.Join(homeDir, ".config", "systemd", "user", "justtunnel-worker-alpha.service")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestLingerConsentPromptVerbatim pins the byte-exact consent text and
// deny-path notice. Per spec §6.4 these strings MUST NOT drift; this
// test is the canonical guard.
func TestLingerConsentPromptVerbatim(t *testing.T) {
	wantPrompt := "Worker daemon needs to keep running after you log out. This requires enabling\n" +
		"user-linger via `loginctl enable-linger <user>` (a system-wide change).\n" +
		"\n" +
		"Without it, your worker will stop when you log out, and CI requests will\n" +
		"return 503 worker_offline until you log back in.\n" +
		"\n" +
		"Enable user-linger now? [Y/n] "
	if LingerConsentPrompt != wantPrompt {
		t.Fatalf("LingerConsentPrompt drifted from spec §6.4\n--- got ---\n%q\n--- want ---\n%q", LingerConsentPrompt, wantPrompt)
	}

	wantNotice := "OK: worker installed without user-linger.\n" +
		"NOTE: this worker will stop when you log out. To enable persistent operation later:\n" +
		"      loginctl enable-linger <user>\n" +
		"      systemctl --user restart justtunnel-worker-alpha\n"
	if got := LingerDeniedNotice("alpha"); got != wantNotice {
		t.Fatalf("LingerDeniedNotice drifted\n--- got ---\n%q\n--- want ---\n%q", got, wantNotice)
	}
}

func TestBootstrap_NoLinger_SkipsLingerCommands(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, prompter, homeDir := newTestSystemdInstaller(t)

	result, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: true})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if result.LingerEnabled {
		t.Fatal("LingerEnabled should be false with NoLinger=true")
	}
	if !result.ShouldPrintLingerDeniedNotice {
		t.Fatal("ShouldPrintLingerDeniedNotice should be true with NoLinger=true")
	}
	if prompter.calls != 0 {
		t.Fatalf("prompter should not be called with NoLinger=true; calls=%d", prompter.calls)
	}

	// Verify unit file was written with mode 0644.
	unitPath := filepath.Join(homeDir, ".config", "systemd", "user", "justtunnel-worker-alpha.service")
	info, statErr := os.Stat(unitPath)
	if statErr != nil {
		t.Fatalf("stat unit: %v", statErr)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Fatalf("unit mode = %v, want 0644", mode)
	}

	// Verify systemctl call sequence: daemon-reload then enable --now.
	// No loginctl calls at all (NoLinger short-circuits).
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("want 2 systemctl calls, got %d: %+v", len(calls), calls)
	}
	wantReload := []string{"--user", "daemon-reload"}
	if calls[0].Name != "systemctl" || !equalArgs(calls[0].Args, wantReload) {
		t.Fatalf("call[0] = %+v, want systemctl %v", calls[0], wantReload)
	}
	wantEnable := []string{"--user", "enable", "--now", "justtunnel-worker-alpha.service"}
	if calls[1].Name != "systemctl" || !equalArgs(calls[1].Args, wantEnable) {
		t.Fatalf("call[1] = %+v, want systemctl %v", calls[1], wantEnable)
	}
	for _, call := range calls {
		if call.Name == "loginctl" {
			t.Fatalf("loginctl should not be called with NoLinger=true; got %+v", call)
		}
	}
}

func TestBootstrap_LingerAlreadyEnabled_SkipsPrompt(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, prompter, _ := newTestSystemdInstaller(t)
	// Sequence: daemon-reload, enable --now, loginctl show-user (returns Linger=yes).
	fake.results = []runResult{
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: []byte("Linger=yes\n"), Err: nil},
	}

	result, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: false})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !result.LingerEnabled {
		t.Fatal("LingerEnabled should be true when linger was already on")
	}
	if result.ShouldPrintLingerDeniedNotice {
		t.Fatal("ShouldPrintLingerDeniedNotice should be false when linger was already on")
	}
	if prompter.calls != 0 {
		t.Fatalf("prompter should not be called when linger already on; calls=%d", prompter.calls)
	}
	calls := fake.Calls()
	// daemon-reload, enable, show-user — no enable-linger.
	if len(calls) != 3 {
		t.Fatalf("want 3 calls, got %d: %+v", len(calls), calls)
	}
	if calls[2].Name != "loginctl" {
		t.Fatalf("call[2] should probe loginctl, got %+v", calls[2])
	}
	for _, call := range calls {
		if call.Name == "loginctl" && len(call.Args) > 0 && call.Args[0] == "enable-linger" {
			t.Fatalf("enable-linger should not be called when already on; got %+v", call)
		}
	}
}

func TestBootstrap_LingerNotEnabled_AcceptsPrompt(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, prompter, _ := newTestSystemdInstaller(t)
	prompter.answers = []bool{true}
	// Sequence: daemon-reload, enable --now, loginctl show-user (Linger=no), loginctl enable-linger.
	fake.results = []runResult{
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: []byte("Linger=no\n"), Err: nil},
		{Output: nil, Err: nil},
	}

	result, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: false})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !result.LingerEnabled {
		t.Fatal("LingerEnabled should be true after consent")
	}
	if result.ShouldPrintLingerDeniedNotice {
		t.Fatal("ShouldPrintLingerDeniedNotice should be false after consent")
	}
	if prompter.calls != 1 {
		t.Fatalf("prompter should be called once; calls=%d", prompter.calls)
	}
	calls := fake.Calls()
	if len(calls) != 4 {
		t.Fatalf("want 4 calls, got %d: %+v", len(calls), calls)
	}
	wantEnableLinger := []string{"enable-linger", "alice"}
	if calls[3].Name != "loginctl" || !equalArgs(calls[3].Args, wantEnableLinger) {
		t.Fatalf("call[3] = %+v, want loginctl %v", calls[3], wantEnableLinger)
	}
}

func TestBootstrap_LingerNotEnabled_DeniesPrompt(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, prompter, _ := newTestSystemdInstaller(t)
	prompter.answers = []bool{false}
	fake.results = []runResult{
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: []byte("Linger=no\n"), Err: nil},
	}

	result, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: false})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if result.LingerEnabled {
		t.Fatal("LingerEnabled should be false after deny")
	}
	if !result.ShouldPrintLingerDeniedNotice {
		t.Fatal("ShouldPrintLingerDeniedNotice should be true after deny")
	}
	if prompter.calls != 1 {
		t.Fatalf("prompter should be called once; calls=%d", prompter.calls)
	}
	calls := fake.Calls()
	if len(calls) != 3 {
		t.Fatalf("want 3 calls (no enable-linger), got %d: %+v", len(calls), calls)
	}
	for _, call := range calls {
		if call.Name == "loginctl" && len(call.Args) > 0 && call.Args[0] == "enable-linger" {
			t.Fatalf("enable-linger must not be called after deny; got %+v", call)
		}
	}
}

func TestBootstrap_EnableLingerFails_InstallStillSucceeds(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, prompter, _ := newTestSystemdInstaller(t)
	prompter.answers = []bool{true}
	fake.results = []runResult{
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: []byte("Linger=no\n"), Err: nil},
		{Output: []byte("Failed to enable linger: permission denied\n"), Err: &fakeExitError{Code: 1}},
	}

	result, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: false})
	if err == nil {
		t.Fatal("want error from enable-linger failure")
	}
	if !strings.Contains(err.Error(), "loginctl") {
		t.Fatalf("error %q should mention loginctl", err)
	}
	// D3: enable-linger failures wrap ErrLingerOnly so the cmd layer
	// can downgrade them to a warning rather than a hard error.
	if !errors.Is(err, ErrLingerOnly) {
		t.Fatalf("error %q should wrap ErrLingerOnly", err)
	}
	if result.LingerEnabled {
		t.Fatal("LingerEnabled should be false on enable-linger failure")
	}
	if !result.ShouldPrintLingerDeniedNotice {
		t.Fatal("ShouldPrintLingerDeniedNotice should be true on enable-linger failure")
	}
}

func TestSystemdBootstrap_RequiresWorkerConfig(t *testing.T) {
	withWorkerHome(t)
	installer, fake, _, _ := newTestSystemdInstaller(t)
	_, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: true})
	if err == nil {
		t.Fatal("want error for missing worker config, got nil")
	}
	if !strings.Contains(err.Error(), "worker create") {
		t.Fatalf("error %q should hint at `worker create`", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("systemctl should not be called when config missing; got %+v", calls)
	}
}

func TestBootstrap_SystemdNotDetected(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, _, _ := newTestSystemdInstaller(t)
	installer.SystemdDetector = func() error { return errors.New("installer: systemd not detected") }

	_, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: true})
	if err == nil {
		t.Fatal("want error when systemd not detected")
	}
	if !strings.Contains(err.Error(), "systemd not detected") {
		t.Fatalf("error %q should mention 'systemd not detected'", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("systemctl should not be called when systemd missing; got %+v", calls)
	}
}

func TestBootstrap_Idempotent(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, _, _, _ := newTestSystemdInstaller(t)

	if _, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: true}); err != nil {
		t.Fatalf("Bootstrap 1: %v", err)
	}
	if _, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: true}); err != nil {
		t.Fatalf("Bootstrap 2 (idempotent): %v", err)
	}
}

func TestSystemdUnbootstrap_HappyPath(t *testing.T) {
	withWorkerHome(t)
	installer, fake, _, homeDir := newTestSystemdInstaller(t)
	// Pre-create the unit file so we can verify removal.
	unitDir := filepath.Join(homeDir, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	unitPath := filepath.Join(unitDir, "justtunnel-worker-alpha.service")
	if err := os.WriteFile(unitPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := installer.Unbootstrap(context.Background(), "alpha"); err != nil {
		t.Fatalf("Unbootstrap: %v", err)
	}
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit should be removed; stat err = %v", err)
	}
	calls := fake.Calls()
	// disable --now, daemon-reload (best-effort).
	if len(calls) != 2 {
		t.Fatalf("want 2 systemctl calls, got %d: %+v", len(calls), calls)
	}
	wantDisable := []string{"--user", "disable", "--now", "justtunnel-worker-alpha.service"}
	if calls[0].Name != "systemctl" || !equalArgs(calls[0].Args, wantDisable) {
		t.Fatalf("call[0] = %+v, want systemctl %v", calls[0], wantDisable)
	}
}

func TestSystemdUnbootstrap_Idempotent_NotLoaded(t *testing.T) {
	withWorkerHome(t)
	installer, fake, _, _ := newTestSystemdInstaller(t)
	fake.results = []runResult{
		{Output: []byte("Failed to disable unit: Unit file justtunnel-worker-alpha.service does not exist.\n"), Err: &fakeExitError{Code: 5}},
	}
	if err := installer.Unbootstrap(context.Background(), "alpha"); err != nil {
		t.Fatalf("Unbootstrap should swallow not-loaded error, got %v", err)
	}
}

func TestSystemdUnbootstrap_PropagatesUnexpectedError(t *testing.T) {
	withWorkerHome(t)
	installer, fake, _, _ := newTestSystemdInstaller(t)
	fake.results = []runResult{
		{Output: []byte("permission denied\n"), Err: &fakeExitError{Code: 1}},
	}
	err := installer.Unbootstrap(context.Background(), "alpha")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "disable") {
		t.Fatalf("error %q should mention disable", err)
	}
}

func TestStdLingerPrompter_AcceptsEnter(t *testing.T) {
	out := &bytes.Buffer{}
	prompter := &StdLingerPrompter{In: strings.NewReader("\n"), Out: out}
	got, err := prompter.Prompt(context.Background())
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !got {
		t.Fatal("Enter (empty input) should accept")
	}
	if out.String() != LingerConsentPrompt {
		t.Fatalf("prompt text drifted; got %q", out.String())
	}
}

func TestStdLingerPrompter_AcceptsY(t *testing.T) {
	for _, answer := range []string{"y", "Y", "yes", "YES", "Yes\n"} {
		out := &bytes.Buffer{}
		prompter := &StdLingerPrompter{In: strings.NewReader(answer), Out: out}
		got, err := prompter.Prompt(context.Background())
		if err != nil {
			t.Fatalf("Prompt(%q): %v", answer, err)
		}
		if !got {
			t.Fatalf("Prompt(%q) = false, want true", answer)
		}
	}
}

func TestStdLingerPrompter_DeniesN(t *testing.T) {
	for _, answer := range []string{"n", "N", "no", "NO", "wat\n", "0\n"} {
		out := &bytes.Buffer{}
		prompter := &StdLingerPrompter{In: strings.NewReader(answer), Out: out}
		got, err := prompter.Prompt(context.Background())
		if err != nil {
			t.Fatalf("Prompt(%q): %v", answer, err)
		}
		if got {
			t.Fatalf("Prompt(%q) = true, want false", answer)
		}
	}
}

func TestStdLingerPrompter_EOFDenies(t *testing.T) {
	prompter := &StdLingerPrompter{In: strings.NewReader(""), Out: io.Discard}
	got, err := prompter.Prompt(context.Background())
	if err != nil {
		t.Fatalf("EOF should not surface as error, got %v", err)
	}
	if got {
		t.Fatal("EOF should deny")
	}
}

func TestIsUnitNotLoaded_Variants(t *testing.T) {
	cases := []struct {
		output string
		want   bool
	}{
		{"Unit file justtunnel-worker-alpha.service does not exist.", true},
		{"Unit not loaded.", true},
		{"No such file or directory", true},
		{"Failed to disable unit: No such unit", true},
		{"some other failure", false},
	}
	for _, testCase := range cases {
		err := &systemctlError{Bin: "systemctl", Args: []string{"disable"}, Output: testCase.output, Err: errors.New("x")}
		if got := isUnitNotLoaded(err); got != testCase.want {
			t.Fatalf("isUnitNotLoaded(%q) = %v, want %v", testCase.output, got, testCase.want)
		}
	}
	if isUnitNotLoaded(errors.New("not a systemctl error")) {
		t.Fatal("non-systemctl error should not match")
	}
}

func TestIsUnitNotLoaded_ExitCodeOnly(t *testing.T) {
	err := &systemctlError{
		Bin:      "systemctl",
		Args:     []string{"disable"},
		Output:   "",
		ExitCode: 5,
		Err:      errors.New("exit status 5"),
	}
	if !isUnitNotLoaded(err) {
		t.Fatal("isUnitNotLoaded should match exit code 5 even without text")
	}
}

// TestStdLingerPrompter_WhitespaceOnlyAccepts pins the documented behavior
// that whitespace-only input is treated like Enter (accept). This follows
// from TrimSpace: `"   \n"` reduces to `""`, which the `[Y/n]` convention
// maps to "yes". The test exists so a future "tighten input handling"
// patch can't quietly flip this without updating the prompter godoc.
func TestStdLingerPrompter_WhitespaceOnlyAccepts(t *testing.T) {
	out := &bytes.Buffer{}
	prompter := &StdLingerPrompter{In: strings.NewReader("   \n"), Out: out}
	got, err := prompter.Prompt(context.Background())
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !got {
		t.Fatal("whitespace-only input should accept (TrimSpace -> empty -> Enter)")
	}
}

// TestValidateUnitPath_RejectsPercent guards against systemd specifier
// expansion smuggling values into the unit file. `%n` would expand to the
// unit name; `%h` to the user home; etc.
func TestValidateUnitPath_RejectsPercent(t *testing.T) {
	if err := validateUnitPath("log path", "/log/%n.log"); err == nil {
		t.Fatal("want error for path containing %, got nil")
	} else if !strings.Contains(err.Error(), "%") {
		t.Fatalf("error %q should mention %%", err)
	}
}

// TestBootstrap_DaemonReloadFails_RemovesUnitFile verifies the
// compensating-action path: a successful unit-file write followed by a
// daemon-reload failure must NOT leave the unit file behind on disk.
func TestBootstrap_DaemonReloadFails_RemovesUnitFile(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, _, homeDir := newTestSystemdInstaller(t)
	// First systemctl call (daemon-reload) fails. We never reach
	// enable --now, so only one queued result is needed.
	fake.results = []runResult{
		{Output: []byte("Failed to connect to bus\n"), Err: &fakeExitError{Code: 1}},
	}

	_, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: true})
	if err == nil {
		t.Fatal("want error from daemon-reload failure")
	}
	if !strings.Contains(err.Error(), "daemon-reload") {
		t.Fatalf("error %q should mention daemon-reload", err)
	}
	if !strings.Contains(err.Error(), "unit file removed") {
		t.Fatalf("error %q should report unit file cleanup outcome", err)
	}
	unitPath := filepath.Join(homeDir, ".config", "systemd", "user", "justtunnel-worker-alpha.service")
	if _, statErr := os.Stat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit file should be removed after daemon-reload failure; stat err = %v", statErr)
	}
}

// TestBootstrap_LingerProbeError_ContinuesToPrompt asserts the warning
// path: a non-benign loginctl failure (e.g. dbus error) must NOT abort
// the install; it must log a warning and fall through to the prompt.
func TestBootstrap_LingerProbeError_ContinuesToPrompt(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, prompter, _ := newTestSystemdInstaller(t)
	warn := &bytes.Buffer{}
	installer.WarnOut = warn
	prompter.answers = []bool{false} // user denies at the prompt

	// daemon-reload OK, enable --now OK, loginctl show-user fails with
	// a NON-"unknown user" error (simulating dbus failure), then no
	// enable-linger because user denies.
	fake.results = []runResult{
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: []byte("Failed to connect to bus: No such file or directory\n"), Err: &fakeExitError{Code: 1}},
	}

	result, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: false})
	if err != nil {
		t.Fatalf("Bootstrap should not fail when linger probe errors; got %v", err)
	}
	if result.LingerEnabled {
		t.Fatal("LingerEnabled should be false when user denies after probe warning")
	}
	if prompter.calls != 1 {
		t.Fatalf("prompter should be invoked exactly once; calls=%d", prompter.calls)
	}
	if !strings.Contains(warn.String(), "could not query linger status") {
		t.Fatalf("warning text not emitted; warn=%q", warn.String())
	}
}

// TestBootstrap_LingerProbe_UnknownUserIsBenign confirms the existing
// behavior survives the new error policy: loginctl exit 1 with
// "unknown user" output is still treated as "linger=no, prompt the user".
// No warning should be printed in this case.
func TestBootstrap_LingerProbe_UnknownUserIsBenign(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, prompter, _ := newTestSystemdInstaller(t)
	warn := &bytes.Buffer{}
	installer.WarnOut = warn
	prompter.answers = []bool{false}

	fake.results = []runResult{
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: []byte("Failed to look up user alice: Unknown user\n"), Err: &fakeExitError{Code: 1}},
	}

	_, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: false})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if prompter.calls != 1 {
		t.Fatalf("prompter should be called once for unknown-user benign path; calls=%d", prompter.calls)
	}
	if warn.Len() != 0 {
		t.Fatalf("no warning should be printed for benign unknown-user; got %q", warn.String())
	}
}

// TestBootstrap_Idempotent_LingerAlreadyEnabledOnRerun asserts that on a
// second Bootstrap with NoLinger=false, when loginctl now reports
// Linger=yes (because the first run enabled it), the prompter is NOT
// re-invoked. This is the canonical re-run scenario.
func TestBootstrap_Idempotent_LingerAlreadyEnabledOnRerun(t *testing.T) {
	withWorkerHome(t)
	seedWorkerConfig(t, "alpha")
	installer, fake, prompter, _ := newTestSystemdInstaller(t)
	prompter.answers = []bool{true} // first run accepts; second run shouldn't ask

	// Run 1: daemon-reload, enable --now, show-user (Linger=no),
	// enable-linger.
	// Run 2: daemon-reload, enable --now, show-user (Linger=yes) — done.
	fake.results = []runResult{
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: []byte("Linger=no\n"), Err: nil},
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: nil, Err: nil},
		{Output: []byte("Linger=yes\n"), Err: nil},
	}

	if _, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: false}); err != nil {
		t.Fatalf("Bootstrap 1: %v", err)
	}
	if prompter.calls != 1 {
		t.Fatalf("after run 1, prompter should have been called once; calls=%d", prompter.calls)
	}

	result, err := installer.Bootstrap(context.Background(), "alpha", SystemdOptions{NoLinger: false})
	if err != nil {
		t.Fatalf("Bootstrap 2: %v", err)
	}
	if !result.LingerEnabled {
		t.Fatal("run 2: LingerEnabled should be true (already on)")
	}
	if result.ShouldPrintLingerDeniedNotice {
		t.Fatal("run 2: ShouldPrintLingerDeniedNotice should be false")
	}
	if prompter.calls != 1 {
		t.Fatalf("run 2: prompter must NOT be called when linger already on; total calls=%d", prompter.calls)
	}

	// Verify call sequence: 4 calls in run 1, 3 in run 2 (no
	// enable-linger because already on).
	calls := fake.Calls()
	if len(calls) != 7 {
		t.Fatalf("want 7 total calls, got %d: %+v", len(calls), calls)
	}
	for _, call := range calls[4:] {
		if call.Name == "loginctl" && len(call.Args) > 0 && call.Args[0] == "enable-linger" {
			t.Fatalf("run 2: enable-linger should not be called; got %+v", call)
		}
	}
}

// TestSystemctlError_NoDoubleErrText is the systemctl sibling of
// TestLaunchctlError_NoDoubleErrText (D5). Same shape, same expectation.
func TestSystemctlError_NoDoubleErrText(t *testing.T) {
	inner := errors.New("inner-systemctl-marker")
	wrapped := &systemctlError{
		Bin:      "systemctl",
		Args:     []string{"--user", "enable", "x.service"},
		Output:   "Failed to enable",
		ExitCode: 1,
		Err:      inner,
	}
	got := wrapped.Error()
	count := strings.Count(got, "inner-systemctl-marker")
	if count != 1 {
		t.Fatalf("inner error appears %d times in %q; want exactly 1", count, got)
	}
	if !errors.Is(wrapped, inner) {
		t.Fatalf("Unwrap chain broken: %q", got)
	}
}

// Note: seedWorkerConfig and withWorkerHome are reused from launchd_test.go;
// they live in the same package and write a schema-identical worker config.
