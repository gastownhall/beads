package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Review SF-6 (2026-10-03): check-release-cross-compile.sh (the F7a fold of
// pr.yml's check-release-target-cross-compilation matrix into two per-group
// legs) had no behavioural test. A planted "always exit 0" mutation passed
// every other policy test, which would make the required
// CHECK_RELEASE_TARGET_CROSS_COMPILATION token green even with every target
// broken. These tests put a fake `go` on PATH that records every invocation
// and can be told to fail for one target, then assert the script's actual
// failure-propagation, env/flag, and group-coverage behaviour against it.

// fakeGoRecordingScript is a stand-in for `go build` that appends one line
// per invocation to $GO_CALL_LOG (goos, goarch, CGO_ENABLED, and the
// remaining args) and exits non-zero only when GOOS/GOARCH matches
// $GO_FAIL_TARGET.
const fakeGoRecordingScript = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\t%s\t%s\t%s\n' "${GOOS:-}" "${GOARCH:-}" "${CGO_ENABLED:-}" "$*" >> "$GO_CALL_LOG"
if [ "${GOOS:-}/${GOARCH:-}" = "${GO_FAIL_TARGET:-}" ]; then
  echo "simulated build failure for ${GOOS:-}/${GOARCH:-}" >&2
  exit 1
fi
exit 0
`

// runCheckReleaseCrossCompile copies the real check-release-cross-compile.sh
// into a throwaway directory alongside a caller-supplied manifest (so the
// test does not depend on the real scripts/ci/release-targets.txt growing or
// shrinking), puts a fake `go` on PATH ahead of everything else, and runs the
// script for the given group. failTarget (e.g. "linux/amd64") is the single
// GOOS/GOARCH the fake go fails for; empty means every target succeeds.
func runCheckReleaseCrossCompile(t *testing.T, group, manifest, failTarget string) (out string, err error, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script is a Bash boundary")
	}
	script, readErr := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "ci", "check-release-cross-compile.sh"))
	if readErr != nil {
		t.Fatalf("read check-release-cross-compile.sh: %v", readErr)
	}
	dir := t.TempDir()
	scriptsCI := filepath.Join(dir, "scripts", "ci")
	if mkErr := os.MkdirAll(scriptsCI, 0o700); mkErr != nil {
		t.Fatal(mkErr)
	}
	scriptPath := filepath.Join(scriptsCI, "check-release-cross-compile.sh")
	if writeErr := os.WriteFile(scriptPath, script, 0o700); writeErr != nil {
		t.Fatal(writeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(scriptsCI, "release-targets.txt"), []byte(manifest), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	fakeBin := filepath.Join(dir, "fakebin")
	if mkErr := os.MkdirAll(fakeBin, 0o700); mkErr != nil {
		t.Fatal(mkErr)
	}
	if writeErr := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(fakeGoRecordingScript), 0o700); writeErr != nil {
		t.Fatal(writeErr)
	}
	logPath := filepath.Join(dir, "go-calls.log")
	if writeErr := os.WriteFile(logPath, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	cmd := exec.Command("bash", scriptPath, group)
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"GO_CALL_LOG="+logPath,
		"GO_FAIL_TARGET="+failTarget,
	)
	outBytes, runErr := cmd.CombinedOutput()
	logBytes, _ := os.ReadFile(logPath)
	return string(outBytes), runErr, string(logBytes)
}

const testManifest = "linux amd64 unix\n" +
	"darwin arm64 unix\n" +
	"windows amd64 desktop\n"

// A failure in one target must propagate as a non-zero exit (the gate's
// whole point), must still attempt every other target in the group (so a PR
// touching two platforms sees both failures in one run), must name the
// failing target in an ::error:: line, and must pass CGO_ENABLED=0 and the
// gms_pure_go tag through to every invocation, not just the failing one.
func TestCheckReleaseCrossCompilePropagatesFailureAndCoversGroup(t *testing.T) {
	out, err, log := runCheckReleaseCrossCompile(t, "unix", testManifest, "linux/amd64")
	if err == nil {
		t.Fatalf("script exited 0 with a failing target; want non-zero\noutput:\n%s", out)
	}
	if !strings.Contains(out, "::error::cross-compilation failed for linux/amd64") {
		t.Errorf("output does not name the failing target:\n%s", out)
	}
	if !strings.Contains(out, "::error::cross-compilation failed for: linux/amd64") {
		t.Errorf("output does not contain the final failed-targets summary line:\n%s", out)
	}
	// Both unix-group targets must have been attempted, in spite of the first
	// one failing.
	for _, want := range []string{"linux\tamd64", "darwin\tarm64"} {
		if !strings.Contains(log, want) {
			t.Errorf("fake go call log is missing an attempt for %s; every group target must run even after an earlier one fails:\n%s", want, log)
		}
	}
	// The desktop-group target must not have been touched by a unix-group run.
	if strings.Contains(log, "windows\tamd64") {
		t.Errorf("fake go call log recorded a desktop-group target during a unix-group run:\n%s", log)
	}
	for _, line := range strings.Split(strings.TrimRight(log, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			t.Fatalf("malformed fake go call log line %q", line)
		}
		if fields[2] != "0" {
			t.Errorf("fake go call %q: CGO_ENABLED = %q, want \"0\"", line, fields[2])
		}
		if !strings.Contains(fields[3], "-tags gms_pure_go ./...") {
			t.Errorf("fake go call %q: args = %q, want them to contain \"-tags gms_pure_go ./...\"", line, fields[3])
		}
	}
}

// Every target in a group still passes without a failTarget.
func TestCheckReleaseCrossCompileAllTargetsPass(t *testing.T) {
	out, err, log := runCheckReleaseCrossCompile(t, "unix", testManifest, "")
	if err != nil {
		t.Fatalf("script failed with no simulated failures: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "cross-compilation passed for group 'unix'") {
		t.Errorf("output does not report success for the unix group:\n%s", out)
	}
	for _, want := range []string{"linux\tamd64", "darwin\tarm64"} {
		if !strings.Contains(log, want) {
			t.Errorf("fake go call log is missing an attempt for %s:\n%s", want, log)
		}
	}
}

// A group name absent from the manifest must fail loudly, not silently
// succeed with zero targets built.
func TestCheckReleaseCrossCompileUnknownGroupFails(t *testing.T) {
	out, err, log := runCheckReleaseCrossCompile(t, "bogus-group", testManifest, "")
	if err == nil {
		t.Fatalf("script exited 0 for an unknown group; want non-zero\noutput:\n%s", out)
	}
	if !strings.Contains(out, "::error::no release targets found for group 'bogus-group'") {
		t.Errorf("output does not report the unknown group:\n%s", out)
	}
	if log != "" {
		t.Errorf("fake go was invoked for an unknown group; want zero invocations, got log:\n%s", log)
	}
}
