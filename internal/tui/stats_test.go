package tui

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestRequestStatsRecord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		recordCount    int
		wantTotal      int64
		wantRecentLen  int
	}{
		{
			name:          "record 5 requests returns 5 entries",
			recordCount:   5,
			wantTotal:     5,
			wantRecentLen: 5,
		},
		{
			name:          "record 50 requests returns 50 entries at capacity",
			recordCount:   50,
			wantTotal:     50,
			wantRecentLen: 50,
		},
		{
			name:          "record 51 requests caps rolling log at 50",
			recordCount:   51,
			wantTotal:     51,
			wantRecentLen: 50,
		},
		{
			name:          "record 100 requests still caps at 50",
			recordCount:   100,
			wantTotal:     100,
			wantRecentLen: 50,
		},
		{
			name:          "record 0 requests returns empty",
			recordCount:   0,
			wantTotal:     0,
			wantRecentLen: 0,
		},
		{
			name:          "record 1 request returns 1 entry",
			recordCount:   1,
			wantTotal:     1,
			wantRecentLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stats := NewRequestStats()

			for idx := range tt.recordCount {
				stats.Record(RequestEntry{
					Method:     "GET",
					Path:       "/test",
					StatusCode: 200,
					Duration:   time.Duration(idx) * time.Millisecond,
					Timestamp:  time.Now(),
				})
			}

			if stats.TotalCount() != tt.wantTotal {
				t.Errorf("TotalCount() = %d, want %d", stats.TotalCount(), tt.wantTotal)
			}

			recent := stats.RecentRequests()
			if len(recent) != tt.wantRecentLen {
				t.Errorf("len(RecentRequests()) = %d, want %d", len(recent), tt.wantRecentLen)
			}
		})
	}
}

func TestRecentRequestsNewestFirst(t *testing.T) {
	t.Parallel()

	stats := NewRequestStats()

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for idx := range 5 {
		stats.Record(RequestEntry{
			Method:     "GET",
			Path:       "/test",
			StatusCode: 200,
			Duration:   10 * time.Millisecond,
			Timestamp:  baseTime.Add(time.Duration(idx) * time.Second),
		})
	}

	recent := stats.RecentRequests()
	if len(recent) != 5 {
		t.Fatalf("len(RecentRequests()) = %d, want 5", len(recent))
	}

	// Verify newest-first ordering
	for idx := 1; idx < len(recent); idx++ {
		if recent[idx].Timestamp.After(recent[idx-1].Timestamp) {
			t.Errorf("entry %d timestamp (%v) is after entry %d timestamp (%v); expected newest-first",
				idx, recent[idx].Timestamp, idx-1, recent[idx-1].Timestamp)
		}
	}

	// The newest entry should have the latest timestamp
	expectedNewest := baseTime.Add(4 * time.Second)
	if !recent[0].Timestamp.Equal(expectedNewest) {
		t.Errorf("newest entry timestamp = %v, want %v", recent[0].Timestamp, expectedNewest)
	}
}

func TestRollingLogEvictsOldest(t *testing.T) {
	t.Parallel()

	stats := NewRequestStats()

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Record 51 requests with sequential paths so we can identify them
	for idx := range 51 {
		path := "/old"
		if idx >= 1 {
			path = "/new"
		}
		if idx == 0 {
			path = "/oldest"
		}
		stats.Record(RequestEntry{
			Method:     "GET",
			Path:       path,
			StatusCode: 200,
			Duration:   10 * time.Millisecond,
			Timestamp:  baseTime.Add(time.Duration(idx) * time.Second),
		})
	}

	recent := stats.RecentRequests()
	if len(recent) != 50 {
		t.Fatalf("len(RecentRequests()) = %d, want 50", len(recent))
	}

	// The oldest entry (/oldest at idx 0) should have been evicted
	for _, entry := range recent {
		if entry.Path == "/oldest" {
			t.Error("oldest entry should have been evicted but was found in RecentRequests()")
		}
	}
}

func TestRecentRequestsReturnsCopy(t *testing.T) {
	t.Parallel()

	stats := NewRequestStats()

	stats.Record(RequestEntry{
		Method:     "GET",
		Path:       "/original",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
		Timestamp:  time.Now(),
	})

	// Get a copy and mutate it
	recent := stats.RecentRequests()
	recent[0].Path = "/mutated"

	// Original should be unchanged
	original := stats.RecentRequests()
	if original[0].Path != "/original" {
		t.Errorf("RecentRequests() returned a reference, not a copy; path = %q, want /original", original[0].Path)
	}
}

func TestAvgReqPerSecSlidingWindow(t *testing.T) {
	t.Parallel()

	t.Run("no requests returns 0", func(t *testing.T) {
		t.Parallel()
		stats := NewRequestStats()

		avg := stats.AvgReqPerSec()
		if avg != 0.0 {
			t.Errorf("AvgReqPerSec() = %f, want 0.0", avg)
		}
	})

	t.Run("10 requests in last 10 seconds", func(t *testing.T) {
		t.Parallel()
		stats := NewRequestStats()
		now := time.Now()

		// Record 10 requests spread over the last 10 seconds
		for idx := range 10 {
			stats.RecordWithTimestamp(RequestEntry{
				Method:     "GET",
				Path:       "/test",
				StatusCode: 200,
				Duration:   10 * time.Millisecond,
				Timestamp:  now.Add(-time.Duration(idx) * time.Second),
			}, now.Add(-time.Duration(idx)*time.Second))
		}

		// 10 requests in 60 seconds = ~0.167 req/sec
		avg := stats.AvgReqPerSec()
		expected := 10.0 / 60.0
		if math.Abs(avg-expected) > 0.1 {
			t.Errorf("AvgReqPerSec() = %f, want ~%f (within 0.1)", avg, expected)
		}
	})

	t.Run("only counts requests within 60 second window", func(t *testing.T) {
		t.Parallel()
		stats := NewRequestStats()
		now := time.Now()

		// Record 50 requests 120 seconds ago (outside window)
		for range 50 {
			stats.RecordWithTimestamp(RequestEntry{
				Method:     "GET",
				Path:       "/old",
				StatusCode: 200,
				Duration:   10 * time.Millisecond,
				Timestamp:  now.Add(-120 * time.Second),
			}, now.Add(-120*time.Second))
		}

		// Record 10 requests within the last 10 seconds (inside window)
		for idx := range 10 {
			stats.RecordWithTimestamp(RequestEntry{
				Method:     "GET",
				Path:       "/recent",
				StatusCode: 200,
				Duration:   10 * time.Millisecond,
				Timestamp:  now.Add(-time.Duration(idx) * time.Second),
			}, now.Add(-time.Duration(idx)*time.Second))
		}

		// Should only count the 10 recent requests: 10/60 ≈ 0.167
		avg := stats.AvgReqPerSec()
		expected := 10.0 / 60.0
		if math.Abs(avg-expected) > 0.1 {
			t.Errorf("AvgReqPerSec() = %f, want ~%f (within 0.1); old requests should be excluded", avg, expected)
		}
	})

	t.Run("60 requests over 60 seconds", func(t *testing.T) {
		t.Parallel()
		stats := NewRequestStats()
		now := time.Now()

		// Record 60 requests, one per second over the last 60 seconds
		for idx := range 60 {
			stats.RecordWithTimestamp(RequestEntry{
				Method:     "GET",
				Path:       "/test",
				StatusCode: 200,
				Duration:   10 * time.Millisecond,
				Timestamp:  now.Add(-time.Duration(idx) * time.Second),
			}, now.Add(-time.Duration(idx)*time.Second))
		}

		// 60 requests in 60 seconds = 1.0 req/sec
		avg := stats.AvgReqPerSec()
		if math.Abs(avg-1.0) > 0.1 {
			t.Errorf("AvgReqPerSec() = %f, want ~1.0 (within 0.1)", avg)
		}
	})
}

func TestReset(t *testing.T) {
	t.Parallel()

	stats := NewRequestStats()

	// Record some requests
	for idx := range 10 {
		stats.Record(RequestEntry{
			Method:     "GET",
			Path:       "/test",
			StatusCode: 200,
			Duration:   time.Duration(idx) * time.Millisecond,
			Timestamp:  time.Now(),
		})
	}

	// Verify data exists before reset
	if stats.TotalCount() != 10 {
		t.Fatalf("TotalCount() before reset = %d, want 10", stats.TotalCount())
	}

	stats.Reset()

	if stats.TotalCount() != 0 {
		t.Errorf("TotalCount() after reset = %d, want 0", stats.TotalCount())
	}

	recent := stats.RecentRequests()
	if len(recent) != 0 {
		t.Errorf("len(RecentRequests()) after reset = %d, want 0", len(recent))
	}

	avg := stats.AvgReqPerSec()
	if avg != 0.0 {
		t.Errorf("AvgReqPerSec() after reset = %f, want 0.0", avg)
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	stats := NewRequestStats()
	var waitGroup sync.WaitGroup

	// Spawn multiple goroutines doing concurrent Record() calls
	numWriters := 10
	writesPerGoroutine := 100
	waitGroup.Add(numWriters)

	for writerIdx := range numWriters {
		go func(workerID int) {
			defer waitGroup.Done()
			for reqIdx := range writesPerGoroutine {
				stats.Record(RequestEntry{
					Method:     "GET",
					Path:       "/concurrent",
					StatusCode: 200,
					Duration:   time.Duration(reqIdx) * time.Millisecond,
					Timestamp:  time.Now(),
				})
				_ = workerID // used to differentiate goroutines
			}
		}(writerIdx)
	}

	// Spawn readers concurrently
	numReaders := 5
	waitGroup.Add(numReaders)
	for range numReaders {
		go func() {
			defer waitGroup.Done()
			for range 50 {
				_ = stats.TotalCount()
				_ = stats.RecentRequests()
				_ = stats.AvgReqPerSec()
			}
		}()
	}

	waitGroup.Wait()

	expectedTotal := int64(numWriters * writesPerGoroutine)
	if stats.TotalCount() != expectedTotal {
		t.Errorf("TotalCount() = %d, want %d after concurrent writes", stats.TotalCount(), expectedTotal)
	}
}

func TestTotalCountIncrements(t *testing.T) {
	t.Parallel()

	stats := NewRequestStats()

	for idx := range 200 {
		stats.Record(RequestEntry{
			Method:     "POST",
			Path:       "/submit",
			StatusCode: 201,
			Duration:   time.Duration(idx) * time.Millisecond,
			Timestamp:  time.Now(),
		})

		if stats.TotalCount() != int64(idx+1) {
			t.Errorf("TotalCount() after %d records = %d, want %d", idx+1, stats.TotalCount(), idx+1)
		}
	}

	// Total should be 200 even though rolling log is capped at 50
	if stats.TotalCount() != 200 {
		t.Errorf("final TotalCount() = %d, want 200", stats.TotalCount())
	}
}

func TestRequestEntryFieldsPreserved(t *testing.T) {
	t.Parallel()

	stats := NewRequestStats()
	recorded := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

	stats.Record(RequestEntry{
		Method:     "POST",
		Path:       "/api/tunnels",
		StatusCode: 201,
		Duration:   42 * time.Millisecond,
		Timestamp:  recorded,
	})

	recent := stats.RecentRequests()
	if len(recent) != 1 {
		t.Fatalf("len(RecentRequests()) = %d, want 1", len(recent))
	}

	entry := recent[0]
	if entry.Method != "POST" {
		t.Errorf("Method = %q, want POST", entry.Method)
	}
	if entry.Path != "/api/tunnels" {
		t.Errorf("Path = %q, want /api/tunnels", entry.Path)
	}
	if entry.StatusCode != 201 {
		t.Errorf("StatusCode = %d, want 201", entry.StatusCode)
	}
	if entry.Duration != 42*time.Millisecond {
		t.Errorf("Duration = %v, want 42ms", entry.Duration)
	}
	if !entry.Timestamp.Equal(recorded) {
		t.Errorf("Timestamp = %v, want %v", entry.Timestamp, recorded)
	}
}
