// Package crashlog appends bd crashes and errors to a durable log file under
// the XDG state directory: $XDG_STATE_HOME/beads/logs/bd.log (default
// ~/.local/state/beads/logs/bd.log). Entries contain timestamps, error
// messages, and panic stack traces only: no issue bodies, credentials, or
// health data. Logging is best-effort and never fails a command.
package crashlog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

// Dir returns the directory holding bd's durable error log ("" if unknown).
//
// $XDG_STATE_HOME is honored where it is set and the documented
// ~/.local/state default is used otherwise, including on platforms with no
// XDG convention: Go's stdlib has UserConfigDir and UserCacheDir but no state
// equivalent, and a log of past crashes is neither configuration nor a
// disposable cache. One predictable path everywhere beats a per-OS mapping
// nobody can recite when they need to find the file.
func Dir() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "beads", "logs")
}

// Path returns the full path of the log file ("" if the state dir is unknown).
func Path() string {
	dir := Dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "bd.log")
}

// Recover logs a panic with its stack, then re-panics to preserve the
// original crash behavior. Defer it from main. Note: it fires on panics only;
// os.Exit paths skip defers and are logged separately via Error.
func Recover() {
	if r := recover(); r != nil {
		write("panic: %v\n%s", r, debug.Stack())
		panic(r)
	}
}

// Error appends one error line attributed to the command that failed.
func Error(cmd string, err error) {
	if err == nil {
		return
	}
	write("%s: error: %v", cmd, err)
}

// maxLogSize caps the log. It truncates on overflow rather than rotating:
// rotation means a naming scheme, a retention count and a reaper, and the
// file exists so someone can read what bd did just before it broke — history
// older than the last few megabytes has no reader. Add rotation only if a cap
// this size is ever actually reached in practice.
const maxLogSize = 5 << 20

// write appends one entry. Every failure is swallowed: the log exists to
// explain a command that already went wrong, and a logger that can itself
// fail the command would be worse than no log at all.
//
// O_APPEND gives concurrent bd processes interleaving-free short writes; a
// stack trace can exceed the platform's atomic-append size and interleave
// with a concurrent crash. That is accepted — the alternative is a lock file
// on the crash path, which is where locking is least safe.
func write(format string, args ...any) {
	path := Path()
	if path == "" {
		return
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogSize {
		_ = os.Truncate(path, 0)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	// #nosec G304 -- path is Dir()/bd.log, built from the environment's state
	// directory and a constant filename; no caller supplies any part of it.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format(time.RFC3339)+" "+format+"\n", args...)
}
