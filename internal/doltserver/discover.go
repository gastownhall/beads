// Server discovery: finding every dolt sql-server on this machine, so
// `bd dolt status --all` and `bd dolt stop --all` (GH#4282) can describe and
// end bd's own servers no matter which workspace they serve — and name the
// servers that are not bd's instead of touching them.

package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// DiscoveredServer is one dolt sql-server found running on this machine.
type DiscoveredServer struct {
	// PID is the dolt sql-server process.
	PID int `json:"pid"`
	// ServerDir is the directory the server is attributed to — the
	// directory holding its state files (beadsDir in per-project mode) —
	// or "" when neither its --config argument nor its working directory
	// names one. A server with no ServerDir is never BDManaged.
	ServerDir string `json:"server_dir"`
	// Port is the port the serverDir's port file names (0 when absent).
	Port int `json:"port"`
	// BDManaged reports whether the serverDir's pid file names this PID:
	// a server bd started, or adopted, through its own lifecycle.
	BDManaged bool `json:"bd_managed"`
	// Uptime is how long the process has been running (0 when the process
	// table does not say).
	Uptime time.Duration `json:"uptime"`
	// IdleStop describes the idle watch over a BDManaged server: disabled
	// (dolt.idle-timeout: 0), the running watcher's window, or why no
	// watcher is running (an explicit start). Empty for servers bd does
	// not manage.
	IdleStop string `json:"idle_stop,omitempty"`
}

// doltProcessRow is one dolt sql-server row of the process-table snapshot.
type doltProcessRow struct {
	PID     int
	Command string
	// Uptime is the parsed elapsed time (0 when the table does not say).
	Uptime time.Duration
}

// doltProcessRows reads the process table's dolt sql-server rows. A variable
// so tests can feed canned snapshots; the platform implementations are
// readDoltProcessRows in doltserver_unix.go and doltserver_windows.go.
var doltProcessRows = readDoltProcessRows

// processCwd resolves a live process's working directory ("" when unknown).
// A variable for the same reason; the platform implementations are
// readProcessCwd in the same per-platform files.
var processCwd = readProcessCwd

// DiscoverServers returns every dolt sql-server running on this machine, in
// process-table order, bd's own servers and foreign ones alike. An error
// means the process table could not be read — not that no server runs.
func DiscoverServers() ([]DiscoveredServer, error) {
	rows, err := doltProcessRows()
	if err != nil {
		return nil, err
	}
	servers := make([]DiscoveredServer, 0, len(rows))
	for _, row := range rows {
		servers = append(servers, describeDiscoveredServer(row))
	}
	return servers, nil
}

// describeDiscoveredServer attributes one process-table row. A server is
// bd's exactly when a serverDir can be attributed AND that directory's pid
// file names the process: bd writes both whenever it starts or adopts a
// server, so a foreign sql-server with a coincidental cwd cannot be stopped
// by bd's --all.
func describeDiscoveredServer(row doltProcessRow) DiscoveredServer {
	s := DiscoveredServer{
		PID:       row.PID,
		Uptime:    row.Uptime,
		Port:      0,
		ServerDir: attributeServerDir(row),
	}
	if s.ServerDir == "" {
		return s
	}
	s.Port = readPortFile(s.ServerDir)
	data, err := os.ReadFile(pidPath(s.ServerDir))
	if err != nil {
		return s
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err == nil && pid == row.PID {
		s.BDManaged = true
		s.IdleStop = DescribeIdleWatch(s.ServerDir)
	}
	return s
}

// DescribeIdleWatch names the idle watch state of a bd-managed serverDir:
// the configured window with the watcher running, the window disabled, or
// no watcher running (an explicit start's server, or one whose watcher
// failed to spawn).
func DescribeIdleWatch(serverDir string) string {
	window := ResolveIdleTimeoutForDir(serverDir)
	if window <= 0 {
		return "disabled (dolt.idle-timeout: 0)"
	}
	wdata, err := os.ReadFile(watcherPIDPath(serverDir))
	if err == nil {
		if wpid, perr := strconv.Atoi(strings.TrimSpace(string(wdata))); perr == nil && isProcessAlive(wpid) {
			return fmt.Sprintf("stops after %s idle", window)
		}
	}
	return fmt.Sprintf("idle after %s, no watcher running", window)
}

// attributeServerDir resolves the workspace directory a dolt sql-server
// process serves. bd always starts the server with its data directory as
// the working directory (serverDir/dolt) and, when the dolt binary supports
// it, a --config file inside serverDir, so the command line is tried first
// and the working directory second; "" means unattributed.
func attributeServerDir(row doltProcessRow) string {
	if dir := serverDirFromConfigArg(row.Command); dir != "" {
		return dir
	}
	cwd := processCwd(row.PID)
	if cwd == "" {
		return ""
	}
	if base := filepath.Base(cwd); base == "dolt" {
		return filepath.Dir(cwd)
	}
	return ""
}

// serverDirFromConfigArg extracts the server directory from a
// `--config <serverDir>/dolt-server-config.yaml` argument: the same path
// bd's own start writes (doltServerConfigPath). It accepts both `--config
// path` and `--config=path` spellings.
func serverDirFromConfigArg(command string) string {
	fields := strings.Fields(command)
	for i, field := range fields {
		path := ""
		switch {
		case field == "--config" && i+1 < len(fields):
			path = fields[i+1]
		case strings.HasPrefix(field, "--config="):
			path = strings.TrimPrefix(field, "--config=")
		}
		if path == "" {
			continue
		}
		if filepath.Base(path) == doltServerConfigFileName {
			return filepath.Dir(path)
		}
	}
	return ""
}

// parseDoltProcessRows extracts the dolt sql-server rows of a
// `ps -axo pid=,state=,etime=,command=` snapshot, skipping zombies and
// defunct processes. It preserves the source row order.
func parseDoltProcessRows(snapshot []byte) []doltProcessRow {
	var rows []doltProcessRow
	for _, line := range strings.Split(string(snapshot), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidText, rest, ok := nextPSField(line)
		if !ok {
			continue
		}
		state, rest, ok := nextPSField(rest)
		if !ok {
			continue
		}
		etime, command := splitEtimeField(rest)
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 {
			continue
		}
		if state[0] == 'Z' || state[0] == 'X' {
			continue
		}
		doltIndex := strings.Index(command, "dolt")
		if doltIndex >= 0 && strings.Contains(command[doltIndex+len("dolt"):], "sql-server") {
			rows = append(rows, doltProcessRow{
				PID:     pid,
				Command: command,
				Uptime:  parseEtime(etime),
			})
		}
	}
	return rows
}

// nextPSField separates the next whitespace-delimited ps field from the
// remaining text (the per-platform splitPSField is unix-only; this copy
// keeps discover.go compilable everywhere).
func nextPSField(line string) (field, rest string, ok bool) {
	fieldEnd := strings.IndexFunc(line, unicode.IsSpace)
	if fieldEnd == -1 {
		return "", "", false
	}
	field = line[:fieldEnd]
	rest = strings.TrimLeftFunc(line[fieldEnd:], unicode.IsSpace)
	return field, rest, rest != ""
}

// splitEtimeField splits an `etime` column (elapsed time, ps's own
// [[dd-]hh:]mm:ss format) from the remaining text. The etime column never
// contains whitespace, and the command column always does.
func splitEtimeField(rest string) (etime, command string) {
	fieldEnd := strings.IndexFunc(rest, unicode.IsSpace)
	if fieldEnd == -1 {
		return rest, ""
	}
	return rest[:fieldEnd], strings.TrimLeftFunc(rest[fieldEnd:], unicode.IsSpace)
}

// parseEtime parses ps's elapsed-time format: [[dd-]hh:]mm:ss. The empty
// string is 0, not a parse failure: a row the table did not measure.
func parseEtime(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var days int
	if dash := strings.Index(s, "-"); dash >= 0 {
		d, err := strconv.Atoi(s[:dash])
		if err != nil {
			return 0
		}
		days = d
		s = s[dash+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0
	}
	var units []time.Duration
	switch len(parts) {
	case 3:
		units = []time.Duration{time.Hour, time.Minute, time.Second}
	case 2:
		units = []time.Duration{time.Minute, time.Second}
	case 1:
		units = []time.Duration{time.Second}
	}
	var total time.Duration
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil {
			return 0
		}
		total += time.Duration(v) * units[i]
	}
	return total + time.Duration(days)*24*time.Hour
}
