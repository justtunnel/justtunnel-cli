package installer

import (
	"context"
	"fmt"
	"runtime"
)

// ServiceInstaller is the abstraction over the per-OS supervisor installers
// (launchd on darwin, systemd-user on linux). The interface intentionally
// uses BootstrapOptions / SystemdResult as the contract even on darwin so
// callers don't have to branch on OS once they have an installer in hand.
// The launchd adapter ignores the linger fields in opts and returns a zero
// SystemdResult — they are systemd-only concerns.
//
// Unbootstrap is the inverse used by `worker uninstall`: stop the service
// and remove the on-disk service definition. Both per-OS impls treat a
// missing service as a successful no-op so callers can run uninstall
// unconditionally during teardown.
//
// E4: moved from cmd/worker_install.go. Living in the installer package
// means cmd/ no longer carries the per-OS dispatch logic, and adapter
// types live next to the underlying LaunchdInstaller / SystemdInstaller
// they wrap.
type ServiceInstaller interface {
	Bootstrap(ctx context.Context, name string, opts BootstrapOptions) (SystemdResult, error)
	Unbootstrap(ctx context.Context, name string) error
}

// BootstrapOptions carries call-site flags for ServiceInstaller.Bootstrap.
//
// E5: renamed from SystemdOptions to make clear that the type is
// supervisor-agnostic at the cmd-layer interface. The launchd adapter
// ignores all fields; only systemd consumes NoLinger.
type BootstrapOptions = SystemdOptions

// New returns a ServiceInstaller appropriate for the supplied GOOS. Pass
// runtime.GOOS in production; tests can pin "linux" / "darwin" / etc.
// independent of the host. Returns an UnsupportedOSError-shaped error
// for windows or any other unrecognized platform.
func New(goos string) (ServiceInstaller, error) {
	switch goos {
	case "darwin":
		return &launchdAdapter{inner: NewLaunchdInstaller()}, nil
	case "linux":
		return &systemdAdapter{inner: NewSystemdInstaller()}, nil
	default:
		return nil, fmt.Errorf(
			"installer: worker install is not supported on %s; use `worker start <name>` to run in foreground (see docs/windows-recipe.md for Windows alternatives)",
			goos,
		)
	}
}

// NewForCurrentOS is the convenience wrapper around New that uses
// runtime.GOOS. Most production callers want this; tests prefer New
// for OS-pinned dispatch.
func NewForCurrentOS() (ServiceInstaller, error) {
	return New(runtime.GOOS)
}

// launchdAdapter wraps LaunchdInstaller so the launchd path satisfies the
// ServiceInstaller interface (which carries BootstrapOptions / SystemdResult
// so cmd code never branches on OS after dispatch).
type launchdAdapter struct {
	inner *LaunchdInstaller
}

func (adapter *launchdAdapter) Bootstrap(ctx context.Context, name string, _ BootstrapOptions) (SystemdResult, error) {
	if err := adapter.inner.Bootstrap(ctx, name); err != nil {
		return SystemdResult{}, err
	}
	return SystemdResult{}, nil
}

func (adapter *launchdAdapter) Unbootstrap(ctx context.Context, name string) error {
	return adapter.inner.Unbootstrap(ctx, name)
}

// systemdAdapter wraps SystemdInstaller so its existing signature already
// matches ServiceInstaller; the wrapper exists for symmetry with
// launchdAdapter and to keep the factory's switch tidy.
type systemdAdapter struct {
	inner *SystemdInstaller
}

func (adapter *systemdAdapter) Bootstrap(ctx context.Context, name string, opts BootstrapOptions) (SystemdResult, error) {
	return adapter.inner.Bootstrap(ctx, name, opts)
}

func (adapter *systemdAdapter) Unbootstrap(ctx context.Context, name string) error {
	return adapter.inner.Unbootstrap(ctx, name)
}
