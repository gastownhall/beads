package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// windowsTestBinaryManifestEntry mirrors one non-comment, non-blank line of
// scripts/ci/windows-test-binaries.txt: "name cgo tags package".
type windowsTestBinaryManifestEntry struct {
	name string
	cgo  string
	tags string
	pkg  string
}

func readWindowsTestBinariesManifest(t *testing.T) []windowsTestBinaryManifestEntry {
	t.Helper()

	path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", "windows-test-binaries.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var entries []windowsTestBinaryManifestEntry
	for _, line := range strings.Split(string(data), "\n") {
		if idx := strings.Index(line, "#"); idx != -1 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			t.Fatalf("malformed manifest line %q: want 4 whitespace-separated fields (name cgo tags package), got %d", line, len(fields))
		}
		entries = append(entries, windowsTestBinaryManifestEntry{name: fields[0], cgo: fields[1], tags: fields[2], pkg: fields[3]})
	}
	return entries
}

// TestWindowsTestBinariesManifestPinned pins the advisory-phase manifest to
// exactly the three binaries its two "-prebuilt" consumer jobs need (see the
// manifest's own header comment for why the rollout is scoped this way).
// Growing the manifest without growing its consumers, or vice versa, is
// exactly the drift TestWindowsTestBinariesConsumersMatchManifest below
// exists to catch; this test instead catches an unreviewed change to the
// three pinned entries themselves (toolchain flags, package, or tags
// silently drifting out from under the native jobs they mirror).
func TestWindowsTestBinariesManifestPinned(t *testing.T) {
	entries := readWindowsTestBinariesManifest(t)
	want := []windowsTestBinaryManifestEntry{
		{name: "bd.exe", cgo: "1", tags: "gms_pure_go", pkg: "./cmd/bd"},
		{name: "cmd-bd-cgo.test.exe", cgo: "1", tags: "gms_pure_go", pkg: "./cmd/bd"},
		{name: "cmd-bd-nocgo.test.exe", cgo: "0", tags: "gms_pure_go", pkg: "./cmd/bd"},
	}
	if len(entries) != len(want) {
		t.Fatalf("manifest has %d entries, want %d: got %+v", len(entries), len(want), entries)
	}
	for i, got := range entries {
		if got != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got, want[i])
		}
	}
}

// TestWindowsCrossCompileToolchainPinned pins build-windows-test-binaries.sh
// to the same mingw-w64 cross-compiler release.yml's goreleaser job uses for
// the shipped bd-windows-amd64 binary (.goreleaser.yml), so the two toolchains
// cannot silently diverge.
func TestWindowsCrossCompileToolchainPinned(t *testing.T) {
	path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", "build-windows-test-binaries.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		`env_args=(GOOS=windows GOARCH=amd64 CGO_ENABLED="$cgo")`,
		`env_args+=(CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++)`,
		// F4 review nit: the resolved mingw-w64 version is logged (once, up
		// front) so an advisory-phase run's log can confirm which compiler
		// release actually built the binaries, the same way "== host go
		// toolchain ==" confirms the Go side.
		`x86_64-w64-mingw32-gcc --version`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("build-windows-test-binaries.sh does not contain %q", want)
		}
	}

	goreleaser := readGoreleaserBuilds(t)
	var windowsBuild *goreleaserBuild
	for i := range goreleaser {
		if goreleaser[i].ID == "bd-windows-amd64" {
			windowsBuild = &goreleaser[i]
			break
		}
	}
	if windowsBuild == nil {
		t.Fatal(".goreleaser.yml has no bd-windows-amd64 build to compare against")
	}
	wantEnv := []string{"CGO_ENABLED=1", "CC=x86_64-w64-mingw32-gcc", "CXX=x86_64-w64-mingw32-g++"}
	if !equalStrings(windowsBuild.Env, wantEnv) {
		t.Errorf(".goreleaser.yml bd-windows-amd64 build env = %v, want %v (build-windows-test-binaries.sh's cgo=1 entries are pinned to match this exact toolchain)", windowsBuild.Env, wantEnv)
	}
}

// winBinRefPattern matches `.../win-bin/<name>` references in step `run`
// bodies, capturing the file name so it can be checked against the manifest.
var winBinRefPattern = regexp.MustCompile(`win-bin/([A-Za-z0-9_.-]+)`)

// TestWindowsTestBinariesConsumersMatchManifest confirms every binary name
// the "-prebuilt" jobs reference actually exists in the manifest that builds
// it - a renamed manifest entry (or a typo'd reference) fails a Windows job
// at runtime with a missing-file error; this catches it at review time
// instead.
func TestWindowsTestBinariesConsumersMatchManifest(t *testing.T) {
	entries := readWindowsTestBinariesManifest(t)
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.name] = true
	}

	pr := readCIWorkflow(t, "pr.yml")
	referenced := make(map[string]bool)
	for _, jobName := range []string{"test-windows-liveness-prebuilt", "worktree-remove-windows-prebuilt"} {
		job := pr.job(t, jobName)
		for _, step := range job.Steps {
			for _, env := range step.Env {
				for _, m := range winBinRefPattern.FindAllStringSubmatch(env, -1) {
					referenced[m[1]] = true
					if !names[m[1]] {
						t.Errorf("%s step %q env references win-bin/%s, not in manifest %+v", jobName, step.Name, m[1], entries)
					}
				}
			}
			for _, m := range winBinRefPattern.FindAllStringSubmatch(step.Run, -1) {
				referenced[m[1]] = true
				if !names[m[1]] {
					t.Errorf("%s step %q run references win-bin/%s, not in manifest %+v", jobName, step.Name, m[1], entries)
				}
			}
		}
	}
	// Every manifest entry this advisory phase builds should be consumed by
	// at least one of the two twins - an unused entry is dead weight in the
	// cross-compile job for no benefit.
	for name := range names {
		if !referenced[name] {
			t.Errorf("manifest entry %q is never referenced by test-windows-liveness-prebuilt or worktree-remove-windows-prebuilt", name)
		}
	}
}

// TestWindowsTestBinariesArtifactNameConsistent pins the upload/download
// artifact name shared by windows-test-binaries and its two consumers, and
// that both consumers declare the producer as a `needs` dependency (without
// it, the download step would race the upload and fail intermittently
// instead of deterministically).
func TestWindowsTestBinariesArtifactNameConsistent(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	producer := pr.job(t, "windows-test-binaries")
	upload := producer.step(t, "Upload Windows test binaries")
	uploadName := upload.With["name"]
	if uploadName != "windows-test-binaries" {
		t.Fatalf("producer upload name = %q, want %q", uploadName, "windows-test-binaries")
	}

	for _, jobName := range []string{"test-windows-liveness-prebuilt", "worktree-remove-windows-prebuilt"} {
		job := pr.job(t, jobName)
		download := job.step(t, "Download Windows test binaries")
		if got := download.With["name"]; got != uploadName {
			t.Errorf("%s download name = %q, want %q (matching the producer's upload name)", jobName, got, uploadName)
		}
		if len(job.Needs) != 1 || job.Needs[0] != "windows-test-binaries" {
			t.Errorf("%s needs = %v, want exactly [windows-test-binaries]", jobName, job.Needs)
		}
	}
}

// runFlagLiteral extracts the `-run '...'` argument from a step's run body.
func runFlagLiteral(t *testing.T, step ciWorkflowStep) string {
	t.Helper()
	m := regexp.MustCompile(`-run '([^']+)'`).FindStringSubmatch(step.Run)
	if m == nil {
		t.Fatalf("step %q run body has no -run '...' literal:\n%s", step.Name, step.Run)
	}
	return m[1]
}

// TestWindowsTestBinariesSelectSameTestsAsNativeJobs is the F4.4 side-by-side
// policy test: the cross-built "-prebuilt" jobs must select exactly the same
// tests, with the same CGO_ENABLED selection, as the native windows-latest
// jobs they are advisory twins of. A -run literal or CGO_ENABLED value that
// drifts between the two paths would mean the cross-build is silently
// validating a different test subset than the required native lane -
// defeating the entire point of running them side by side before any flip.
func TestWindowsTestBinariesSelectSameTestsAsNativeJobs(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")

	native := pr.job(t, "test-windows-liveness")
	prebuilt := pr.job(t, "test-windows-liveness-prebuilt")

	nativeGlobalPrime := native.step(t, "Run native Windows global Prime override")
	prebuiltGlobalPrime := prebuilt.step(t, "Run native Windows global Prime override (prebuilt)")
	if n, p := runFlagLiteral(t, nativeGlobalPrime), runFlagLiteral(t, prebuiltGlobalPrime); n != p {
		t.Errorf("global-prime -run literal mismatch: native %q prebuilt %q", n, p)
	}
	if got := nativeGlobalPrime.Env["CGO_ENABLED"]; got != "1" {
		t.Fatalf("test-windows-liveness CGO_ENABLED = %q, want \"1\" (manifest entries bd.exe/cmd-bd-cgo.test.exe assume this)", got)
	}

	nativeLiveness := native.step(t, "Run Windows liveness regression test")
	prebuiltLiveness := prebuilt.step(t, "Run Windows liveness regression test (prebuilt)")
	if n, p := runFlagLiteral(t, nativeLiveness), runFlagLiteral(t, prebuiltLiveness); n != p {
		t.Errorf("liveness regression -run literal mismatch: native %q prebuilt %q", n, p)
	}

	nativeWorktree := pr.job(t, "worktree-remove-windows").step(t, "Run native Windows worktree removal boundary tests")
	prebuiltWorktree := pr.job(t, "worktree-remove-windows-prebuilt").step(t, "Run native Windows worktree removal boundary tests (prebuilt)")
	if n, p := runFlagLiteral(t, nativeWorktree), runFlagLiteral(t, prebuiltWorktree); n != p {
		t.Errorf("worktree-remove -run literal mismatch: native %q prebuilt %q", n, p)
	}
	if got := nativeWorktree.Env["CGO_ENABLED"]; got != "0" {
		t.Fatalf("worktree-remove-windows CGO_ENABLED = %q, want \"0\" (manifest entry cmd-bd-nocgo.test.exe assumes this)", got)
	}
}

// runGoTestBinaryInvocationPattern matches a scripts/ci/run-go-test-binary.sh
// invocation's binary path argument and the flags that follow it, so tests
// can pin the exact PKGDIR and flag set passed to a prebuilt binary without
// having to byte-match the entire run body.
var runGoTestBinaryInvocationPattern = regexp.MustCompile(`run-go-test-binary\.sh "[^"]*/win-bin/([A-Za-z0-9_.-]+)" (\S+) (.*)`)

// jobWinBinNames returns the distinct win-bin/<name> binaries a job's steps
// reference, across both `run` bodies and step `env` values.
func jobWinBinNames(job ciWorkflowJob) map[string]bool {
	names := make(map[string]bool)
	for _, step := range job.Steps {
		for _, m := range winBinRefPattern.FindAllStringSubmatch(step.Run, -1) {
			names[m[1]] = true
		}
		for _, env := range step.Env {
			for _, m := range winBinRefPattern.FindAllStringSubmatch(env, -1) {
				names[m[1]] = true
			}
		}
	}
	return names
}

// TestWindowsPrebuiltJobsPinnedToExactBinaryAndArgs closes the gap left by
// TestWindowsTestBinariesConsumersMatchManifest (which only checks referenced
// binaries exist in the manifest, not which job references which one) and by
// TestWindowsTestBinariesSelectSameTestsAsNativeJobs (which checks -run
// literals and the *native* job's CGO_ENABLED, not the prebuilt binary name
// or its invocation flags). Without this, swapping which "-prebuilt" job
// downloads/runs cmd-bd-cgo.test.exe vs. cmd-bd-nocgo.test.exe, dropping
// -count=1 from a run-go-test-binary.sh invocation, or changing the PKGDIR
// argument passed to it, would all pass every other test in this file.
func TestWindowsPrebuiltJobsPinnedToExactBinaryAndArgs(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")

	cases := []struct {
		jobName    string
		wantBinary string
	}{
		{"test-windows-liveness-prebuilt", "cmd-bd-cgo.test.exe"},
		{"worktree-remove-windows-prebuilt", "cmd-bd-nocgo.test.exe"},
	}
	for _, c := range cases {
		job := pr.job(t, c.jobName)

		// Exactly one *.test.exe referenced, and it is the expected one
		// (catches a cgo/nocgo swap between the two twins). bd.exe (the
		// plain executable, not a go-test-c binary) is referenced too by
		// test-windows-liveness-prebuilt and is out of scope here.
		testBinaries := make(map[string]bool)
		for name := range jobWinBinNames(job) {
			if strings.HasSuffix(name, ".test.exe") {
				testBinaries[name] = true
			}
		}
		if len(testBinaries) != 1 || !testBinaries[c.wantBinary] {
			t.Errorf("%s references *.test.exe binaries %v, want exactly {%q}", c.jobName, testBinaries, c.wantBinary)
		}

		// Every step that actually exercises the test binary (whether via a
		// direct run-go-test-binary.sh invocation or, for
		// test-windows-liveness-prebuilt's first step, through
		// scripts/test.sh's BEADS_TEST_PREBUILT_TEST_BINARY short-circuit)
		// passes -count=1 (catches a dropped -count=1 on either path).
		for _, step := range job.Steps {
			if strings.Contains(step.Run, ".test.exe") && !strings.Contains(step.Run, "-count=1") {
				t.Errorf("%s step %q exercises the prebuilt test binary without -count=1", c.jobName, step.Name)
			}
		}

		// Every direct run-go-test-binary.sh invocation in the job also
		// passes the expected binary and PKGDIR (catches a cgo/nocgo swap or
		// a PKGDIR change on this path specifically).
		sawInvocation := false
		for _, step := range job.Steps {
			for _, m := range runGoTestBinaryInvocationPattern.FindAllStringSubmatch(step.Run, -1) {
				sawInvocation = true
				binary, pkgdir, rest := m[1], m[2], m[3]
				if binary != c.wantBinary {
					t.Errorf("%s step %q invokes run-go-test-binary.sh with binary %q, want %q", c.jobName, step.Name, binary, c.wantBinary)
				}
				if pkgdir != "./cmd/bd" {
					t.Errorf("%s step %q invokes run-go-test-binary.sh with PKGDIR %q, want \"./cmd/bd\"", c.jobName, step.Name, pkgdir)
				}
				if !regexp.MustCompile(`(^|\s)-count=1(\s|$)`).MatchString(rest) {
					t.Errorf("%s step %q run-go-test-binary.sh invocation is missing -count=1: %q", c.jobName, step.Name, rest)
				}
			}
		}
		if !sawInvocation {
			t.Errorf("%s has no direct run-go-test-binary.sh invocation; this test's PKGDIR/-count=1 checks did not run", c.jobName)
		}
	}
}

// fakeGoTestBinaryForTimeoutProbe is a stand-in `go test -c` binary used only
// to observe the exact -test.* argv scripts/ci/run-go-test-binary.sh builds,
// without a real go toolchain. It is deliberately simpler than
// fakePrebuiltTestBinary in test_script_test.go (which also records cwd and
// BEADS_TEST_REPO_ROOT): this probe only needs argv.
const fakeGoTestBinaryForTimeoutProbe = `#!/usr/bin/env bash
set -euo pipefail
for a in "$@"; do
    printf 'arg=%s\n' "$a"
done >"$FAKE_GO_TEST_BINARY_LOG"
`

// TestRunGoTestBinaryDefaultsTimeoutWhenOmitted is the regression test for
// F4 review SF-3: scripts/ci/run-go-test-binary.sh must inject the same
// -test.timeout=10m default `go test` itself injects when a caller omits
// -timeout, and the unconditional -test.paniconexit0 flag Go 1.14+ always
// adds. pr.yml's two direct invocations of this script
// (test-windows-liveness-prebuilt, worktree-remove-windows-prebuilt; see
// TestWindowsPrebuiltJobsPinnedToExactBinaryAndArgs above) both omit
// -timeout, relying on exactly this default -- unlike every path that goes
// through scripts/test.sh, which always supplies its own -timeout default
// (25m, or $TEST_TIMEOUT) before run-go-test-binary.sh ever sees the args,
// so that path can never exercise the omitted-timeout branch. This test
// calls the real script directly, bypassing test.sh entirely, to pin the one
// branch no other test reaches.
func TestRunGoTestBinaryDefaultsTimeoutWhenOmitted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run-go-test-binary.sh is a bash script; exercised by the Windows CI job itself, not this test")
	}

	repoRoot := sourceRepoRoot(t)
	root := t.TempDir()
	fakeBin := filepath.Join(root, "fake-go-test-binary")
	if err := os.WriteFile(fakeBin, []byte(fakeGoTestBinaryForTimeoutProbe), 0o755); err != nil {
		t.Fatalf("write fake go test binary: %v", err)
	}
	logPath := filepath.Join(root, "fake-go-test-binary.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("initialize fake go test binary log: %v", err)
	}

	bash, lookErr := exec.LookPath("bash")
	if lookErr != nil {
		t.Fatalf("bash is required to exercise run-go-test-binary.sh: %v", lookErr)
	}

	script := filepath.Join(repoRoot, "scripts", "ci", "run-go-test-binary.sh")
	// Deliberately no -timeout: this is the exact invocation shape pr.yml
	// uses (binary, PKGDIR, -run, -count=1, no -timeout).
	cmd := exec.Command(bash, script, fakeBin, repoRoot, "-run", "TestNothing", "-count=1")
	cmd.Env = append(os.Environ(), "FAKE_GO_TEST_BINARY_LOG="+logPath, "GITHUB_WORKSPACE="+repoRoot)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-go-test-binary.sh failed: %v\n%s", err, output)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake go test binary log: %v", err)
	}
	var args []string
	for _, line := range strings.Split(strings.TrimRight(string(logBytes), "\n"), "\n") {
		if arg, ok := strings.CutPrefix(line, "arg="); ok {
			args = append(args, arg)
		}
	}

	wantArgs := []string{"-test.run", "TestNothing", "-test.count", "1", "-test.timeout", "10m", "-test.paniconexit0"}
	if strings.Join(args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("run-go-test-binary.sh with no -timeout launched with args %v, want %v (default -test.timeout=10m plus unconditional -test.paniconexit0, matching native `go test`'s own injected flags)", args, wantArgs)
	}
}

// TestWindowsPrebuiltRequiredFlagMechanism pins the F4 review SF-5
// single-flag flip mechanism: WINDOWS_PREBUILT_REQUIRED (pr.yml's top-level
// env) is "false" by default, both Windows pairs are unconditionally in
// ci-gate's needs and env map (so the gate always evaluates both), and
// which pair is actually enforced is decided dynamically in the "Evaluate CI
// gate" step's run script rather than by two more names sitting in the
// static CI_GATE_REQUIRED list. Without this test, someone could add
// test-windows-liveness-prebuilt/worktree-remove-windows-prebuilt back into
// CI_GATE_REQUIRED directly (making both pairs required at once, defeating
// the flip) or add a new Windows job to ci-gate.needs without routing it
// through this mechanism (M3 from the F4 review).
func TestWindowsPrebuiltRequiredFlagMechanism(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")

	if got := pr.Env["WINDOWS_PREBUILT_REQUIRED"]; got != "false" {
		t.Errorf("pr.yml top-level env WINDOWS_PREBUILT_REQUIRED = %q, want \"false\" (the native pair is required by default)", got)
	}

	gate := pr.job(t, "ci-gate")
	for _, jobName := range []string{
		"test-windows-liveness", "worktree-remove-windows",
		"test-windows-liveness-prebuilt", "worktree-remove-windows-prebuilt",
	} {
		if !contains(gate.Needs, jobName) {
			t.Errorf("ci-gate.needs = %v, missing %q", gate.Needs, jobName)
		}
	}

	evaluate := gate.step(t, "Evaluate CI gate")
	wantEnv := map[string]string{
		"TEST_WINDOWS_LIVENESS":            "${{ needs.test-windows-liveness.result }}",
		"WORKTREE_REMOVE_WINDOWS":          "${{ needs.worktree-remove-windows.result }}",
		"TEST_WINDOWS_LIVENESS_PREBUILT":   "${{ needs.test-windows-liveness-prebuilt.result }}",
		"WORKTREE_REMOVE_WINDOWS_PREBUILT": "${{ needs.worktree-remove-windows-prebuilt.result }}",
	}
	for k, want := range wantEnv {
		if got := evaluate.Env[k]; got != want {
			t.Errorf("Evaluate CI gate step env %s = %q, want %q", k, got, want)
		}
	}

	// The static CI_GATE_REQUIRED declaration must not itself name either
	// Windows pair -- both are added dynamically below, exactly once, based
	// on the flag. A static entry here would make that pair unconditionally
	// required regardless of the flag.
	staticRequired := evaluate.Env["CI_GATE_REQUIRED"]
	for _, name := range []string{
		"TEST_WINDOWS_LIVENESS", "WORKTREE_REMOVE_WINDOWS",
		"TEST_WINDOWS_LIVENESS_PREBUILT", "WORKTREE_REMOVE_WINDOWS_PREBUILT",
	} {
		if regexp.MustCompile(`(^|\s)` + name + `(\s|$)`).MatchString(staticRequired) {
			t.Errorf("CI_GATE_REQUIRED statically lists %s; it must only be added dynamically by the WINDOWS_PREBUILT_REQUIRED conditional in the run script", name)
		}
	}

	run := evaluate.Run
	for _, want := range []string{
		`if [[ "$WINDOWS_PREBUILT_REQUIRED" == "true" ]]; then`,
		`CI_GATE_REQUIRED="$CI_GATE_REQUIRED TEST_WINDOWS_LIVENESS_PREBUILT WORKTREE_REMOVE_WINDOWS_PREBUILT"`,
		`skipped_ok="$skipped_ok TEST_WINDOWS_LIVENESS WORKTREE_REMOVE_WINDOWS"`,
		`CI_GATE_REQUIRED="$CI_GATE_REQUIRED TEST_WINDOWS_LIVENESS WORKTREE_REMOVE_WINDOWS"`,
		`skipped_ok="$skipped_ok TEST_WINDOWS_LIVENESS_PREBUILT WORKTREE_REMOVE_WINDOWS_PREBUILT"`,
		`export CI_GATE_REQUIRED`,
	} {
		if !strings.Contains(run, want) {
			t.Errorf("Evaluate CI gate run body missing %q", want)
		}
	}
}

// TestWindowsCrossCompileJobsNeverUseSecrets guards the fork-safety
// constraint this whole slice was built under: windows-test-binaries and its
// two "-prebuilt" consumers run on every PR, including from forks and
// Dependabot, and must never need a secret to do so (they only compile code
// already in the checkout and move an artifact between jobs). The same
// applies to main.yml's cache-seeding twin, which never runs on a fork PR at
// all but should still carry no incentive to grow one.
func TestWindowsCrossCompileJobsNeverUseSecrets(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	main := readCIWorkflow(t, "main.yml")

	type namedJob struct {
		workflow string
		name     string
		job      ciWorkflowJob
	}
	jobs := []namedJob{
		{"pr.yml", "windows-test-binaries", pr.job(t, "windows-test-binaries")},
		{"pr.yml", "test-windows-liveness-prebuilt", pr.job(t, "test-windows-liveness-prebuilt")},
		{"pr.yml", "worktree-remove-windows-prebuilt", pr.job(t, "worktree-remove-windows-prebuilt")},
		{"main.yml", "windows-test-binaries-cache", main.job(t, "windows-test-binaries-cache")},
	}
	for _, nj := range jobs {
		if nj.job.Secrets != nil {
			t.Errorf("%s job %s has non-nil secrets: %v", nj.workflow, nj.name, nj.job.Secrets)
		}
		for _, step := range nj.job.Steps {
			if strings.Contains(step.Run, "secrets.") {
				t.Errorf("%s job %s step %q run references secrets.*", nj.workflow, nj.name, step.Name)
			}
			for k, v := range step.Env {
				if strings.Contains(v, "secrets.") {
					t.Errorf("%s job %s step %q env %s references secrets.*", nj.workflow, nj.name, step.Name, k)
				}
			}
			for k, v := range step.With {
				if strings.Contains(v, "secrets.") {
					t.Errorf("%s job %s step %q with %s references secrets.*", nj.workflow, nj.name, step.Name, k)
				}
			}
		}
	}
}
