package installer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/justtunnel/justtunnel-cli/internal/worker"
	"github.com/justtunnel/justtunnel-cli/internal/worker/systemctl"
)

// UnitPrefix re-exports the systemd-user unit namespace from the systemctl
// package. Both the installer and LinuxSupervisor reach for the same
// constant; defining it once in systemctl keeps them in lockstep.
const UnitPrefix = systemctl.UnitPrefix

// LingerConsentPrompt is the verbatim copy from tech spec §6.4. The string
// is exported so tests can assert byte-exact equality and so docs can
// reference the same constant. Any edit here is a user-visible change and
// MUST be reflected in spec §6.4 too.
const LingerConsentPrompt = `Worker daemon needs to keep running after you log out. This requires enabling
user-linger via ` + "`loginctl enable-linger <user>`" + ` (a system-wide change).

Without it, your worker will stop when you log out, and CI requests will
return 503 worker_offline until you log back in.

Enable user-linger now? [Y/n] `

// LingerDeniedNotice is the verbatim message printed when the user declines
// the linger prompt or passes --no-linger. Like LingerConsentPrompt, this
// is exported for byte-exact test assertions and spec parity. The
// `<name>` placeholder is filled in via fmt.Sprintf at call sites that
// know the worker name.
const LingerDeniedNoticeFormat = `OK: worker installed without user-linger.
NOTE: this worker will stop when you log out. To enable persistent operation later:
      loginctl enable-linger <user>
      systemctl --user restart justtunnel-worker-%s
`

// LingerDeniedNotice formats LingerDeniedNoticeFormat for the given worker
// name. Kept as a function so callers don't have to remember the format
// directive; the format string itself is exported for spec parity.
func LingerDeniedNotice(workerName string) string {
	return fmt.Sprintf(LingerDeniedNoticeFormat, workerName)
}

// LingerPrompter prints the linger consent text and reads the user's
// response. Production callers use NewStdLingerPrompter; tests inject a
// fake so consent flow can be exercised without touching stdin.
type LingerPrompter interface {
	// Prompt prints LingerConsentPrompt to its output and reads a single
	// line of input. Returns true on Y / y / "" (Enter), false on
	// anything else. EOF / read error returns false with a non-nil err
	// only when the error is not io.EOF (EOF is treated as "deny").
	Prompt(ctx context.Context) (bool, error)
}

// StdLingerPrompter prompts on os.Stderr and reads from os.Stdin. The
// pipe-friendly default is "deny on EOF" so a scripted bootstrap that
// forgot --no-linger does not hang waiting for input.
type StdLingerPrompter struct {
	In  io.Reader
	Out io.Writer
}

// NewStdLingerPrompter returns a prompter wired to os.Stdin / os.Stderr.
// We deliberately use stderr (not stdout) so callers piping `worker
// install` output to a file still see the prompt.
func NewStdLingerPrompter() *StdLingerPrompter {
	return &StdLingerPrompter{In: os.Stdin, Out: os.Stderr}
}

// Prompt implements LingerPrompter.
func (prompter *StdLingerPrompter) Prompt(_ context.Context) (bool, error) {
	if _, err := fmt.Fprint(prompter.Out, LingerConsentPrompt); err != nil {
		return false, fmt.Errorf("installer: write linger prompt: %w", err)
	}
	scanner := bufio.NewScanner(prompter.In)
	if !scanner.Scan() {
		// EOF or read error: treat as deny so a non-interactive shell
		// never blocks. Surface non-EOF errors so genuine I/O failures
		// are still visible, but only after returning the safe "deny".
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("installer: read linger response: %w", err)
		}
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	// `[Y/n]` convention: empty answer (Enter) accepts; explicit y/yes
	// accepts; everything else (including "n", "no", garbage) denies.
	switch answer {
	case "", "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// SystemdInstaller installs and removes worker systemd-user units.
//
// Concurrency: a single installer is safe to call from multiple goroutines
// only if the underlying Runner / Prompter are. The default ExecRunner is;
// StdLingerPrompter is NOT (it shares a single bufio.Scanner per call but
// reads from a shared os.Stdin), so callers should not invoke Bootstrap
// concurrently.
type SystemdInstaller struct {
	// Runner is the command executor. Tests inject a fake; production
	// callers leave it nil and get ExecRunner via NewSystemdInstaller.
	Runner CommandRunner

	// Prompter prints the linger consent text and reads y/n. Tests
	// inject a fake; production callers leave it nil and get
	// StdLingerPrompter.
	Prompter LingerPrompter

	// Executable lets tests pin the path that ends up as the unit's
	// ExecStart binary. When nil, resolveExecutable is used.
	Executable func() (string, error)

	// HomeDir lets tests pin the directory under which
	// .config/systemd/user lives. When nil, os.UserHomeDir is used.
	// Note: systemd-user units must live under the *real* user home,
	// not under JUSTTUNNEL_HOME, so this is intentionally distinct
	// from worker.home().
	HomeDir func() (string, error)

	// CurrentUser lets tests pin the username passed to
	// `loginctl enable-linger`. When nil, os/user.Current is used.
	// We deliberately do NOT use $USER (it can be empty in scripted
	// contexts and is overridable).
	CurrentUser func() (string, error)

	// SystemdDetector lets tests bypass the /run/systemd/system probe
	// (which always fails on macOS). Returning a nil error means
	// "systemd is present"; a non-nil error is surfaced verbatim by
	// Bootstrap. When nil, the production check probes the filesystem.
	SystemdDetector func() error

	// WarnOut receives non-fatal diagnostic warnings (e.g. "could not
	// query linger status"). When nil, os.Stderr is used. Tests inject
	// a buffer to assert on warning text.
	WarnOut io.Writer
}

// warnWriter returns the configured warning writer, defaulting to stderr
// so production output goes where operators expect.
func (s *SystemdInstaller) warnWriter() io.Writer {
	if s.WarnOut != nil {
		return s.WarnOut
	}
	return os.Stderr
}

// NewSystemdInstaller returns an installer wired to ExecRunner, the
// std-stream linger prompter, and the real process executable / user home
// dir / current user.
func NewSystemdInstaller() *SystemdInstaller {
	return &SystemdInstaller{
		Runner:   ExecRunner{},
		Prompter: NewStdLingerPrompter(),
	}
}

// SystemdOptions carries call-site flags for Bootstrap.
type SystemdOptions struct {
	// NoLinger skips the linger prompt entirely. Used by CI / scripted
	// installs that don't want to enable persistent operation.
	NoLinger bool
}

// SystemdResult tells the caller which path Bootstrap took so it can render
// any operator-facing follow-up notice. The installer NEVER prints to
// stdout itself; it returns this struct and lets the cmd package decide.
type SystemdResult struct {
	// LingerEnabled is true when, after Bootstrap, user-linger is on for
	// this user. True both when we just enabled it and when it was
	// already enabled before Bootstrap ran.
	LingerEnabled bool

	// NoLingerWarningPrinted is true when the caller should print
	// LingerDeniedNotice — i.e. linger was NOT enabled (either via
	// --no-linger or via deny at the prompt) AND was not already on.
	NoLingerWarningPrinted bool
}

// unitTemplate is the systemd-user unit. Keep the indentation stable —
// the golden test in systemd_test.go pins the exact bytes.
const unitTemplate = `[Unit]
Description=JustTunnel Worker {{.Name}}
After=network-online.target
Wants=network-online.target

[Service]
ExecStart={{.BinaryPath}} worker start {{.Name}}
Restart=on-failure
RestartSec=5
StandardOutput=append:{{.LogPath}}
StandardError=append:{{.LogPath}}

[Install]
WantedBy=default.target
`

var compiledUnitTemplate = template.Must(template.New("unit").Parse(unitTemplate))

type unitData struct {
	Name       string
	BinaryPath string
	LogPath    string
}

// UnitName returns the full systemd-user unit name for the given worker
// (e.g. "justtunnel-worker-alpha.service"). Thin wrapper over
// systemctl.UnitName so installer-side callers don't need a second import.
func UnitName(workerName string) string { return systemctl.UnitName(workerName) }

// RenderUnit produces the unit-file bytes for a worker. All inputs are
// passed in explicitly so the function is pure.
//
// Paths containing newlines or NUL are rejected — systemd unit files are
// line-oriented INI and an embedded newline would let an attacker who
// controls $HOME inject extra directives.
func (s *SystemdInstaller) RenderUnit(workerName, binaryPath, logPath string) ([]byte, error) {
	if err := worker.ValidateName(workerName); err != nil {
		return nil, err
	}
	if err := validateUnitPath("binary path", binaryPath); err != nil {
		return nil, err
	}
	if err := validateUnitPath("log path", logPath); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	data := unitData{
		Name:       workerName,
		BinaryPath: binaryPath,
		LogPath:    logPath,
	}
	if err := compiledUnitTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("installer: render unit: %w", err)
	}
	return buf.Bytes(), nil
}

// UnitPath returns the absolute path at which the unit file for workerName
// will be written: ~/.config/systemd/user/justtunnel-worker-<name>.service.
// The systemd/user directory is NOT created here; Bootstrap creates it.
func (s *SystemdInstaller) UnitPath(workerName string) (string, error) {
	if err := worker.ValidateName(workerName); err != nil {
		return "", err
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".config", "systemd", "user", UnitName(workerName)), nil
}

// Bootstrap installs and starts the worker as a systemd-user unit.
//
// Sequence:
//  1. validate worker name
//  2. require worker.Read(name) to succeed (user must have run `worker create`)
//  3. resolve binary path (os.Executable, EvalSymlinks)
//  4. render unit; mkdir ~/.config/systemd/user (0755), write atomic (0644)
//  5. systemctl --user daemon-reload
//  6. systemctl --user enable --now <unit>
//  7. linger consent flow (see SystemdOptions / SystemdResult)
//
// Bootstrap is idempotent: re-running on an already-installed worker
// re-writes the unit file and re-runs daemon-reload + enable, both of
// which are no-ops on an already-enabled unit.
func (s *SystemdInstaller) Bootstrap(ctx context.Context, workerName string, opts SystemdOptions) (SystemdResult, error) {
	if err := worker.ValidateName(workerName); err != nil {
		return SystemdResult{}, err
	}
	if _, err := worker.Read(workerName); err != nil {
		return SystemdResult{}, fmt.Errorf("installer: worker %q config not found (run `justtunnel worker create` first): %w", workerName, err)
	}
	// Detect systemd before doing any work so we surface a clear error
	// rather than a confusing systemctl-not-found.
	if err := s.requireSystemd(ctx); err != nil {
		return SystemdResult{}, err
	}
	binaryPath, err := s.executable()
	if err != nil {
		return SystemdResult{}, fmt.Errorf("installer: resolve executable: %w", err)
	}
	logPath, err := worker.LogFilePath(workerName)
	if err != nil {
		return SystemdResult{}, err
	}
	unitContent, err := s.RenderUnit(workerName, binaryPath, logPath)
	if err != nil {
		return SystemdResult{}, err
	}
	unitPath, err := s.UnitPath(workerName)
	if err != nil {
		return SystemdResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return SystemdResult{}, fmt.Errorf("installer: create systemd/user dir: %w", err)
	}
	if err := writeFileAtomic(unitPath, unitContent, 0o644); err != nil {
		return SystemdResult{}, err
	}

	if err := s.runSystemctl(ctx, "--user", "daemon-reload"); err != nil {
		// daemon-reload failed AFTER we successfully wrote the unit
		// file. Leaving the file behind would cause the next
		// daemon-reload (on the user's next login) to silently pick
		// up a partially-installed worker. Compensate by removing the
		// just-written unit file and surface BOTH outcomes.
		cleanupNote := "unit file removed"
		if removeErr := os.Remove(unitPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupNote = fmt.Sprintf("unit file cleanup failed: %v", removeErr)
		}
		return SystemdResult{}, fmt.Errorf("installer: systemctl daemon-reload (%s): %w", cleanupNote, err)
	}
	if err := s.runSystemctl(ctx, "--user", "enable", "--now", UnitName(workerName)); err != nil {
		return SystemdResult{}, fmt.Errorf("installer: systemctl enable --now: %w", err)
	}

	// --- linger consent flow ----------------------------------------
	// The unit is already enabled at this point. Linger is a separate,
	// system-wide change; failure to enable linger does NOT roll back
	// the unit install — the worker just becomes session-bound.
	if opts.NoLinger {
		return SystemdResult{LingerEnabled: false, NoLingerWarningPrinted: true}, nil
	}

	username, userErr := s.currentUser()
	if userErr != nil {
		// We can't enable linger without a username. Fall through to
		// the deny-path notice rather than failing the whole install.
		return SystemdResult{LingerEnabled: false, NoLingerWarningPrinted: true},
			fmt.Errorf("installer: resolve current user (worker installed but linger NOT configured): %w", userErr)
	}

	alreadyOn, lingerErr := s.lingerEnabled(ctx, username)
	if lingerErr != nil {
		// Couldn't probe linger state (e.g. dbus failure). Don't fail
		// the install — just warn and proceed to the consent prompt.
		// The user can still opt in or out, and the worker is already
		// installed and enabled at this point.
		fmt.Fprintf(s.warnWriter(), "warning: could not query linger status (%v); proceeding with consent prompt\n", lingerErr)
		alreadyOn = false
	}
	if alreadyOn {
		return SystemdResult{LingerEnabled: true}, nil
	}

	consent, promptErr := s.Prompter.Prompt(ctx)
	if promptErr != nil {
		return SystemdResult{LingerEnabled: false, NoLingerWarningPrinted: true},
			fmt.Errorf("installer: linger prompt failed (worker installed but linger NOT configured): %w", promptErr)
	}
	if !consent {
		return SystemdResult{LingerEnabled: false, NoLingerWarningPrinted: true}, nil
	}

	if err := s.runLoginctl(ctx, "enable-linger", username); err != nil {
		// Most common failure: permissions. Worker is still installed
		// and enabled; we just couldn't make it session-independent.
		return SystemdResult{LingerEnabled: false, NoLingerWarningPrinted: true},
			fmt.Errorf("installer: loginctl enable-linger %s failed (worker is installed but session-bound; see `man loginctl`): %w", username, err)
	}
	return SystemdResult{LingerEnabled: true}, nil
}

// Unbootstrap stops and removes the worker's systemd-user unit. Both
// systemctl disable and the file removal are idempotent: a missing unit
// is treated as a successful no-op so callers can run unbootstrap
// unconditionally during teardown.
//
// Linger is a user-managed system-wide setting; we NEVER disable it
// during teardown even if Bootstrap was the one to enable it. Disabling
// linger could break unrelated user services.
func (s *SystemdInstaller) Unbootstrap(ctx context.Context, workerName string) error {
	if err := worker.ValidateName(workerName); err != nil {
		return err
	}
	unitName := UnitName(workerName)
	if err := s.runSystemctl(ctx, "--user", "disable", "--now", unitName); err != nil && !isUnitNotLoaded(err) {
		return fmt.Errorf("installer: systemctl disable --now: %w", err)
	}
	unitPath, err := s.UnitPath(workerName)
	if err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("installer: remove unit %s: %w", unitPath, err)
	}
	// daemon-reload after delete is best-effort: it lets systemd forget
	// the deleted unit immediately. Failure here is not fatal — systemd
	// will pick up the removal on its next reload anyway.
	_ = s.runSystemctl(ctx, "--user", "daemon-reload")
	return nil
}

// requireSystemd verifies that systemd is the init system. Some containers
// and minimal Linux installs ship with OpenRC, runit, or no init at all;
// failing here with a clear message beats a confusing `systemctl: command
// not found` later in the flow.
func (s *SystemdInstaller) requireSystemd(_ context.Context) error {
	if s.SystemdDetector != nil {
		return s.SystemdDetector()
	}
	// /run/systemd/system is created by systemd at boot. It's the
	// canonical "is systemd PID 1" check, used by sd_booted(3).
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("installer: systemd not detected (worker install requires systemd; see https://systemd.io)")
		}
		return fmt.Errorf("installer: detect systemd: %w", err)
	}
	return nil
}

// lingerEnabled returns true when loginctl reports Linger=yes for username.
//
// Error policy: loginctl exits non-zero for unknown users (a brand-new
// user that has never logged in) — that is benign and we surface it as
// `(false, nil)` so the install proceeds via the prompt path. Anything
// else (dbus failure, EACCES, loginctl missing) returns `(false, err)`
// so the caller can decide whether to abort or continue.
func (s *SystemdInstaller) lingerEnabled(ctx context.Context, username string) (bool, error) {
	out, err := s.Runner.Run(ctx, "loginctl", "show-user", username, "--property=Linger")
	if err != nil {
		if isUnknownLoginctlUser(out, err) {
			return false, nil
		}
		return false, fmt.Errorf("installer: loginctl show-user %s: %w", username, err)
	}
	return systemctl.ParseLingerEnabled(string(out)), nil
}

// isUnknownLoginctlUser reports whether a non-zero loginctl exit indicates
// the queried user is unknown to systemd-logind (which is benign for a
// linger probe — there is simply nothing to query). Distinguishing this
// from real I/O failures lets the caller surface dbus / permission errors
// instead of silently treating them as "linger=no".
func isUnknownLoginctlUser(out []byte, err error) bool {
	// "executable not installed" — loginctl missing entirely. Not an
	// unknown-user case; surface it.
	if errors.Is(err, exec.ErrNotFound) {
		return false
	}
	lower := strings.ToLower(string(out))
	if strings.Contains(lower, "unknown user") || strings.Contains(lower, "no such user") {
		return true
	}
	// Some loginctl builds print to the err string only.
	if strings.Contains(strings.ToLower(err.Error()), "unknown user") {
		return true
	}
	return false
}

// runSystemctl invokes `systemctl <args...>` via Runner. The exit code is
// extracted into a systemctlError so isUnitNotLoaded can match on either
// signal — older systemd releases sometimes drop the "Unit not loaded"
// text but reliably exit 5.
func (s *SystemdInstaller) runSystemctl(ctx context.Context, args ...string) error {
	out, err := s.Runner.Run(ctx, "systemctl", args...)
	if err != nil {
		return wrapSystemctlError("systemctl", args, out, err)
	}
	return nil
}

// runLoginctl invokes `loginctl <args...>` via Runner. We share the
// systemctlError type because the two binaries surface failures the same
// way (combined output + non-zero exit) and the caller doesn't need to
// distinguish them.
func (s *SystemdInstaller) runLoginctl(ctx context.Context, args ...string) error {
	out, err := s.Runner.Run(ctx, "loginctl", args...)
	if err != nil {
		return wrapSystemctlError("loginctl", args, out, err)
	}
	return nil
}

// systemctlError carries combined output AND exit code so error classifiers
// can match on whichever signal systemctl surfaces.
type systemctlError struct {
	Bin      string
	Args     []string
	Output   string
	ExitCode int // -1 when the exit code couldn't be extracted.
	Err      error
}

func (e *systemctlError) Error() string {
	return fmt.Sprintf("%s %s: exit %d: %v: %s", e.Bin, strings.Join(e.Args, " "), e.ExitCode, e.Err, strings.TrimSpace(e.Output))
}

func (e *systemctlError) Unwrap() error { return e.Err }

func wrapSystemctlError(bin string, args []string, out []byte, err error) error {
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if coded, ok := err.(interface{ ExitCode() int }); ok {
		// Test fakes implement ExitCode() without being *exec.ExitError.
		exitCode = coded.ExitCode()
	}
	return &systemctlError{
		Bin:      bin,
		Args:     append([]string{}, args...),
		Output:   string(out),
		ExitCode: exitCode,
		Err:      err,
	}
}

// exitCodeUnitNotLoaded is the systemctl exit code for "unit file not
// found" / "unit not loaded". Stable across modern systemd releases.
const exitCodeUnitNotLoaded = 5

// isUnitNotLoaded matches the family of systemctl disable failures meaning
// "no such unit". We check exit code first (cheapest, most reliable) and
// fall back to substring matching for releases where the diagnostic was
// the only signal that propagated.
func isUnitNotLoaded(err error) bool {
	var systemctlErr *systemctlError
	if !errors.As(err, &systemctlErr) {
		return false
	}
	if systemctlErr.ExitCode == exitCodeUnitNotLoaded {
		return true
	}
	out := strings.ToLower(systemctlErr.Output)
	switch {
	case strings.Contains(out, "no such file or directory"):
		return true
	case strings.Contains(out, "not loaded"):
		return true
	case strings.Contains(out, "does not exist"):
		return true
	case strings.Contains(out, "no such unit"):
		return true
	}
	return false
}

// --- helpers ---------------------------------------------------------------

func (s *SystemdInstaller) executable() (string, error) {
	if s.Executable != nil {
		return s.Executable()
	}
	return resolveExecutable()
}

func (s *SystemdInstaller) homeDir() (string, error) {
	if s.HomeDir != nil {
		return s.HomeDir()
	}
	return os.UserHomeDir()
}

func (s *SystemdInstaller) currentUser() (string, error) {
	if s.CurrentUser != nil {
		return s.CurrentUser()
	}
	currentUser, err := user.Current()
	if err != nil {
		return "", err
	}
	if currentUser.Username == "" {
		return "", errors.New("os/user.Current returned empty username")
	}
	return currentUser.Username, nil
}

// validateUnitPath rejects paths containing characters that would break
// the systemd unit file's INI grammar OR get reinterpreted by systemd's
// specifier expansion. Real Linux paths in commonly-used locations don't
// contain newlines, NUL, or `%`; rejecting them defends against an attacker
// who controls $HOME or some other input flowing into the unit file.
//
// `%` is rejected because systemd performs specifier expansion on unit-file
// values (e.g. `%n` -> unit name, `%h` -> user home) and a path like
// `/log/%n.log` would silently substitute rather than write to the literal
// path. See systemd.unit(5) "SPECIFIERS".
func validateUnitPath(label, path string) error {
	if path == "" {
		return fmt.Errorf("installer: empty %s", label)
	}
	if strings.ContainsAny(path, "\n\r\x00") {
		return fmt.Errorf("installer: %s %q contains newline or NUL", label, path)
	}
	if strings.ContainsRune(path, '%') {
		return fmt.Errorf("installer: %s %q contains %% (systemd specifier metacharacter)", label, path)
	}
	return nil
}
