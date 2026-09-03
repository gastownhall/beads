package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SuiteOwnerMarkerName is the file a test suite's TestMain writes at the root
// of the temp tree it owns, recording the PID of the process that created it.
//
// It exists so a LATER run can tell two indistinguishable-looking leftovers
// apart: the temp root of a sibling run that is alive right now (a parallel
// package under scripts/test.sh -p N, or a second `go test` in another
// terminal), and the temp root of a run whose process is gone — killed by
// `go test -timeout`, a CI cancel, or Ctrl-C — which skipped every defer and
// left its dolt sql-server behind (wy-j2zc8q).
const SuiteOwnerMarkerName = "testmain.pid"

// FailOnLeakEnv turns a post-run sweep that actually reaped something from a
// warning into a suite failure. It is opt-in on purpose: a single leaky test
// would otherwise redden a whole package for everyone, so the default is a
// loud line on stderr and the code the tests themselves produced.
const FailOnLeakEnv = "BEADS_TEST_FAIL_ON_LEAK"

// WriteSuiteOwnerMarker records this process as the owner of root by writing
// SuiteOwnerMarkerName inside it. Call it from TestMain immediately after
// creating the suite's temp root; SweepDeadSuiteRoots in a later run reads it.
//
// A root with no marker is never swept, so failing to write one only costs
// the next run its chance to clean up — it is never unsafe.
func WriteSuiteOwnerMarker(root string) error {
	return writeSuiteOwnerMarkerPID(root, os.Getpid())
}

// writeSuiteOwnerMarkerPID writes an owner marker naming an arbitrary PID.
// SweepDeadSuiteRoots uses it to put a marker BACK, naming the same dead
// owner, when it could not finish removing a root.
func writeSuiteOwnerMarkerPID(root string, pid int) error {
	if root == "" {
		return fmt.Errorf("doltserver: WriteSuiteOwnerMarker: empty root")
	}
	path := filepath.Join(root, SuiteOwnerMarkerName)
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// readSuiteOwnerMarker returns the PID recorded in root's owner marker.
// ok is false when the marker is absent, unreadable, or does not hold a
// plausible PID — all of which mean "owner unknown", never "owner dead".
func readSuiteOwnerMarker(root string) (pid int, ok bool) {
	// #nosec G304 -- the path is this package's own constant joined to a
	// temp root the caller vouches for; nothing here comes from user input.
	data, err := os.ReadFile(filepath.Join(root, SuiteOwnerMarkerName))
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// suiteRootAction is what SweepDeadSuiteRoots does with one candidate root.
type suiteRootAction int

const (
	// leaveSuiteRoot means the root is not provably debris: either nobody
	// claimed it (a pre-marker leftover, or another tool's directory that
	// merely shares the prefix) or its owning process is still running.
	leaveSuiteRoot suiteRootAction = iota
	// sweepSuiteRoot means a run claimed this root and that run is gone, so
	// anything still alive under it is leaked debris.
	sweepSuiteRoot
)

// decideSuiteRoot is the whole safety judgment of SweepDeadSuiteRoots,
// isolated from the filesystem and from process signaling so it can be
// tabled in a unit test.
//
// Only one combination reaps: a marker exists AND the PID it names is gone.
// "No marker" deliberately does NOT reap — a directory nobody claimed cannot
// be proven to be ours, and deleting it (plus SIGTERMing servers under it)
// on a guess is exactly the cross-suite killer sweep.go's contract forbids.
func decideSuiteRoot(hasMarker, ownerAlive bool) suiteRootAction {
	if !hasMarker || ownerAlive {
		return leaveSuiteRoot
	}
	return sweepSuiteRoot
}

// SweepDeadSuiteRoots reaps the temp roots left behind by earlier runs of the
// calling suite whose owning process is dead: it kills the dolt sql-servers
// still running under each such root and removes the tree.
//
// parentDir is where the suite creates its roots (os.TempDir() at call time)
// and prefix is the suite's own os.MkdirTemp pattern minus the random tail
// (e.g. "beads-bd-tests-"). Both must be non-empty; an empty prefix would
// glob the entire temp directory and is refused.
//
// Call it from TestMain BEFORE creating this run's own root. It is
// best-effort and returns the roots it removed.
//
// Safety rests entirely on decideSuiteRoot: a root is only ever touched when
// it is owned by the current user, carries this suite's own owner marker, and
// that owner is gone. Live sibling runs (marker + live PID) and unclaimed
// leftovers (no marker) are both left exactly as they are.
//
// The per-root reap is sweepServersUnderRoots, NOT SweepOrphanedTestServers:
// this runs at suite START, while sibling packages (go test -p N) are mid-run,
// so it must only ever reach processes provably inside the dead root it is
// cleaning up. SweepOrphanedTestServers' extra deleted-cwd arm spans every
// temp dir on the box and belongs only at end-of-run, where the suite is the
// last thing standing.
func SweepDeadSuiteRoots(parentDir, prefix string) []string {
	return sweepDeadSuiteRoots(parentDir, prefix, processAlive, sweepServersUnderRoots, removeSuiteRoot)
}

// removeSuiteRoot removes a suite temp root, making unwritable directories
// writable first. cmd/bd's root doubles as an isolated $HOME, so it can hold
// read-only Go module cache entries that plain os.RemoveAll refuses; this
// mirrors forceRemoveAll in cmd/bd's TestMain.
func removeSuiteRoot(root string) error {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && info.Mode()&0o200 == 0 {
			// #nosec G122 -- filepath.Walk lstats, so this only ever fires on
			// a real directory (never a symlink), inside a root the caller
			// already proved is owned by this user and abandoned by a dead
			// process. Widening its owner-write bit is also the least
			// consequential thing that could be done to a swapped path.
			_ = os.Chmod(path, info.Mode()|0o200)
		}
		return nil
	})
	return os.RemoveAll(root)
}

// sweepDeadSuiteRoots is SweepDeadSuiteRoots with its three effects injected:
// the liveness probe, the orphan-server reaper, and the tree removal.
func sweepDeadSuiteRoots(
	parentDir, prefix string,
	alive func(int) bool,
	sweepServers func(...string) []int,
	removeAll func(string) error,
) []string {
	if parentDir == "" || prefix == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(parentDir, prefix+"*"))
	if err != nil {
		return nil
	}

	var swept []string
	for _, root := range matches {
		// Lstat, not Stat: a symlink that happens to match the prefix must
		// not be followed out of parentDir and removed.
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		// On a shared /tmp anyone can plant a directory with this prefix and
		// a marker naming a dead PID. Only trust a root this user owns.
		if !rootOwnedBySelf(info) {
			continue
		}
		pid, hasMarker := readSuiteOwnerMarker(root)
		if decideSuiteRoot(hasMarker, hasMarker && alive(pid)) != sweepSuiteRoot {
			continue
		}
		sweepServers(root)
		if err := removeAll(root); err != nil {
			// Removal is not atomic: the marker is usually among the first
			// entries deleted, so a partial failure would leave an unclaimed
			// root that no later run may ever touch again. Put the marker
			// back, naming the same dead owner, so the next run retries.
			if writeErr := writeSuiteOwnerMarkerPID(root, pid); writeErr != nil {
				fmt.Fprintf(os.Stderr,
					"Warning: could not remove dead test-suite root %s (%v) and could not restore its owner marker (%v); it will not be retried\n",
					root, err, writeErr)
			} else {
				fmt.Fprintf(os.Stderr,
					"Warning: could not remove dead test-suite root %s: %v (marker restored; the next run retries)\n",
					root, err)
			}
			continue
		}
		swept = append(swept, root)
	}

	if len(swept) > 0 {
		fmt.Fprintf(os.Stderr, "Info: swept %d dead test-suite temp root(s): %v\n", len(swept), swept)
	}
	return swept
}

// ApplyLeakPolicy folds the result of a post-run SweepOrphanedTestServers
// call into the suite's exit code. suite names the package for the log line.
//
// A non-empty sweep means the suite leaked a dolt sql-server: the tests
// finished, but a server survived every t.Cleanup and TestMain defer. That is
// always reported loudly on stderr; it only fails the run when
// FailOnLeakEnv is set to "1", so one flaky test cannot redden a package for
// everyone who did not opt in. A code the tests already failed with is
// preserved, never downgraded.
func ApplyLeakPolicy(suite string, code int, swept []int) int {
	if len(swept) == 0 {
		return code
	}
	if os.Getenv(FailOnLeakEnv) == "1" {
		fmt.Fprintf(os.Stderr,
			"FAIL: %s leaked %d dolt sql-server process(es) %v; swept them, and %s=1 makes that a failure\n",
			suite, len(swept), swept, FailOnLeakEnv)
		if code == 0 {
			return 1
		}
		return code
	}
	fmt.Fprintf(os.Stderr,
		"Warning: %d leaked dolt sql-server(s) swept after %s: %v (set %s=1 to fail the run on this)\n",
		len(swept), suite, swept, FailOnLeakEnv)
	return code
}
