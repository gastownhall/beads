//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// PR_SET_CHILD_SUBREAPER is Linux's prctl operation 36. Keeping the value
	// local avoids making the census depend on a generated x/sys constant.
	prSetChildSubreaper = 36

	historicalPipeWaitDelay      = 250 * time.Millisecond
	historicalContainmentTimeout = 2 * time.Second
	historicalQuiescenceWindow   = 50 * time.Millisecond
	historicalCleanupPoll        = 10 * time.Millisecond
)

var (
	historicalSubreaperOnce sync.Once
	historicalSubreaperErr  error
	historicalCommandMu     sync.Mutex
)

// enableHistoricalProcessContainment adopts orphaned descendants of historical
// binaries so process-group escapees remain observable and reapable.
func enableHistoricalProcessContainment() error {
	historicalSubreaperOnce.Do(func() {
		historicalSubreaperErr = unix.Prctl(prSetChildSubreaper, 1, 0, 0, 0)
	})
	if historicalSubreaperErr != nil {
		return fmt.Errorf("enable historical process child subreaper: %w", historicalSubreaperErr)
	}
	return nil
}

type historicalProcess struct {
	pid       int
	ppid      int
	startTime string
}

type historicalProcessIdentity struct {
	pid       int
	startTime string
}

// runHistoricalCommand is the sole execution boundary for catalog-authenticated
// historical bd binaries. They are still hostile to census evidence integrity:
// an escaped descendant makes the entire action an infrastructure failure.
func runHistoricalCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return runHistoricalCommandIn(ctx, binary, "", nil, args...)
}

func runHistoricalCommandIn(ctx context.Context, binary, directory string, environment []string, args ...string) ([]byte, error) {
	historicalCommandMu.Lock()
	defer historicalCommandMu.Unlock()

	if err := enableHistoricalProcessContainment(); err != nil {
		return nil, err
	}
	binding, err := historicalBinaryBinding(ctx, binary)
	if err != nil {
		return nil, err
	}
	// Rehash immediately before each execution. This is deliberately separate
	// from acquisition-time verification because a prior historical binary may
	// have changed an attacker-writable path between two invocations.
	actual, err := executableSHA256(binary)
	if err != nil {
		return nil, fmt.Errorf("rehash historical binary before execution: %w", err)
	}
	if actual != binding.executableSHA256 {
		return nil, errors.New("historical binary digest does not match its in-generation binding")
	}

	baseline, err := historicalDescendantSnapshot(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("snapshot baseline historical descendants: %w", err)
	}
	captured, err := os.CreateTemp("", "beads-schema-census-historical-output-")
	if err != nil {
		return nil, fmt.Errorf("capture historical command output: %w", err)
	}
	capturedPath := captured.Name()
	defer func() { _ = os.Remove(capturedPath) }()
	command := exec.CommandContext(ctx, binary, args...) //nolint:gosec // binary is context-bound and rehashed immediately above; args are fixed census invocations.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Historical children receive a regular file rather than inherited pipes,
	// so an escapee cannot delay direct-child reaping by retaining stdout or
	// stderr. WaitDelay remains bounded as a second guard for context cleanup.
	command.WaitDelay = historicalPipeWaitDelay
	command.Dir = directory
	command.Env = environment
	command.Stdout = captured
	command.Stderr = captured
	actionErr := command.Run()

	var groupErr error
	if command.Process != nil {
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			groupErr = fmt.Errorf("kill historical process group: %w", err)
		}
	}
	cleanup := containHistoricalDescendants(baseline)
	captureErr := captured.Close()
	output, readErr := os.ReadFile(capturedPath) //nolint:gosec // capturedPath was created by os.CreateTemp in this process.
	if cleanup.unexpectedDescendant || groupErr != nil || cleanup.err != nil || captureErr != nil || readErr != nil {
		cleanupErr := errors.Join(groupErr, cleanup.err, captureErr, readErr)
		return output, &historicalProcessContainmentError{
			actionErr:            actionErr,
			cleanupErr:           cleanupErr,
			unexpectedDescendant: cleanup.unexpectedDescendant,
		}
	}
	return output, actionErr
}

type historicalCleanupResult struct {
	unexpectedDescendant bool
	err                  error
}

func containHistoricalDescendants(baseline map[historicalProcessIdentity]historicalProcess) historicalCleanupResult {
	deadline := time.Now().Add(historicalContainmentTimeout)
	var quietSince time.Time
	result := historicalCleanupResult{}
	for {
		descendants, err := historicalDescendantSnapshot(os.Getpid())
		if err != nil {
			return historicalCleanupResult{unexpectedDescendant: result.unexpectedDescendant, err: fmt.Errorf("discover historical descendants: %w", err)}
		}
		unexpected := make([]historicalProcess, 0, len(descendants))
		for _, process := range descendants {
			identity := historicalProcessIdentity{pid: process.pid, startTime: process.startTime}
			if _, known := baseline[identity]; !known {
				unexpected = append(unexpected, process)
			}
		}
		if len(unexpected) == 0 {
			if quietSince.IsZero() {
				quietSince = time.Now()
			} else if time.Since(quietSince) >= historicalQuiescenceWindow {
				return result
			}
		} else {
			result.unexpectedDescendant = true
			quietSince = time.Time{}
			for _, process := range unexpected {
				if err := syscall.Kill(process.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					return historicalCleanupResult{unexpectedDescendant: true, err: fmt.Errorf("kill historical descendant %d: %w", process.pid, err)}
				}
			}
			for _, process := range unexpected {
				if err := reapHistoricalDescendant(process.pid); err != nil {
					return historicalCleanupResult{unexpectedDescendant: true, err: err}
				}
			}
		}
		if time.Now().After(deadline) {
			return historicalCleanupResult{unexpectedDescendant: result.unexpectedDescendant, err: errors.New("historical descendant quiescence deadline exceeded")}
		}
		time.Sleep(historicalCleanupPoll)
	}
}

func reapHistoricalDescendant(pid int) error {
	var status syscall.WaitStatus
	_, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if err == nil || errors.Is(err, syscall.ECHILD) {
		return nil
	}
	return fmt.Errorf("reap historical descendant %d: %w", pid, err)
}

func historicalDescendantSnapshot(root int) (map[historicalProcessIdentity]historicalProcess, error) {
	processes, err := historicalProcessSnapshot()
	if err != nil {
		return nil, err
	}
	descendants := make(map[historicalProcessIdentity]historicalProcess)
	for _, process := range processes {
		if historicalProcessDescendsFrom(process, root, processes) {
			identity := historicalProcessIdentity{pid: process.pid, startTime: process.startTime}
			descendants[identity] = process
		}
	}
	return descendants, nil
}

func historicalProcessDescendsFrom(process historicalProcess, root int, all map[int]historicalProcess) bool {
	seen := make(map[int]struct{})
	for process.ppid != 0 {
		if process.ppid == root {
			return true
		}
		if _, duplicate := seen[process.ppid]; duplicate {
			return false
		}
		seen[process.ppid] = struct{}{}
		parent, ok := all[process.ppid]
		if !ok {
			return false
		}
		process = parent
	}
	return false
}

func historicalProcessSnapshot() (map[int]historicalProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	processes := make(map[int]historicalProcess)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 1 {
			continue
		}
		process, err := readHistoricalProcess(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		processes[pid] = process
	}
	return processes, nil
}

func readHistoricalProcess(pid int) (historicalProcess, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return historicalProcess{}, err
	}
	endName := strings.LastIndexByte(string(raw), ')')
	if endName < 0 {
		return historicalProcess{}, fmt.Errorf("parse /proc/%d/stat: missing process name terminator", pid)
	}
	fields := strings.Fields(string(raw)[endName+1:])
	if len(fields) < 20 {
		return historicalProcess{}, fmt.Errorf("parse /proc/%d/stat: missing process fields", pid)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid < 0 {
		return historicalProcess{}, fmt.Errorf("parse /proc/%d/stat parent: %w", pid, err)
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return historicalProcess{}, fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}
	return historicalProcess{pid: pid, ppid: ppid, startTime: fields[19]}, nil
}
