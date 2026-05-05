package worker

import "context"

// Supervisor probes the local OS service supervisor (launchd on macOS,
// systemd --user on Linux) to determine whether a worker tunnel is being
// managed by us and whether it is currently running.
//
// The interface exists so `justtunnel worker status` can render a unified
// table covering BOTH server-side state and local supervisor state without
// shelling out from tests. Per-OS implementations live in
// supervisor_darwin.go, supervisor_linux.go, and supervisor_other.go;
// each returns a stub for #32 and is filled in by #34 (launchd) and
// #35 (systemd --user).
type Supervisor interface {
	// Probe returns the current managed state of the worker. The error
	// return is non-nil only if the probe itself failed (e.g. the
	// supervisor binary is missing); a worker that the supervisor
	// reports as "stopped" or "unknown" is NOT an error.
	Probe(ctx context.Context, workerName string) (ProbeResult, error)
}

// ProbeResult is the per-worker observation returned by a Supervisor.
type ProbeResult struct {
	// ServiceBackend identifies which supervisor was probed.
	// One of: "launchd" | "systemd" | "none" | "unsupported".
	ServiceBackend string
	// ManagedByUs is true when the supervisor has a unit/agent loaded
	// for this worker name. False both when nothing is installed and
	// when the local config records ServiceBackend == "none".
	ManagedByUs bool
	// Running is true when the supervisor reports the worker process
	// as currently running. Meaningless when ManagedByUs is false.
	Running bool
	// Detail is an optional human-readable suffix (e.g. PID, exit
	// reason, "probe not yet implemented") used when rendering detail
	// view.
	Detail string
}
