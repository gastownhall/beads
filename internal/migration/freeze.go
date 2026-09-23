// Package migration provides read-side access to the MIGRATION-FREEZE
// write-freeze marker. The marker is a plain file that an operator — or an
// external orchestration tool driving a migration — creates to stop writes
// against a workspace, and removes to resume them. bd only ever reads it,
// before a write command runs, so a human typing 'bd create'/'bd update'
// mid-migration cannot slip a write past whatever quiesced the workspace
// (dc-6jaq).
package migration

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// FileName is the name of the freeze marker file. bd looks for it in a start
// directory and every ancestor up to the filesystem root, so a marker placed
// at the top of a multi-repo tree freezes every workspace beneath it.
const FileName = "MIGRATION-FREEZE"

// EnvFreezeFile names an explicit freeze-marker path. When set and non-empty
// it is authoritative: bd checks that path only and skips the ancestor walk,
// so it doubles as the hook for markers that live outside any workspace tree
// and as an opt-out (point it at a path that does not exist).
const EnvFreezeFile = "BD_MIGRATION_FREEZE_FILE"

// maxMarkerSize caps how much of a marker bd will read. The payload is one
// short line; anything larger is a mistake or a hostile file (the ancestor
// walk can reach directories bd does not control), and a freeze must not turn
// into a multi-gigabyte allocation.
const maxMarkerSize = 64 << 10

// Info is the parsed contents of a freeze marker.
type Info struct {
	Operator  string    // who initiated the freeze (a username, a tool name)
	Reason    string    // human-readable migration reason
	Timestamp time.Time // when the freeze was set
}

// Result is the outcome of a freeze lookup.
type Result struct {
	// Path is the active marker's path, or "" when no marker was found.
	Path string

	// FromEnv reports that Path — or, when the lookup failed, the path that
	// failed — came from EnvFreezeFile rather than an ancestor walk. Callers
	// use it to add "or unset BD_MIGRATION_FREEZE_FILE" to a refusal.
	FromEnv bool

	// Err reports that the lookup could not be completed: a path that is
	// neither present nor absent as far as bd can tell (a permission error on
	// the marker or a directory above it). Callers must treat a non-nil Err as
	// frozen — an undeterminable gate is not an open gate — and say so.
	Err error
}

// Frozen reports whether writes must be refused: either a marker was found or
// the lookup could not be completed.
func (r Result) Frozen() bool { return r.Path != "" || r.Err != nil }

// Find looks for an active freeze marker and reports what it found.
//
// EnvFreezeFile, when set, is authoritative and the only path consulted.
// Otherwise Find walks the current working directory to the filesystem root,
// then does the same for each directory in also, stopping at the first marker.
//
// Passing the resolved workspace in also is what keys the gate on the store
// being written rather than on the caller's shell: `BEADS_DIR=/work/repo/.beads
// bd create` run from $HOME, `bd -C /work/repo create` (which sets BEADS_DIR
// without ever chdir'ing), and a daemon with an unrelated cwd must all still
// be stopped by /work/MIGRATION-FREEZE.
func Find(also ...string) Result {
	if r, handled := lookupEnvOverride(); handled {
		return r
	}

	starts := make([]string, 0, len(also)+1)
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	starts = append(starts, also...)
	return walkAll(starts)
}

// FindFrom looks for an active freeze marker in dir and its ancestors only,
// ignoring the process working directory. Use it when the caller's cwd is not
// part of the operation — a command handed an explicit target path — and in
// tests that must exercise the walk without depending on process state.
// EnvFreezeFile still wins when set.
func FindFrom(dir string) Result {
	if r, handled := lookupEnvOverride(); handled {
		return r
	}
	return walkAll([]string{dir})
}

// lookupEnvOverride resolves EnvFreezeFile. The second return reports whether
// the variable was set at all; when it was, its answer is final and no walk
// runs — that is what makes the variable a usable opt-out.
func lookupEnvOverride() (Result, bool) {
	override := os.Getenv(EnvFreezeFile)
	if override == "" {
		return Result{}, false
	}
	switch found, err := statMarker(override, true); {
	case err != nil:
		return Result{FromEnv: true, Err: err}, true
	case found:
		return Result{Path: override, FromEnv: true}, true
	default:
		return Result{FromEnv: true}, true
	}
}

// walkAll walks each start directory to the filesystem root, returning the
// first marker found. Duplicate and empty starts are skipped, as are ancestors
// already visited by an earlier start.
func walkAll(starts []string) Result {
	seen := make(map[string]bool)
	for _, start := range starts {
		if start == "" {
			continue
		}
		dir, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for {
			if !seen[dir] {
				seen[dir] = true
				switch found, statErr := statMarker(filepath.Join(dir, FileName), false); {
				case statErr != nil:
					return Result{Err: statErr}
				case found && !inUntrustedDir(dir):
					return Result{Path: filepath.Join(dir, FileName)}
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return Result{}
}

// statMarker reports whether path is a usable freeze marker. A missing path is
// simply not a marker; so is a directory or a device named MIGRATION-FREEZE,
// which the walk can otherwise hit in vendored fixtures or an extracted
// archive. Any other stat failure — a permission error on the marker or on a
// directory above it — is returned, because "bd cannot tell" must not read as
// "not frozen".
//
// trusted separates the operator-named EnvFreezeFile path from one the untrusted
// ancestor walk stumbled onto, which decides how a symlink is treated. os.Lstat
// does not follow links, so a symlink is neither regular nor absent; left alone
// it would silently fail OPEN — the one direction a write gate must never take.
// On the trusted path a symlink is honored (resolved with os.Stat, so a link to
// a real regular marker still freezes): the operator named it, and an
// indirection like `current -> releases/N` is exactly what EnvFreezeFile is
// for. On the untrusted walk a symlink is NOT followed — anything the walk
// merely passed through could otherwise redirect the gate — but the skip is
// announced on stderr so it is visible rather than a silent bypass.
func statMarker(path string, trusted bool) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return classifyMarkerStatErr(path, err)
	}
	switch {
	case info.Mode().IsRegular():
		return true, nil
	case info.Mode()&os.ModeSymlink != 0:
		return statSymlinkMarker(path, trusted)
	default:
		// A directory, device, socket, or similar named MIGRATION-FREEZE is not
		// a marker.
		return false, nil
	}
}

// statSymlinkMarker decides a symlink found where a marker was expected. On the
// untrusted walk it refuses to follow but warns, so an ignored would-be marker
// never disappears in silence. On the operator-named path it follows the link
// with os.Stat and honors a regular target; a dangling or non-regular target is
// simply not a marker, and an unreadable one stays fail-closed.
func statSymlinkMarker(path string, trusted bool) (bool, error) {
	if !trusted {
		fmt.Fprintf(os.Stderr,
			"bd: ignoring symlinked %s at %s; a freeze marker must be a regular file (set %s to honor a symlink)\n",
			FileName, path, EnvFreezeFile)
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return classifyMarkerStatErr(path, err)
	}
	return info.Mode().IsRegular(), nil
}

// classifyMarkerStatErr maps a stat/lstat error to the (found, err) a marker
// lookup reports: a genuinely-absent path (ENOENT/EINVAL, or ENOTDIR when a
// component of the path is not a directory) is "not a marker"; anything else is
// undeterminable and returned so the caller treats it as frozen.
func classifyMarkerStatErr(path string, err error) (bool, error) {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrInvalid):
		return false, nil
	case errors.Is(err, syscall.ENOTDIR):
		// A component of path is not a directory, so the marker cannot exist
		// here. That is an answer, not a failure.
		return false, nil
	default:
		return false, fmt.Errorf("checking for a %s marker at %s: %w", FileName, path, err)
	}
}

// inUntrustedDir reports whether dir is a world-writable sticky directory —
// /tmp and its kin. The ancestor walk reaches those on the way to the root,
// and a marker there is both forgeable by any local account and, thanks to the
// sticky bit, undeletable by the victim the refusal tells to delete it. An
// explicit BD_MIGRATION_FREEZE_FILE is exempt: that path is the operator's own
// stated intent, not something the walk stumbled onto.
func inUntrustedDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	mode := info.Mode()
	return mode.IsDir() && mode&os.ModeSticky != 0 && mode.Perm()&0o002 != 0
}

// ReadFile parses the freeze marker at path. Returns nil when the file is
// missing or unreadable. At most maxMarkerSize bytes are read.
func ReadFile(path string) *Info {
	if path == "" {
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: path comes from Find/FindFrom — an ancestor walk for a fixed filename, or the operator's own BD_MIGRATION_FREEZE_FILE
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxMarkerSize))
	if err != nil {
		return nil
	}
	return parse(string(data))
}

// parse reads the marker's optional payload: operator, RFC3339 timestamp, and
// reason, tab-separated on a single line. Every field is optional — an empty
// file is a valid freeze, and so is one that records a reason but no operator.
func parse(content string) *Info {
	// Only the first line is payload. Taking it before splitting keeps a
	// multi-line file from smuggling extra lines into Reason, and trimming
	// per-field afterwards (rather than trimming the whole string first)
	// preserves a leading empty operator instead of shifting the timestamp
	// into it.
	line, _, _ := strings.Cut(content, "\n")
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		// No recorded timestamp to parse (e.g. an empty marker file, such
		// as one created with `touch`) — leave Timestamp at its zero value
		// rather than fabricating "now": a caller that ever inspects it
		// should not be told the freeze started at check-time.
		return &Info{}
	}

	parts := strings.SplitN(line, "\t", 3)
	info := &Info{}
	info.Operator = strings.TrimSpace(parts[0])
	if len(parts) >= 2 {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[1])); err == nil {
			info.Timestamp = t
		}
	}
	if len(parts) >= 3 {
		info.Reason = strings.TrimSpace(parts[2])
	}
	return info
}
