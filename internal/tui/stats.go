package tui

import (
	"sync"
	"time"
)

const (
	// maxRecentRequests is the maximum number of request entries kept in the rolling log.
	maxRecentRequests = 50

	// slidingWindowDuration is the time window used to calculate average requests per second.
	slidingWindowDuration = 60 * time.Second
)

// RequestEntry represents a single HTTP request that was forwarded through a tunnel.
type RequestEntry struct {
	Method     string
	Path       string
	StatusCode int
	Duration   time.Duration
	Timestamp  time.Time
}

// RequestStats tracks per-tunnel request statistics with thread-safe access.
// It maintains a rolling log of the last 50 requests and a sliding window
// of timestamps for calculating average requests per second.
type RequestStats struct {
	mu              sync.RWMutex
	totalCount      int64
	recentRequests  []RequestEntry
	windowTimestamps []time.Time
}

// NewRequestStats creates a new RequestStats instance with pre-allocated slices.
func NewRequestStats() *RequestStats {
	return &RequestStats{
		recentRequests:  make([]RequestEntry, 0, maxRecentRequests),
		windowTimestamps: make([]time.Time, 0),
	}
}

// Record adds a new request entry to both the rolling log and sliding window.
// The entry's Timestamp field is used for the sliding window.
func (s *RequestStats) Record(entry RequestEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalCount++

	// Add to rolling log, evicting oldest if at capacity.
	if len(s.recentRequests) >= maxRecentRequests {
		// Shift everything left by one to drop the oldest entry.
		copy(s.recentRequests, s.recentRequests[1:])
		s.recentRequests[len(s.recentRequests)-1] = entry
	} else {
		s.recentRequests = append(s.recentRequests, entry)
	}

	// Add timestamp to sliding window.
	s.windowTimestamps = append(s.windowTimestamps, entry.Timestamp)
}

// RecordWithTimestamp adds a request entry using a specific timestamp for the sliding window.
// This is useful for testing with controlled timestamps.
func (s *RequestStats) RecordWithTimestamp(entry RequestEntry, windowTime time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalCount++

	// Add to rolling log, evicting oldest if at capacity.
	if len(s.recentRequests) >= maxRecentRequests {
		copy(s.recentRequests, s.recentRequests[1:])
		s.recentRequests[len(s.recentRequests)-1] = entry
	} else {
		s.recentRequests = append(s.recentRequests, entry)
	}

	// Add the explicit timestamp to sliding window.
	s.windowTimestamps = append(s.windowTimestamps, windowTime)
}

// TotalCount returns the total number of requests recorded since creation or last reset.
func (s *RequestStats) TotalCount() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalCount
}

// RecentRequests returns a copy of the rolling log sorted newest-first.
// The returned slice is independent of the internal state and safe to mutate.
func (s *RequestStats) RecentRequests() []RequestEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.recentRequests) == 0 {
		return nil
	}

	// Create a copy in reverse order (newest first).
	result := make([]RequestEntry, len(s.recentRequests))
	for idx, entry := range s.recentRequests {
		result[len(s.recentRequests)-1-idx] = entry
	}
	return result
}

// AvgReqPerSec calculates the average requests per second over the sliding window.
// It counts timestamps within the last 60 seconds and divides by 60.
func (s *RequestStats) AvgReqPerSec() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.windowTimestamps) == 0 {
		return 0.0
	}

	cutoff := time.Now().Add(-slidingWindowDuration)
	recentCount := 0
	for _, timestamp := range s.windowTimestamps {
		if !timestamp.Before(cutoff) {
			recentCount++
		}
	}

	return float64(recentCount) / slidingWindowDuration.Seconds()
}

// Reset clears all statistics. Called when a tunnel reconnects with a different subdomain.
func (s *RequestStats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalCount = 0
	s.recentRequests = make([]RequestEntry, 0, maxRecentRequests)
	s.windowTimestamps = make([]time.Time, 0)
}
