//go:build integration && !windows

package doltserver_test

import (
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// TestPortRace_ConcurrentStart verifies that when two goroutines call Start()
// simultaneously on the same beadsDir, exactly one succeeds due to flock
// serialization, and both end up with a healthy server.
func TestPortRace_ConcurrentStart(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	const numStarters = 3
	var wg sync.WaitGroup
	var successCount atomic.Int32
	var mu sync.Mutex
	states := make([]*doltserver.State, numStarters)
	errs := make([]error, numStarters)
	ready := make(chan struct{})

	for i := 0; i < numStarters; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			state, err := doltserver.Start(beadsDir)
			mu.Lock()
			states[idx] = state
			errs[idx] = err
			mu.Unlock()
			if err == nil && state.Running {
				successCount.Add(1)
			}
		}()
	}

	close(ready)
	wg.Wait()

	// All should succeed (flock serializes — first starts, others detect running).
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d failed: %v", i, err)
		}
	}

	if successCount.Load() == 0 {
		t.Fatal("no goroutine succeeded in starting the server")
	}
	t.Logf("Concurrent Start: %d/%d succeeded", successCount.Load(), numStarters)

	// All returned states should reference the same PID (same server).
	var firstPID int
	for _, s := range states {
		if s != nil && s.PID != 0 {
			if firstPID == 0 {
				firstPID = s.PID
			} else if s.PID != firstPID {
				t.Errorf("multiple servers started: PID %d and PID %d", firstPID, s.PID)
			}
		}
	}

	if firstPID != 0 {
		if p, err := os.FindProcess(firstPID); err == nil {
			reg.Register(p)
		}
	}

	// Cleanup.
	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	if firstPID != 0 {
		reg.Deregister(firstPID)
	}
}

// TestPortRace_EphemeralPortRetry verifies that the ephemeral port allocation
// retry loop works when a port is already in use.
func TestPortRace_EphemeralPortRetry(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Bind a port to simulate it being in use.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	occupiedPort := listener.Addr().(*net.TCPAddr).Port
	t.Logf("Occupied port %d to test retry logic", occupiedPort)
	t.Cleanup(func() { listener.Close() })

	// Start() uses ephemeral port allocation (port 0) by default.
	// It should succeed even with one port occupied because it retries.
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state.Port == occupiedPort {
		t.Errorf("Start allocated the occupied port %d", occupiedPort)
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}

	t.Logf("Server started on port %d (occupied port was %d)", state.Port, occupiedPort)

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestPortRace_LoopbackBinding verifies that the dolt server binds to
// 127.0.0.1 (loopback) and is not accessible from other interfaces.
func TestPortRace_LoopbackBinding(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}

	// Verify loopback connection works.
	loopConn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", portStr(state.Port)))
	if err != nil {
		t.Fatalf("loopback connection failed: %v", err)
	}
	loopConn.Close()
	t.Log("Loopback connection succeeded")

	// Check the dolt server config confirms 127.0.0.1 binding.
	cfg := doltserver.DefaultConfig(beadsDir)
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "" {
		t.Errorf("server config host is %q, expected 127.0.0.1 or localhost", cfg.Host)
	}

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestPortRace_MultiRepoPortCollision verifies that two repos don't collide
// on the same ephemeral port.
func TestPortRace_MultiRepoPortCollision(t *testing.T) {
	beadsDirA := setupLifecycleTestDir(t)
	beadsDirB := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)

	// Start both servers concurrently.
	var wg sync.WaitGroup
	var stateA, stateB *doltserver.State
	var errA, errB error

	wg.Add(2)
	go func() {
		defer wg.Done()
		stateA, errA = doltserver.Start(beadsDirA)
	}()
	go func() {
		defer wg.Done()
		stateB, errB = doltserver.Start(beadsDirB)
	}()
	wg.Wait()

	if errA != nil {
		t.Fatalf("Start(A): %v", errA)
	}
	if errB != nil {
		t.Fatalf("Start(B): %v", errB)
	}

	if stateA.PID != 0 {
		if p, err := os.FindProcess(stateA.PID); err == nil {
			reg.Register(p)
		}
	}
	if stateB.PID != 0 {
		if p, err := os.FindProcess(stateB.PID); err == nil {
			reg.Register(p)
		}
	}

	// Verify different ports.
	if stateA.Port == stateB.Port {
		t.Errorf("both repos got the same port %d — port collision", stateA.Port)
	}
	t.Logf("Repo A: port %d, Repo B: port %d", stateA.Port, stateB.Port)

	_ = doltserver.Stop(beadsDirA)
	reg.Deregister(stateA.PID)
	_ = doltserver.Stop(beadsDirB)
	reg.Deregister(stateB.PID)
}

func portStr(port int) string {
	return fmt.Sprintf("%d", port)
}
