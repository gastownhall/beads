package rpc

import (
	"math"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics holds all telemetry data for the daemon
type Metrics struct {
	mu sync.RWMutex

	// Request metrics
	requestCounts  map[string]int64           // operation -> count
	requestErrors  map[string]int64           // operation -> error count
	requestLatency map[string][]time.Duration // operation -> latency samples (bounded slice)
	maxSamples     int

	// Connection metrics
	totalConns    int64
	rejectedConns int64

	// Cache metrics (handled separately via atomic in Server)
	cacheEvictions int64

	// System start time (for uptime calculation)
	startTime time.Time
}

// NewMetrics creates a new metrics collector
func NewMetrics() *Metrics {
	return &Metrics{
		requestCounts:  make(map[string]int64),
		requestErrors:  make(map[string]int64),
		requestLatency: make(map[string][]time.Duration),
		maxSamples:     1000, // Keep last 1000 samples per operation
		startTime:      time.Now(),
	}
}

// RecordRequest records a request (successful or failed)
func (m *Metrics) RecordRequest(operation string, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requestCounts[operation]++

	// Add latency sample to bounded slice
	samples := m.requestLatency[operation]
	if len(samples) >= m.maxSamples {
		// Drop oldest sample to maintain max size
		samples = samples[1:]
	}
	samples = append(samples, latency)
	m.requestLatency[operation] = samples
}

// RecordError records a failed request
func (m *Metrics) RecordError(operation string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requestErrors[operation]++
}

// RecordConnection records a new connection
func (m *Metrics) RecordConnection() {
	atomic.AddInt64(&m.totalConns, 1)
}

// RecordRejectedConnection records a rejected connection (max conns reached)
func (m *Metrics) RecordRejectedConnection() {
	atomic.AddInt64(&m.rejectedConns, 1)
}

// RecordCacheEviction records a cache eviction event
func (m *Metrics) RecordCacheEviction() {
	atomic.AddInt64(&m.cacheEvictions, 1)
}

// Snapshot returns a point-in-time snapshot of all metrics
func (m *Metrics) Snapshot(cacheHits, cacheMisses int64, cacheSize, activeConns int) MetricsSnapshot {
	// Copy data under a short critical section
	m.mu.RLock()

	// Build union of all operations (from both counts and errors)
	opsSet := make(map[string]struct{})
	for op := range m.requestCounts {
		opsSet[op] = struct{}{}
	}
	for op := range m.requestErrors {
		opsSet[op] = struct{}{}
	}

	// Copy counts, errors, and latency slices
	countsCopy := make(map[string]int64, len(opsSet))
	errorsCopy := make(map[string]int64, len(opsSet))
	latCopy := make(map[string][]time.Duration, len(opsSet))

	for op := range opsSet {
		countsCopy[op] = m.requestCounts[op]
		errorsCopy[op] = m.requestErrors[op]
		// Deep copy the latency slice
		if samples := m.requestLatency[op]; len(samples) > 0 {
			latCopy[op] = append([]time.Duration(nil), samples...)
		}
	}

	m.mu.RUnlock()

	// Compute statistics outside the lock
	uptime := time.Since(m.startTime)
	// Round up uptime and enforce minimum of 1 second if any time has passed
	// This prevents flaky tests on fast systems (especially Windows VMs)
	uptimeSeconds := math.Ceil(uptime.Seconds())
	if uptime > 0 && uptimeSeconds == 0 {
		uptimeSeconds = 1
	}

	// Calculate per-operation stats
	operations := make([]OperationMetrics, 0, len(opsSet))
	for op := range opsSet {
		count := countsCopy[op]
		errors := errorsCopy[op]
		samples := latCopy[op]

		// Ensure success count is never negative
		successCount := count - errors
		if successCount < 0 {
			successCount = 0
		}

		opMetrics := OperationMetrics{
			Operation:    op,
			TotalCount:   count,
			ErrorCount:   errors,
			SuccessCount: successCount,
		}

		// Calculate latency percentiles if we have samples
		if len(samples) > 0 {
			opMetrics.Latency = calculateLatencyStats(samples)
		}

		operations = append(operations, opMetrics)
	}

	// Sort by total count (most frequent first)
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].TotalCount > operations[j].TotalCount
	})

	// Get memory stats
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return MetricsSnapshot{
		Timestamp:      time.Now(),
		UptimeSeconds:  uptimeSeconds,
		Operations:     operations,
		CacheHits:      cacheHits,
		CacheMisses:    cacheMisses,
		CacheSize:      cacheSize,
		CacheEvictions: atomic.LoadInt64(&m.cacheEvictions),
		TotalConns:     atomic.LoadInt64(&m.totalConns),
		ActiveConns:    activeConns,
		RejectedConns:  atomic.LoadInt64(&m.rejectedConns),
		MemoryAllocMB:  memStats.Alloc / 1024 / 1024,
		MemorySysMB:    memStats.Sys / 1024 / 1024,
		GoroutineCount: runtime.NumGoroutine(),
	}
}

// MetricsSnapshot is a point-in-time view of all metrics
type MetricsSnapshot struct {
	Timestamp      time.Time          `json:"timestamp"`
	UptimeSeconds  float64            `json:"uptime_seconds"`
	Operations     []OperationMetrics `json:"operations"`
	CacheHits      int64              `json:"cache_hits"`
	CacheMisses    int64              `json:"cache_misses"`
	CacheSize      int                `json:"cache_size"`
	CacheEvictions int64              `json:"cache_evictions"`
	TotalConns     int64              `json:"total_connections"`
	ActiveConns    int                `json:"active_connections"`
	RejectedConns  int64              `json:"rejected_connections"`
	MemoryAllocMB  uint64             `json:"memory_alloc_mb"`
	MemorySysMB    uint64             `json:"memory_sys_mb"`
	GoroutineCount int                `json:"goroutine_count"`
}

// OperationMetrics holds metrics for a single operation type
type OperationMetrics struct {
	Operation    string       `json:"operation"`
	TotalCount   int64        `json:"total_count"`
	SuccessCount int64        `json:"success_count"`
	ErrorCount   int64        `json:"error_count"`
	Latency      LatencyStats `json:"latency,omitempty"`
}

// LatencyStats holds latency percentile data in milliseconds
type LatencyStats struct {
	MinMS float64 `json:"min_ms"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
	AvgMS float64 `json:"avg_ms"`
}

// calculateLatencyStats computes percentiles from latency samples and returns milliseconds
func calculateLatencyStats(samples []time.Duration) LatencyStats {
	if len(samples) == 0 {
		return LatencyStats{}
	}

	// Sort samples
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	n := len(sorted)
	// Calculate percentiles with defensive clamping
	p50Idx := minInt(n-1, n*50/100)
	p95Idx := minInt(n-1, n*95/100)
	p99Idx := minInt(n-1, n*99/100)

	// Calculate average
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	avg := sum / time.Duration(n)

	// Convert to milliseconds
	toMS := func(d time.Duration) float64 {
		return float64(d) / float64(time.Millisecond)
	}

	return LatencyStats{
		MinMS: toMS(sorted[0]),
		P50MS: toMS(sorted[p50Idx]),
		P95MS: toMS(sorted[p95Idx]),
		P99MS: toMS(sorted[p99Idx]),
		MaxMS: toMS(sorted[n-1]),
		AvgMS: toMS(avg),
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
