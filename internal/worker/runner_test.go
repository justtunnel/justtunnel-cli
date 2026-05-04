package worker

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestBackoffSchedule verifies the exponential backoff schedule: 1s base,
// doubling per attempt, capped at 60s, with jitter staying within ±25%.
func TestBackoffSchedule(t *testing.T) {
	tests := []struct {
		attempt int
		minBase time.Duration // expected base before jitter
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second}, // 64s capped to 60s
		{8, 60 * time.Second},
		{15, 60 * time.Second},
		{50, 60 * time.Second}, // far past cap, no overflow
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tc.attempt), func(t *testing.T) {
			// Sample many times to exercise the jitter range.
			low := time.Duration(float64(tc.minBase) * 0.75)
			high := time.Duration(float64(tc.minBase) * 1.25)
			for i := 0; i < 50; i++ {
				got := Backoff(tc.attempt)
				if got < low || got > high {
					t.Fatalf("Backoff(%d) = %s; want within [%s, %s]",
						tc.attempt, got, low, high)
				}
			}
		})
	}
}

// TestBackoff_NonPositiveAttempt clamps attempts <= 0 to a 1s base so a
// programmer error in the caller doesn't produce a zero-length spin.
func TestBackoff_NonPositiveAttempt(t *testing.T) {
	for _, attempt := range []int{0, -1, -100} {
		got := Backoff(attempt)
		if got < 750*time.Millisecond || got > 1250*time.Millisecond {
			t.Fatalf("Backoff(%d) = %s; want clamped to ~1s±25%%", attempt, got)
		}
	}
}

// fakeDialer simulates a transport without opening real sockets. Each
// dial pulls the next behavior from `script`.
type fakeDialer struct {
	script   []dialResult
	attempts int32
}

type dialResult struct {
	// dialErr, if non-nil, fails the dial itself.
	dialErr error
	// closeErr is what readLoop returns to simulate the server closing the
	// session after a connection succeeds. Use a *CloseError-like sentinel
	// (use `closeStatus` to encode the code).
	closeErr error
	// blockUntilCancel: if true, the read loop blocks until ctx is cancelled.
	blockUntilCancel bool
}

type fakeConn struct {
	result dialResult
}

func (fc *fakeConn) ReadLoop(ctx context.Context) error {
	if fc.result.blockUntilCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	if fc.result.closeErr != nil {
		return fc.result.closeErr
	}
	return errors.New("read loop ended without error")
}

func (fc *fakeConn) Close() error { return nil }

func (fd *fakeDialer) Dial(ctx context.Context, dialURL string) (workerConn, error) {
	idx := int(atomic.AddInt32(&fd.attempts, 1)) - 1
	if idx >= len(fd.script) {
		// After script ends, block forever (so the runner is held up until
		// the test cancels the context).
		return &fakeConn{result: dialResult{blockUntilCancel: true}}, nil
	}
	step := fd.script[idx]
	if step.dialErr != nil {
		return nil, step.dialErr
	}
	return &fakeConn{result: step}, nil
}

// TestRunner_ReconnectsAfterTransientFailures verifies that dial errors and
// non-terminal close codes drive a reconnect, and that the runner exits
// cleanly when the context is cancelled.
func TestRunner_ReconnectsAfterTransientFailures(t *testing.T) {
	dialer := &fakeDialer{
		script: []dialResult{
			{dialErr: errors.New("network unreachable")},
			{dialErr: errors.New("connection refused")},
			// Connect succeeds, server closes with 1006 (abnormal) — must reconnect.
			{closeErr: &fakeCloseError{code: 1006, reason: "abnormal"}},
			// Then succeed and block until ctx cancel.
			{blockUntilCancel: true},
		},
	}
	runner := newTestRunner(dialer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// Wait for the runner to reach the blocking-success state.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&dialer.attempts) >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&dialer.attempts); got < 4 {
		t.Fatalf("expected at least 4 dial attempts; got %d", got)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of context cancel")
	}
}

// TestRunner_TerminalCloseSuspended verifies that close code 4403 (suspended)
// causes the runner to exit non-zero with no reconnect.
func TestRunner_TerminalCloseSuspended(t *testing.T) {
	dialer := &fakeDialer{
		script: []dialResult{
			{closeErr: &fakeCloseError{code: 4403, reason: "suspended"}},
		},
	}
	runner := newTestRunner(dialer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := runner.Run(ctx)
	if err == nil {
		t.Fatal("Run returned nil; want terminal error")
	}
	var terminal *TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("Run returned %v; want *TerminalError", err)
	}
	if terminal.Code != 4403 {
		t.Fatalf("TerminalError.Code = %d; want 4403", terminal.Code)
	}
	if got := atomic.LoadInt32(&dialer.attempts); got != 1 {
		t.Fatalf("expected exactly 1 dial attempt for terminal close; got %d", got)
	}
}

// TestRunner_TerminalCloseAlreadyAttached verifies that 4409 (already attached)
// is also terminal.
func TestRunner_TerminalCloseAlreadyAttached(t *testing.T) {
	dialer := &fakeDialer{
		script: []dialResult{
			{closeErr: &fakeCloseError{code: 4409, reason: "already attached"}},
		},
	}
	runner := newTestRunner(dialer)
	err := runner.Run(context.Background())
	var terminal *TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("Run returned %v; want *TerminalError", err)
	}
	if terminal.Code != 4409 {
		t.Fatalf("TerminalError.Code = %d; want 4409", terminal.Code)
	}
}

// TestRunner_CancelDuringBackoff verifies that cancelling the context while
// the runner is sleeping between reconnect attempts exits cleanly. Uses a
// channel-based sync (instead of time.Sleep) so the test is deterministic
// under race-detector load.
func TestRunner_CancelDuringBackoff(t *testing.T) {
	dialer := &fakeDialer{
		script: []dialResult{
			{dialErr: errors.New("fail 1")},
			{dialErr: errors.New("fail 2")},
			{dialErr: errors.New("fail 3")},
		},
	}
	runner := newTestRunner(dialer)
	// Signal the moment the runner enters backoff sleep so we cancel
	// AFTER it's actually sleeping. Buffered + sync.Once-style guard so
	// later backoff calls don't panic on a closed channel.
	inBackoff := make(chan struct{}, 1)
	runner.backoff = func(attempt int) time.Duration {
		select {
		case inBackoff <- struct{}{}:
		default:
		}
		return 1 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// Wait deterministically for the runner to enter backoff sleep.
	select {
	case <-inBackoff:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not enter backoff within 2s")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of cancel during backoff")
	}
}

// TestDeriveSubdomain enforces the personal vs team naming rules. Personal
// context is `<name>`; team context is `<name>--<slug>`.
func TestDeriveSubdomain(t *testing.T) {
	tests := []struct {
		name        string
		workerName  string
		contextName string
		want        string
	}{
		{"personal", "build", "personal", "build"},
		{"team", "build", "team:acme", "build--acme"},
		{"team_dashed_name", "ci-runner", "team:my-org", "ci-runner--my-org"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveSubdomain(tc.workerName, tc.contextName)
			if err != nil {
				t.Fatalf("DeriveSubdomain returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DeriveSubdomain(%q,%q) = %q; want %q",
					tc.workerName, tc.contextName, got, tc.want)
			}
		})
	}
}

// TestDeriveSubdomain_InvalidContext rejects malformed contexts so a corrupt
// per-worker config can't silently produce a wrong subdomain.
func TestDeriveSubdomain_InvalidContext(t *testing.T) {
	if _, err := DeriveSubdomain("build", ""); err == nil {
		t.Fatal("expected error for empty context")
	}
	if _, err := DeriveSubdomain("build", "team:"); err == nil {
		t.Fatal("expected error for empty team slug")
	}
	if _, err := DeriveSubdomain("build", "weird:foo"); err == nil {
		t.Fatal("expected error for unknown context prefix")
	}
}

// TestBuildDialURL verifies the worker WS handshake URL shape and that the
// auth token is not embedded in the URL (it goes in a header).
func TestBuildDialURL(t *testing.T) {
	got, err := BuildDialURL("wss://api.example.com/ws", "wkr_123", "build", "build--acme")
	if err != nil {
		t.Fatalf("BuildDialURL returned error: %v", err)
	}
	want := "wss://api.example.com/ws?subdomain=build--acme&worker_id=wkr_123&worker_name=build"
	if got != want {
		t.Fatalf("BuildDialURL = %q; want %q", got, want)
	}
}

// fakeCloseError implements the closeCoder interface so the runner can
// extract the status code without taking a hard dependency on the websocket
// package in tests.
type fakeCloseError struct {
	code   int
	reason string
}

func (fce *fakeCloseError) Error() string {
	return fmt.Sprintf("close %d: %s", fce.code, fce.reason)
}

func (fce *fakeCloseError) CloseCode() int { return fce.code }

// newTestRunner builds a Runner wired to a fake dialer with deterministic,
// near-zero backoff so reconnect tests run in milliseconds.
func newTestRunner(dialer Dialer) *Runner {
	return &Runner{
		WorkerName: "build",
		WorkerID:   "wkr_123",
		Subdomain:  "build--acme",
		ServerURL:  "wss://api.example.com/ws",
		Logger:     newDiscardLogger(),
		Dialer:     dialer,
		backoff:    func(attempt int) time.Duration { return time.Millisecond },
	}
}

// TestRunner_AuthFailureIsTerminal verifies that a Dialer returning an
// ErrAuthFailed-wrapped error causes Runner.Run to exit immediately with
// no retry. This guards against the previous behavior of looping forever
// on a 401/403 — the token won't change without operator action, so
// retrying just hammers the server.
func TestRunner_AuthFailureIsTerminal(t *testing.T) {
	dialer := &fakeDialer{
		script: []dialResult{
			{dialErr: fmt.Errorf("worker: dial returned 401: %w", ErrAuthFailed)},
		},
	}
	runner := newTestRunner(dialer)

	err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil; want ErrAuthFailed")
	}
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("Run returned %v; want errors.Is(err, ErrAuthFailed)", err)
	}
	if got := atomic.LoadInt32(&dialer.attempts); got != 1 {
		t.Fatalf("expected exactly 1 dial attempt for auth failure; got %d", got)
	}
}

// TestRunner_AuthFailureForbidden is the 403 sibling of the 401 test —
// same expectation: terminate immediately.
func TestRunner_AuthFailureForbidden(t *testing.T) {
	dialer := &fakeDialer{
		script: []dialResult{
			{dialErr: fmt.Errorf("worker: dial returned 403: %w", ErrAuthFailed)},
		},
	}
	runner := newTestRunner(dialer)
	err := runner.Run(context.Background())
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("Run returned %v; want errors.Is(err, ErrAuthFailed)", err)
	}
	if got := atomic.LoadInt32(&dialer.attempts); got != 1 {
		t.Fatalf("expected exactly 1 dial attempt; got %d", got)
	}
}

// TestRunner_FlapBackoffBetweenServerDisconnects verifies that when the
// server accepts the handshake but immediately closes (1006), the runner
// SLEEPS the backoff before reconnecting — it does not tight-loop.
//
// Regression test for the bug where Run reset attempt=0 on every successful
// dial, causing flapping connections to spin at thousands of dials/sec.
//
// Asserts wall-clock gap between dial #1 and dial #2 is at least 0.5s
// (1s backoff with 25% jitter floor = 0.75s; 0.5s is a safe lower bound).
func TestRunner_FlapBackoffBetweenServerDisconnects(t *testing.T) {
	dialTimes := make(chan time.Time, 4)
	dialer := &recordingDialer{
		dialTimes: dialTimes,
		script: []dialResult{
			{closeErr: &fakeCloseError{code: 1006, reason: "abnormal"}},
			{closeErr: &fakeCloseError{code: 1006, reason: "abnormal"}},
			{blockUntilCancel: true},
		},
	}
	runner := newTestRunner(dialer)
	// Real (non-instant) backoff so we can measure the gap.
	runner.backoff = func(attempt int) time.Duration { return 1 * time.Second }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// Wait for the first two dials.
	var firstDial, secondDial time.Time
	select {
	case firstDial = <-dialTimes:
	case <-time.After(2 * time.Second):
		t.Fatal("first dial did not happen within 2s")
	}
	select {
	case secondDial = <-dialTimes:
	case <-time.After(3 * time.Second):
		t.Fatal("second dial did not happen within 3s")
	}

	gap := secondDial.Sub(firstDial)
	if gap < 500*time.Millisecond {
		t.Fatalf("flap backoff gap = %s; want >= 500ms (server-disconnect must trigger backoff before reconnect)", gap)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of cancel")
	}
}

// TestRunner_MixedCloseCodeSequence verifies the most common real-world
// failure mode: a transient 1006 followed by a terminal 4403. The runner
// must reconnect once after 1006 and then exit cleanly with TerminalError
// on 4403 — no further dials.
func TestRunner_MixedCloseCodeSequence(t *testing.T) {
	dialer := &fakeDialer{
		script: []dialResult{
			{closeErr: &fakeCloseError{code: 1006, reason: "abnormal"}},
			{closeErr: &fakeCloseError{code: 4403, reason: "suspended"}},
		},
	}
	runner := newTestRunner(dialer)

	err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil; want *TerminalError")
	}
	var terminal *TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("Run returned %v; want *TerminalError", err)
	}
	if terminal.Code != 4403 {
		t.Fatalf("TerminalError.Code = %d; want 4403", terminal.Code)
	}
	if got := atomic.LoadInt32(&dialer.attempts); got != 2 {
		t.Fatalf("expected exactly 2 dial attempts (1006 then 4403); got %d", got)
	}
}

// TestRunner_SecondCancelDuringTeardown verifies that a second context
// cancel arriving while the runner is shutting down does not deadlock —
// Run still returns context.Canceled. This mirrors a SIGINT-then-SIGINT
// sequence in production: the first signal flips the context, the second
// arrives during the read-loop unwind. Without correct context plumbing
// the second cancel could race with conn.Close() and stall.
func TestRunner_SecondCancelDuringTeardown(t *testing.T) {
	dialer := &fakeDialer{
		script: []dialResult{
			{blockUntilCancel: true},
		},
	}
	runner := newTestRunner(dialer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// Wait for the runner to actually be attached and blocking.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&dialer.attempts) >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// First cancel — graceful.
	cancel()
	// Second cancel — must be a no-op, not a deadlock or panic.
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s after double-cancel")
	}
}

// recordingDialer is a fakeDialer variant that timestamps each Dial call
// so flap-backoff tests can measure the wall-clock gap between attempts.
type recordingDialer struct {
	script    []dialResult
	attempts  int32
	dialTimes chan<- time.Time
}

func (rd *recordingDialer) Dial(ctx context.Context, dialURL string) (workerConn, error) {
	idx := int(atomic.AddInt32(&rd.attempts, 1)) - 1
	select {
	case rd.dialTimes <- time.Now():
	default:
	}
	if idx >= len(rd.script) {
		return &fakeConn{result: dialResult{blockUntilCancel: true}}, nil
	}
	step := rd.script[idx]
	if step.dialErr != nil {
		return nil, step.dialErr
	}
	return &fakeConn{result: step}, nil
}
