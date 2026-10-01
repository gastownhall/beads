//go:build !windows

package testutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// EnsureDoltContainerForTestMain and DoltUnavailableForTestMain are the pair
// every Dolt-governed TestMain runs (internal/utils/testmain_test.go and its
// siblings): the first tries to bring up the shared Dolt server, the second
// decides whether the error it returned stops the package or lets it run with
// its Dolt tests skipped. The verdict ends in a process exit and reads
// process-wide state (sync.Once singletons, the environment), so each case
// runs that same sequence in a re-exec'd copy of this test binary and reads
// the verdict from its exit status, the way `go test` sees a TestMain.
//
// Nothing here needs a container runtime or a real dolt. The local backend
// (BEADS_TEST_DOLT_SERVER=local) starts whatever BEADS_TEST_DOLT_BINARY
// names, so a stand-in CLI can pass the version probe (the environment
// reports ready) and then refuse to serve (starting the shared server really
// fails). That is the shape of a container runtime that reports ready and
// then cannot start the reaper.
const testMainVerdictHelperEnv = "BEADS_TESTUTIL_TESTMAIN_VERDICT_HELPER"

// Exit statuses of the helper process. None is 1 (what the go tool reports
// for a failed test binary) or 2 (a panic), so a crash cannot pass for a
// verdict.
const (
	// verdictPackageRuns: EnsureDoltContainerForTestMain failed and
	// DoltUnavailableForTestMain said to carry on; the package runs and its
	// Dolt tests skip.
	verdictPackageRuns = 10
	// verdictServerUp: EnsureDoltContainerForTestMain succeeded.
	verdictServerUp = 11
	// verdictPackageFails: DoltUnavailableForTestMain said the TestMain must
	// exit non-zero.
	verdictPackageFails = 12
)

func verdictName(code int) string {
	switch code {
	case verdictPackageRuns:
		return "package runs, Dolt tests skip"
	case verdictServerUp:
		return "shared server up"
	case verdictPackageFails:
		return "package fails"
	default:
		return fmt.Sprintf("helper exited %d without a verdict", code)
	}
}

// TestDoltTestMainVerdictHelper is the child half of runTestMainVerdict. It
// is inert unless re-exec'd with the marker set.
func TestDoltTestMainVerdictHelper(t *testing.T) {
	if os.Getenv(testMainVerdictHelperEnv) != "1" {
		t.Skip("helper process for runTestMainVerdict")
	}
	if err := EnsureDoltContainerForTestMain(); err != nil {
		if DoltUnavailableForTestMain(err) {
			os.Exit(verdictPackageFails)
		}
		os.Exit(verdictPackageRuns)
	}
	TerminateDoltContainer()
	os.Exit(verdictServerUp)
}

// runTestMainVerdict runs the TestMain sequence in a child with env applied
// on top of a scrubbed environment and returns its verdict and output. Every
// BEADS_TEST_* the surrounding run set is dropped first: scripts/test.sh
// exports BEADS_TEST_SKIP=dolt and the Dolt lanes export
// BEADS_TEST_REQUIRE_DOLT_CONTAINER=1, and either would decide the case
// before the case did.
func runTestMainVerdict(t *testing.T, env ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestDoltTestMainVerdictHelper$", "-test.count=1") // #nosec G204 -- re-exec of this test binary
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BEADS_TEST_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	// A later duplicate key wins, so env can override PATH.
	cmd.Env = append(cmd.Env, testMainVerdictHelperEnv+"=1", "TMPDIR="+t.TempDir())
	cmd.Env = append(cmd.Env, env...)

	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		t.Fatalf("helper exited 0 without reaching a verdict:\n%s", out)
	case !errors.As(err, &exitErr):
		t.Fatalf("running helper: %v\n%s", err, out)
	}
	return exitErr.ExitCode(), string(out)
}

// fakeDoltCLI writes a stand-in `dolt`: it answers `version` with the pinned
// release, so the local backend accepts it, and fails every other command,
// so `dolt sql-server` never comes up. It returns the binary and a log that
// gets one line per invocation.
func fakeDoltCLI(t *testing.T) (bin, invocations string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "dolt")
	invocations = filepath.Join(dir, "invocations.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%s'
case "$1" in
version) echo "dolt version %s" ;;
*) echo "fake dolt: cannot run: $*" >&2; exit 1 ;;
esac
`, invocations, pinnedDoltVersion)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { // #nosec G306 -- test-only executable
		t.Fatal(err)
	}
	return bin, invocations
}

func TestDoltTestMainVerdict(t *testing.T) {
	// The failure this file exists for: the environment says a Dolt server can
	// run, the server then fails to start, and a TestMain that only warns
	// lets the package report ok having run none of its Dolt tests. Nobody
	// asked for that skip, so it must stop the package even without
	// BEADS_TEST_REQUIRE_DOLT_CONTAINER=1.
	t.Run("a server that fails to start stops the package", func(t *testing.T) {
		bin, invocations := fakeDoltCLI(t)
		code, out := runTestMainVerdict(t,
			EnvDoltServerBackend+"=local",
			"BEADS_TEST_DOLT_BINARY="+bin,
		)
		log, _ := os.ReadFile(invocations)
		if !strings.Contains(string(log), "sql-server") {
			t.Fatalf("the shared server was never started, so this case did not reach the failure it is about\ndolt invocations: %q\nhelper output:\n%s", log, out)
		}
		if code != verdictPackageFails {
			t.Fatalf("verdict = %q, want %q: a Dolt server that failed to start let the package run and report ok\nhelper output:\n%s",
				verdictName(code), verdictName(verdictPackageFails), out)
		}
		if !strings.Contains(out, "FATAL:") {
			t.Errorf("output has no FATAL line naming the failure:\n%s", out)
		}
		if !strings.Contains(out, "BEADS_TEST_SKIP=dolt") {
			t.Errorf("output does not point at BEADS_TEST_SKIP=dolt, the deliberate way to skip:\n%s", out)
		}
	})

	// Everything below must hold before and after that change: only a server
	// that fails to start after the environment said it could run escalates.

	// BEADS_TEST_SKIP=dolt is the deliberate opt-out that scripts/test.sh sets
	// for every ordinary run; it is decided before anything is started.
	t.Run("BEADS_TEST_SKIP=dolt skips without starting a server", func(t *testing.T) {
		bin, invocations := fakeDoltCLI(t)
		code, out := runTestMainVerdict(t,
			"BEADS_TEST_SKIP=dolt",
			EnvDoltServerBackend+"=local",
			"BEADS_TEST_DOLT_BINARY="+bin,
		)
		if code != verdictPackageRuns {
			t.Fatalf("verdict = %q, want %q\nhelper output:\n%s", verdictName(code), verdictName(verdictPackageRuns), out)
		}
		if log, _ := os.ReadFile(invocations); len(log) != 0 {
			t.Errorf("BEADS_TEST_SKIP=dolt still ran the dolt CLI: %q", log)
		}
	})

	// A machine with no container runtime is a precondition, not a start
	// failure: contributors without Docker keep running the rest of the suite.
	t.Run("no container runtime skips", func(t *testing.T) {
		code, out := runTestMainVerdict(t, "PATH="+t.TempDir())
		if code != verdictPackageRuns {
			t.Fatalf("verdict = %q, want %q\nhelper output:\n%s", verdictName(code), verdictName(verdictPackageRuns), out)
		}
		if !strings.Contains(out, "WARN:") {
			t.Errorf("output has no WARN line for the skipped Dolt tests:\n%s", out)
		}
	})

	// Lanes that exist to run the Dolt suites set this so they cannot pass
	// having run nothing; it outranks every precondition, including the
	// opt-out.
	t.Run("BEADS_TEST_REQUIRE_DOLT_CONTAINER=1 stops the package when Dolt is unavailable", func(t *testing.T) {
		code, out := runTestMainVerdict(t,
			"BEADS_TEST_SKIP=dolt",
			EnvRequireDoltContainer+"=1",
		)
		if code != verdictPackageFails {
			t.Fatalf("verdict = %q, want %q\nhelper output:\n%s", verdictName(code), verdictName(verdictPackageFails), out)
		}
		if !strings.Contains(out, "FATAL:") {
			t.Errorf("output has no FATAL line:\n%s", out)
		}
	})

	// The cases above run the verdict in the helper; this one is about the
	// helper itself. It is an ordinary Test* function, so every run of this
	// package executes it with no marker set, and it has to come out as a
	// pass there: a t.Skip would put a SKIP in every run for a test that has
	// nothing to run outside the re-exec, which reads as coverage that went
	// missing.
	t.Run("the helper passes rather than skips outside a verdict run", func(t *testing.T) {
		exe, err := os.Executable()
		if err != nil {
			t.Fatalf("os.Executable: %v", err)
		}
		cmd := exec.Command(exe, "-test.run=^TestDoltTestMainVerdictHelper$", "-test.count=1", "-test.v") // #nosec G204 -- re-exec of this test binary
		// A later duplicate key wins, so an empty marker outranks one the
		// surrounding run set.
		cmd.Env = append(os.Environ(), testMainVerdictHelperEnv+"=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper run with no marker failed: %v\n%s", err, out)
		}
		if strings.Contains(string(out), "--- SKIP") || !strings.Contains(string(out), "--- PASS: TestDoltTestMainVerdictHelper") {
			t.Fatalf("the helper must report a pass, not a skip, when run with no marker:\n%s", out)
		}
	})
}
