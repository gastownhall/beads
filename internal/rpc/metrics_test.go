package rpc

import (
	"testing"
	"time"
)

func TestMetricsRecording(t *testing.T) {
	m := NewMetrics()

	t.Run("record request", func(t *testing.T) {
		m.RecordRequest("create", 10*time.Millisecond)
		m.RecordRequest("create", 20*time.Millisecond)

		m.mu.RLock()
		count := m.requestCounts["create"]
		m.mu.RUnlock()

		if count != 2 {
			t.Errorf("Expected 2 requests, got %d", count)
		}
	})

	t.Run("record error", func(t *testing.T) {
		m.RecordError("create")

		m.mu.RLock()
		errors := m.requestErrors["create"]
		m.mu.RUnlock()

		if errors != 1 {
			t.Errorf("Expected 1 error, got %d", errors)
		}
	})

	t.Run("record connection", func(t *testing.T) {
		before := m.totalConns
		m.RecordConnection()
		after := m.totalConns

		if after != before+1 {
			t.Errorf("Expected connection count to increase by 1, got %d -> %d", before, after)
		}
	})

	t.Run("record rejected connection", func(t *testing.T) {
		before := m.rejectedConns
		m.RecordRejectedConnection()
		after := m.rejectedConns

		if after != before+1 {
			t.Errorf("Expected rejected count to increase by 1, got %d -> %d", before, after)
		}
	})

	t.Run("record cache eviction", func(t *testing.T) {
		before := m.cacheEvictions
		m.RecordCacheEviction()
		after := m.cacheEvictions

		if after != before+1 {
			t.Errorf("Expected eviction count to increase by 1, got %d -> %d", before, after)
		}
	})
}

func TestMetricsSnapshot(t *testing.T) {
	m := NewMetrics()

	// Record some operations
	m.RecordRequest("create", 10*time.Millisecond)
	m.RecordRequest("create", 20*time.Millisecond)
	m.RecordRequest("update", 5*time.Millisecond)
	m.RecordError("create")
	m.RecordConnection()
	m.RecordRejectedConnection()
	m.RecordCacheEviction()

	// Take snapshot
	snapshot := m.Snapshot(100, 10, 50, 3)

	t.Run("basic metrics", func(t *testing.T) {
		if snapshot.TotalConns < 1 {
			t.Error("Expected at least 1 total connection")
		}
		if snapshot.RejectedConns < 1 {
			t.Error("Expected at least 1 rejected connection")
		}
		if snapshot.CacheEvictions < 1 {
			t.Error("Expected at least 1 cache eviction")
		}
		if snapshot.CacheHits != 100 {
			t.Errorf("Expected 100 cache hits, got %d", snapshot.CacheHits)
		}
		if snapshot.CacheMisses != 10 {
			t.Errorf("Expected 10 cache misses, got %d", snapshot.CacheMisses)
		}
		if snapshot.CacheSize != 50 {
			t.Errorf("Expected cache size 50, got %d", snapshot.CacheSize)
		}
		if snapshot.ActiveConns != 3 {
			t.Errorf("Expected 3 active connections, got %d", snapshot.ActiveConns)
		}
	})

	t.Run("operation metrics", func(t *testing.T) {
		if len(snapshot.Operations) != 2 {
			t.Errorf("Expected 2 operations, got %d", len(snapshot.Operations))
		}

		// Find create operation
		var createOp *OperationMetrics
		for i := range snapshot.Operations {
			if snapshot.Operations[i].Operation == "create" {
				createOp = &snapshot.Operations[i]
				break
			}
		}

		if createOp == nil {
			t.Fatal("Expected to find 'create' operation")
		}

		if createOp.TotalCount != 2 {
			t.Errorf("Expected 2 total creates, got %d", createOp.TotalCount)
		}
		if createOp.ErrorCount != 1 {
			t.Errorf("Expected 1 error, got %d", createOp.ErrorCount)
		}
		if createOp.SuccessCount != 1 {
			t.Errorf("Expected 1 success, got %d", createOp.SuccessCount)
		}
	})

	t.Run("latency stats", func(t *testing.T) {
		var createOp *OperationMetrics
		for i := range snapshot.Operations {
			if snapshot.Operations[i].Operation == "create" {
				createOp = &snapshot.Operations[i]
				break
			}
		}

		if createOp == nil {
			t.Fatal("Expected to find 'create' operation")
		}

		// Should have latency stats
		if createOp.Latency.MinMS <= 0 {
			t.Error("Expected non-zero min latency")
		}
		if createOp.Latency.MaxMS <= 0 {
			t.Error("Expected non-zero max latency")
		}
		if createOp.Latency.AvgMS <= 0 {
			t.Error("Expected non-zero avg latency")
		}
	})

	t.Run("uptime", func(t *testing.T) {
		if snapshot.UptimeSeconds <= 0 {
			t.Error("Expected positive uptime")
		}
	})

	t.Run("memory stats", func(t *testing.T) {
		if snapshot.MemoryAllocMB == 0 {
			t.Error("Expected non-zero memory allocation")
		}
		if snapshot.GoroutineCount == 0 {
			t.Error("Expected non-zero goroutine count")
		}
	})
}

func TestCalculateLatencyStats(t *testing.T) {
	t.Run("empty samples", func(t *testing.T) {
		stats := calculateLatencyStats([]time.Duration{})
		if stats.MinMS != 0 || stats.MaxMS != 0 {
			t.Error("Expected zero stats for empty samples")
		}
	})

	t.Run("single sample", func(t *testing.T) {
		samples := []time.Duration{10 * time.Millisecond}
		stats := calculateLatencyStats(samples)

		if stats.MinMS != 10.0 {
			t.Errorf("Expected min 10ms, got %f", stats.MinMS)
		}
		if stats.MaxMS != 10.0 {
			t.Errorf("Expected max 10ms, got %f", stats.MaxMS)
		}
		if stats.AvgMS != 10.0 {
			t.Errorf("Expected avg 10ms, got %f", stats.AvgMS)
		}
	})

	t.Run("multiple samples", func(t *testing.T) {
		samples := []time.Duration{
			5 * time.Millisecond,
			10 * time.Millisecond,
			15 * time.Millisecond,
			20 * time.Millisecond,
			100 * time.Millisecond,
		}
		stats := calculateLatencyStats(samples)

		if stats.MinMS != 5.0 {
			t.Errorf("Expected min 5ms, got %f", stats.MinMS)
		}
		if stats.MaxMS != 100.0 {
			t.Errorf("Expected max 100ms, got %f", stats.MaxMS)
		}
		if stats.AvgMS != 30.0 {
			t.Errorf("Expected avg 30ms, got %f", stats.AvgMS)
		}
		// P50 should be around 15ms (middle value)
		if stats.P50MS < 10.0 || stats.P50MS > 20.0 {
			t.Errorf("Expected P50 around 15ms, got %f", stats.P50MS)
		}
	})
}

func TestLatencySampleBounding(t *testing.T) {
	m := NewMetrics()
	m.maxSamples = 10 // Small size for testing

	// Add more samples than max
	for i := 0; i < 20; i++ {
		m.RecordRequest("test", time.Duration(i)*time.Millisecond)
	}

	m.mu.RLock()
	samples := m.requestLatency["test"]
	m.mu.RUnlock()

	if len(samples) != 10 {
		t.Errorf("Expected 10 samples (bounded), got %d", len(samples))
	}

	// Verify oldest samples were dropped (should have newest 10)
	expectedMin := 10 * time.Millisecond
	if samples[0] != expectedMin {
		t.Errorf("Expected oldest sample to be %v, got %v", expectedMin, samples[0])
	}
}

func TestMinHelper(t *testing.T) {
	if min(5, 10) != 5 {
		t.Error("min(5, 10) should be 5")
	}
	if min(10, 5) != 5 {
		t.Error("min(10, 5) should be 5")
	}
	if min(7, 7) != 7 {
		t.Error("min(7, 7) should be 7")
	}
}
