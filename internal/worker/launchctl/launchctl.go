// Package launchctl holds the build-tag-free helpers shared by both the
// launchd installer (internal/worker/installer) and DarwinSupervisor
// (internal/worker, build-tagged darwin). Keeping these in a leaf package
// breaks what would otherwise be an installer<->worker import cycle.
//
// Everything in this package is pure: no syscalls, no env reads, no clock.
// That makes the parser unit-testable on every OS and lets DarwinSupervisor
// (which IS build-tagged) stay tiny.
package launchctl

import "strings"

// LabelPrefix is the launchd label namespace for worker agents. The full
// label for worker "foo" is "dev.justtunnel.worker.foo". Both the installer
// (when bootstrapping/booting out) and DarwinSupervisor (when probing) MUST
// use the same prefix; defining it here ensures they can't drift.
const LabelPrefix = "dev.justtunnel.worker."

// Label returns the full launchd label for the given worker name.
func Label(workerName string) string { return LabelPrefix + workerName }

// ProbeState is the high-level result extracted from `launchctl print`.
type ProbeState int

const (
	// ProbeStateUnknown — the parser couldn't classify the output.
	// Callers should treat this as ManagedByUs=true, Running=false and
	// surface the raw output in Detail.
	ProbeStateUnknown ProbeState = iota
	// ProbeStateRunning — launchctl reports `state = running`.
	ProbeStateRunning
	// ProbeStateWaiting — launchctl reports `state = waiting` (KeepAlive
	// enabled, between restarts).
	ProbeStateWaiting
	// ProbeStateNotLoaded — service is not loaded into the target domain
	// (launchctl exit 113 / "Could not find specified service").
	ProbeStateNotLoaded
)

// ExitCodeNotFound is the exit code launchctl returns when the requested
// service is not loaded. Stable across recent macOS releases (Monterey
// through Sequoia); the textual fallback in ParsePrint covers releases
// where the exit code didn't propagate cleanly through wrapper invocations.
const ExitCodeNotFound = 113

// ParsePrint classifies the combined output of `launchctl print
// gui/<uid>/<label>` plus the process's exit code, returning the high-level
// state and an optional human-readable detail (e.g. "pid=12345").
//
// We accept BOTH exit code AND output text so the classifier survives macOS
// updates that change one signal but not the other.
func ParsePrint(output string, exitCode int) (ProbeState, string) {
	lower := strings.ToLower(output)

	if exitCode == ExitCodeNotFound ||
		strings.Contains(lower, "could not find service") ||
		strings.Contains(lower, "could not find specified service") {
		return ProbeStateNotLoaded, ""
	}

	if exitCode != 0 {
		return ProbeStateUnknown, firstLine(output)
	}

	state := extractStateField(lower)
	pidDetail := extractPidDetail(output)

	switch state {
	case "running":
		return ProbeStateRunning, pidDetail
	case "waiting":
		return ProbeStateWaiting, pidDetail
	case "":
		return ProbeStateUnknown, firstLine(output)
	default:
		// Some other state value (e.g. "spawn scheduled"). Report it
		// verbatim via Detail; treat as not-running.
		return ProbeStateWaiting, "state=" + state
	}
}

func extractStateField(loweredOutput string) string {
	for _, line := range strings.Split(loweredOutput, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "state") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		if key != "state" {
			continue
		}
		value := strings.TrimSpace(trimmed[eq+1:])
		value = strings.TrimSuffix(value, ";")
		return strings.TrimSpace(value)
	}
	return ""
}

func extractPidDetail(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		lowerLine := strings.ToLower(trimmed)
		if !strings.HasPrefix(lowerLine, "pid") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(strings.ToLower(trimmed[:eq]))
		if key != "pid" {
			continue
		}
		value := strings.TrimSpace(trimmed[eq+1:])
		value = strings.TrimSuffix(value, ";")
		value = strings.TrimSpace(value)
		if value == "" {
			return ""
		}
		return "pid=" + value
	}
	return ""
}

func firstLine(text string) string {
	trimmed := strings.TrimSpace(text)
	if idx := strings.Index(trimmed, "\n"); idx >= 0 {
		return strings.TrimSpace(trimmed[:idx])
	}
	return trimmed
}
