// Package systemctl holds the build-tag-free helpers shared by both the
// systemd-user installer (internal/worker/installer) and LinuxSupervisor
// (internal/worker, build-tagged linux). Keeping these in a leaf package
// breaks what would otherwise be an installer<->worker import cycle and
// mirrors the launchctl package's role on macOS.
//
// Everything in this package is pure: no syscalls, no env reads, no clock.
// That makes the parser unit-testable on every OS and lets LinuxSupervisor
// (which IS build-tagged) stay tiny.
package systemctl

import "strings"

// UnitPrefix is the systemd-user unit namespace for worker tunnels. The
// full unit name for worker "foo" is "justtunnel-worker-foo.service".
// Both the installer (when writing the unit file / enabling it) and
// LinuxSupervisor (when probing) MUST use the same prefix; defining it
// here ensures they can't drift.
const UnitPrefix = "justtunnel-worker-"

// UnitSuffix is the systemd unit type suffix. Kept separate from UnitPrefix
// so callers that need just the bare name (logging, error messages) don't
// have to strip ".service" by hand.
const UnitSuffix = ".service"

// UnitName returns the full systemd-user unit name for the given worker
// name (e.g. "justtunnel-worker-alpha.service").
func UnitName(workerName string) string { return UnitPrefix + workerName + UnitSuffix }

// ProbeState is the high-level result extracted from
// `systemctl --user is-active <unit>`.
type ProbeState int

const (
	// ProbeStateUnknown — the parser couldn't classify the output.
	// Callers should treat this as ManagedByUs=true, Running=false and
	// surface the raw output in Detail.
	ProbeStateUnknown ProbeState = iota
	// ProbeStateRunning — systemctl reports `active` (exit 0).
	ProbeStateRunning
	// ProbeStateWaiting — systemctl reports `inactive` or `failed`
	// (exit 3): unit is loaded but not currently running.
	ProbeStateWaiting
	// ProbeStateNotLoaded — unit is not loaded (exit 4 / "unknown").
	ProbeStateNotLoaded
)

// Exit codes documented at:
//   https://www.freedesktop.org/software/systemd/man/latest/systemctl.html#Exit%20status
//
// We pin the codes we rely on as named constants so the classifier stays
// readable; callers should NOT depend on these directly.
const (
	exitCodeActive   = 0 // active
	exitCodeInactive = 3 // inactive / failed
	exitCodeUnknown  = 4 // no such unit
)

// ParseIsActive classifies the combined output of `systemctl --user
// is-active <unit>` plus the process's exit code, returning the high-level
// state and an optional human-readable detail (e.g. "failed").
//
// We accept BOTH exit code AND output text so the classifier survives
// systemd updates that change one signal but not the other; the textual
// state ("active"/"inactive"/"failed"/"unknown") is the canonical signal,
// the exit code is the fallback when output was redirected away.
func ParseIsActive(output string, exitCode int) (ProbeState, string) {
	state := strings.TrimSpace(strings.ToLower(output))
	// is-active prints exactly one word per line; if multiple units were
	// queried we'd see multiple lines, but worker probes pass a single
	// unit so the first line is authoritative.
	if idx := strings.Index(state, "\n"); idx >= 0 {
		state = strings.TrimSpace(state[:idx])
	}

	switch state {
	case "active":
		return ProbeStateRunning, ""
	case "inactive":
		return ProbeStateWaiting, ""
	case "failed":
		return ProbeStateWaiting, "failed"
	case "unknown":
		return ProbeStateNotLoaded, ""
	}

	// Text was empty or unrecognized. Fall back to the exit code.
	switch exitCode {
	case exitCodeActive:
		return ProbeStateRunning, ""
	case exitCodeInactive:
		return ProbeStateWaiting, ""
	case exitCodeUnknown:
		return ProbeStateNotLoaded, ""
	}
	if state == "" {
		return ProbeStateUnknown, ""
	}
	return ProbeStateUnknown, state
}

// ParseLingerEnabled returns true when `loginctl show-user <user>
// --property=Linger` reports `Linger=yes`. Empty / malformed input is
// treated as "not enabled" so the installer prompts on uncertainty.
func ParseLingerEnabled(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Linger=") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "Linger="))
		return strings.EqualFold(value, "yes")
	}
	return false
}
