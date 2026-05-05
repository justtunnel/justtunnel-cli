//go:build !darwin && !linux

package worker

import "context"

// UnsupportedSupervisor is returned on operating systems where
// JustTunnel does not (yet) integrate with a service supervisor —
// notably Windows. It always reports ServiceBackend="unsupported" so
// `worker status` can still render a row without a special case at the
// call site.
type UnsupportedSupervisor struct{}

// NewSupervisorForOS returns a Supervisor appropriate for the build OS.
// On non-darwin/non-linux platforms it returns an UnsupportedSupervisor.
func NewSupervisorForOS() Supervisor { return &UnsupportedSupervisor{} }

// Probe always returns ServiceBackend="unsupported".
func (s *UnsupportedSupervisor) Probe(ctx context.Context, workerName string) (ProbeResult, error) {
	return ProbeResult{
		ServiceBackend: "unsupported",
		ManagedByUs:    false,
		Running:        false,
		Detail:         "service supervision not supported on this OS",
	}, nil
}
