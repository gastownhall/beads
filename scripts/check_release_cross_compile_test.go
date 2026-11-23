package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bazel-release-cross-compile.sh is the release-target cross-compilation
// gate: bazel.yml's pure-Go lane runs it, and it builds
// //tools/bazel:release_cross for every row of scripts/ci/release-targets.txt
// in one invocation. These tests put a fake `bazel` on PATH that records
// every invocation and can be told to fail for one platform, then assert the
// script's platform list, coverage check, flags, failure propagation and
// per-platform attribution against it, so an "always exit 0" mutation cannot
// keep the gate green with every target broken.

// fakeBazelRecordingScript stands in for bazel. `query` prints
// $FAKE_BUILT_PKGS for the deps of //tools/bazel:release_cross and
// $FAKE_WANT_PKGS for anything else; every command appends one line
// ("<command>\t<args>") to $BAZEL_CALL_LOG; `build` exits non-zero when its
// --//tools/bazel:release_platforms list names $BAZEL_FAIL_PLATFORM.
const fakeBazelRecordingScript = `#!/usr/bin/env bash
set -euo pipefail
cmd="$1"; shift
printf '%s\t%s\n' "$cmd" "$*" >> "$BAZEL_CALL_LOG"
if [ "$cmd" = query ]; then
  case "$*" in
  *"deps(//tools/bazel:release_cross)"*) printf '%s\n' $FAKE_BUILT_PKGS ;;
  *) printf '%s\n' $FAKE_WANT_PKGS ;;
  esac
  exit 0
fi
for a in "$@"; do
  case "$a" in
  --//tools/bazel:release_platforms=*)
    if [ -n "${BAZEL_FAIL_PLATFORM:-}" ] && [[ ",${a#*=}," == *",$BAZEL_FAIL_PLATFORM,"* ]]; then
      echo "simulated build failure for $a" >&2
      exit 1
    fi ;;
  esac
done
exit 0
`

const testPkgs = "cmd/bd internal/types"

// runBazelReleaseCrossCompile copies the real script next to a
// caller-supplied manifest (so the test does not track the real
// release-targets.txt), puts the fake bazel first on PATH and runs it.
func runBazelReleaseCrossCompile(t *testing.T, manifest, failPlatform, builtPkgs string, args ...string) (out string, err error, calls []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script is a Bash boundary")
	}
	script, readErr := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "ci", "bazel-release-cross-compile.sh"))
	if readErr != nil {
		t.Fatalf("read bazel-release-cross-compile.sh: %v", readErr)
	}
	dir := t.TempDir()
	scriptsCI := filepath.Join(dir, "scripts", "ci")
	fakeBin := filepath.Join(dir, "fakebin")
	for _, d := range []string{scriptsCI, fakeBin} {
		if mkErr := os.MkdirAll(d, 0o700); mkErr != nil {
			t.Fatal(mkErr)
		}
	}
	scriptPath := filepath.Join(scriptsCI, "bazel-release-cross-compile.sh")
	for path, content := range map[string]string{
		scriptPath: string(script),
		filepath.Join(scriptsCI, "release-targets.txt"): manifest,
		filepath.Join(fakeBin, "bazel"):                 fakeBazelRecordingScript,
	} {
		if writeErr := os.WriteFile(path, []byte(content), 0o700); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	logPath := filepath.Join(dir, "bazel-calls.log")
	if writeErr := os.WriteFile(logPath, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	cmd := exec.Command("bash", append([]string{scriptPath}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"BAZEL_CALL_LOG="+logPath,
		"BAZEL_FAIL_PLATFORM="+failPlatform,
		"FAKE_WANT_PKGS="+testPkgs,
		"FAKE_BUILT_PKGS="+builtPkgs,
	)
	outBytes, runErr := cmd.CombinedOutput()
	logBytes, _ := os.ReadFile(logPath)
	for _, line := range strings.Split(strings.TrimRight(string(logBytes), "\n"), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return string(outBytes), runErr, calls
}

const testReleaseManifest = "# comment\n" +
	"linux amd64\n" +
	"\n" +
	"windows arm64\n" +
	"darwin arm64\n"

func buildCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "build\t") {
			out = append(out, c)
		}
	}
	return out
}

const releaseFlag = "--//tools/bazel:release_platforms="

// One `bazel build --keep_going` of //tools/bazel:release_cross with every
// manifest row as a rules_go platform name, the caller's extra flags passed
// through, after a coverage check that queries the Go packages bazel knows
// and the ones release_cross reaches.
func TestBazelReleaseCrossCompileBuildsEveryManifestPlatform(t *testing.T) {
	out, err, calls := runBazelReleaseCrossCompile(t, testReleaseManifest, "", testPkgs, "--config=remote-exec")
	if err != nil {
		t.Fatalf("script failed with no simulated failures: %v\noutput:\n%s", err, out)
	}
	if len(calls) != 3 {
		t.Fatalf("bazel calls = %q, want two queries and one build", calls)
	}
	for i, want := range []string{
		`kind("go_library|go_binary", //...) except siblings(attr(tags, "\bcgo-only\b", `,
		"deps(//tools/bazel:release_cross)",
	} {
		if !strings.HasPrefix(calls[i], "query\t") || !strings.Contains(calls[i], want) || !strings.Contains(calls[i], "--output=package") {
			t.Errorf("bazel call %d = %q, want a package query containing %q", i, calls[i], want)
		}
	}
	build := calls[2]
	for _, required := range []string{
		"build\t--keep_going",
		releaseFlag + "linux_amd64,windows_arm64,darwin_arm64",
		"--config=remote-exec -- //tools/bazel:release_cross",
	} {
		if !strings.Contains(build, required) {
			t.Errorf("build call %q does not contain %q", build, required)
		}
	}
	if !strings.Contains(out, "release cross-compilation passed: linux/amd64 windows/arm64 darwin/arm64") {
		t.Errorf("output does not report success for every target:\n%s", out)
	}
}

// A Go package release_cross does not reach fails the script before any
// build, naming the package.
func TestBazelReleaseCrossCompileFailsOnUncoveredPackage(t *testing.T) {
	out, err, calls := runBazelReleaseCrossCompile(t, testReleaseManifest, "", "cmd/bd")
	if err == nil {
		t.Fatalf("script exited 0 with an uncovered package; want non-zero\noutput:\n%s", out)
	}
	if !strings.Contains(out, "::error::Go packages //tools/bazel:release_cross does not build") || !strings.Contains(out, "internal/types") {
		t.Errorf("output does not name the uncovered package:\n%s", out)
	}
	if b := buildCalls(calls); len(b) != 0 {
		t.Errorf("bazel build ran despite an uncovered package: %q", b)
	}
}

// A failing platform fails the script; each platform is then rebuilt alone,
// so the output names exactly the failing ones.
func TestBazelReleaseCrossCompilePropagatesFailureAndNamesPlatform(t *testing.T) {
	out, err, calls := runBazelReleaseCrossCompile(t, testReleaseManifest, "windows_arm64", testPkgs)
	if err == nil {
		t.Fatalf("script exited 0 with a failing platform; want non-zero\noutput:\n%s", out)
	}
	builds := buildCalls(calls)
	if len(builds) != 4 {
		t.Fatalf("bazel build calls = %q, want the full build plus one per platform", builds)
	}
	for i, platform := range []string{"linux_amd64", "windows_arm64", "darwin_arm64"} {
		if !strings.Contains(builds[i+1], releaseFlag+platform+" ") {
			t.Errorf("attribution build %d = %q, want %s alone", i, builds[i+1], platform)
		}
	}
	for _, want := range []string{
		"::error::release cross-compilation failed for windows/arm64",
		"::error::release cross-compilation failed for: windows/arm64",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"failed for linux/amd64", "failed for darwin/arm64"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output blames a passing platform (%q):\n%s", unwanted, out)
		}
	}
}

// An empty manifest must fail loudly rather than pass with nothing built.
func TestBazelReleaseCrossCompileEmptyManifestFails(t *testing.T) {
	out, err, calls := runBazelReleaseCrossCompile(t, "# nothing\n\n", "", testPkgs)
	if err == nil {
		t.Fatalf("script exited 0 for an empty manifest; want non-zero\noutput:\n%s", out)
	}
	if !strings.Contains(out, "::error::no release targets in") {
		t.Errorf("output does not report the empty manifest:\n%s", out)
	}
	if len(calls) != 0 {
		t.Errorf("bazel ran for an empty manifest: %q", calls)
	}
}

// A malformed row (not exactly GOOS GOARCH) must fail, not be skipped.
func TestBazelReleaseCrossCompileMalformedRowFails(t *testing.T) {
	out, err, _ := runBazelReleaseCrossCompile(t, "linux amd64 unix\n", "", testPkgs)
	if err == nil {
		t.Fatalf("script exited 0 for a malformed row; want non-zero\noutput:\n%s", out)
	}
	if !strings.Contains(out, "::error::malformed row") {
		t.Errorf("output does not report the malformed row:\n%s", out)
	}
}
