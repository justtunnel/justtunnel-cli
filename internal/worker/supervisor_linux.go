//go:build linux

package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/justtunnel/justtunnel-cli/internal/worker/systemctl"
)

// probeDefaultTimeout caps how long Probe will wait on systemctl when the
// caller did not supply a deadline. systemctl --user is-active is normally
// <100ms but can hang on a wedged systemd; 5s is generous without making
// CLI commands like `worker status` feel unresponsive.
const probeDefaultTimeout = 5 * time.Second

// LinuxSupervisor probes systemd --user via
// `systemctl --user is-active justtunnel-worker-<name>.service`.
//
// The systemctl shell-out lives here (build-tagged linux), but ALL output
// parsing is delegated to systemctl.ParseIsActive, which is pure and
// build-tag-free. This split keeps the parser unit-testable on every OS
// while keeping the syscall localized to the platform that needs it.
//
// We import internal/worker/systemctl (not internal/worker/installer) on
// purpose: installer depends on this package via worker.Read /
// worker.LogFilePath, so importing installer here would create a cycle.
type LinuxSupervisor struct{}

// NewSupervisorForOS returns a Supervisor appropriate for the build OS.
// On linux it returns a LinuxSupervisor.
func NewSupervisorForOS() Supervisor { return &LinuxSupervisor{} }

// Probe shells out to systemctl --user is-active and classifies the
// result. The unit-name convention `justtunnel-worker-<name>.service`
// is owned by the systemctl package; both the installer and this probe
// reach for the same constant to guarantee they can't drift.
//
// ManagedByUs is determined by checking the unit file on disk
// (~/.config/systemd/user/justtunnel-worker-<name>.service) rather than
// trusting systemctl's view, because `is-active` of a deleted-but-still-
// loaded unit can briefly return "active" before the next daemon-reload.
// The filesystem check is the authoritative "did we install this?" signal.
//
// Context behavior: if ctx has no deadline, Probe applies probeDefaultTimeout
// so a wedged systemd cannot hang an interactive CLI command indefinitely.
func (s *LinuxSupervisor) Probe(ctx context.Context, workerName string) (ProbeResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, probeDefaultTimeout)
		defer cancel()
	}
	unitName := systemctl.UnitName(workerName)
	managedByUs := unitFileExists(workerName)

	cmd := exec.CommandContext(ctx, "systemctl", "--user", "is-active", unitName)
	// Use Output() (NOT CombinedOutput): is-active prints the unit state
	// to stdout. Stderr carries diagnostics like
	// "Failed to connect to bus: …" that would corrupt the parser if
	// they were folded into the same stream.
	output, err := cmd.Output()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			// E8: stderr is available on *exec.ExitError when Output()
			// is used. The parser intentionally only sees stdout (the
			// canonical state vocabulary lives there); if we ever want
			// to surface stderr, we'll add it at that time.
		} else {
			// Process couldn't be started at all (e.g. systemctl not
			// found). Surface as a probe error rather than silently
			// claiming "not loaded".
			return ProbeResult{ServiceBackend: "systemd"}, err
		}
	}

	state, detail := systemctl.ParseIsActive(string(output), exitCode)
	switch state {
	case systemctl.ProbeStateRunning:
		return ProbeResult{ServiceBackend: "systemd", ManagedByUs: managedByUs, Running: true, Detail: detail}, nil
	case systemctl.ProbeStateWaiting:
		return ProbeResult{ServiceBackend: "systemd", ManagedByUs: managedByUs, Running: false, Detail: detail}, nil
	case systemctl.ProbeStateNotLoaded:
		// Even when systemctl says "unknown", we honor the on-disk
		// unit file as the authoritative ManagedByUs signal: a unit
		// can exist on disk before the user ran daemon-reload.
		return ProbeResult{ServiceBackend: "systemd", ManagedByUs: managedByUs, Running: false}, nil
	default:
		return ProbeResult{ServiceBackend: "systemd", ManagedByUs: managedByUs, Running: false, Detail: detail}, nil
	}
}

// unitFileExists checks whether
// ~/.config/systemd/user/justtunnel-worker-<name>.service is present on
// disk. Errors (including a missing $HOME) are treated as "not present"
// — the goal is a cheap best-effort signal, not authoritative ownership.
func unitFileExists(workerName string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	path := filepath.Join(home, ".config", "systemd", "user", systemctl.UnitName(workerName))
	_, err = os.Stat(path)
	return err == nil
}
