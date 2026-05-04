//go:build linux

package worker

import "context"

// LinuxSupervisor probes systemd --user via
// `systemctl --user is-active <unit>`. For #32 this is a stub returning
// ServiceBackend="systemd" and Detail="probe not yet implemented" so
// `worker status` renders without crashing on Linux.
//
// TODO(#35): replace this stub with a real `systemctl --user is-active
// justtunnel-worker-<name>.service` invocation. Map exit 0 to
// Running=true, exit 3 (inactive) to ManagedByUs=true/Running=false,
// and exit 4 (no such unit) to ManagedByUs=false. The unit-name prefix
// `justtunnel-worker-` mirrors the launchd label convention on macOS.
type LinuxSupervisor struct{}

// NewSupervisorForOS returns a Supervisor appropriate for the build OS.
// On linux it returns a LinuxSupervisor.
func NewSupervisorForOS() Supervisor { return &LinuxSupervisor{} }

// Probe returns a stub result. See the package-level TODO(#35).
func (s *LinuxSupervisor) Probe(ctx context.Context, workerName string) (ProbeResult, error) {
	return ProbeResult{
		ServiceBackend: "systemd",
		ManagedByUs:    false,
		Running:        false,
		Detail:         "probe not yet implemented",
	}, nil
}
