//go:build darwin

package worker

import "context"

// DarwinSupervisor probes launchd via `launchctl print user/<UID>/<label>`.
// For #32 this is a stub: it returns ServiceBackend="launchd" and
// Detail="probe not yet implemented" so `worker status` renders without
// crashing on macOS.
//
// TODO(#34): replace this stub with a real `launchctl print
// user/<UID>/dev.justtunnel.worker.<name>` invocation. Parse the output
// for `state = running` (Running=true) and treat exit code 113
// ("Could not find specified service") as ManagedByUs=false. The label
// prefix `dev.justtunnel.worker.` is the convention chosen for this
// project; mirror it in the systemd unit name on Linux.
type DarwinSupervisor struct{}

// NewSupervisorForOS returns a Supervisor appropriate for the build OS.
// On darwin it returns a DarwinSupervisor.
func NewSupervisorForOS() Supervisor { return &DarwinSupervisor{} }

// Probe returns a stub result. See the package-level TODO(#34).
func (s *DarwinSupervisor) Probe(ctx context.Context, workerName string) (ProbeResult, error) {
	return ProbeResult{
		ServiceBackend: "launchd",
		ManagedByUs:    false,
		Running:        false,
		Detail:         "probe not yet implemented",
	}, nil
}
