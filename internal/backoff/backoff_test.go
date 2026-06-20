package backoff

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// TestComputeSchedule verifies the exponential schedule (1s base, doubling)
// capped at 60s, with every sample staying inside the ±25% jitter band.
func TestComputeSchedule(t *testing.T) {
	tests := []struct {
		attempt int
		base    time.Duration // expected base before jitter
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second}, // 64s capped to 60s
		{8, 60 * time.Second},
		{50, 60 * time.Second}, // far past cap, no overflow
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tc.attempt), func(t *testing.T) {
			low := time.Duration(float64(tc.base) * 0.75)
			high := time.Duration(float64(tc.base) * 1.25)
			for sample := 0; sample < 100; sample++ {
				got := Compute(tc.attempt)
				if got < low || got > high {
					t.Fatalf("Compute(%d) = %s; want within [%s, %s]",
						tc.attempt, got, low, high)
				}
			}
		})
	}
}

// TestComputeNonPositiveAttempt clamps attempts <= 0 to the 1s base so a
// caller off-by-one never produces a zero-length spin.
func TestComputeNonPositiveAttempt(t *testing.T) {
	for _, attempt := range []int{0, -1, -100} {
		got := Compute(attempt)
		if got < 750*time.Millisecond || got > 1250*time.Millisecond {
			t.Fatalf("Compute(%d) = %s; want clamped to ~1s±25%%", attempt, got)
		}
	}
}

// TestComputeRespectsCapAfterJitter guards that the MaxDelay cap holds even
// when jitter rounds upward — the result is always in [0, MaxDelay].
func TestComputeRespectsCapAfterJitter(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for sample := 0; sample < 5000; sample++ {
		got := ComputeWithRand(100, rng)
		if got > MaxDelay {
			t.Fatalf("ComputeWithRand(100) = %s; exceeds %s cap (sample %d)", got, MaxDelay, sample)
		}
		if got < 0 {
			t.Fatalf("ComputeWithRand(100) = %s; negative (sample %d)", got, sample)
		}
	}
}
