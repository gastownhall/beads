package scripts_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCIWorkflowArtifactOwnership(t *testing.T) {
	for _, workflowName := range []string{"pr.yml", "main.yml"} {
		t.Run(workflowName, func(t *testing.T) {
			workflow := readCIWorkflow(t, workflowName)

			for _, forbidden := range []string{
				"golangci-lint",
				"make ci-pr-policy",
				"make ci-pr-lint",
			} {
				for _, step := range workflow.job(t, "build-artifacts").Steps {
					if strings.Contains(step.Run, forbidden) {
						t.Errorf("build-artifacts runs %q in step %q", forbidden, step.Name)
					}
				}
			}

			assertJobRunsExactly(t, workflow.job(t, "pr-policy-wrapper"), "make ci-pr-policy")
			assertJobRunsExactly(t, workflow.job(t, "pr-lint-wrapper"), "make ci-pr-lint")

			// F5.1: the duplicate whole-tree/diff-scoped `Lint` job (and its
			// LINT gate id, pr.yml only) is folded into pr-lint-wrapper.
			if _, found := workflow.Jobs["lint"]; found {
				t.Errorf("%s has a %q job; its only-new-issues pass duplicates pr-lint-wrapper's native leg (H7) and was folded into it", workflowName, "lint")
			}

			// F5.2: pr-lint-wrapper is a 3-leg matrix keyed on matrix.target,
			// with BD_LINT_TARGETS telling scripts/pr-lint which legs to run.
			lintWrapper := workflow.job(t, "pr-lint-wrapper")
			if lintWrapper.Strategy.FailFast {
				t.Errorf("%s pr-lint-wrapper strategy.fail-fast = true, want false", workflowName)
			}
			if want := []string{"native", "windows", "darwin"}; !equalStrings(lintWrapper.Strategy.Matrix.Target, want) {
				t.Errorf("%s pr-lint-wrapper matrix.target = %v, want %v", workflowName, lintWrapper.Strategy.Matrix.Target, want)
			}
			if got := lintWrapper.step(t, "Run PR lint wrapper").Env["BD_LINT_TARGETS"]; got != "${{ matrix.target }}" {
				t.Errorf("%s pr-lint-wrapper BD_LINT_TARGETS = %q, want %q", workflowName, got, "${{ matrix.target }}")
			}
			// N-5: a dropped GOCACHE env here silently stops the lint cache
			// from doing anything (the step still passes; it just never
			// reuses or populates the restored/saved directory).
			if got, want := lintWrapper.step(t, "Run PR lint wrapper").Env["GOCACHE"], "${{ runner.temp }}/go-cache/lint-${{ matrix.target }}"; got != want {
				t.Errorf("%s pr-lint-wrapper GOCACHE = %q, want %q", workflowName, got, want)
			}
			if got := lintWrapper.step(t, "Install golangci-lint").Run; got != "./scripts/ci/install-golangci-lint.sh" {
				t.Errorf("%s pr-lint-wrapper installs golangci-lint with %q, want the pinned install script", workflowName, got)
			}
			// N-7.
			if lintWrapper.TimeoutMinutes != 20 {
				t.Errorf("%s pr-lint-wrapper timeout-minutes = %d, want 20 (matches scripts-go-checks)", workflowName, lintWrapper.TimeoutMinutes)
			}
		})
	}

	if got := readCIWorkflow(t, "pr.yml").job(t, "pr-lint-wrapper").step(t, "Run PR lint wrapper").Env["BD_LINT_NEW_FROM_MERGE_BASE"]; got != "origin/main" {
		t.Errorf("pr.yml pr-lint-wrapper BD_LINT_NEW_FROM_MERGE_BASE = %q, want origin/main (PR lane stays diff-scoped)", got)
	}
	if got := readCIWorkflow(t, "main.yml").job(t, "pr-lint-wrapper").step(t, "Run PR lint wrapper").Env["BD_LINT_NEW_FROM_MERGE_BASE"]; got != "" {
		t.Errorf("main.yml pr-lint-wrapper BD_LINT_NEW_FROM_MERGE_BASE = %q, want unset", got)
	}
	// N-5: --new-from-merge-base in pr.yml's pr-lint-wrapper needs real
	// history; a dropped fetch-depth: 0 would fail loudly (M3, a NIT-grade
	// mutation per the review), but pin it anyway since it is part of the
	// same wrapper contract as the GOCACHE pin above.
	if got := readCIWorkflow(t, "pr.yml").job(t, "pr-lint-wrapper").Steps[0].With["fetch-depth"]; got != "0" {
		t.Errorf("pr.yml pr-lint-wrapper checkout fetch-depth = %q, want %q", got, "0")
	}
	// go-vet-cache (N-7).
	if job := readCIWorkflow(t, "main.yml").job(t, "go-vet-cache"); job.TimeoutMinutes != 20 {
		t.Errorf("main.yml go-vet-cache timeout-minutes = %d, want 20 (matches scripts-go-checks)", job.TimeoutMinutes)
	}
}

func TestPRCIGateRequiresPolicyAndLintWrappers(t *testing.T) {
	gate := readCIWorkflow(t, "pr.yml").job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env

	for _, job := range []string{"pr-policy-wrapper", "pr-lint-wrapper"} {
		if !contains(gate.Needs, job) {
			t.Errorf("ci-gate needs %q: %v", job, gate.Needs)
		}
	}

	for key, want := range map[string]string{
		"PR_POLICY_WRAPPER": "${{ needs.pr-policy-wrapper.result }}",
		"PR_LINT_WRAPPER":   "${{ needs.pr-lint-wrapper.result }}",
	} {
		if got := gateEnv[key]; got != want {
			t.Errorf("ci-gate env %s = %q, want %q", key, got, want)
		}
	}

	for _, required := range []string{"PR_POLICY_WRAPPER", "PR_LINT_WRAPPER"} {
		if !strings.Contains(gateEnv["CI_GATE_REQUIRED"], required) {
			t.Errorf("ci-gate CI_GATE_REQUIRED does not include %q", required)
		}
	}

	// F5.1/F5.4: the deleted `lint` job's LINT id must not linger in the gate.
	if contains(gate.Needs, "lint") {
		t.Errorf("ci-gate needs %q, which no longer exists: %v", "lint", gate.Needs)
	}
	if contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "LINT") {
		t.Error("ci-gate CI_GATE_REQUIRED still includes LINT")
	}
	if _, found := gateEnv["LINT"]; found {
		t.Error("ci-gate env still sets LINT")
	}
}

// F5.2/F5.7: the pinned golangci-lint install script's version and sha256
// values must stay in sync with LINTING.md's documented version and with
// .githooks/pre-commit's `go run ...@<version>`, and the three workflows that
// use the script (pr.yml, main.yml, ci-measurements.yml) must all call it
// instead of `go install`.
func TestGolangciLintInstallScriptPinned(t *testing.T) {
	root := sourceRepoRoot(t)
	script := readPolicyFile(t, root, "scripts/ci/install-golangci-lint.sh")

	const version = "2.10.1"
	wantSHA256 := map[string]string{
		"amd64": "dfa775874cf0561b404a02a8f4481fc69b28091da95aa697259820d429b09c99",
		"arm64": "6652b42ae02915eb2f9cb2a2e0cac99514c8eded8388d88ae3e06e1a52c00de8",
	}

	if !strings.Contains(script, `readonly version="`+version+`"`) {
		t.Errorf("install-golangci-lint.sh does not pin version %q", version)
	}
	for arch, sha := range wantSHA256 {
		if !strings.Contains(script, "["+arch+"]=\""+sha+"\"") {
			t.Errorf("install-golangci-lint.sh does not pin %s sha256 %q", arch, sha)
		}
	}

	linting := readPolicyFile(t, root, "engdocs/LINTING.md")
	if !strings.Contains(linting, "v"+version) {
		t.Errorf("engdocs/LINTING.md does not mention v%s", version)
	}

	preCommit := readPolicyFile(t, root, ".githooks/pre-commit")
	if !strings.Contains(preCommit, "golangci-lint/v2/cmd/golangci-lint@v"+version) {
		t.Errorf(".githooks/pre-commit does not pin golangci-lint @v%s", version)
	}
	// The hook stays on `go run` deliberately (CI-only RUNNER_TEMP/GITHUB_PATH
	// requirement); it must not switch to the install script.
	if strings.Contains(preCommit, "install-golangci-lint.sh") {
		t.Error(".githooks/pre-commit must keep using `go run`, not the CI-only install script")
	}

	for _, workflow := range []string{"pr.yml", "main.yml"} {
		job := readCIWorkflow(t, workflow).job(t, "pr-lint-wrapper")
		if got := job.step(t, "Install golangci-lint").Run; got != "./scripts/ci/install-golangci-lint.sh" {
			t.Errorf("%s pr-lint-wrapper installs golangci-lint with %q, want the pinned install script", workflow, got)
		}
	}
	measurements := readPolicyFile(t, root, ".github/workflows/ci-measurements.yml")
	if strings.Contains(measurements, "go install github.com/golangci/golangci-lint") {
		t.Error("ci-measurements.yml still uses go install for golangci-lint; use scripts/ci/install-golangci-lint.sh")
	}
	if !strings.Contains(measurements, "install-golangci-lint.sh") {
		t.Error("ci-measurements.yml does not call scripts/ci/install-golangci-lint.sh")
	}

	// SF-2: install-golangci-lint.sh and the "pr-lint wrapper" measurement run
	// in the same shell block (not separate steps), so appending to
	// $GITHUB_PATH (which only takes effect in later *steps*) is not enough;
	// the script's install_dir must also be exported into this step's own
	// PATH, after the install call and before the wrapper that needs it.
	const (
		installMarker = "install-golangci-lint.sh"
		pathExport    = `export PATH="$RUNNER_TEMP/golangci-lint:$PATH"`
		wrapperMarker = `ci_time "pr-lint wrapper"`
	)
	installIdx := strings.Index(measurements, installMarker)
	pathIdx := strings.Index(measurements, pathExport)
	wrapperIdx := strings.Index(measurements, wrapperMarker)
	if pathIdx < 0 {
		t.Errorf("ci-measurements.yml does not export golangci-lint's install dir onto PATH in the same step (want %q)", pathExport)
	}
	if installIdx < 0 || wrapperIdx < 0 {
		t.Fatalf("ci-measurements.yml missing expected markers: install=%d wrapper=%d", installIdx, wrapperIdx)
	}
	if !(installIdx < pathIdx && pathIdx < wrapperIdx) {
		t.Errorf("ci-measurements.yml PATH export must sit between the golangci-lint install (%d) and the pr-lint wrapper call (%d); got PATH export at %d", installIdx, wrapperIdx, pathIdx)
	}
}

// TestInstallGolangciLintFailsClosedOnChecksumMismatch is the regression net
// for install-golangci-lint.sh's supply-chain gate (SF-3). Without it, nothing
// exercises the sha256 comparison's behavior -- TestGolangciLintInstallScriptPinned
// above only pins the hash strings as text, so e.g. replacing the comparison
// with an always-false condition passes every other test in this package. A
// fake curl hands the script a tarball whose content never matches either
// pinned hash; a fake tar makes extraction succeed anyway (so a bypassed check
// is exposed cleanly as "installed the wrong thing", not masked by tar
// choking on bad bytes), which is exactly the gap a real supply-chain attack
// (a compromised release asset, a MITM'd download) would exploit if the
// comparison were missing or broken.
func TestInstallGolangciLintFailsClosedOnChecksumMismatch(t *testing.T) {
	bash := requireHostTool(t, "bash")

	bin := t.TempDir()
	stateDir := t.TempDir()
	runnerTemp := t.TempDir()
	curlLog := filepath.Join(stateDir, "curl-calls")
	tarLog := filepath.Join(stateDir, "tar-calls")
	githubPath := filepath.Join(stateDir, "github-path")
	for _, path := range []string{curlLog, tarLog, githubPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Writes fixed, wrong-checksum bytes to curl's -o target. Any download
	// (first attempt or a retry) "succeeds" the same way, so the script
	// reaches the sha256 comparison deterministically.
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$GOLANGCI_INSTALL_CURL_LOG"
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out="$arg"
  fi
  prev="$arg"
done
if [ -n "$out" ]; then
  printf 'not the real golangci-lint release asset\n' >"$out"
fi
`)
	// Materializes a plausible-looking extracted binary unconditionally, the
	// way a real tarball's contents would after a successful `tar -xzf`. The
	// script's sha256 check -- not tar's own format validation -- must be
	// what stands between this fake binary and $RUNNER_TEMP/golangci-lint.
	writeExecutable(t, filepath.Join(bin, "tar"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$GOLANGCI_INSTALL_TAR_LOG"
outdir=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-C" ]; then
    outdir="$arg"
  fi
  prev="$arg"
done
mkdir -p "$outdir/golangci-lint-2.10.1-linux-amd64"
cat >"$outdir/golangci-lint-2.10.1-linux-amd64/golangci-lint" <<'SCRIPT'
#!/bin/sh
printf 'golangci-lint has version 2.10.1 built from unknown\n'
SCRIPT
chmod +x "$outdir/golangci-lint-2.10.1-linux-amd64/golangci-lint"
`)
	// Pins the platform so the asset name, arch lookup and extracted layout
	// are fixed regardless of the machine running the test.
	writeExecutable(t, filepath.Join(bin, "uname"), `#!/bin/sh
set -eu
case "${1:-}" in
  -m) printf 'x86_64\n' ;;
  *) printf 'Linux\n' ;;
esac
`)

	pathEnv := shellPathEnv()
	binPath := shellPathUnderEnv(t, bash, bin, pathEnv)
	statePath := shellPathUnderEnv(t, bash, stateDir, pathEnv)
	commandPath := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		commandPath = binPath + ":/usr/bin:/bin"
	}
	root := sourceRepoRoot(t)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"BEADS_TEST_COMMAND_PATH=" + commandPath,
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"RUNNER_TEMP=" + shellPathUnderEnv(t, bash, runnerTemp, pathEnv),
		"GITHUB_PATH=" + statePath + "/github-path",
		"GOLANGCI_INSTALL_CURL_LOG=" + statePath + "/curl-calls",
		"GOLANGCI_INSTALL_TAR_LOG=" + statePath + "/tar-calls",
	}
	for _, name := range []string{"curl", "tar", "uname"} {
		requireShellCommandPath(t, bash, root, env, name, binPath+"/"+name)
	}

	cmd := bashScriptCommand(bash, "scripts/ci/install-golangci-lint.sh")
	cmd.Dir = root
	cmd.Env = env
	output, runErr := cmd.CombinedOutput()

	if code := pullDoltExitCode(runErr); code == 0 {
		t.Fatalf("exit = 0, want non-zero; output:\n%s", output)
	}
	if !strings.Contains(string(output), "sha256 mismatch") {
		t.Errorf("output does not contain %q:\n%s", "sha256 mismatch", output)
	}
	if calls := readCallLines(t, tarLog); len(calls) != 0 {
		t.Errorf("tar was invoked %d time(s), want 0 (the sha256 check must reject the download before extraction): %q", len(calls), calls)
	}
	if calls := readCallLines(t, curlLog); len(calls) == 0 {
		t.Fatalf("curl was never invoked; test did not exercise the script's download path")
	}
	if entries, err := os.ReadDir(runnerTemp); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Errorf("RUNNER_TEMP = %v, want empty (nothing installed on a checksum mismatch)", entries)
	}
	if data, err := os.ReadFile(githubPath); err != nil {
		t.Fatal(err)
	} else if len(data) != 0 {
		t.Errorf("GITHUB_PATH = %q, want empty (nothing appended on a checksum mismatch)", data)
	}
}

// TestPRCIGateRequiresReleaseTargetCrossCompilation pins the cross-compilation
// check into the gate. Wiring a job into ci-gate takes three separate edits --
// needs:, the CI_GATE_REQUIRED token list, and the CHECK_* env mapping -- and
// the gate silently ignores a token that is missing any one of them. Every
// other load-bearing check in this file is pinned by name for that reason.
func TestPRCIGateRequiresReleaseTargetCrossCompilation(t *testing.T) {
	const (
		jobName = "check-release-target-cross-compilation"
		token   = "CHECK_RELEASE_TARGET_CROSS_COMPILATION"
	)

	gate := readCIWorkflow(t, "pr.yml").job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env

	if !contains(gate.Needs, jobName) {
		t.Errorf("ci-gate needs %q: %v", jobName, gate.Needs)
	}
	if got, want := gateEnv[token], "${{ needs."+jobName+".result }}"; got != want {
		t.Errorf("ci-gate env %s = %q, want %q", token, got, want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), token) {
		t.Errorf("ci-gate CI_GATE_REQUIRED does not include %q", token)
	}
}

// TestReleaseTargetCrossCompilationMatrixMatchesGoreleaser keeps
// scripts/ci/release-targets.txt (F7a: the single source of truth for the
// pr.yml cross-compilation job's two matrix legs, read by
// scripts/ci/check-release-cross-compile.sh) and the set of shipped release
// targets in lockstep. Without a guard a newly added release target -- the
// way freebsd/amd64 once was -- is silently uncovered while a green "release
// target cross-compilation" check still stands. That is worse than having no
// check at all, because the check's existence implies the coverage it has
// quietly lost.
func TestReleaseTargetCrossCompilationMatrixMatchesGoreleaser(t *testing.T) {
	const jobName = "check-release-target-cross-compilation"
	const manifestPath = "scripts/ci/release-targets.txt"

	// darwin/amd64 and darwin/arm64 are shipped release targets that are
	// deliberately absent from .goreleaser.yml's builds: release.yml's
	// goreleaser-macos job builds them natively at CGO_ENABLED=1 for embedded
	// Dolt support, as .goreleaser.yml's own comment records.
	want := map[string]string{
		"darwin/amd64": "release.yml goreleaser-macos",
		"darwin/arm64": "release.yml goreleaser-macos",
	}
	for _, build := range readGoreleaserBuilds(t) {
		for _, goos := range build.GOOS {
			for _, goarch := range build.GOARCH {
				want[goos+"/"+goarch] = ".goreleaser.yml " + build.ID
			}
		}
	}

	// The job itself must still read the manifest, so a future rewrite of the
	// job that stops threading matrix.group through to the script cannot pass
	// silently.
	job := readCIWorkflow(t, "pr.yml").job(t, jobName)
	groups := append([]string(nil), job.Strategy.Matrix.Group...)
	sort.Strings(groups)
	if !reflect.DeepEqual(groups, []string{"desktop", "unix"}) {
		t.Errorf("%s matrix groups = %v, want [desktop unix]", jobName, groups)
	}
	var ranScript bool
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "check-release-cross-compile.sh ${{ matrix.group }}") {
			ranScript = true
		}
	}
	if !ranScript {
		t.Errorf("%s does not run check-release-cross-compile.sh with matrix.group", jobName)
	}

	raw, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), manifestPath))
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	got := make(map[string]bool)
	seenGroups := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%s: malformed row %q, want 'GOOS GOARCH GROUP'", manifestPath, line)
		}
		goos, goarch, group := fields[0], fields[1], fields[2]
		if group != "unix" && group != "desktop" {
			t.Fatalf("%s: row %q has unknown group %q", manifestPath, line, group)
		}
		seenGroups[group] = true
		got[goos+"/"+goarch] = true
	}
	for _, g := range []string{"unix", "desktop"} {
		if !seenGroups[g] {
			t.Errorf("%s: no targets in group %q", manifestPath, g)
		}
	}

	for target, source := range want {
		if !got[target] {
			t.Errorf("release target %s (%s) is not covered by %s", target, source, manifestPath)
		}
	}
	for target := range got {
		if _, ok := want[target]; !ok {
			t.Errorf("%s lists %s, which is not a shipped release target", manifestPath, target)
		}
	}
}

type goreleaserBuild struct {
	ID     string   `yaml:"id"`
	GOOS   []string `yaml:"goos"`
	GOARCH []string `yaml:"goarch"`
	Env    []string `yaml:"env"`
}

func readGoreleaserBuilds(t *testing.T) []goreleaserBuild {
	t.Helper()

	path := filepath.Join(sourceRepoRoot(t), ".goreleaser.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var config struct {
		Builds []goreleaserBuild `yaml:"builds"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(config.Builds) == 0 {
		t.Fatalf("%s declares no builds", path)
	}
	return config.Builds
}

func TestPRCoreRequiresExcludeReadPermissionCoverage(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-core-wrapper")
	// Its only condition is D2 step 3's stand-down, where Bazel's PR-core
	// lane (required instead) runs the test with the same requirement.
	if job.RunsOn != "ubuntu-latest" || job.If != prLaneLegacyIf || job.ContinueOnError {
		t.Error("exclude permission coverage must remain in the required Linux PR Core job")
	}
	if !contains(bazelPRLaneRCLines["prcore"], "test:prcore --test_env=BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION=1") {
		t.Error("Bazel's PR-core lane must require actual exclude read-permission coverage where PR Core stands down")
	}
	step := job.Steps[job.stepIndex(t, "Run PR core wrapper")]
	if step.If != "" || (step.ContinueOnError != nil && step.ContinueOnError != false) || strings.TrimSpace(step.Run) != "make ci-pr-core" {
		t.Error("exclude permission coverage must run through the nonoptional PR Core wrapper")
	}
	if step.Env["BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION"] != "1" {
		t.Error("PR Core must require actual exclude read-permission coverage")
	}
	gate := workflow.job(t, "ci-gate")
	evaluate := gate.step(t, "Evaluate CI gate")
	if gate.If != "${{ always() }}" || gate.ContinueOnError || evaluate.If != "" || (evaluate.ContinueOnError != nil && evaluate.ContinueOnError != false) {
		t.Error("CI gate must propagate required PR Core failures")
	}
	if !contains(gate.Needs, "pr-core-wrapper") || evaluate.Env["PR_CORE_WRAPPER"] != "${{ needs.pr-core-wrapper.result }}" || !contains(strings.Fields(evaluate.Env["CI_GATE_REQUIRED"]), "PR_CORE_WRAPPER") {
		t.Error("CI gate must require the PR Core result")
	}
}

func TestPRComplexityReportIsAdvisoryAndBestEffort(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "advisory-reports")
	// F7a: advisory-reports (examples build + complexity) moved to a
	// same-repo Blacksmith runner and gained a generous job-level timeout
	// (TestSameRepoBlacksmithRunners requires one of every moved job) that is
	// a backstop against a stuck runner, not a realistic ceiling - every
	// step underneath still carries its own tight timeout/continue-on-error.
	if job.RunsOn != sameRepoBlacksmith2vcpu || job.TimeoutMinutes != 60 || job.ContinueOnError {
		t.Errorf("advisory-reports job must run on same-repo Blacksmith with a backstop timeout/no job continue-on-error: runs-on=%q timeout=%d continue=%v", job.RunsOn, job.TimeoutMinutes, job.ContinueOnError)
	}
	if contains(job.Needs, "ci-gate") {
		t.Errorf("complexity report unexpectedly depends on ci-gate: %v", job.Needs)
	}
	for _, name := range []string{"Set up Go", "Install gocyclo", "Generate complexity report", "Annotate unavailable complexity report", "Upload complexity report"} {
		step := job.step(t, name)
		if step.TimeoutMinutes <= 0 || step.ContinueOnError != true {
			t.Errorf("complexity step %q is not bounded/best-effort: timeout=%d continue=%v", name, step.TimeoutMinutes, step.ContinueOnError)
		}
	}
	checkout := job.Steps[0]
	if checkout.TimeoutMinutes <= 0 || checkout.ContinueOnError != true {
		t.Errorf("complexity checkout is not bounded/best-effort: timeout=%d continue=%v", checkout.TimeoutMinutes, checkout.ContinueOnError)
	}
	report := job.step(t, "Generate complexity report")
	if report.ID != "generate-complexity" || report.If != "always()" || !strings.Contains(report.Run, "complexity.sh diff") || !strings.Contains(report.Run, "COMPLEXITY_BASE_REF=origin/main") {
		t.Errorf("complexity report step missing diff/always contract: id=%q if=%q run=%q", report.ID, report.If, report.Run)
	}
	annotate := job.step(t, "Annotate unavailable complexity report")
	if annotate.If != "always()" || !strings.Contains(annotate.Run, "::warning") {
		t.Errorf("complexity annotation step missing always/warning contract: if=%q run=%q", annotate.If, annotate.Run)
	}
	gate := workflow.job(t, "ci-gate")
	if contains(gate.Needs, "advisory-reports") {
		t.Errorf("ci-gate must not require advisory complexity report: %v", gate.Needs)
	}
}

func TestPRWorkflowExercisesNativeUserConfigDiagnostics(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-preflight-platforms")
	// The benchmark-environment test owns the shared job and matrix shape.
	wantHosts := map[string]string{"ubuntu-latest": "linux", "macos-latest": "darwin", "windows-latest": "windows"}
	gotHosts := make(map[string][]string)
	for _, tuple := range job.Strategy.Matrix.Include {
		gotHosts[tuple.OS] = append(gotHosts[tuple.OS], tuple.ExpectedGOOS)
	}
	for host, want := range wantHosts {
		if got := gotHosts[host]; len(got) != 1 || got[0] != want {
			t.Fatalf("native host %s = %v, want exactly one %s", host, got, want)
		}
	}
	step := job.step(t, "Check native user config diagnostics")
	// F7b review fix (S1): the Windows shard split this step's `if:` used to
	// be gated on (matrix.shard == 1) was reverted (see pr-preflight-platforms'
	// own comment in pr.yml) because it measurably failed to help: shard 2
	// only ever held 15-25s of work while shard 1 stayed at 450-480s. The
	// step is unconditional again, same as every OS's leg.
	if step.If != "" || step.Shell != "bash" || step.Env["CGO_ENABLED"] != "0" ||
		(step.ContinueOnError != nil && step.ContinueOnError != false) {
		t.Fatalf("native diagnostic step is conditional, optional, or uses the wrong host: %+v", step)
	}
	assertStepRunsExactly(t, job, step.Name, "bash scripts/ci/test-user-config-diagnostic.sh '${{ matrix.expected_goos }}'")
	assertStepsBefore(t, job, []string{"Set up Go", "Restore Go module cache"}, []string{step.Name})
	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "pr-preflight-platforms") ||
		gateEnv["PR_PREFLIGHT_PLATFORMS"] != "${{ needs.pr-preflight-platforms.result }}" ||
		!contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "PR_PREFLIGHT_PLATFORMS") {
		t.Fatal("native diagnostic platform results must feed the required aggregate gate")
	}
}

func TestPRWorkflowRequiresNativeInitGatewayCredential(t *testing.T) {
	const (
		jobName     = "pr-preflight-platforms"
		stepCommand = `bash scripts/ci/test-init-gateway-credential.sh "$RUNNER_OS"`
		gateKey     = "PR_PREFLIGHT_PLATFORMS"
	)

	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, jobName)
	if job.RunsOn != sameRepoPlatformsMatrixMarkerRunsOn || !equalStrings(matrixIncludeHosts(job.Strategy.Matrix.Include), []string{"macos-latest", "ubuntu-latest", "windows-latest"}) {
		t.Errorf("credential shell boundary requires the native three-host matrix: runner=%q include=%v", job.RunsOn, job.Strategy.Matrix.Include)
	}
	if job.If != "" || job.ContinueOnError {
		t.Errorf("credential process job is bypassable: if=%q continue-on-error=%v", job.If, job.ContinueOnError)
	}
	matchingSteps := 0
	for _, step := range job.Steps {
		if strings.TrimSpace(step.Run) != stepCommand {
			continue
		}
		matchingSteps++
		if step.Shell != "bash" || step.Env["RUNNER_OS"] != "" {
			t.Errorf("credential driver needs Bash and the native runner OS: shell=%q env=%v", step.Shell, step.Env)
		}
		// F7b review fix (S1): common to all three hosts; the shard split this
		// step's `if:` used to be gated on (matrix.shard == 1) was reverted
		// (see pr-preflight-platforms' own comment in pr.yml), so the step
		// is unconditional again.
		if step.If != "" || (step.ContinueOnError != nil && step.ContinueOnError != false) {
			t.Errorf("gateway credential step is bypassable: if=%q continue-on-error=%v", step.If, step.ContinueOnError)
		}
	}
	if matchingSteps != 1 {
		t.Fatalf("native preflight job has %d gateway credential commands, want 1", matchingSteps)
	}

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, jobName) || gateEnv[gateKey] != "${{ needs.pr-preflight-platforms.result }}" ||
		!contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), gateKey) {
		t.Errorf("ci-gate does not require the native credential process lane: needs=%v %s=%q required=%q",
			gate.Needs, gateKey, gateEnv[gateKey], gateEnv["CI_GATE_REQUIRED"])
	}
}

func TestPRWorkflowExercisesWindowsBenchmarkEnvScrubbing(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-preflight-platforms")

	if job.RunsOn != sameRepoPlatformsMatrixMarkerRunsOn {
		t.Errorf("pr-preflight-platforms runs-on = %q, want the matrix-marker chained ternary", job.RunsOn)
	}
	if got := matrixIncludeHosts(job.Strategy.Matrix.Include); !equalStrings(got, []string{"macos-latest", "ubuntu-latest", "windows-latest"}) {
		t.Errorf("pr-preflight-platforms matrix os = %v, want required three-host matrix", got)
	}
	if job.If != "" {
		t.Errorf("pr-preflight-platforms job is conditional: %q", job.If)
	}
	if job.ContinueOnError {
		t.Error("pr-preflight-platforms job may not continue on error")
	}

	step := job.step(t, "Check benchmark environment scrubbing")
	// F7b: this step is Windows-only.
	if step.If != "matrix.os == 'windows-latest'" {
		t.Errorf("benchmark environment scrubbing selector = %q, want native Windows only", step.If)
	}
	if step.ContinueOnError != nil && step.ContinueOnError != false {
		t.Error("benchmark environment scrubbing step may not continue on error")
	}
	const command = "go test -tags gms_pure_go -count=1 -run '^(TestCleanEnvUsesHostKeySemantics|TestBenchmarkCommandBuildersStripDoltEnvOverrides)$' ./scripts/repro-dolt-prod-timeouts"
	if got := strings.TrimSpace(step.Run); got != command {
		t.Errorf("benchmark environment scrubbing command = %q, want %q", got, command)
	}

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if gate.If != "${{ always() }}" {
		t.Errorf("ci-gate condition = %q, want always() aggregation", gate.If)
	}
	if gate.ContinueOnError {
		t.Error("ci-gate may not continue on error")
	}
	if !contains(gate.Needs, "pr-preflight-platforms") {
		t.Errorf("ci-gate does not require pr-preflight-platforms: %v", gate.Needs)
	}
	if got := gateEnv["PR_PREFLIGHT_PLATFORMS"]; got != "${{ needs.pr-preflight-platforms.result }}" {
		t.Errorf("ci-gate pr-preflight-platforms result = %q", got)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "PR_PREFLIGHT_PLATFORMS") {
		t.Error("ci-gate required set omits pr-preflight-platforms")
	}
}

func TestPRWorkflowExercisesWindowsEnvironmentHelpers(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	// The benchmark test owns this shared job's matrix and CI Gate propagation.
	job := workflow.job(t, "pr-preflight-platforms")
	step := job.step(t, "Check shared environment key semantics")
	// F7b: Windows-only, alongside the other windows-only steps.
	if step.If != "matrix.os == 'windows-latest'" || step.Shell != "bash" {
		t.Errorf("environment helpers need native Windows Bash: if=%q shell=%q", step.If, step.Shell)
	}
	if step.ContinueOnError != nil && step.ContinueOnError != false {
		t.Error("environment helper step may not continue on error")
	}
	if got := strings.TrimSpace(step.Run); got != "bash scripts/ci/test-windows-env-helpers.sh" {
		t.Errorf("environment helper entrypoint = %q", got)
	}
}

func TestPRPreflightPlatformsExercisesCredentialCommandFixturesOnWindows(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-preflight-platforms")
	step := job.step(t, "Exercise credential command fixtures on Windows")

	// F7b: Windows-only, alongside the other windows-only steps.
	if step.If != "matrix.os == 'windows-latest'" {
		t.Errorf("credential command fixture selector = %q, want native Windows only", step.If)
	}
	if step.Shell != "pwsh" {
		t.Errorf("credential command fixture shell = %q, want PowerShell", step.Shell)
	}
	if step.ContinueOnError != nil && step.ContinueOnError != false {
		t.Error("credential command fixture step may not continue on error")
	}
	if got := step.Env["CGO_ENABLED"]; got != "0" {
		t.Errorf("credential command fixture CGO_ENABLED = %q, want 0", got)
	}
	// Pin the step's assertions, not just its wiring. Pinning only the packages
	// and one test name left a required gate whose sole surviving guarantee was
	// "go test exited 0" — which a fully skipped suite also satisfies. Dropping
	// six of the nine names from $expected, deleting the fail/skip throw, or
	// deleting the exactly-one-PASS loop all kept this test green. Every name
	// below is a test the Windows job must actually observe passing, so a
	// selector that narrows silently now fails here; widen the two together.
	for _, required := range []string{
		"internal/testutil/credentialcmd", "TestProtocol",
		"internal/creds",
		"TestCommandSourceRealShell", "TestCredentialCommandFixtureProtocol",
		"internal/storage/dolt",
		"TestApplyGatewayCredentialCommand", "TestApplyGatewayCredentialJSONEnvelope",
		"TestApplyGatewayCredentialFailsClosed", "TestApplyGatewayCredentialPresetWins",
		"TestApplyGatewayCredentialRejectsBadCharToken", "TestApplyResolvedConfigGatewayCredential",
		// The two load-bearing guards inside the step body.
		"'fail', 'skip'", "Expected one PASS",
	} {
		if !strings.Contains(step.Run, required) {
			t.Errorf("credential command fixture step omits %q", required)
		}
	}

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "pr-preflight-platforms") ||
		gateEnv["PR_PREFLIGHT_PLATFORMS"] != "${{ needs.pr-preflight-platforms.result }}" ||
		!contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "PR_PREFLIGHT_PLATFORMS") {
		t.Errorf("credential command fixture job is not required by ci-gate: needs=%v result=%q required=%q",
			gate.Needs, gateEnv["PR_PREFLIGHT_PLATFORMS"], gateEnv["CI_GATE_REQUIRED"])
	}
}

func TestPRCIGateRequiresWindowsGlobalPrimeOverride(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "test-windows-liveness")
	step := job.step(t, "Run native Windows global Prime override")
	if job.If != "" || job.ContinueOnError || step.If != "" ||
		(step.ContinueOnError != nil && step.ContinueOnError != false) {
		t.Fatal("native Windows global Prime override must be unconditional and required")
	}
	if step.Shell != "bash" || step.Env["CGO_ENABLED"] != "1" {
		t.Fatal("native Windows global Prime override requires Bash and CGO")
	}
	if !strings.Contains(step.Run, "./scripts/test.sh") ||
		!strings.Contains(step.Run, "^TestPrimeBinaryPortfolio$/^TestPrime_HookJSON_GlobalPrimeOverride$") {
		t.Fatal("native Windows gate must execute the global Prime override fixture")
	}
	gate := workflow.job(t, "ci-gate")
	evaluate := gate.step(t, "Evaluate CI gate")
	env := evaluate.Env
	// F4 side-by-side rollout (SF-5): TEST_WINDOWS_LIVENESS is no longer in
	// the static CI_GATE_REQUIRED list -- the WINDOWS_PREBUILT_REQUIRED flag
	// mechanism (TestWindowsPrebuiltRequiredFlagMechanism,
	// scripts/windows_test_binaries_manifest_test.go) adds it dynamically in
	// the run script. Since the rollout finished the flag is "true": the
	// prebuilt pair is required and the native pair stays wired (advisory)
	// for a one-line rollback. Check that wiring here instead of the old
	// static list membership.
	if !contains(gate.Needs, "test-windows-liveness") ||
		env["TEST_WINDOWS_LIVENESS"] != "${{ needs.test-windows-liveness.result }}" ||
		workflow.Env["WINDOWS_PREBUILT_REQUIRED"] != "true" ||
		!strings.Contains(evaluate.Run, `CI_GATE_REQUIRED="$CI_GATE_REQUIRED TEST_WINDOWS_LIVENESS WORKTREE_REMOVE_WINDOWS"`) ||
		!strings.Contains(evaluate.Run, `CI_GATE_REQUIRED="$CI_GATE_REQUIRED TEST_WINDOWS_LIVENESS_PREBUILT WORKTREE_REMOVE_WINDOWS_PREBUILT"`) {
		t.Fatal("CI gate must keep both Windows pairs wired and require the prebuilt pair (WINDOWS_PREBUILT_REQUIRED=\"true\"); the native pair stays wired for rollback")
	}
}

func TestPRCIGateRequiresJSWasmHookExecution(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "check-cmd-bd-puregeo-tests")
	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("js/wasm hook job runs-on = %q, want ubuntu-latest", job.RunsOn)
	}
	// Only D2 step 3's stand-down, where Bazel's pure-Go and js/wasm lane
	// (TestBazelPureJobMirrorsPureGoJob) is required instead
	// (TestPRLegacyLanesDeferToBazelLanes).
	if job.If != prLaneLegacyIf {
		t.Errorf("js/wasm hook job is conditional: %q", job.If)
	}

	setupGo := job.step(t, "Set up Go")
	if setupGo.Uses != setupGoActionFamily+"@"+setupGoSHA {
		t.Errorf("setup-go action = %q", setupGo.Uses)
	}
	if setupGo.With["go-version-file"] != "go.mod" || setupGo.With["cache"] != "false" {
		t.Errorf("setup-go inputs = %v", setupGo.With)
	}

	setupNode := job.step(t, "Set up Node.js")
	if setupNode.Uses != setupNodeActionFamily+"@"+setupNodeSHA {
		t.Errorf("setup-node action = %q", setupNode.Uses)
	}
	if setupNode.With["node-version"] != "24" {
		t.Errorf("setup-node version = %q, want 24", setupNode.With["node-version"])
	}

	execute := job.step(t, "Run js/wasm hook boundary")
	if execute.If != "" {
		t.Errorf("js/wasm hook step is conditional: %q", execute.If)
	}
	for key, want := range map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      "wasm",
		"GOOS":        "js",
	} {
		got, ok := execute.Env[key]
		if !ok || got != want {
			t.Errorf("js/wasm hook env %s = %q (present=%v), want %q", key, got, ok, want)
		}
	}
	for _, required := range []string{
		`go test -tags gms_pure_go -count=1 -timeout=2m`,
		`-exec="$(go env GOROOT)/lib/wasm/go_js_wasm_exec"`,
		`-run '^TestRunHookReportsUnsupportedExecution$'`,
		`-v ./internal/hooks`,
		`|| test_status=$?`,
		`=== RUN   TestRunHookReportsUnsupportedExecution`,
		`^--- PASS: TestRunHookReportsUnsupportedExecution`,
		`nonpass_pattern='^[[:space:]]*--- (FAIL|SKIP): '`,
		`[[ "$line" =~ $nonpass_pattern ]]`,
		`run_count != 1 || pass_count != 1 || nonpass_count != 0`,
	} {
		if !strings.Contains(execute.Run, required) {
			t.Errorf("js/wasm hook command does not contain %q", required)
		}
	}
	if regexp.MustCompile(`\bgo1\.[0-9]`).MatchString(execute.Run) {
		t.Errorf("js/wasm hook command duplicates the Go version owned by go.mod")
	}

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "check-cmd-bd-puregeo-tests") {
		t.Errorf("ci-gate does not require js/wasm hook job: %v", gate.Needs)
	}
	if got := gateEnv["CHECK_CMD_BD_PUREGEO_TESTS"]; got != "${{ needs.check-cmd-bd-puregeo-tests.result }}" {
		t.Errorf("ci-gate js/wasm hook result = %q", got)
	}
	if !strings.Contains(gateEnv["CI_GATE_REQUIRED"], "CHECK_CMD_BD_PUREGEO_TESTS") {
		t.Errorf("ci-gate required set omits js/wasm hook job")
	}
}

func TestPRCIGateRequiresGeneratedHookTimeoutProcessBoundary(t *testing.T) {
	const (
		jobName     = "pr-preflight-platforms"
		stepName    = "Exercise generated Git hook timeout process boundary"
		stepCommand = "go test '-tags=gms_pure_go' -count=1 -run '^TestGeneratedHookTimeoutProcessBoundary$' ./cmd/bd"
		gateKey     = "PR_PREFLIGHT_PLATFORMS"
	)

	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, jobName)
	if job.RunsOn != sameRepoPlatformsMatrixMarkerRunsOn || !equalStrings(matrixIncludeHosts(job.Strategy.Matrix.Include), []string{"macos-latest", "ubuntu-latest", "windows-latest"}) {
		t.Errorf("generated-hook process job is not the required three-host matrix: runs-on=%q include=%v", job.RunsOn, job.Strategy.Matrix.Include)
	}
	if job.TimeoutMinutes != 20 {
		t.Errorf("generated-hook process job timeout = %d minutes, want 20", job.TimeoutMinutes)
	}
	step := job.step(t, stepName)
	// F7b review fix (S1): common to all three hosts; unconditional again
	// now that the shard split is reverted (see pr-preflight-platforms' own
	// comment in pr.yml).
	if step.If != "" || (step.ContinueOnError != nil && step.ContinueOnError != false) || step.Shell != "bash" || step.Run != stepCommand {
		t.Errorf("generated-hook process step is not required exact Bash execution: if=%q continue-on-error=%v shell=%q run=%q",
			step.If, step.ContinueOnError, step.Shell, step.Run)
	}
	assertStepsBefore(t, job, []string{"Restore Go module cache"}, []string{stepName})

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, jobName) || gateEnv[gateKey] != "${{ needs.pr-preflight-platforms.result }}" ||
		!contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), gateKey) {
		t.Errorf("ci-gate does not require the three-host generated-hook lane: needs=%v %s=%q required=%q",
			gate.Needs, gateKey, gateEnv[gateKey], gateEnv["CI_GATE_REQUIRED"])
	}
}

func TestStorageDomainUOWJobsUseNestedTimeoutBudgets(t *testing.T) {
	const (
		storageTimeoutMinutes     = 15
		doctorTimeoutMinutes      = 10
		setupTeardownSlackMinutes = 5
		jobTimeoutMinutes         = storageTimeoutMinutes + doctorTimeoutMinutes + setupTeardownSlackMinutes
	)
	storageCommand := fmt.Sprintf(
		"go test -tags gms_pure_go -race -count=1 -timeout %dm -v ./internal/storage/domain/... ./internal/storage/uow/... ./internal/tracker/...",
		storageTimeoutMinutes)
	doctorCommand := fmt.Sprintf(
		"go test -tags gms_pure_go -race -count=1 -timeout %dm -v ./cmd/bd/doctor/fix/",
		doctorTimeoutMinutes)

	for _, workflowName := range []string{"pr.yml", "main.yml"} {
		t.Run(workflowName, func(t *testing.T) {
			job := readCIWorkflow(t, workflowName).job(t, "test-domain-uow")
			if job.TimeoutMinutes != jobTimeoutMinutes {
				t.Errorf("test-domain-uow timeout = %d minutes, want %d", job.TimeoutMinutes, jobTimeoutMinutes)
			}
			// Go's timeout applies per package test binary, so this is a
			// maintenance tripwire for the declared sequential tier budgets,
			// not a mathematical upper bound for the multi-package first step.
			if job.TimeoutMinutes <= storageTimeoutMinutes+doctorTimeoutMinutes {
				t.Errorf(
					"test-domain-uow timeout = %d minutes, want more than %d minutes of declared tier budgets",
					job.TimeoutMinutes,
					storageTimeoutMinutes+doctorTimeoutMinutes)
			}
			assertStepRunsExactly(t, job, "Test domain + uow + tracker", storageCommand)
			assertStepRunsExactly(t, job, "Test doctor/fix (Dolt-backed, hard-require container)", doctorCommand)
		})
	}

	// The container and local test servers are compared in pr.yml's own
	// every-PR job (it was test-domain-uow's first step until D2 step 3;
	// TestPRDoltServerFingerprintRunsOnEveryPR pins it).
	job := readCIWorkflow(t, "pr.yml").job(t, prFingerprintJob)
	const fingerprintStep = "Test Dolt server fingerprint (container + local)"
	assertStepRunsExactly(t, job, fingerprintStep,
		"go test -tags gms_pure_go -count=1 -timeout 5m -v -run '^TestDoltServerFingerprint$' ./internal/testutil/")
	assertStepEnvValue(t, job, fingerprintStep, "BEADS_TEST_REQUIRE_DOLT_CONTAINER", "1")

	gate := readCIWorkflow(t, "pr.yml").job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "test-domain-uow") {
		t.Errorf("ci-gate needs test-domain-uow: %v", gate.Needs)
	}
	if got, want := gateEnv["TEST_DOMAIN_UOW"], "${{ needs.test-domain-uow.result }}"; got != want {
		t.Errorf("ci-gate TEST_DOMAIN_UOW = %q, want %q", got, want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "TEST_DOMAIN_UOW") {
		t.Errorf("ci-gate CI_GATE_REQUIRED does not include TEST_DOMAIN_UOW: %q", gateEnv["CI_GATE_REQUIRED"])
	}
}

func TestMacOSTestJobsReuseWorkspaceBDBinary(t *testing.T) {
	const (
		workspaceBDBinary = "${{ github.workspace }}/bd"
		buildCommand      = "go build -v -tags gms_pure_go ./cmd/bd"
		// -timeout=30m is pinned here because ./cmd/bd has outgrown `go test`'s
		// 10m per-package default (#6091, and wy-5b5fbl before it — that
		// default is what made this leg flaky). It sits on the invocation
		// rather than in matrix.test-flags, so editing the matrix cannot
		// silently drop it.
		mainTestCommand = "go test -tags gms_pure_go ${{ matrix.test-flags }} -timeout=30m -skip '^TestEmbedded' ./..."
		// The macOS leg is the only consumer of main.yml's matrix test-flags
		// (the ubuntu leg's coverage step hardcodes its own). The deadline is
		// deliberately NOT here — see mainTestCommand.
		mainMacOSTestFlags = "-v -race -short"
	)

	workflows := map[string]ciWorkflow{
		"main.yml": readCIWorkflow(t, "main.yml"),
		"pr.yml":   readCIWorkflow(t, "pr.yml"),
	}

	// pr.yml's own "test-macos" job (a full-suite pre-merge race against
	// main.yml's macOS leg, run on every PR to catch macOS-only regressions
	// before merge) was removed under the F4 rollout: main.yml's
	// push-triggered macOS leg below is now the only lane that ever runs
	// this suite on macOS, so no PR run exercises it pre-merge any more.
	// Nothing in pr.yml should carry the workspace bd binary override
	// checked in the loop below.
	mainTest := workflows["main.yml"].job(t, "test")
	assertStepRunsExactly(t, mainTest, "Build", buildCommand)
	assertStepRunsExactly(t, mainTest, "Test", mainTestCommand)
	assertStepsBefore(t, mainTest, []string{"Build"}, []string{"Test"})
	if got := mainTest.step(t, "Build").If; got != "matrix.os != 'ubuntu-latest'" {
		t.Errorf("main build condition = %q, want macOS-only condition", got)
	}
	if got := mainTest.step(t, "Test").If; got != "${{ !matrix.coverage }}" {
		t.Errorf("main test condition = %q, want non-coverage condition", got)
	}
	if got := mainTest.Strategy.Matrix.OS; !equalStrings(got, []string{"ubuntu-latest", macOSRunner}) {
		t.Errorf("main test matrix os = %v, want [ubuntu-latest %s]", got, macOSRunner)
	}
	if got := mainTest.Strategy.Matrix.Include; len(got) != 2 ||
		got[0].OS != "ubuntu-latest" || !got[0].Coverage ||
		got[1].OS != macOSRunner || got[1].Coverage || got[1].TestFlags != mainMacOSTestFlags {
		t.Errorf("main test matrix include = %+v, want macOS non-coverage entry with %s", got, mainMacOSTestFlags)
	}
	assertStepEnvValue(t, mainTest, "Test", "BEADS_TEST_BD_BINARY", workspaceBDBinary)

	for workflowName, workflow := range workflows {
		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if step.Env["BEADS_TEST_BD_BINARY"] == workspaceBDBinary &&
					!(workflowName == "main.yml" && jobName == "test" && step.Name == "Test") {
					t.Errorf("%s job %q step %q has unexpected workspace bd binary override", workflowName, jobName, step.Name)
				}
			}
		}
	}
}

// TestDoltTestcontainerStepsDisableRyuk pins TESTCONTAINERS_RYUK_DISABLED on
// the four steps enumerated in the table below — the two Dolt-backed steps of
// the test-domain-uow job, in pr.yml and main.yml. It is an allow-list of
// literal (workflow, job, step) triples, so it catches an un-pinning
// regression on those four steps only; it does not detect the class, and a
// newly added container-starting step passes it unpinned.
//
// Why the pin: testcontainers-go shares one Ryuk reaper per host; when a step
// runs several container-starting packages as concurrent test binaries (plain
// "go test" parallelizes across packages), they race to attach to that shared
// reaper, and a failed handshake from one process reaps a sibling's live
// container mid-suite (be-2on). A GitHub Actions runner is destroyed after the
// job, so the reaper buys nothing there and only costs this race.
//
// The scope is the job, not a per-step predicate: "Test domain + uow +
// tracker" runs three container-starting package trees in one invocation and
// is the step the race is concrete on, while "Test doctor/fix" runs the single
// ./cmd/bd/doctor/fix/ package and is pinned for consistency inside the same
// job rather than because it has an in-step sibling.
//
// Left unpinned, deliberately. A step can only start a Dolt container if its
// job pre-caches the image via scripts/ci/pull-dolt-image.sh: checkDolt in
// internal/testutil gates on `docker image inspect` and never auto-pulls. The
// other jobs that do pull it are pr.yml/contract-corpus,
// main.yml/test-proxied-cmd, pr-risk.yml/test-proxied-cmd,
// pr-risk.yml/test-server-storage and -full, regression.yml/regression, and
// bazel.yml's --config=docker lane. They are out of scope for this change, not
// immune: no reap has been attributed to them, and the docker lane would
// additionally need --test_env=TESTCONTAINERS_RYUK_DISABLED=true because bazel
// does not forward ambient environment into tests. Extending the pin — and
// teaching this guard to scan for the class, the way assertGoCacheWriter below
// walks every job and step — is follow-up work on be-2on.
//
// Steps that run under BEADS_TEST_SKIP=dolt (pr-core.sh's hermetic wrapper,
// the sharded main-linux-integration-* jobs) never start a container at all
// and are correctly excluded: internal/testutil's readiness check treats
// BEADS_TEST_SKIP=dolt as an explicit opt-out before it ever reaches Docker.
func TestDoltTestcontainerStepsDisableRyuk(t *testing.T) {
	type doltContainerStep struct {
		workflow string
		job      string
		step     string
	}

	steps := []doltContainerStep{
		{"pr.yml", "test-domain-uow", "Test domain + uow + tracker"},
		{"pr.yml", "test-domain-uow", "Test doctor/fix (Dolt-backed, hard-require container)"},
		{"main.yml", "test-domain-uow", "Test domain + uow + tracker"},
		{"main.yml", "test-domain-uow", "Test doctor/fix (Dolt-backed, hard-require container)"},
	}

	for _, tc := range steps {
		t.Run(tc.workflow+"/"+tc.job+"/"+tc.step, func(t *testing.T) {
			job := readCIWorkflow(t, tc.workflow).job(t, tc.job)
			assertStepEnvValue(t, job, tc.step, "TESTCONTAINERS_RYUK_DISABLED", "true")
		})
	}
}

// TestNightlyFullTestRunsEmbeddedDoltBatchApplySuite pins the nightly
// full-test job's embedded-Dolt step: it must set BEADS_TEST_EMBEDDED_DOLT=1
// (the gate skipUnlessEmbeddedDolt checks) and run exactly
// TestBatchApplyContract, TestLargeBatchApplyWallClock_Embedded and (F1)
// TestLargeBatchApplyStatementCounts712_Embedded, non-race, after the main
// "Full Test Suite" step. That main step never sets BEADS_TEST_EMBEDDED_DOLT,
// so without this step the nightly job would never exercise a real
// 1000-item apply through the embedded backend, nor the 712-item shape's
// pinned statement-count baseline, at all — see the step's own comment in
// nightly.yml for why non-race and why these tests specifically.
func TestNightlyFullTestRunsEmbeddedDoltBatchApplySuite(t *testing.T) {
	job := readCIWorkflow(t, "nightly.yml").job(t, "full-test")
	const stepName = "Embedded Dolt batch-apply suite (non-race)"
	const wantRun = "go test -tags gms_pure_go -timeout 20m -run '^(TestBatchApplyContract|TestLargeBatchApplyWallClock_Embedded|TestLargeBatchApplyStatementCounts712_Embedded)$' ./internal/storage/embeddeddolt"
	assertStepRunsExactly(t, job, stepName, wantRun)
	assertStepEnvValue(t, job, stepName, "BEADS_TEST_EMBEDDED_DOLT", "1")
	assertStepsBefore(t, job, []string{"Full Test Suite (including integration tests)"}, []string{stepName})
	if strings.Contains(wantRun, "-race") {
		t.Errorf("embedded-dolt nightly step run = %q, must stay non-race (race dramatically inflates this backend's own wall-clock)", wantRun)
	}
	// N4 (F1 review): this step must still run (and report) even if an
	// earlier nightly step failed, and must carry its own explicit Go
	// timeout independent of the job-level timeout-minutes.
	if got := job.step(t, stepName).If; got != "${{ !cancelled() }}" {
		t.Errorf("embedded-dolt nightly step if = %q, want ${{ !cancelled() }}", got)
	}
}

func TestPRPreflightPlatformsRunsTestScriptPrebuiltBinaryContract(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-preflight-platforms")
	if job.RunsOn != sameRepoPlatformsMatrixMarkerRunsOn {
		t.Errorf("pr-preflight-platforms runs-on = %q, want the matrix-marker chained ternary", job.RunsOn)
	}
	if job.If != "" || job.TimeoutMinutes != 20 {
		t.Errorf("pr-preflight-platforms condition/timeout = %q/%d, want unconditional/20",
			job.If, job.TimeoutMinutes)
	}
	if got := matrixIncludeHosts(job.Strategy.Matrix.Include); !equalStrings(got, []string{"macos-latest", "ubuntu-latest", "windows-latest"}) {
		t.Errorf("pr-preflight-platforms matrix os = %v, want all three hosted platforms", got)
	}

	const stepName = "Exercise test.sh prebuilt binary path"
	assertStepRunsExactly(t, job, stepName,
		"go test '-tags=gms_pure_go' -count=1 -run '^TestTestScriptPrebuiltBinaryContract$' ./scripts")
	step := job.step(t, stepName)
	// F7b review fix (S1): common to all three hosts; unconditional again
	// now that the shard split is reverted (see pr-preflight-platforms' own
	// comment in pr.yml).
	if step.Shell != "bash" || step.If != "" || (step.ContinueOnError != nil && step.ContinueOnError != false) {
		t.Errorf("%s shell/condition/continue-on-error = %q/%q/%v, want unconditional required bash",
			stepName, step.Shell, step.If, step.ContinueOnError)
	}

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "pr-preflight-platforms") {
		t.Errorf("ci-gate does not need pr-preflight-platforms: %v", gate.Needs)
	}
	if got, want := gateEnv["PR_PREFLIGHT_PLATFORMS"], "${{ needs.pr-preflight-platforms.result }}"; got != want {
		t.Errorf("ci-gate PR_PREFLIGHT_PLATFORMS = %q, want %q", got, want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "PR_PREFLIGHT_PLATFORMS") {
		t.Errorf("ci-gate required set omits PR_PREFLIGHT_PLATFORMS")
	}
}

func TestRepositoryTextEOLPolicyWorkflow(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "check-doc-freshness-platforms")

	if want := sameRepoPlatformsMatrixMarkerRunsOn; job.RunsOn != want {
		t.Errorf("check-doc-freshness-platforms runs-on = %q, want %q", job.RunsOn, want)
	}
	wantMatrix := map[string]string{
		"ubuntu-latest":  "linux",
		"macos-latest":   "darwin",
		"windows-latest": "windows",
	}
	// F7b: each host's include tuple also carries a "runner" marker
	// (same-repo-linux/-macos/-windows) that sameRepoPlatformsMatrixMarkerRunsOn
	// resolves against; check it matches the host instead of treating it as an
	// unexpected field.
	wantRunnerMarker := map[string]string{
		"ubuntu-latest":  "same-repo-linux",
		"macos-latest":   "same-repo-macos",
		"windows-latest": "same-repo-windows",
	}
	if len(job.Strategy.Matrix.OS) != 0 {
		t.Errorf("check-doc-freshness-platforms retains an unbound os-list matrix: %v", job.Strategy.Matrix.OS)
	}
	if got, want := len(job.Strategy.Matrix.Include), len(wantMatrix); got != want {
		t.Fatalf("check-doc-freshness-platforms include tuple count = %d, want %d", got, want)
	}
	seen := make(map[string]bool, len(wantMatrix))
	for _, tuple := range job.Strategy.Matrix.Include {
		wantGOOS, ok := wantMatrix[tuple.OS]
		if !ok {
			t.Errorf("unexpected check-doc-freshness-platforms runner tuple: %+v", tuple)
			continue
		}
		if seen[tuple.OS] {
			t.Errorf("duplicate check-doc-freshness-platforms runner tuple for %q", tuple.OS)
		}
		seen[tuple.OS] = true
		if tuple.ExpectedGOOS != wantGOOS {
			t.Errorf("runner %q expected_goos = %q, want %q", tuple.OS, tuple.ExpectedGOOS, wantGOOS)
		}
		if tuple.Runner != wantRunnerMarker[tuple.OS] {
			t.Errorf("runner %q matrix runner marker = %q, want %q", tuple.OS, tuple.Runner, wantRunnerMarker[tuple.OS])
		}
		if tuple.Coverage || tuple.TestFlags != "" {
			t.Errorf(
				"runner %q has unexpected shared matrix fields: coverage=%t test-flags=%q",
				tuple.OS,
				tuple.Coverage,
				tuple.TestFlags,
			)
		}
		if len(tuple.Extra) != 0 {
			t.Errorf("runner %q has unexpected matrix fields: %v", tuple.OS, tuple.Extra)
		}
	}

	docStep := job.step(t, "Exercise native date and Bash process boundary")
	const (
		requiredSuiteSelector = "-required-suite=doc-freshness"
		wantDocCommand        = "go test '-tags=integration,gms_pure_go' -count=1 -run '^(TestDocFreshness.*|TestRequiredSuiteContract)$' ./scripts -args " + requiredSuiteSelector
	)
	if docStep.Run != wantDocCommand {
		t.Errorf("doc-freshness command = %q, want required-suite execution %q", docStep.Run, wantDocCommand)
	}

	eolStep := job.step(t, "Exercise repository text EOL policy boundary")
	const wantEOLCommand = "go test '-tags=integration,gms_pure_go' -count=1 ./scripts/gitattributespolicy -args -required-host -expected-goos '${{ matrix.expected_goos }}'"
	if eolStep.Run != wantEOLCommand {
		t.Errorf("repository EOL command = %q, want %q", eolStep.Run, wantEOLCommand)
	}
	if eolStep.If != "" {
		t.Errorf("repository EOL step has conditional if = %q", eolStep.If)
	}
	if strings.Contains(eolStep.Run, "-run") {
		t.Errorf("repository EOL step may not filter the narrow package: %q", eolStep.Run)
	}
	if job.stepIndex(t, "Exercise native date and Bash process boundary") >=
		job.stepIndex(t, "Exercise repository text EOL policy boundary") {
		t.Error("repository EOL step must remain separate and follow doc freshness")
	}

	gate := workflow.job(t, "ci-gate")
	if !contains(gate.Needs, "check-doc-freshness-platforms") {
		t.Errorf("ci-gate does not need check-doc-freshness-platforms: %v", gate.Needs)
	}
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	const gateKey = "CHECK_DOC_FRESHNESS_PLATFORMS"
	if want := "${{ needs.check-doc-freshness-platforms.result }}"; gateEnv[gateKey] != want {
		t.Errorf("ci-gate env %s = %q, want %q", gateKey, gateEnv[gateKey], want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), gateKey) {
		t.Errorf("ci-gate CI_GATE_REQUIRED does not include %q", gateKey)
	}
}

func TestGoCacheOwnershipTopology(t *testing.T) {
	workflows := map[string]ciWorkflow{
		"main.yml":    readCIWorkflow(t, "main.yml"),
		"pr.yml":      readCIWorkflow(t, "pr.yml"),
		"pr-risk.yml": readCIWorkflow(t, "pr-risk.yml"),
	}

	for workflowName, workflow := range workflows {
		assertPinnedGoCacheActions(t, workflowName, workflow)
	}
	t.Run("monolithic cache action is forbidden", func(t *testing.T) {
		if !isGoCacheActionFamily(cacheMonolithicActionFamily) || !isForbiddenGoCacheActionFamily(cacheMonolithicActionFamily) {
			t.Fatal("actions/cache must be recognized as a forbidden cache action family")
		}
	})

	assertGoCacheInventory(t, workflows["main.yml"].job(t, "build-artifacts"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCache("non-race"), saveModuleCache(), saveBuildCache("non-race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "build-embedded"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCache("race"), saveBuildCache("race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "pr-core-wrapper"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCache("race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "test"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCacheIf("non-race", macOSMatrixCondition), mainRestoreBuildCache("race"),
		saveModuleCacheAfterFailureOnMacOS(), saveBuildCacheAfterFailureOnMacOS("non-race"), saveBuildCacheAfterFailureOnMacOS("race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "test-windows"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCacheDayBucketedIf("non-race", ""), saveModuleCache(), saveBuildCacheDayBucketed("non-race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "pr-lint-wrapper"), []goCacheStep{
		restoreModuleCache(), mainRestoreGolangciCache(), saveGolangciCache(),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "go-vet-cache"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreVetCache(), saveVetCache(),
	})
	assertConditionalCacheWritersHaveMatrixMember(t, workflows["main.yml"].job(t, "test"), macOSRunner)
	// F4.7: seeds the module and xcompile caches pr.yml's windows-test-binaries
	// job only restores (PR workflows never save).
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "windows-test-binaries-cache"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreXCompileCache(), saveModuleCacheAfterFailure(), saveXCompileCache(),
	})
	// F7b: Blacksmith-only analogue of build-artifacts'/build-embedded's
	// race/non-race GOCACHE saves, for same-repo PR consumers
	// (scripts-go-checks, pr-preflight-platforms, check-doc-freshness-platforms)
	// that now run on Blacksmith, whose cache namespace is invisible to
	// GitHub-hosted build-artifacts/build-embedded.
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "blacksmith-go-build-cache"), []goCacheStep{
		mainRestoreModuleCache(),
		mainRestoreBuildCacheDayBucketedIf("race", "matrix.flavor == 'race'"),
		mainRestoreBuildCacheDayBucketedIf("non-race", "matrix.flavor == 'non-race'"),
		saveBuildCacheDayBucketedAfterFailureIf("race", "matrix.flavor == 'race'"),
		saveBuildCacheDayBucketedAfterFailureIf("non-race", "matrix.flavor == 'non-race'"),
	})

	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "build-artifacts"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("non-race"),
	})
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "pr-core-wrapper"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("race"),
	})
	// D2 step 3: PR Core's environment for ./scripts/..., go vet and the
	// Bazel-skipped tests; read-only like PR Core.
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "scripts-go-checks"), []goCacheStep{
		restoreModuleCache(), restoreBuildCacheIf("race", "matrix.check == 'scripts-test'"), restoreVetCache(), restoreBuildCacheIf("non-race", "matrix.check == 'allowlisted'"),
	})
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "pr-lint-wrapper"), []goCacheStep{restoreModuleCache(), restoreGolangciCache()})
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "worktree-remove-windows"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("non-race"),
	})
	// F4.4 advisory phase: windows-test-binaries cross-compiles on Linux and
	// only restores (never saves; main.yml's windows-test-binaries-cache job
	// is the saver). Its "-prebuilt" twins (test-windows-liveness-prebuilt,
	// worktree-remove-windows-prebuilt) download the resulting artifact and
	// run the binaries directly, so they use no Go cache action at all and
	// need no entry here or in the managed map below.
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "windows-test-binaries"), []goCacheStep{
		restoreModuleCache(), restoreXCompileCache(),
	})
	// F7b review (S2): restore-only non-race Go build cache seeded by
	// main.yml's test-windows Blacksmith leg (warm-non-race-cache.sh) on the
	// exact same runner selection, targeting this job's own cold cmd/bd test
	// compile (the reviewer's measured long pole here).
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "test-windows-liveness"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("non-race"),
	})
	for _, jobName := range []string{
		"advisory-reports", "check-release-target-cross-compilation",
	} {
		assertGoCacheInventory(t, workflows["pr.yml"].job(t, jobName), []goCacheStep{restoreModuleCache()})
	}
	// F7b review (S2): check-doc-freshness-platforms additionally restores a
	// restore-only non-race Go build cache, seeded by the same push-to-main
	// savers pr-preflight-platforms' leg restores below (see that job's own
	// comment in pr.yml for the per-OS seeder mapping).
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "check-doc-freshness-platforms"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("non-race"),
	})
	// F7b review (S1): the Windows shard split this job's matrix once had
	// (an extra `shard: 2` leg) was reverted: it measurably failed to help
	// (shard 2 only ever held 15-25s of work while shard 1 stayed at
	// 450-480s). The job is a single leg per OS again, so its module-cache
	// restore is unconditional, same as every other three-host job; see S2
	// above for the added non-race build-cache restore.
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "pr-preflight-platforms"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("non-race"),
	})
	assertGoCacheInventory(t, workflows["pr-risk.yml"].job(t, "build-embedded"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("race"), restoreBuildCache("non-race"),
	})
	assertNoUnmanagedGoCacheSteps(t, workflows, map[string]map[string]bool{
		"main.yml": {
			"build-artifacts": true, "build-embedded": true, "pr-core-wrapper": true, "test": true, "test-windows": true,
			"pr-lint-wrapper": true, "go-vet-cache": true, "windows-test-binaries-cache": true, "blacksmith-go-build-cache": true,
			// F7c: blacksmith-setup-go-cache's own restore/save pair seeds the
			// self-defined blacksmith-sg-v1- cache namespace every advisory
			// Blacksmith consumer restores from (B2, F7c implementation
			// report); it is covered by its own
			// TestBlacksmithSetupGoSeedExistsForAdvisoryConsumers and
			// TestBlacksmithSetupGoCacheKeysMatchAcrossSeederAndConsumers in
			// ci_f7c_advisory_test.go, not by assertGoCacheInventory above.
			"blacksmith-setup-go-cache": true,
		},
		"pr.yml": {
			"build-artifacts": true, "pr-core-wrapper": true, "scripts-go-checks": true, "worktree-remove-windows": true,
			"check-doc-freshness-platforms": true, "pr-preflight-platforms": true, "advisory-reports": true,
			"check-release-target-cross-compilation": true, "pr-lint-wrapper": true, "windows-test-binaries": true,
			"test-windows-liveness": true,
		},
		"pr-risk.yml": {"build-embedded": true},
	})

	mainArtifacts := workflows["main.yml"].job(t, "build-artifacts")
	assertStepsBefore(t, mainArtifacts, []string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Build reusable Linux artifacts"})
	assertStepsBefore(t, mainArtifacts, []string{"Build reusable Linux artifacts", "Upload build artifacts"}, []string{"Save Go module cache", "Save non-race Go build cache"})

	mainEmbedded := workflows["main.yml"].job(t, "build-embedded")
	embeddedRaceBuilds := []string{"Build embedded bd binary", "Build embedded storage test binary", "Build embedded cmd test binary"}
	assertStepsBefore(t, mainEmbedded, []string{"Restore Go module cache", "Restore race Go build cache"}, embeddedRaceBuilds)
	assertStepsBefore(t, mainEmbedded, append(embeddedRaceBuilds, "Upload binaries"), []string{"Save race Go build cache"})

	assertStepsBefore(t, workflows["main.yml"].job(t, "pr-core-wrapper"),
		[]string{"Restore Go module cache", "Restore race Go build cache"}, []string{"Run PR core wrapper"})
	mainTest := workflows["main.yml"].job(t, "test")
	assertStepsBefore(t, mainTest, []string{"Restore Go module cache"}, []string{"Install gotestsum", "Build", "Test (with coverage + JUnit XML)", "Test"})
	assertStepsBefore(t, mainTest, []string{"Restore non-race Go build cache"}, []string{"Build"})
	assertStepsBefore(t, mainTest, []string{"Restore race Go build cache"}, []string{"Test (with coverage + JUnit XML)", "Test"})
	assertStepsBefore(t, mainTest, []string{"Build", "Test"}, []string{"Save Go module cache"})
	assertStepsBefore(t, mainTest, []string{"Build"}, []string{"Save non-race Go build cache"})
	assertStepsBefore(t, mainTest, []string{"Test"}, []string{"Save race Go build cache"})

	mainWindows := workflows["main.yml"].job(t, "test-windows")
	assertStepsBefore(t, mainWindows, []string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Build (pure Go regex)"})
	assertStepsBefore(t, mainWindows, []string{"Build (pure Go regex)", "Smoke test - version", "Smoke test - help"}, []string{"Save Go module cache", "Save non-race Go build cache"})

	assertStepsBefore(t, workflows["pr.yml"].job(t, "build-artifacts"),
		[]string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Build reusable Linux artifacts"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "pr-core-wrapper"),
		[]string{"Restore Go module cache", "Restore race Go build cache"}, []string{"Run PR core wrapper"})
	prXCompile := workflows["pr.yml"].job(t, "windows-test-binaries")
	assertStepsBefore(t, prXCompile, []string{"Restore Go module cache"}, []string{"Verify Go module checksums", "Build Windows test binaries"})
	assertStepsBefore(t, prXCompile, []string{"Install mingw cross-compiler", "Restore Go build cache (Windows cross-compile)"}, []string{"Build Windows test binaries"})
	assertStepsBefore(t, prXCompile, []string{"Build Windows test binaries"}, []string{"Upload Windows test binaries"})

	mainXCompileCache := workflows["main.yml"].job(t, "windows-test-binaries-cache")
	assertStepsBefore(t, mainXCompileCache, []string{"Restore Go module cache"}, []string{"Install mingw cross-compiler", "Build Windows test binaries"})
	assertStepsBefore(t, mainXCompileCache, []string{"Install mingw cross-compiler", "Restore Go build cache (Windows cross-compile)"}, []string{"Build Windows test binaries"})
	assertStepsBefore(t, mainXCompileCache, []string{"Build Windows test binaries"}, []string{"Save Go module cache", "Save Go build cache (Windows cross-compile)"})

	assertStepsBefore(t, workflows["pr.yml"].job(t, "worktree-remove-windows"),
		[]string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Run native Windows worktree removal boundary tests"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "check-doc-freshness-platforms"),
		[]string{"Restore Go module cache"}, []string{"Exercise native date and Bash process boundary"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "pr-preflight-platforms"),
		[]string{"Restore Go module cache"}, []string{"Exercise the real Bash process boundary", "Exercise test.sh prebuilt binary path"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "advisory-reports"),
		[]string{"Restore Go module cache"}, []string{"Type-check every module under examples/"})

	prRiskEmbedded := workflows["pr-risk.yml"].job(t, "build-embedded")
	prRiskNonRaceBuilds := []string{"Build proxied bd subprocess binary", "Build server Dolt conformance test binary"}
	assertStepsBefore(t, prRiskEmbedded, []string{"Restore Go module cache"}, append(append([]string{}, embeddedRaceBuilds...), prRiskNonRaceBuilds...))
	assertStepsBefore(t, prRiskEmbedded, []string{"Restore race Go build cache"}, embeddedRaceBuilds)
	assertStepsBefore(t, prRiskEmbedded, []string{"Restore non-race Go build cache"}, prRiskNonRaceBuilds)

	assertGoCacheEnv(t, workflows["main.yml"].job(t, "build-artifacts"), "Build reusable Linux artifacts", "non-race")
	for _, stepName := range []string{"Build embedded bd binary", "Build embedded storage test binary", "Build embedded cmd test binary"} {
		assertGoCacheEnv(t, workflows["main.yml"].job(t, "build-embedded"), stepName, "race")
	}
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "test"), "Build", "non-race")
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "test"), "Test (with coverage + JUnit XML)", "race")
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "test"), "Test", "race")
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "test-windows"), "Build (pure Go regex)", "non-race")
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "windows-test-binaries-cache"), "Build Windows test binaries", xcompileCacheProfile())
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "build-artifacts"), "Build reusable Linux artifacts", "non-race")
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "pr-core-wrapper"), "Run PR core wrapper", "race")
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "windows-test-binaries"), "Build Windows test binaries", xcompileCacheProfile())
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "worktree-remove-windows"), "Run native Windows worktree removal boundary tests", "non-race")
	for _, stepName := range []string{"Build embedded bd binary", "Build embedded storage test binary", "Build embedded cmd test binary"} {
		assertGoCacheEnv(t, workflows["pr-risk.yml"].job(t, "build-embedded"), stepName, "race")
	}
	for _, stepName := range []string{"Build proxied bd subprocess binary", "Build server Dolt conformance test binary"} {
		assertGoCacheEnv(t, workflows["pr-risk.yml"].job(t, "build-embedded"), stepName, "non-race")
	}
	for _, workflowName := range []string{"pr.yml", "pr-risk.yml"} {
		for jobName, job := range workflows[workflowName].Jobs {
			for _, step := range job.Steps {
				family := actionFamily(step.Uses)
				if family == cacheSaveActionFamily || isForbiddenGoCacheActionFamily(family) {
					t.Errorf("%s job %q may not save cache in PR workflow", workflowName, jobName)
				}
			}
		}
	}

	assertGoCacheWriter(t, workflows["main.yml"], "build-artifacts", "ubuntu-latest", "Save Go module cache", cacheMissCondition(goModuleCacheRestoreID))
	assertGoCacheWriter(t, workflows["main.yml"], "build-artifacts", "ubuntu-latest", "Save non-race Go build cache", cacheMissCondition(goBuildCacheRestoreID("non-race")))
	assertGoCacheWriter(t, workflows["main.yml"], "build-embedded", "ubuntu-latest", "Save race Go build cache", cacheMissCondition(goBuildCacheRestoreID("race")))
	assertGoCacheWriter(t, workflows["main.yml"], "test", "${{ matrix.os }}", "Save Go module cache", failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goModuleCacheRestoreID)))
	assertGoCacheWriter(t, workflows["main.yml"], "test", "${{ matrix.os }}", "Save non-race Go build cache", failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goBuildCacheRestoreID("non-race"))))
	assertGoCacheWriter(t, workflows["main.yml"], "test", "${{ matrix.os }}", "Save race Go build cache", failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goBuildCacheRestoreID("race"))))
	assertGoCacheWriter(t, workflows["main.yml"], "test-windows", mainVenueBlacksmithWindows4vcpuRunsOn, "Save Go module cache", cacheMissCondition(goModuleCacheRestoreID))
	assertGoCacheWriter(t, workflows["main.yml"], "test-windows", mainVenueBlacksmithWindows4vcpuRunsOn, "Save non-race Go build cache", cacheMissCondition(goBuildCacheRestoreID("non-race")))
	assertGoCacheWriter(t, workflows["main.yml"], "pr-lint-wrapper", mainVenueBlacksmithUbuntu4vcpuRunsOn, "Save golangci-lint cache", failureSurvivingCacheSaveCondition(cacheMissCondition(golangciCacheRestoreID())))
	assertGoCacheWriter(t, workflows["main.yml"], "go-vet-cache", mainVenueBlacksmithUbuntu4vcpuRunsOn, "Save vet Go build cache", failureSurvivingCacheSaveCondition(cacheMissCondition(goVetCacheRestoreID())))
	assertGoCacheWriter(t, workflows["main.yml"], "blacksmith-go-build-cache", mainBlacksmithGoBuildCacheRunsOn, "Save race Go build cache", saveBuildCacheAfterFailureIf("race", "matrix.flavor == 'race'").ifCondition)
	assertGoCacheWriter(t, workflows["main.yml"], "blacksmith-go-build-cache", mainBlacksmithGoBuildCacheRunsOn, "Save non-race Go build cache", saveBuildCacheAfterFailureIf("non-race", "matrix.flavor == 'non-race'").ifCondition)
	assertGoCacheWriter(t, workflows["main.yml"], "windows-test-binaries-cache", mainWindowsTestBinariesCacheRunsOn, "Save Go module cache", saveModuleCacheAfterFailure().ifCondition)
	assertGoCacheWriter(t, workflows["main.yml"], "windows-test-binaries-cache", mainWindowsTestBinariesCacheRunsOn, "Save Go build cache (Windows cross-compile)", saveXCompileCache().ifCondition)
	for _, target := range []struct{ workflow, job string }{
		{"main.yml", "build-artifacts"},
		{"main.yml", "build-embedded"},
		{"main.yml", "pr-core-wrapper"},
		{"main.yml", "test"},
		{"main.yml", "test-windows"},
		{"main.yml", "windows-test-binaries-cache"},
		{"pr.yml", "build-artifacts"},
		{"pr.yml", "pr-core-wrapper"},
		{"pr.yml", "windows-test-binaries"},
		{"pr.yml", "worktree-remove-windows"},
		{"pr.yml", "check-doc-freshness-platforms"},
		{"pr.yml", "pr-preflight-platforms"},
		{"pr.yml", "advisory-reports"},
		{"pr-risk.yml", "build-embedded"},
	} {
		if got := workflows[target.workflow].job(t, target.job).step(t, "Set up Go").ID; got != "setup-go" {
			t.Errorf("%s job %q setup-go id = %q, want setup-go", target.workflow, target.job, got)
		}
	}
}

// TestMainWindowsTestBinariesCacheSeedsBothRunnerSelections is the regression
// test for F4 review SF-1: main.yml's windows-test-binaries-cache job must
// seed its cache keys from both runner selections pr.yml's windows-test-
// binaries job can land on (Blacksmith for same-repo PRs/merge_group,
// ubuntu-latest for forks/Dependabot), because Blacksmith and GitHub-hosted
// runners cannot see each other's actions/cache entries even for an
// identical key. A single-leg seeder would silently leave one of the two
// consumer paths permanently cold.
func TestMainWindowsTestBinariesCacheSeedsBothRunnerSelections(t *testing.T) {
	job := readCIWorkflow(t, "main.yml").job(t, "windows-test-binaries-cache")

	if job.RunsOn != mainWindowsTestBinariesCacheRunsOn {
		t.Errorf("windows-test-binaries-cache runs-on = %q, want %q", job.RunsOn, mainWindowsTestBinariesCacheRunsOn)
	}
	wantRunners := []string{"blacksmith", "github"}
	if !equalStrings(job.Strategy.Matrix.Runner, wantRunners) {
		t.Errorf("windows-test-binaries-cache strategy.matrix.runner = %v, want %v", job.Strategy.Matrix.Runner, wantRunners)
	}
	if job.Strategy.FailFast {
		t.Error("windows-test-binaries-cache strategy.fail-fast must be false: one runner selection being unavailable must not cancel the other's seed")
	}
	// Mirrors pr.yml's windows-test-binaries job (pr.yml:522's timeout is 30
	// too): this job runs the same mingw install + cross-compile.
	if job.TimeoutMinutes != 30 {
		t.Errorf("windows-test-binaries-cache timeout-minutes = %d, want 30", job.TimeoutMinutes)
	}
}

// TestPRRiskGateReachesFullServerDoltStorageSuite is the regression test for
// be-aiy5: test-server-storage already keeps a live Dolt server up (via
// build-embedded's /tmp/dolt-conformance-test binary, which compiles every
// top-level test in package internal/storage/dolt, not just conformance),
// but restricts execution to -test.run '^TestConformance$'. Every other
// server-gated test in that same binary -- TestCreateGuard_*,
// TestFederationPeerCredentialLifecycleLazyKeyInit, and diff-owned tests
// such as TestBenchDBPurgeDoesNotLeak on PR #5792 -- is reached by no
// PR-triggered lane, so it silently SKIPs instead of producing a real
// PASS/FAIL. A sibling job must run everything else in the same binary
// against the same live server, without a new build step, and must be wired
// into ci-gate as required so a SKIP there can no longer hide behind green.
func TestPRRiskGateReachesFullServerDoltStorageSuite(t *testing.T) {
	const jobName = "test-server-storage-full"

	workflow := readCIWorkflow(t, "pr-risk.yml")
	job := workflow.job(t, jobName)

	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("%s runs-on = %q, want ubuntu-latest", jobName, job.RunsOn)
	}
	if job.TimeoutMinutes != 20 {
		t.Errorf("%s timeout = %d minutes, want 20 (matches test-server-storage)", jobName, job.TimeoutMinutes)
	}
	if !contains(job.Needs, "detect-ci-tier") || !contains(job.Needs, "build-embedded") {
		t.Errorf("%s needs = %v, want detect-ci-tier and build-embedded (reuse the existing artifact, no new build)", jobName, job.Needs)
	}
	if sibling := workflow.job(t, "test-server-storage").If; job.If != sibling || !strings.HasPrefix(job.If, "needs.detect-ci-tier.outputs.full_embedded == 'true'") {
		t.Errorf("%s if = %q, want the same tier gate as test-server-storage (%q)", jobName, job.If, sibling)
	}

	download := job.step(t, "Download binaries")
	if download.With["name"] != "embedded-test-binaries" {
		t.Errorf("%s does not download the existing embedded-test-binaries artifact (would require a new build step): %v", jobName, download.With)
	}

	// Pro-rata against the embedded lane (75 shard-minutes / 324 tests):
	// 1126 tests at the same per-test cost is ~260 shard-minutes; 16 shards
	// x 15m = 240 shard-minutes, with the ~9.5-minute TestCloudAuthCLIRouting
	// outlier isolated on its own shard via the shard manifest.
	const totalShards = 16
	wantShards := make([]int, totalShards)
	for i := range wantShards {
		wantShards[i] = i + 1
	}
	if !reflect.DeepEqual(job.Strategy.Matrix.Shard, wantShards) {
		t.Errorf("%s strategy.matrix.shard = %v, want %v", jobName, job.Strategy.Matrix.Shard, wantShards)
	}
	if job.Strategy.FailFast {
		t.Errorf("%s strategy.fail-fast = true, want false (one slow/flaky shard should not cancel the others)", jobName)
	}

	wantTestCommand := fmt.Sprintf("bash .github/scripts/server-storage-test-shard.sh ${{ matrix.shard }} %d", totalShards)
	assertStepRunsExactly(t, job, "Test", wantTestCommand)
	// federation_test.go Fatals instead of silently skipping when this is set
	// and the server it expects isn't reachable -- this job's whole point is
	// a real PASS/FAIL, not a hidden self-skip if its own setup regresses.
	assertStepEnvValue(t, job, "Test", "BEADS_TEST_ENV_RUN_DOLT", "1")

	// test-server-storage itself is untouched: conformance keeps its own
	// dedicated job and timeout budget; this is an additive sibling, not a
	// widened filter on the existing job.
	existing := workflow.job(t, "test-server-storage")
	assertStepRunsExactly(t, existing, "Test", `/tmp/dolt-conformance-test -test.v -test.count=1 -test.timeout=15m -test.run '^TestConformance$'`)

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	const gateKey = "TEST_SERVER_STORAGE_FULL"
	if !contains(gate.Needs, jobName) {
		t.Errorf("ci-gate does not need %q: %v", jobName, gate.Needs)
	}
	if got, want := gateEnv[gateKey], fmt.Sprintf("${{ needs.%s.result }}", jobName); got != want {
		t.Errorf("ci-gate env %s = %q, want %q", gateKey, got, want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), gateKey) {
		t.Errorf("ci-gate CI_GATE_REQUIRED does not include %q", gateKey)
	}
}

func TestServerStorageShardScriptRunsPrebuiltBinaryFromPackageDir(t *testing.T) {
	// pr4107_corruption_test.go and journal_scope_completeness_test.go use
	// paths relative to internal/storage/dolt (e.g. ../schema/migrations,
	// ../issueops). `go test` runs a package's tests with cwd = the package
	// dir, so those resolve; a prebuilt test binary inherits the invoking
	// shell's cwd instead. server-storage-test-shard.sh is invoked from the
	// repo root (see TestPRRiskGateReachesFullServerDoltStorageSuite above),
	// so the prebuilt-binary branch must cd into the package dir itself,
	// immediately before exec -- any earlier and it breaks the repo-root-
	// relative manifest/discovery above it; any later, or absent, and the 6
	// relative-path tests fail with "open ../issueops: no such file or
	// directory".
	path := filepath.Join(sourceRepoRoot(t), ".github", "scripts", "server-storage-test-shard.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")

	indexOfContains := func(want string) int {
		for i, line := range lines {
			if strings.Contains(line, want) {
				return i
			}
		}
		return -1
	}
	indexOfExact := func(want string) int {
		for i, line := range lines {
			if strings.TrimSpace(line) == want {
				return i
			}
		}
		return -1
	}

	discoveryIndex := indexOfContains(`grep -rh '^func Test' internal/storage/dolt/*_test.go`)
	if discoveryIndex < 0 {
		t.Fatal("could not find repo-root-relative test-discovery grep line")
	}
	prebuiltBranchIndex := indexOfContains(`if [ -x "$STORAGE_BINARY" ]; then`)
	if prebuiltBranchIndex < 0 {
		t.Fatal("could not find prebuilt-binary branch")
	}
	execIndex := indexOfContains(`exec "$STORAGE_BINARY"`)
	if execIndex < 0 {
		t.Fatal("could not find prebuilt-binary exec line")
	}
	fallbackExecIndex := indexOfContains(`exec go test`)
	if fallbackExecIndex < 0 {
		t.Fatal("could not find go-test fallback exec line")
	}
	if fallbackExecIndex < execIndex {
		t.Fatal("go-test fallback exec line appears before the prebuilt-binary exec line -- branch order assumption violated")
	}

	cdIndex := indexOfExact("cd internal/storage/dolt")
	if cdIndex < 0 {
		t.Fatal(`script does not "cd internal/storage/dolt" before running the prebuilt binary -- ` +
			`relative-path tests (../schema/migrations, ../issueops) will fail when this script ` +
			`is invoked from the repo root, as pr-risk.yml's test-server-storage-full job does`)
	}
	if cdIndex <= discoveryIndex {
		t.Fatalf("cd internal/storage/dolt at line %d is at or before the repo-root-relative test "+
			"discovery grep at line %d -- that discovery must still run from the repo root",
			cdIndex+1, discoveryIndex+1)
	}
	if cdIndex <= prebuiltBranchIndex || cdIndex >= execIndex {
		t.Fatalf("cd internal/storage/dolt at line %d must sit strictly between the prebuilt-binary "+
			"branch at line %d and its exec at line %d", cdIndex+1, prebuiltBranchIndex+1, execIndex+1)
	}

	// The go-test fallback already scopes via the ./internal/storage/dolt/
	// argument (cwd = repo root is fine for `go test`); it must not also cd.
	for i := execIndex + 1; i <= fallbackExecIndex; i++ {
		if strings.TrimSpace(lines[i]) == "cd internal/storage/dolt" {
			t.Fatalf("unexpected cd internal/storage/dolt at line %d in the go-test fallback branch -- "+
				"it already scopes via the ./internal/storage/dolt/ argument", i+1)
		}
	}
}

func assertNoUnmanagedGoCacheSteps(t *testing.T, workflows map[string]ciWorkflow, managed map[string]map[string]bool) {
	t.Helper()

	for workflowName, workflow := range workflows {
		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				family := actionFamily(step.Uses)
				if isForbiddenGoCacheActionFamily(family) {
					t.Errorf("%s job %q has forbidden monolithic cache step %q", workflowName, jobName, step.Name)
					continue
				}
				if isGoCacheActionFamily(family) && !managed[workflowName][jobName] {
					t.Errorf("%s job %q has unmanaged cache step %q", workflowName, jobName, step.Name)
				}
			}
		}
	}
}

const (
	setupGoActionFamily         = "actions/setup-go"
	setupNodeActionFamily       = "actions/setup-node"
	setupPythonActionFamily     = "actions/setup-python"
	cacheMonolithicActionFamily = "actions/cache"
	cacheRestoreActionFamily    = "actions/cache/restore"
	cacheSaveActionFamily       = "actions/cache/save"
	setupGoSHA                  = "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	setupNodeSHA                = "820762786026740c76f36085b0efc47a31fe5020"
	setupPythonSHA              = "5fda3b95a4ea91299a34e894583c3862153e4b97"
	cacheSHA                    = "55cc8345863c7cc4c66a329aec7e433d2d1c52a9"
	goCacheSchema               = "v2"
	goBaseTag                   = "gms_pure_go"
	goModuleCachePath           = "~/go/pkg/mod"
	goModuleCacheRestoreID      = "restore-go-module-cache"
	macOSRunner                 = "macos-latest"
	macOSMatrixCondition        = "matrix.os == '" + macOSRunner + "'"
)

type goCacheStep struct {
	name        string
	id          string
	family      string
	key         string
	restoreKeys string
	path        string
	ifCondition string
}

func goModuleCacheKey() string {
	return "beads-go-mod-" + goCacheSchema + "-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-${{ hashFiles('go.mod', 'go.sum') }}"
}

func goModuleCacheRestoreKeys() string {
	return "beads-go-mod-" + goCacheSchema + "-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-"
}

func goBuildCachePrefix(profile string) string {
	// The base tag identifies the cache topology. Go's content-addressed
	// cache includes compiler options, so extra tags can safely share it.
	return "beads-go-build-" + goCacheSchema + "-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-base-" + goBaseTag + "-" + profile + "-"
}

func goBuildCacheKey(profile string) string {
	return goBuildCachePrefix(profile) + "${{ github.sha }}"
}

// goCacheDateSuffix is the key suffix for the day-bucketed caches saved by
// main.yml's push-to-main-only Blacksmith seeders (blacksmith-go-build-cache,
// go-vet-cache, test-windows): bounded by go.sum content plus a UTC calendar
// day, not by commit sha, so an ordinary day of push traffic cannot exhaust
// Blacksmith's 25 GB/week/repo cache quota the way a brand-new multi-GB entry
// per push did (F7b review fix S7; measured ~1.74-2.4GB/push under the old
// per-sha scheme). Each seeder's own "Compute cache date" step (id:
// cache-date) in main.yml computes the day half; restore-keys are unaffected,
// since they are already valid prefixes of this format too (they end right
// before where the sha/hash segment used to start). Pinned by
// TestBlacksmithSaverCacheKeysAreNotPerCommit.
func goCacheDateSuffix() string {
	return "${{ hashFiles('go.sum') }}-${{ steps.cache-date.outputs.today }}"
}

func goBuildCacheDayBucketedKey(profile string) string {
	return goBuildCachePrefix(profile) + goCacheDateSuffix()
}

// mainRestoreBuildCacheDayBucketedIf is mainRestoreBuildCacheIf with a
// day-bucketed key (S7), for main.yml's blacksmith-go-build-cache and
// test-windows seeders.
func mainRestoreBuildCacheDayBucketedIf(profile, condition string) goCacheStep {
	step := mainRestoreBuildCacheIf(profile, condition)
	step.key = goBuildCacheDayBucketedKey(profile)
	return step
}

// saveBuildCacheDayBucketedAfterFailureIf is saveBuildCacheAfterFailureIf
// with a day-bucketed key (S7), for main.yml's blacksmith-go-build-cache
// seeder.
func saveBuildCacheDayBucketedAfterFailureIf(profile, condition string) goCacheStep {
	step := saveBuildCacheAfterFailureIf(profile, condition)
	step.key = goBuildCacheDayBucketedKey(profile)
	return step
}

// saveBuildCacheDayBucketed is saveBuildCache with a day-bucketed key (S7),
// for main.yml's test-windows seeder.
func saveBuildCacheDayBucketed(profile string) goCacheStep {
	step := saveBuildCache(profile)
	step.key = goBuildCacheDayBucketedKey(profile)
	return step
}

func goBuildCachePath(profile string) string { return "${{ runner.temp }}/go-cache/" + profile }

func goBuildCacheRestoreID(profile string) string {
	return "restore-" + profile + "-go-build-cache"
}

func cacheMissCondition(restoreID string) string {
	return "steps." + restoreID + ".outputs.cache-hit != 'true'"
}

func combineConditions(conditions ...string) string {
	var nonEmpty []string
	for _, condition := range conditions {
		if condition != "" {
			nonEmpty = append(nonEmpty, condition)
		}
	}
	return strings.Join(nonEmpty, " && ")
}

func failureSurvivingCacheSaveCondition(conditions ...string) string {
	return "${{ !cancelled() && " + combineConditions(conditions...) + " }}"
}

func restoreModuleCache() goCacheStep {
	return goCacheStep{name: "Restore Go module cache", family: cacheRestoreActionFamily, key: goModuleCacheKey(), restoreKeys: goModuleCacheRestoreKeys(), path: goModuleCachePath}
}

func mainRestoreModuleCache() goCacheStep {
	step := restoreModuleCache()
	step.id = goModuleCacheRestoreID
	return step
}

func saveModuleCache() goCacheStep { return saveModuleCacheIf("") }

func saveModuleCacheIf(condition string) goCacheStep {
	return goCacheStep{name: "Save Go module cache", family: cacheSaveActionFamily, key: goModuleCacheKey(), path: goModuleCachePath, ifCondition: combineConditions(condition, cacheMissCondition(goModuleCacheRestoreID))}
}

func saveModuleCacheAfterFailureOnMacOS() goCacheStep {
	step := saveModuleCache()
	step.ifCondition = failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goModuleCacheRestoreID))
	return step
}

// saveModuleCacheAfterFailure is saveModuleCacheAfterFailureOnMacOS without
// the matrix condition, for single-runner saver jobs (windows-test-binaries-
// cache) where a flaky mid-build step (e.g. the mingw apt-get install)
// should not prevent the module cache fetched before it from being saved.
func saveModuleCacheAfterFailure() goCacheStep {
	step := saveModuleCache()
	step.ifCondition = failureSurvivingCacheSaveCondition(cacheMissCondition(goModuleCacheRestoreID))
	return step
}

func restoreBuildCache(profile string) goCacheStep {
	return restoreBuildCacheIf(profile, "")
}

func restoreBuildCacheIf(profile, condition string) goCacheStep {
	return goCacheStep{name: "Restore " + profile + " Go build cache", family: cacheRestoreActionFamily, key: goBuildCacheKey(profile), restoreKeys: goBuildCachePrefix(profile), path: goBuildCachePath(profile), ifCondition: condition}
}

func mainRestoreBuildCache(profile string) goCacheStep {
	return mainRestoreBuildCacheIf(profile, "")
}

func mainRestoreBuildCacheIf(profile, condition string) goCacheStep {
	step := restoreBuildCacheIf(profile, condition)
	step.id = goBuildCacheRestoreID(profile)
	return step
}

func saveBuildCache(profile string) goCacheStep { return saveBuildCacheIf(profile, "") }

func saveBuildCacheIf(profile, condition string) goCacheStep {
	return goCacheStep{name: "Save " + profile + " Go build cache", family: cacheSaveActionFamily, key: goBuildCacheKey(profile), path: goBuildCachePath(profile), ifCondition: combineConditions(condition, cacheMissCondition(goBuildCacheRestoreID(profile)))}
}

func saveBuildCacheAfterFailureOnMacOS(profile string) goCacheStep {
	step := saveBuildCache(profile)
	step.ifCondition = failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goBuildCacheRestoreID(profile)))
	return step
}

// saveBuildCacheAfterFailureIf is saveBuildCacheAfterFailureOnMacOS
// parameterized on an arbitrary matrix condition, for main.yml's
// blacksmith-go-build-cache job (F7b), which gates its race/non-race saves on
// matrix.flavor instead of matrix.os.
func saveBuildCacheAfterFailureIf(profile, condition string) goCacheStep {
	step := saveBuildCache(profile)
	step.ifCondition = failureSurvivingCacheSaveCondition(condition, cacheMissCondition(goBuildCacheRestoreID(profile)))
	return step
}

// The go-vet-cache family (F5.3): a dedicated, go-vet-only GOCACHE restored
// by pr.yml's scripts-go-checks vet leg and warmed by main.yml's go-vet-cache
// job on push. There is deliberately no non-race-build-cache fallback restore
// key (SF-5): actions/cache versions each entry by its path list, and that
// cache saves to a different path, so a prefix fallback naming it could never
// hit.
const goVetCacheSchema = "v1"

func goVetCachePrefix() string {
	return "beads-go-vet-" + goVetCacheSchema + "-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-base-" + goBaseTag + "-vet-"
}

func goVetCacheKey() string { return goVetCachePrefix() + "${{ github.sha }}" }

// goVetCacheDayBucketedKey is goVetCacheKey with the S7 day-bucketed suffix
// (see goCacheDateSuffix), for main.yml's go-vet-cache seeder.
func goVetCacheDayBucketedKey() string { return goVetCachePrefix() + goCacheDateSuffix() }

func goVetCacheRestoreKeys() string { return goVetCachePrefix() }

func goVetCachePath() string { return "${{ runner.temp }}/go-cache/vet" }

func goVetCacheRestoreID() string { return "restore-vet-go-build-cache" }

func restoreVetCache() goCacheStep {
	return goCacheStep{name: "Restore vet Go build cache", family: cacheRestoreActionFamily, key: goVetCacheKey(), restoreKeys: goVetCacheRestoreKeys(), path: goVetCachePath(), ifCondition: "matrix.check == 'vet'"}
}

func mainRestoreVetCache() goCacheStep {
	step := restoreVetCache()
	step.id = goVetCacheRestoreID()
	step.ifCondition = ""
	step.key = goVetCacheDayBucketedKey()
	return step
}

func saveVetCache() goCacheStep {
	return goCacheStep{name: "Save vet Go build cache", family: cacheSaveActionFamily, key: goVetCacheDayBucketedKey(), path: goVetCachePath(), ifCondition: failureSurvivingCacheSaveCondition(cacheMissCondition(goVetCacheRestoreID()))}
}

// The golangci-lint cache family (F5.2): one cache per PR Lint matrix leg,
// keyed on file content and the leg. A miss on the exact content key falls
// back to the leg's prefix (its most recent entry); golangci-lint revalidates
// a restored cache against the current config and inputs, so a stale restore
// cannot mask an issue. pr.yml only ever restores; main.yml's matrix is the
// only saver. The key deliberately omits the commit sha (SF-4): with it,
// every green push to main saved a new multi-GB entry per leg regardless of
// whether lint's inputs changed; keying on content alone means a save only
// happens when .golangci.yml or go.sum change. Only the native leg's path
// includes the lint GOCACHE (SF-4): the windows/darwin legs cross-compile and
// their GOCACHE is large relative to the small golangci-lint result cache
// that is the part actually worth keeping warm for them.
func golangciCachePrefix() string {
	return "beads-golangci-v1-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-v2.10.1-${{ matrix.target }}-"
}

func golangciCacheKey() string {
	return golangciCachePrefix() + "${{ hashFiles('.golangci.yml', 'go.sum') }}"
}

func golangciCacheRestoreKeys() string { return golangciCachePrefix() }

func golangciCachePath() string {
	return "~/.cache/golangci-lint\n${{ matrix.target == 'native' && format('{0}/go-cache/lint-{1}', runner.temp, matrix.target) || '' }}\n"
}

func golangciCacheRestoreID() string { return "restore-golangci-lint-cache" }

func restoreGolangciCache() goCacheStep {
	return goCacheStep{name: "Restore golangci-lint cache", family: cacheRestoreActionFamily, key: golangciCacheKey(), restoreKeys: golangciCacheRestoreKeys(), path: golangciCachePath()}
}

func mainRestoreGolangciCache() goCacheStep {
	step := restoreGolangciCache()
	step.id = golangciCacheRestoreID()
	return step
}

func saveGolangciCache() goCacheStep {
	return goCacheStep{name: "Save golangci-lint cache", family: cacheSaveActionFamily, key: golangciCacheKey(), path: golangciCachePath(), ifCondition: failureSurvivingCacheSaveCondition(cacheMissCondition(golangciCacheRestoreID()))}
}

// The Windows cross-compile GOCACHE (F4.4) is a separate cache dimension from
// goBuildCache*'s base/race/non-race profiles: it is never shared with a
// native build on the runner's own OS, so it gets its own key shape (no
// "-base-<tag>-" segment) and its own fixed restore/save id, rather than
// reusing goBuildCacheRestoreID's "restore-<profile>-go-build-cache" pattern.
func xcompileCacheProfile() string { return "xcompile-windows-amd64" }

func xcompileCachePrefix() string {
	return "beads-go-build-" + goCacheSchema + "-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-" + xcompileCacheProfile() + "-"
}

func xcompileCacheKey() string { return xcompileCachePrefix() + "${{ github.sha }}" }

func xcompileCachePath() string { return goBuildCachePath(xcompileCacheProfile()) }

func xcompileCacheRestoreID() string { return "restore-xcompile-go-build-cache" }

// restoreXCompileCache is the PR-side (restore-only, no id needed since
// pr.yml never saves) shape; mainRestoreXCompileCache is main.yml's
// saver-side shape, which needs the id for its Save step's cache-miss `if`.
func restoreXCompileCache() goCacheStep {
	return goCacheStep{name: "Restore Go build cache (Windows cross-compile)", family: cacheRestoreActionFamily, key: xcompileCacheKey(), restoreKeys: xcompileCachePrefix(), path: xcompileCachePath()}
}

func mainRestoreXCompileCache() goCacheStep {
	step := restoreXCompileCache()
	step.id = xcompileCacheRestoreID()
	return step
}

func saveXCompileCache() goCacheStep {
	return goCacheStep{name: "Save Go build cache (Windows cross-compile)", family: cacheSaveActionFamily, key: xcompileCacheKey(), path: xcompileCachePath(), ifCondition: failureSurvivingCacheSaveCondition(cacheMissCondition(xcompileCacheRestoreID()))}
}

func assertGoCacheInventory(t *testing.T, job ciWorkflowJob, want []goCacheStep) {
	t.Helper()

	var got []ciWorkflowStep
	for _, step := range job.Steps {
		if isGoCacheActionFamily(actionFamily(step.Uses)) {
			got = append(got, step)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("cache steps = %d, want %d; got %+v", len(got), len(want), got)
	}
	for i, expected := range want {
		step := got[i]
		if step.Name != expected.name || step.ID != expected.id || actionFamily(step.Uses) != expected.family || step.With["key"] != expected.key || step.With["restore-keys"] != expected.restoreKeys || step.With["path"] != expected.path || step.If != expected.ifCondition {
			t.Errorf("cache step %d = {name:%q id:%q family:%q key:%q restore-keys:%q path:%q if:%q}, want {name:%q id:%q family:%q key:%q restore-keys:%q path:%q if:%q}", i, step.Name, step.ID, actionFamily(step.Uses), step.With["key"], step.With["restore-keys"], step.With["path"], step.If, expected.name, expected.id, expected.family, expected.key, expected.restoreKeys, expected.path, expected.ifCondition)
		}
	}
}

func assertConditionalCacheWritersHaveMatrixMember(t *testing.T, job ciWorkflowJob, wantMember string) {
	t.Helper()

	memberCount := 0
	for _, member := range job.Strategy.Matrix.OS {
		if member == wantMember {
			memberCount++
		}
	}
	if memberCount != 1 {
		t.Errorf("strategy matrix os has %d concrete %q members, want exactly 1: %v", memberCount, wantMember, job.Strategy.Matrix.OS)
	}

	conditionalWriters := 0
	wantCondition := "matrix.os == '" + wantMember + "'"
	for _, step := range job.Steps {
		if actionFamily(step.Uses) != cacheSaveActionFamily {
			continue
		}
		if !strings.Contains(step.If, wantCondition) {
			t.Errorf("cache writer %q condition %q does not target matrix member %q", step.Name, step.If, wantMember)
			continue
		}
		conditionalWriters++
	}
	if conditionalWriters == 0 {
		t.Fatal("job has no conditional matrix cache writers")
	}
}

func assertStepsBefore(t *testing.T, job ciWorkflowJob, before, after []string) {
	t.Helper()

	for _, beforeName := range before {
		beforeIndex := job.stepIndex(t, beforeName)
		for _, afterName := range after {
			afterIndex := job.stepIndex(t, afterName)
			if beforeIndex >= afterIndex {
				t.Errorf("step %q index %d must precede %q index %d", beforeName, beforeIndex, afterName, afterIndex)
			}
		}
	}
}

func assertGoCacheEnv(t *testing.T, job ciWorkflowJob, stepName, profile string) {
	t.Helper()
	if got := job.step(t, stepName).Env["GOCACHE"]; got != goBuildCachePath(profile) {
		t.Errorf("step %q GOCACHE = %q, want %q", stepName, got, goBuildCachePath(profile))
	}
}

func assertStepRunsExactly(t *testing.T, job ciWorkflowJob, stepName, want string) {
	t.Helper()
	if got := job.step(t, stepName).Run; got != want {
		t.Errorf("step %q run = %q, want %q", stepName, got, want)
	}
}

func assertStepEnvValue(t *testing.T, job ciWorkflowJob, stepName, key, want string) {
	t.Helper()
	if got := job.step(t, stepName).Env[key]; got != want {
		t.Errorf("step %q env %s = %q, want %q", stepName, key, got, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// matrixIncludeHosts returns the sorted, de-duplicated set of matrix.include
// os values -- used by tests covering pr-preflight-platforms and
// check-doc-freshness-platforms now that F7b restructured them from a
// top-level os: list into an include:-only "matrix marker" shape.
func matrixIncludeHosts(include []ciWorkflowMatrixInclude) []string {
	seen := make(map[string]bool)
	var hosts []string
	for _, tuple := range include {
		if !seen[tuple.OS] {
			seen[tuple.OS] = true
			hosts = append(hosts, tuple.OS)
		}
	}
	sort.Strings(hosts)
	return hosts
}

func assertGoCacheWriter(t *testing.T, workflow ciWorkflow, wantJob, wantRunner, wantStep, wantCondition string) {
	t.Helper()

	var writers []string
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if job.RunsOn == wantRunner && actionFamily(step.Uses) == cacheSaveActionFamily && step.Name == wantStep && step.If == wantCondition {
				writers = append(writers, jobName+"/"+step.Name)
			}
		}
	}
	want := wantJob + "/" + wantStep
	if len(writers) != 1 || writers[0] != want {
		t.Errorf("cache writer %q with if %q = %v, want exactly [%s]", wantStep, wantCondition, writers, want)
	}
}

var actionPin = regexp.MustCompile(`^[0-9a-f]{40}$`)

func actionFamily(uses string) string {
	family, _, found := strings.Cut(uses, "@")
	if !found {
		return uses
	}
	return family
}

func isGoCacheActionFamily(family string) bool {
	return family == cacheMonolithicActionFamily || family == cacheRestoreActionFamily || family == cacheSaveActionFamily
}

func isForbiddenGoCacheActionFamily(family string) bool {
	return family == cacheMonolithicActionFamily
}

func assertPinnedGoCacheActions(t *testing.T, workflowName string, workflow ciWorkflow) {
	t.Helper()

	allowed := map[string]string{
		setupGoActionFamily:      setupGoSHA,
		cacheRestoreActionFamily: cacheSHA,
		cacheSaveActionFamily:    cacheSHA,
	}
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			family := actionFamily(step.Uses)
			wantSHA, managed := allowed[family]
			if isForbiddenGoCacheActionFamily(family) {
				managed = true
			}
			if !managed {
				continue
			}
			_, sha, found := strings.Cut(step.Uses, "@")
			if !found || !actionPin.MatchString(sha) {
				t.Errorf("%s job %q step %q action %q is not pinned to exactly 40 lowercase hex characters", workflowName, jobName, step.Name, step.Uses)
			}
			if isForbiddenGoCacheActionFamily(family) {
				t.Errorf("%s job %q step %q uses forbidden monolithic action %q", workflowName, jobName, step.Name, family)
				continue
			}
			if !found || !actionPin.MatchString(sha) {
				continue
			}
			if sha != wantSHA {
				t.Errorf("%s job %q step %q action %q has SHA %q, want released SHA %q", workflowName, jobName, step.Name, family, sha, wantSHA)
			}
			if family == setupGoActionFamily && step.With["cache"] != "false" {
				t.Errorf("%s job %q setup-go cache = %q, want false", workflowName, jobName, step.With["cache"])
			}
		}
	}
}

type ciWorkflow struct {
	Env  map[string]string        `yaml:"env"`
	Jobs map[string]ciWorkflowJob `yaml:"jobs"`
}

type ciWorkflowJob struct {
	Name            string               `yaml:"name"`
	Uses            string               `yaml:"uses"`
	With            map[string]string    `yaml:"with"`
	Secrets         any                  `yaml:"secrets"`
	Permissions     any                  `yaml:"permissions"`
	Needs           ciWorkflowStringList `yaml:"needs"`
	Steps           []ciWorkflowStep     `yaml:"steps"`
	RunsOn          string               `yaml:"runs-on"`
	If              string               `yaml:"if"`
	ContinueOnError bool                 `yaml:"continue-on-error"`
	TimeoutMinutes  int                  `yaml:"timeout-minutes"`
	Strategy        ciWorkflowStrategy   `yaml:"strategy"`
	Env             map[string]string    `yaml:"env"`
	Outputs         map[string]string    `yaml:"outputs"`
}

type ciWorkflowStrategy struct {
	FailFast bool             `yaml:"fail-fast"`
	Matrix   ciWorkflowMatrix `yaml:"matrix"`
}

type ciWorkflowMatrix struct {
	OS     []string `yaml:"os"`
	Runner []string `yaml:"runner"`
	Shard  []int    `yaml:"shard"`
	Target []string `yaml:"target"`
	Check  []string `yaml:"check"`
	Group  []string `yaml:"group"`
	// Venue and Flavor back main.yml's "venue matrix" (F7b spec §4) push-to-
	// main-only Blacksmith cache savers: pr-lint-wrapper/go-vet-cache/
	// test-windows's `venue: [blacksmith, github]` and
	// blacksmith-go-build-cache's `flavor: [race, non-race]`. Pinned by
	// TestBlacksmithSaverVenueAndFlavorMatricesAreComplete (F7b review B2/S7
	// follow-up) so dropping either leg from these lists -- silently losing
	// that leg's cache seed or (for flavor) its race coverage -- fails loudly.
	Venue   []string                  `yaml:"venue"`
	Flavor  []string                  `yaml:"flavor"`
	Include []ciWorkflowMatrixInclude `yaml:"include"`
}

type ciWorkflowMatrixInclude struct {
	OS           string `yaml:"os"`
	ExpectedGOOS string `yaml:"expected_goos"`
	Coverage     bool   `yaml:"coverage"`
	TestFlags    string `yaml:"test-flags"`
	Runner       string `yaml:"runner"`
	// F7b review fix (S1): no typed `shard` field here. pr-preflight-
	// platforms' Windows shard split (the one thing in this file that ever
	// used it) was reverted as not measurably helpful, and a typed field
	// here is shared by every workflow's `strategy.matrix.include` tuples —
	// migration-test.yml's shard: src/pre-dolt/dolt-runtime entries use a
	// STRING, not an int, so a mismatched type here would break parsing of
	// an unrelated workflow. Untyped matrix keys fall through to Extra.
	Extra map[string]any `yaml:",inline"`
}

type ciWorkflowStep struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	If              string            `yaml:"if"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	Shell           string            `yaml:"shell"`
	ContinueOnError any               `yaml:"continue-on-error"`
	TimeoutMinutes  int               `yaml:"timeout-minutes"`
	Env             map[string]string `yaml:"env"`
	With            map[string]string `yaml:"with"`
}

type ciWorkflowStringList []string

func (items *ciWorkflowStringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*items = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("needs item must be scalar, got YAML kind %d", item.Kind)
			}
			values = append(values, item.Value)
		}
		*items = values
		return nil
	default:
		return fmt.Errorf("needs must be scalar or sequence, got YAML kind %d", node.Kind)
	}
}

func readCIWorkflow(t *testing.T, name string) ciWorkflow {
	t.Helper()

	path := filepath.Join(sourceRepoRoot(t), ".github", "workflows", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var workflow ciWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return workflow
}

func (workflow ciWorkflow) job(t *testing.T, name string) ciWorkflowJob {
	t.Helper()

	job, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("workflow has no %q job", name)
	}
	return job
}

func (job ciWorkflowJob) step(t *testing.T, name string) ciWorkflowStep {
	t.Helper()

	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("job has no %q step", name)
	return ciWorkflowStep{}
}

func (job ciWorkflowJob) stepIndex(t *testing.T, name string) int {
	t.Helper()

	index := -1
	for i, step := range job.Steps {
		if step.Name != name {
			continue
		}
		if index >= 0 {
			t.Fatalf("job has more than one %q step", name)
		}
		index = i
	}
	if index < 0 {
		t.Fatalf("job has no %q step", name)
	}
	return index
}

func assertJobRunsExactly(t *testing.T, job ciWorkflowJob, want string) {
	t.Helper()

	for _, step := range job.Steps {
		if strings.TrimSpace(step.Run) == want {
			return
		}
	}
	t.Errorf("job has no step that runs exactly %q", want)
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// TestWorkflowsInstallPinnedDolt keeps the Dolt CLI under test pinned. Every
// workflow used to install it by piping dolthub/dolt's releases/latest
// install.sh, so the binary under test changed whenever upstream published —
// including backports, which can move "latest" backwards. When Dolt 2.3.0
// landed it regressed CALL DOLT_RESET('--hard'): roughly one freshly created
// database in twenty comes up with the procedure permanently broken
// ("Error 1105 (HY000): context canceled"), which made
// TestFreshBootstrapHealIncarnation fail on a coin flip. See
// scripts/ci/install-dolt.sh for the per-version measurements. The CLI is now
// pinned to the same release as the container image, so the two halves of
// every server-mode test (the per-test sql-server doltserver.Start launches,
// and the shared container) can never drift apart.
func TestWorkflowsInstallPinnedDolt(t *testing.T) {
	workflowDir := filepath.Join(sourceRepoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatal(err)
	}

	installers := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}
		path := filepath.Join(workflowDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		if strings.Contains(body, "dolt/releases/latest") {
			t.Errorf("%s installs dolt from releases/latest; use ./scripts/ci/install-dolt.sh so the "+
				"binary under test is pinned", entry.Name())
		}
		installers += strings.Count(body, "scripts/ci/install-dolt.sh")
	}
	if installers == 0 {
		t.Fatal("no workflow installs dolt via scripts/ci/install-dolt.sh — the pin is not wired up")
	}
}

// TestPinnedDoltCLIMatchesContainerImage keeps the CLI pin, the sql-server
// container pin and the hermetic Bazel dolt (tools/bazel/dolt.bzl) on the same
// Dolt release. Server-mode tests run both at once
// against the same databases; a drifting pair tests a combination no release
// ever shipped.
func TestPinnedDoltCLIMatchesContainerImage(t *testing.T) {
	root := sourceRepoRoot(t)

	installer, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "install-dolt.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cliVersion := captureOne(t, `(?m)^readonly version="([0-9]+\.[0-9]+\.[0-9]+)"$`, string(installer), "scripts/ci/install-dolt.sh")

	common, err := os.ReadFile(filepath.Join(root, "internal", "testutil", "testdoltcommon.go"))
	if err != nil {
		t.Fatal(err)
	}
	imageVersion := captureOne(t, `dolthub/dolt-sql-server:([0-9]+\.[0-9]+\.[0-9]+)`, string(common), "testdoltcommon.go:DoltDockerImage")

	pullScript, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "pull-dolt-image.sh"))
	if err != nil {
		t.Fatal(err)
	}
	pullVersion := captureOne(t, `dolthub/dolt-sql-server:([0-9]+\.[0-9]+\.[0-9]+)`, string(pullScript), "scripts/ci/pull-dolt-image.sh")

	bazelRule, err := os.ReadFile(filepath.Join(root, "tools", "bazel", "dolt.bzl"))
	if err != nil {
		t.Fatal(err)
	}
	bazelVersion := captureOne(t, `(?m)^DOLT_VERSION = "([0-9]+\.[0-9]+\.[0-9]+)"$`, string(bazelRule), "tools/bazel/dolt.bzl:DOLT_VERSION")

	if cliVersion != imageVersion || cliVersion != pullVersion || cliVersion != bazelVersion {
		t.Errorf("dolt pins disagree: CLI %s, DoltDockerImage %s, pull-dolt-image.sh %s, tools/bazel/dolt.bzl %s",
			cliVersion, imageVersion, pullVersion, bazelVersion)
	}
}

// TestProxiedLocalSmokeMatchesPinnedDoltVersion keeps the proxied-local-smoke
// lane's standalone Dolt CLI install on the same release as the rest of the
// suite. That lane downloads its own dolt binary straight from GitHub
// releases instead of going through scripts/ci/install-dolt.sh, so nothing
// else catches it drifting off the measured pin (see "Which Dolt version to
// install" in docs/architecture/dolt.md for why the pin is not just "latest").
func TestProxiedLocalSmokeMatchesPinnedDoltVersion(t *testing.T) {
	root := sourceRepoRoot(t)

	installer, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "install-dolt.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cliVersion := captureOne(t, `(?m)^readonly version="([0-9]+\.[0-9]+\.[0-9]+)"$`, string(installer), "scripts/ci/install-dolt.sh")

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "proxied-local-smoke.yml"))
	if err != nil {
		t.Fatal(err)
	}
	smokeVersion := captureOne(t, `(?m)^\s*DOLT_VERSION:\s*([0-9]+\.[0-9]+\.[0-9]+)\s*$`, string(workflow), "proxied-local-smoke.yml:DOLT_VERSION")

	if cliVersion != smokeVersion {
		t.Errorf("dolt pins disagree: CLI %s, proxied-local-smoke.yml DOLT_VERSION %s", cliVersion, smokeVersion)
	}
}

func captureOne(t *testing.T, pattern, body, source string) string {
	t.Helper()

	matches := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if matches == nil {
		t.Fatalf("%s does not match %s", source, pattern)
	}
	return matches[1]
}

// --- Bazel lane (.github/workflows/bazel.yml, gated through pr.yml) ----------

const (
	bazelWorkflowName   = "bazel.yml"
	bazelJobName        = "bazel-test"
	bazelPureJobName    = "bazel-pure"
	bazelDoltJobName    = "bazel-doltserver"
	bazelEmbedJobName   = "bazel-embedded"
	bazelRBEJobName     = "rbe"
	bazelIntegJobName   = "bazel-integration"
	bazelProxiedJobName = "bazel-proxied"
	bazelServerJobName  = "bazel-server-storage"
	// F3: the MCP and npm package gates, moved here from pr.yml so they need
	// only the rbe job. Unlike every other lane they do not mirror a Bazel
	// config; pr.yml opts them in with package-gates: "on".
	bazelPackageMCPJobName = "package-mcp"
	bazelPackageNPMJobName = "package-npm"
	// Best-effort, advisory pre-warm of gastownhall/gascity's rbe-west OSS
	// worker pool; see bazel.yml's own comment and
	// engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "rbe-west Pre-warm".
	bazelRBEPrewarmJobName = "rbe-prewarm"
	setupBazelActionDir    = ".github/actions/setup-bazel"
)

// F3: package-mcp and package-npm, the two lanes bazel-gate.sh does not
// know about at all - their skip reason is the caller's package-gates
// input, never the rbe job's mode.
var bazelPackageJobs = map[string]bool{bazelPackageMCPJobName: true, bazelPackageNPMJobName: true}

const (
	uploadArtifactSHA   = "043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"
	downloadArtifactSHA = "3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c"
	checkoutSHA         = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	bazelCacheKeyPrefix = "bazel-repo-v3-${{ runner.os }}-"
	bazelCacheKey       = bazelCacheKeyPrefix + "${{ hashFiles('.bazelversion', 'MODULE.bazel.lock') }}"
	bazelCachePath      = "${{ runner.temp }}/bazel-ci-cache"
	// Save only from a push to main that missed the exact key: the content is
	// fixed by the key, so re-saving every push only churns the quota.
	bazelCacheSaveIf = "${{ always() && github.event_name == 'push' && github.ref == 'refs/heads/main' && " +
		"steps.bazel.outcome == 'success' && steps.bazel.outputs.cache-hit != 'true' }}"
)

// bazel.yml's jobs: the rbe job that decides the execution mode, the
// --config=ci lane, one job per CI job a Bazel config mirrors, and the two
// package gates (F3), sorted alphabetically to match TestBazelWorkflowJobsAndExecutionMode's sort.Strings.
var bazelJobNames = []string{bazelDoltJobName, bazelEmbedJobName, bazelIntegJobName, bazelProxiedJobName, bazelPureJobName, bazelServerJobName, bazelJobName, bazelPackageMCPJobName, bazelPackageNPMJobName, bazelRBEJobName, bazelRBEPrewarmJobName}

// The lanes that only run remotely (skipped unless the rbe job chose remote);
// bazel-integration runs remotely or with the read-only cache (bazelIntegIf);
// every other lane also runs locally.
var bazelRemoteOnlyJobs = map[string]bool{bazelEmbedJobName: true, bazelProxiedJobName: true, bazelServerJobName: true}

// The rbe job's decision step reads exactly one secret, and only to test it
// for emptiness: its env value is a boolean, not the secret.
const (
	bazelRBESecretPath  = ".jobs." + bazelRBEJobName + ".steps[0].env.HAS_EXECUTOR"
	bazelRBESecretValue = "${{ secrets.RBE_WEST_EXECUTOR != '' }}"
)

// rbe-prewarm reads its two app secrets in exactly three places, mirroring
// the rbe job's own emptiness-test/mint pattern above: an env boolean
// (never an `if:`) gating the mint step, and the mint step's own `with:`
// (the only step in bazel.yml allowed to read a secret outside a
// setup-bazel env, alongside bazelRBESecretPath/Value). Step indices are
// steps[0]=credential check, [1]=mint, [2]=dispatch, [3]=record result (no
// checkout step: B1, security review of bdef342d5).
const (
	bazelRBEPrewarmHasAppPath  = ".jobs." + bazelRBEPrewarmJobName + ".steps[0].env.HAS_POOL_APP"
	bazelRBEPrewarmHasAppValue = "${{ secrets.RBE_POOL_APP_PRIVATE_KEY != '' }}"
	bazelRBEPrewarmAppIDPath   = ".jobs." + bazelRBEPrewarmJobName + ".steps[1].with.app-id"
	bazelRBEPrewarmAppIDValue  = "${{ secrets.RBE_POOL_APP_ID }}"
	bazelRBEPrewarmKeyPath     = ".jobs." + bazelRBEPrewarmJobName + ".steps[1].with.private-key"
	bazelRBEPrewarmKeyValue    = "${{ secrets.RBE_POOL_APP_PRIVATE_KEY }}"
	// The job-level continue-on-error path itself: see
	// TestBazelWorkflowJobsAndExecutionMode's comment on the same exception.
	bazelRBEPrewarmContinueOnErrorPath = ".jobs." + bazelRBEPrewarmJobName + ".continue-on-error"
)

// rbe-fork: the lanes' setup-bazel env that asks it for a certificate (modes
// fork-ro and fork-rw only), and the mint's status URL the rbe job probes.
const (
	bazelForkRemoteValue = "${{ startsWith(needs.rbe.outputs.mode, 'fork-') && 'true' || '' }}"
	bazelMintStatusURL   = "https://rbe-mint.ops.gascity.com:8444/v1/status?repo="
)

// The only triggers bazel.yml may have. pull_request_target (and
// workflow_run) would run with secrets in the context of fork PRs. PRs and
// merge groups reach it only through pr.yml's call, so it runs once per PR.
var bazelWorkflowTriggers = []string{"push", "workflow_call", "workflow_dispatch"}

// What pr.yml's ci-gate does with each bazel.yml lane (every job but the rbe
// job, whose rbe-mode output the gate reads): a gated lane has a
// CI_GATE_REQUIRED id read from its workflow_call output; an advisory lane is
// not part of pr.yml's call at all (the call's inputs turn it off, checked by
// TestBazelGateSimulation), for the recorded reason. A new job in bazel.yml
// must be added to one of the two (TestBazelLaneIsGatedAlongsideLegacy).
var bazelLaneGateIDs = map[string]string{
	bazelJobName:      "BAZEL_TEST",
	bazelPureJobName:  "BAZEL_PURE",
	bazelEmbedJobName: "BAZEL_EMBEDDED",
	bazelDoltJobName:  "BAZEL_DOLTSERVER",
	// Remote-only PR Risk tiers (fork and Dependabot PRs rely on
	// pr-risk.yml's legacy jobs, like the embedded tier's).
	bazelProxiedJobName: "BAZEL_PROXIED",
	bazelServerJobName:  "BAZEL_SERVER_STORAGE",
	// main.yml's integration jobs, required on PRs although their legacy
	// twins run only on push to main: remotely on same-repo PRs, locally
	// with the read-only cache (mode cache) on fork and Dependabot PRs.
	bazelIntegJobName: "BAZEL_INTEGRATION",
	// F3: never skippable on pr.yml (package-gates is always "on" there), so
	// bazel-gate.sh's skip list never names them.
	bazelPackageMCPJobName: "PACKAGE_MCP",
	bazelPackageNPMJobName: "PACKAGE_NPM",
}

// rbe-prewarm: not a build/test lane at all (no Bazel config mirrors it), so
// it has nothing for ci-gate to require; it is advisory by design
// (continue-on-error, best-effort pre-warm dispatch) and reports only so
// TestBazelLaneIsGatedAlongsideLegacy's generic shape check still applies.
var bazelAdvisoryLanes = map[string]string{
	bazelRBEPrewarmJobName: "best-effort pre-warm dispatch to gastownhall/gascity; never gates or blocks a lane",
}

// bazel-integration's if: remote (modes remote, fork-ro, fork-rw: enabled),
// or local with the read-only cache (mode cache: fork and Dependabot PRs
// while rbe-fork is closed, whose only PR-time integration run it is; a
// warm run builds and tests only what the PR changed), never plain local
// (rbe=off: a cold tagged race build of //... on one runner); and off when a
// caller passes integration: "off" (pr.yml and bazel-farm.yml pass "on"). A
// string input: on push and dispatch it is null, and null != 'off', so the
// lane keeps running on main.
const bazelIntegIf = "${{ (needs.rbe.outputs.enabled == 'true' || needs.rbe.outputs.mode == 'cache') && inputs.integration != 'off' }}"

// F3: package-mcp and package-npm's if. Unlike every other lane's, it does
// not read the rbe job's outputs at all - only the caller's package-gates
// input - so these two never skip because of execution mode, only because a
// caller left package-gates at its default "off".
const bazelPackageGatesIf = "${{ inputs.package-gates == 'on' }}"

// F3: the package gates' runner. 4 vCPU, not bazel.yml's usual 2: pytest-xdist
// -n 8 is pinned to measured timing on a 4 vCPU runner (tools/f3). Mode
// remote only: the gates take no rbe-fork certificate, so fork modes
// (enabled too) build bd with go build on a GitHub-hosted runner, as before.
const bazelPackageRunsOn = "${{ needs.rbe.outputs.mode == 'remote' && 'blacksmith-4vcpu-ubuntu-2404' || 'ubuntu-latest' }}"

// rbe-prewarm's bespoke if (TestBazelWorkflowJobsAndExecutionMode): mode
// remote only (B1, security review of bdef342d5 - was remote or fork-rw).
// fork-rw is reached only by a fork or Dependabot pull_request run, which
// never carries a workflow_call secret regardless of this if (GitHub does
// not forward repository/App secrets to a fork's pull_request event), so
// that half of the old condition always found no-app and no-op'd; it also
// cost an idle runner-minute for nothing. bazel-farm.yml no longer forwards
// the app secrets either (TestBazelFarmWorkflowSecurity), removing the only
// other path that could have reached fork-rw with the credential attached.
// Its runs-on still reuses the exact same mode-ternary every other lane
// uses (wantRunsOn): TestBazelRBEJobDecidesOnce forbids re-deriving
// fork/event facts outside the rbe job.
const bazelRBEPrewarmIf = "${{ needs.rbe.outputs.mode == 'remote' }}"

// F3: the rbe job's own runner (not gated by its own outputs - it decides
// them). Same-repo PRs and merge_group/push/dispatch/schedule (never forks)
// get Blacksmith; a pull_request_target farm run and any fork or Dependabot
// PR stay GitHub-hosted. Pinned verbatim by TestSameRepoBlacksmithRunners.
const wantRBERunsOn = "${{ (github.event_name == 'push' || github.event_name == 'workflow_dispatch' || github.event_name == 'schedule' || github.event_name == 'merge_group' || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository && github.actor != 'dependabot[bot]')) && 'blacksmith-2vcpu-ubuntu-2404' || 'ubuntu-latest' }}"

// The sameRepoBlacksmith{2,4,8}vcpu consts and the real-expression evaluator
// that checks them now live in ci_blacksmith_runner_test.go (shared with
// F7c; see that file's doc comment for the API F7c rebases onto).

// F4 review SF-1: main.yml's windows-test-binaries-cache job seeds the same
// cache keys/paths from both runner selections pr.yml's windows-test-binaries
// job can land on (sameRepoBlacksmith8vcpu, defined in
// ci_blacksmith_runner_test.go, for same-repo PRs/merge_group; ubuntu-latest
// for forks/Dependabot). Blacksmith runners can't
// see a GitHub-hosted runner's actions/cache entries (and vice versa) even
// though both report the same runner.os/runner.arch, so a single-leg seeder
// only ever warms one of the two consumer paths. matrix.runner (not a
// literal Blacksmith label substituted directly into runs-on) keeps
// actionlint from trying and failing to resolve an undeclared self-hosted
// label: it treats this ternary form as a dynamic value and skips it, same
// as every other Blacksmith runs-on in this repo.
const mainWindowsTestBinariesCacheRunsOn = "${{ matrix.runner == 'blacksmith' && 'blacksmith-8vcpu-ubuntu-2404' || 'ubuntu-latest' }}"

// F7c: used to define its own sameRepoBlacksmith4vcpu here (same expression
// as sameRepoBlacksmith2vcpu with the 4 vCPU label, for advisory jobs that
// compile Go: conformance.yml, regression.yml, migration-test.yml,
// cross-version-smoke.yml, proxied-local-smoke.yml). F7a independently
// defined the same const; both are now served by the single shared
// sameRepoBlacksmith4vcpu in ci_blacksmith_runner_test.go. migration-test.yml's
// historical-upgrades job uses sameRepoBlacksmith4vcpuNoble instead (below).

// F7c review fix (B1): migration-test.yml's historical-upgrades job cannot
// use the plain ubuntu-latest fallback that sameRepoBlacksmith4vcpu uses,
// because the v0.55.4 fixture is dynamically linked against the ICU ABI
// shipped by Ubuntu 24.04's libicu74 package, and GitHub's ubuntu-latest
// label is scheduled to move from 24.04 to 26.04 starting 2026-10-19 (see
// actions/runner-images#14748); 26.04 drops libicu74 entirely. The fallback
// here is pinned to the literal `ubuntu-24.04` label instead so the
// non-Blacksmith path keeps working after that migration regardless of when
// it lands. Do not fold this back into sameRepoBlacksmith4vcpu.
const sameRepoBlacksmith4vcpuNoble = "${{ (github.event_name == 'merge_group' || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository && github.actor != 'dependabot[bot]')) && 'blacksmith-4vcpu-ubuntu-2404' || 'ubuntu-24.04' }}"

// F7b: venue-matrix runs-on for main.yml jobs that seed a Blacksmith GOCACHE
// alongside their existing GitHub-hosted seed ("venue: [blacksmith, github]"):
// pr-lint-wrapper, go-vet-cache, test-windows. blacksmith-go-build-cache needs
// no fork/github fallback leg (main.yml only runs on push to main), so its
// runs-on is a bare Blacksmith literal wrapped for actionlint, not a matrix.
const mainVenueBlacksmithUbuntu4vcpuRunsOn = "${{ matrix.venue == 'blacksmith' && 'blacksmith-4vcpu-ubuntu-2404' || 'ubuntu-latest' }}"
const mainVenueBlacksmithWindows4vcpuRunsOn = "${{ matrix.venue == 'blacksmith' && 'blacksmith-4vcpu-windows-2025' || 'windows-latest' }}"
const mainBlacksmithGoBuildCacheRunsOn = "${{ 'blacksmith-4vcpu-ubuntu-2404' }}"

// pr.yml's call of bazel.yml: exactly these inputs (review D1 v2 N3). An rbe
// override would put every PR in local mode and ungate the embedded tier
// while the gate stays self-consistent; integration: "off" would drop the
// required integration lane (explicit "on", so the pin and the gate
// simulation, which reads these inputs, do not depend on the default).
var bazelPRCallWith = map[string]string{
	"build-artifact-name": "bazel-ci-build-artifacts",
	"integration":         "on",
	// F3: only pr.yml opts in; bazel-farm.yml and nightly.yml keep the
	// default "off" (TestBazelGateSimulation's other callers).
	"package-gates": "on",
}

// The call's aggregate result (needs.bazel.result, through bazel-gate.sh).
const bazelAggregateGateID = "BAZEL"

// The gate script and the rbe job's execution modes.
const bazelGateScript = ".github/scripts/bazel-gate.sh"

var bazelRBEModes = []string{"remote", "fork-ro", "fork-rw", "cache", "local", "skip"}

// bazelRemoteModes: the modes whose lanes execute on rbe-west (the rbe job's
// enabled output is "true" in exactly these): remote with the CI secrets,
// fork-ro and fork-rw with an rbe-fork-mint certificate.
var bazelRemoteModes = map[string]bool{"remote": true, "fork-ro": true, "fork-rw": true}

// bazelModeEnabled: the rbe job's enabled output for a mode.
func bazelModeEnabled(mode string) string { return strconv.FormatBool(bazelRemoteModes[mode]) }

// The four RBE secrets plus the rbe-prewarm job's "bazel-allocator" GitHub
// App id and private key (it mints its own gastownhall/gascity installation
// token from these, inline in the job's own dispatch step - see bazel.yml's
// rbe-prewarm job), the full set bazel.yml declares under workflow_call and
// the only secrets pr.yml or nightly.yml may hand it
// (TestBazelLaneIsGatedAlongsideLegacy below).
var bazelCallSecrets = map[string]string{
	"RBE_WEST_EXECUTOR":        "${{ secrets.RBE_WEST_EXECUTOR }}",
	"RBE_TLS_CERT":             "${{ secrets.RBE_TLS_CERT }}",
	"RBE_TLS_KEY":              "${{ secrets.RBE_TLS_KEY }}",
	"RBE_TLS_CA":               "${{ secrets.RBE_TLS_CA }}",
	"RBE_POOL_APP_ID":          "${{ secrets.RBE_POOL_APP_ID }}",
	"RBE_POOL_APP_PRIVATE_KEY": "${{ secrets.RBE_POOL_APP_PRIVATE_KEY }}",
}

// bazel-farm.yml's pull_request_target run must never carry the
// gastownhall/gascity app credential (B1, security review of bdef342d5): an
// allowlisted fork author's own PR code runs under that trust tier, and the
// app secrets mint a token with write access to another organization's
// repository. Only pr.yml and nightly.yml (same-repo/trusted triggers only)
// pass the two RBE_POOL_APP_* secrets; bazel-farm.yml passes exactly these
// four.
var bazelFarmCallSecrets = map[string]string{
	"RBE_WEST_EXECUTOR": "${{ secrets.RBE_WEST_EXECUTOR }}",
	"RBE_TLS_CERT":      "${{ secrets.RBE_TLS_CERT }}",
	"RBE_TLS_KEY":       "${{ secrets.RBE_TLS_KEY }}",
	"RBE_TLS_CA":        "${{ secrets.RBE_TLS_CA }}",
}

type ciCompositeAction struct {
	Runs struct {
		Using string           `yaml:"using"`
		Steps []ciWorkflowStep `yaml:"steps"`
	} `yaml:"runs"`
}

func readSetupBazelAction(t *testing.T) ciCompositeAction {
	t.Helper()
	path := filepath.Join(sourceRepoRoot(t), setupBazelActionDir, "action.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var action ciCompositeAction
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if action.Runs.Using != "composite" {
		t.Fatalf("%s runs.using = %q, want composite", path, action.Runs.Using)
	}
	return action
}

// bazel.yml's jobs, their runner, rbe-west gate and skip rules, and the
// setup-bazel env. The skip rules are also what pr.yml's ci-gate accepts as a
// skip (TestBazelGateSimulation).
func TestBazelWorkflowJobsAndExecutionMode(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	var names []string
	for name, job := range workflow.Jobs {
		names = append(names, name)
		// rbe-prewarm is the one deliberate exception: it is advisory (never
		// a `needs` of any lane, never in ci-gate's required list - see
		// bazelAdvisoryLanes and TestBazelLaneIsGatedAlongsideLegacy), and a
		// called reusable workflow's overall conclusion is "failure" if ANY
		// job within it fails regardless of `needs`, so without job-level
		// continue-on-error here a pre-warm hiccup would still turn
		// bazel.yml's own run - and thus pr.yml's ci-gate - red for a job
		// nothing requires.
		if job.ContinueOnError && name != bazelRBEPrewarmJobName {
			t.Errorf("%s continue-on-error hides failures from pr.yml's ci-gate", name)
		}
		if job.TimeoutMinutes == 0 {
			t.Errorf("%s has no timeout-minutes", name)
		}
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, bazelJobNames) {
		t.Errorf("%s jobs = %v, want %v", bazelWorkflowName, names, bazelJobNames)
	}
	// Every lane needs the rbe job and takes its runner, skip rule and
	// setup-bazel env from that job's outputs alone. Remote runs use the
	// Blacksmith pool; fork modes (fork-ro/fork-rw: remote execution with an
	// rbe-fork-mint certificate, the runner only a client), local and cache
	// runs (forks while rbe-fork is closed, rbe=off/cache) the GitHub-hosted
	// runner. Only mode remote gets the secrets; fork modes get
	// BAZEL_FORK_REMOTE (setup-bazel mints the certificate), cache runs
	// BAZEL_FORK_CACHE.
	const wantRunsOn = "${{ needs.rbe.outputs.mode == 'remote' && 'blacksmith-2vcpu-ubuntu-2404' || 'ubuntu-latest' }}"
	// Local-capable lanes skip only in mode skip (same-repo, RBE_WEST_WORKERS
	// unset); remote-only lanes (27 race processes and more, an hour or more
	// on a GitHub-hosted runner, for tests the Go jobs already run there)
	// skip unless remote.
	const wantIf = "${{ needs.rbe.outputs.mode != 'skip' }}"
	const wantRemoteOnlyIf = "${{ needs.rbe.outputs.enabled == 'true' }}"
	gate := "needs.rbe.outputs.mode == 'remote' && "
	// The package gates' setup-bazel (mode remote only, see
	// bazelPackageRunsOn): the secrets, never a fork certificate.
	wantPackageSetupEnv := map[string]string{
		"BAZEL_REMOTE_EXECUTOR": "${{ " + gate + "secrets.RBE_WEST_EXECUTOR || '' }}",
		"RBE_TLS_CERT":          "${{ " + gate + "secrets.RBE_TLS_CERT || '' }}",
		"RBE_TLS_KEY":           "${{ " + gate + "secrets.RBE_TLS_KEY || '' }}",
		"RBE_TLS_CA":            "${{ " + gate + "secrets.RBE_TLS_CA || '' }}",
		"RBE_INSTANCE":          "${{ " + gate + "'oss' || '' }}",
		"BAZEL_FORK_CACHE":      "${{ needs.rbe.outputs.mode == 'cache' && 'true' || '' }}",
	}
	wantSetupEnv := copyMap(wantPackageSetupEnv)
	wantSetupEnv["BAZEL_FORK_REMOTE"] = bazelForkRemoteValue
	wantSetupEnv["RBE_FORK_TIER"] = "${{ needs.rbe.outputs.tier }}"
	wantSetupEnv["RBE_FORK_PR"] = "${{ github.event.pull_request.number }}"
	// F3: the rbe job itself runs on Blacksmith for same-repo PRs (and
	// merge_group/push/dispatch/schedule, which are never forks); a
	// pull_request_target farm run and any fork or Dependabot PR stay
	// GitHub-hosted. Pinned again, verbatim, by TestSameRepoBlacksmithRunners.
	if rbe, ok := workflow.Jobs[bazelRBEJobName]; !ok {
		t.Fatalf("%s has no %s job", bazelWorkflowName, bazelRBEJobName)
	} else if rbe.RunsOn != wantRBERunsOn {
		t.Errorf("%s runs-on = %q, want %q", bazelRBEJobName, rbe.RunsOn, wantRBERunsOn)
	}
	for name, job := range workflow.Jobs {
		if name == bazelRBEJobName {
			continue
		}
		if !reflect.DeepEqual([]string(job.Needs), []string{bazelRBEJobName}) {
			t.Errorf("%s needs = %v, want [%s]", name, job.Needs, bazelRBEJobName)
		}
		// F3: the package gates use a 4 vCPU runner (pytest-xdist -n 8) and
		// their own if (the caller's package-gates input, not the rbe job's
		// mode - they never skip for execution-mode reasons).
		wantJobRunsOn, wantJobIf, wantJobSetupEnv := wantRunsOn, wantIf, wantSetupEnv
		if bazelPackageJobs[name] {
			wantJobRunsOn, wantJobIf, wantJobSetupEnv = bazelPackageRunsOn, bazelPackageGatesIf, wantPackageSetupEnv
		} else if bazelRemoteOnlyJobs[name] {
			wantJobIf = wantRemoteOnlyIf
		} else if name == bazelIntegJobName {
			wantJobIf = bazelIntegIf
		} else if name == bazelRBEPrewarmJobName {
			// Same runs-on ternary as every other lane (wantRunsOn, set
			// above); only the if differs. Not a lane: never uses
			// setup-bazel, so wantJobSetupEnv is moot (the per-step check
			// below only fires for a setup-bazel step).
			wantJobIf = bazelRBEPrewarmIf
		}
		if job.RunsOn != wantJobRunsOn {
			t.Errorf("%s runs-on = %q, want %q", name, job.RunsOn, wantJobRunsOn)
		}
		if job.If != wantJobIf {
			t.Errorf("%s if = %q, want %q", name, job.If, wantJobIf)
		}
		for _, step := range job.Steps {
			if step.Uses == "./"+setupBazelActionDir && !reflect.DeepEqual(step.Env, wantJobSetupEnv) {
				t.Errorf("%s setup-bazel env = %v, want %v", name, step.Env, wantJobSetupEnv)
			}
		}
	}
}

// F3: bazelPackageRunsOn's comment and the package-mcp job's 4 vCPU runner
// are both premised on mcp_pytest() in package-mcp.sh passing an explicit
// "-n 8" (not "-n auto", which would reintroduce the CPU-count/cgroup-quota
// mismatch -n 8 was pinned to avoid), and on pytest-xdist itself being a
// locked dev dependency so that worker count is reproducible across CI runs.
// Neither half of that premise is checked by any other policy test, so pin
// both here directly against the script and the MCP package's manifest/lock.
func TestMCPPytestWorkerCountPinned(t *testing.T) {
	root := sourceRepoRoot(t)
	script := readPolicyFile(t, root, "scripts/ci/package-mcp.sh")
	if !strings.Contains(script, "pytest -n 8") {
		t.Errorf("scripts/ci/package-mcp.sh mcp_pytest() does not pass an explicit -n 8")
	}
	if strings.Contains(script, "-n auto") {
		t.Errorf("scripts/ci/package-mcp.sh mcp_pytest() uses -n auto; want the pinned -n 8")
	}
	pyproject := readPolicyFile(t, root, "integrations/beads-mcp/pyproject.toml")
	if !regexp.MustCompile(`(?m)^\s*"pytest-xdist[><=]`).MatchString(pyproject) {
		t.Errorf("integrations/beads-mcp/pyproject.toml does not pin pytest-xdist as a dev dependency")
	}
	lock := readPolicyFile(t, root, "integrations/beads-mcp/uv.lock")
	if !strings.Contains(lock, "name = \"pytest-xdist\"") {
		t.Errorf("integrations/beads-mcp/uv.lock has no locked pytest-xdist entry; run uv lock")
	}
}

// F3: package-mcp and package-npm's job definitions (including the very
// detect step this script's output feeds) moved into bazel.yml, so a PR that
// edits only bazel.yml's package-gate logic must still self-trigger both
// gates the same way editing pr.yml/main.yml/pr-risk.yml does; otherwise a
// change to the job that runs the gates could ship without the gates having
// run against it. No other policy test reads this script's content.
func TestDetectPackageGatesCoversBazelWorkflow(t *testing.T) {
	root := sourceRepoRoot(t)
	script := readPolicyFile(t, root, "scripts/ci/detect-package-gates.sh")
	re := regexp.MustCompile(`(?s)scripts/ci/detect-package-gates\.sh\|[^)]*\)\s*\n\s*mcp_package=true\s*\n\s*npm_package=true`)
	loc := re.FindString(script)
	if loc == "" {
		t.Fatalf("scripts/ci/detect-package-gates.sh has no case arm forcing both mcp_package and npm_package true alongside its own path")
	}
	if !strings.Contains(loc, ".github/workflows/bazel.yml") {
		t.Errorf("scripts/ci/detect-package-gates.sh's both-gates case arm does not include .github/workflows/bazel.yml, where the package-mcp/package-npm job definitions now live")
	}
	// F3 review NIT-6: package-bazel-bd.sh packages the Bazel-built bd for
	// both package-mcp and package-npm on the remote-execution path, so a
	// change to it must run both gates too.
	if !strings.Contains(loc, "scripts/ci/package-bazel-bd.sh") {
		t.Errorf("scripts/ci/detect-package-gates.sh's both-gates case arm does not include scripts/ci/package-bazel-bd.sh, which both package gates use to package the Bazel-built bd")
	}
}

// F3: the exact "same-repo PR, or merge_group" Blacksmith expression, pinned
// once per job so a future edit cannot drift one copy from another. Reads no
// repo variable, so the rollback is a plain revert. The rbe job's own
// version additionally covers push, workflow_dispatch and schedule (never
// forks); every other job below reads only merge_group and pull_request.
func TestSameRepoBlacksmithRunners(t *testing.T) {
	want := map[string]map[string]string{
		// F7a: cache-free same-repo Linux jobs that restore no GitHub-saved
		// build cache moved onto same-repo Blacksmith at their measured vCPU
		// size (module-cache misses on the Blacksmith pool are acceptable
		// per spec; jobs that need a Blacksmith-side build-cache saver are
		// F7b's scope, not this one). windows-test-binaries is F4's Linux
		// mingw cross-compile job, sized at 8vcpu like the cross-compilation
		// matrix fold below.
		"pr.yml": {
			"bazel-coverage": sameRepoBlacksmith2vcpu, "ci-gate": sameRepoBlacksmith2vcpu,
			"fast-checks":                            sameRepoBlacksmith2vcpu,
			"advisory-reports":                       sameRepoBlacksmith2vcpu,
			"test-dolt-server-fingerprint":           sameRepoBlacksmith2vcpu,
			"check-doc-flags":                        sameRepoBlacksmith4vcpu,
			"pr-policy-wrapper":                      sameRepoBlacksmith4vcpu,
			"check-release-target-cross-compilation": sameRepoBlacksmith8vcpu,
			"windows-test-binaries":                  sameRepoBlacksmith8vcpu,
			// F7b: scripts-go-checks/pr-lint-wrapper moved to same-repo
			// Blacksmith now that main.yml's blacksmith-go-build-cache and
			// pr-lint-wrapper `venue` matrix seed their Blacksmith-selection
			// GOCACHEs.
			"scripts-go-checks": sameRepoBlacksmith4vcpu,
			"pr-lint-wrapper":   sameRepoBlacksmith4vcpu,
			// F7b: same-repo Blacksmith Windows (blacksmith-*vcpu-windows-2025,
			// public beta). test-windows-liveness and worktree-remove-windows are
			// sized at 4vcpu (a build plus a focused test run each);
			// test-windows-small and the two "-prebuilt" twins (which only run an
			// already-cross-compiled binary) are sized at 2vcpu; windows-make-shell
			// is sized at 4vcpu (installs three separate Make toolchains). See the
			// report's risk notes for test-windows-liveness (cgo/mingw on
			// windows-2025, unverified) and windows-make-shell (Chocolatey
			// preinstalled, unverified).
			"test-windows-liveness":            sameRepoBlacksmithWindows4vcpu,
			"worktree-remove-windows":          sameRepoBlacksmithWindows4vcpu,
			"test-windows-small":               sameRepoBlacksmithWindows2vcpu,
			"test-windows-liveness-prebuilt":   sameRepoBlacksmithWindows2vcpu,
			"worktree-remove-windows-prebuilt": sameRepoBlacksmithWindows2vcpu,
			"windows-make-shell":               sameRepoBlacksmithWindows4vcpu,
			// F7b: mixed-OS matrix jobs using the "matrix marker" chained-ternary
			// form (spec §2.1) instead of a flat same-repo ternary - see
			// sameRepoPlatformsMatrixMarkerRunsOn's doc comment in
			// scripts/ci_blacksmith_runner_test.go. (F7b review fix S1: an
			// earlier revision additionally split pr-preflight-platforms' Windows
			// leg into two `shard` matrix entries; that split was reverted as not
			// measurably helpful, so this single map entry now covers exactly
			// three matrix legs, one per OS.)
			"check-doc-freshness-platforms": sameRepoPlatformsMatrixMarkerRunsOn,
			"pr-preflight-platforms":        sameRepoPlatformsMatrixMarkerRunsOn,
		},
		"pr-risk.yml": {
			"bazel-coverage": sameRepoBlacksmith2vcpu, "ci-gate": sameRepoBlacksmith2vcpu, "detect-ci-tier": sameRepoBlacksmith2vcpu,
			"test-nix": sameRepoBlacksmith4vcpu,
		},
		bazelWorkflowName: {bazelRBEJobName: wantRBERunsOn},
		// F7c: advisory workflows. Each compiles Go (or, for docsync/
		// broken-links, is cheap enough to size at 2 vCPU per spec-f7.md
		// §2.2) and moves to Blacksmith for same-repo PRs/merge_group only;
		// forks, Dependabot and push stay on ubuntu-latest.
		"conformance.yml":         {"conformance": sameRepoBlacksmith4vcpu},
		"regression.yml":          {"regression": sameRepoBlacksmith4vcpu},
		"migration-test.yml":      {"historical-upgrades": sameRepoBlacksmith4vcpuNoble},
		"cross-version-smoke.yml": {"smoke": sameRepoBlacksmith4vcpu, "versions": sameRepoBlacksmith2vcpu},
		"docs-mintlify.yml":       {"docsync": sameRepoBlacksmith2vcpu, "broken-links": sameRepoBlacksmith2vcpu},
		"proxied-local-smoke.yml": {"managed-local-smoke": sameRepoBlacksmith4vcpu},
		// main.yml's seeder job (B2, F7c implementation report) is the one
		// Blacksmith job that is NOT gated by the same-repo-PR expression: it
		// is push-to-main only (always trusted), so it wraps the literal
		// label in a trivial expression instead (see the job's own comment).
		// Tracked here so the "no other Blacksmith label leaks" sweep below
		// also covers main.yml.
		"main.yml": {
			"blacksmith-setup-go-cache":   "${{ 'blacksmith-4vcpu-ubuntu-2404' }}",
			"windows-test-binaries-cache": mainWindowsTestBinariesCacheRunsOn,
			// F7b review (B2): these four push-to-main-only jobs seed the
			// Blacksmith-selection GOCACHEs same-repo PR/merge_group jobs
			// above restore from (pr-lint-wrapper/go-vet-cache/test-windows's
			// "venue" matrix, blacksmith-go-build-cache's own single leg).
			// Each is additionally guarded by a job-level
			// github.event_name == 'push' && github.ref ==
			// 'refs/heads/main' && github.repository == 'gastownhall/beads'
			// if: (TestBlacksmithSaverJobsGuardedAgainstPullRequest below),
			// matching F7c's own saver convention.
			"pr-lint-wrapper":           mainVenueBlacksmithUbuntu4vcpuRunsOn,
			"go-vet-cache":              mainVenueBlacksmithUbuntu4vcpuRunsOn,
			"test-windows":              mainVenueBlacksmithWindows4vcpuRunsOn,
			"blacksmith-go-build-cache": mainBlacksmithGoBuildCacheRunsOn,
		},
	}
	// The two required gates' display names are a stable external contract
	// (branch protection rule names) - moving them to Blacksmith must not
	// rename them. Reviewed 2026-10-03: a rename of either survived every
	// other policy test, since nothing else reads job.Name.
	wantGateName := map[string]map[string]string{
		"pr.yml":      {"ci-gate": "CI Gate / Required"},
		"pr-risk.yml": {"ci-gate": "PR Risk Gate / Required"},
	}
	for file, jobs := range want {
		workflow := readCIWorkflow(t, file)
		for name, wantRunsOn := range jobs {
			job, ok := workflow.Jobs[name]
			if !ok {
				t.Fatalf("%s has no %s job", file, name)
			}
			if job.RunsOn != wantRunsOn {
				t.Errorf("%s %s runs-on = %q, want %q", file, name, job.RunsOn, wantRunsOn)
			}
			if wantName := wantGateName[file][name]; wantName != "" && job.Name != wantName {
				t.Errorf("%s %s name = %q, want %q", file, name, job.Name, wantName)
			}
			// F3: every job moved onto Blacksmith needs its own
			// timeout-minutes - a hang there burns paid Blacksmith minutes,
			// not just a free GitHub-hosted runner's default 360.
			if job.TimeoutMinutes == 0 {
				t.Errorf("%s %s has no timeout-minutes (now runs on Blacksmith for same-repo PRs)", file, name)
			}
		}
	}
	// No other job in pr.yml or pr-risk.yml may name a Blacksmith label beyond
	// the ones enumerated in `want` above (F4 added windows-test-binaries).
	// Same sweep for the F7c advisory workflows: nothing in them may name a
	// Blacksmith label beyond the jobs listed in `want` above.
	for _, file := range []string{
		"pr.yml", "pr-risk.yml", "main.yml",
		"conformance.yml", "regression.yml", "migration-test.yml",
		"cross-version-smoke.yml", "docs-mintlify.yml", "proxied-local-smoke.yml",
	} {
		workflow := readCIWorkflow(t, file)
		allowed := want[file]
		for name, job := range workflow.Jobs {
			if allowed[name] != "" {
				continue
			}
			if strings.Contains(job.RunsOn, "blacksmith-") {
				t.Errorf("%s job %s runs-on %q names a Blacksmith label; only %v may", file, name, job.RunsOn, allowed)
			}
		}
	}
}

// TestBlacksmithJobsReadNoSecrets: F7a's rule that a job whose runs-on can
// select a Blacksmith label never reads a secret, in any form (job env, job
// if:, step env, step with:, step if:, or a step's run: body interpolating
// ${{ secrets.X }}). Blacksmith's ternary already falls back to ubuntu-latest
// for forks and Dependabot, but the no-secrets rule is a second, independent
// backstop: even a same-repo Blacksmith run should never need repository
// secrets for a cache-free, read-only check.
//
// Review N-4 (2026-10-03): this also now scans job.If and step.If, since a
// conditional guarded on a secret (e.g. an if: expression testing
// secrets.FOO) would otherwise slip past the env/with/run checks below. It
// does not yet inspect
// container/services credentials or resolve matrix markers in runs-on
// (neither ciWorkflowJob field exists today); that is deferred to F7b, which
// is where container/services and matrix-aware runs-on resolution land.
func TestBlacksmithJobsReadNoSecrets(t *testing.T) {
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	for _, file := range []string{"pr.yml", "pr-risk.yml"} {
		workflow := readCIWorkflow(t, file)
		for name, job := range workflow.Jobs {
			if !strings.Contains(job.RunsOn, "blacksmith-") {
				continue
			}
			if secretRef.MatchString(job.If) {
				t.Errorf("%s job %s if %q reads a secret; Blacksmith-eligible jobs must not", file, name, job.If)
			}
			for k, v := range job.Env {
				if secretRef.MatchString(v) {
					t.Errorf("%s job %s env %s = %q reads a secret; Blacksmith-eligible jobs must not", file, name, k, v)
				}
			}
			for _, step := range job.Steps {
				if secretRef.MatchString(step.If) {
					t.Errorf("%s job %s step %q if %q reads a secret", file, name, step.Name, step.If)
				}
				for k, v := range step.Env {
					if secretRef.MatchString(v) {
						t.Errorf("%s job %s step %q env %s = %q reads a secret", file, name, step.Name, k, v)
					}
				}
				for k, v := range step.With {
					if secretRef.MatchString(v) {
						t.Errorf("%s job %s step %q with %s = %q reads a secret", file, name, step.Name, k, v)
					}
				}
				if secretRef.MatchString(step.Run) {
					t.Errorf("%s job %s step %q run reads a secret:\n%s", file, name, step.Name, step.Run)
				}
			}
		}
	}
}

// TestSameRepoBlacksmithExpressionSemantics now lives in
// ci_blacksmith_runner_test.go, which evaluates the real
// sameRepoBlacksmith{2,4,8}vcpu expression strings with a small GitHub
// Actions expression evaluator instead of a hand-written Go mirror
// (review SF-3). That file is shared with F7c.

// TestPRCIGateFastChecksTokens pins pr.yml's F7a fold of five formerly
// standalone jobs (check-build-tags, check-version-consistency,
// check-migration-hygiene, check-no-beads-changes, fmt-check) into
// fast-checks' five steps: each step's id, its outputs entry, and the
// CI_GATE_REQUIRED token/env mapping that lets ci-gate still red the exact
// same token a failing check reddened before the fold. It also pins the
// FAST_CHECKS backstop token (the job's own .result), which gives ci-gate.sh
// one clear line if the whole job is lost before any step reports.
func TestPRCIGateFastChecksTokens(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	job := pr.job(t, "fast-checks")
	gate := pr.job(t, "ci-gate")
	gateStep := gate.step(t, "Evaluate CI gate")
	required := strings.Fields(gateStep.Env["CI_GATE_REQUIRED"])

	if !contains(gate.Needs, "fast-checks") {
		t.Errorf("ci-gate does not need fast-checks: %v", gate.Needs)
	}
	if !contains(required, "FAST_CHECKS") || gateStep.Env["FAST_CHECKS"] != "${{ needs.fast-checks.result }}" {
		t.Errorf("ci-gate does not require fast-checks' own result as the FAST_CHECKS backstop: required=%v env=%q", required, gateStep.Env["FAST_CHECKS"])
	}

	// stepID -> (job output name, CI_GATE_REQUIRED token).
	mapping := map[string]struct{ output, token string }{
		"build-tags":          {"build-tags", "CHECK_BUILD_TAGS"},
		"version-consistency": {"version-consistency", "CHECK_VERSION_CONSISTENCY"},
		"migration-hygiene":   {"migration-hygiene", "CHECK_MIGRATION_HYGIENE"},
		"no-beads-changes":    {"no-beads-changes", "CHECK_NO_BEADS_CHANGES"},
		"fmt":                 {"fmt", "FMT_CHECK"},
	}
	for stepID, m := range mapping {
		step := job.step(t, map[string]string{
			"build-tags": "Check build-tag policy", "version-consistency": "Check all versions match",
			"migration-hygiene": "Run migration hygiene checks", "no-beads-changes": "Check for .beads/issues.jsonl changes",
			"fmt": "Check gofmt",
		}[stepID])
		wantIf := "${{ !cancelled() }}"
		if stepID == "no-beads-changes" {
			// pull_request-only, same as before the fold: merge_group has no
			// base_ref to diff against. The CI_GATE_SKIPPED_OK allowlist
			// below covers exactly that skip on merge_group runs.
			wantIf = "${{ !cancelled() && github.event_name == 'pull_request' }}"
		}
		if step.ID != stepID || step.If != wantIf {
			t.Errorf("fast-checks step %q: id %q, if %q; want id %q, if %q", step.Name, step.ID, step.If, stepID, wantIf)
		}
		wantOutput := "${{ steps." + stepID + ".outcome }}"
		if job.Outputs[m.output] != wantOutput {
			t.Errorf("fast-checks outputs.%s = %q, want %q", m.output, job.Outputs[m.output], wantOutput)
		}
		if !contains(required, m.token) {
			t.Errorf("ci-gate CI_GATE_REQUIRED does not include %s", m.token)
		}
		wantEnv := "${{ needs.fast-checks.outputs." + m.output + " || 'skipped' }}"
		if gateStep.Env[m.token] != wantEnv {
			t.Errorf("ci-gate env %s = %q, want %q", m.token, gateStep.Env[m.token], wantEnv)
		}
	}
	// CHECK_NO_BEADS_CHANGES's step only runs on pull_request; the gate's own
	// run: script must still allow-list exactly that skip on merge_group,
	// unchanged by the fold.
	if !strings.Contains(gateStep.Run, `skipped_ok="CHECK_NO_BEADS_CHANGES"`) {
		t.Error("ci-gate run script no longer allow-lists CHECK_NO_BEADS_CHANGES's merge_group skip")
	}

	// Review N-6 (2026-10-03): pin the folded steps' actual commands/env, not
	// just their id/if/output/token wiring. These three gaps predate F7a (the
	// standalone jobs were never pinned either), but fast-checks is now one
	// job, so the cost of covering them here is marginal.
	buildTags := job.step(t, "Check build-tag policy")
	for _, want := range []string{
		"./scripts/check-build-tags.sh",
		"./scripts/check-go-install-guidance.sh",
		"./scripts/check-winget-portable-alias.sh",
	} {
		if !strings.Contains(buildTags.Run, want) {
			t.Errorf("fast-checks build-tags step run does not call %s:\n%s", want, buildTags.Run)
		}
	}
	migrationHygiene := job.step(t, "Run migration hygiene checks")
	const wantBaseSHA = "${{ github.event.pull_request.base.sha || github.event.merge_group.base_sha }}"
	if migrationHygiene.Env["BASE_SHA"] != wantBaseSHA {
		t.Errorf("fast-checks migration-hygiene step env BASE_SHA = %q, want %q", migrationHygiene.Env["BASE_SHA"], wantBaseSHA)
	}
}

// Review N-6 (2026-10-03): test-windows-small's dbproxy-server leg must keep
// building CGO-disabled, same as before the fold.
func TestPRWindowsSmallDbproxyServerIsCGODisabled(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	job := pr.job(t, "test-windows-small")
	step := job.step(t, "Run dbproxy server package tests on Windows")
	if step.Env["CGO_ENABLED"] != "0" {
		t.Errorf("test-windows-small dbproxy-server step env CGO_ENABLED = %q, want \"0\"", step.Env["CGO_ENABLED"])
	}
}

// stepContinueOnError reports whether a step's continue-on-error is truthy
// (the field unmarshals as `any` because the YAML schema also allows an
// expression string, which GitHub itself treats as whatever it evaluates to
// - this test only cares about the literal `true` every current step uses).
func stepContinueOnError(step ciWorkflowStep) bool {
	v, ok := step.ContinueOnError.(bool)
	return ok && v
}

// TestPRWindowsMakeShellLegsFailTheJob is review SF-1 (2026-10-03):
// windows-make-shell is a required gate (WINDOWS_MAKE_SHELL). Before this
// fix, every leg's install/exercise step carried continue-on-error and a
// hand-written final step reconstructed the job's pass/fail from each step's
// .outcome - two mutations (an aggregator that always exits 0, and one that
// silently dropped the MSYS2 install check) both passed the suite, because
// nothing pinned the aggregator's own correctness. The fix removes the
// aggregator entirely: every step after checkout keeps `if: !cancelled()`
// (so a failing leg does not stop later legs from attempting to run), but
// only the Cygwin *setup* step (a flaky installer download, not a code
// failure) may carry continue-on-error. Every other step's failure now fails
// the job directly, with no hand-written logic in between to go stale.
func TestPRWindowsMakeShellLegsFailTheJob(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	// Only the install/exercise "leg" steps need `if: !cancelled()` (so a
	// failing leg does not stop a later, independent leg from attempting to
	// run) and the no-continue-on-error rule; checkout/setup steps that every
	// leg depends on are deliberately left to run unconditionally.
	legSteps := map[string][]string{
		"windows-make-shell": {
			"Install native GNU Make", "Set up MSYS2 GNU Make", "Set up Cygwin GNU Make",
			"Warn when Cygwin setup was skipped by a download failure",
			"Exercise native Windows Make", "Exercise MSYS2-hosted Make", "Exercise Cygwin-hosted Make",
		},
		"test-windows-small": {
			"Run doltversion package tests on Windows", "Run dbproxy server package tests on Windows",
		},
	}
	for jobName, names := range legSteps {
		job := pr.job(t, jobName)
		for _, name := range names {
			step := job.step(t, name)
			if !strings.Contains(step.If, "!cancelled()") {
				t.Errorf("%s step %q if = %q, want it to contain !cancelled() so later legs still run after an earlier one fails", jobName, step.Name, step.If)
			}
			allowCOE := jobName == "windows-make-shell" && step.ID == "cygwin"
			if stepContinueOnError(step) && !allowCOE {
				t.Errorf("%s step %q (id %q) has continue-on-error; only windows-make-shell's Cygwin setup step may, so every other leg's failure reds the job directly", jobName, step.Name, step.ID)
			}
			if allowCOE && !stepContinueOnError(step) {
				t.Errorf("%s Cygwin setup step lost continue-on-error; a flaky installer download would now fail the job", jobName)
			}
		}
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Name, "Fail if") {
				t.Errorf("%s still has a hand-written aggregator step %q; SF-1 replaced aggregators with per-step failure propagation", jobName, step.Name)
			}
			if step.ID != "cygwin" && stepContinueOnError(step) {
				t.Errorf("%s step %q (id %q) has continue-on-error; only windows-make-shell's Cygwin setup step may", jobName, step.Name, step.ID)
			}
		}
	}
	// The aggregator's job outputs (native/msys2/cygwin) are gone too
	// (review N-3): nothing reads them once ci-gate only needs the job's own
	// .result.
	windowsMakeShell := pr.job(t, "windows-make-shell")
	if len(windowsMakeShell.Outputs) != 0 {
		t.Errorf("windows-make-shell outputs = %v, want none (N-3: they were unused)", windowsMakeShell.Outputs)
	}
}

// TestPRWindowsMakeShellSetupStepsHaveTimeouts is review SF-2 (2026-10-03):
// the Cygwin and MSYS2 setup steps download third-party installers from the
// network; a step timeout bounds how long a hang can block the later legs'
// exercise steps behind it, instead of consuming the whole 30-minute job
// timeout (which would also lose native/msys2 coverage, since their exercise
// steps would never get a chance to run before the job-level cancel).
func TestPRWindowsMakeShellSetupStepsHaveTimeouts(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	job := pr.job(t, "windows-make-shell")
	for _, name := range []string{"Set up MSYS2 GNU Make", "Set up Cygwin GNU Make"} {
		step := job.step(t, name)
		if step.TimeoutMinutes <= 0 {
			t.Errorf("windows-make-shell step %q has no timeout-minutes", name)
		}
	}
}

type bazelWorkflowCall struct {
	Inputs map[string]struct {
		Type    string `yaml:"type"`
		Default string `yaml:"default"`
	} `yaml:"inputs"`
	Secrets map[string]struct {
		Required bool `yaml:"required"`
	} `yaml:"secrets"`
	Outputs map[string]struct {
		Value string `yaml:"value"`
	} `yaml:"outputs"`
}

func readBazelWorkflowCall(t *testing.T) bazelWorkflowCall {
	t.Helper()
	path := filepath.Join(sourceRepoRoot(t), ".github", "workflows", bazelWorkflowName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		On struct {
			WorkflowCall bazelWorkflowCall `yaml:"workflow_call"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc.On.WorkflowCall
}

// Slice D1: pr.yml calls bazel.yml once per PR and merge group, and its
// ci-gate requires the Bazel lanes in addition to (not instead of) the legacy
// jobs they mirror, which stay required in pr.yml and pr-risk.yml.
func TestBazelLaneIsGatedAlongsideLegacy(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	call := readBazelWorkflowCall(t)

	// Every job is the rbe job, a gated lane or an advisory lane (review D1
	// F4): a new job cannot join the call without a decision about the gate.
	wantCallOutputs := map[string]string{
		"rbe-enabled": "${{ jobs." + bazelRBEJobName + ".outputs.enabled }}",
		"rbe-mode":    "${{ jobs." + bazelRBEJobName + ".outputs.mode }}",
	}
	for name, job := range workflow.Jobs {
		if name == bazelRBEJobName {
			continue
		}
		_, gated := bazelLaneGateIDs[name]
		_, advisory := bazelAdvisoryLanes[name]
		if gated == advisory {
			t.Errorf("%s job %s: gated=%v advisory=%v; add it to exactly one of bazelLaneGateIDs (with a CI_GATE_REQUIRED id in pr.yml) or bazelAdvisoryLanes (with the reason)",
				bazelWorkflowName, name, gated, advisory)
		}
		// Each lane reports its job.status from an always() last step.
		if !reflect.DeepEqual(job.Outputs, map[string]string{"result": "${{ steps.result.outputs.result }}"}) {
			t.Errorf("%s outputs = %v, want only result from the result step", name, job.Outputs)
		}
		last := job.Steps[len(job.Steps)-1]
		if last.ID != "result" || last.If != "${{ always() }}" || !reflect.DeepEqual(last.Env, map[string]string{"JOB_STATUS": "${{ job.status }}"}) ||
			last.Run != `echo "result=$JOB_STATUS" >> "$GITHUB_OUTPUT"` {
			t.Errorf("%s last step = %+v; want the always() job.status recorder", name, last)
		}
		wantCallOutputs[name] = "${{ jobs." + name + ".outputs.result }}"
	}
	for name := range bazelLaneGateIDs {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("bazelLaneGateIDs lists %s, which %s does not have", name, bazelWorkflowName)
		}
	}
	for name := range bazelAdvisoryLanes {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("bazelAdvisoryLanes lists %s, which %s does not have", name, bazelWorkflowName)
		}
	}
	gotCallOutputs := map[string]string{}
	for name, out := range call.Outputs {
		gotCallOutputs[name] = out.Value
	}
	if !reflect.DeepEqual(gotCallOutputs, wantCallOutputs) {
		t.Errorf("workflow_call outputs = %v, want %v", gotCallOutputs, wantCallOutputs)
	}

	// One caller on PR events: pr.yml's bazel job, with the four RBE secrets
	// only, read-only contents, and no if (the rbe job decides).
	pr := readCIWorkflow(t, "pr.yml")
	bazel := pr.job(t, "bazel")
	if bazel.Uses != "./.github/workflows/"+bazelWorkflowName || bazel.If != "" || len(bazel.Needs) != 0 {
		t.Errorf("pr.yml bazel job uses=%q if=%q needs=%v; want an unconditional call of %s", bazel.Uses, bazel.If, bazel.Needs, bazelWorkflowName)
	}
	if !reflect.DeepEqual(bazel.Permissions, map[string]any{"contents": "read"}) {
		t.Errorf("pr.yml bazel job permissions = %v, want contents: read", bazel.Permissions)
	}
	if !reflect.DeepEqual(bazel.With, bazelPRCallWith) {
		t.Errorf("pr.yml bazel job with = %v, want exactly %v (no rbe or other override)", bazel.With, bazelPRCallWith)
	}
	if in := call.Inputs["integration"]; in.Type != "string" || in.Default != "on" {
		t.Errorf("workflow_call input integration = %+v, want type string, default on (a boolean reads null as false on push)", in)
	}
	var declared []string
	for name := range call.Secrets {
		declared = append(declared, name)
		if call.Secrets[name].Required {
			t.Errorf("workflow_call secret %s is required; fork and Dependabot runs have none", name)
		}
	}
	sort.Strings(declared)
	var wantSecrets []string
	for name := range bazelCallSecrets {
		wantSecrets = append(wantSecrets, name)
	}
	sort.Strings(wantSecrets)
	if !reflect.DeepEqual(declared, wantSecrets) {
		t.Errorf("workflow_call secrets = %v, want %v", declared, wantSecrets)
	}
	callers := 0
	entries, err := os.ReadDir(filepath.Join(sourceRepoRoot(t), ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yml") || entry.Name() == bazelWorkflowName {
			continue
		}
		for jobName, job := range readCIWorkflow(t, entry.Name()).Jobs {
			if !strings.HasSuffix(job.Uses, "/"+bazelWorkflowName) {
				continue
			}
			callers++
			// A caller forwards the RBE secrets: it must never run in a
			// fork's privileged context (review D1 v2 N5). The one
			// deliberate exception is bazel-farm.yml, whose pull_request_target
			// run is limited to allowlisted fork authors and whose every
			// security property TestBazelFarmWorkflowSecurity pins.
			triggers := yamlMapKeys(readYAMLNode(t, filepath.Join(".github", "workflows", entry.Name())), "on")
			for _, trigger := range triggers {
				if trigger == "workflow_run" || (trigger == "pull_request_target" && entry.Name() != bazelFarmWorkflowName) {
					t.Errorf("%s calls %s and has trigger %s; a caller may not run with secrets in a fork PR's context", entry.Name(), bazelWorkflowName, trigger)
				}
			}
			if entry.Name() == bazelFarmWorkflowName && !reflect.DeepEqual(triggers, []string{"pull_request_target"}) {
				t.Errorf("%s triggers = %v, want exactly [pull_request_target]", entry.Name(), triggers)
			}
			if entry.Name() != "pr.yml" && entry.Name() != "nightly.yml" && entry.Name() != bazelFarmWorkflowName {
				t.Errorf("%s job %s calls %s; only pr.yml (PRs), nightly.yml and %s (trusted forks) may", entry.Name(), jobName, bazelWorkflowName, bazelFarmWorkflowName)
			}
			got := map[string]string{}
			if m, ok := job.Secrets.(map[string]any); ok {
				for k, v := range m {
					got[k] = fmt.Sprint(v)
				}
			}
			// bazel-farm.yml is the one deliberate exception (B1, security
			// review of bdef342d5): its pull_request_target run must never
			// carry the rbe-prewarm app secrets, so it gets the four-secret
			// map instead of the full six pr.yml and nightly.yml pass.
			want := bazelCallSecrets
			if entry.Name() == bazelFarmWorkflowName {
				want = bazelFarmCallSecrets
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s job %s secrets = %v, want exactly %v (never inherit)", entry.Name(), jobName, job.Secrets, want)
			}
		}
	}
	if callers != 3 {
		t.Errorf("%d jobs call %s, want 3 (pr.yml, nightly.yml, %s)", callers, bazelWorkflowName, bazelFarmWorkflowName)
	}

	// The gate: needs the call and requires exactly BAZEL plus one id per
	// gated lane, each read from the lane's output (a missing output reads
	// as skipped). BAZEL itself comes from bazel-gate.sh, never the env.
	gate := pr.job(t, "ci-gate")
	evaluate := gate.step(t, "Evaluate CI gate")
	required := strings.Fields(evaluate.Env["CI_GATE_REQUIRED"])
	if !contains(gate.Needs, "bazel") {
		t.Errorf("ci-gate needs = %v, want bazel", gate.Needs)
	}
	// Plus D2's decision and retirement checks (TestPRRiskDecisionMatchesBazelMode).
	wantBazelIDs := []string{bazelAggregateGateID, prRiskCoverageGateID}
	for _, r := range retiredTiers {
		wantBazelIDs = append(wantBazelIDs, r.retiredID)
	}
	for lane, id := range bazelLaneGateIDs {
		// F3: package-mcp/package-npm's ids (PACKAGE_MCP, PACKAGE_NPM) do not
		// follow the "BAZEL" naming convention the prefix filter below
		// groups by - they predate bazel.yml and keep their names - so they
		// are checked here but excluded from wantBazelIDs.
		if !bazelPackageJobs[lane] {
			wantBazelIDs = append(wantBazelIDs, id)
		}
		if want := "${{ needs.bazel.outputs." + lane + " || 'skipped' }}"; evaluate.Env[id] != want {
			t.Errorf("ci-gate env %s = %q, want %q", id, evaluate.Env[id], want)
		}
	}
	sort.Strings(wantBazelIDs)
	var gotBazelIDs []string
	for _, id := range required {
		if strings.HasPrefix(id, "BAZEL") {
			gotBazelIDs = append(gotBazelIDs, id)
		}
	}
	sort.Strings(gotBazelIDs)
	if !reflect.DeepEqual(gotBazelIDs, wantBazelIDs) {
		t.Errorf("ci-gate CI_GATE_REQUIRED Bazel ids = %v, want exactly %v", gotBazelIDs, wantBazelIDs)
	}
	if _, ok := evaluate.Env[bazelAggregateGateID]; ok {
		t.Errorf("ci-gate env sets %s; it must come from %s aggregate", bazelAggregateGateID, bazelGateScript)
	}
	for id, value := range evaluate.Env {
		for lane := range bazelAdvisoryLanes {
			if strings.Contains(value, "outputs."+lane) {
				t.Errorf("ci-gate env %s reads advisory lane %s, which is not in the PR call (%s)", id, lane, bazelAdvisoryLanes[lane])
			}
		}
	}
	for key, want := range map[string]string{
		"BAZEL_CALL":        "${{ needs.bazel.result }}",
		"BAZEL_RBE_MODE":    "${{ needs.bazel.outputs.rbe-mode }}",
		"BAZEL_RBE_ENABLED": "${{ needs.bazel.outputs.rbe-enabled }}",
	} {
		if evaluate.Env[key] != want {
			t.Errorf("ci-gate env %s = %q, want %q", key, evaluate.Env[key], want)
		}
	}
	// The gate reads the call's decision; it never re-derives it.
	rederive := regexp.MustCompile(`(?i)RBE_WEST_WORKERS|head\.repo\.fork|HEAD_REPO_FORK|github\.actor|dependabot|secrets\.RBE`)
	for key, value := range evaluate.Env {
		if rederive.MatchString(key + "=" + value) {
			t.Errorf("ci-gate env %s=%q re-derives the Bazel execution mode; read needs.bazel.outputs.rbe-mode", key, value)
		}
	}
	if rederive.MatchString(evaluate.Run) {
		t.Errorf("ci-gate run re-derives the Bazel execution mode:\n%s", evaluate.Run)
	}
	for _, line := range strings.Split(readPolicyFile(t, sourceRepoRoot(t), bazelGateScript), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") && (rederive.MatchString(line) || strings.Contains(line, "GITHUB_EVENT")) {
			t.Errorf("%s re-derives the execution mode: %q", bazelGateScript, line)
		}
	}

	// Legacy jobs stay required: D1 adds, D2 removes. (D2 lets
	// pr-risk.yml's embedded, proxied and server Dolt test jobs, and
	// build-embedded, skip where the gated lanes run remotely, but they stay
	// required ids: TestPRRiskLegacyTiersDeferToBazelLanes.)
	for id, job := range map[string]string{
		"BUILD_ARTIFACTS":            "build-artifacts",
		"PR_CORE_WRAPPER":            "pr-core-wrapper",
		"CHECK_CMD_BD_PUREGEO_TESTS": "check-cmd-bd-puregeo-tests",
		"TEST_DOMAIN_UOW":            "test-domain-uow",
		"CONTRACT_CORPUS":            "contract-corpus",
	} {
		if !contains(required, id) || !contains(gate.Needs, job) {
			t.Errorf("pr.yml ci-gate no longer requires legacy %s (%s)", job, id)
		}
	}
	risk := readCIWorkflow(t, "pr-risk.yml")
	riskGate := risk.job(t, "ci-gate")
	riskRequired := strings.Fields(riskGate.step(t, "Evaluate CI gate").Env["CI_GATE_REQUIRED"])
	for _, id := range []string{"BUILD_EMBEDDED", "TEST_EMBEDDED_STORAGE", "TEST_EMBEDDED_CONFORMANCE", "TEST_EMBEDDED_CMD", "TEST_PROXIED_CMD", "TEST_SERVER_STORAGE", "TEST_SERVER_STORAGE_FULL"} {
		if !contains(riskRequired, id) {
			t.Errorf("pr-risk.yml ci-gate no longer requires legacy %s", id)
		}
	}
	// PR Risk does not run the lane a second time.
	for jobName, job := range risk.Jobs {
		if strings.Contains(job.Uses, "bazel") || contains(job.Needs, "bazel") {
			t.Errorf("pr-risk.yml job %s runs or needs the Bazel lane; pr.yml owns it", jobName)
		}
	}
}

// A called workflow's workflow-level concurrency group is evaluated in the
// caller's context, where github.workflow is the caller's name: a group equal
// to the caller's own deadlocks and GitHub cancels the call (BAZEL cancelled,
// every PR red). So bazel.yml's group never uses github.workflow and, for
// every event its callers run on, differs from each caller's group (review
// D1 v2 N4).
func TestBazelCallConcurrencyDiffersFromCallers(t *testing.T) {
	group := func(file string) string {
		t.Helper()
		var doc struct {
			Concurrency struct {
				Group string `yaml:"group"`
			} `yaml:"concurrency"`
		}
		if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+file)), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Concurrency.Group
	}
	bazelGroup := group(bazelWorkflowName)
	if bazelGroup == "" || strings.Contains(bazelGroup, "github.workflow") {
		t.Fatalf("%s concurrency group = %q; want a fixed prefix, never github.workflow (the caller's name in a call)", bazelWorkflowName, bazelGroup)
	}
	exprRe := regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
	eval := func(g, workflowName, event string) string {
		t.Helper()
		ctx := map[string]string{
			"github.workflow":   workflowName,
			"github.event_name": event,
			"github.ref":        "refs/pull/123/merge",
			"github.event.pull_request.number || github.ref": "123",
			"github.event.pull_request.number":               "123",
		}
		if event != "pull_request" && event != "pull_request_target" {
			ctx["github.ref"] = "refs/heads/gh-readonly-queue/main/pr-123"
			ctx["github.event.pull_request.number || github.ref"] = ctx["github.ref"]
		}
		return exprRe.ReplaceAllStringFunc(g, func(m string) string {
			v, ok := ctx[exprRe.FindStringSubmatch(m)[1]]
			if !ok {
				t.Fatalf("concurrency group %q: cannot evaluate %s", g, m)
			}
			return v
		})
	}
	for _, caller := range []string{"pr.yml", "nightly.yml", bazelFarmWorkflowName} {
		callerGroup := group(caller)
		if callerGroup == "" {
			continue // no workflow-level group, nothing to collide with
		}
		name := yamlScalar(readYAMLNode(t, filepath.Join(".github", "workflows", caller)), "name")
		for _, event := range []string{"pull_request", "pull_request_target", "merge_group", "push", "schedule", "workflow_dispatch"} {
			if a, b := eval(bazelGroup, name, event), eval(callerGroup, name, event); a == b {
				t.Errorf("%s event %s: %s's concurrency group %q equals the caller's; the call would deadlock", caller, event, bazelWorkflowName, a)
			}
		}
	}
}

// bazelLaneRunModes: the rbe modes in which a lane's `if:` runs it, for a
// call with these inputs. Only the forms TestBazelWorkflowJobsAndExecutionMode
// allows are known. GitHub's != on strings is case-insensitive.
func bazelLaneRunModes(t *testing.T, lane, ifExpr string, with map[string]string) map[string]bool {
	t.Helper()
	switch ifExpr {
	case "${{ needs.rbe.outputs.mode != 'skip' }}":
		return map[string]bool{"remote": true, "fork-ro": true, "fork-rw": true, "cache": true, "local": true}
	case "${{ needs.rbe.outputs.enabled == 'true' }}":
		return map[string]bool{"remote": true, "fork-ro": true, "fork-rw": true}
	case bazelIntegIf:
		if strings.EqualFold(with["integration"], "off") {
			return map[string]bool{}
		}
		return map[string]bool{"remote": true, "fork-ro": true, "fork-rw": true, "cache": true}
	case bazelPackageGatesIf:
		// F3: unlike every other lane, these two never skip because of the
		// rbe job's decision - only because the caller turned them off
		// entirely (pr.yml always passes "on").
		if strings.EqualFold(with["package-gates"], "on") {
			return map[string]bool{"remote": true, "fork-ro": true, "fork-rw": true, "cache": true, "local": true, "skip": true}
		}
		return map[string]bool{}
	case bazelRBEPrewarmIf:
		// Mode remote only (TestBazelWorkflowJobsAndExecutionMode's comment
		// on the job: B1 removed fork-rw). Unlike the historical advisory
		// lanes, this one legitimately runs in pr.yml's own call (see
		// bazelAdvisoryLanes' name-specific exception in
		// TestBazelGateSimulation below).
		return map[string]bool{"remote": true}
	}
	t.Fatalf("%s if = %q: teach bazelLaneRunModes which modes run it", lane, ifExpr)
	return nil
}

// bazelGateScenario: what pr.yml's ci-gate sees of one bazel.yml call.
type bazelGateScenario struct {
	name          string
	event         string
	mode, enabled string            // the call's rbe-mode / rbe-enabled outputs
	call          string            // needs.bazel.result
	outputs       map[string]string // the lanes' outputs ("" = not reported)
	// pr.yml's bazel-coverage job (D2): its outputs, per retiredTiers
	// output name ("" = not reported), and its result ("" = success).
	covered  map[string]string
	coverage string
	// Non-Bazel needs' results that differ from what the jobs' if: give
	// (success, or skipped for pr.yml's legacy jobs that bazel-coverage
	// retired, D2 step 3).
	results     map[string]string
	wantPass    bool
	wantMention string // a red gate must name this id
}

// fastChecksLegacyJobNames: pr.yml's fast-checks job (F7a) output id -> the
// standalone job name it replaced. Lets the gate simulation resolve
// needs.fast-checks.outputs.* through the same prLegacyJobResult lookup
// (and sc.results overrides) the pre-fold standalone jobs used.
var fastChecksLegacyJobNames = map[string]string{
	"build-tags":          "check-build-tags",
	"version-consistency": "check-version-consistency",
	"migration-hygiene":   "check-migration-hygiene",
	"no-beads-changes":    "check-no-beads-changes",
	"fmt":                 "fmt-check",
}

// runPRGateStep runs pr.yml's actual "Evaluate CI gate" step (its run block,
// under GitHub's bash flags) with its env evaluated for the scenario: every
// non-Bazel need succeeded; Bazel expressions read the scenario. An env
// expression of any other form fails the test, so the simulation cannot
// silently drift from the workflow.
func runPRGateStep(t *testing.T, step ciWorkflowStep, sc bazelGateScenario) (bool, string) {
	t.Helper()
	expr := regexp.MustCompile(`^\$\{\{ needs\.([A-Za-z0-9_-]+)\.(result|outputs\.([A-Za-z0-9_-]+))( \|\| 'skipped')? \}\}$`)
	env := []string{"PATH=" + os.Getenv("PATH"), "GITHUB_EVENT_NAME=" + sc.event}
	for key, value := range step.Env {
		if !strings.Contains(value, "${{") {
			env = append(env, key+"="+value)
			continue
		}
		m := expr.FindStringSubmatch(value)
		if m == nil {
			t.Fatalf("ci-gate env %s = %q: the gate simulation cannot evaluate it", key, value)
		}
		var got string
		switch {
		case m[1] == prRiskCoverageJobName && m[2] == "result":
			got = sc.coverage
			if got == "" {
				got = "success"
			}
		case m[1] == prRiskCoverageJobName:
			known := false
			for _, r := range retiredTiers {
				known = known || r.output == m[3]
			}
			if !known {
				t.Fatalf("ci-gate env %s = %q: %s has no such tier output", key, value, prRiskCoverageJobName)
			}
			got = sc.covered[m[3]]
		case m[1] == "fast-checks" && m[2] != "result":
			// F7a: fast-checks folds five formerly-standalone jobs into one
			// job's steps; ci-gate reads each step's outcome via the job's
			// outputs instead of a separate job's .result. Resolve through
			// the same prLegacyJobResult the standalone jobs used, keyed by
			// their old job name, so sc.results overrides (and the default
			// "success") work exactly as they did before the fold.
			legacy, ok := fastChecksLegacyJobNames[m[3]]
			if !ok {
				t.Fatalf("ci-gate env %s = %q: fast-checks has no such step output", key, value)
			}
			got = prLegacyJobResult(legacy, sc)
		case m[1] != "bazel" && m[2] == "result":
			got = prLegacyJobResult(m[1], sc)
		case m[1] != "bazel":
			t.Fatalf("ci-gate env %s = %q: the gate simulation cannot evaluate it", key, value)
		case m[2] == "result":
			got = sc.call
		case m[3] == "rbe-mode":
			got = sc.mode
		case m[3] == "rbe-enabled":
			got = sc.enabled
		default:
			got = sc.outputs[m[3]]
		}
		if got == "" && m[4] != "" {
			got = "skipped"
		}
		env = append(env, key+"="+got)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", step.Run)
	cmd.Dir = sourceRepoRoot(t)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

// The gate over every execution mode (review D1 F3): the skip script allows
// exactly the skips the lanes' own `if:`s produce in that mode, and for every
// lane that should run, that lane alone skipped, failed or cancelled turns
// pr.yml's actual gate step red, whatever the aggregate says (in mode remote
// that includes the integration lane, which skips green in mode local); a
// missing or inconsistent mode, or an aggregate failure no lane explains,
// turns it red.
func TestBazelGateSimulation(t *testing.T) {
	requireHostTool(t, "bash")
	root := sourceRepoRoot(t)
	workflow := readCIWorkflow(t, bazelWorkflowName)
	pr := readCIWorkflow(t, "pr.yml")
	step := pr.job(t, "ci-gate").step(t, "Evaluate CI gate")
	callWith := pr.job(t, "bazel").With

	lanes := map[string]map[string]bool{} // lane -> modes it runs in, in pr.yml's call
	for name, job := range workflow.Jobs {
		if name == bazelRBEJobName {
			continue
		}
		lanes[name] = bazelLaneRunModes(t, name, job.If, callWith)
		// Advisory lanes are normally not in the PR call at all; the one
		// exception is rbe-prewarm, which is advisory (never gated, never a
		// `needs`: TestBazelLaneIsGatedAlongsideLegacy) but, unlike every
		// other advisory lane this map has ever held, is deliberately part
		// of every pr.yml call so it can dispatch before the lanes below
		// need capacity - see bazel.yml's own comment on the job.
		if _, advisory := bazelAdvisoryLanes[name]; advisory && name != bazelRBEPrewarmJobName && len(lanes[name]) != 0 {
			t.Errorf("advisory lane %s runs in pr.yml's call in modes %v; turn it off there (%s)", name, lanes[name], bazelAdvisoryLanes[name])
		}
		if _, gated := bazelLaneGateIDs[name]; gated && !lanes[name]["remote"] {
			t.Errorf("gated lane %s does not run in pr.yml's call even in remote mode", name)
		}
	}
	enabledFor := bazelModeEnabled

	runScript := func(mode, enabled, arg string) string {
		t.Helper()
		cmd := exec.Command("bash", bazelGateScript, arg)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "BAZEL_RBE_MODE=" + mode, "BAZEL_RBE_ENABLED=" + enabled}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %s (mode %q, enabled %q): %v", bazelGateScript, arg, mode, enabled, err)
		}
		return strings.TrimSpace(string(out))
	}

	var scenarios []bazelGateScenario
	for _, mode := range bazelRBEModes {
		// Expected skips, from the jobs' ifs alone.
		var wantSkips []string
		anyRuns := false
		for lane, modes := range lanes {
			// F3: bazel-gate.sh's skip list and its BAZEL aggregate are
			// mode-derived only; it has never heard of package-mcp/npm
			// (their own PACKAGE_MCP/PACKAGE_NPM scenarios are exercised
			// below, by lane, not through this script).
			if bazelPackageJobs[lane] {
				continue
			}
			if modes[mode] {
				anyRuns = true
			} else if id, gated := bazelLaneGateIDs[lane]; gated {
				wantSkips = append(wantSkips, id)
			}
		}
		if !anyRuns {
			wantSkips = append(wantSkips, bazelAggregateGateID)
		}
		sort.Strings(wantSkips)
		gotSkips := strings.Fields(runScript(mode, enabledFor(mode), "skips"))
		sort.Strings(gotSkips)
		if !reflect.DeepEqual(gotSkips, wantSkips) && !(len(gotSkips) == 0 && len(wantSkips) == 0) {
			t.Errorf("mode %s: %s skips = %v, want exactly %v (the ids whose job if is false)", mode, bazelGateScript, gotSkips, wantSkips)
		}

		base := func() map[string]string {
			out := map[string]string{}
			for lane, modes := range lanes {
				if modes[mode] {
					out[lane] = "success"
				}
			}
			return out
		}
		with := func(kv ...string) map[string]string {
			out := base()
			for i := 0; i+1 < len(kv); i += 2 {
				out[kv[i]] = kv[i+1]
			}
			return out
		}
		sc := func(name, call string, outputs map[string]string, pass bool, mention string) {
			for _, event := range []string{"pull_request", "merge_group"} {
				scenarios = append(scenarios, bazelGateScenario{
					name: mode + "/" + event + "/" + name, event: event, mode: mode, enabled: enabledFor(mode),
					call: call, outputs: outputs, wantPass: pass, wantMention: mention,
				})
			}
		}

		sc("every lane as designed", "success", base(), true, "")
		sc("aggregate failure no lane explains", "failure", base(), false, bazelAggregateGateID)
		sc("aggregate cancelled", "cancelled", base(), false, bazelAggregateGateID)
		sc("aggregate skipped", "skipped", base(), mode == "skip", bazelAggregateGateID)

		for lane, modes := range lanes {
			id, gated := bazelLaneGateIDs[lane]
			if !gated {
				// Advisory: not in the call, so it cannot report, and
				// nothing about it excuses the aggregate.
				for _, result := range []string{"failure", "cancelled"} {
					sc(lane+" reports "+result+" though off", "failure", with(lane, result), false, bazelAggregateGateID)
				}
				continue
			}
			if !modes[mode] {
				// Skipped by design (baseline); if it ran anyway and failed,
				// the gate still sees it.
				sc(lane+" ran and failed", "failure", with(lane, "failure"), false, id)
				continue
			}
			for _, call := range []string{"success", "failure"} {
				sc(lane+" alone skipped, aggregate "+call, call, with(lane, ""), false, id)
				sc(lane+" failed, aggregate "+call, call, with(lane, "failure"), false, id)
				sc(lane+" cancelled, aggregate "+call, call, with(lane, "cancelled"), false, id)
			}
			sc(lane+" cancelled, aggregate cancelled", "cancelled", with(lane, "cancelled"), false, id)
		}
	}

	// A missing or inconsistent decision (the rbe job failed, the call never
	// started, or the outputs disagree) allows no skip and fails the gate.
	for _, bad := range []struct{ mode, enabled string }{
		{"", ""}, {"remote", "false"}, {"local", "true"}, {"cache", "true"}, {"skip", "true"}, {"remote", ""}, {"bogus", "false"}, {"REMOTE", "true"}, {"CACHE", "false"},
		{"fork-ro", "false"}, {"fork-rw", "false"}, {"fork-ro", ""}, {"FORK-RO", "true"}, {"fork", "true"}, {"fork-", "true"}, {"fork-rx", "true"},
	} {
		if got := runScript(bad.mode, bad.enabled, "skips"); got != "" {
			t.Errorf("mode %q enabled %q: skips = %q, want none", bad.mode, bad.enabled, got)
		}
		all := map[string]string{}
		for lane := range lanes {
			all[lane] = "success"
		}
		scenarios = append(scenarios,
			bazelGateScenario{name: "invalid " + bad.mode + "/" + bad.enabled + ", every lane success", event: "pull_request",
				mode: bad.mode, enabled: bad.enabled, call: "success", outputs: all, wantMention: bazelAggregateGateID},
			bazelGateScenario{name: "invalid " + bad.mode + "/" + bad.enabled + ", nothing ran", event: "pull_request",
				mode: bad.mode, enabled: bad.enabled, call: "failure", outputs: map[string]string{}, wantMention: bazelAggregateGateID},
		)
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			pass, out := runPRGateStep(t, step, sc)
			if pass != sc.wantPass {
				t.Errorf("gate pass = %v, want %v (outputs %v, call %s)\n%s", pass, sc.wantPass, sc.outputs, sc.call, out)
			}
			if !sc.wantPass && sc.wantMention != "" && !regexp.MustCompile(`::error::`+sc.wantMention+`\b`).MatchString(out) {
				t.Errorf("red gate does not name %s:\n%s", sc.wantMention, out)
			}
		})
	}
}

// Artifact names are unique within a run, and a called workflow's uploads
// belong to the caller's run: a duplicate name makes the second upload fail
// (409) or a download pick the wrong file. So every run that calls bazel.yml
// (with its own inputs) must not upload any name twice (review D1 F7).
func TestBazelArtifactNamesUniqueInCallerRuns(t *testing.T) {
	for _, caller := range []string{"pr.yml", "nightly.yml", bazelFarmWorkflowName} {
		t.Run(caller, func(t *testing.T) {
			uses := collectRunArtifactUploads(t, caller, nil, "")
			var sawBazel bool
			for _, u := range uses {
				sawBazel = sawBazel || strings.Contains(u.where, "-> "+bazelWorkflowName+" job ")
			}
			if !sawBazel {
				t.Fatalf("%s run has no upload from %s; the collector did not follow the call", caller, bazelWorkflowName)
			}
			for i, a := range uses {
				if a.matrix && a.pattern == nil {
					t.Errorf("%s: %s uploads fixed name %q from a matrix job; every leg collides", caller, a.where, a.name)
				}
				for _, b := range uses[i+1:] {
					if bazelArtifactNamesCollide(a, b) {
						t.Errorf("%s run: %s and %s both upload %q / %q", caller, a.where, b.where, a.name, b.name)
					}
				}
			}
		})
	}
}

type runArtifactUpload struct {
	name, where string
	pattern     *regexp.Regexp // nil when the name resolved to a literal
	matrix      bool
}

func bazelArtifactNamesCollide(a, b runArtifactUpload) bool {
	switch {
	case a.pattern == nil && b.pattern == nil:
		return a.name == b.name
	case a.pattern == nil:
		return b.pattern.MatchString(a.name)
	case b.pattern == nil:
		return a.pattern.MatchString(b.name)
	}
	return a.name == b.name
}

// collectRunArtifactUploads lists the upload-artifact names of one run of
// file: its jobs' steps and, recursively, those of the local workflows its
// jobs call with their `with:` inputs (workflow_call defaults otherwise).
// inputs.X, `inputs.X || 'lit'` and env.X (job, then workflow env) resolve;
// any other expression (a matrix value) becomes a wildcard.
func collectRunArtifactUploads(t *testing.T, file string, with map[string]string, prefix string) []runArtifactUpload {
	t.Helper()
	path := filepath.Join(sourceRepoRoot(t), ".github", "workflows", file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		On struct {
			WorkflowCall struct {
				Inputs map[string]struct {
					Default any `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Uses     string            `yaml:"uses"`
			With     map[string]any    `yaml:"with"`
			Env      map[string]string `yaml:"env"`
			Strategy struct {
				Matrix any `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []ciWorkflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	inputs := map[string]string{}
	for name, in := range doc.On.WorkflowCall.Inputs {
		if in.Default != nil {
			inputs[name] = fmt.Sprint(in.Default)
		}
	}
	for k, v := range with {
		inputs[k] = v
	}
	exprRe := regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
	inputOr := regexp.MustCompile(`^inputs\.([A-Za-z0-9_-]+)(?:\s*\|\|\s*'([^']*)')?$`)
	envRef := regexp.MustCompile(`^env\.([A-Za-z0-9_]+)$`)
	var out []runArtifactUpload
	var names []string
	for name := range doc.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, jobName := range names {
		job := doc.Jobs[jobName]
		if strings.HasPrefix(job.Uses, "./.github/workflows/") {
			callWith := map[string]string{}
			for k, v := range job.With {
				callWith[k] = fmt.Sprint(v)
			}
			called := strings.TrimPrefix(job.Uses, "./.github/workflows/")
			out = append(out, collectRunArtifactUploads(t, called, callWith, prefix+file+" job "+jobName+" -> ")...)
			continue
		}
		var resolve func(s string, depth int) (string, bool)
		resolve = func(s string, depth int) (string, bool) {
			literal := true
			res := exprRe.ReplaceAllStringFunc(s, func(m string) string {
				inner := exprRe.FindStringSubmatch(m)[1]
				if in := inputOr.FindStringSubmatch(inner); in != nil {
					if v := inputs[in[1]]; v != "" {
						return v
					}
					return in[2]
				}
				if e := envRef.FindStringSubmatch(inner); e != nil && depth < 4 {
					v, ok := job.Env[e[1]]
					if !ok {
						v, ok = doc.Env[e[1]]
					}
					if ok {
						r, lit := resolve(v, depth+1)
						literal = literal && lit
						return r
					}
				}
				literal = false
				return "\x00"
			})
			return res, literal
		}
		for _, step := range job.Steps {
			if actionFamily(step.Uses) != "actions/upload-artifact" {
				continue
			}
			name, literal := resolve(step.With["name"], 0)
			u := runArtifactUpload{name: name, where: prefix + file + " job " + jobName, matrix: job.Strategy.Matrix != nil}
			if !literal {
				parts := strings.Split(name, "\x00")
				for i := range parts {
					parts[i] = regexp.QuoteMeta(parts[i])
				}
				u.pattern = regexp.MustCompile("^" + strings.Join(parts, ".+") + "$")
				u.name = strings.ReplaceAll(name, "\x00", "*")
			}
			out = append(out, u)
		}
	}
	return out
}

// Every action in the Bazel lane is pinned to a full commit SHA, with the same
// released SHAs the Go lanes use, and the monolithic actions/cache is banned
// here too (restore everywhere, save only where the topology says).
func TestBazelWorkflowActionsArePinned(t *testing.T) {
	steps := map[string][]ciWorkflowStep{
		setupBazelActionDir + "/action.yml": readSetupBazelAction(t).Runs.Steps,
	}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		steps[bazelWorkflowName+" "+name] = job.Steps
	}
	want := map[string]string{
		setupNodeActionFamily:       setupNodeSHA,
		setupPythonActionFamily:     setupPythonSHA,
		"actions/checkout":          checkoutSHA,
		setupGoActionFamily:         setupGoSHA,
		cacheRestoreActionFamily:    cacheSHA,
		cacheSaveActionFamily:       cacheSHA,
		"actions/upload-artifact":   uploadArtifactSHA,
		"actions/download-artifact": downloadArtifactSHA,
		// rbe-prewarm's mint step: same SHA/comment already used in this repo
		// for the same action, in update-flake-lock.yml.
		"actions/create-github-app-token": "bcd2ba49218906704ab6c1aa796996da409d3eb1",
	}
	for file, list := range steps {
		for _, step := range list {
			if step.Uses == "" || strings.HasPrefix(step.Uses, "./") {
				continue
			}
			family, sha, found := strings.Cut(step.Uses, "@")
			if !found || !actionPin.MatchString(sha) {
				t.Errorf("%s step %q action %q is not pinned to a 40-hex commit SHA", file, step.Name, step.Uses)
				continue
			}
			if isForbiddenGoCacheActionFamily(family) {
				t.Errorf("%s step %q uses forbidden monolithic %q; use actions/cache/restore and /save", file, step.Name, family)
				continue
			}
			wantSHA, known := want[family]
			if !known {
				t.Errorf("%s step %q uses %q, which this policy does not know; add its released SHA here", file, step.Name, family)
			} else if sha != wantSHA {
				t.Errorf("%s step %q action %q has SHA %q, want %q", file, step.Name, family, sha, wantSHA)
			}
			if family == setupGoActionFamily && step.With["cache"] != "false" {
				t.Errorf("%s setup-go cache = %q, want false", file, step.With["cache"])
			}
			if family == cacheSaveActionFamily && file != bazelWorkflowName+" "+bazelJobName {
				t.Errorf("%s step %q saves a cache; only %s's push-to-main step may", file, step.Name, bazelJobName)
			}
		}
		for _, step := range list {
			if strings.Contains(step.Run, "apt-get") {
				t.Errorf("%s step %q runs apt-get; bound it (see ci_workflow_apt_get_timeout_test.go) or drop it", file, step.Name)
			}
		}
	}
}

// The runner cache holds only content fixed by .bazelversion and
// MODULE.bazel.lock (repository cache, Bazelisk), so it is keyed on exactly
// that and written only by a push to main that missed the key; it never holds
// the generated rc or credentials.
func TestBazelWorkflowCacheTopology(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	var saves []ciWorkflowStep
	for _, step := range job.Steps {
		if actionFamily(step.Uses) == cacheSaveActionFamily {
			saves = append(saves, step)
		}
	}
	if len(saves) != 1 {
		t.Fatalf("%s has %d cache save steps, want 1", bazelWorkflowName, len(saves))
	}
	save := saves[0]
	if save.If != bazelCacheSaveIf {
		t.Errorf("cache save if = %q, want exactly %q", save.If, bazelCacheSaveIf)
	}
	if save.With["key"] != bazelCacheKey || save.With["path"] != bazelCachePath {
		t.Errorf("cache save key/path = %q / %q, want %q / %q", save.With["key"], save.With["path"], bazelCacheKey, bazelCachePath)
	}

	var restore, writer ciWorkflowStep
	for _, step := range readSetupBazelAction(t).Runs.Steps {
		switch {
		case actionFamily(step.Uses) == cacheRestoreActionFamily:
			restore = step
		case step.Env["BAZEL_CI_SECRET_DIR"] != "":
			writer = step
		}
	}
	if restore.With["path"] != bazelCachePath || restore.With["key"] != bazelCacheKey ||
		strings.TrimSpace(restore.With["restore-keys"]) != bazelCacheKeyPrefix {
		t.Errorf("setup-bazel restore path/key/restore-keys = %q / %q / %q; want the save step's path and key with a %q prefix fallback",
			restore.With["path"], restore.With["key"], restore.With["restore-keys"], bazelCacheKeyPrefix)
	}
	if writer.Env["BAZEL_CI_CACHE_DIR"] != bazelCachePath {
		t.Errorf("rc writer BAZEL_CI_CACHE_DIR = %q, want %q", writer.Env["BAZEL_CI_CACHE_DIR"], bazelCachePath)
	}
	if secret := writer.Env["BAZEL_CI_SECRET_DIR"]; !strings.HasPrefix(secret, "${{ runner.temp }}/") || strings.HasPrefix(secret, bazelCachePath) {
		t.Errorf("rc writer BAZEL_CI_SECRET_DIR = %q; want a runner.temp directory outside the cached %q", secret, bazelCachePath)
	}
}

// The lane runs the committed ci config over //..., reports the critical path
// and the go test equivalence even when tests fail, catches BUILD drift, and
// hands secrets to nothing but setup-bazel.
func TestBazelWorkflowRunsCIConfigWithReports(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	if job.TimeoutMinutes == 0 {
		t.Errorf("%s has no timeout-minutes", bazelJobName)
	}
	testStep := job.step(t, "bazel test //... --config=ci")
	for _, required := range []string{"bazel test //... --config=ci", "--profile=", "--build_event_json_file=", "set -o pipefail"} {
		if !strings.Contains(testStep.Run, required) {
			t.Errorf("test step does not contain %q:\n%s", required, testStep.Run)
		}
	}
	if strings.Contains(testStep.Run, "--config=remote-exec") {
		t.Errorf("test step selects remote-exec itself; setup-bazel's rc does that only when secrets are present")
	}
	if i, j := strings.Index(testStep.Run, "bazel test"), strings.Index(testStep.Run, "--profile="); j < i {
		t.Errorf("--profile is a command option and must follow `bazel test`")
	}
	for name, script := range map[string]string{
		"Critical-path report": "tools/bazel/critpath.py",
		"Go test equivalence":  "tools/bazel/equivalence.py",
	} {
		step := job.step(t, name)
		if !strings.Contains(step.Run, script) || !strings.Contains(step.If, "always()") {
			t.Errorf("step %q must run %s under always(); run=%q if=%q", name, script, step.Run, step.If)
		}
		assertReportStepKeepsExitStatus(t, step, "python3 "+script)
	}
	assertTestStepKeepsExitStatus(t, testStep)
	sync := job.step(t, "BUILD files in sync (gazelle, go_srcs, MODULE.bazel)")
	for _, required := range []string{"make bazel-sync-check", "make bazel-sync\n", "git status --porcelain --untracked-files=all", "exit 1"} {
		if !strings.Contains(sync.Run, required) {
			t.Errorf("sync step does not contain %q:\n%s", required, sync.Run)
		}
	}
}

// bazel.yml publishes a Bazel-built bd under pr.yml's build-artifacts contract
// (artifact name, file names, checksum file, retention), so the jobs that
// download ci-build-artifacts can switch to it without other edits.
func TestBazelWorkflowPublishesBuildArtifacts(t *testing.T) {
	var prUpload ciWorkflowStep
	for _, step := range readCIWorkflow(t, "pr.yml").job(t, "build-artifacts").Steps {
		if actionFamily(step.Uses) == "actions/upload-artifact" {
			prUpload = step
		}
	}
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	pkg := job.step(t, "Package bd (ci-build-artifacts layout)")
	upload := job.step(t, "Upload build artifacts")
	// Named by the build-artifact-name input, pr.yml's name by default; the
	// run inside pr.yml renames it (TestBazelLaneIsGatedAlongsideLegacy) so
	// the two uploads cannot collide.
	if prUpload.With["name"] != "ci-build-artifacts" ||
		upload.With["name"] != "${{ inputs.build-artifact-name || '"+prUpload.With["name"]+"' }}" ||
		readBazelWorkflowCall(t).Inputs["build-artifact-name"].Default != prUpload.With["name"] {
		t.Errorf("artifact name = %q, want the build-artifact-name input defaulting to pr.yml's %q", upload.With["name"], prUpload.With["name"])
	}
	for _, key := range []string{"retention-days", "if-no-files-found"} {
		if upload.With[key] != prUpload.With[key] {
			t.Errorf("upload %s = %q, want pr.yml's %q", key, upload.With[key], prUpload.With[key])
		}
	}
	if pkg.ID == "" || upload.If != "${{ always() && steps."+pkg.ID+".outcome == 'success' }}" {
		t.Errorf("upload if = %q; want it gated on the package step's success", upload.If)
	}
	// The commands live in a script that sources .buildflags itself, so the
	// build-tag scan (scripts/check-build-tags.sh) still covers bazel.yml.
	const script = "scripts/ci/package-bazel-bd.sh"
	if pkg.Run != "./"+script+` "$RUNNER_TEMP/bd-artifacts"` {
		t.Errorf("package step = %q, want %s", pkg.Run, script)
	}
	if strings.Contains(readPolicyFile(t, bazelPolicyRoot(t), ".github/workflows/"+bazelWorkflowName), ".buildflags") {
		t.Errorf("%s mentions .buildflags; sourcing it would exempt the whole file from the build-tag scan", bazelWorkflowName)
	}
	body := readPolicyFile(t, bazelPolicyRoot(t), script)
	for _, required := range []string{
		"set -euo pipefail", "source ./.buildflags",
		"/bin/cmd/bd/bd_for_tests/bd", "bd-linux-gms-pure", "sha256sum bd-linux-gms-pure > SHA256SUMS",
		"build-manifest.txt", "commit=", "go_version=", "build_tags=", "artifact=bd-linux-gms-pure",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("%s does not contain %q", script, required)
		}
	}
	// test:ci must download the binary under --remote_download_minimal.
	if !strings.Contains(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "test:ci --remote_download_regex=.*/bin/cmd/bd/bd_for_tests/bd$") {
		t.Error(".bazelrc test:ci does not download //cmd/bd:bd_for_tests")
	}
}

// bazel-doltserver replaces pr.yml's container-backed jobs: --config=doltserver
// (hermetic dolt sql-servers, remote-executable) by default, and a dolt-server
// target in every package those jobs run. --config=docker stays reachable as
// the A/B control (dispatch dolt-lane=docker), with the jobs' image pull.
func TestBazelDoltJobMirrorsContainerJobs(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelDoltJobName)
	pull := job.step(t, "Pull Dolt sql-server image")
	for _, name := range []string{"test-domain-uow", "contract-corpus"} {
		if want := pr.job(t, name).step(t, "Pull Dolt sql-server image").Run; pull.Run != want {
			t.Errorf("%s pulls the dolt image differently from %s (%q)", bazelDoltJobName, name, want)
		}
	}
	if pull.If != "${{ env.BAZEL_DOLT_LANE == 'docker' }}" {
		t.Errorf("%s pulls the dolt image with if %q; only the docker lane needs it", bazelDoltJobName, pull.If)
	}
	if got := job.Env["BAZEL_DOLT_LANE"]; got != "${{ inputs.dolt-lane || 'doltserver' }}" {
		t.Errorf("%s BAZEL_DOLT_LANE = %q, want the doltserver lane unless dispatched otherwise", bazelDoltJobName, got)
	}
	test := job.step(t, "bazel test //... --config=doltserver")
	if !strings.Contains(test.Run, `bazel test //... "--config=$BAZEL_DOLT_LANE"`) || !strings.Contains(test.Run, "set -o pipefail") {
		t.Errorf("dolt lane step does not run the lane over //...:\n%s", test.Run)
	}
	if strings.Contains(test.Run, "--config=remote-exec") {
		t.Errorf("dolt lane step selects remote-exec itself; setup-bazel's rc does that only when secrets are present")
	}
	assertTestStepKeepsExitStatus(t, test)

	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	for _, want := range []string{"test:doltserver --test_tag_filters=dolt-server", "test:docker --test_tag_filters=requires-docker"} {
		if !strings.Contains(rc, want+"\n") {
			t.Errorf(".bazelrc lacks %q", want)
		}
	}

	// The packages those jobs run (test-domain-uow: domain/..., uow,
	// tracker/... and doctor/fix; contract-corpus: protocol) each need a
	// dolt-server target. Every such target, checked on its own, picks the
	// local backend and fails closed, so a broken backend fails rather than
	// skipping into a cached pass; the ones whose TestMain owns a server also
	// set the package's own REQUIRE switch. Only under go test: scripts_test's
	// runfiles hold no other package's BUILD.
	if os.Getenv("TEST_SRCDIR") != "" {
		return
	}
	root := sourceRepoRoot(t)
	for pkg, docker := range map[string]bool{
		"internal/storage/domain":      false,
		"internal/storage/domain/db":   true,
		"internal/storage/domain/fs":   false,
		"internal/storage/domain/git":  false,
		"internal/storage/uow":         true,
		"internal/tracker":             true,
		"internal/tracker/conformance": false,
		"cmd/bd/doctor/fix":            true,
		"cmd/bd/protocol":              true,
	} {
		build := readPolicyFile(t, root, pkg+"/BUILD.bazel")
		for _, err := range checkDoltServerRules(pkg, build) {
			t.Error(err)
		}
		if docker && !strings.Contains(build, `"requires-docker"`) {
			t.Errorf("%s/BUILD.bazel has no requires-docker variant for the docker A/B lane", pkg)
		}
	}
}

// doltServerRuleEnv is the rule env every dolt-server target must set, and
// doltServerExtraEnv what particular targets need on top: the TestMain
// switches that turn a server which cannot start into a failure.
var (
	doltServerRuleEnv  = []string{`"BEADS_TEST_DOLT_SERVER": "local"`, `"BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1"`}
	doltServerExtraEnv = map[string][]string{
		"fix_dolt_test":      {`"BEADS_FIX_REQUIRE_DOLT": "1"`},
		"protocol_dolt_test": {`"BEADS_PROTOCOL_REQUIRE_DOLT": "1"`},
		// Without it every proxied test self-skips (the jobs' step env).
		"bd_proxied_test": {`"BEADS_TEST_PROXIED_SERVER": "1"`},
		// federation_test.go fails instead of skipping without a server.
		"dolt_server_full_test": {`"BEADS_TEST_ENV_RUN_DOLT": "1"`},
	}
	// doltServerLaneTags are the tags of the lanes whose targets start
	// hermetic dolt sql-servers; checkDoltServerRules holds each of them to
	// the same rules.
	doltServerLaneTags = []string{"dolt-server", "dolt-server-proxied", "dolt-server-integration"}
	bazelRuleNameRe    = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
)

// bazelTopRules splits a BUILD file into its top-level calls: each starts at
// a line matching bazelTopRuleRe and ends at the next line that is just ")".
func bazelTopRules(build string) []string {
	var rules []string
	var cur []string
	in := false
	for _, line := range strings.Split(build, "\n") {
		if !in && bazelTopRuleRe.MatchString(line) {
			in, cur = true, nil
		}
		if in {
			cur = append(cur, line)
			if line == ")" || (len(cur) == 1 && strings.HasSuffix(strings.TrimSpace(line), ")")) {
				rules = append(rules, strings.Join(cur, "\n"))
				in = false
			}
		}
	}
	return rules
}

// doltServerRuleTag returns the dolt-server lane tag a rule carries, or "".
func doltServerRuleTag(rule string) string {
	for _, tag := range doltServerLaneTags {
		if strings.Contains(rule, `"`+tag+`"`) {
			return tag
		}
	}
	return ""
}

// checkDoltServerRules checks each rule tagged for a dolt-server lane
// (doltServerLaneTags) in one package's BUILD file on its own (a file-wide
// search would accept another target's env), and that the package has at
// least one.
func checkDoltServerRules(pkg, build string) []error {
	var errs []error
	found := 0
	for _, rule := range bazelTopRules(stripStarlarkComments(build)) {
		tag := doltServerRuleTag(rule)
		if tag == "" {
			continue
		}
		found++
		name := "?"
		if m := bazelRuleNameRe.FindStringSubmatch(rule); m != nil {
			name = m[1]
		}
		where := "//" + pkg + ":" + name + " (" + tag + ")"
		for _, env := range append(append([]string{}, doltServerRuleEnv...), doltServerExtraEnv[name]...) {
			if !strings.Contains(rule, env) {
				errs = append(errs, errors.New(where+" lacks rule env "+env))
			}
		}
		if strings.Contains(rule, `"no-remote-exec"`) {
			errs = append(errs, errors.New(where+" is tagged no-remote-exec; the lane runs on remote workers"))
		}
	}
	if found == 0 {
		errs = append(errs, errors.New(pkg+"/BUILD.bazel has no dolt-server target for the dolt-server lane"))
	}
	return errs
}

// A dolt-server rule is checked on its own: another target in the same file
// (the docker variant) carrying the env must not cover for it.
func TestCheckDoltServerRulesPerTarget(t *testing.T) {
	const docker = `sh_test(
    name = "uow_docker_test",
    env = {
        "BEADS_TEST_DOLT_SERVER": "container",
        "BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1",
    },
    tags = ["requires-docker", "no-remote-exec"],
)
`
	good := `load("@rules_shell//shell:sh_test.bzl", "sh_test")

` + docker + `
# Tags:
#   dolt-server: lane.
sh_test(
    name = "uow_dolt_test",
    env = {
        "BEADS_TEST_DOLT_SERVER": "local",
        "BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1",
    },
    tags = ["dolt-server"],
)
`
	if errs := checkDoltServerRules("p", good); len(errs) != 0 {
		t.Errorf("good BUILD: %v", errs)
	}
	for name, bad := range map[string]string{
		"no require":      strings.Replace(good, "        \"BEADS_TEST_REQUIRE_DOLT_CONTAINER\": \"1\",\n    },\n    tags = [\"dolt-server\"]", "    },\n    tags = [\"dolt-server\"]", 1),
		"container":       strings.Replace(good, `"local"`, `"container"`, 1),
		"no-remote-exec":  strings.Replace(good, `tags = ["dolt-server"]`, `tags = ["dolt-server", "no-remote-exec"]`, 1),
		"no target":       docker,
		"fix switch":      strings.Replace(good, `"uow_dolt_test"`, `"fix_dolt_test"`, 1),
		"protocol switch": strings.Replace(good, `"uow_dolt_test"`, `"protocol_dolt_test"`, 1),
		"proxied switch": strings.Replace(strings.Replace(good, `"uow_dolt_test"`, `"bd_proxied_test"`, 1),
			`tags = ["dolt-server"]`, `tags = ["dolt-server-proxied"]`, 1),
		"run-dolt switch": strings.Replace(strings.Replace(good, `"uow_dolt_test"`, `"dolt_server_full_test"`, 1),
			`tags = ["dolt-server"]`, `tags = ["dolt-server-integration"]`, 1),
		"proxied container": strings.Replace(strings.Replace(good, `"local"`, `"container"`, 1),
			`tags = ["dolt-server"]`, `tags = ["dolt-server-proxied"]`, 1),
		"integration no-remote-exec": strings.Replace(good, `tags = ["dolt-server"]`, `tags = ["dolt-server-integration", "no-remote-exec"]`, 1),
	} {
		if bad == good {
			t.Fatalf("%s: mutation did not apply", name)
		}
		if errs := checkDoltServerRules("p", bad); len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The default shard manifest of a PR Risk shard script.
var shardManifestDefault = regexp.MustCompile(`\$\{BEADS_TEST_SHARD_MANIFEST:-([^}]+)\}`)

// bazelAttrBlock returns the text of a list attribute (`    name = [` up to
// its closing `    ],`) of a rule block from bazelRuleBlock, or "".
func bazelAttrBlock(rule, name string) string {
	i := strings.Index(rule, "\n    "+name+" = [")
	if i < 0 {
		return ""
	}
	end := strings.Index(rule[i:], "\n    ],\n")
	if end < 0 {
		return rule[i:]
	}
	return rule[i : i+end+7]
}

// bazelRuleBlock returns the text of the top-level rule named name in a
// BUILD file, or "" if there is none.
func bazelRuleBlock(build, name string) string {
	i := strings.Index(build, "\n    name = \""+name+"\",\n")
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(build[:i], "\n") + 1
	end := strings.Index(build[i:], "\n)\n")
	if end < 0 {
		return build[start:]
	}
	return build[start : i+end+3]
}

// bazelProxiedShardCount returns cmd/bd:bd_proxied_test's own shard_count
// from cmd/bd/BUILD.bazel: the single source of truth for the Bazel-only
// bazel-proxied lane's shard split, which no longer has to equal PR
// Risk's/main.yml's legacy test-proxied-cmd jobs' matrix size (F2). Under
// `bazel test`, scripts_test's runfiles hold no other package's BUILD file,
// so this falls back to the literal the structural checks below pin under
// plain `go test` (TestBazelDoltServerTiersMirrorPRRisk and
// TestBazelRetiredLanesCheckListedTestsRan both fail if cmd/bd/BUILD.bazel's
// shard_count ever drifts from this fallback).
func bazelProxiedShardCount(t *testing.T) int {
	t.Helper()
	const bazelTestFallback = 30
	if os.Getenv("TEST_SRCDIR") != "" {
		return bazelTestFallback
	}
	root := sourceRepoRoot(t)
	rule := bazelRuleBlock(readPolicyFile(t, root, "cmd/bd/BUILD.bazel"), "bd_proxied_test")
	m := regexp.MustCompile(`(?m)^    shard_count = (\d+),$`).FindStringSubmatch(rule)
	if m == nil {
		t.Fatalf("cmd/bd:bd_proxied_test has no `shard_count = N,` in cmd/bd/BUILD.bazel:\n%s", rule)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("cmd/bd:bd_proxied_test shard_count: %v", err)
	}
	return n
}

// bazel-integration mirrors main.yml's integration jobs with the unmodified
// --config=integration (whose .bazelrc filter and flags
// TestBazelrcIntegrationLane pins): no extra filter, selector or remote
// config on the command line, a BEP for the test-count check, and no
// equivalence step. ci and integration share bazel-testlogs (review F3), so
// neither the integration lane in bazel-test nor --config=ci here.
func TestBazelIntegrationJob(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelIntegJobName)
	test := job.step(t, "bazel test //... --config=integration")
	// A required PR lane: the step must fit a cold compile (the first
	// GitHub run took 17 minutes end to end) followed by the longest test
	// action, which .bazelrc caps at its test:integration --test_timeout
	// (rbe-west's 1200s limit). The job adds setup and log upload. In mode
	// cache it runs on a 4-CPU GitHub-hosted runner, and with the read-only
	// cache closed (the farm's kill switch) the whole suite builds and runs
	// there cold, about 30 minutes simulated: the step keeps twice that.
	const coldCompileMinutes = 20
	const cacheClosedMinutes = 30
	if test.TimeoutMinutes < 2*cacheClosedMinutes {
		t.Errorf("%s test step timeout-minutes = %d, want at least %d (mode cache with the cache closed: ~%d minutes cold on a GitHub-hosted runner)",
			bazelIntegJobName, test.TimeoutMinutes, 2*cacheClosedMinutes, cacheClosedMinutes)
	}
	actionCapMinutes := 0
	for line := range bazelrcLines(t) {
		if v, ok := strings.CutPrefix(line, "test:integration --test_timeout="); ok {
			secs, err := strconv.Atoi(v)
			if err != nil || secs <= 0 || secs > 1200 {
				t.Fatalf(".bazelrc test:integration --test_timeout=%s, want one value of at most 1200 (rbe-west's cap)", v)
			}
			actionCapMinutes = (secs + 59) / 60
		}
	}
	if actionCapMinutes == 0 {
		t.Fatal(".bazelrc has no test:integration --test_timeout")
	}
	if want := coldCompileMinutes + actionCapMinutes; test.TimeoutMinutes < want {
		t.Errorf("%s test step timeout-minutes = %d, want at least %d (cold compile %d + action cap %d)",
			bazelIntegJobName, test.TimeoutMinutes, want, coldCompileMinutes, actionCapMinutes)
	}
	if job.TimeoutMinutes <= test.TimeoutMinutes || job.TimeoutMinutes > test.TimeoutMinutes+15 {
		t.Errorf("%s timeout-minutes = %d, want above the test step's %d by at most 15", bazelIntegJobName, job.TimeoutMinutes, test.TimeoutMinutes)
	}
	cmd := regexp.MustCompile(`\s*\\\n\s*`).ReplaceAllString(test.Run, " ")
	const wantCmd = `bazel test //... --config=integration --build_event_json_file="$RUNNER_TEMP/bazel-bep.json" 2>&1 | tee "$RUNNER_TEMP/bazel-test.log" || rc=$?`
	if !strings.Contains(cmd, wantCmd) || !strings.Contains(test.Run, "set -o pipefail") {
		t.Errorf("%s test step does not run exactly %q:\n%s", bazelIntegJobName, wantCmd, test.Run)
	}
	if n := strings.Count(test.Run, "bazel test //"); n != 1 {
		t.Errorf("%s test step runs bazel test %d times, want 1", bazelIntegJobName, n)
	}
	assertTestStepKeepsExitStatus(t, test)
	count := job.step(t, "Every target and shard ran tests")
	const wantCount = `python3 tools/bazel/check_testcases.py --bep "$RUNNER_TEMP/bazel-bep.json" --not-go //tools/bazel:dolt_version_test`
	if strings.TrimSpace(count.Run) != wantCount ||
		count.If != "${{ always() && steps.test.outcome != 'skipped' }}" ||
		(count.ContinueOnError != nil && count.ContinueOnError != false) {
		t.Errorf("test-count step: if=%q continue-on-error=%v run=%q; want run %q", count.If, count.ContinueOnError, count.Run, wantCount)
	}
	logs := job.step(t, "Upload test logs")
	if logs.If != "${{ failure() && steps.test.outcome != 'skipped' }}" || logs.With["name"] != "bazel-integration-testlogs" {
		t.Errorf("test-log upload: if=%q name=%q", logs.If, logs.With["name"])
	}
	for name, j := range workflow.Jobs {
		for _, step := range j.Steps {
			integ := strings.Contains(step.Run, "--config=integration")
			if name == bazelIntegJobName && (strings.Contains(step.Run, "--config=ci") || strings.Contains(step.Run, "equivalence.py")) {
				t.Errorf("%s step %q runs the ci lane or equivalence.py; they would read each other's bazel-testlogs", name, step.Name)
			}
			if name != bazelIntegJobName && integ {
				t.Errorf("%s step %q runs --config=integration; only %s may (shared bazel-testlogs)", name, step.Name, bazelIntegJobName)
			}
		}
	}
	if err := checkBazelrcIntegrationLane(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")); err != nil {
		t.Error(err)
	}
}

// bazelrcLines returns .bazelrc's trimmed lines as a set.
func bazelrcLines(t *testing.T) map[string]bool {
	t.Helper()
	lines := map[string]bool{}
	for _, line := range strings.Split(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	return lines
}

// assertBazelTierStep checks the test step of a remote-only tier job in
// bazel.yml: it runs exactly `bazel test //... --config=<config>` with a BEP,
// keeps bazel's exit status, has a timeout below the job's, and is followed
// by check_testcases.py on that BEP.
func assertBazelTierStep(t *testing.T, job ciWorkflowJob, jobName, config string) {
	t.Helper()
	const id, bep, wantIf = "test", "bazel-bep.json", ""
	step := job.step(t, "bazel test //... --config="+config)
	if step.ID != id || step.If != wantIf || step.TimeoutMinutes == 0 || step.TimeoutMinutes >= job.TimeoutMinutes {
		t.Errorf("%s step --config=%s: id=%q if=%q timeout-minutes=%d; want id %q, if %q, a timeout below the job's %d",
			jobName, config, step.ID, step.If, step.TimeoutMinutes, id, wantIf, job.TimeoutMinutes)
	}
	cmd := regexp.MustCompile(`\s*\\\n\s*`).ReplaceAllString(step.Run, " ")
	wantCmd := `bazel test //... --config=` + config + ` --build_event_json_file="$RUNNER_TEMP/` + bep + `"`
	if !strings.Contains(cmd, wantCmd) || !strings.Contains(step.Run, "set -o pipefail") || strings.Count(step.Run, "bazel test //") != 1 ||
		strings.Contains(step.Run, "--config=remote-exec") || strings.Contains(step.Run, "--test_tag_filters") {
		t.Errorf("%s step --config=%s does not run exactly %q:\n%s", jobName, config, wantCmd, step.Run)
	}
	assertTestStepKeepsExitStatus(t, step)
	found := false
	for _, s := range job.Steps {
		if strings.TrimSpace(s.Run) == `python3 tools/bazel/check_testcases.py --bep "$RUNNER_TEMP/`+bep+`"` {
			found = true
			if s.If != "${{ always() && steps."+id+".outcome != 'skipped' }}" || (s.ContinueOnError != nil && s.ContinueOnError != false) {
				t.Errorf("%s: check_testcases.py for %s: if=%q continue-on-error=%v", jobName, bep, s.If, s.ContinueOnError)
			}
		}
	}
	if !found {
		t.Errorf("%s: no check_testcases.py step on %s (a shard or selector that runs no tests exits 0)", jobName, bep)
	}
}

// The proxied-server tier (pr-risk.yml "Test (Proxied Dolt Cmd N/15)"; main.yml's
// twin runs the same shard script on a non-race binary) and the server-Dolt storage tier (pr-risk.yml "Test (Server Dolt
// Conformance)", "Test (Server Dolt Full Suite N/16)") as Bazel variants:
// each manifest-sharded variant runs its jobs' shard script with their shard
// total, the conformance variant the job's exact flags, with the jobs' race
// setting, subprocess binaries and switches; the lanes' configs keep the
// jobs' selection, and the storage lane shares --config=integration's build.
// bazel.yml runs both tiers remotely only.
func TestBazelDoltServerTiersMirrorPRRisk(t *testing.T) {
	rc := bazelrcLines(t)
	for _, want := range []string{
		"test:doltserver-proxied --@rules_go//go/config:race",
		"test:doltserver-proxied --test_tag_filters=dolt-server-proxied",
		"test:doltserver-proxied --test_timeout=-1,-1,-1,1200",
		"test:doltserver-integration --test_tag_filters=dolt-server-integration",
		"test:doltserver-integration --test_timeout=-1,-1,-1,1200",
	} {
		if !rc[want] {
			t.Errorf(".bazelrc lacks %q", want)
		}
	}
	var tierTags, integTags map[string]bool
	for line := range rc {
		for _, config := range []string{"doltserver-proxied", "doltserver-integration"} {
			if strings.HasPrefix(line, "test:"+config+" ") && (strings.Contains(line, "-test.short") || strings.Contains(line, "BEADS_TEST_SKIP") ||
				strings.Contains(line, "-test.run") || strings.Contains(line, "-test.skip") || strings.Contains(line, "--test_filter")) {
				t.Errorf(".bazelrc %q: the jobs run their scripts' selection, without -short or BEADS_TEST_SKIP", line)
			}
		}
		if v, ok := strings.CutPrefix(line, "build:doltserver-integration --@rules_go//go/config:tags="); ok {
			tierTags = tagSet(v)
		}
		if v, ok := strings.CutPrefix(line, "build:integration --@rules_go//go/config:tags="); ok {
			integTags = tagSet(v)
		}
	}
	// The same build flags as --config=integration, or the tier compiles the
	// tagged graph once more.
	if tierTags == nil || !sameTagSet(tierTags, integTags) {
		t.Errorf(".bazelrc build:doltserver-integration tags = %v, want build:integration's %v", tierTags, integTags)
	}
	if rc["test:integration --@rules_go//go/config:race"] != rc["test:doltserver-integration --@rules_go//go/config:race"] {
		t.Error(".bazelrc: test:doltserver-integration and test:integration differ in race; they must share their build")
	}

	risk := readCIWorkflow(t, "pr-risk.yml")
	build := risk.job(t, "build-embedded")
	for step, want := range map[string]string{
		// The proxied jobs' test binary is this race build (--config=
		// doltserver-proxied's bd_test), their subprocess bd the non-race one
		// (bd_for_tests).
		"Build embedded cmd test binary":     "go test -tags gms_pure_go -race -c -o /tmp/bd-cmd-test ./cmd/bd/",
		"Build proxied bd subprocess binary": "go build -tags gms_pure_go -o /tmp/bd-proxied ./cmd/bd/",
	} {
		if got := strings.TrimSpace(build.step(t, step).Run); got != want {
			t.Errorf("pr-risk.yml %q = %q, want %q", step, got, want)
		}
	}
	// main.yml's twin proxied jobs run build-artifacts' bd-cmd-test, which
	// is not race: bd_proxied_test (race, like PR Risk's) is the stricter of
	// the two. Pinned so a change there is a decision, not drift.
	mainYML := readCIWorkflow(t, "main.yml")
	if run := mainYML.job(t, "build-artifacts").step(t, "Build reusable Linux artifacts").Run; !strings.Contains(run, `go test -tags gms_pure_go -c -o artifacts/bd-cmd-test ./cmd/bd`+"\n") {
		t.Errorf("main.yml build-artifacts no longer builds the non-race bd-cmd-test this tier is documented against (.bazelrc, cmd/bd:bd_proxied_test):\n%s", run)
	}
	if got := mainYML.job(t, "test-proxied-cmd").step(t, "Test proxied-server cmd shard").Env["BEADS_TEST_CMD_BINARY"]; got != "${{ github.workspace }}/ci-build-artifacts/bd-cmd-test" {
		t.Errorf("main.yml test-proxied-cmd BEADS_TEST_CMD_BINARY = %q, want build-artifacts' bd-cmd-test", got)
	}
	// The server jobs' binary: integration-tagged (the lane's build tags) and
	// not race (dolt_race_off).
	m := regexp.MustCompile(`^go test -tags=(\S+) -c -o /tmp/dolt-conformance-test \./internal/storage/dolt/$`).
		FindStringSubmatch(strings.TrimSpace(build.step(t, "Build server Dolt conformance test binary").Run))
	if m == nil || !sameTagSet(tagSet(m[1]), tierTags) {
		t.Errorf("pr-risk.yml's server Dolt test binary is no longer the non-race `go test -tags=<build:doltserver-integration tags> -c`: %v", m)
	}

	type shardTier struct {
		workflow, job, step, script, binVar, pkg, target, env string
		// noBuildShardPin is true when this tier's target's BUILD.bazel
		// shard_count is allowed to differ from this workflow/job's own
		// matrix size, because a separate Bazel-only lane shards that same
		// target on its own manifest block (F2). Reading the expected value
		// back out of the same BUILD.bazel file we are about to check would
		// be tautological, so these tiers skip the `shard_count = N,`
		// sub-assertion below entirely; the real cross-file pin for them
		// lives in pr_risk_bazel_coverage_test.go, which ties BUILD.bazel's
		// shard_count to bazel.yml's check_shard_coverage.py argument.
		noBuildShardPin bool
	}
	tiers := []shardTier{
		// pr-risk.yml/main.yml's own "Test (Proxied Dolt Cmd N/15)" matrix
		// stays 15 (the legacy, frozen bd-init-cost-proxy manifest block),
		// but cmd/bd:bd_proxied_test's shard_count need not match: the
		// Bazel-only bazel-proxied job (bazel.yml) runs that target with its
		// own duration-balanced manifest block, not these jobs'. Shard k in
		// one split is not shard k in the other; see cmd/bd/BUILD.bazel's
		// bd_proxied_test comment and bazel.yml's bazel-proxied comment. Both
		// matrices are still required (below, after this loop) to equal each
		// other and the legacy manifest block's total.
		{"pr-risk.yml", "test-proxied-cmd", "Test proxied-server cmd shard", ".github/scripts/proxied-test-shard.sh", "BEADS_TEST_CMD_BINARY", "cmd/bd", "bd_proxied_test", "BEADS_TEST_PROXIED_SERVER", true},
		{"main.yml", "test-proxied-cmd", "Test proxied-server cmd shard", ".github/scripts/proxied-test-shard.sh", "BEADS_TEST_CMD_BINARY", "cmd/bd", "bd_proxied_test", "BEADS_TEST_PROXIED_SERVER", true},
		{"pr-risk.yml", "test-server-storage-full", "Test", ".github/scripts/server-storage-test-shard.sh", "BEADS_TEST_SERVER_TEST_BINARY", "internal/storage/dolt", "dolt_server_full_test", "BEADS_TEST_ENV_RUN_DOLT", false},
	}
	proxiedMatrixLen := map[string]int{}
	for _, c := range tiers {
		j := readCIWorkflow(t, c.workflow).job(t, c.job)
		shards := len(j.Strategy.Matrix.Shard)
		if c.target == "bd_proxied_test" {
			proxiedMatrixLen[c.workflow] = shards
		}
		step := j.step(t, c.step)
		if want := "bash " + c.script + " ${{ matrix.shard }} " + strconv.Itoa(shards); strings.TrimSpace(step.Run) != want {
			t.Errorf("%s %s runs %q, want %q", c.workflow, c.job, step.Run, want)
		}
		if step.Env[c.env] != "1" {
			t.Errorf("%s %s no longer sets %s=1; update %s:%s", c.workflow, c.job, c.env, c.pkg, c.target)
		}
		if os.Getenv("TEST_SRCDIR") != "" {
			continue // scripts_test's runfiles hold no other package's BUILD
		}
		buildShards := shards
		root := sourceRepoRoot(t)
		rule := bazelRuleBlock(readPolicyFile(t, root, c.pkg+"/BUILD.bazel"), c.target)
		want := []string{
			`srcs = ["//tools/bazel:go_test_manifest_shard.sh"],`,
			`"$(rootpath //:` + c.script + `)",`,
			`"` + c.binVar + `",`,
			`timeout = "eternal",`,
		}
		if !c.noBuildShardPin {
			want = append(want, "shard_count = "+strconv.Itoa(buildShards)+",")
		}
		for _, w := range want {
			if !strings.Contains(rule, w) {
				t.Errorf("%s:%s does not contain %q (%s %s):\n%s", c.pkg, c.target, w, c.workflow, c.job, rule)
			}
		}
		mm := shardManifestDefault.FindStringSubmatch(readPolicyFile(t, root, c.script))
		if mm == nil {
			t.Fatalf("%s has no ${BEADS_TEST_SHARD_MANIFEST:-...} default manifest", c.script)
		}
		data := bazelAttrBlock(rule, "data")
		for _, file := range []string{c.script, mm[1]} {
			if !strings.Contains(data, `"//:`+file+`",`) {
				t.Errorf("%s:%s data lacks //:%s (without the manifest every shard falls back to hashing):\n%s", c.pkg, c.target, file, data)
			}
		}
	}
	// S1: pr-risk.yml's and main.yml's legacy test-proxied-cmd matrices must
	// stay equal to each other (they share one frozen manifest block); a
	// divergence must fail here even though cmd/bd:bd_proxied_test's own
	// shard_count is no longer pinned to either of them (above).
	if a, b := proxiedMatrixLen["pr-risk.yml"], proxiedMatrixLen["main.yml"]; a != b {
		t.Errorf("pr-risk.yml test-proxied-cmd has %d shards, main.yml has %d; these share one legacy manifest block and must match", a, b)
	}

	conf := risk.job(t, "test-server-storage").step(t, "Test").Run
	quoted := regexp.MustCompile(`(-test\.[a-z]+) '([^']*)'|(-test\.[a-z]+=\S+|-test\.v)`)
	fields := strings.Fields(conf)
	if len(fields) == 0 || fields[0] != "/tmp/dolt-conformance-test" {
		t.Fatalf("pr-risk.yml test-server-storage no longer runs /tmp/dolt-conformance-test: %q", conf)
	}
	var wantArgs []string
	for _, m := range quoted.FindAllStringSubmatch(conf, -1) {
		if m[1] != "" {
			wantArgs = append(wantArgs, `"`+m[1]+"="+strings.ReplaceAll(m[2], "$", "$$")+`",`)
		} else {
			wantArgs = append(wantArgs, `"`+m[3]+`",`)
		}
	}
	if len(wantArgs) != 4 {
		t.Fatalf("parsed %v from pr-risk.yml test-server-storage %q", wantArgs, conf)
	}

	if os.Getenv("TEST_SRCDIR") == "" {
		root := sourceRepoRoot(t)
		doltBuild := readPolicyFile(t, root, "internal/storage/dolt/BUILD.bazel")
		rule := bazelRuleBlock(doltBuild, "dolt_server_conformance_test")
		for _, w := range append(wantArgs, `"$(rootpath :dolt_race_off)",`, `srcs = ["//tools/bazel:go_test_variant.sh"],`) {
			if !strings.Contains(bazelAttrBlock(rule, "args")+rule, w) {
				t.Errorf("dolt:dolt_server_conformance_test does not contain %q (pr-risk.yml test-server-storage):\n%s", w, rule)
			}
		}
		if strings.Contains(rule, "BEADS_TEST_ENV_RUN_DOLT") {
			t.Error("dolt:dolt_server_conformance_test sets BEADS_TEST_ENV_RUN_DOLT; test-server-storage does not")
		}
		if r := bazelRuleBlock(doltBuild, "dolt_race_off"); !strings.Contains(r, "go_test_race_off(") || !strings.Contains(r, `test = ":dolt_test",`) {
			t.Errorf("dolt:dolt_race_off must be go_test_race_off of :dolt_test (the jobs' binary is not race):\n%s", r)
		}
		full := bazelRuleBlock(doltBuild, "dolt_server_full_test")
		for _, w := range []string{`"$(rootpath :dolt_race_off)",`, `"BEADS_TEST_SUBPROCESS_BINARY": "$(rlocationpath :dolt_race_off)"`, `"//:go.mod",`} {
			if !strings.Contains(full, w) {
				t.Errorf("dolt:dolt_server_full_test lacks %q (SubprocessRunner reuses the test binary; ModuleRoot needs go.mod):\n%s", w, full)
			}
		}
		proxied := bazelRuleBlock(readPolicyFile(t, root, "cmd/bd/BUILD.bazel"), "bd_proxied_test")
		for _, w := range []string{`"$(rootpath :bd_test)",`, `"BEADS_TEST_BD_BINARY": "$(rlocationpath :bd_for_tests)"`} {
			if !strings.Contains(proxied, w) {
				t.Errorf("cmd/bd:bd_proxied_test lacks %q (race bd_test, non-race subprocess bd, like the jobs):\n%s", w, proxied)
			}
		}
		for pkg, build := range map[string]string{"cmd/bd": readPolicyFile(t, root, "cmd/bd/BUILD.bazel"), "internal/storage/dolt": doltBuild} {
			for _, err := range checkDoltServerRules(pkg, build) {
				t.Error(err)
			}
		}
	}

	// Each tier is a remote-only job of its own (the job if is pinned by
	// TestBazelWorkflowJobsAndExecutionMode via bazelRemoteOnlyJobs, and
	// pr.yml's gate requires it: bazelLaneGateIDs): not a step of
	// bazel-doltserver (which also runs locally, and whose job a gate may
	// require) or of bazel-integration (which a caller may switch off,
	// while the server tier always runs remotely).
	workflow := readCIWorkflow(t, bazelWorkflowName)
	for _, c := range []struct{ job, config, logs string }{
		{bazelProxiedJobName, "doltserver-proxied", "bazel-proxied-testlogs"},
		{bazelServerJobName, "doltserver-integration", "bazel-server-storage-testlogs"},
	} {
		job := workflow.job(t, c.job)
		if job.TimeoutMinutes == 0 || job.TimeoutMinutes > 30 {
			t.Errorf("%s timeout-minutes = %d; it runs remotely only (longest shard ~2-8 min), keep it at most 30", c.job, job.TimeoutMinutes)
		}
		assertBazelTierStep(t, job, c.job, c.config)
		if n := len(job.Steps); n != 8 {
			t.Errorf("%s has %d steps, want checkout, setup-bazel, the flaky query, the tier, check_testcases.py, check_shard_coverage.py, log upload, result recorder", c.job, n)
		}
		logs := job.step(t, "Upload test logs")
		if logs.If != "${{ failure() && steps.test.outcome != 'skipped' }}" || logs.With["name"] != c.logs || !strings.HasPrefix(logs.Uses, "actions/upload-artifact@") {
			t.Errorf("%s test-log upload: if=%q name=%q uses=%q", c.job, logs.If, logs.With["name"], logs.Uses)
		}
	}
	for name, j := range workflow.Jobs {
		for _, step := range j.Steps {
			proxied := strings.Contains(step.Run, "--config=doltserver-proxied")
			server := strings.Contains(step.Run, "--config=doltserver-integration")
			if (proxied && name != bazelProxiedJobName) || (server && name != bazelServerJobName) {
				t.Errorf("%s step %q runs a dolt-server tier outside its own job", name, step.Name)
			}
			if (name == bazelIntegJobName || name == bazelDoltJobName) && (proxied || server || strings.Contains(step.Run, "doltserver-")) {
				t.Errorf("%s step %q runs the proxied or server storage tier; each has its own job", name, step.Name)
			}
		}
	}
}

// bazel-embedded replaces pr-risk.yml's embedded-Dolt tier: --config=embedded
// runs the embedded variants race like the jobs' binaries, with the jobs'
// parallelism; each shard variant runs its job's shard script with the job's
// shard total, and the conformance variants pass the jobs' exact selectors.
// bazel-embedded's test step, exactly (review F4).
const bazelEmbeddedTestRun = `set -o pipefail
start=$(date +%s)
rc=0
bazel test //... --config=embedded \
  --build_event_json_file="$RUNNER_TEMP/bazel-bep.json" \
  2>&1 | tee "$RUNNER_TEMP/bazel-test.log" || rc=$?
echo "bazel test --config=embedded: exit $rc, $(( $(date +%s) - start ))s wall" | tee -a "$GITHUB_STEP_SUMMARY"
exit "$rc"`

// .bazelrc's --config=embedded, exactly and in order (review F4): no
// --test_filter, no -test.short/-test.run/-test.skip, no retries, no
// result caching. The conformance targets' own -test.run/-test.skip args
// (the legacy jobs' partition) are checked against pr-risk.yml below.
var bazelEmbeddedRCLines = []string{
	"test:embedded --@rules_go//go/config:race",
	"test:embedded --test_tag_filters=embedded",
	"test:embedded --build_tests_only",
	"test:embedded --keep_going",
	"test:embedded --test_summary=terse",
	"test:embedded --test_timeout=-1,-1,-1,1200",
	"test:embedded --test_arg=-test.parallel=4",
	"test:embedded --test_env=GO_TEST_WRAP_TESTV=1",
	"test:embedded --remote_download_regex=.*/test\\.xml$",
	"test:embedded --nocache_test_results",
	"test:embedded --experimental_remote_cache_eviction_retries=0",
}

func TestBazelEmbeddedJobMirrorsEmbeddedTier(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelEmbedJobName)
	test := job.step(t, "bazel test //... --config=embedded")
	if !strings.Contains(test.Run, "bazel test //... --config=embedded") || !strings.Contains(test.Run, "set -o pipefail") {
		t.Errorf("embedded step does not run --config=embedded over //...:\n%s", test.Run)
	}
	if strings.Contains(test.Run, "--config=remote-exec") {
		t.Errorf("embedded step selects remote-exec itself; setup-bazel's rc does that only when secrets are present")
	}
	assertTestStepKeepsExitStatus(t, test)
	// Review F4: the exact command line. Since D2 step 1 this lane is the
	// tier's only pre-merge run on same-repo PRs, so an extra flag (a
	// --test_filter, a --test_arg=-test.short/-test.run/-test.skip) that
	// quietly narrows it must be a reviewed edit of this test too.
	if strings.TrimSpace(test.Run) != bazelEmbeddedTestRun {
		t.Errorf("embedded step run changed; want exactly:\n%s\ngot:\n%s", bazelEmbeddedTestRun, test.Run)
	}
	// A shard with no tests assigned, or a selector that matches nothing,
	// exits 0; the job fails on any target or shard whose test.xml lists none.
	if !strings.Contains(test.Run, `--build_event_json_file="$RUNNER_TEMP/bazel-bep.json"`) {
		t.Errorf("embedded step writes no BEP for the test-count check:\n%s", test.Run)
	}
	count := job.step(t, "Every target and shard ran tests")
	if strings.TrimSpace(count.Run) != `python3 tools/bazel/check_testcases.py --bep "$RUNNER_TEMP/bazel-bep.json"` ||
		count.If != "${{ always() && steps.test.outcome != 'skipped' }}" ||
		(count.ContinueOnError != nil && count.ContinueOnError != false) {
		t.Errorf("test-count step: if=%q continue-on-error=%v run=%q", count.If, count.ContinueOnError, count.Run)
	}

	rc := map[string]bool{}
	for _, line := range strings.Split(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "\n") {
		rc[strings.TrimSpace(line)] = true
	}
	for _, want := range []string{
		"test:embedded --@rules_go//go/config:race",
		"test:embedded --test_tag_filters=embedded",
		// The jobs pass no -parallel: GOMAXPROCS on 4-vCPU ubuntu-latest.
		"test:embedded --test_arg=-test.parallel=4",
		// The tier's only pre-merge run (D2 step 1) must execute, like the
		// legacy jobs' -test.count=1, never replay a cached result.
		"test:embedded --nocache_test_results",
	} {
		if !rc[want] {
			t.Errorf(".bazelrc lacks %q", want)
		}
	}
	// Review F4: --config=embedded is exactly these lines, in this order, and
	// no unconfigured common/build/test line (which applies to every config)
	// selects or narrows tests.
	var embeddedLines []string
	narrow := regexp.MustCompile(`test_filter|test_arg|-test\.(short|run|skip)|test_tag_filters|test_lang_filters|test_size_filters|test_timeout_filters`)
	for _, line := range strings.Split(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "\n") {
		line = strings.TrimSpace(line)
		cmd, _, _ := strings.Cut(line, " ")
		if strings.HasSuffix(cmd, ":embedded") {
			embeddedLines = append(embeddedLines, line)
		}
		if (cmd == "common" || cmd == "build" || cmd == "test") && narrow.MatchString(line) {
			t.Errorf(".bazelrc %q narrows every config's tests, including --config=embedded", line)
		}
	}
	if !reflect.DeepEqual(embeddedLines, bazelEmbeddedRCLines) {
		t.Errorf(".bazelrc --config=embedded lines changed; want exactly:\n%s\ngot:\n%s",
			strings.Join(bazelEmbeddedRCLines, "\n"), strings.Join(embeddedLines, "\n"))
	}
	if gen := readPolicyFile(t, sourceRepoRoot(t), setupBazelActionDir+"/write-bazelrc.sh"); narrow.MatchString(gen) {
		t.Errorf("setup-bazel's generated rc selects or narrows tests; it may only configure remote execution")
	}
	for line := range rc {
		// Nothing turns result caching back on for the embedded lane (a
		// later --cache_test_results wins over --nocache_test_results).
		if !strings.HasPrefix(line, "#") && strings.Contains(line, "cache_test_results") &&
			line != "test:embedded --nocache_test_results" && line != "test:docker --nocache_test_results" &&
			line != "test:doltserver-proxied --nocache_test_results" && line != "test:doltserver-integration --nocache_test_results" &&
			line != bazelSoleRunNoCacheLine {
			t.Errorf(".bazelrc %q: only test:embedded and test:docker set test result caching", line)
		}
		if strings.HasPrefix(line, "test:embedded ") && (strings.Contains(line, "-test.short") || strings.Contains(line, "BEADS_TEST_SKIP")) {
			t.Errorf(".bazelrc %q: the embedded jobs run without -short and BEADS_TEST_SKIP", line)
		}
	}

	risk := readCIWorkflow(t, "pr-risk.yml")
	build := risk.job(t, "build-embedded")
	for step, want := range map[string]string{
		"Build embedded bd binary":           "go build -tags gms_pure_go -race -o /tmp/bd-embedded-test ./cmd/bd/",
		"Build embedded storage test binary": "go test -tags gms_pure_go -race -c -o /tmp/embeddeddolt-test ./internal/storage/embeddeddolt/",
		"Build embedded cmd test binary":     "go test -tags gms_pure_go -race -c -o /tmp/bd-cmd-test ./cmd/bd/",
	} {
		if got := strings.TrimSpace(build.step(t, step).Run); got != want {
			t.Errorf("pr-risk.yml %q = %q, want %q (the race build --config=embedded mirrors)", step, got, want)
		}
	}

	sharded := []struct {
		job, script, pkg, target string
	}{
		{"test-embedded-cmd", ".github/scripts/embedded-test-shard.sh", "cmd/bd", "bd_embedded_test"},
		{"test-embedded-storage", ".github/scripts/embedded-storage-test-shard.sh", "internal/storage/embeddeddolt", "embeddeddolt_embedded_test"},
	}
	for _, c := range sharded {
		j := risk.job(t, c.job)
		shards := len(j.Strategy.Matrix.Shard)
		step := j.step(t, "Test")
		if want := "bash " + c.script + " ${{ matrix.shard }} " + strconv.Itoa(shards); strings.TrimSpace(step.Run) != want {
			t.Errorf("pr-risk.yml %s runs %q, want %q", c.job, step.Run, want)
		}
		if step.Env["BEADS_TEST_EMBEDDED_DOLT"] != "1" {
			t.Errorf("pr-risk.yml %s no longer sets BEADS_TEST_EMBEDDED_DOLT=1; update %s", c.job, c.target)
		}
		if os.Getenv("TEST_SRCDIR") != "" {
			continue // scripts_test's runfiles hold no other package's BUILD
		}
		root := sourceRepoRoot(t)
		rule := bazelRuleBlock(readPolicyFile(t, root, c.pkg+"/BUILD.bazel"), c.target)
		// c.target's own shard_count is deliberately NOT asserted to equal
		// shards (pr-risk.yml's matrix size) here: since slice F1, the
		// Bazel-only bazel-embedded job (bazel.yml) shards both of these
		// targets on its own duration-balanced manifest block, a different
		// total from PR Risk's/main.yml's legacy "Test (Embedded Dolt {Cmd
		// N/20,Storage N/5})" jobs' frozen block (same reasoning as
		// cmd/bd:bd_proxied_test's noBuildShardPin above, F2). The real
		// cross-file pin for c.target's shard_count lives in
		// pr_risk_bazel_coverage_test.go's TestBazelRetiredLanesCheckListedTestsRan,
		// which ties it to bazel.yml's check_shard_coverage.py argument via
		// bazelEmbeddedCmdShardCount/bazelEmbeddedStorageShardCount.
		for _, want := range []string{
			`srcs = ["//tools/bazel:go_test_manifest_shard.sh"],`,
			`"$(rootpath //:` + c.script + `)",`,
			`"BEADS_TEST_EMBEDDED_DOLT": "1"`,
			`"embedded"`,
		} {
			if !strings.Contains(rule, want) {
				t.Errorf("%s:%s does not contain %q (pr-risk.yml %s):\n%s", c.pkg, c.target, want, c.job, rule)
			}
		}
		// The script's -test.timeout=20m equals Bazel's 1200s action limit,
		// which kills without a goroutine dump; 19m lets Go's fire first.
		script := readPolicyFile(t, root, c.script)
		if !strings.Contains(script, "-test.timeout=20m") {
			t.Errorf("%s no longer passes -test.timeout=20m; revisit %s:%s's -test.timeout=19m", c.script, c.pkg, c.target)
		}
		if !strings.Contains(bazelAttrBlock(rule, "args"), `"-test.timeout=19m",`) {
			t.Errorf("%s:%s must pass -test.timeout=19m after the script's own:\n%s", c.pkg, c.target, rule)
		}
		// The script and its manifest must be in the runfiles: without the
		// manifest the script silently falls back to hash assignment, so
		// every shard still passes but no longer runs its job's tests.
		m := shardManifestDefault.FindStringSubmatch(script)
		if m == nil {
			t.Fatalf("%s has no ${BEADS_TEST_SHARD_MANIFEST:-...} default manifest", c.script)
		}
		data := bazelAttrBlock(rule, "data")
		for _, file := range []string{c.script, m[1]} {
			if !strings.Contains(data, `"//:`+file+`",`) {
				t.Errorf("%s:%s data lacks //:%s:\n%s", c.pkg, c.target, file, data)
			}
		}
	}
	if os.Getenv("TEST_SRCDIR") == "" {
		// The cmd jobs' subprocess bd is the race build, as //cmd/bd:bd is
		// under --config=embedded (bd_for_tests never is).
		rule := bazelRuleBlock(readPolicyFile(t, sourceRepoRoot(t), "cmd/bd/BUILD.bazel"), "bd_embedded_test")
		if !strings.Contains(rule, `"BEADS_TEST_BD_BINARY": "$(rlocationpath :bd)"`) {
			t.Errorf("cmd/bd:bd_embedded_test must run the race //cmd/bd:bd as BEADS_TEST_BD_BINARY:\n%s", rule)
		}
	}

	conformance := risk.job(t, "test-embedded-conformance")
	if conformance.Env["BEADS_TEST_EMBEDDED_DOLT"] != "1" {
		t.Error("pr-risk.yml test-embedded-conformance no longer sets BEADS_TEST_EMBEDDED_DOLT=1")
	}
	quoted := regexp.MustCompile(`(-test\.[a-z]+) '([^']*)'|(-test\.[a-z]+=\S+|-test\.v)`)
	for partition, target := range map[string]string{"core": "embeddeddolt_conformance_core_test", "audit": "embeddeddolt_conformance_audit_test"} {
		run := conformance.step(t, "Test "+partition+" conformance").Run
		fields := strings.Fields(run)
		if len(fields) == 0 || fields[0] != "/tmp/embeddeddolt-test" {
			t.Fatalf("pr-risk.yml %s conformance no longer runs /tmp/embeddeddolt-test: %q", partition, run)
		}
		var want []string
		for _, m := range quoted.FindAllStringSubmatch(run, -1) {
			switch {
			case m[1] != "":
				want = append(want, `"`+m[1]+"="+strings.ReplaceAll(m[2], "$", "$$")+`",`)
			case strings.HasPrefix(m[3], "-test.timeout="):
				// Documented deviation: Bazel kills at 1200s without a
				// goroutine dump, so the variant's Go timeout is 19m.
				if m[3] != "-test.timeout=30m" {
					t.Errorf("pr-risk.yml %s conformance timeout is now %q; revisit the variant's -test.timeout=19m", partition, m[3])
				}
				want = append(want, `"-test.timeout=19m",`)
			default:
				want = append(want, `"`+m[3]+`",`)
			}
		}
		if len(want) < 4 {
			t.Fatalf("parsed only %v from pr-risk.yml %s conformance %q", want, partition, run)
		}
		if os.Getenv("TEST_SRCDIR") != "" {
			continue
		}
		rule := bazelRuleBlock(readPolicyFile(t, sourceRepoRoot(t), "internal/storage/embeddeddolt/BUILD.bazel"), target)
		for _, w := range append(want, `"BEADS_TEST_EMBEDDED_DOLT": "1"`, `"embedded"`) {
			if !strings.Contains(rule, w) {
				t.Errorf("embeddeddolt:%s does not contain %q (pr-risk.yml %s conformance):\n%s", target, w, partition, rule)
			}
		}
	}
}

// bazel-pure replaces pr.yml's check-cmd-bd-puregeo-tests job: the same pure
// cmd/bd test selector, the same pure build set, and the js/wasm hook test
// under the same exact-count guard.
func TestBazelPureJobMirrorsPureGoJob(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml").job(t, "check-cmd-bd-puregeo-tests")
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelPureJobName)

	prSubset := pr.step(t, "Run pure-Go cmd/bd test subset (CGO_ENABLED=0)").Run
	m := regexp.MustCompile(`-run '([^']+)' \./cmd/bd`).FindStringSubmatch(prSubset)
	if m == nil {
		t.Fatalf("cannot find the -run selector in pr.yml's pure subset step:\n%s", prSubset)
	}
	if job.Env["PURE_CMD_BD_TESTS"] != m[1] || !strings.Contains(prSubset, "-short") {
		t.Errorf("%s PURE_CMD_BD_TESTS = %q, want pr.yml's -short -run selector %q", bazelPureJobName, job.Env["PURE_CMD_BD_TESTS"], m[1])
	}
	run := job.step(t, "Run pure-Go cmd/bd test subset (--config=pure)").Run
	for _, required := range []string{"bazel test --config=pure " + bazelSoleRunArg + " //cmd/bd:bd_test", `"--test_arg=-test.run=$PURE_CMD_BD_TESTS"`, "(( n > 0 ))"} {
		if !strings.Contains(run, required) {
			t.Errorf("pure subset step does not contain %q:\n%s", required, run)
		}
	}
	if !strings.Contains(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "test:pure --test_arg=-test.short") {
		t.Error(".bazelrc test:pure does not pass -test.short like the pr.yml subset")
	}

	build := job.step(t, "Build cmd/bd and pure-Go test binaries (--config=pure)").Run
	prBuild := pr.step(t, "Build cmd/bd (CGO_ENABLED=0, gms_pure_go)").Run + pr.step(t, "Compile pure-Go test binaries (CGO_ENABLED=0, gms_pure_go)").Run
	for pkg, target := range map[string]string{
		"go build -tags gms_pure_go -o /tmp/bd-puregeo ./cmd/bd":               "//cmd/bd:bd",
		"-o /tmp/bd-cmd-puregeo-test ./cmd/bd":                                 "//cmd/bd:bd_test",
		"-o /tmp/bd-embeddeddolt-puregeo-test ./internal/storage/embeddeddolt": "//internal/storage/embeddeddolt:embeddeddolt_test",
		"-o /tmp/bd-tracker-puregeo-test ./internal/tracker":                   "//internal/tracker:tracker_test",
	} {
		if !strings.Contains(prBuild, pkg) {
			t.Errorf("pr.yml pure build steps no longer contain %q; update %s to match", pkg, bazelPureJobName)
		}
		if !strings.Contains(build, target) || !strings.Contains(build, "bazel build --config=pure") {
			t.Errorf("pure build step does not build %s:\n%s", target, build)
		}
	}

	// `go build` rejects a pure binary that imports gozstd at compile time;
	// Bazel compiles gozstd's stubs, whose init panics, so the lane must start
	// every pure artifact (bd_test starts in the subset step above).
	start := job.step(t, "Start every pure-Go artifact (gozstd contamination check)").Run
	for _, required := range []string{
		"set -euo pipefail",
		"bazel run --config=pure //cmd/bd:bd -- version",
		"bazel test --config=pure",
		"//internal/storage/embeddeddolt:embeddeddolt_test",
		"//internal/tracker:tracker_test",
		"'--test_arg=-test.run=^$'",
	} {
		if !strings.Contains(start, required) {
			t.Errorf("pure artifact start step does not contain %q:\n%s", required, start)
		}
	}
	if os.Getenv("TEST_SRCDIR") == "" {
		patch := readPolicyFile(t, sourceRepoRoot(t), "third_party/patches/gozstd_nocgo.patch")
		if !strings.Contains(patch, "+func init() { panic(") {
			t.Error("gozstd_nocgo.patch stubs no longer panic in init; a contaminated pure binary would start")
		}
	}

	prWasm := pr.step(t, "Run js/wasm hook boundary").Run
	wasm := job.step(t, "Run js/wasm hook boundary").Run
	for _, required := range []string{"bazel build --config=js-wasm //internal/hooks:hooks_test", "go_js_wasm_exec"} {
		if !strings.Contains(wasm, required) {
			t.Errorf("js/wasm step does not contain %q:\n%s", required, wasm)
		}
	}
	// The selector and everything from the count guard on are pr.yml's.
	const selector = "run '^TestRunHookReportsUnsupportedExecution$'"
	if !strings.Contains(prWasm, " -"+selector) || !strings.Contains(wasm, " -test."+selector) {
		t.Errorf("js/wasm selector drifted from pr.yml's %q", selector)
	}
	guard := func(script string) string {
		i := strings.Index(script, "run_count=0")
		if i < 0 {
			return ""
		}
		return script[i:]
	}
	if guard(wasm) == "" || guard(wasm) != guard(prWasm) {
		t.Errorf("js/wasm count guard differs from pr.yml's:\n--- pr.yml\n%s\n--- %s\n%s", guard(prWasm), bazelPureJobName, guard(wasm))
	}
}

// swallowedExit matches shell that hides a failing command's status.
var swallowedExit = regexp.MustCompile(`\bexit\b|\|\|\s*(true|:)|;\s*(true|:)\s*$|\bset\s+\+e\b|\btrap\b`)

// shellCommands joins backslash continuations and drops blank and comment
// lines.
func shellCommands(run string) []string {
	var cmds []string
	cur := ""
	for _, line := range strings.Split(run, "\n") {
		trimmed := strings.TrimSpace(line)
		if cur == "" && (trimmed == "" || strings.HasPrefix(trimmed, "#")) {
			continue
		}
		if strings.HasSuffix(trimmed, "\\") {
			cur += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		cmds = append(cmds, strings.Join(strings.Fields(cur+trimmed), " "))
		cur = ""
	}
	if cur != "" {
		cmds = append(cmds, strings.Join(strings.Fields(cur), " "))
	}
	return cmds
}

// A report step's status is its script's: the script is the last command,
// nothing after it on that line, and nothing in the step can mask a failure.
func assertReportStepKeepsExitStatus(t *testing.T, step ciWorkflowStep, invocation string) {
	t.Helper()
	if step.Shell != "" {
		t.Errorf("step %q overrides shell %q; the default bash -e is part of the contract", step.Name, step.Shell)
	}
	cmds := shellCommands(step.Run)
	if len(cmds) == 0 {
		t.Errorf("step %q runs nothing", step.Name)
		return
	}
	last := cmds[len(cmds)-1]
	if !strings.HasPrefix(last, invocation+" ") || strings.ContainsAny(last, ";&|") {
		t.Errorf("step %q must end with a bare %q so its exit status is the step's; last command %q", step.Name, invocation, last)
	}
	for _, cmd := range cmds {
		if swallowedExit.MatchString(cmd) {
			t.Errorf("step %q command %q can hide the script's exit status", step.Name, cmd)
		}
	}
}

// The bazel test step captures bazel's status through tee and must exit
// with it.
func assertTestStepKeepsExitStatus(t *testing.T, step ciWorkflowStep) {
	t.Helper()
	if step.Shell != "" {
		t.Errorf("step %q overrides shell %q", step.Name, step.Shell)
	}
	cmds := shellCommands(step.Run)
	if len(cmds) == 0 || cmds[len(cmds)-1] != `exit "$rc"` {
		t.Errorf("step %q must end with exit \"$rc\"; commands %q", step.Name, cmds)
	}
	exits := 0
	for _, cmd := range cmds {
		if regexp.MustCompile(`(^|[;&|]\s*)exit\b`).MatchString(cmd) {
			exits++
		}
		if regexp.MustCompile(`\|\|\s*(true|:)|;\s*(true|:)\s*$|\bset\s+\+e\b|\btrap\b`).MatchString(cmd) {
			t.Errorf("step %q command %q can hide bazel's exit status", step.Name, cmd)
		}
	}
	if exits != 1 {
		t.Errorf("step %q has %d exit commands, want only the final exit \"$rc\"", step.Name, exits)
	}
}

func readYAMLNode(t *testing.T, rel string) *yaml.Node {
	t.Helper()
	path := filepath.Join(sourceRepoRoot(t), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("%s: want a YAML mapping document", path)
	}
	return doc.Content[0]
}

// walkYAML calls fn with the dotted path of every mapping key and scalar.
func walkYAML(node *yaml.Node, path string, fn func(path string, key bool, value string)) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			k := node.Content[i].Value
			fn(path+"."+k, true, k)
			walkYAML(node.Content[i+1], path+"."+k, fn)
		}
	case yaml.SequenceNode:
		for i, item := range node.Content {
			walkYAML(item, fmt.Sprintf("%s[%d]", path, i), fn)
		}
	case yaml.ScalarNode:
		fn(path, false, node.Value)
	case yaml.AliasNode:
		if node.Alias != nil {
			walkYAML(node.Alias, path, fn)
		}
	}
}

func yamlMapKeys(node *yaml.Node, key string) []string {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != key {
			continue
		}
		v := node.Content[i+1]
		switch v.Kind {
		case yaml.MappingNode:
			var keys []string
			for j := 0; j < len(v.Content); j += 2 {
				keys = append(keys, v.Content[j].Value)
			}
			return keys
		case yaml.SequenceNode:
			var keys []string
			for _, item := range v.Content {
				keys = append(keys, item.Value)
			}
			return keys
		default:
			return []string{v.Value}
		}
	}
	return nil
}

// bazel.yml hands the RBE credentials to PR-head code on same-repo PRs (an
// accepted, documented risk). Keep that exposure from growing: only the
// listed triggers (never pull_request_target), secrets referenced from the
// setup-bazel step's env and nowhere else (not workflow/job env, run, with or
// if), and no continue-on-error anywhere, so a red lane stays red.
func TestBazelWorkflowSecretsAndFailureSurface(t *testing.T) {
	root := readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName))
	triggers := yamlMapKeys(root, "on")
	sort.Strings(triggers)
	if !reflect.DeepEqual(triggers, bazelWorkflowTriggers) {
		t.Errorf("%s triggers = %v, want exactly %v", bazelWorkflowName, triggers, bazelWorkflowTriggers)
	}

	setupSteps := map[string]bool{}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if name == bazelRBEJobName || name == bazelRBEPrewarmJobName {
			continue // no setup-bazel; rbe-prewarm's secret reads are pinned below
		}
		n := 0
		for i, step := range job.Steps {
			if step.Uses == "./"+setupBazelActionDir {
				setupSteps[fmt.Sprintf(".jobs.%s.steps[%d].env.", name, i)] = true
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s job %s has %d setup-bazel steps, want 1", bazelWorkflowName, name, n)
		}
	}
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	walkYAML(root, "", func(path string, key bool, value string) {
		if key && value == "continue-on-error" {
			// rbe-prewarm's job-level continue-on-error is the one
			// deliberate exception (TestBazelWorkflowJobsAndExecutionMode's
			// comment on the job has the full reasoning): without it, this
			// advisory job's failure would still fail bazel.yml's own run
			// and cascade into pr.yml's ci-gate.
			if path != bazelRBEPrewarmContinueOnErrorPath {
				t.Errorf("%s: %s hides failures from pr.yml's ci-gate", bazelWorkflowName, path)
			}
		}
		if key && (value == "secrets" && strings.HasPrefix(path, ".jobs.")) {
			t.Errorf("%s: %s passes secrets to a called workflow or container", bazelWorkflowName, path)
		}
		if key || !secretRef.MatchString(value) {
			return
		}
		if path == bazelRBESecretPath && value == bazelRBESecretValue {
			return // the rbe job's emptiness test (TestBazelRBEJobDecidesOnce)
		}
		switch {
		case path == bazelRBEPrewarmHasAppPath && value == bazelRBEPrewarmHasAppValue,
			path == bazelRBEPrewarmAppIDPath && value == bazelRBEPrewarmAppIDValue,
			path == bazelRBEPrewarmKeyPath && value == bazelRBEPrewarmKeyValue:
			return // rbe-prewarm's own credential-check/mint step (see their doc comment)
		}
		prefix := path[:strings.LastIndex(path, ".")+1]
		if !setupSteps[prefix] {
			t.Errorf("%s: %s reads secrets (%q); only the setup-bazel step's env may", bazelWorkflowName, path, value)
		}
	})

	action := readYAMLNode(t, filepath.Join(setupBazelActionDir, "action.yml"))
	walkYAML(action, "", func(path string, key bool, value string) {
		if key && value == "continue-on-error" {
			t.Errorf("setup-bazel: %s hides failures", path)
		}
		if !key && secretRef.MatchString(value) {
			t.Errorf("setup-bazel: %s reads secrets directly; the caller passes them in the step env", path)
		}
	})
}

// The execution mode (remote, cache, local, skip) is decided once, by the rbe job,
// so a run cannot mix modes and no lane can drift from the others (review D1
// F1/F5/F6): Dependabot PRs are same-repo but get no secrets, so a condition
// on vars and fork alone sent them to the remote-only path with nothing to
// run it. The job's one step reads nothing but booleans, runs no
// repository code, and its outputs are the workflow_call outputs.
func TestBazelRBEJobDecidesOnce(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelRBEJobName)
	// F3: runs-on picks the *venue* (Blacksmith for trusted same-repo PRs,
	// ubuntu-latest otherwise) - a separate concern from the RBE mode
	// decision (remote/cache/local/skip), which still lives solely in the
	// decide step's outputs below.
	if len(job.Needs) != 0 || job.If != "" || job.RunsOn != wantRBERunsOn || len(job.Env) != 0 {
		t.Errorf("%s: needs %v, if %q, runs-on %q, env %v; want no needs, if or env, on %q",
			bazelRBEJobName, job.Needs, job.If, job.RunsOn, job.Env, wantRBERunsOn)
	}
	wantOutputs := map[string]string{
		"enabled": "${{ steps.decide.outputs.enabled }}",
		"mode":    "${{ steps.decide.outputs.mode }}",
		"tier":    "${{ steps.decide.outputs.tier }}",
	}
	if !reflect.DeepEqual(job.Outputs, wantOutputs) {
		t.Errorf("%s outputs = %v, want %v", bazelRBEJobName, job.Outputs, wantOutputs)
	}
	if len(job.Steps) != 1 {
		t.Fatalf("%s has %d steps, want exactly the decision step", bazelRBEJobName, len(job.Steps))
	}
	step := job.Steps[0]
	wantEnv := map[string]string{
		"RBE_VAR_ON":      "${{ vars.RBE_WEST_WORKERS == 'true' }}",
		"RBE_INPUT_OFF":   "${{ inputs.rbe == 'off' }}",
		"RBE_INPUT_CACHE": "${{ inputs.rbe == 'cache' }}",
		"PULL_REQUEST":    prRiskPullRequestValue,
		"FORK":            "${{ github.event.pull_request.head.repo.fork == true }}",
		"DEPENDABOT":      prRiskDependabotValue,
		"FORK_FARM":       bazelForkFarmValue,
		"HAS_EXECUTOR":    bazelRBESecretValue,
		"PR_NUMBER":       "${{ github.event.pull_request.number }}",
	}
	if step.ID != "decide" || step.Uses != "" || step.Shell != "" || len(step.With) != 0 || !reflect.DeepEqual(step.Env, wantEnv) {
		t.Errorf("%s step: id %q, uses %q, shell %q, with %v, env %v; want id decide, a run step with env %v",
			bazelRBEJobName, step.ID, step.Uses, step.Shell, step.With, step.Env, wantEnv)
	}

	// The workflow_call outputs are the rbe job's, for a caller's gate.
	var doc struct {
		On struct {
			WorkflowCall struct {
				Outputs map[string]struct {
					Value string `yaml:"value"`
				} `yaml:"outputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)), &doc); err != nil {
		t.Fatal(err)
	}
	// (The lanes' result outputs: TestBazelLaneIsGatedAlongsideLegacy.)
	for name, want := range map[string]string{
		"rbe-enabled": "${{ jobs.rbe.outputs.enabled }}",
		"rbe-mode":    "${{ jobs.rbe.outputs.mode }}",
	} {
		if got := doc.On.WorkflowCall.Outputs[name].Value; got != want {
			t.Errorf("workflow_call output %s = %q, want %q", name, got, want)
		}
	}

	// Nothing outside the decision step re-derives the condition.
	rederive := regexp.MustCompile(`(?i)vars\.RBE_WEST_WORKERS|inputs\.rbe\b|inputs\.fork-farm|head\.repo\.fork|github\.actor|dependabot`)
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName)), "", func(path string, key bool, value string) {
		// The decision step itself (its env and, since rbe-fork, its run:
		// the DEPENDABOT fact) is the one place that may.
		if key || !rederive.MatchString(value) || strings.HasPrefix(path, ".jobs."+bazelRBEJobName+".steps[0].env.") ||
			path == ".jobs."+bazelRBEJobName+".steps[0].run" {
			return
		}
		// F3: the rbe job's own runs-on picks a runner venue (Blacksmith vs
		// ubuntu-latest) for trusted same-repo PRs; it shares some of the
		// same trust predicates (github.actor, dependabot) as the mode
		// decision but decides something else entirely (where the decide
		// step runs, not what it decides) - not a re-derivation of mode.
		if path == ".jobs."+bazelRBEJobName+".runs-on" {
			return
		}
		// The checkout opt-in for fork code (TestBazelWorkflowForkFarmInputs
		// pins it): a checkout input, not an execution-mode decision.
		if value == bazelAllowUnsafeCheckout && strings.HasSuffix(path, ".with.allow-unsafe-pr-checkout") {
			return
		}
		t.Errorf("%s: %s re-derives the execution mode (%q); read needs.rbe.outputs instead", bazelWorkflowName, path, value)
	})

	// The dispatch choices: on, cache (fork simulation) and off.
	var dispatch struct {
		On struct {
			WorkflowDispatch struct {
				Inputs map[string]struct {
					Options []string `yaml:"options"`
				} `yaml:"inputs"`
			} `yaml:"workflow_dispatch"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)), &dispatch); err != nil {
		t.Fatal(err)
	}
	if got, want := dispatch.On.WorkflowDispatch.Inputs["rbe"].Options, []string{"on", "cache", "off"}; !reflect.DeepEqual(got, want) {
		t.Errorf("workflow_dispatch rbe options = %v, want %v", got, want)
	}

	// The decision, for the facts GitHub evaluates the env expressions on
	// (its == is case-insensitive; a missing secret reads as ''), and for
	// fork and Dependabot pull_request runs what rbe-fork-mint's /v1/status
	// answers (runBazelRBEDecision's curl stub; "" = unreachable).
	type facts struct {
		rbeVar, rbeInput, secret string
		fork, farm               bool
		pr, dependabot           bool // event pull_request; github.actor dependabot[bot]
		mint                     string
	}
	cases := []struct {
		name                string
		in                  facts
		mode, enabled, tier string
	}{
		{"same-repo PR with secrets", facts{"true", "", "grpcs://x", false, false, true, false, ""}, "remote", "true", ""},
		{"push to main", facts{"true", "", "grpcs://x", false, false, false, false, ""}, "remote", "true", ""},
		{"var in other case", facts{"True", "on", "grpcs://x", false, false, false, false, ""}, "remote", "true", ""},
		// Fork and Dependabot pull_request runs: rbe-fork decides. Open:
		// remote execution with a mint certificate, tier ro or rw. Closed,
		// refusing, unreachable or nonsense: local execution with the
		// read-only cache, as before rbe-fork (rbe=cache simulates them);
		// rbe=off drops the cache too.
		{"fork PR, mint ro", facts{"true", "", "", true, false, true, false, "ro"}, "fork-ro", "true", "ro"},
		{"fork PR, mint rw", facts{"true", "", "", true, false, true, false, "rw"}, "fork-rw", "true", "rw"},
		{"fork PR, mint ro, var unset (forks cannot read it)", facts{"", "", "", true, false, true, false, "ro"}, "fork-ro", "true", "ro"},
		{"fork PR somehow with the secret, mint ro", facts{"true", "", "grpcs://x", true, false, true, false, "ro"}, "fork-ro", "true", "ro"},
		{"Dependabot PR, mint ro", facts{"true", "", "", false, false, true, true, "ro"}, "fork-ro", "true", "ro"},
		{"Dependabot PR, var unset, mint ro", facts{"", "", "", false, false, true, true, "ro"}, "fork-ro", "true", "ro"},
		{"fork PR, mint unreachable", facts{"true", "", "", true, false, true, false, ""}, "cache", "false", ""},
		{"fork PR, mint closed", facts{"true", "", "", true, false, true, false, "closed"}, "cache", "false", ""},
		{"fork PR, rw tier closed", facts{"true", "", "", true, false, true, false, "rw-closed"}, "cache", "false", ""},
		{"fork PR, canary refuses", facts{"true", "", "", true, false, true, false, "canary"}, "cache", "false", ""},
		{"fork PR, mint busy", facts{"true", "", "", true, false, true, false, "busy"}, "cache", "false", ""},
		{"fork PR, mint answers garbage", facts{"true", "", "", true, false, true, false, "garbage"}, "cache", "false", ""},
		{"fork PR, mint answers an unknown tier", facts{"true", "", "", true, false, true, false, "evil"}, "cache", "false", ""},
		{"fork PR, var unset, mint unreachable", facts{"", "", "", true, false, true, false, ""}, "cache", "false", ""},
		{"fork PR, var false, mint unreachable", facts{"false", "", "", true, false, true, false, ""}, "cache", "false", ""},
		{"Dependabot PR, mint unreachable", facts{"true", "", "", false, false, true, true, ""}, "cache", "false", ""},
		{"Dependabot, var unset, mint unreachable", facts{"", "", "", false, false, true, true, ""}, "cache", "false", ""},
		{"dispatch rbe=cache", facts{"true", "cache", "grpcs://x", false, false, false, false, ""}, "cache", "false", ""},
		{"dispatch rbe=cache, var unset", facts{"", "cache", "", false, false, false, false, ""}, "cache", "false", ""},
		{"call rbe=CACHE", facts{"true", "CACHE", "grpcs://x", false, false, true, false, ""}, "cache", "false", ""},
		{"fork PR rbe=cache, mint ro", facts{"true", "cache", "", true, false, true, false, "ro"}, "cache", "false", ""},
		{"dispatch rbe=off", facts{"true", "off", "grpcs://x", false, false, false, false, ""}, "local", "false", ""},
		{"call rbe=OFF", facts{"true", "OFF", "grpcs://x", false, false, true, false, ""}, "local", "false", ""},
		{"fork PR rbe=off, mint ro", facts{"true", "off", "", true, false, true, false, "ro"}, "local", "false", ""},
		{"same-repo, var unset", facts{"", "", "grpcs://x", false, false, true, false, ""}, "skip", "false", ""},
		{"same-repo without the secret", facts{"true", "", "", false, false, true, false, ""}, "cache", "false", ""},
		{"var false", facts{"false", "on", "grpcs://x", false, false, false, false, ""}, "skip", "false", ""},
		// bazel-farm.yml's authorized fork runs: remote or nothing, never
		// a second local run (pr.yml already runs one). Not a pull_request
		// event, so the mint is never asked.
		{"authorized fork farm", facts{"true", "", "grpcs://x", true, true, false, false, "ro"}, "remote", "true", ""},
		{"authorized fork farm, var unset", facts{"", "", "grpcs://x", true, true, false, false, ""}, "skip", "false", ""},
		{"authorized fork farm, no secret", facts{"true", "", "", true, true, false, false, ""}, "skip", "false", ""},
		{"authorized fork farm, rbe=off", facts{"true", "off", "grpcs://x", true, true, false, false, ""}, "local", "false", ""},
		// A fork run that is neither a pull_request nor authorized
		// (pull_request_target without bazel-farm.yml's inputs) stays local.
		{"fork, not authorized, secret", facts{"true", "", "grpcs://x", true, false, false, false, "ro"}, "cache", "false", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{
				"RBE_VAR_ON":      strconv.FormatBool(strings.EqualFold(c.in.rbeVar, "true")),
				"RBE_INPUT_OFF":   strconv.FormatBool(strings.EqualFold(c.in.rbeInput, "off")),
				"RBE_INPUT_CACHE": strconv.FormatBool(strings.EqualFold(c.in.rbeInput, "cache")),
				"PULL_REQUEST":    strconv.FormatBool(c.in.pr),
				"FORK":            strconv.FormatBool(c.in.fork),
				"DEPENDABOT":      strconv.FormatBool(c.in.dependabot),
				"FORK_FARM":       strconv.FormatBool(c.in.farm),
				"HAS_EXECUTOR":    strconv.FormatBool(c.in.secret != ""),
				"PR_NUMBER":       "",
				bazelTestMintEnv:  c.in.mint,
			}
			if c.in.pr || c.in.farm {
				env["PR_NUMBER"] = "7123"
			}
			out, log, err := runBazelRBEDecisionLog(t, step.Run, env)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"mode": c.mode, "enabled": c.enabled, "tier": c.tier}
			if !reflect.DeepEqual(out, want) {
				t.Errorf("outputs = %v, want %v", out, want)
			}
			// The mint is asked exactly for fork and Dependabot
			// pull_request runs not forced local or cache, about this run.
			asked := c.in.pr && (c.in.fork || c.in.dependabot) && c.in.rbeInput == ""
			if got := strings.Count(log, bazelMintStatusURL); got != map[bool]int{false: 0, true: 1}[asked] {
				t.Errorf("mint status asked %d times, want %v:\n%s", got, asked, log)
			}
			if asked && !strings.Contains(log, bazelMintStatusURL+"beads&run=4242&attempt=2&pr=7123") {
				t.Errorf("mint status URL does not name this run:\n%s", log)
			}
			// A closed gate drops the connection: each try gives up in 5 s.
			if asked && !strings.Contains(log, "curl -sS --connect-timeout 5 --max-time 30 ") {
				t.Errorf("mint status asked without --connect-timeout 5 --max-time 30:\n%s", log)
			}
		})
	}
	// A value that is not a boolean fails the job rather than picking a mode.
	if out, err := runBazelRBEDecision(t, step.Run, map[string]string{
		"RBE_VAR_ON": "true", "RBE_INPUT_OFF": "false", "RBE_INPUT_CACHE": "false", "PULL_REQUEST": "false", "FORK": "", "DEPENDABOT": "false", "FORK_FARM": "false", "HAS_EXECUTOR": "true",
	}); err == nil {
		t.Errorf("decision with FORK='' succeeded with %v; want failure", out)
	}
	if out, err := runBazelRBEDecision(t, step.Run, map[string]string{
		"RBE_VAR_ON": "true", "RBE_INPUT_OFF": "false", "PULL_REQUEST": "false", "FORK": "false", "DEPENDABOT": "false", "FORK_FARM": "false", "HAS_EXECUTOR": "true",
	}); err == nil {
		t.Errorf("decision without RBE_INPUT_CACHE succeeded with %v; want failure", out)
	}
	// A fork pull_request run without a sane PR number fails rather than
	// asking the mint about something else.
	for _, n := range []string{"", "0", "12a", "1&repo=gascity", "1234567890"} {
		if out, err := runBazelRBEDecision(t, step.Run, map[string]string{
			"RBE_VAR_ON": "false", "RBE_INPUT_OFF": "false", "RBE_INPUT_CACHE": "false", "PULL_REQUEST": "true", "FORK": "true", "DEPENDABOT": "false",
			"FORK_FARM": "false", "HAS_EXECUTOR": "false", "PR_NUMBER": n, bazelTestMintEnv: "ro",
		}); err == nil {
			t.Errorf("decision with PR_NUMBER %q succeeded with %v; want failure", n, out)
		}
	}
}

// bazelTestMintEnv selects runBazelRBEDecision's curl stub's answer for
// rbe-fork-mint's /v1/status: ro, rw (open, that tier), closed, rw-closed
// (open false; today's mint answers ro instead while rw is off), canary (403), busy (502), garbage, evil (open with a tier
// that is neither), or anything else (connection refused, as while the
// farm's gate is closed or DNS has no rbe-mint yet).
const bazelTestMintEnv = "BAZEL_TEST_MINT"

// bazelTestCurlStub stands in for curl on the decision step's PATH: it logs
// its arguments and prints what the mint would. Its exit status is curl's
// (no -f: HTTP errors still print the body and exit 0).
const bazelTestCurlStub = `#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$BAZEL_TEST_CURL_LOG"
open='"instance": "oss-fork", "endpoint": "grpcs://rbe-fork.ops.gascity.com:8444"'
case "${BAZEL_TEST_MINT:-}" in
ro) echo "{\"open\": true, \"tier\": \"ro\", $open, \"reason\": \"eligible\"}" ;;
rw) echo '{"open": true, "tier": "rw", "instance": "oss", "endpoint": "grpcs://rbe-fork.ops.gascity.com:8444", "reason": "eligible"}' ;;
closed) echo "{\"open\": false, \"tier\": \"ro\", $open, \"reason\": \"rbe-fork is closed\"}" ;;
rw-closed) echo '{"open": false, "tier": "rw", "instance": "oss", "endpoint": "grpcs://rbe-fork.ops.gascity.com:8444", "reason": "the rw tier is closed"}' ;;
canary) echo '{"error": "rbe-fork canary: this PR is not enabled yet"}' ;;
busy) echo '{"error": "mint busy; retry later"}' ;;
garbage) echo '<html>bad gateway</html>' ;;
evil) echo '{"open": true, "tier": "admin", "instance": "", "endpoint": "grpcs://elsewhere:1"}' ;;
*) echo "curl: (7) Failed to connect to rbe-mint.ops.gascity.com port 8444" >&2; exit 7 ;;
esac
`

// runBazelRBEDecision runs the rbe job's step script under bash -e (as
// GitHub's default shell) and returns its $GITHUB_OUTPUT. curl is
// bazelTestCurlStub (env bazelTestMintEnv picks the mint's answer), so no
// test reaches the network.
func runBazelRBEDecision(t *testing.T, script string, env map[string]string) (map[string]string, error) {
	t.Helper()
	out, _, err := runBazelRBEDecisionLog(t, script, env)
	return out, err
}

// runBazelRBEDecisionLog: runBazelRBEDecision, plus the stub's curl log.
func runBazelRBEDecisionLog(t *testing.T, script string, env map[string]string) (map[string]string, string, error) {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	bin := filepath.Join(dir, "bin")
	curlLog := filepath.Join(dir, "curl.log")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(bazelTestCurlStub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "GITHUB_OUTPUT=" + output, "GITHUB_STEP_SUMMARY=" + filepath.Join(dir, "summary"),
		"GITHUB_REPOSITORY=gastownhall/beads", "GITHUB_RUN_ID=4242", "GITHUB_RUN_ATTEMPT=2", "BAZEL_TEST_CURL_LOG=" + curlLog}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	b, err := cmd.CombinedOutput()
	logData, _ := os.ReadFile(curlLog)
	if err != nil {
		return nil, string(logData), fmt.Errorf("%v: %s", err, b)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return nil, string(logData), err
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		out[k] = v
	}
	return out, string(logData), nil
}

// write-bazelrc.sh: no secrets means a local-only rc, plus --config=fork-cache
// with BAZEL_FORK_CACHE=true (fork PRs), a partial set (or the fork cache
// with the secrets) fails, and the full set enables remote-exec with the key
// material and rc outside the workspace and the runner cache.
func TestSetupBazelRCWriter(t *testing.T) {
	bash := requireHostTool(t, "bash")
	script := filepath.Join(sourceRepoRoot(t), setupBazelActionDir, "write-bazelrc.sh")
	pem := func(kind string) string {
		body := "-----BEGIN " + kind + "-----\nMIIBfake" + strings.ReplaceAll(kind, " ", "") + "\nline2\n-----END " + kind + "-----\n"
		return base64.StdEncoding.EncodeToString([]byte(body))
	}
	run := func(t *testing.T, extra ...string) (string, string, string, error) {
		t.Helper()
		dir := t.TempDir()
		workspace := filepath.Join(dir, "ws")
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "github_output")
		secret := filepath.Join(dir, "secret")
		cmd := exec.Command(bash, script)
		cmd.Dir = workspace
		cmd.Env = append([]string{
			"PATH=" + os.Getenv("PATH"),
			"GITHUB_WORKSPACE=" + workspace,
			"GITHUB_OUTPUT=" + out,
			"BAZEL_CI_CACHE_DIR=" + filepath.Join(dir, "cache"),
			"BAZEL_CI_SECRET_DIR=" + secret,
		}, extra...)
		logs, err := cmd.CombinedOutput()
		outputs, _ := os.ReadFile(out)
		rc, _ := os.ReadFile(filepath.Join(secret, "ci.bazelrc"))
		return string(outputs), string(rc), string(logs), err
	}
	executor := "BAZEL_REMOTE_EXECUTOR=grpcs://" + "farm.invalid:443"

	t.Run("no secrets stays local", func(t *testing.T) {
		outputs, rc, logs, err := run(t)
		if err != nil {
			t.Fatalf("err=%v\n%s", err, logs)
		}
		if !strings.Contains(outputs, "remote=false") || strings.Contains(rc, "remote") || strings.Contains(rc, "tls") {
			t.Errorf("outputs=%q rc=%q; want remote=false and no remote flags", outputs, rc)
		}
		if !strings.Contains(rc, "--repository_cache=") || strings.Contains(rc, "--disk_cache=") {
			t.Errorf("local rc = %q; want --repository_cache and no --disk_cache (local runs never save it)", rc)
		}
		// The repo contents cache (extracted repos, never re-verified) would
		// live in the runner cache: off.
		if !strings.Contains(rc, "\ncommon --repo_contents_cache=\n") {
			t.Errorf("rc = %q; want common --repo_contents_cache= (disabled)", rc)
		}
		if !strings.Contains(outputs, "cache=false") || strings.Contains(rc, "fork-cache") {
			t.Errorf("outputs=%q rc=%q; want cache=false and no fork-cache line", outputs, rc)
		}
	})
	t.Run("fork cache without secrets", func(t *testing.T) {
		outputs, rc, logs, err := run(t, "BAZEL_FORK_CACHE=true")
		if err != nil {
			t.Fatalf("err=%v\n%s", err, logs)
		}
		if !strings.Contains(rc, "\nbuild --config=fork-cache\n") {
			t.Errorf("rc = %q; want build --config=fork-cache", rc)
		}
		if strings.Contains(rc, "remote-exec") || strings.Contains(rc, "tls") || strings.Contains(rc, "--remote_") {
			t.Errorf("rc = %q; the fork cache must add nothing but --config=fork-cache", rc)
		}
		if !strings.Contains(outputs, "remote=false") || !strings.Contains(outputs, "cache=true") {
			t.Errorf("outputs = %q, want remote=false and cache=true", outputs)
		}
		if !strings.Contains(logs, "setup-bazel: read-only remote cache (rbe-cache); executing locally") {
			t.Errorf("log lacks the read-only cache notice:\n%s", logs)
		}
	})
	t.Run("rejects fork cache with the secrets", func(t *testing.T) {
		_, rc, logs, err := run(t, "BAZEL_FORK_CACHE=true", executor, "RBE_TLS_CERT="+pem("CERTIFICATE"), "RBE_TLS_KEY="+pem("PRIVATE KEY"))
		if err == nil || !strings.Contains(logs, "mutually exclusive") {
			t.Errorf("err=%v, want failure naming the conflict:\n%s", err, logs)
		}
		if strings.Contains(rc, "remote_executor") || strings.Contains(rc, "fork-cache") {
			t.Errorf("rc = %q after the conflict; want neither remote-exec nor fork-cache", rc)
		}
	})
	for _, bad := range []string{"yes", "false", "1", "TRUE"} {
		t.Run("rejects BAZEL_FORK_CACHE="+bad, func(t *testing.T) {
			if _, _, logs, err := run(t, "BAZEL_FORK_CACHE="+bad); err == nil {
				t.Errorf("want failure, got success:\n%s", logs)
			}
		})
	}
	for name, env := range map[string][]string{
		"executor only":    {executor},
		"cert and key":     {"RBE_TLS_CERT=" + pem("CERTIFICATE"), "RBE_TLS_KEY=" + pem("PRIVATE KEY")},
		"executor no key":  {executor, "RBE_TLS_CERT=" + pem("CERTIFICATE")},
		"cert not pem b64": {executor, "RBE_TLS_CERT=bm90IGEgcGVt", "RBE_TLS_KEY=" + pem("PRIVATE KEY")},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, _, logs, err := run(t, env...); err == nil {
				t.Errorf("want failure, got success:\n%s", logs)
			}
		})
	}
	t.Run("full set enables remote-exec", func(t *testing.T) {
		outputs, rc, logs, err := run(t, executor, "RBE_TLS_CERT="+pem("CERTIFICATE"), "RBE_TLS_KEY="+pem("PRIVATE KEY"))
		if err != nil {
			t.Fatalf("err=%v\n%s", err, logs)
		}
		for _, want := range []string{
			"build:remote-exec --remote_executor=grpcs://farm.invalid:443", "--tls_client_certificate=", "--tls_client_key=",
			"build:remote-exec --noremote_upload_local_results", "build --config=remote-exec",
		} {
			if !strings.Contains(rc, want) {
				t.Errorf("rc lacks %q:\n%s", want, rc)
			}
		}
		if strings.Contains(rc, "--disk_cache") || strings.Contains(rc, "--remote_instance_name") {
			t.Errorf("rc sets --disk_cache or an instance nobody asked for:\n%s", rc)
		}
		if !strings.Contains(outputs, "remote=true") || !strings.Contains(outputs, "cache=false") || strings.Contains(rc, "fork-cache") {
			t.Errorf("outputs = %q, want remote=true, cache=false and no fork-cache in the rc", outputs)
		}
		for _, want := range []string{"::add-mask::farm.invalid\n", "::add-mask::MIIBfakeCERTIFICATE\n", "::add-mask::MIIBfakePRIVATEKEY\n", "::add-mask::line2\n", "::warning title=RBE_INSTANCE not set::"} {
			if !strings.Contains(logs, want) {
				t.Errorf("log lacks %q:\n%s", want, logs)
			}
		}
		if strings.Contains(logs, "::add-mask::-----") {
			t.Errorf("PEM armor lines are masked (they would blank every PEM header in the log):\n%s", logs)
		}
	})
	t.Run("CA and instance", func(t *testing.T) {
		_, rc, logs, err := run(t, executor, "RBE_TLS_CERT="+pem("CERTIFICATE"), "RBE_TLS_KEY="+pem("PRIVATE KEY"),
			"RBE_TLS_CA="+pem("CA CERT"), "RBE_INSTANCE=beads")
		if err != nil {
			t.Fatalf("err=%v\n%s", err, logs)
		}
		for _, want := range []string{"build:remote-exec --tls_certificate=", "build:remote-exec --remote_instance_name=beads"} {
			if !strings.Contains(rc, want) {
				t.Errorf("rc lacks %q:\n%s", want, rc)
			}
		}
		if !strings.Contains(logs, "::add-mask::MIIBfakeCACERT\n") || strings.Contains(logs, "RBE_INSTANCE not set") {
			t.Errorf("want the CA masked and no instance warning:\n%s", logs)
		}
	})
	t.Run("secret dir inside workspace fails", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command(bash, script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_WORKSPACE=" + dir,
			"BAZEL_CI_CACHE_DIR=" + filepath.Join(t.TempDir(), "c"), "BAZEL_CI_SECRET_DIR=" + filepath.Join(dir, "s")}
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("want failure for a secret dir inside the workspace:\n%s", out)
		}
	})
}

const (
	bazelAutofixWorkflowName = "bazel-autofix.yml"
	bazelAutofixPushScript   = "scripts/bazel-autofix-push.sh"
	bazelSyncPatchScript     = "scripts/ci/bazel-sync-patch.sh"
	bazelSyncStepName        = "BUILD files in sync (gazelle, go_srcs, MODULE.bazel)"
)

// bazel.yml's sync step stays red on drift but leaves the allowlisted part of
// the fix as the bazel-sync-patch artifact, from PR-head code that sees no
// secrets; bazel-autofix.yml is the only consumer.
func TestBazelSyncStepPublishesPatch(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	sync := job.step(t, bazelSyncStepName)
	if sync.ID != "sync" {
		t.Errorf("sync step id = %q, want sync", sync.ID)
	}
	patchCmd := "./" + bazelSyncPatchScript + ` "$RUNNER_TEMP/bazel-sync-patch"`
	for _, required := range []string{"make bazel-sync-check || check=$?", patchCmd, "exit 1", `exit "$check"`} {
		if !strings.Contains(sync.Run, required) {
			t.Errorf("sync step does not contain %q:\n%s", required, sync.Run)
		}
	}
	if i, j := strings.Index(sync.Run, patchCmd), strings.Index(sync.Run, "exit 1"); j < i {
		t.Errorf("sync step must write the patch before failing")
	}
	for key, value := range sync.Env {
		if strings.Contains(value, "secrets") {
			t.Errorf("sync step env %s reads secrets; it runs PR-head code", key)
		}
	}
	upload := job.step(t, "Upload BUILD sync patch")
	if upload.If != "${{ always() && steps.sync.outcome == 'failure' && github.event_name != 'pull_request_target' }}" {
		t.Errorf("patch upload if = %q; want it gated on the sync step's failure, never from %s's pull_request_target runs", upload.If, bazelFarmWorkflowName)
	}
	if upload.With["name"] != "bazel-sync-patch" || upload.With["path"] != "${{ runner.temp }}/bazel-sync-patch/" ||
		upload.With["if-no-files-found"] != "ignore" {
		t.Errorf("patch upload with = %v", upload.With)
	}
	if job.stepIndex(t, "Upload BUILD sync patch") != job.stepIndex(t, bazelSyncStepName)+1 {
		t.Errorf("patch upload must directly follow the sync step")
	}
}

// bazel-autofix.yml runs with write permissions via workflow_run, so it must
// never check out or execute PR code: the base branch's checkout, one trusted
// script, the artifact as data, no expressions in run bodies, and the push
// token visible only to the push step.
func TestBazelAutofixWorkflowSecurity(t *testing.T) {
	rel := filepath.Join(".github", "workflows", bazelAutofixWorkflowName)
	root := readYAMLNode(t, rel)

	if got := yamlMapKeys(root, "on"); !reflect.DeepEqual(got, []string{"workflow_run"}) {
		t.Errorf("triggers = %v, want exactly [workflow_run] (never pull_request_target)", got)
	}
	var doc struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
	}
	if err := root.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	// Only "PR" (pr.yml, whose bazel job calls bazel.yml) uploads
	// bazel-sync-patch in a pull_request run: bazel.yml has no pull_request
	// trigger of its own, so a "Bazel" run is never a PR event. Any other
	// trigger would only ever be a no-op run (and, with a shared concurrency
	// group, could get in a real fix's way).
	if want := []string{"PR"}; !reflect.DeepEqual(doc.On.WorkflowRun.Workflows, want) {
		t.Errorf("workflow_run.workflows = %v, want %v", doc.On.WorkflowRun.Workflows, want)
	}
	requireWorkflowProducesArtifact(t, "pr.yml", "PR", "bazel-sync-patch")
	if slices.Contains(yamlMapKeys(readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName)), "on"), "pull_request") {
		t.Errorf("%s has its own pull_request trigger; its PR runs would upload bazel-sync-patch outside the \"PR\" run this workflow watches", bazelWorkflowName)
	}
	if !reflect.DeepEqual(doc.On.WorkflowRun.Types, []string{"completed"}) {
		t.Errorf("workflow_run.types = %v, want [completed]", doc.On.WorkflowRun.Types)
	}
	wantPerms := map[string]string{"contents": "write", "pull-requests": "write", "actions": "read"}
	if !reflect.DeepEqual(doc.Permissions, wantPerms) {
		t.Errorf("permissions = %v, want exactly %v", doc.Permissions, wantPerms)
	}

	workflow := readCIWorkflow(t, bazelAutofixWorkflowName)
	if len(workflow.Jobs) != 1 {
		t.Fatalf("%s has %d jobs, want 1", bazelAutofixWorkflowName, len(workflow.Jobs))
	}
	job := workflow.job(t, "autofix")
	for _, cond := range []string{"github.event.workflow_run.event == 'pull_request'", "github.event.workflow_run.conclusion == 'failure'"} {
		if !strings.Contains(job.If, cond) {
			t.Errorf("job if = %q lacks %q", job.If, cond)
		}
	}
	if job.TimeoutMinutes == 0 {
		t.Error("autofix job has no timeout-minutes")
	}

	var checkouts int
	for _, step := range job.Steps {
		if step.Uses != "" {
			family, sha, _ := strings.Cut(step.Uses, "@")
			if family != "actions/checkout" || sha != checkoutSHA {
				t.Errorf("step %q uses %q; only actions/checkout@%s is allowed", step.Name, step.Uses, checkoutSHA)
			}
			if !reflect.DeepEqual(step.With, map[string]string{"persist-credentials": "false"}) {
				t.Errorf("checkout has with %v; want only persist-credentials: false (base default branch, never a PR ref, no token on disk)", step.With)
			}
			checkouts++
		}
		// Event fields (branch names, commit messages) are attacker text:
		// pass them through env, never interpolate them into a script.
		if strings.Contains(step.Run, "${{") {
			t.Errorf("step %q interpolates an expression into run", step.Name)
		}
		if regexp.MustCompile(`\bgit\s+(checkout|fetch|clone|worktree)\b|\bgh\s+pr\s+checkout\b|\bmake\b|\bgo\s+(run|build|test)\b`).MatchString(step.Run) {
			t.Errorf("step %q fetches or runs code in the workflow itself:\n%s", step.Name, step.Run)
		}
	}
	if checkouts != 1 {
		t.Errorf("want exactly one checkout step, got %d", checkouts)
	}
	push := job.step(t, "Push sync commit or leave apply recipe")
	if strings.TrimSpace(push.Run) != "./"+bazelAutofixPushScript {
		t.Errorf("push step run = %q, want ./%s", push.Run, bazelAutofixPushScript)
	}
	download := job.step(t, "Download BUILD sync patch (if any)")
	if !strings.Contains(download.Run, `select(.name == "bazel-sync-patch")`) ||
		!strings.Contains(download.Run, "bazel-sync.patch bazel-sync-meta.txt -d") {
		t.Errorf("download step must fetch bazel-sync-patch and extract only its two files:\n%s", download.Run)
	}
	for key, want := range map[string]string{
		"HEAD_REPO":          "${{ github.event.workflow_run.head_repository.full_name }}",
		"HEAD_BRANCH":        "${{ github.event.workflow_run.head_branch }}",
		"HEAD_SHA":           "${{ github.event.workflow_run.head_sha }}",
		"GH_TOKEN":           "${{ github.token }}",
		"PUSH_TOKEN":         "${{ secrets.DOCS_AUTOFIX_TOKEN || github.token }}",
		"AUTOFIX_TOKEN_KIND": "${{ secrets.DOCS_AUTOFIX_TOKEN && 'pat' || 'default' }}",
	} {
		if got := push.Env[key]; got != want {
			t.Errorf("push step env %s = %q, want %q", key, got, want)
		}
	}

	// Secrets: only the push step's PUSH_TOKEN / AUTOFIX_TOKEN_KIND, and only
	// the docs autofix token that this workflow shares.
	pushIndex := job.stepIndex(t, push.Name)
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	allowed := map[string]bool{
		fmt.Sprintf(".jobs.autofix.steps[%d].env.PUSH_TOKEN", pushIndex):         true,
		fmt.Sprintf(".jobs.autofix.steps[%d].env.AUTOFIX_TOKEN_KIND", pushIndex): true,
	}
	walkYAML(root, "", func(path string, key bool, value string) {
		if key || !secretRef.MatchString(value) {
			return
		}
		if !allowed[path] {
			t.Errorf("%s: %s reads secrets (%q)", bazelAutofixWorkflowName, path, value)
		}
		if refs := regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`).FindAllStringSubmatch(value, -1); len(refs) == 0 {
			t.Errorf("%s: %s uses a non-literal secrets reference", bazelAutofixWorkflowName, path)
		} else {
			for _, ref := range refs {
				if ref[1] != "DOCS_AUTOFIX_TOKEN" {
					t.Errorf("%s: %s reads secret %s; only DOCS_AUTOFIX_TOKEN is shared with this workflow", bazelAutofixWorkflowName, path, ref[1])
				}
			}
		}
	})
}

// requireWorkflowProducesArtifact: the workflow a workflow_run trigger names
// runs on pull_request and uploads the artifact the autofix job consumes,
// from one of its own jobs or from a local workflow a job calls
// unconditionally (a called workflow's uploads belong to the caller's run,
// under the caller's run id).
func requireWorkflowProducesArtifact(t *testing.T, file, name, artifact string) {
	t.Helper()
	node := readYAMLNode(t, filepath.Join(".github", "workflows", file))
	if got := yamlScalar(node, "name"); got != name {
		t.Errorf("%s name = %q, want %q (the workflow_run trigger names it)", file, got, name)
	}
	if !slices.Contains(yamlMapKeys(node, "on"), "pull_request") {
		t.Errorf("%s has no pull_request trigger; the autofix job only acts on PR runs", file)
	}
	uploads := func(wf ciWorkflow) bool {
		for _, job := range wf.Jobs {
			for _, step := range job.Steps {
				if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && step.With["name"] == artifact {
					return true
				}
			}
		}
		return false
	}
	found := uploads(readCIWorkflow(t, file))
	for _, job := range readCIWorkflow(t, file).Jobs {
		if called, ok := strings.CutPrefix(job.Uses, "./.github/workflows/"); ok && job.If == "" && uploads(readCIWorkflow(t, called)) {
			found = true
		}
	}
	if !found {
		t.Errorf("%s never uploads %s (itself or through a called workflow); the workflow_run trigger on it would be dead", file, artifact)
	}
}

// Both autofix workflows push to the same PR branches. Job-level concurrency
// (skipped runs take no part) in ONE group per head branch, never cancelling:
// they queue instead of racing, and an unrelated completion cannot cancel a
// fix in flight.
func TestAutofixWorkflowsShareConcurrency(t *testing.T) {
	const group = "autofix-${{ github.event.workflow_run.head_repository.full_name }}-${{ github.event.workflow_run.head_branch }}"
	for _, file := range []string{bazelAutofixWorkflowName, "docs-autofix.yml"} {
		root := readYAMLNode(t, filepath.Join(".github", "workflows", file))
		if slices.ContainsFunc(root.Content, func(n *yaml.Node) bool { return n.Value == "concurrency" }) {
			t.Errorf("%s has workflow-level concurrency; it must be on the job so skipped runs take no part", file)
		}
		var doc struct {
			Jobs map[string]struct {
				Concurrency struct {
					Group            string `yaml:"group"`
					CancelInProgress *bool  `yaml:"cancel-in-progress"`
				} `yaml:"concurrency"`
				TimeoutMinutes int              `yaml:"timeout-minutes"`
				Steps          []ciWorkflowStep `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := root.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		job, ok := doc.Jobs["autofix"]
		if !ok || len(doc.Jobs) != 1 {
			t.Fatalf("%s: want exactly one job, autofix", file)
		}
		if job.Concurrency.Group != group {
			t.Errorf("%s job concurrency group = %q, want %q", file, job.Concurrency.Group, group)
		}
		if job.Concurrency.CancelInProgress == nil || *job.Concurrency.CancelInProgress {
			t.Errorf("%s job concurrency must set cancel-in-progress: false", file)
		}
		// A hung run holds the shared, non-cancelling group: bound it.
		if job.TimeoutMinutes != 10 {
			t.Errorf("%s autofix job timeout-minutes = %d, want 10", file, job.TimeoutMinutes)
		}
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["persist-credentials"] != "false" {
				t.Errorf("%s checkout keeps the workflow token in .git/config; set persist-credentials: false", file)
			}
		}
	}
	requireWorkflowProducesArtifact(t, "pr.yml", "PR", "cli-docs-freshness-patch")
}

// The two push scripts share their security-critical functions verbatim, and
// both apply the same hardening.
func TestAutofixScriptsShareGuards(t *testing.T) {
	root := sourceRepoRoot(t)
	bazel := readPolicyFile(t, root, bazelAutofixPushScript)
	docs := readPolicyFile(t, root, "scripts/docs-autofix-push.sh")
	for _, name := range []string{"git_", "validate_patch", "check_staged", "post_or_update_comment", "head_branch_protected"} {
		re := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(name) + `\(\) \{\n.*?^\}\n`)
		a, b := re.FindString(bazel), re.FindString(docs)
		if a == "" || a != b {
			t.Errorf("%s() differs between %s and docs-autofix-push.sh (or is missing)", name, bazelAutofixPushScript)
		}
	}
	validator := regexp.MustCompile(`(?ms)^validate_patch\(\) \{\n.*?^\}\n`).FindString(bazel)
	for _, want := range []string{
		// Any rename/copy header, including legacy "rename old/new".
		`rename |copy |`,
		// Every index line, not only hex ones.
		`grep -E '^index ' "$file" | grep -qvE '^index [0-9a-f]+\.\.[0-9a-f]+( 100644)?$'`,
		`git apply --summary "$file"`,
		`git apply --numstat -z "$file"`,
	} {
		if !strings.Contains(validator, want) {
			t.Errorf("validate_patch lacks %q", want)
		}
	}
	for script, body := range map[string]string{bazelAutofixPushScript: bazel, "scripts/docs-autofix-push.sh": docs} {
		for _, want := range []string{
			`select(.user.login == \"$COMMENT_AUTHOR\" and`,
			`COMMENT_AUTHOR="github-actions[bot]"`,
			`"--force-with-lease=refs/heads/$HEAD_BRANCH:$HEAD_SHA"`,
			`gh api "repos/$BASE_REPO/branches/$enc"`,
			`gh api "repos/$BASE_REPO/rules/branches/$enc"`,
			`(.base.repo.full_name // "") == $base`,
			"export GIT_LFS_SKIP_SMUDGE=1",
			"git -c core.hooksPath=/dev/null",
			// Only the branches the run needs, never a whole-repo clone.
			"git_ init --quiet --bare",
			"fetch --quiet --no-tags --filter=blob:none origin",
			`"+refs/heads/$HEAD_BRANCH:refs/autofix/head"`,
			// The circuit breaker reads the fetched commit and fails closed.
			`if ! HEAD_SUBJECT="$(git_ log -1 --format=%s "$HEAD_SHA")" || [[ "$HEAD_SUBJECT" == "$AUTOFIX_SUBJECT"* ]]; then`,
			`if length == 1 then .[0]`,
			`git_ read-tree "$HEAD_SHA"`,
			"apply --cached",
			`check_staged "$HEAD_SHA"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", script, want)
			}
		}
		// Heredocs hold the human recipe (git apply --index, git push), not
		// commands this script runs.
		code := regexp.MustCompile(`(?ms)<<'?EOF'?\n.*?^EOF\n`).ReplaceAllString(body, "")
		if regexp.MustCompile(`\bgit_?\s+(-c\s+\S+\s+)*(checkout|switch|worktree|restore|reset|stash)\b|apply --index`).MatchString(code) {
			t.Errorf("%s writes the PR tree to disk; apply to the index only", script)
		}
		if strings.Index(code, "if head_branch_protected;") > strings.Index(code, "push --quiet") {
			t.Errorf("%s must check branch protection before pushing", script)
		}
		if regexp.MustCompile(`(?m)^\s*[^#\s][^#\n]*(\bclone\b|/commits/)`).MatchString(code) {
			t.Errorf("%s clones the whole repository or reads commits through the API", script)
		}
		if strings.Contains(code, "fetch") && strings.Count(code, " fetch ") != 1 {
			t.Errorf("%s: want exactly one fetch (head, plus base for attribution)", script)
		}
		// Every git command goes through git_ (no hooks), except the
		// repository-free `git apply --summary/--numstat` of the validator.
		for _, m := range regexp.MustCompile(`(?m)(?:^\s*|[;&|!(]\s*|\$\(\s*)git\s+(\S+)`).FindAllStringSubmatch(code, -1) {
			if m[1] != "apply" && m[1] != "-c" {
				t.Errorf("%s: git %s runs without the git_ wrapper", script, m[1])
			}
		}
		if n, m := strings.Count(code, "git -c "), strings.Count(code, "git -c core.hooksPath=/dev/null "); n != 1 || m != 1 {
			t.Errorf("%s: want exactly one raw `git -c` call, git_'s core.hooksPath=/dev/null; got %d", script, n)
		}
	}
}

// The producer (PR code) and the consumer (base-branch code) must agree on
// the allowlist, or every patch would be refused; the consumer's copy is the
// one that is enforced.
func TestBazelAutofixAllowlistsMatch(t *testing.T) {
	re := regexp.MustCompile(`(?m)^BUILD_FILE_RE='([^']+)'$`)
	caseRe := regexp.MustCompile(`(?m)^\s+(MODULE\.bazel \| MODULE\.bazel\.lock\) return 0 ;;\n\s+third_party/\*\) return 1 ;;)$`)
	want := `^([A-Za-z0-9_+-][A-Za-z0-9_.+-]*/)*BUILD\.bazel$`
	for _, script := range []string{bazelAutofixPushScript, bazelSyncPatchScript} {
		body := readPolicyFile(t, sourceRepoRoot(t), script)
		m := re.FindStringSubmatch(body)
		if m == nil || m[1] != want {
			t.Errorf("%s BUILD_FILE_RE = %v, want %q", script, m, want)
		}
		if !caseRe.MatchString(body) {
			t.Errorf("%s lacks the MODULE.bazel / third_party cases of path_allowed", script)
		}
	}
	compiled := regexp.MustCompile(want)
	for path, ok := range map[string]bool{
		"BUILD.bazel":                       true,
		"cmd/bd/BUILD.bazel":                true,
		"internal/storage/dolt/BUILD.bazel": true,
		"a_b/c-d/e.f/BUILD.bazel":           true,
		".github/BUILD.bazel":               false,
		"a/../BUILD.bazel":                  false,
		"../BUILD.bazel":                    false,
		"/BUILD.bazel":                      false,
		"a//BUILD.bazel":                    false,
		"a/.hidden/BUILD.bazel":             false,
		"BUILD.bazel.go":                    false,
		"xBUILD.bazel":                      false,
		"a/BUILD":                           false,
		"a b/BUILD.bazel":                   false,
		`"a\tb/BUILD.bazel"`:                false,
	} {
		if compiled.MatchString(path) != ok {
			t.Errorf("BUILD_FILE_RE matches %q = %v, want %v", path, !ok, ok)
		}
	}
}

func yamlScalar(node *yaml.Node, key string) string {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1].Value
		}
	}
	return ""
}

// Release builds sign and attest what they build, so they must not restore
// any Actions cache: setup-go's default cache is keyed predictably and falls
// back to the default branch's module and build caches, which are not
// re-verified (a poisoned build cache compiles straight into the binaries).
func TestReleaseWorkflowRestoresNoCache(t *testing.T) {
	workflow := readCIWorkflow(t, "release.yml")
	setupGo := 0
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/cache") || strings.Contains(step.Uses, "/cache@") {
				t.Errorf("release.yml job %q step %q uses %s; release builds must not restore caches", jobName, step.Name, step.Uses)
			}
			if strings.HasPrefix(step.Uses, "actions/setup-go@") {
				setupGo++
				if step.With["cache"] != "false" {
					t.Errorf("release.yml job %q step %q: setup-go must set cache: false (got %q)", jobName, step.Name, step.With["cache"])
				}
			}
		}
	}
	if setupGo == 0 {
		t.Fatal("release.yml has no setup-go step; update this test")
	}
}

// Review F3: the gated Bazel lanes never retry a failing test. The legacy
// jobs they mirror (and, since D2 step 1, replace for the embedded tier) ran
// each test once, so a retry would hide a flaky failure no other job sees.
// Nothing may set --flaky_test_attempts (or --runs_per_test_detects_flakes,
// which reports a failed-then-passed test as FLAKY, not FAILED): not .bazelrc,
// a workflow, setup-bazel's generated rc, or a tools/bazel wrapper; and no
// BUILD file or macro may mark a target flaky = True (Bazel retries those up
// to three times by default).
func TestBazelGatedLanesNeverRetryFlakyTests(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold no other package's BUILD files")
	}
	root := sourceRepoRoot(t)
	retry := regexp.MustCompile(`flaky_test_attempts|runs_per_test_detects_flakes`)
	// Any flaky = other than a literal False/0 (a variable or macro
	// parameter could be True). bazel-embedded also asks Bazel itself
	// (TestBazelEmbeddedQueriesFlakyTargets).
	flakyAttr := regexp.MustCompile(`\bflaky\s*=\s*([^,)\s]+)`)
	checked := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".beads":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil // bazel-* convenience symlinks
		}
		base := d.Name()
		isBuild := base == "BUILD" || base == "BUILD.bazel" || strings.HasSuffix(base, ".bzl")
		isRetrySurface := rel == ".bazelrc" || strings.HasPrefix(rel, ".github"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, filepath.Join("tools", "bazel")+string(filepath.Separator))
		if !isBuild && !isRetrySurface {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		for i, line := range strings.Split(string(data), "\n") {
			code := line
			if j := strings.Index(code, "#"); j >= 0 && (isBuild || rel == ".bazelrc") {
				code = code[:j]
			}
			if retry.MatchString(code) {
				t.Errorf("%s:%d retries failing tests (%q); the gated lanes run each test once", rel, i+1, strings.TrimSpace(line))
			}
			if m := flakyAttr.FindStringSubmatch(code); isBuild && m != nil && m[1] != "False" && m[1] != "0" {
				t.Errorf("%s:%d marks a target flaky (%q); Bazel would retry it", rel, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 10 {
		t.Fatalf("checked only %d files; is the repository root right?", checked)
	}
}

// Every artifact bazel.yml uploads has a fixed name within its run, and a
// job re-run ("Re-run failed jobs", the documented recovery for a Bazel lane
// that hit a remote cache eviction or a flake) is the same workflow run:
// upload-artifact v4+ refuses a second artifact of the same name there
// (409 Conflict) unless overwrite is set, which would turn a passing retry
// red. bazel-test uploads the bd package even after its tests fail.
func TestBazelWorkflowUploadsSurviveJobReruns(t *testing.T) {
	uploads := 0
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				continue
			}
			uploads++
			if step.With["overwrite"] != "true" {
				t.Errorf("%s step %q uploads %q without overwrite: true; a job re-run's upload would fail with 409", name, step.Name, step.With["name"])
			}
		}
	}
	if uploads < 8 {
		t.Errorf("found %d upload steps in %s, want at least 8", uploads, bazelWorkflowName)
	}
}
