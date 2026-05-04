//go:build darwin

package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/justtunnel/justtunnel-cli/internal/worker/launchctl"
)

// probeDefaultTimeout caps how long Probe will wait on launchctl when the
// caller did not supply a deadline. launchctl print is normally <100ms but
// can hang on a wedged launchd; 5s is generous without making CLI commands
// like `worker status` feel unresponsive.
const probeDefaultTimeout = 5 * time.Second

// DarwinSupervisor probes launchd via `launchctl print gui/<UID>/<label>`.
//
// The launchctl shell-out lives here (build-tagged darwin), but ALL output
// parsing is delegated to launchctl.ParsePrint, which is pure and
// build-tag-free. This split keeps the parser unit-testable on every OS
// while keeping the syscall localized to the platform that needs it.
//
// We import internal/worker/launchctl (not internal/worker/installer) on
// purpose: installer depends on this package via worker.Read /
// worker.LogFilePath, so importing installer here would create a cycle.
type DarwinSupervisor struct{}

// NewSupervisorForOS returns a Supervisor appropriate for the build OS.
// On darwin it returns a DarwinSupervisor.
func NewSupervisorForOS() Supervisor { return &DarwinSupervisor{} }

// Probe shells out to launchctl and classifies the result. The label
// convention `dev.justtunnel.worker.<name>` is owned by the launchctl
// package; both the installer and this probe reach for the same constant
// to guarantee they can't drift.
//
// Context behavior: if ctx has no deadline, Probe applies probeDefaultTimeout
// so a wedged launchd cannot hang an interactive CLI command indefinitely.
// Callers that want unbounded behavior must explicitly pass a context with
// a far-future deadline.
func (s *DarwinSupervisor) Probe(ctx context.Context, workerName string) (ProbeResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, probeDefaultTimeout)
		defer cancel()
	}
	label := launchctl.Label(workerName)
	target := "gui/" + strconv.Itoa(os.Geteuid()) + "/" + label

	cmd := exec.CommandContext(ctx, "launchctl", "print", target)
	output, err := cmd.CombinedOutput()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// Process couldn't be started at all (e.g. launchctl not
			// found). Surface as a probe error rather than silently
			// claiming "not loaded".
			return ProbeResult{ServiceBackend: "launchd"}, err
		}
	}

	state, detail := launchctl.ParsePrint(string(output), exitCode)
	// ManagedByUs=true is INFERRED from launchctl finding the label in the
	// per-user domain. A third party that registers a service under our
	// `dev.justtunnel.worker.<name>` label would be misattributed as ours.
	// Acceptable known limitation for v1: the namespace is reverse-DNS and
	// collisions on a single user's machine are not a realistic threat.
	switch state {
	case launchctl.ProbeStateRunning:
		return ProbeResult{ServiceBackend: "launchd", ManagedByUs: true, Running: true, Detail: detail}, nil
	case launchctl.ProbeStateWaiting:
		return ProbeResult{ServiceBackend: "launchd", ManagedByUs: true, Running: false, Detail: detail}, nil
	case launchctl.ProbeStateNotLoaded:
		return ProbeResult{ServiceBackend: "launchd", ManagedByUs: false, Running: false}, nil
	default:
		return ProbeResult{ServiceBackend: "launchd", ManagedByUs: true, Running: false, Detail: detail}, nil
	}
}
