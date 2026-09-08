//go:build windows

package doltserver

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// portConflictHint is the platform-specific command to diagnose port conflicts.
// Used in error messages when a port is busy but the occupying process can't be identified.
const portConflictHint = "netstat -aon | findstr :%d"

// processListHint is the platform-specific command to list dolt processes.
// Used in error messages when too many dolt servers are running.
const processListHint = `tasklist /FI "IMAGENAME eq dolt.exe"`

// procAttrDetached returns SysProcAttr to detach a child process so it survives
// parent exit. On Windows, CREATE_NEW_PROCESS_GROUP is the analog of Unix Setpgid.
func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// findPIDOnPort returns the PID of the process listening on a TCP port.
// Parses netstat -aon output matching LISTENING lines. Returns 0 if no process
// found or on error.
func findPIDOnPort(port int) int {
	out, err := exec.Command("netstat", "-aon").Output()
	if err != nil {
		return 0
	}
	portSuffix := fmt.Sprintf(":%d", port)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// fields[1] is the local address (e.g., "127.0.0.1:13307" or "[::]:13307")
		localAddr := fields[1]
		if !strings.HasSuffix(localAddr, portSuffix) {
			continue
		}
		// fields[4] is the PID
		pid, err := strconv.Atoi(fields[4])
		if err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// listDoltProcessPIDs returns PIDs of all running dolt sql-server processes.
// Uses PowerShell Get-CimInstance to query dolt.exe processes and filter by
// command line. Zombies are not a concern on Windows (they don't appear in
// process lists).
func listDoltProcessPIDs() []int {
	// Single PowerShell call: find dolt.exe processes whose command line
	// contains "sql-server", output PIDs one per line.
	script := `Get-CimInstance Win32_Process -Filter "Name='dolt.exe'" | ` +
		`Where-Object { $_.CommandLine -match 'sql-server' } | ` +
		`Select-Object -ExpandProperty ProcessId`
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command", script).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// isProcessInDir returns false on Windows. Windows doesn't expose process CWD
// through standard APIs. The consequence: reclaimPort will kill any untracked
// dolt server on the canonical port rather than adopting it. This is conservative
// (safer than accidentally adopting the wrong server). PID file and daemon PID
// file adoption paths still work.
func isProcessInDir(pid int, dir string) bool {
	return false
}

// isProcessAlive checks if a process with the given PID is running.
// Uses OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION — returns error if
// the process doesn't exist.
func isProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

// gracefulStop terminates a process on Windows. Uses TerminateProcess (hard kill)
// directly. Data safety comes from FlushWorkingSet which runs in StopWithForce
// before calling gracefulStop. The Unix SIGTERM is a courtesy; the real protection
// is the flush.
func gracefulStop(pid int, timeout time.Duration) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}
	_ = process.Kill()
	time.Sleep(500 * time.Millisecond)
	return nil
}
