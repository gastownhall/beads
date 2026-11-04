package doltserver

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
)

// This file holds the startup signals both bd-managed dolt sql-server
// launchers read from the child's own output: the plain server-mode Start in
// this package and the proxied DoltServer in internal/storage/dbproxy/server.
// A dial (or even a MySQL greeting) on the chosen port is not proof that the
// child bound it: the port is picked before dolt binds it, and another
// process can take it in between. dolt then logs that the port is in use and
// exits, while the dial reaches the other process.

// ErrPortInUse reports that a dolt sql-server bd launched exited before it
// was ready because another process holds its SQL listener port.
var ErrPortInUse = errors.New("dolt sql-server listener port is already in use")

// DoltReadyLine is what dolt sql-server (go-mysql-server) logs at info level
// once its listener is bound and accepting. Seeing it on the output of the
// process bd launched proves the port belongs to that process.
const DoltReadyLine = "Server ready. Accepting connections."

// doltPortInUseTexts are the ways a dolt sql-server reports that port, its SQL
// listener port, is taken:
//   - dolt's own pre-check: "Port 3306 already in use."
//   - go-mysql-server's pre-check: "Port 127.0.0.1:3306 already in use."
//   - the bind losing the race after both checks, as Go's net error:
//     "listen tcp 127.0.0.1:3306: bind: address already in use", or on
//     Windows "...:3306: bind: Only one usage of each socket address ...".
//
// The port is part of every form, so a different listener dolt fails to bind
// (metrics, MCP) is not mistaken for the SQL port, nor is dolt's non-fatal
// unix-socket warning ("... is already in use"). dolt prints the pre-check
// message whatever its log level, so it is visible at log_level warning too.
func doltPortInUseTexts(port int) [][]byte {
	return [][]byte{
		[]byte(fmt.Sprintf("Port %d already in use", port)),
		[]byte(fmt.Sprintf(":%d already in use", port)),
		[]byte(fmt.Sprintf(":%d: bind: address already in use", port)),
		[]byte(fmt.Sprintf(":%d: bind: Only one usage of each socket address", port)),
	}
}

// LogLevelEmitsReadyLine reports whether dolt logs DoltReadyLine (an info
// message) at level.
func LogLevelEmitsReadyLine(level servercfg.LogLevel) bool {
	switch level {
	case servercfg.LogLevel_Trace, servercfg.LogLevel_Debug, servercfg.LogLevel_Info:
		return true
	}
	return false
}

// StartupWatch receives a dolt sql-server's stdout and stderr. It forwards
// everything to dst (if any) and, until the ready line shows up, scans the
// output for that line and for a conflict on the SQL listener port.
type StartupWatch struct {
	dst io.Writer

	inUse [][]byte

	mu        sync.Mutex
	tail      []byte
	scanning  bool
	portInUse bool
	ready     chan struct{}
}

// startupWatchTail is how much recent output the watcher keeps for matching,
// so a marker split across two writes is still found.
const startupWatchTail = 4096

// NewStartupWatch returns a watcher for a dolt sql-server whose SQL listener
// is port. dst may be nil.
func NewStartupWatch(dst io.Writer, port int) *StartupWatch {
	return &StartupWatch{dst: dst, inUse: doltPortInUseTexts(port), scanning: true, ready: make(chan struct{})}
}

// Write never fails: a log file that cannot be written must not break the
// pipe the dolt sql-server writes to.
func (w *StartupWatch) Write(p []byte) (int, error) {
	if w.dst != nil {
		_, _ = w.dst.Write(p)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.scanning {
		return len(p), nil
	}
	w.tail = append(w.tail, p...)
	for _, text := range w.inUse {
		if bytes.Contains(w.tail, text) {
			w.portInUse = true
		}
	}
	if bytes.Contains(w.tail, []byte(DoltReadyLine)) {
		w.scanning = false
		w.tail = nil
		close(w.ready)
		return len(p), nil
	}
	if over := len(w.tail) - startupWatchTail; over > 0 {
		w.tail = append(w.tail[:0], w.tail[over:]...)
	}
	return len(p), nil
}

// ReadyCh is closed once the ready line has been seen.
func (w *StartupWatch) ReadyCh() <-chan struct{} { return w.ready }

// IsReady reports whether the ready line has been seen.
func (w *StartupWatch) IsReady() bool {
	select {
	case <-w.ready:
		return true
	default:
		return false
	}
}

// SawPortInUse reports whether the output said the SQL listener port is taken.
func (w *StartupWatch) SawPortInUse() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.portInUse
}
