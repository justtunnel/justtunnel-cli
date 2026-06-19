// Package backoff provides the shared exponential-backoff schedule used by
// every reconnect loop in the CLI (tunnel client and worker runner). Keeping
// one implementation guarantees both paths use the same cap and jitter so a
// server restart does not synchronize reconnects into a thundering herd.
package backoff

import (
	"math/rand"
	"sync"
	"time"
)

// BaseDelay is the wait before the first reconnect attempt; subsequent
// attempts double it until MaxDelay.
const BaseDelay = time.Second

// MaxDelay is the hard cap on the backoff wait, enforced AFTER jitter so the
// returned duration NEVER exceeds it even when jitter rounds upward.
const MaxDelay = 60 * time.Second

// global rng + mutex back the package-level Compute convenience wrapper. Hot
// reconnect loops should hold a per-loop *rand.Rand and call ComputeWithRand
// to avoid this lock; Compute exists for tests and ad-hoc one-off callers.
var (
	globalMu  sync.Mutex
	globalRng = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// Compute returns the wait before the Nth reconnect attempt using a shared,
// mutex-guarded rng. Prefer ComputeWithRand with a per-loop rng in production
// reconnect loops.
func Compute(attempt int) time.Duration {
	globalMu.Lock()
	defer globalMu.Unlock()
	return ComputeWithRand(attempt, globalRng)
}

// ComputeWithRand returns the wait before the Nth reconnect attempt. The
// schedule is BaseDelay*2^(attempt-1) clamped to MaxDelay, then spread by
// ±25% uniform jitter to desynchronize reconnects across many clients. The
// MaxDelay cap is applied AFTER jitter so the result is always in [0, MaxDelay].
//
// attempt < 1 is treated as 1 so a caller off-by-one never produces a
// zero-length spin. rng is not used for anything security-sensitive.
func ComputeWithRand(attempt int, rng *rand.Rand) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// Compute 2^(attempt-1) with overflow protection: attempt-1 >= 6 already
	// saturates the cap, so clamp the multiplier there.
	multiplier := 1
	if attempt-1 >= 6 {
		multiplier = 64 // produces >= MaxDelay, clamped below
	} else {
		multiplier = 1 << (attempt - 1)
	}
	wait := BaseDelay * time.Duration(multiplier)
	if wait > MaxDelay {
		wait = MaxDelay
	}
	// Uniform jitter in [-25%, +25%].
	jitterFraction := (rng.Float64() - 0.5) * 0.5
	jittered := time.Duration(float64(wait) * (1.0 + jitterFraction))
	if jittered > MaxDelay {
		jittered = MaxDelay
	}
	if jittered < 0 {
		jittered = 0
	}
	return jittered
}
