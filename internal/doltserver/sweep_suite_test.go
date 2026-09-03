package doltserver

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// TestDecideSuiteRoot pins the safety judgment behind SweepDeadSuiteRoots:
// exactly one of the four states — claimed by a process that is gone — is
// debris. In particular an UNCLAIMED root is left alone, because a directory
// that merely matches the suite's prefix cannot be proven to be this suite's,
// and reaping it would resurrect the cross-suite killer that
// selectOrphanTestServerPIDs' contract exists to prevent (wy-j2zc8q).
func TestDecideSuiteRoot(t *testing.T) {
	cases := []struct {
		name       string
		hasMarker  bool
		ownerAlive bool
		want       suiteRootAction
	}{
		{"claimed by a dead owner is debris", true, false, sweepSuiteRoot},
		{"claimed by a live owner is a concurrent run", true, true, leaveSuiteRoot},
		{"unclaimed root is never provably ours", false, false, leaveSuiteRoot},
		{"unclaimed root stays unclaimed even if some pid is alive", false, true, leaveSuiteRoot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideSuiteRoot(tc.hasMarker, tc.ownerAlive); got != tc.want {
				t.Errorf("decideSuiteRoot(%v, %v) = %v, want %v", tc.hasMarker, tc.ownerAlive, got, tc.want)
			}
		})
	}
}

// TestSuiteOwnerMarkerRoundTrip covers the marker read/write pair, including
// every way a read can be inconclusive — each of which must report "owner
// unknown" (ok=false), never "owner dead".
func TestSuiteOwnerMarkerRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := WriteSuiteOwnerMarker(root); err != nil {
		t.Fatalf("WriteSuiteOwnerMarker: %v", err)
	}
	pid, ok := readSuiteOwnerMarker(root)
	if !ok || pid != os.Getpid() {
		t.Errorf("readSuiteOwnerMarker() = (%d, %v), want (%d, true)", pid, ok, os.Getpid())
	}

	if err := WriteSuiteOwnerMarker(""); err == nil {
		t.Error("WriteSuiteOwnerMarker(\"\") = nil, want an error")
	}

	if _, ok := readSuiteOwnerMarker(t.TempDir()); ok {
		t.Error("readSuiteOwnerMarker() on a root with no marker reported ok=true")
	}

	for _, body := range []string{"", "not-a-pid", "0", "-1"} {
		bad := t.TempDir()
		if err := os.WriteFile(filepath.Join(bad, SuiteOwnerMarkerName), []byte(body), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if _, ok := readSuiteOwnerMarker(bad); ok {
			t.Errorf("readSuiteOwnerMarker() on marker %q reported ok=true, want unknown", body)
		}
	}
}

// TestSweepDeadSuiteRootsSelection walks a synthetic temp directory holding
// one of every case and asserts both halves of the outcome: which roots were
// removed, and which roots the server reaper was vouched for. Nothing here
// signals a real process — the liveness probe and the reaper are injected.
func TestSweepDeadSuiteRootsSelection(t *testing.T) {
	parent := t.TempDir()
	const prefix = "beads-bd-tests-"

	mkRoot := func(name string, markerPID int) string {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if markerPID != 0 {
			body := []byte(strconv.Itoa(markerPID) + "\n")
			if err := os.WriteFile(filepath.Join(dir, SuiteOwnerMarkerName), body, 0o600); err != nil {
				t.Fatalf("write marker in %s: %v", dir, err)
			}
		}
		return dir
	}

	const deadPID, livePID = 4242, 4243
	dead := mkRoot(prefix+"dead", deadPID)
	live := mkRoot(prefix+"live", livePID)
	unclaimed := mkRoot(prefix+"unclaimed", 0)
	otherSuite := mkRoot("beads-storage-dolt-tests-dead", deadPID)

	// A plain file and a symlink that both match the prefix: neither is a
	// directory this suite created, and neither may be removed.
	stray := filepath.Join(parent, prefix+"stray-file")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}
	elsewhere := t.TempDir()
	link := filepath.Join(parent, prefix+"symlink")
	if err := os.Symlink(elsewhere, link); err != nil {
		// Windows without the create-symlink privilege; the rest of the
		// case still carries its weight.
		t.Logf("skipping the symlink leg: %v", err)
		link = ""
	}

	var vouched []string
	swept := sweepDeadSuiteRoots(
		parent, prefix,
		func(pid int) bool { return pid == livePID },
		func(roots ...string) []int {
			vouched = append(vouched, roots...)
			return nil
		},
		os.RemoveAll,
	)

	if want := []string{dead}; !reflect.DeepEqual(swept, want) {
		t.Errorf("sweepDeadSuiteRoots() = %v, want %v", swept, want)
	}
	if want := []string{dead}; !reflect.DeepEqual(vouched, want) {
		t.Errorf("the server reaper was vouched for %v, want %v", vouched, want)
	}
	if _, err := os.Stat(dead); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dead-owner root %s still present: %v", dead, err)
	}
	for _, keep := range []string{live, unclaimed, otherSuite, stray, link, elsewhere} {
		if keep == "" {
			continue
		}
		if _, err := os.Lstat(keep); err != nil {
			t.Errorf("%s must have been left alone: %v", keep, err)
		}
	}
}

// TestSweepDeadSuiteRootsRestoresMarkerOnPartialRemoval covers the recovery
// path for a removal that fails halfway. os.RemoveAll is not atomic and the
// marker is an early casualty, so without the rewrite a root that could not
// be fully deleted (a read-only Go module cache entry under cmd/bd's isolated
// $HOME, say) would come back UNCLAIMED — and an unclaimed root is never
// swept again, by design. The marker must go back naming the same dead owner.
func TestSweepDeadSuiteRootsRestoresMarkerOnPartialRemoval(t *testing.T) {
	parent := t.TempDir()
	const prefix = "beads-partial-tests-"
	const deadPID = 4242

	root := filepath.Join(parent, prefix+"stuck")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := writeSuiteOwnerMarkerPID(root, deadPID); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Stand in for a partial os.RemoveAll: the marker is gone, the root is
	// not, and an error comes back.
	stubbornRemove := func(path string) error {
		if err := os.Remove(filepath.Join(path, SuiteOwnerMarkerName)); err != nil {
			t.Fatalf("simulate partial removal: %v", err)
		}
		return errors.New("permission denied")
	}

	swept := sweepDeadSuiteRoots(parent, prefix, func(int) bool { return false }, func(...string) []int { return nil }, stubbornRemove)
	if len(swept) != 0 {
		t.Errorf("sweepDeadSuiteRoots() = %v, want nothing reported as removed", swept)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root should still be there: %v", err)
	}
	pid, ok := readSuiteOwnerMarker(root)
	if !ok || pid != deadPID {
		t.Fatalf("marker after a failed removal = (%d, %v), want (%d, true) so the next run retries", pid, ok, deadPID)
	}

	// And the next run, with removal working, finishes the job.
	swept = sweepDeadSuiteRoots(parent, prefix, func(int) bool { return false }, func(...string) []int { return nil }, removeSuiteRoot)
	if want := []string{root}; !reflect.DeepEqual(swept, want) {
		t.Errorf("retry swept %v, want %v", swept, want)
	}
}

// TestSweepDeadSuiteRootsRemovesReadOnlyDirs pins removeSuiteRoot's chmod
// walk: cmd/bd's suite root doubles as an isolated $HOME and can hold
// read-only Go module cache directories, which plain os.RemoveAll refuses.
func TestSweepDeadSuiteRootsRemovesReadOnlyDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	nested := filepath.Join(root, "pkg", "mod", "readonly")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "file"), []byte("x"), 0o400); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(nested, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restore write permission whatever happens, so the test's own temp
	// cleanup cannot fail.
	t.Cleanup(func() { _ = os.Chmod(nested, 0o700) })

	if err := os.RemoveAll(root); err == nil {
		t.Skip("this platform's os.RemoveAll already handles read-only directories")
	}
	if err := removeSuiteRoot(root); err != nil {
		t.Fatalf("removeSuiteRoot: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("root still present after removeSuiteRoot: %v", err)
	}
}

// TestSweepDeadSuiteRootsRefusesUnboundedGlob guards the one input that would
// turn this helper into a temp-directory shredder.
func TestSweepDeadSuiteRootsRefusesUnboundedGlob(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "anything")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SuiteOwnerMarkerName), []byte("4242\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	dead := func(int) bool { return false }
	fatalSweep := func(roots ...string) []int {
		t.Errorf("SweepOrphanedTestServers must not be called: %v", roots)
		return nil
	}
	fatalRemove := func(path string) error {
		t.Errorf("removeAll must not be called: %s", path)
		return nil
	}

	if got := sweepDeadSuiteRoots(parent, "", dead, fatalSweep, fatalRemove); got != nil {
		t.Errorf("empty prefix swept %v, want nothing", got)
	}
	if got := sweepDeadSuiteRoots("", "beads-bd-tests-", dead, fatalSweep, fatalRemove); got != nil {
		t.Errorf("empty parent dir swept %v, want nothing", got)
	}
}

// TestSweepDeadSuiteRootsSkipsSelf is the belt-and-braces check that the
// running process's own root survives, since TestMain writes its marker into
// a directory the very same glob will match on the next call.
func TestSweepDeadSuiteRootsSkipsSelf(t *testing.T) {
	parent := t.TempDir()
	const prefix = "beads-self-tests-"
	mine := filepath.Join(parent, prefix+"mine")
	if err := os.MkdirAll(mine, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := WriteSuiteOwnerMarker(mine); err != nil {
		t.Fatalf("WriteSuiteOwnerMarker: %v", err)
	}

	// Real liveness probe this time: our own PID must read as alive.
	swept := sweepDeadSuiteRoots(parent, prefix, processAlive, sweepServersUnderRoots, removeSuiteRoot)
	if len(swept) != 0 {
		t.Fatalf("sweepDeadSuiteRoots removed %v, including this process's own root", swept)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("own root %s was removed: %v", mine, err)
	}
}

// TestApplyLeakPolicyForSuite covers the exit-code arithmetic of the
// env-gated leak-as-failure rule.
func TestApplyLeakPolicyForSuite(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		code  int
		swept []int
		want  int
	}{
		{name: "no leak, passing suite", code: 0, swept: nil, want: 0},
		{name: "no leak, failing suite", code: 2, swept: nil, want: 2},
		{name: "leak warns but does not fail by default", code: 0, swept: []int{101}, want: 0},
		{name: "leak fails a passing suite when opted in", env: "1", code: 0, swept: []int{101}, want: 1},
		{name: "leak never downgrades an existing failure", env: "1", code: 2, swept: []int{101}, want: 2},
		{name: "any value other than 1 stays advisory", env: "true", code: 0, swept: []int{101}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(FailOnLeakEnv, tc.env)
			if got := ApplyLeakPolicy("internal/doltserver", tc.code, tc.swept); got != tc.want {
				t.Errorf("ApplyLeakPolicy(%d, %v) with %s=%q = %d, want %d",
					tc.code, tc.swept, FailOnLeakEnv, tc.env, got, tc.want)
			}
		})
	}
}
