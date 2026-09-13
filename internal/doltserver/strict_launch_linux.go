//go:build linux

package doltserver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/steveyegge/beads/internal/procid"
)

// SupportsStrictLaunchRecovery reports whether this platform can bind a
// crash-window listener to the durable strict launch intent.
func SupportsStrictLaunchRecovery() bool { return procid.SupportsKernelBoundHandle() }

// strictLaunchMatches proves a crash-surviving child is the exact process
// authorized by a durable strict launch intent. It is intentionally Linux
// specific here: /proc gives all three kernel identities without parsing
// display-oriented process output.
func strictLaunchMatches(pid int, options StartOptions, doltDir string) bool {
	match, err := strictLaunchMatch(pid, options, doltDir)
	return err == nil && match
}

// strictLaunchMatch distinguishes a vanished/unrelated process from an
// observation failure. Strict recovery must not treat an unreadable /proc
// record as proof that no nonce-bound child exists and launch a duplicate.
func strictLaunchMatch(pid int, options StartOptions, doltDir string) (bool, error) {
	if pid <= 0 || options.ConfigPath == "" {
		return false, nil
	}
	rawArgs, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read candidate argv %d: %w", pid, err)
	}
	fields := bytes.Split(bytes.TrimSuffix(rawArgs, []byte{0}), []byte{0})
	args := make([]string, 0, len(fields))
	for _, field := range fields {
		args = append(args, string(field))
	}
	// The durable nonce is unguessable and its exact arg position identifies
	// the only processes that need privileged exe/cwd observation. This avoids
	// treating unrelated unreadable system processes as candidates.
	if len(args) != 4 || args[1] != "sql-server" || args[2] != "--config" || args[3] != options.ConfigPath {
		return false, nil
	}
	exe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read candidate executable %d: %w", pid, err)
	}
	if exe != options.Executable {
		return false, nil
	}
	cwd, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read candidate cwd %d: %w", pid, err)
	}
	if filepath.Clean(cwd) != filepath.Clean(doltDir) {
		return false, nil
	}
	return strictConfigArgMatches(args, options.Executable, options.ConfigPath), nil
}

func strictLaunchCandidates(options StartOptions, doltDir string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("enumerate strict launch candidates: %w", err)
	}
	var matches []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		match, matchErr := strictLaunchMatch(pid, options, doltDir)
		if matchErr != nil {
			return nil, matchErr
		}
		if match {
			matches = append(matches, pid)
		}
	}
	return matches, nil
}
