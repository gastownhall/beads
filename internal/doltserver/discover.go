package doltserver

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// doltProcessRow is one running dolt sql-server process.
type doltProcessRow struct {
	PID     int
	Command string
}

// DiscoveredServer is one running dolt sql-server process on this machine.
type DiscoveredServer struct {
	PID int
	// Command is the process's command line.
	Command string
	// Managed is true when bd runs this server: its server directory's
	// dolt-server.pid names this process. The fields below are set only
	// for a managed server.
	Managed bool
	// ServerDir is the server directory (a workspace's .beads, or the
	// shared server's directory).
	ServerDir string
	Port      int
	// StartedAt is when bd recorded the server (dolt-server.pid's mtime).
	StartedAt time.Time
	IdleStop  IdleStopState
}

// DiscoverServers lists every running dolt sql-server process on this
// machine and marks the ones bd runs. It ties a process to a server
// directory by its `--config <dir>/dolt-server-config.yaml` argument, or
// failing that (Unix only) by a working directory of `<dir>/dolt`; the
// process is bd's only when that directory's dolt-server.pid names it.
// Servers of other tools (Gas City cities among them) are listed unmanaged.
func DiscoverServers() ([]DiscoveredServer, error) {
	rows, err := readDoltProcessRows()
	if err != nil {
		return nil, err
	}
	servers := make([]DiscoveredServer, 0, len(rows))
	for _, row := range rows {
		servers = append(servers, attributeDoltProcess(row, processCwd))
	}
	return servers, nil
}

// attributeDoltProcess decides whether row is a bd-run server; cwdOf
// returns a process's working directory, or "" when it is unknown.
func attributeDoltProcess(row doltProcessRow, cwdOf func(pid int) string) DiscoveredServer {
	server := DiscoveredServer{PID: row.PID, Command: row.Command}
	dir := configArgServerDir(row.Command)
	if dir == "" {
		if cwd := cwdOf(row.PID); cwd != "" && filepath.Base(cwd) == "dolt" {
			dir = filepath.Dir(cwd)
		}
	}
	if dir == "" || readPIDFile(dir) != row.PID {
		return server
	}
	server.Managed = true
	server.ServerDir = dir
	server.Port = readPortFile(dir)
	if fi, err := os.Stat(pidPath(dir)); err == nil {
		server.StartedAt = fi.ModTime()
	}
	server.IdleStop = ReadIdleStopState(dir)
	return server
}

// configArgServerDir returns the directory of the bd-written server config
// that command's --config argument names, or "" when it names none. The path
// may contain spaces (ps prints the arguments space-joined), so it runs up to
// the config file name rather than to the next space.
func configArgServerDir(command string) string {
	_, rest, ok := strings.Cut(command, "--config")
	if !ok {
		return ""
	}
	rest = strings.TrimLeft(rest, "= \t\"")
	i := strings.Index(rest, doltServerConfigFileName)
	if i <= 0 || !os.IsPathSeparator(rest[i-1]) {
		return ""
	}
	return filepath.Clean(rest[:i-1])
}
