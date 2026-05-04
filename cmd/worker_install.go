package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/display"
	"github.com/justtunnel/justtunnel-cli/internal/worker"
	"github.com/justtunnel/justtunnel-cli/internal/worker/installer"
)

// serviceInstaller is the abstraction over the per-OS supervisor installers
// (launchd on darwin, systemd-user on linux). The interface intentionally
// uses installer.SystemdOptions / installer.SystemdResult as the contract
// even on darwin so the cmd layer doesn't have to branch on OS once it has
// an installer in hand. The launchd adapter ignores the linger fields in
// opts and returns a zero SystemdResult — they are systemd-only concerns.
//
// Unbootstrap is the inverse used by `worker uninstall` (#37): stop the
// service and remove the on-disk service definition. Both per-OS impls
// treat a missing service as a successful no-op so callers can run
// uninstall unconditionally during teardown.
type serviceInstaller interface {
	Bootstrap(ctx context.Context, name string, opts installer.SystemdOptions) (installer.SystemdResult, error)
	Unbootstrap(ctx context.Context, name string) error
}

// newServiceInstaller is the per-OS factory for serviceInstaller. It is a
// package-level variable so tests can swap in a fake without exercising the
// real launchctl/systemctl shell-outs. Production callers leave it pointing
// at defaultNewServiceInstaller.
var newServiceInstaller = defaultNewServiceInstaller

// launchdAdapter wraps installer.LaunchdInstaller so the launchd path
// satisfies the serviceInstaller interface (which carries SystemdOptions /
// SystemdResult so cmd code never branches on OS after dispatch).
type launchdAdapter struct {
	inner *installer.LaunchdInstaller
}

func (adapter *launchdAdapter) Bootstrap(ctx context.Context, name string, _ installer.SystemdOptions) (installer.SystemdResult, error) {
	if err := adapter.inner.Bootstrap(ctx, name); err != nil {
		return installer.SystemdResult{}, err
	}
	return installer.SystemdResult{}, nil
}

func (adapter *launchdAdapter) Unbootstrap(ctx context.Context, name string) error {
	return adapter.inner.Unbootstrap(ctx, name)
}

// systemdAdapter wraps installer.SystemdInstaller so its existing signature
// already matches serviceInstaller; the wrapper exists only for symmetry
// with launchdAdapter and to keep the factory's switch tidy.
type systemdAdapter struct {
	inner *installer.SystemdInstaller
}

func (adapter *systemdAdapter) Bootstrap(ctx context.Context, name string, opts installer.SystemdOptions) (installer.SystemdResult, error) {
	return adapter.inner.Bootstrap(ctx, name, opts)
}

func (adapter *systemdAdapter) Unbootstrap(ctx context.Context, name string) error {
	return adapter.inner.Unbootstrap(ctx, name)
}

// defaultNewServiceInstaller dispatches by GOOS. windows (and any other
// platform) returns the canonical "not supported" error.
func defaultNewServiceInstaller(goos string) (serviceInstaller, error) {
	switch goos {
	case "darwin":
		return &launchdAdapter{inner: installer.NewLaunchdInstaller()}, nil
	case "linux":
		return &systemdAdapter{inner: installer.NewSystemdInstaller()}, nil
	default:
		return nil, unsupportedOSError(goos)
	}
}

// unsupportedOSError is the canonical "this platform has no supervisor
// adapter" error. Centralized so the test suite and the production
// dispatcher emit byte-identical messages.
func unsupportedOSError(goos string) error {
	return display.InputError(fmt.Sprintf(
		"worker install is not supported on %s; use `worker start <name>` to run in foreground (see docs/windows-recipe.md for Windows alternatives)",
		goos,
	))
}

// workerInstallNoLinger is the --no-linger flag (linux only; ignored on
// darwin). Declared at package scope to match cobra's flag-binding model;
// reset in tests via t.Cleanup.
var workerInstallNoLinger bool

// workerInstallNonInteractive is the --non-interactive flag. On linux it
// has the same effect as --no-linger (skip the user-linger consent
// prompt; worker becomes session-bound). On darwin it has no effect
// (launchd-user agents survive logout natively); we emit a stderr
// warning when the flag is set on darwin to avoid silently misleading
// scripted callers. Either flag set OR'd produces NoLinger=true.
var workerInstallNonInteractive bool

var workerInstallCmd = &cobra.Command{
	Use:   "install <name>",
	Short: "Install a worker as a managed background service (one-liner)",
	Long: "Registers <name> on the server (idempotent), persists local config, and installs\n" +
		"the platform-native service definition (launchd on macOS, systemd-user on Linux).\n" +
		"Re-running on a configured worker is a no-op except for the supervisor refresh.\n\n" +
		"Windows is not supported; use `worker start <name>` to run in the foreground.",
	Args: cobra.ExactArgs(1),
	RunE: runWorkerInstall,
}

func init() {
	workerInstallCmd.Flags().BoolVar(&workerInstallNoLinger, "no-linger", false,
		"(linux) skip the systemd user-linger consent prompt; worker becomes session-bound")
	workerInstallCmd.Flags().BoolVar(&workerInstallNonInteractive, "non-interactive", false,
		"never prompt; on linux equivalent to --no-linger; on macOS has no effect")
	workerCmd.AddCommand(workerInstallCmd)
}

func runWorkerInstall(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := worker.ValidateName(name); err != nil {
		return display.InputError(err.Error())
	}

	// OS dispatch FIRST: failing fast on Windows means we never make a
	// pointless server round-trip just to surface a friendly error.
	svc, err := newServiceInstaller(runtime.GOOS)
	if err != nil {
		return err
	}

	cfg, teamID, ctxName, baseURL, err := loadWorkerEnv()
	if err != nil {
		return err
	}

	// C6: pre-validate the derived subdomain length so a 64+ char
	// `<name>--<slug>` never reaches the server. teamID is the slug.
	if err := worker.ValidateDerivedSubdomain(name, teamID); err != nil {
		return err
	}

	// Reconcile local + server state. The four idempotency modes are
	// collapsed into a single ensureWorkerRegistered call; it returns the
	// authoritative worker record (whichever side already had it) and a
	// flag indicating whether a POST was issued (for the success summary).
	record, err := ensureWorkerRegistered(baseURL, cfg.AuthToken, teamID, ctxName, name, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	// On macOS, --no-linger and --non-interactive are no-ops because
	// launchd-user agents already survive logout natively. We surface a
	// stderr warning rather than silently ignoring the flag so scripted
	// callers can see the misuse without breaking stdout-keyed parsers.
	if runtime.GOOS == "darwin" {
		if workerInstallNoLinger {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"warning: --no-linger has no effect on macOS (launchd-user agents survive logout natively)")
		}
		if workerInstallNonInteractive {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"warning: --non-interactive has no effect on macOS (launchd-user agents survive logout natively)")
		}
	}

	// Bootstrap the supervisor. On Bootstrap failure we deliberately do
	// NOT roll back the server-side worker — the operator can re-run
	// `worker install` (idempotent) once they've fixed the underlying
	// platform issue, or `worker uninstall` to fully tear down. Auto-
	// rolling back here would make the failure mode worse for the common
	// case where the issue is local (e.g. launchctl already in a wedged
	// state) and a retry would succeed.
	//
	// NoLinger is the OR of --no-linger and --non-interactive: either
	// flag means the operator opted out of the user-linger consent
	// prompt. The systemd installer treats NoLinger=true as
	// session-bound (worker dies on logout) — same semantics either way.
	opts := installer.SystemdOptions{NoLinger: workerInstallNoLinger || workerInstallNonInteractive}
	bootstrapResult, err := svc.Bootstrap(cmd.Context(), name, opts)
	if err != nil {
		return fmt.Errorf("install: bootstrap supervisor for %q failed; retry with `justtunnel worker install %s` or `justtunnel worker uninstall %s` to roll back: %w",
			name, name, name, err)
	}

	// A3: ensure ServiceBackend reflects the supervisor that just took
	// ownership. If the local config was written earlier with "none" (e.g.
	// from `worker create` followed by `worker install`), persist the
	// authoritative backend so `worker status` / `worker list` render
	// correctly. Best-effort: a write failure here is non-fatal because
	// the supervisor is already installed and the user-visible install
	// step succeeded.
	expectedBackend := serviceBackendForOS(runtime.GOOS)
	if record.ServiceBackend != expectedBackend {
		updated := *record
		updated.ServiceBackend = expectedBackend
		if writeErr := worker.Write(&updated); writeErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: could not update local ServiceBackend to %q: %v\n",
				expectedBackend, writeErr)
		} else {
			record = &updated
		}
	}

	// A1: spec §6.4 — when linger was NOT enabled (--no-linger, deny at
	// prompt, or a non-fatal failure to enable), print the verbatim
	// "linger denied" notice to stdout so the operator sees how to enable
	// persistence later.
	if bootstrapResult.ShouldPrintLingerDeniedNotice {
		fmt.Fprint(cmd.OutOrStdout(), installer.LingerDeniedNotice(name))
	}

	urlStr, urlErr := workerURL(cfg.ServerURL, record.Subdomain)
	if urlErr != nil {
		// URL derivation is best-effort; a malformed server URL should
		// not fail the install. Print what we know and surface the
		// derivation issue on stderr so it's visible without breaking
		// scripted callers that key on stdout.
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not derive worker URL (%v)\n", urlErr)
		urlStr = "(URL unavailable; check `justtunnel worker status`)"
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Installed worker %q (id=%s) in %s\n  url: %s\n",
		record.Name, record.WorkerID, ctxName, urlStr,
	)
	return nil
}

// ensureWorkerRegistered reconciles the four idempotency modes from the
// plan:
//  1. local present, server present → no POST, no local rewrite
//  2. local present, server missing → POST, rewrite local
//  3. local missing, server present → hydrate local from server
//  4. neither present                → POST, write local
//
// Returns the worker record that should be used for the success summary
// (the one that genuinely lives on disk after this call returns).
func ensureWorkerRegistered(baseURL, authToken, teamID, ctxName, name string, warnOut io.Writer) (*worker.Config, error) {
	localCfg, localErr := worker.Read(name)
	hasLocal := localErr == nil
	if localErr != nil && !errors.Is(localErr, os.ErrNotExist) {
		// Read failed for a reason OTHER than "not found" (e.g.
		// permission denied, malformed JSON). Surface it so the
		// operator can investigate before we make any state changes.
		return nil, fmt.Errorf("read local worker config: %w", localErr)
	}

	servers, fetchErr := fetchWorkers(baseURL, authToken, teamID)
	if fetchErr != nil {
		return nil, fmt.Errorf("list workers: %w", fetchErr)
	}
	var serverRecord *workerAPI
	for index := range servers {
		if servers[index].Name == name {
			serverRecord = &servers[index]
			break
		}
	}

	switch {
	case hasLocal && serverRecord != nil:
		// Mode 1: nothing to do at the registration layer. The
		// supervisor Bootstrap below is independently idempotent.
		return localCfg, nil

	case hasLocal && serverRecord == nil:
		// Mode 2: local has a stale ID. Re-create on server and
		// overwrite local. Reuse worker_create's create+rollback
		// pattern: if local Write fails after server POST, DELETE
		// the just-created server record.
		//
		// NOTE: we deliberately do NOT attempt to delete a stale
		// server-side worker that may have a different ID. Without
		// auth-time identity verification, attempting cleanup risks
		// deleting an unrelated worker (e.g. one a teammate created
		// under a colliding name in a separate session). The orphan
		// record (if any) will be reaped server-side via the
		// retention/quarantine reaper. This is an intentional
		// trade-off: leak-prefer over potential cross-user damage.
		fmt.Fprintf(warnOut,
			"note: local config exists for %q but server has no record; re-creating on server\n", name)
		return createServerSideAndPersist(baseURL, authToken, teamID, ctxName, name, warnOut)

	case !hasLocal && serverRecord != nil:
		// Mode 3: hydrate local from server. No POST needed.
		fmt.Fprintf(warnOut,
			"note: hydrating local config for %q from existing server record\n", name)
		hydrated := workerAPIToConfig(serverRecord, ctxName, warnOut)
		if writeErr := worker.Write(hydrated); writeErr != nil {
			return nil, fmt.Errorf("hydrate local config from server: %w", writeErr)
		}
		return hydrated, nil

	default:
		// Mode 4: clean install.
		return createServerSideAndPersist(baseURL, authToken, teamID, ctxName, name, warnOut)
	}
}

// createServerSideAndPersist POSTs the worker, writes local config, and on
// local-write failure rolls back the server-side create. Used by both
// `worker create` (which writes ServiceBackend="none" — bare-create, no
// supervisor yet) and `worker install` Mode 2/4 (which leaves "none" so the
// caller can later overwrite with the platform supervisor name once
// Bootstrap succeeds — see runWorkerInstall for the post-install update).
//
// We deliberately persist "none" here rather than serviceBackendForOS so a
// crash AFTER write but BEFORE Bootstrap leaves the state truthful: there
// is no supervisor managing this worker yet.
func createServerSideAndPersist(baseURL, authToken, teamID, ctxName, name string, warnOut io.Writer) (*worker.Config, error) {
	created, err := postWorker(baseURL, authToken, teamID, name)
	if err != nil {
		return nil, err
	}
	createdAt, parseErr := time.Parse(time.RFC3339, created.CreatedAt)
	if parseErr != nil {
		// Mirror worker_create.go: surface server-side timestamp
		// regressions on stderr instead of silently substituting
		// time.Now(). Local config is a CLI-side convenience, not a
		// source of truth, so we still proceed.
		fmt.Fprintf(warnOut,
			"warning: server returned unparseable created_at %q; using current time\n",
			created.CreatedAt,
		)
		createdAt = time.Now().UTC()
	}
	subdomain := created.Subdomain
	if derived, derr := worker.DeriveSubdomain(created.Name, ctxName); derr == nil {
		if created.Subdomain != "" && created.Subdomain != derived {
			fmt.Fprintf(warnOut,
				"warning: server-reported subdomain %q for %q disagrees with locally-derived %q; using local value\n",
				created.Subdomain, created.Name, derived,
			)
		}
		subdomain = derived
	}
	cfg := &worker.Config{
		WorkerID:       created.ID,
		Name:           created.Name,
		Context:        ctxName,
		Subdomain:      subdomain,
		CreatedAt:      createdAt,
		ServiceBackend: "none",
	}
	if writeErr := worker.Write(cfg); writeErr != nil {
		_, deleteErr := deleteWorker(baseURL, authToken, teamID, created.ID)
		if deleteErr != nil {
			return nil, fmt.Errorf(
				"WARNING: server-side worker %q (id %s) created but local config write failed AND rollback also failed.\n"+
					"Run `justtunnel worker rm %s --delete-on-server` to clean up before retrying create.\n"+
					"  local write error: %v\n"+
					"  rollback error:    %v",
				created.Name, created.ID, created.Name, writeErr, deleteErr,
			)
		}
		return nil, fmt.Errorf("local config write failed; rolled back server-side create — please retry: %w", writeErr)
	}
	return cfg, nil
}

// workerAPIToConfig copies the server's view of a worker into the on-disk
// schema. Used by the "hydrate local from server" idempotency branch.
//
// A4: subdomain verification. If the server's reported subdomain disagrees
// with the locally-derived value (DeriveSubdomain(name, ctxName)), warn on
// stderr AND prefer the locally-derived value in the persisted config. The
// CLI considers the local derivation authoritative because subdomain shape
// is owned by the worker package's documented rules (`<name>` for personal,
// `<name>--<slug>` for team) — a server that reports a different value is
// either rolling out a new format or is buggy, and either way we'd rather
// match what the runner will dial.
func workerAPIToConfig(api *workerAPI, ctxName string, warnOut io.Writer) *worker.Config {
	createdAt, parseErr := time.Parse(time.RFC3339, api.CreatedAt)
	if parseErr != nil {
		if warnOut != nil {
			fmt.Fprintf(warnOut,
				"warning: server returned unparseable created_at %q; using current time\n",
				api.CreatedAt,
			)
		}
		createdAt = time.Now().UTC()
	}
	subdomain := api.Subdomain
	if derived, derr := worker.DeriveSubdomain(api.Name, ctxName); derr == nil {
		if api.Subdomain != "" && api.Subdomain != derived && warnOut != nil {
			fmt.Fprintf(warnOut,
				"warning: server-reported subdomain %q for %q disagrees with locally-derived %q; using local value\n",
				api.Subdomain, api.Name, derived,
			)
		}
		subdomain = derived
	}
	return &worker.Config{
		WorkerID:       api.ID,
		Name:           api.Name,
		Context:        ctxName,
		Subdomain:      subdomain,
		CreatedAt:      createdAt,
		ServiceBackend: serviceBackendForOS(runtime.GOOS),
	}
}

// serviceBackendForOS returns the canonical service-backend label persisted
// in worker.Config so `worker list` / `worker status` can render which
// supervisor manages the worker.
func serviceBackendForOS(goos string) string {
	switch goos {
	case "darwin":
		return "launchd"
	case "linux":
		return "systemd"
	default:
		return "none"
	}
}

// workerURL derives the public URL for a worker subdomain from the
// configured server URL. The transformation is:
//
//   - Convert ws/wss → http/https.
//   - If the host begins with "api." AND the URL carries no port, strip
//     the "api." prefix and prepend "<subdomain>.".
//     e.g. wss://api.justtunnel.dev/ws + "build--acme" → https://build--acme.justtunnel.dev
//   - Otherwise (localhost, custom domain, "api."-prefixed host with an
//     explicit port like dev/staging splits, or any non-standard host),
//     fall back to "<server>/<subdomain>" — less polished but always
//     meaningful and unambiguous.
//
// Note: the "api." strip ONLY kicks in when no port is present.
// Production hosts are bare; "api.example.com:8443" indicates a
// dev/staging environment where the operator deliberately pinned a
// port, and rewriting `api.example.com:8443` to
// `<sub>.example.com:8443` would silently lose that signal. We instead
// keep the original host and append "/<subdomain>" so the result still
// resolves something the operator can click through to.
//
// This lives in cmd because it's a CLI display concern, not a server URL
// rule. If the URL shape ever moves into the worker config (e.g. as
// served by GET /api/teams/{id}/workers), prefer the server's value over
// re-derivation here.
func workerURL(serverURL, subdomain string) (string, error) {
	if subdomain == "" {
		return "", errors.New("empty subdomain")
	}
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "wss":
		parsed.Scheme = "https"
	case "ws":
		parsed.Scheme = "http"
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	// C1: strip userinfo (e.g. wss://user:pass@host) so the rendered
	// worker URL never carries credentials. The CLI prints this URL to
	// stdout; embedding userinfo in operator-visible output would leak
	// the secret into terminal scrollback and CI logs.
	parsed.User = nil

	host := parsed.Host
	// Only transform when host has the literal "api." prefix AND port is
	// empty — production hosts are bare. localhost:8080 has a port and
	// would never carry the api. prefix anyway, so this gate keeps the
	// "fall back to /<sub>" branch simple.
	if strings.HasPrefix(host, "api.") && parsed.Port() == "" {
		baseDomain := strings.TrimPrefix(host, "api.")
		parsed.Host = subdomain + "." + baseDomain
		return parsed.String(), nil
	}
	// Fallback: append /<subdomain> to the server URL. Documented as a
	// less-polished form for non-standard server URLs.
	parsed.Path = "/" + subdomain
	return parsed.String(), nil
}
