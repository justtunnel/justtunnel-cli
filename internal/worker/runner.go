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
	"time"

	"nhooyr.io/websocket"
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

// Runner owns the connect-reconnect loop for a single worker. Construct one
// per `worker start` invocation; Run blocks until ctx is done or a terminal
// close code arrives.
type Runner struct {
	WorkerName string
	WorkerID   string
	// Subdomain is the derived host-router subdomain (`<name>` for personal,
	// `<name>--<team-slug>` for team). Computed by DeriveSubdomain so the
	// runner does not need to know context-parsing rules.
	Subdomain string
	ServerURL string
	AuthToken string
	Logger    *slog.Logger
	Dialer    Dialer

	// backoff is the wait-between-attempts function. Nil means use the
	// package-level Backoff (jittered exponential, capped at 60s). Tests
	// override to a near-zero constant.
	backoff func(attempt int) time.Duration
}

// Run drives the connect-read-reconnect loop. It returns:
//   - ctx.Err() when the context is cancelled (graceful shutdown)
//   - *TerminalError when the server closes with 4403/4409
//
// All other failures (dial errors, non-terminal close codes) trigger
// reconnection with no upper bound on attempts — the only way out is
// context cancellation or a terminal close.
func (r *Runner) Run(ctx context.Context) error {
	if r.Dialer == nil {
		return errors.New("worker: runner missing Dialer")
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	backoffFunc := r.backoff
	if backoffFunc == nil {
		backoffFunc = Backoff
	}

	dialURL, err := BuildDialURL(r.ServerURL, r.WorkerID, r.WorkerName, r.Subdomain)
	if err != nil {
		return fmt.Errorf("worker: build dial URL: %w", err)
	}

	for attempt := 1; ; attempt++ {
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
			r.Logger.Info("worker dial failed",
				"worker", r.WorkerName,
				"attempt", attempt,
				"error", dialErr,
			)
			wait := backoffFunc(attempt)
			r.Logger.Info("worker reconnecting",
				"worker", r.WorkerName,
				"in", wait.String(),
				"attempt", attempt+1,
			)
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			continue
		}

		// Successful dial — reset attempt counter so the next disconnect
		// starts the backoff schedule fresh.
		attempt = 0
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

		// Loop continues — `attempt` was reset to 0, so the increment in
		// the for clause makes it 1 for the next backoff calculation.
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

// Backoff computes the wait before the Nth reconnect attempt. The schedule
// is base*2^(attempt-1) with a hard cap at 60s, then ±25% uniform jitter
// to spread thundering-herd reconnects across many workers.
//
// Pure function — safe to call from tests with no setup.
func Backoff(attempt int) time.Duration {
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
	jitterFraction := (rand.Float64() - 0.5) * 0.5 // [-0.25, +0.25]
	jittered := float64(wait) * (1.0 + jitterFraction)
	return time.Duration(jittered)
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
func BuildDialURL(serverURL, workerID, workerName, subdomain string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("worker: parse server URL: %w", err)
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", fmt.Errorf("worker: server URL must be ws:// or wss://, got %q", parsed.Scheme)
	}
	query := parsed.Query()
	query.Set("worker_id", workerID)
	query.Set("worker_name", workerName)
	query.Set("subdomain", subdomain)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

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

// OpenLogFile opens (or creates) the worker's log file in append mode with
// 0600 permissions. The caller owns the returned file and must Close it.
func OpenLogFile(workerName string) (*os.File, error) {
	path, err := LogFilePath(workerName)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("worker: open log file %s: %w", path, err)
	}
	// Defense-in-depth: tighten perms in case the file pre-existed with a
	// looser mode (e.g. created by a buggy earlier version).
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("worker: chmod log file %s: %w", path, err)
	}
	return file, nil
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
			"Authorization": []string{"Bearer " + rd.authToken},
		}
	}
	conn, httpResp, err := websocket.Dial(ctx, dialURL, opts)
	if err != nil {
		// Surface auth failures distinctly so callers can decide whether
		// reconnecting is futile (it is — the token won't change).
		if httpResp != nil && (httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden) {
			return nil, fmt.Errorf("worker: auth failed (%d): %w", httpResp.StatusCode, err)
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
