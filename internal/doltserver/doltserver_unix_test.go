//go:build !windows

package doltserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestIsProcessAliveTreatsEPERMAsAlive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may signal any process, so there is no EPERM to observe")
	}
	// PID 1 always exists, and an unprivileged user gets EPERM from kill(1, 0).
	if !isProcessAlive(1) {
		t.Error("expected PID 1 to be reported alive")
	}
}

// startStaleServerFiles writes a pid file naming an unrelated process (a
// sleep child, as if the PID had been reused) and a port file. The returned
// channel receives the child's exit.
func startStaleServerFiles(t *testing.T, dir string) chan error {
	t.Helper()
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-exited
	})
	if err := os.WriteFile(pidPath(dir), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(dir, 14599); err != nil {
		t.Fatal(err)
	}
	return exited
}

func expectNotSignaled(t *testing.T, exited chan error) {
	t.Helper()
	select {
	case err := <-exited:
		exited <- err // let the cleanup finish
		t.Errorf("the unrelated process was signaled: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestStopDoesNotSignalUnverifiedProcess(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	orig := readDoltProcesses
	readDoltProcesses = func() ([]int, error) { return nil, errors.New("listing processes: operation not permitted") }
	t.Cleanup(func() { readDoltProcesses = orig })

	err := Stop(dir)
	if err == nil || errors.Is(err, ErrServerNotRunning) {
		t.Errorf("expected Stop to refuse an unverified PID, got %v", err)
	}
	expectNotSignaled(t, exited)
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to be kept: %v", filepath.Base(path), err)
		}
	}
}

func TestStopDoesNotSignalReusedPIDWhenListBecomesReadable(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	// The first read (inside IsRunning) fails, the second (in stopLocked) works
	// and does not contain the PID.
	calls := 0
	orig := readDoltProcesses
	readDoltProcesses = func() ([]int, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("listing processes: resource temporarily unavailable")
		}
		return nil, nil
	}
	t.Cleanup(func() { readDoltProcesses = orig })

	if err := Stop(dir); !errors.Is(err, ErrServerNotRunning) {
		t.Errorf("expected ErrServerNotRunning for a reused PID, got %v", err)
	}
	expectNotSignaled(t, exited)
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", filepath.Base(path))
		}
	}
}

func TestParseDoltProcessPIDs(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		want     []int
	}{
		{
			name:     "ordinary command",
			snapshot: "  101 S /usr/local/bin/dolt sql-server --port 3306\n",
			want:     []int{101},
		},
		{
			name:     "profiled command",
			snapshot: "102 Sl dolt --prof cpu --prof-path /tmp/dolt-pprof sql-server\n",
			want:     []int{102},
		},
		{
			name:     "ordered loose false positive",
			snapshot: "103 S some-tool says dolt then sql-server\n",
			want:     []int{103},
		},
		{
			name:     "sql server before dolt is rejected",
			snapshot: "104 S sql-server then dolt\n",
		},
		{
			name:     "case sensitive command matching",
			snapshot: "105 S Dolt sql-server\n106 S dolt SQL-SERVER\n",
		},
		{
			name:     "zombie and dead states including modifiers are rejected",
			snapshot: "107 Z dolt sql-server\n108 Z+ dolt sql-server\n109 X dolt sql-server\n110 X< dolt sql-server\n",
		},
		{
			name:     "valid state prefix with modifier is accepted",
			snapshot: "111 S+ dolt sql-server\n",
			want:     []int{111},
		},
		{
			name: "malformed and invalid rows are rejected",
			snapshot: "\n" +
				"not-a-pid S dolt sql-server\n" +
				"0 S dolt sql-server\n" +
				"-1 S dolt sql-server\n" +
				"112\n" +
				"113 S\n" +
				"114\tdolt sql-server\n" +
				"115 S \n",
		},
		{
			name:     "preserves source order",
			snapshot: "202 S dolt sql-server\n201 S dolt sql-server\n203 R dolt sql-server\n",
			want:     []int{202, 201, 203},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDoltProcessPIDs([]byte(tt.snapshot)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseDoltProcessPIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}
