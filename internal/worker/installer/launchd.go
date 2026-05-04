// Package installer renders OS service-supervisor manifests (launchd plists,
// systemd unit files) and shells out to the appropriate supervisor CLI to
// install/uninstall worker tunnels.
//
// All shell-outs flow through the CommandRunner interface so unit tests can
// inject a fake. The plist rendering itself is pure: no syscalls, no env
// reads, no clock — every input arrives as a string parameter so the tests
// can pin the output as a golden string.
package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/justtunnel/justtunnel-cli/internal/worker"
	"github.com/justtunnel/justtunnel-cli/internal/worker/launchctl"
)

// LabelPrefix re-exports the launchd label namespace from the launchctl
// package. Both the installer and DarwinSupervisor reach for the same
// constant; defining it once in launchctl keeps them in lockstep.
const LabelPrefix = launchctl.LabelPrefix

// CommandRunner is the seam between the installer and the OS. Production
// code uses ExecRunner (which shells out via os/exec); tests use a fake.
type CommandRunner interface {
	// Run executes name with args, returning combined stdout+stderr and
	// any process error. Implementations MUST honor ctx cancellation.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands via os/exec.CommandContext.CombinedOutput.
type ExecRunner struct{}

// Run implements CommandRunner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// LaunchdInstaller installs and removes worker launchd agents.
//
// Concurrency: a single installer is safe to call from multiple goroutines
// only if the underlying Runner is. The default ExecRunner is.
type LaunchdInstaller struct {
	// Runner is the command executor. Tests inject a fake; production
	// callers leave it nil and get ExecRunner via NewLaunchdInstaller.
	Runner CommandRunner

	// Geteuid lets tests pin the uid that ends up in "gui/<uid>". When
	// nil, os.Geteuid is used. (launchctl bootstrap targets a specific
	// per-user domain, so this needs to match the calling user's uid.)
	Geteuid func() int

	// Executable lets tests pin the path that ends up as the plist's
	// ProgramArguments[0]. When nil, resolveExecutable is used.
	Executable func() (string, error)

	// HomeDir lets tests pin the directory under which
	// Library/LaunchAgents lives. When nil, os.UserHomeDir is used.
	// Note: launchd plists must live under the *real* user home, not
	// under JUSTTUNNEL_HOME, so this is intentionally distinct from
	// worker.home().
	HomeDir func() (string, error)
}

// NewLaunchdInstaller returns an installer wired to ExecRunner and the real
// process uid / executable / user home dir.
func NewLaunchdInstaller() *LaunchdInstaller {
	return &LaunchdInstaller{Runner: ExecRunner{}}
}

// plistTemplate is the launchd agent plist. Keep the indentation stable —
// the golden test in launchd_test.go pins the exact bytes.
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>{{.Label}}</string>
  <key>ProgramArguments</key> <array>
    <string>{{.BinaryPath}}</string>
    <string>worker</string>
    <string>start</string>
    <string>{{.Name}}</string>
  </array>
  <key>KeepAlive</key>        <true/>
  <key>RunAtLoad</key>        <true/>
  <key>StandardOutPath</key>  <string>{{.LogPath}}</string>
  <key>StandardErrorPath</key><string>{{.LogPath}}</string>
</dict>
</plist>
`

var compiledPlistTemplate = template.Must(template.New("plist").Parse(plistTemplate))

type plistData struct {
	Label      string
	Name       string
	BinaryPath string
	LogPath    string
}

// Label returns the full launchd label for the given worker name. It does
// NOT validate the name — callers should have already done so. Thin wrapper
// over launchctl.Label so installer-side callers don't need a second import.
func Label(workerName string) string { return launchctl.Label(workerName) }

// RenderPlist produces the plist bytes for a worker. All inputs are passed
// in explicitly so the function is pure; the only XML-escaping done is via
// text/template's default escaping (sufficient for path strings, which are
// the only attacker-influenced fields, and even those are validated upstream
// by worker.validateName / filesystem-safe path resolution).
//
// To keep the function safe against pathological inputs, paths containing
// XML metacharacters (<, >, &, ") are rejected — launchd plists are XML
// and a path with embedded angle brackets is almost certainly an attack
// rather than a real macOS path.
func (l *LaunchdInstaller) RenderPlist(workerName, binaryPath, logPath string) ([]byte, error) {
	if err := validateWorkerName(workerName); err != nil {
		return nil, err
	}
	if err := validatePlistPath("binary path", binaryPath); err != nil {
		return nil, err
	}
	if err := validatePlistPath("log path", logPath); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	data := plistData{
		Label:      Label(workerName),
		Name:       workerName,
		BinaryPath: binaryPath,
		LogPath:    logPath,
	}
	if err := compiledPlistTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("installer: render plist: %w", err)
	}
	return buf.Bytes(), nil
}

// PlistPath returns the absolute path at which the plist for workerName
// will be written: ~/Library/LaunchAgents/dev.justtunnel.worker.<name>.plist.
// The LaunchAgents directory is NOT created here; Bootstrap creates it.
func (l *LaunchdInstaller) PlistPath(workerName string) (string, error) {
	if err := validateWorkerName(workerName); err != nil {
		return "", err
	}
	homeDir, err := l.homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, "Library", "LaunchAgents", Label(workerName)+".plist"), nil
}

// Bootstrap installs and starts the worker as a launchd user agent.
//
// Sequence:
//  1. validate worker name
//  2. require worker.Read(name) to succeed (user must have run `worker create`)
//  3. resolve binary path (os.Executable, EvalSymlinks)
//  4. render plist
//  5. mkdir ~/Library/LaunchAgents (0755), write plist (0644, atomic)
//  6. launchctl bootstrap gui/<uid> <plistPath>; on "already loaded", do
//     bootout-then-retry once (idempotent contract)
//  7. launchctl enable gui/<uid>/<label>
func (l *LaunchdInstaller) Bootstrap(ctx context.Context, workerName string) error {
	if err := validateWorkerName(workerName); err != nil {
		return err
	}
	if _, err := worker.Read(workerName); err != nil {
		return fmt.Errorf("installer: worker %q config not found (run `justtunnel worker create` first): %w", workerName, err)
	}
	binaryPath, err := l.executable()
	if err != nil {
		return fmt.Errorf("installer: resolve executable: %w", err)
	}
	logPath, err := worker.LogFilePath(workerName)
	if err != nil {
		return err
	}
	plistContent, err := l.RenderPlist(workerName, binaryPath, logPath)
	if err != nil {
		return err
	}
	plistPath, err := l.PlistPath(workerName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("installer: create LaunchAgents dir: %w", err)
	}
	if err := writeFileAtomic(plistPath, plistContent, 0o644); err != nil {
		return err
	}

	uid := strconv.Itoa(l.geteuid())
	domain := "gui/" + uid
	bootstrapErr := l.runLaunchctl(ctx, "bootstrap", domain, plistPath)
	if bootstrapErr != nil && isAlreadyLoaded(bootstrapErr) {
		// Idempotent retry: bootout the existing target then bootstrap
		// again. We deliberately ignore bootout failure here; if the
		// state is genuinely broken the second bootstrap will surface it.
		_ = l.runLaunchctl(ctx, "bootout", domain+"/"+Label(workerName))
		bootstrapErr = l.runLaunchctl(ctx, "bootstrap", domain, plistPath)
	}
	if bootstrapErr != nil {
		return fmt.Errorf("installer: launchctl bootstrap: %w", bootstrapErr)
	}
	if err := l.runLaunchctl(ctx, "enable", domain+"/"+Label(workerName)); err != nil {
		return fmt.Errorf("installer: launchctl enable: %w", err)
	}
	return nil
}

// Unbootstrap stops and removes the worker's launchd agent. Both steps are
// idempotent: a missing service or a missing plist file is treated as a
// successful no-op so callers can run unbootstrap unconditionally during
// teardown.
func (l *LaunchdInstaller) Unbootstrap(ctx context.Context, workerName string) error {
	if err := validateWorkerName(workerName); err != nil {
		return err
	}
	uid := strconv.Itoa(l.geteuid())
	target := "gui/" + uid + "/" + Label(workerName)
	if err := l.runLaunchctl(ctx, "bootout", target); err != nil && !isNotLoaded(err) {
		return fmt.Errorf("installer: launchctl bootout: %w", err)
	}
	plistPath, err := l.PlistPath(workerName)
	if err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("installer: remove plist %s: %w", plistPath, err)
	}
	return nil
}

// runLaunchctl invokes `launchctl <args...>` via Runner. The combined output
// is folded into the returned error so callers (and isAlreadyLoaded /
// isNotLoaded) can pattern-match on launchctl's stderr text.
func (l *LaunchdInstaller) runLaunchctl(ctx context.Context, args ...string) error {
	out, err := l.Runner.Run(ctx, "launchctl", args...)
	if err != nil {
		return &launchctlError{
			Args:   args,
			Output: string(out),
			Err:    err,
		}
	}
	return nil
}

// launchctlError carries the combined output so error classifiers (e.g.
// isAlreadyLoaded) can look at stderr text rather than relying solely on
// exit codes, which launchctl uses inconsistently across macOS versions.
type launchctlError struct {
	Args   []string
	Output string
	Err    error
}

func (e *launchctlError) Error() string {
	return fmt.Sprintf("launchctl %s: %v: %s", strings.Join(e.Args, " "), e.Err, strings.TrimSpace(e.Output))
}

func (e *launchctlError) Unwrap() error { return e.Err }

// isAlreadyLoaded matches the family of launchctl bootstrap failures meaning
// "a service with this label is already loaded into the target domain". We
// match on substrings of the combined output rather than on exit codes
// because macOS has shipped at least three different exit codes for this
// condition over the years.
func isAlreadyLoaded(err error) bool {
	var launchctlErr *launchctlError
	if !errors.As(err, &launchctlErr) {
		return false
	}
	out := strings.ToLower(launchctlErr.Output)
	switch {
	case strings.Contains(out, "service already loaded"):
		return true
	case strings.Contains(out, "already loaded"):
		return true
	case strings.Contains(out, "already bootstrapped"):
		return true
	}
	return false
}

// isNotLoaded matches launchctl bootout's "no such service" / "could not
// find specified service" failure modes. Same rationale as isAlreadyLoaded
// for matching on output instead of exit code.
func isNotLoaded(err error) bool {
	var launchctlErr *launchctlError
	if !errors.As(err, &launchctlErr) {
		return false
	}
	out := strings.ToLower(launchctlErr.Output)
	switch {
	case strings.Contains(out, "could not find specified service"):
		return true
	case strings.Contains(out, "no such process"):
		return true
	case strings.Contains(out, "not loaded"):
		return true
	}
	return false
}

// --- helpers ---------------------------------------------------------------

func (l *LaunchdInstaller) geteuid() int {
	if l.Geteuid != nil {
		return l.Geteuid()
	}
	return os.Geteuid()
}

func (l *LaunchdInstaller) executable() (string, error) {
	if l.Executable != nil {
		return l.Executable()
	}
	return resolveExecutable()
}

func (l *LaunchdInstaller) homeDir() (string, error) {
	if l.HomeDir != nil {
		return l.HomeDir()
	}
	return os.UserHomeDir()
}

// resolveExecutable returns the absolute, symlink-resolved path to the
// running binary. EvalSymlinks matters on macOS because Homebrew installs
// `justtunnel` as a symlink under /opt/homebrew/bin pointing into the
// versioned Cellar; pinning the symlink in a plist would break on the
// next `brew upgrade`. EvalSymlinks resolves to the underlying path.
func resolveExecutable() (string, error) {
	binaryPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(binaryPath)
	if err != nil {
		// EvalSymlinks failure is not necessarily fatal — fall back to
		// the unresolved path rather than aborting bootstrap.
		return binaryPath, nil
	}
	return resolved, nil
}

// validateWorkerName re-applies the worker package's name regex without
// exporting it. We can't import worker.validateName (unexported), but the
// pattern is part of the cross-package contract so it's mirrored here.
// If you change one, change the other (and the server-side validator).
func validateWorkerName(name string) error {
	if name == "" {
		return errors.New("installer: empty worker name")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return fmt.Errorf("installer: invalid worker name %q (lowercase alphanumeric and hyphen only)", name)
		}
	}
	if name[0] == '-' {
		return fmt.Errorf("installer: invalid worker name %q (must start with [a-z0-9])", name)
	}
	if len(name) > 63 {
		return fmt.Errorf("installer: invalid worker name %q (max 63 chars)", name)
	}
	return nil
}

// validatePlistPath rejects paths containing XML metacharacters. Real
// macOS paths cannot contain <, >, or & in any commonly-installed location;
// rejecting them defends against an attacker who controls $HOME or some
// other input flowing into the plist.
func validatePlistPath(label, path string) error {
	if path == "" {
		return fmt.Errorf("installer: empty %s", label)
	}
	if strings.ContainsAny(path, "<>&\"") {
		return fmt.Errorf("installer: %s %q contains XML metacharacters", label, path)
	}
	return nil
}

// writeFileAtomic writes data to path via temp-file + rename. The temp file
// is created in the same directory so rename is atomic on the same fs.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("installer: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("installer: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("installer: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("installer: close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("installer: chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("installer: rename temp -> final: %w", err)
	}
	cleanup = false
	return nil
}
