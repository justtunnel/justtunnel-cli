package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/justtunnel/justtunnel-cli/internal/config"
)

// TerminalError is returned by Runner.Run when the server closes the
// connection with a code that the operator must resolve manually
// (suspended, already attached). The runner does NOT retry on these.
type TerminalError struct {
	Code   int
	Reason string
}

func (te *TerminalError) Error() string {
	return fmt.Sprintf("worker terminated by server: code=%d reason=%q", te.Code, te.Reason)
}

// ErrAuthFailed is returned (wrapped) by a Dialer when the server rejects
// the worker handshake with 401 or 403. The token will not change on its
// own, so Runner.Run treats this as terminal — same semantics as
// TerminalError — to avoid a tight retry loop hammering the server.
var ErrAuthFailed = errors.New("worker: auth failed")

// Terminal close codes per tech spec §7. Both indicate the operator must
// take action — restart the worker won't help.
const (
	closeCodeSuspended       = 4403 // user/team suspended; operator must resolve billing/abuse
	closeCodeAlreadyAttached = 4409 // another worker is already attached for this name
)

// closeCoder is satisfied by anything that exposes a websocket close code.
// The real type is *websocket.CloseError; tests use a small fake to avoid
// dialing real sockets.
type closeCoder interface {
	error
	CloseCode() int
}

// closeCodeFromError extracts a websocket close code from err. Returns 0
// when err is not a recognized close error.
func closeCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	// Real path: nhooyr's helper recognizes its own *CloseError.
	if status := websocket.CloseStatus(err); status != -1 {
		return int(status)
	}
	// Test path: anything implementing closeCoder.
	var coder closeCoder
	if errors.As(err, &coder) {
		return coder.CloseCode()
	}
	return 0
}

// workerConn is the minimum API the runner needs from a live connection.
// Production uses *wsConn (wraps nhooyr.io/websocket); tests use a fake.
type workerConn interface {
	// ReadLoop blocks reading frames until ctx is cancelled or the server
	// closes the connection. The returned error indicates why the loop
	// exited; for a server-initiated close it should wrap a *websocket.CloseError
	// (or a closeCoder in tests).
	ReadLoop(ctx context.Context) error
	// Close releases any underlying socket. Idempotent.
	Close() error
}

// Dialer abstracts the act of opening a worker WebSocket. Production uses
// realDialer; tests inject a fakeDialer to script connect/close behaviors
// without opening real sockets.
type Dialer interface {
	Dial(ctx context.Context, dialURL string) (workerConn, error)
}

// stableConnDuration is how long an attached connection must survive
// before its disconnect "resets" the flap counter. Anything shorter is
// treated as part of an ongoing flap cycle and the next dial is delayed
// by the backoff schedule.
const stableConnDuration = 30 * time.Second

// RunnerIdentity carries the immutable per-worker identity fields. Split
// from RunnerDeps so it's obvious which inputs the operator supplies (a
// worker has a name, an id, a subdomain) versus which are wiring (logger,
// dialer, clock).
type RunnerIdentity struct {
	WorkerName string
	WorkerID   string
	// Subdomain is the derived host-router subdomain (`<name>` for personal,
	// `<name>--<team-slug>` for team). Computed by DeriveSubdomain so the
	// runner does not need to know context-parsing rules.
	Subdomain string
	ServerURL string
}

// RunnerDeps groups the runtime collaborators the runner needs. Both fields
// are required; NewRunner panics if either is nil so misuse is caught at
// construction rather than producing a confusing nil-deref later.
type RunnerDeps struct {
	Logger *slog.Logger
	Dialer Dialer
}

// RunnerOption configures optional behavior on a Runner. Used with NewRunner
// for test knobs (deterministic backoff, fake clock, short stable-conn
// threshold). Production code never needs these — the defaults are correct.
type RunnerOption func(*Runner)

// WithBackoff overrides the wait-between-attempts function. Tests pass a
// near-zero constant so reconnect tests run in milliseconds.
func WithBackoff(fn func(attempt int) time.Duration) RunnerOption {
	return func(r *Runner) { r.backoff = fn }
}

// WithClock overrides the wall-clock used for measuring connection lifetime
// (flap detection). Tests inject a synthetic clock to drive the stable-conn
// threshold without waiting 30s of wall time.
func WithClock(now func() time.Time) RunnerOption {
	return func(r *Runner) { r.now = now }
}

// WithStableConnDuration overrides how long a connection must survive before
// its disconnect resets the flap counter. Tests set a small value to
// exercise reset behavior without long waits.
func WithStableConnDuration(duration time.Duration) RunnerOption {
	return func(r *Runner) { r.stableConnDuration = duration }
}

// Runner owns the connect-reconnect loop for a single worker. Construct via
// NewRunner; the zero value is not usable. Run blocks until ctx is done or
// a terminal close code arrives.
//
// Auth token is intentionally NOT a field: it lives in the Dialer (see
// NewRealDialer) so there's a single source of truth and we don't risk
// passing a stale value via two paths.
//
// Concurrency: a single Runner is owned by exactly one Run call. The
// per-runner *rand.Rand is NOT safe for concurrent use and the backoff
// function reads it without locking.
type Runner struct {
	// Identity / deps — populated by NewRunner from RunnerIdentity / RunnerDeps.
	WorkerName string
	WorkerID   string
	Subdomain  string
	ServerURL  string
	Logger     *slog.Logger
	Dialer     Dialer

	// backoff is the wait-between-attempts function. Defaults to a
	// closure over `rng` that calls backoffWithRand. Tests override via
	// WithBackoff for determinism.
	backoff func(attempt int) time.Duration

	// now is the clock for measuring connection lifetime to detect flaps.
	// Defaults to time.Now. Tests inject a fake clock via WithClock.
	now func() time.Time

	// stableConnDuration overrides the package-level constant. Defaults
	// to stableConnDuration. Tests shrink this via WithStableConnDuration
	// to exercise reset behavior without waiting wall-clock seconds.
	stableConnDuration time.Duration

	// rng is the per-runner random source for backoff jitter. NOT safe
	// for concurrent use — see Concurrency note above. We deliberately
	// avoid the math/rand global to keep test runs deterministic when
	// tests seed their own runners.
	rng *rand.Rand
}

// NewRunner constructs a Runner with all defaults wired. Identity must be
// fully populated; deps must have a non-nil Logger and Dialer. Options apply
// after defaults so a test can override (e.g.) backoff without losing the
// real clock.
func NewRunner(identity RunnerIdentity, deps RunnerDeps, options ...RunnerOption) *Runner {
	if deps.Logger == nil {
		panic("worker.NewRunner: deps.Logger is required")
	}
	if deps.Dialer == nil {
		panic("worker.NewRunner: deps.Dialer is required")
	}
	runner := &Runner{
		WorkerName:         identity.WorkerName,
		WorkerID:           identity.WorkerID,
		Subdomain:          identity.Subdomain,
		ServerURL:          identity.ServerURL,
		Logger:             deps.Logger,
		Dialer:             deps.Dialer,
		now:                time.Now,
		stableConnDuration: stableConnDuration,
		// Seed from current time + worker id hash so concurrent runners
		// (different workers in the same CLI process — uncommon today,
		// possible later) do not produce identical jitter sequences.
		rng: rand.New(rand.NewSource(time.Now().UnixNano() ^ stringHash(identity.WorkerID))),
	}
	runner.backoff = func(attempt int) time.Duration {
		return backoffWithRand(attempt, runner.rng)
	}
	for _, opt := range options {
		opt(runner)
	}
	return runner
}

// stringHash is a tiny, dependency-free non-cryptographic hash used only as
// a per-worker seed offset for the rng. Quality is irrelevant; we just want
// distinct seeds for distinct workers.
func stringHash(input string) int64 {
	var hash int64 = 1469598103934665603 // FNV offset basis
	for index := 0; index < len(input); index++ {
		hash ^= int64(input[index])
		hash *= 1099511628211 // FNV prime
	}
	return hash
}

// Run drives the connect-read-reconnect loop. It returns:
//   - ctx.Err() when the context is cancelled (graceful shutdown)
//   - *TerminalError when the server closes with 4403/4409
//
// All other failures (dial errors, non-terminal close codes) trigger
// reconnection with no upper bound on attempts — the only way out is
// context cancellation or a terminal close.
func (r *Runner) Run(ctx context.Context) error {
	// NewRunner is the supported construction path and pre-validates
	// deps; this guard exists for the legacy struct-literal callers in
	// tests that construct a Runner without Dialer wired.
	if r.Dialer == nil {
		return errors.New("worker: runner missing Dialer")
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.stableConnDuration == 0 {
		r.stableConnDuration = stableConnDuration
	}
	if r.rng == nil {
		r.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if r.backoff == nil {
		r.backoff = func(attempt int) time.Duration {
			return backoffWithRand(attempt, r.rng)
		}
	}
	backoffFunc := r.backoff
	clock := r.now
	stableThreshold := r.stableConnDuration

	dialURL, err := BuildDialURL(r.ServerURL, r.WorkerID, r.WorkerName, r.Subdomain)
	if err != nil {
		return fmt.Errorf("worker: build dial URL: %w", err)
	}

	// dialAttempt tracks consecutive failed dials (network errors).
	// disconnectAttempt tracks consecutive flapping disconnects (a connect
	// that came up then dropped before stableThreshold). Tracking these
	// separately ensures a server that ACCEPTS the handshake then drops it
	// (e.g. 1006) still triggers backoff — the previous implementation
	// reset the attempt counter on any successful dial, which produced a
	// tight reconnect loop on flapping connections.
	dialAttempt := 0
	disconnectAttempt := 0

	for {
		// Fresh context check before any work this iteration.
		if err := ctx.Err(); err != nil {
			return err
		}

		conn, dialErr := r.Dialer.Dial(ctx, dialURL)
		if dialErr != nil {
			// Honor context cancellation that interrupted the dial itself
			// rather than treating it as a transient network error.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// Auth failures are terminal: the token won't change without
			// operator action, so retrying is futile and would hammer the
			// server.
			if errors.Is(dialErr, ErrAuthFailed) {
				r.Logger.Error("worker auth failed (no reconnect)",
					"worker", r.WorkerName,
					"error", dialErr,
				)
				return dialErr
			}
			dialAttempt++
			r.Logger.Info("worker dial failed",
				"worker", r.WorkerName,
				"attempt", dialAttempt,
				"error", dialErr,
			)
			wait := backoffFunc(dialAttempt)
			r.Logger.Info("worker reconnecting",
				"worker", r.WorkerName,
				"in", wait.String(),
				"attempt", dialAttempt+1,
			)
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			continue
		}

		// Dial succeeded. Reset the dial-failure counter; the disconnect
		// counter is governed by how long this connection survives.
		dialAttempt = 0
		attachedAt := clock()
		r.Logger.Info("worker attached",
			"worker", r.WorkerName,
			"subdomain", r.Subdomain,
		)

		readErr := conn.ReadLoop(ctx)
		_ = conn.Close()

		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		closeCode := closeCodeFromError(readErr)
		reason := ""
		if readErr != nil {
			reason = readErr.Error()
		}
		r.Logger.Info("worker disconnected",
			"worker", r.WorkerName,
			"code", closeCode,
			"reason", reason,
		)

		if closeCode == closeCodeSuspended || closeCode == closeCodeAlreadyAttached {
			r.Logger.Error("worker terminated by server (no reconnect)",
				"worker", r.WorkerName,
				"code", closeCode,
				"reason", reason,
			)
			return &TerminalError{Code: closeCode, Reason: reason}
		}

		// Flap detection: a connection that survived stableThreshold is
		// considered "stable" — its disconnect should NOT pay a backoff
		// penalty, because the server proved it can accept us. Reconnect
		// immediately. Anything shorter (a flap) increments the
		// disconnect counter so backoff escalates and we stop hammering
		// the server.
		//
		// B1: previous code reset disconnectAttempt to 0 then immediately
		// incremented, paying Backoff(1) ≈ 1s after every stable
		// disconnect. The fix special-cases stable disconnects to skip
		// backoff entirely; flapping disconnects still escalate.
		if clock().Sub(attachedAt) >= stableThreshold {
			disconnectAttempt = 0
			r.Logger.Info("worker reconnecting after stable disconnect (no backoff)",
				"worker", r.WorkerName,
			)
			continue
		}
		disconnectAttempt++
		wait := backoffFunc(disconnectAttempt)
		r.Logger.Info("worker reconnecting after disconnect",
			"worker", r.WorkerName,
			"in", wait.String(),
			"attempt", disconnectAttempt,
		)
		if err := sleepCtx(ctx, wait); err != nil {
			return err
		}
	}
}

// sleepCtx is a context-aware time.Sleep. Returns ctx.Err() if cancelled
// during the wait.
func sleepCtx(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// backoffMu guards the package-level rng used by the convenience Backoff
// function. Production runners use a per-runner *rand.Rand (see
// backoffWithRand) and never touch this mutex; only direct callers of
// Backoff (tests, ad-hoc code) pay the lock cost.
var (
	backoffMu  sync.Mutex
	backoffRng = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// Backoff computes the wait before the Nth reconnect attempt. The schedule
// is base*2^(attempt-1) with a hard cap at 60s, then ±25% uniform jitter
// to spread thundering-herd reconnects across many workers.
//
// B5: the 60s cap is enforced AFTER jitter so the actual wait NEVER exceeds
// 60s even when jitter rounds up.
//
// Convenience wrapper around backoffWithRand. Production runners pass a
// per-runner rng (see NewRunner) to avoid the global-mutex cost; this
// variant exists for direct test calls and external one-off uses.
func Backoff(attempt int) time.Duration {
	backoffMu.Lock()
	defer backoffMu.Unlock()
	return backoffWithRand(attempt, backoffRng)
}

// backoffWithRand is the pure (modulo rng) backoff implementation. Cap is
// applied AFTER jitter so the returned duration is bounded by maxDelay
// regardless of jitter direction.
func backoffWithRand(attempt int, rng *rand.Rand) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	const baseDelay = time.Second
	const maxDelay = 60 * time.Second
	// Compute 2^(attempt-1) with overflow protection: anything >= 6 saturates
	// the 60s cap before jitter.
	multiplier := 1
	if attempt-1 >= 6 {
		multiplier = 64 // produces >= 60s, will be clamped below
	} else {
		multiplier = 1 << (attempt - 1)
	}
	wait := baseDelay * time.Duration(multiplier)
	if wait > maxDelay {
		wait = maxDelay
	}
	// Uniform jitter in [-25%, +25%]. math/rand is fine here — this is a
	// reconnect spreader, not a security primitive.
	jitterFraction := (rng.Float64() - 0.5) * 0.5 // [-0.25, +0.25]
	jittered := time.Duration(float64(wait) * (1.0 + jitterFraction))
	// Clamp AFTER jitter so the upper bound is honored even when jitter
	// pushes a 60s wait toward 75s.
	if jittered > maxDelay {
		jittered = maxDelay
	}
	if jittered < 0 {
		jittered = 0
	}
	return jittered
}

// DeriveSubdomain returns the host-router subdomain for the given worker
// name + active context. Personal context uses `<name>`; team contexts use
// `<name>--<team-slug>`. Other context shapes are rejected.
//
// The two valid context shapes match config.PersonalContext and
// config.TeamContextPrefix — duplicated here as string literals to keep
// this package free of a `cmd`/`config` import cycle.
func DeriveSubdomain(workerName, contextName string) (string, error) {
	if workerName == "" {
		return "", errors.New("worker: empty worker name")
	}
	if contextName == "" {
		return "", errors.New("worker: empty context")
	}
	const personal = "personal"
	const teamPrefix = "team:"
	switch {
	case contextName == personal:
		return workerName, nil
	case strings.HasPrefix(contextName, teamPrefix):
		slug := strings.TrimPrefix(contextName, teamPrefix)
		if slug == "" {
			return "", fmt.Errorf("worker: team context %q has empty slug", contextName)
		}
		return workerName + "--" + slug, nil
	default:
		return "", fmt.Errorf("worker: unsupported context %q", contextName)
	}
}

// BuildDialURL composes the worker WebSocket handshake URL. The auth token
// is intentionally NOT included — it goes in an Authorization header so it
// doesn't leak into proxy logs.
//
// B4: when the resolved scheme is plaintext ws://, prints a one-line
// warning to plaintextWarnOut so an operator who pinned a cleartext server
// URL realizes the Bearer token is going over the wire unencrypted. The
// dial still proceeds (a CI lab on a private network is a legitimate use
// case); we only warn.
func BuildDialURL(serverURL, workerID, workerName, subdomain string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("worker: parse server URL: %w", err)
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", fmt.Errorf("worker: server URL must be ws:// or wss://, got %q", parsed.Scheme)
	}
	if parsed.Scheme == "ws" {
		fmt.Fprintln(plaintextWarnOut,
			"warning: using plaintext ws:// for worker connection — Bearer token will be sent unencrypted")
	}
	query := parsed.Query()
	query.Set("worker_id", workerID)
	query.Set("worker_name", workerName)
	query.Set("subdomain", subdomain)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// plaintextWarnOut is the destination for the BuildDialURL ws:// warning.
// Defaults to os.Stderr so production operators see it; tests swap it
// (via assignment in the test file) to capture and assert on the text.
var plaintextWarnOut io.Writer = os.Stderr

// LogFilePath returns the path to ~/.justtunnel/logs/worker-<name>.log,
// creating the parent directory with 0700 if missing. The name is validated
// to prevent path traversal — same regex as config files.
func LogFilePath(workerName string) (string, error) {
	if err := validateName(workerName); err != nil {
		return "", err
	}
	root, err := home()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("worker: create logs dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("worker: chmod logs dir: %w", err)
	}
	return filepath.Join(dir, "worker-"+workerName+".log"), nil
}

// OpenLogFile opens the worker's log file as a date-rotating writer. The
// returned writer is mutex-guarded and rotates on date boundaries — see
// RotatingWriter for the full contract. The caller owns the writer and must
// Close it. Production callers pass a nil clock; tests construct
// NewRotatingWriter directly with a synthetic clock.
func OpenLogFile(workerName string) (*RotatingWriter, error) {
	return NewRotatingWriter(workerName, nil)
}

// realDialer is the production Dialer using nhooyr.io/websocket. The
// auth token is set as an Authorization header (NOT a query param) to avoid
// leaking it into intermediary logs.
type realDialer struct {
	authToken string
}

// NewRealDialer returns a Dialer that opens real WebSocket connections.
func NewRealDialer(authToken string) Dialer {
	return &realDialer{authToken: authToken}
}

func (rd *realDialer) Dial(ctx context.Context, dialURL string) (workerConn, error) {
	opts := &websocket.DialOptions{}
	if rd.authToken != "" {
		opts.HTTPHeader = http.Header{
			"Authorization": []string{config.AuthHeaderPrefix + rd.authToken},
		}
	}
	conn, httpResp, err := websocket.Dial(ctx, dialURL, opts)
	if err != nil {
		// Surface auth failures distinctly so callers can decide whether
		// reconnecting is futile (it is — the token won't change).
		if httpResp != nil && (httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden) {
			// Wrap with ErrAuthFailed so Runner.Run can match via
			// errors.Is and exit terminally instead of looping.
			return nil, fmt.Errorf("worker: dial returned %d: %w", httpResp.StatusCode, ErrAuthFailed)
		}
		return nil, fmt.Errorf("worker: dial: %w", err)
	}
	// Reasonable read limit — workers don't receive large frames in v1
	// (control plane only); tighten to 1 MiB to bound memory.
	conn.SetReadLimit(1 << 20)
	return &wsConn{conn: conn}, nil
}

// wsConn adapts *websocket.Conn to the workerConn interface.
type wsConn struct {
	conn *websocket.Conn
}

// ReadLoop drains frames until the connection closes. v1 worker is a
// long-running attach with no client-side response handling, so we discard
// payloads — server-pushed frames will be wired in #32 (status) and beyond.
func (wc *wsConn) ReadLoop(ctx context.Context) error {
	for {
		_, _, err := wc.conn.Read(ctx)
		if err != nil {
			return err
		}
	}
}

func (wc *wsConn) Close() error {
	// StatusNormalClosure is the right code for a client-side graceful
	// shutdown; the server distinguishes this from abnormal disconnects
	// when deciding whether to reschedule.
	return wc.conn.Close(websocket.StatusNormalClosure, "client shutting down")
}

// newDiscardLogger returns a slog.Logger that drops everything. Used by tests
// that don't care about log output.
func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
