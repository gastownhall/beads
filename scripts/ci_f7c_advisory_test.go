package scripts_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// F7c (spec-f7.md §2.4, §4.3): advisory workflows moved onto a same-repo-PR
// Blacksmith runner, gained a shared "upgrade-relevant code" path filter, and
// had their matrices folded (Migration Test Harness 14 -> 3, Cross-Version
// Smoke 6 -> 2). These tests pin the invariants that make those changes safe:
// the path filter is identical where it should be, no historical version or
// scenario was dropped by a fold, no advisory job can read a secret just
// because it now names a Blacksmith label, and the Blacksmith-side setup-go
// seed in main.yml actually exists for the jobs that depend on it.

// advisoryPathFilteredWorkflows are the three workflows that share the
// "upgrade-relevant code" allowlist verbatim, save for each one's own
// workflow-file and script entries (spec-f7.md §2.4).
var advisoryPathFilteredWorkflows = []string{
	"conformance.yml",
	"migration-test.yml",
	"cross-version-smoke.yml",
}

// advisoryPathFilterBase is the shared prefix of the allowlist: any non-test
// Go change or build input. It must appear, in this order, at the start of
// each of advisoryPathFilteredWorkflows' pull_request.paths list.
var advisoryPathFilterBase = []string{
	"**.go",
	"!**_test.go",
	"go.mod",
	"go.sum",
	"Makefile",
	".buildflags",
}

// advisoryPathFilterOwnEntries is each workflow's own file/script additions,
// appended after advisoryPathFilterBase.
var advisoryPathFilterOwnEntries = map[string][]string{
	"conformance.yml": {
		".github/workflows/conformance.yml",
		"scripts/conformance.sh",
		"test/conformance/**",
		// F7c review fix (S3): conformance.sh's own comments name this file
		// as the Tier-1 embedded-Dolt oracle test it runs under
		// BEADS_TEST_EMBEDDED_DOLT=1; it was previously excluded from the
		// filter by the shared base's `!**_test.go` negation.
		"internal/storage/embeddeddolt/conformance_test.go",
		// F7c review fix (S4): TestMain for the whole embeddeddolt package
		// (the fixture conformance_test.go and every sibling test reuse)
		// lives in test_fixture_test.go, not conformance_test.go; it was
		// excluded by the same `!**_test.go` negation with nothing to
		// re-include it until now.
		"internal/storage/embeddeddolt/test_fixture_test.go",
		// Future-proofing: mirrors the existing test/conformance/** re-include.
		"backend/conformance/**",
	},
	"migration-test.yml": {
		".github/workflows/migration-test.yml",
		"scripts/migration-test/**",
		// F7c review fix (S3): the migration harness invokes this script
		// directly, but it lives at scripts/ root, not under
		// scripts/migration-test/, so it was not covered by the filter.
		"scripts/migrate-legacy-to-current.sh",
	},
	"cross-version-smoke.yml": {
		".github/workflows/cross-version-smoke.yml",
		"scripts/upgrade-smoke-test.sh",
	},
}

type pullRequestPaths struct {
	On struct {
		PullRequest struct {
			Paths []string `yaml:"paths"`
		} `yaml:"pull_request"`
	} `yaml:"on"`
}

func readPullRequestPaths(t *testing.T, file string) []string {
	t.Helper()
	var parsed pullRequestPaths
	text := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+file)
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return parsed.On.PullRequest.Paths
}

type pushPaths struct {
	On struct {
		Push struct {
			Paths []string `yaml:"paths"`
		} `yaml:"push"`
	} `yaml:"on"`
}

func readPushPaths(t *testing.T, file string) []string {
	t.Helper()
	var parsed pushPaths
	text := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+file)
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return parsed.On.Push.Paths
}

// TestConformancePushPathsMatchPullRequestPaths pins that conformance.yml's
// push.paths and pull_request.paths stay byte-for-byte identical, in order
// (F7c review fix S1/S3): a change that updates one list but not the other
// would silently create a gap where main's push run and a PR's run disagree
// about what counts as "upgrade-relevant code".
func TestConformancePushPathsMatchPullRequestPaths(t *testing.T) {
	pr := readPullRequestPaths(t, "conformance.yml")
	push := readPushPaths(t, "conformance.yml")
	if !equalStrings(pr, push) {
		t.Errorf("conformance.yml pull_request.paths = %v, push.paths = %v; want identical", pr, push)
	}
}

// TestAdvisoryWorkflowPathFiltersAreIdentical pins that the shared base of
// the "upgrade-relevant code" allowlist is byte-for-byte identical, in the
// same order, across all three workflows it applies to. GitHub Actions has no
// cross-file include for `on:` triggers, so this is the fallback the spec
// explicitly allows: identical literal lists plus a policy test asserting
// they stay identical (spec-f7.md §2.4).
func TestAdvisoryWorkflowPathFiltersAreIdentical(t *testing.T) {
	for _, file := range advisoryPathFilteredWorkflows {
		paths := readPullRequestPaths(t, file)
		if len(paths) < len(advisoryPathFilterBase) {
			t.Fatalf("%s pull_request.paths = %v, too short to hold the shared base %v", file, paths, advisoryPathFilterBase)
		}
		got := paths[:len(advisoryPathFilterBase)]
		for i, want := range advisoryPathFilterBase {
			if got[i] != want {
				t.Errorf("%s pull_request.paths[%d] = %q, want %q (shared base must match byte-for-byte and in order)", file, i, got[i], want)
			}
		}
	}
}

// TestAdvisoryWorkflowPathFiltersCoverOwnInputs pins that each workflow also
// allowlists its own workflow file and the scripts/fixtures it actually
// exercises, so an edit to e.g. scripts/migration-test/** is never silently
// skipped by the filter that was added to cut unrelated-PR load.
func TestAdvisoryWorkflowPathFiltersCoverOwnInputs(t *testing.T) {
	for file, want := range advisoryPathFilterOwnEntries {
		paths := readPullRequestPaths(t, file)
		for _, entry := range want {
			if !contains(paths, entry) {
				t.Errorf("%s pull_request.paths %v does not contain its own entry %q", file, paths, entry)
			}
		}
	}
}

// TestNixBuildDropsPullRequestTriggerNotPushOrDispatch pins the one
// "delete the pull_request trigger" trigger change in F7c: nix-build.yml's
// PR coverage is fully redundant with PR Risk's required test-nix job (which
// runs `nix run .#default` plus `nix flake check -L` on every PR, a superset
// of `nix build .#default`), but push and workflow_dispatch must survive so
// the plain `nix build` path stays covered post-merge.
func TestNixBuildDropsPullRequestTriggerNotPushOrDispatch(t *testing.T) {
	type nixTriggers struct {
		On map[string]any `yaml:"on"`
	}
	var parsed nixTriggers
	text := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/nix-build.yml")
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.On["pull_request"]; ok {
		t.Errorf("nix-build.yml still has a pull_request trigger; PR Risk's test-nix job is a superset (spec-f7.md §2.4)")
	}
	push, ok := parsed.On["push"].(map[string]any)
	if !ok {
		t.Fatalf("nix-build.yml has no push trigger: %+v", parsed.On)
	}
	branches, _ := push["branches"].([]any)
	var branchNames []string
	for _, b := range branches {
		branchNames = append(branchNames, fmt.Sprint(b))
	}
	if !contains(branchNames, "main") {
		t.Errorf("nix-build.yml must still push on main: %+v", push)
	}
	if _, ok := parsed.On["workflow_dispatch"]; !ok {
		t.Errorf("nix-build.yml must still support workflow_dispatch")
	}

	// PR Risk's test-nix must actually be the superset this removal leans on:
	// it has to build the default package AND run the flake checks, not just
	// one of the two.
	prRisk := readCIWorkflow(t, "pr-risk.yml")
	testNix := prRisk.job(t, "test-nix")
	var sawBuild, sawFlakeCheck bool
	for _, step := range testNix.Steps {
		if strings.Contains(step.Run, "nix run .#default") {
			sawBuild = true
		}
		if strings.Contains(step.Run, "nix flake check") {
			sawFlakeCheck = true
		}
	}
	if !sawBuild {
		t.Error("pr-risk.yml's test-nix no longer builds/runs the default package; nix-build.yml's pull_request trigger would need to come back")
	}
	if !sawFlakeCheck {
		t.Error("pr-risk.yml's test-nix no longer runs the flake checks")
	}
}

// --- Migration Test Harness: 14 -> 3 shards, no version dropped -----------

// migrationHarnessOriginalVersions is the pre-F7c 14-leg matrix's version
// list, captured verbatim so a shard rebalance can be checked against it
// without re-deriving it from the (now folded) workflow file.
var migrationHarnessOriginalVersions = []string{
	"v0.9.1", "v0.17.0", "v0.49.6", "v0.50.3", "v0.55.4", "v0.56.1",
	"v0.57.0", "v0.62.0", "v0.63.3", "v1.0.0", "v1.0.1", "v1.1.0",
	"v1.1.2", "v1.2.2",
}

// migrationHarnessDoltRuntimeVersions is the old per-version
// `contains(fromJSON('[...]'), matrix.version)` list that gated the "Install
// Dolt test runtime" step. It must become exactly the dolt-runtime shard.
var migrationHarnessDoltRuntimeVersions = []string{
	"v0.55.4", "v0.56.1", "v0.57.0", "v0.62.0", "v1.0.1", "v1.1.0", "v1.1.2", "v1.2.2",
}

func migrationHarnessShards(t *testing.T) map[string][]string {
	t.Helper()
	workflow := readCIWorkflow(t, "migration-test.yml")
	job := workflow.job(t, "historical-upgrades")
	shards := map[string][]string{}
	for _, leg := range job.Strategy.Matrix.Include {
		shardAny, ok := leg.Extra["shard"]
		if !ok {
			t.Fatalf("migration-test.yml matrix leg %+v has no shard field", leg.Extra)
		}
		shard, ok := shardAny.(string)
		if !ok {
			t.Fatalf("migration-test.yml matrix leg shard = %#v, not a string", shardAny)
		}
		versionsAny, ok := leg.Extra["versions"]
		if !ok {
			t.Fatalf("migration-test.yml shard %q has no versions field", shard)
		}
		versionsJSON, ok := versionsAny.(string)
		if !ok {
			t.Fatalf("migration-test.yml shard %q versions = %#v, not a string", shard, versionsAny)
		}
		var versions []string
		if err := json.Unmarshal([]byte(versionsJSON), &versions); err != nil {
			t.Fatalf("migration-test.yml shard %q versions %q does not parse as a JSON string array: %v", shard, versionsJSON, err)
		}
		if _, dup := shards[shard]; dup {
			t.Fatalf("migration-test.yml declares shard %q more than once", shard)
		}
		shards[shard] = versions
	}
	return shards
}

// TestMigrationHarnessShardsCoverAllHistoricalVersions is the equivalence
// check the fold requires: the union of the 3 shards' version lists must be
// exactly the old 14-version set, with no duplicates and nothing dropped.
func TestMigrationHarnessShardsCoverAllHistoricalVersions(t *testing.T) {
	shards := migrationHarnessShards(t)

	wantShardNames := []string{"src", "pre-dolt", "dolt-runtime"}
	for _, name := range wantShardNames {
		if _, ok := shards[name]; !ok {
			t.Errorf("migration-test.yml is missing shard %q", name)
		}
	}
	if len(shards) != len(wantShardNames) {
		t.Errorf("migration-test.yml has shards %v, want exactly %v", mapKeys(shards), wantShardNames)
	}

	seen := map[string]string{} // version -> owning shard
	var union []string
	for shard, versions := range shards {
		for _, v := range versions {
			if owner, dup := seen[v]; dup {
				t.Errorf("version %s is claimed by both shard %q and shard %q", v, owner, shard)
				continue
			}
			seen[v] = shard
			union = append(union, v)
		}
	}
	sort.Strings(union)
	want := append([]string(nil), migrationHarnessOriginalVersions...)
	sort.Strings(want)
	if !equalStrings(union, want) {
		t.Fatalf("shard union = %v, want exactly the old 14-version set %v", union, want)
	}

	if !equalStrings(sortedCopy(shards["dolt-runtime"]), sortedCopy(migrationHarnessDoltRuntimeVersions)) {
		t.Errorf("dolt-runtime shard = %v, want exactly the old Dolt-runtime contains() list %v", shards["dolt-runtime"], migrationHarnessDoltRuntimeVersions)
	}
	if !equalStrings(shards["src"], []string{"v0.9.1"}) {
		t.Errorf("src shard = %v, want exactly [v0.9.1]", shards["src"])
	}
}

// TestMigrationHarnessLoopExecutesBehaviorally runs the actual "Verify
// explicit historical upgrades" bash against a stub run.sh, proving the
// loop's real behavior instead of its source text (F7c review fix S1/S4/S5,
// closes mutations M1 `exit 0`, M2 `break`, M14 `| head -1`, and pins the
// fail-closed and per-version-timeout fixes the reviewer required):
//   - every version in the shard is attempted even after an earlier one
//     fails (a short-circuiting `exit 0`/`break` would hide the rest);
//   - the step's own exit code reflects any failure;
//   - failures are both ::error:: annotated and written to
//     $GITHUB_STEP_SUMMARY;
//   - an empty or unparsable SHARD_VERSIONS fails closed instead of
//     reporting a false pass, without ever invoking run.sh;
//   - a hung version is bounded by `timeout`, and the loop still reaches the
//     version queued after it.
//
// A bare `./scripts/migration-test/run.sh --version "$HISTORICAL_VERSION"`
// (the pre-fold single-version invocation) must also be gone: if it came
// back, the shard would silently only test one version again.
func TestMigrationHarnessLoopExecutesBehaviorally(t *testing.T) {
	requireHostTool(t, "bash")
	requireHostTool(t, "jq")
	requireHostTool(t, "timeout")

	job := readCIWorkflow(t, "migration-test.yml").job(t, "historical-upgrades")
	step := job.step(t, "Verify explicit historical upgrades")

	if strings.Contains(step.Run, "$HISTORICAL_VERSION") {
		t.Errorf("migration-test.yml's Verify step still references the old single-version $HISTORICAL_VERSION env var")
	}

	writeHarness := func(t *testing.T, runSh string) (dir, summaryFile string) {
		t.Helper()
		dir = t.TempDir()
		scriptDir := filepath.Join(dir, "scripts", "migration-test")
		if err := os.MkdirAll(scriptDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(scriptDir, "run.sh"), []byte(runSh), 0o755); err != nil {
			t.Fatal(err)
		}
		summaryFile = filepath.Join(dir, "summary.md")
		if err := os.WriteFile(summaryFile, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, summaryFile
	}

	baseEnv := func(shardVersions, summaryFile string) []string {
		return append(os.Environ(),
			"CANDIDATE_BIN=./bd",
			"GIT_CONFIG_NOSYSTEM=1",
			"DOLT_BIN=/nonexistent/dolt",
			"SHARD_VERSIONS="+shardVersions,
			"SHARD=test-shard",
			"GITHUB_STEP_SUMMARY="+summaryFile,
		)
	}

	run := func(t *testing.T, script, shardVersions, runSh string) (exitCode int, out, summary string) {
		t.Helper()
		dir, summaryFile := writeHarness(t, runSh)
		cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
		cmd.Dir = dir
		cmd.Env = baseEnv(shardVersions, summaryFile)
		outBytes, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run loop: %v\noutput:\n%s", err, outBytes)
			}
			code = exitErr.ExitCode()
		}
		summaryBytes, readErr := os.ReadFile(summaryFile)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return code, string(outBytes), string(summaryBytes)
	}

	t.Run("every version runs even after an early failure, and the step fails", func(t *testing.T) {
		const stub = `#!/usr/bin/env bash
set -euo pipefail
v=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--version" ]; then v="$2"; fi
  shift
done
echo "stub-ran:$v"
[ "$v" = "v2" ] && exit 1
exit 0
`
		code, out, summary := run(t, step.Run, `["v1","v2","v3"]`, stub)
		for _, v := range []string{"v1", "v2", "v3"} {
			if !strings.Contains(out, "stub-ran:"+v) {
				t.Errorf("version %s never ran; output:\n%s", v, out)
			}
		}
		if code != 1 {
			t.Errorf("exit code = %d, want 1 (v2 failed)", code)
		}
		if !strings.Contains(out, "::error::historical upgrade failed for v2") {
			t.Errorf("missing ::error:: for v2; output:\n%s", out)
		}
		if strings.Contains(out, "::error::historical upgrade failed for v1") || strings.Contains(out, "::error::historical upgrade failed for v3") {
			t.Errorf("v1/v3 must not be reported as failed; output:\n%s", out)
		}
		if !strings.Contains(summary, "v2") {
			t.Errorf("GITHUB_STEP_SUMMARY does not mention the failed version v2:\n%s", summary)
		}
		if strings.Contains(summary, "v1") || strings.Contains(summary, "v3") {
			t.Errorf("GITHUB_STEP_SUMMARY must only list failed versions:\n%s", summary)
		}
	})

	t.Run("all versions pass, step exits 0", func(t *testing.T) {
		const stub = "#!/usr/bin/env bash\nexit 0\n"
		code, out, summary := run(t, step.Run, `["v1","v2"]`, stub)
		if code != 0 {
			t.Errorf("exit code = %d, want 0, output:\n%s", code, out)
		}
		if strings.TrimSpace(summary) != "" {
			t.Errorf("GITHUB_STEP_SUMMARY should be untouched on an all-pass run, got:\n%s", summary)
		}
	})

	for _, badInput := range []string{`[]`, `null`, `not-json`, `"a string, not an array"`} {
		t.Run("fails closed on "+badInput, func(t *testing.T) {
			const stub = "#!/usr/bin/env bash\necho ran >&2\nexit 1\n"
			code, out, _ := run(t, step.Run, badInput, stub)
			if code != 1 {
				t.Errorf("SHARD_VERSIONS=%s: exit code = %d, want 1 (fail closed)", badInput, code)
			}
			if !strings.Contains(out, "::error::shard test-shard resolved to zero versions") {
				t.Errorf("SHARD_VERSIONS=%s: missing the fail-closed ::error::; output:\n%s", badInput, out)
			}
			if strings.Contains(out, "ran") {
				t.Errorf("SHARD_VERSIONS=%s: run.sh must never be invoked when the version list fails closed; output:\n%s", badInput, out)
			}
		})
	}

	t.Run("a hung version is bounded by timeout and does not hide the next version", func(t *testing.T) {
		// Exercise the REAL timeout/kill-after wiring, just at durations a
		// unit test can afford: substitute the pinned 8m/30s for 2s/1s. If a
		// mutation removed the `timeout` wrapper entirely, this substitution
		// is a no-op and the stub's 10s sleep would make this subtest's own
		// assertions fail well before a developer-visible hang, bounded by
		// the 20s context below.
		fastStep := strings.NewReplacer("8m", "2s", "30s", "1s").Replace(step.Run)
		const stub = `#!/usr/bin/env bash
v=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--version" ]; then v="$2"; fi
  shift
done
if [ "$v" = "hangs" ]; then
  sleep 10
  exit 0
fi
echo "stub-ran:$v"
exit 0
`
		dir, summaryFile := writeHarness(t, stub)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", fastStep)
		cmd.Dir = dir
		cmd.Env = baseEnv(`["hangs","after"]`, summaryFile)
		start := time.Now()
		out, err := cmd.CombinedOutput()
		elapsed := time.Since(start)
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("loop did not return within 20s; the timeout wrapper did not bound the hang. output:\n%s", out)
		}
		if elapsed > 8*time.Second {
			t.Errorf("loop took %s to process a version meant to be killed after ~3s; output:\n%s", elapsed, out)
		}
		if _, ok := err.(*exec.ExitError); !ok && err != nil {
			t.Fatalf("run loop: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(string(out), "stub-ran:after") {
			t.Errorf("the version after the hang never ran; output:\n%s", out)
		}
		if !strings.Contains(string(out), "::error::historical upgrade failed for hangs") {
			t.Errorf("the killed version was not reported as failed; output:\n%s", out)
		}
	})
}

// TestMigrationHarnessDoltRuntimeStepGatedByShard pins that the "Install Dolt
// test runtime" step's gate is exactly `matrix.shard == 'dolt-runtime'`, not
// some other formulation that might accidentally also match (or exclude) a
// different shard (F7c review fix S1, closes mutation M3).
func TestMigrationHarnessDoltRuntimeStepGatedByShard(t *testing.T) {
	job := readCIWorkflow(t, "migration-test.yml").job(t, "historical-upgrades")
	step := job.step(t, "Install Dolt test runtime")
	const want = "matrix.shard == 'dolt-runtime'"
	if step.If != want {
		t.Errorf("migration-test.yml's Install Dolt test runtime if = %q, want %q", step.If, want)
	}
}

// TestMigrationHarnessCacheHashMatchesDerivation pins that each shard's
// cache-hash is still sha256(join(",", <shard's per-version cache-identity
// strings>))[:16], computed from the pre-fold 14-entry identity table that
// existed before the 14 -> 3 shard fold (git show
// 362eecc6bf:.github/workflows/migration-test.yml), so a future shard
// rebalance or version-list edit can't silently leave a stale cache key
// behind (F7c review fix S1, closes mutation M5).
func TestMigrationHarnessCacheHashMatchesDerivation(t *testing.T) {
	identities := map[string]string{
		"v0.9.1":  "v0.9.1-source-e3c8554fa2c3e4b9caf7e296e9c8abbe24211a72-go1.25.0-cgo1",
		"v0.17.0": "v0.17.0-d4d08617a324c85b45c9628bc519d659a9ff9c7c37da67aa48727e0af7f19a75",
		"v0.49.6": "v0.49.6-8546dc9a47e11dc31ac2bc9a0224a9c690975e91850932cbb62623053fbb7db8",
		"v0.50.3": "v0.50.3-e94b09e0b6a9324bbc0e81ea36bccaaa42172a926bfedfb389e9a26dedb63184",
		"v0.55.4": "v0.55.4-e0fa25456dd82890230eef17653448a0bf995104c78864be91c5ed84426a5f49",
		"v0.56.1": "v0.56.1-4f9f6cc44465a11613ff529009901eaaf841c6b1f91c15e002b0ecda2015a15c",
		"v0.57.0": "v0.57.0-f8629d5627bed7d25f06f92334addc171d679f9aed9d08c5d42a9684205dc04b",
		"v0.62.0": "v0.62.0-4cca7265b22e5c3ca8d62ab0b9752bec31f68b7f5fa636282a4c7e5454c35535",
		"v0.63.3": "v0.63.3-5f4efd2e010209b3f381dbcd783b2a3a652f50ea72f40ef04c8ba434d408bf9e",
		"v1.0.0":  "v1.0.0-7057db1e92428fcf5c08d5dc6b07ead57e588b262cba78b9a26893d55bd29fdb",
		"v1.0.1":  "v1.0.1-1d2364d5d7083a4634a9e734ca87822fb79c2b6625988f9f791e3376313b1b77",
		"v1.1.0":  "v1.1.0-b0f3dd607c3fb989ee08d0a6854fba80d0402971eb108f9af6170bc14d491a34",
		"v1.1.2":  "v1.1.2-a72d71ed374955dc9f83a0f90b54bd7b6a0016709dd1676ae2e368651ed401c2",
		"v1.2.2":  "v1.2.2-8140098a51d3b81d5548d1c5e6db1a2d9930e5d141efe2a4bff7d079c4d321e8",
	}

	job := readCIWorkflow(t, "migration-test.yml").job(t, "historical-upgrades")
	for _, leg := range job.Strategy.Matrix.Include {
		shard, _ := leg.Extra["shard"].(string)
		versionsJSON, _ := leg.Extra["versions"].(string)
		var versions []string
		if err := json.Unmarshal([]byte(versionsJSON), &versions); err != nil {
			t.Fatalf("shard %q versions %q: %v", shard, versionsJSON, err)
		}
		var parts []string
		for _, v := range versions {
			id, ok := identities[v]
			if !ok {
				t.Fatalf("shard %q version %q has no known pre-fold cache-identity; update the table in this test", shard, v)
			}
			parts = append(parts, id)
		}
		sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
		want := hex.EncodeToString(sum[:])[:16]
		gotAny, ok := leg.Extra["cache-hash"]
		if !ok {
			t.Fatalf("shard %q has no cache-hash field", shard)
		}
		if got := fmt.Sprint(gotAny); got != want {
			t.Errorf("shard %q cache-hash = %q, want sha256(join(\",\", identities))[:16] = %q", shard, got, want)
		}
	}
}

func mapKeys[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func sortedCopy(items []string) []string {
	out := append([]string(nil), items...)
	sort.Strings(out)
	return out
}

// --- Cross-Version Smoke: 6 -> 2 jobs, chunks of 5, no version dropped ----

// ghExtractJQProgram pulls the single-quoted jq program out of a standalone
// `jq -<flags> '<program>'` invocation inside a step's `run:` text, so a test
// can execute the REAL program with `jq` directly instead of asserting on a
// substring of the surrounding bash. The flag group is required (one or
// more) specifically so this does not also match `gh`'s own `--jq
// '[.[].tagName]'` filter, which has no `-c`/`-r` flag of its own between
// "jq" and the quoted program. None of this repo's embedded jq programs
// contain a literal single quote, so a non-greedy single-quote match is
// sufficient.
var jqProgramPattern = regexp.MustCompile(`jq(?: -[A-Za-z]+)+ '([^']*)'`)

func ghExtractJQProgram(t *testing.T, run string) string {
	t.Helper()
	m := jqProgramPattern.FindStringSubmatch(run)
	if m == nil {
		t.Fatalf("no `jq '...'` invocation found in:\n%s", run)
	}
	return m[1]
}

func runJQ(t *testing.T, program string, stdin string) string {
	t.Helper()
	requireHostTool(t, "jq")
	cmd := exec.Command("jq", "-c", program)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jq -c %q <<<%q: %v\n%s", program, stdin, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCrossVersionSmokeChunksEveryResolvedVersion runs the REAL jq programs
// embedded in the "Resolve release versions" and "Compute chunk cache key"
// steps against synthetic version lists, instead of asserting on their
// source text (F7c review fix S1, closes mutation M6 `[range(0; length; 5)
// as $i | .[$i:$i+4]]` off-by-one and M7 jq-slice-dropping-versions
// mutations): every resolved version must appear in exactly one chunk, no
// chunk may exceed 5 versions, and the chunk-key step's join must reproduce
// every version in its chunk, in order, space-separated.
func TestCrossVersionSmokeChunksEveryResolvedVersion(t *testing.T) {
	workflow := readCIWorkflow(t, "cross-version-smoke.yml")

	versionsJob := workflow.job(t, "versions")
	resolve := versionsJob.step(t, "Resolve release versions")
	chunkProgram := ghExtractJQProgram(t, resolve.Run)
	if versionsJob.Outputs["chunks"] == "" {
		t.Errorf("cross-version-smoke.yml's versions job has no chunks output")
	}

	for _, n := range []int{0, 1, 4, 5, 6, 9, 10, 29, 30} {
		versions := make([]string, 0, n)
		for i := 0; i < n; i++ {
			versions = append(versions, fmt.Sprintf("v0.%d.0", i))
		}
		versionsJSON, err := json.Marshal(versions)
		if err != nil {
			t.Fatal(err)
		}
		var chunks [][]string
		if err := json.Unmarshal([]byte(runJQ(t, chunkProgram, string(versionsJSON))), &chunks); err != nil {
			t.Fatalf("n=%d: chunk output did not parse as [][]string: %v", n, err)
		}
		wantChunks := (n + 4) / 5
		if n == 0 {
			wantChunks = 0
		}
		if len(chunks) != wantChunks {
			t.Errorf("n=%d: got %d chunks, want %d", n, len(chunks), wantChunks)
		}
		var flat []string
		for _, c := range chunks {
			if len(c) > 5 {
				t.Errorf("n=%d: chunk %v has more than 5 versions", n, c)
			}
			flat = append(flat, c...)
		}
		if !equalStrings(flat, versions) {
			t.Errorf("n=%d: concatenated chunks = %v, want exactly the resolved version list %v in order", n, flat, versions)
		}
	}

	smokeJob := workflow.job(t, "smoke")
	if !contains(smokeJob.Needs, "versions") {
		t.Errorf("cross-version-smoke.yml's smoke job does not need versions")
	}

	chunkKeyStep := smokeJob.step(t, "Compute chunk cache key")
	joinProgram := ghExtractJQProgram(t, chunkKeyStep.Run)
	for _, chunk := range [][]string{
		{"v1.2.2"},
		{"v1.0.0", "v1.0.1", "v1.1.0", "v1.1.2", "v1.2.2"},
	} {
		chunkJSON, err := json.Marshal(chunk)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Trim(runJQ(t, joinProgram, string(chunkJSON)), `"`)
		want := strings.Join(chunk, " ")
		if got != want {
			t.Errorf("chunk-key join(%v) = %q, want %q", chunk, got, want)
		}
	}

	buildStep := smokeJob.stepIndex(t, "Build candidate binary")
	runStep := smokeJob.stepIndex(t, "Run upgrade smoke tests")
	if buildStep >= runStep {
		t.Errorf("Build candidate binary (index %d) must run before Run upgrade smoke tests (index %d), so the candidate is built once per chunk, not once per version", buildStep, runStep)
	}

	run := smokeJob.step(t, "Run upgrade smoke tests")
	if run.Env["SMOKE_VERSIONS"] != "${{ steps.chunk.outputs.versions }}" {
		t.Errorf("cross-version-smoke.yml's smoke job SMOKE_VERSIONS = %q, want the chunk step's versions output", run.Env["SMOKE_VERSIONS"])
	}
	// Exact match (not a substring check, F7c review fix S1): the whole point
	// of SMOKE_VERSIONS is that upgrade-smoke-test.sh's own loop consumes it;
	// any extra positional arg (e.g. a reintroduced matrix.prev_version) would
	// silently make the script test only one version per chunk again.
	if want := "./scripts/upgrade-smoke-test.sh"; strings.TrimSpace(run.Run) != want {
		t.Errorf("cross-version-smoke.yml's Run upgrade smoke tests run = %q, want exactly %q", run.Run, want)
	}
}

// --- Security: no job gains a secret just by naming a Blacksmith label ----

// blacksmithAdvisoryWorkflows are every workflow file F7c moved a job onto a
// same-repo-PR (or push-only, for main.yml's seed) Blacksmith runner.
var blacksmithAdvisoryWorkflows = []string{
	"conformance.yml",
	"regression.yml",
	"migration-test.yml",
	"cross-version-smoke.yml",
	"docs-mintlify.yml",
	"proxied-local-smoke.yml",
	"main.yml",
}

// TestBlacksmithAdvisoryJobsReadNoSecrets is the security invariant spec-f7.md
// §3 requires: "a new policy test asserts that no job whose runs-on names
// blacksmith- reads secrets.". pr-risk.yml's own no-secrets walk is separate
// and untouched; this one covers the F7c advisory workflows plus main.yml's
// new seed job.
func TestBlacksmithAdvisoryJobsReadNoSecrets(t *testing.T) {
	for _, file := range blacksmithAdvisoryWorkflows {
		workflow := readCIWorkflow(t, file)
		for name, job := range workflow.Jobs {
			if !strings.Contains(job.RunsOn, "blacksmith-") {
				continue
			}
			t.Run(file+"/"+name, func(t *testing.T) {
				if containsSecretRef(fmt.Sprint(job.Env)) {
					t.Errorf("%s job %s's env references secrets.", file, name)
				}
				for _, step := range job.Steps {
					if containsSecretRef(step.Run) {
						t.Errorf("%s job %s step %q's run references secrets.", file, name, step.Name)
					}
					if containsSecretRef(fmt.Sprint(step.Env)) {
						t.Errorf("%s job %s step %q's env references secrets.", file, name, step.Name)
					}
					if containsSecretRef(fmt.Sprint(step.With)) {
						t.Errorf("%s job %s step %q's with references secrets.", file, name, step.Name)
					}
				}
			})
		}
	}
}

func containsSecretRef(s string) bool {
	return strings.Contains(s, "secrets.")
}

// advisoryBlacksmithRunnerJobs pins which F7c advisory job runs on which
// same-repo Blacksmith expression, and what label each falls back to when
// the same-repo/merge_group condition is false. migration-test.yml's
// historical-upgrades job is the one case with a non-ubuntu-latest fallback
// (sameRepoBlacksmith4vcpuNoble, F7c review fix B1): it must still resolve to
// the literal ubuntu-24.04 label, not the usual ubuntu-latest, for forks/
// Dependabot/push.
var advisoryBlacksmithRunnerJobs = []struct {
	file            string
	job             string
	blacksmithLabel string
	fallback        string
}{
	{"conformance.yml", "conformance", "blacksmith-4vcpu-ubuntu-2404", "ubuntu-latest"},
	{"regression.yml", "regression", "blacksmith-4vcpu-ubuntu-2404", "ubuntu-latest"},
	{"migration-test.yml", "historical-upgrades", "blacksmith-4vcpu-ubuntu-2404", "ubuntu-24.04"},
	{"cross-version-smoke.yml", "smoke", "blacksmith-4vcpu-ubuntu-2404", "ubuntu-latest"},
	{"cross-version-smoke.yml", "versions", "blacksmith-2vcpu-ubuntu-2404", "ubuntu-latest"},
	{"docs-mintlify.yml", "docsync", "blacksmith-2vcpu-ubuntu-2404", "ubuntu-latest"},
	{"docs-mintlify.yml", "broken-links", "blacksmith-2vcpu-ubuntu-2404", "ubuntu-latest"},
	{"proxied-local-smoke.yml", "managed-local-smoke", "blacksmith-4vcpu-ubuntu-2404", "ubuntu-latest"},
}

// TestF7cAdvisorySameRepoBlacksmithExpressionSemantics evaluates each F7c
// advisory job's REAL, as-parsed-from-YAML runs-on expression text through
// F7a's shared evalGHExpr/mustEvalGHRunsOn (ci_blacksmith_runner_test.go),
// against a truth table covering every event shape the advisory workflows
// see: merge_group, a same-repo PR, a fork PR, a Dependabot PR, a deleted
// fork head, and push/pull_request_target/workflow_dispatch/schedule.
//
// This replaces an earlier, F7c-local hand-written Go mirror of the same
// boolean logic (a tautology risk the F7a review flagged for its own
// equivalent test: a change to both the mirror and the real expression, in
// the same wrong way, would still pass). Per the coordinator, this test now
// reuses F7a's real evaluator instead of building a second one, and is
// scoped to the F7c advisory jobs specifically; F7a's own
// TestSameRepoBlacksmithExpressionSemantics (ci_blacksmith_runner_test.go)
// covers the shared sameRepoBlacksmith{2,4,8}vcpu consts themselves.
func TestF7cAdvisorySameRepoBlacksmithExpressionSemantics(t *testing.T) {
	const ownRepo = "steveyegge/beads"
	type tc struct {
		name           string
		event          string
		headRepo       string // github.event.pull_request.head.repo.full_name; "" = fork or deleted fork head
		actor          string
		wantBlacksmith bool
	}
	cases := []tc{
		{"same-repo PR, human actor", "pull_request", ownRepo, "alice", true},
		{"merge_group always Blacksmith", "merge_group", "", "", true},
		{"fork PR stays on fallback", "pull_request", "someone-else/beads", "alice", false},
		{"deleted fork head stays on fallback", "pull_request", "", "alice", false},
		{"same-repo PR, dependabot actor stays on fallback", "pull_request", ownRepo, "dependabot[bot]", false},
		{"push stays on fallback", "push", "", "alice", false},
		{"pull_request_target stays on fallback", "pull_request_target", ownRepo, "alice", false},
		{"schedule stays on fallback", "schedule", "", "", false},
		{"workflow_dispatch stays on fallback", "workflow_dispatch", "", "", false},
	}
	for _, j := range advisoryBlacksmithRunnerJobs {
		job := readCIWorkflow(t, j.file).job(t, j.job)
		for _, c := range cases {
			t.Run(j.file+"/"+j.job+"/"+c.name, func(t *testing.T) {
				ctx := map[string]string{
					"github.event_name":                             c.event,
					"github.event.pull_request.head.repo.full_name": c.headRepo,
					"github.repository":                             ownRepo,
					"github.actor":                                  c.actor,
				}
				want := j.fallback
				if c.wantBlacksmith {
					want = j.blacksmithLabel
				}
				if got := mustEvalGHRunsOn(t, job.RunsOn, ctx); got != want {
					t.Errorf("%s/%s real runs-on %q evaluated under %+v = %q, want %q", j.file, j.job, job.RunsOn, c, got, want)
				}
			})
		}
	}
}

// --- Blacksmith cache-visibility precondition (spec-f7.md §2.2 Group B) ---

// blacksmithSetupGoCacheConsumers is every advisory job that restores the
// self-defined `blacksmith-sg-v1-` setup-go cache main.yml's
// blacksmith-setup-go-cache job seeds (B2, F7c implementation report).
var blacksmithSetupGoCacheConsumers = map[string][]string{
	"conformance.yml":         {"conformance"},
	"regression.yml":          {"regression"},
	"migration-test.yml":      {"historical-upgrades"},
	"cross-version-smoke.yml": {"smoke"},
	"docs-mintlify.yml":       {"docsync"},
	"proxied-local-smoke.yml": {"managed-local-smoke"},
}

// blacksmithSetupGoCacheKeyNamespace is the self-defined cache key prefix
// (not setup-go's own implicit key) that the seeder and every consumer share,
// so the "no save in a consumer job" checks below can scope to exactly this
// cache without also flagging an unrelated, legitimately-caching step (e.g.
// migration-test.yml's historical-dolt-* cache, which is a distinct
// restore/save pair of its own - see advisoryBinaryCaches below).
const blacksmithSetupGoCacheKeyNamespace = "blacksmith-sg-v1-"

// TestBlacksmithSetupGoSeedExistsForAdvisoryConsumers pins that main.yml's
// blacksmith-setup-go-cache job exists, restores whatever is already cached,
// unconditionally re-runs every warm-up command (not gated on a cache hit,
// since a stale or partial restore must still self-heal), and then always
// saves - the "always warm, always save" design B2 requires so this job can
// safely be the ONLY writer of the Blacksmith-side setup-go cache every
// advisory consumer below reads from.
func TestBlacksmithSetupGoSeedExistsForAdvisoryConsumers(t *testing.T) {
	job := readCIWorkflow(t, "main.yml").job(t, "blacksmith-setup-go-cache")

	if !strings.Contains(job.RunsOn, "blacksmith-") {
		t.Errorf("main.yml's blacksmith-setup-go-cache runs-on = %q, want a Blacksmith label", job.RunsOn)
	}
	if job.TimeoutMinutes == 0 {
		t.Error("main.yml's blacksmith-setup-go-cache has no timeout-minutes")
	}

	setupGoIndex := -1
	for i, step := range job.Steps {
		if actionFamily(step.Uses) != setupGoActionFamily {
			continue
		}
		setupGoIndex = i
		if step.ID != "setup-go" {
			t.Errorf("main.yml's blacksmith-setup-go-cache setup-go step has id %q, want \"setup-go\"", step.ID)
		}
		// The seeder disables setup-go's OWN implicit cache (which would try
		// to use a GitHub-hosted cache entry Blacksmith can't see) in favor of
		// the explicit, self-keyed restore/save steps below.
		if step.With["cache"] != "false" {
			t.Errorf("main.yml's blacksmith-setup-go-cache setup-go cache = %q, want \"false\" (this job manages its own cache explicitly)", step.With["cache"])
		}
		if step.With["go-version-file"] != "go.mod" {
			t.Errorf("main.yml's blacksmith-setup-go-cache setup-go go-version-file = %q, want go.mod", step.With["go-version-file"])
		}
	}
	if setupGoIndex < 0 {
		t.Fatal("main.yml's blacksmith-setup-go-cache has no actions/setup-go step")
	}

	restore := job.step(t, "Restore Blacksmith setup-go cache")
	if actionFamily(restore.Uses) != cacheRestoreActionFamily {
		t.Errorf("main.yml's blacksmith-setup-go-cache Restore step uses %q, want family %q", restore.Uses, cacheRestoreActionFamily)
	}
	if restore.If != "" {
		t.Errorf("main.yml's blacksmith-setup-go-cache Restore step has if=%q, want unconditional (push-to-main only job, always trusted)", restore.If)
	}
	if !strings.HasPrefix(restore.With["key"], blacksmithSetupGoCacheKeyNamespace) {
		t.Errorf("main.yml's blacksmith-setup-go-cache Restore key = %q, want it to start with %q", restore.With["key"], blacksmithSetupGoCacheKeyNamespace)
	}

	wantWarmups := []string{
		"make build",
		"go test -c -tags regression,gms_pure_go ./tests/regression",
		"go test -c -tags gms_pure_go ./internal/storage/embeddeddolt",
		"go test -c -tags 'gms_pure_go e2e' ./test/conformance",
	}
	for _, want := range wantWarmups {
		found := false
		for i, step := range job.Steps[setupGoIndex+1:] {
			if strings.TrimSpace(step.Run) != want {
				continue
			}
			found = true
			// Unconditional, not gated on a cache hit (F7c review fix B2):
			// the whole point of this job is to keep the cache warm, so it
			// must always repopulate GOCACHE/GOMODCACHE.
			if step.If != "" {
				t.Errorf("main.yml's blacksmith-setup-go-cache step %q (index %d) has if=%q, want it unconditional",
					want, setupGoIndex+1+i, step.If)
			}
		}
		if !found {
			t.Errorf("main.yml's blacksmith-setup-go-cache has no step that runs exactly %q", want)
		}
	}

	save := job.step(t, "Save Blacksmith setup-go cache")
	if actionFamily(save.Uses) != cacheSaveActionFamily {
		t.Errorf("main.yml's blacksmith-setup-go-cache Save step uses %q, want family %q", save.Uses, cacheSaveActionFamily)
	}
	if save.If != "always()" {
		t.Errorf("main.yml's blacksmith-setup-go-cache Save step has if=%q, want \"always()\" (save even if a warm-up step above failed)", save.If)
	}
	if !strings.HasPrefix(save.With["key"], blacksmithSetupGoCacheKeyNamespace) {
		t.Errorf("main.yml's blacksmith-setup-go-cache Save key = %q, want it to start with %q", save.With["key"], blacksmithSetupGoCacheKeyNamespace)
	}
	if save.With["key"] != restore.With["key"] {
		t.Errorf("main.yml's blacksmith-setup-go-cache Save key = %q, Restore key = %q; the seeder must save under the exact key it restores from", save.With["key"], restore.With["key"])
	}
}

// TestBlacksmithSeederGuardedAgainstPullRequest pins main.yml's
// blacksmith-setup-go-cache job `if:` byte-for-byte (F7c review fix S2): this
// job is the ONLY writer every advisory consumer's blacksmith-sg-v1- restore
// trusts, so it must stay push-to-main-only even in the counterfactual where
// main.yml's `on:` trigger set grows a pull_request (or merge_group) entry
// someday. A guard that only checks github.repository (the pre-fix state)
// would not catch that: every same-repo PR also satisfies
// github.repository == 'gastownhall/beads'. The real regression this closes
// is evaluated below via evalGHExpr, not just a string match, so the job is
// also proven actually unreachable under a same-repo pull_request event.
func TestBlacksmithSeederGuardedAgainstPullRequest(t *testing.T) {
	const wantIf = "github.event_name == 'push' && github.ref == 'refs/heads/main' && github.repository == 'gastownhall/beads'"
	job := readCIWorkflow(t, "main.yml").job(t, "blacksmith-setup-go-cache")
	if job.If != wantIf {
		t.Fatalf("main.yml's blacksmith-setup-go-cache if=%q, want exactly %q", job.If, wantIf)
	}

	const ownRepo = "gastownhall/beads"
	cases := []struct {
		name string
		ctx  map[string]string
		want bool
	}{
		{"actual push to main", map[string]string{
			"github.event_name": "push", "github.ref": "refs/heads/main", "github.repository": ownRepo,
		}, true},
		{"same-repo pull_request stays excluded", map[string]string{
			"github.event_name": "pull_request", "github.ref": "refs/pull/1/merge", "github.repository": ownRepo,
			"github.event.pull_request.head.repo.full_name": ownRepo,
		}, false},
		{"merge_group stays excluded", map[string]string{
			"github.event_name": "merge_group", "github.ref": "refs/heads/gh-readonly-queue/main/pr-1", "github.repository": ownRepo,
		}, false},
		{"push to a non-main branch stays excluded", map[string]string{
			"github.event_name": "push", "github.ref": "refs/heads/not-main", "github.repository": ownRepo,
		}, false},
		{"push from a fork stays excluded", map[string]string{
			"github.event_name": "push", "github.ref": "refs/heads/main", "github.repository": "someone-else/beads",
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := evalGHExpr(job.If, c.ctx)
			if err != nil {
				t.Fatalf("evalGHExpr(%q): %v", job.If, err)
			}
			if ghTruthy(got) != c.want {
				t.Errorf("evalGHExpr(%q) under %+v = %#v, want truthy=%v", job.If, c.ctx, got, c.want)
			}
		})
	}
}

// TestMainWorkflowHasNoSameRepoPRReachableTrigger is the direct half of F7c
// review fix S2's "pin the seeder's trust boundary" ask: belt-and-suspenders
// alongside the job-level guard in TestBlacksmithSeederGuardedAgainstPullRequest.
// The guard defuses the vulnerability even if one of these triggers is added
// to main.yml's `on:` block, but this test catches the trigger addition
// itself at the workflow-trust-surface level, so a future edit here gets
// flagged before anyone has to reason about whether the job-level `if:`
// still holds. (A mutation that only edits `on:`, leaving the job-level `if:`
// untouched, would otherwise not fail any other test here.)
func TestMainWorkflowHasNoSameRepoPRReachableTrigger(t *testing.T) {
	type mainTriggers struct {
		On map[string]any `yaml:"on"`
	}
	var parsed mainTriggers
	text := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/main.yml")
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"pull_request", "pull_request_target", "merge_group", "workflow_run"} {
		if _, ok := parsed.On[forbidden]; ok {
			t.Errorf("main.yml's on: has a %q trigger; its blacksmith-setup-go-cache seeder is the ONLY writer every advisory consumer trusts, so this workflow must never become reachable from a same-repo PR/merge-queue event even with the job-level guard as a second layer", forbidden)
		}
	}
	if _, ok := parsed.On["push"]; !ok {
		t.Errorf("main.yml's on: has no push trigger: %+v", parsed.On)
	}
}

// TestAdvisoryBlacksmithConsumersAreCacheRestoreOnly replaces the pre-review
// TestAdvisoryBlacksmithConsumersKeepImplicitSetupGoCache (F7c review fix
// B2): each consumer must disable setup-go's own implicit cache on a
// self-hosted (Blacksmith) runner and restore-only from the self-keyed
// `blacksmith-sg-v1-` namespace, with NO save step in that namespace - only
// main.yml's seeder may ever write it, so a same-repo PR run can read the
// cache but never poison what another PR or main's seeder reads back. The
// "no save" check is scoped to the blacksmith-sg-v1- key namespace
// specifically (not "no actions/cache/save in this job at all"), since
// migration-test.yml's historical-upgrades job keeps its own historical-dolt-*
// cache in a different namespace - governed by its own restore/save-gated
// pair below (advisoryBinaryCaches / F7c review fix X1), not an exemption
// from this one.
func TestAdvisoryBlacksmithConsumersAreCacheRestoreOnly(t *testing.T) {
	for file, jobNames := range blacksmithSetupGoCacheConsumers {
		workflow := readCIWorkflow(t, file)
		for _, jobName := range jobNames {
			t.Run(file+"/"+jobName, func(t *testing.T) {
				job := workflow.job(t, jobName)

				var sawSetupGo, sawRestore bool
				for _, step := range job.Steps {
					switch actionFamily(step.Uses) {
					case setupGoActionFamily:
						sawSetupGo = true
						// F7c review fix N1: fail-closed. Pinned to the
						// "== 'github-hosted'" form (not "!= 'self-hosted'")
						// so an empty/unknown runner.environment value keeps
						// caching OFF instead of turning it on.
						if step.With["cache"] != "${{ runner.environment == 'github-hosted' }}" {
							t.Errorf("%s job %s setup-go cache = %q, want it disabled on self-hosted runners (fail-closed)", file, jobName, step.With["cache"])
						}
					case cacheRestoreActionFamily:
						if strings.HasPrefix(step.With["key"], blacksmithSetupGoCacheKeyNamespace) {
							sawRestore = true
							if !strings.Contains(step.If, "runner.environment == 'self-hosted'") {
								t.Errorf("%s job %s setup-go cache Restore step has if=%q, want it gated on runner.environment == 'self-hosted'", file, jobName, step.If)
							}
						}
					case cacheSaveActionFamily, cacheMonolithicActionFamily:
						if strings.HasPrefix(step.With["key"], blacksmithSetupGoCacheKeyNamespace) {
							t.Errorf("%s job %s has a %s step keyed in the blacksmith-sg-v1- namespace (%q); only main.yml's seeder may save this cache", file, jobName, actionFamily(step.Uses), step.With["key"])
						}
					}
				}
				if !sawSetupGo {
					t.Errorf("%s job %s has no actions/setup-go step", file, jobName)
				}
				if !sawRestore {
					t.Errorf("%s job %s has no blacksmith-sg-v1- cache restore step", file, jobName)
				}
			})
		}
	}
}

// TestBlacksmithSeederCacheKeyIsNotPerCommit pins F7c review fix S5: the
// seeder's save/restore key suffix must be a UTC calendar day
// (steps.cache-date.outputs.today), not github.sha. Keying per commit meant
// every single push to main - whether or not go.sum changed - wrote a new
// multi-GB ~/go/pkg/mod + ~/.cache/go-build entry under this namespace; on a
// cache store with LRU eviction (Blacksmith) that churn risked evicting
// F7a's own beads-go-mod-v2-*/beads-go-build-v2-* entries. Keying per day
// instead caps writes to at most once per calendar day while still picking
// up a go.sum change on the very next push.
func TestBlacksmithSeederCacheKeyIsNotPerCommit(t *testing.T) {
	job := readCIWorkflow(t, "main.yml").job(t, "blacksmith-setup-go-cache")

	dateStep := job.step(t, "Compute cache date")
	if dateStep.ID != "cache-date" {
		t.Errorf("main.yml's blacksmith-setup-go-cache Compute cache date step has id %q, want \"cache-date\"", dateStep.ID)
	}
	if !strings.Contains(dateStep.Run, "date -u") {
		t.Errorf("main.yml's blacksmith-setup-go-cache Compute cache date step run = %q, want it to compute a UTC date", dateStep.Run)
	}

	restore := job.step(t, "Restore Blacksmith setup-go cache")
	save := job.step(t, "Save Blacksmith setup-go cache")
	for name, step := range map[string]ciWorkflowStep{"Restore": restore, "Save": save} {
		key := step.With["key"]
		if strings.Contains(key, "github.sha") {
			t.Errorf("main.yml's blacksmith-setup-go-cache %s step key = %q, must not key per-commit (github.sha) - see F7c review fix S5", name, key)
		}
		if !strings.Contains(key, "steps.cache-date.outputs.today") {
			t.Errorf("main.yml's blacksmith-setup-go-cache %s step key = %q, want it keyed by steps.cache-date.outputs.today", name, key)
		}
	}
}

// TestBlacksmithSetupGoCacheKeysMatchAcrossSeederAndConsumers pins that every
// consumer's restore key/restore-keys are textually identical to main.yml
// seeder's save key (F7c review fix B2, closes a seeder/consumer key-mismatch
// mutation): if a consumer's key format ever drifted from the seeder's (a
// different hash segment order, a missing runner.arch, etc.) the seeder would
// keep writing entries no consumer could ever restore, silently degrading
// every advisory Blacksmith job back to a cold cache.
func TestBlacksmithSetupGoCacheKeysMatchAcrossSeederAndConsumers(t *testing.T) {
	seeder := readCIWorkflow(t, "main.yml").job(t, "blacksmith-setup-go-cache")
	seederSave := seeder.step(t, "Save Blacksmith setup-go cache")
	seederKey := seederSave.With["key"]
	if seederKey == "" {
		t.Fatal("main.yml's blacksmith-setup-go-cache Save step has no key")
	}

	for file, jobNames := range blacksmithSetupGoCacheConsumers {
		workflow := readCIWorkflow(t, file)
		for _, jobName := range jobNames {
			job := workflow.job(t, jobName)
			var restore *ciWorkflowStep
			for i := range job.Steps {
				step := &job.Steps[i]
				if actionFamily(step.Uses) == cacheRestoreActionFamily && strings.HasPrefix(step.With["key"], blacksmithSetupGoCacheKeyNamespace) {
					restore = step
					break
				}
			}
			if restore == nil {
				t.Errorf("%s job %s has no blacksmith-sg-v1- restore step", file, jobName)
				continue
			}
			if restore.With["key"] != seederKey {
				t.Errorf("%s job %s restore key = %q, want it identical to the seeder's save key %q", file, jobName, restore.With["key"], seederKey)
			}
			if !strings.Contains(restore.With["restore-keys"], strings.TrimSuffix(seederKey, "${{ steps.cache-date.outputs.today }}")) {
				t.Errorf("%s job %s restore-keys %q does not contain the seeder's key prefix (without the commit-specific suffix)", file, jobName, restore.With["restore-keys"])
			}
		}
	}
}

// --- Binary caches: restore-always, save only off pull_request ------------

// advisoryBinaryCaches are the per-binary caches F7c review fixes B2 and X1
// converted from a monolithic (auto-saving) actions/cache into an explicit
// restore/save pair, so a same-repo PR can read a previously published
// binary but never publish its own into a cache another run would trust
// unverified. migration-test.yml's historical-dolt-* cache was originally
// exempted here on the theory that scripts/migration-test/lib/binary.sh's
// sha256 verification of every extracted archive made a bare, auto-saving
// actions/cache safe regardless of who wrote it. That exemption was unsound
// (F7c review fix X1): `tar -P` extraction plus lib/binary.sh's `cp -f`
// following a symlink planted inside the archive can redirect the final copy
// to an arbitrary path (e.g. over the checked-out workspace or the candidate
// binary itself) before the checksum check ever runs, so checksum
// verification alone does not make a same-repo-PR-writable cache entry safe
// to trust. historical-dolt-* now gets the same restore-always/save-off-PR
// split as the other two.
var advisoryBinaryCaches = []struct {
	file, job, restoreStep, saveStep, keyPrefix string
	// wantSaveIf is the save step's `if:` condition, required byte-for-byte
	// (F7c review fix S3): a `strings.Contains` check here would pass under
	// e.g. `... || true`, which always evaluates true and silently
	// reintroduces the same-repo-PR poisoning path this whole table exists
	// to close.
	wantSaveIf string
}{
	{"cross-version-smoke.yml", "smoke", "Restore previous release binaries cache", "Save previous release binaries cache", "smoke-binaries-", "github.event_name == 'push' || github.event_name == 'workflow_dispatch'"},
	{"regression.yml", "regression", "Restore baseline binary cache", "Save baseline binary cache", "regression-baseline-", "steps.detect.outputs.run_regression == 'true' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')"},
	{"migration-test.yml", "historical-upgrades", "Restore pinned historical release cache", "Save pinned historical release cache", "historical-dolt-", "github.event_name == 'push' || github.event_name == 'workflow_dispatch'"},
}

// TestAdvisoryBinaryCachesAreRestoreAlwaysSavePRGated pins the restore/save
// split itself (F7c review fix B2): the restore step always runs (modulo the
// job's own pre-existing gate, e.g. regression's run_regression detection),
// the save step additionally requires an exact allow-list of
// `github.event_name == 'push' || github.event_name == 'workflow_dispatch'`
// (F7c review fix, discovered via the S2 sweep: a deny-list of
// `!= 'pull_request'` also admits a hypothetical future merge_group event,
// which each of these three jobs' runs-on expressions already resolve to
// Blacksmith for), both steps key off the same cache, and no monolithic
// (bare) actions/cache step remains for either binary cache - a monolithic
// step would silently reintroduce the auto-save-on-any-PR poisoning path B2
// closes.
func TestAdvisoryBinaryCachesAreRestoreAlwaysSavePRGated(t *testing.T) {
	for _, c := range advisoryBinaryCaches {
		t.Run(c.file, func(t *testing.T) {
			workflow := readCIWorkflow(t, c.file)
			job := workflow.job(t, c.job)

			restore := job.step(t, c.restoreStep)
			if actionFamily(restore.Uses) != cacheRestoreActionFamily {
				t.Errorf("%s job %s step %q uses %q, want family %q", c.file, c.job, c.restoreStep, restore.Uses, cacheRestoreActionFamily)
			}
			if !strings.HasPrefix(restore.With["key"], c.keyPrefix) {
				t.Errorf("%s job %s step %q key = %q, want prefix %q", c.file, c.job, c.restoreStep, restore.With["key"], c.keyPrefix)
			}

			save := job.step(t, c.saveStep)
			if actionFamily(save.Uses) != cacheSaveActionFamily {
				t.Errorf("%s job %s step %q uses %q, want family %q", c.file, c.job, c.saveStep, save.Uses, cacheSaveActionFamily)
			}
			if save.If != c.wantSaveIf {
				t.Errorf("%s job %s step %q has if=%q, want exactly %q", c.file, c.job, c.saveStep, save.If, c.wantSaveIf)
			}
			if save.With["key"] != restore.With["key"] {
				t.Errorf("%s job %s: restore key %q != save key %q", c.file, c.job, restore.With["key"], save.With["key"])
			}

			for _, step := range job.Steps {
				if actionFamily(step.Uses) == cacheMonolithicActionFamily && strings.HasPrefix(step.With["key"], c.keyPrefix) {
					t.Errorf("%s job %s has a monolithic actions/cache step keyed %q; B2 requires an explicit restore/save split here", c.file, c.job, step.With["key"])
				}
			}
		})
	}
}

// TestRegressionStepsAfterDetectAreGated pins that every step after
// regression.yml's "Decide whether to run differential regression tests"
// (id: detect) stays gated on steps.detect.outputs.run_regression == 'true'
// (F7c review fix S1, closes an "ungated regression step" mutation): the
// fold that moved detect-regression-need into this job's own first step
// means a single dropped `if:` would make an inapplicable PR silently pay
// for (or worse, run) the rest of the job instead of skipping it.
func TestRegressionStepsAfterDetectAreGated(t *testing.T) {
	job := readCIWorkflow(t, "regression.yml").job(t, "regression")
	detectIndex := job.stepIndex(t, "Decide whether to run differential regression tests")
	const want = "steps.detect.outputs.run_regression == 'true'"
	for _, step := range job.Steps[detectIndex+1:] {
		if !strings.Contains(step.If, want) {
			t.Errorf("regression.yml step %q has if=%q, want it to contain %q", step.Name, step.If, want)
		}
	}
}

// TestRegressionDetectStepBehavioral runs regression.yml's "Decide whether to
// run differential regression tests" (id: detect) step for real, the same
// way TestMigrationHarnessLoopExecutesBehaviorally exercises the migration
// loop (F7c review fix S1, closes mutation M13: a detect step hardcoded to
// always emit run_regression=false). TestRegressionStepsAfterDetectAreGated
// only pins that steps AFTER detect stay gated on its output; it says nothing
// about whether detect's own logic ever produces "true", so a detect step
// that always reports false would make every downstream gate vacuously
// satisfied while regression silently never ran. This executes the step's
// actual bash against real GITHUB_OUTPUT/GITHUB_EVENT_PATH files and a real
// two-commit git repo for each input that must change the verdict.
func TestRegressionDetectStepBehavioral(t *testing.T) {
	requireHostTool(t, "bash")
	requireHostTool(t, "jq")
	requireHostTool(t, "git")

	job := readCIWorkflow(t, "regression.yml").job(t, "regression")
	step := job.step(t, "Decide whether to run differential regression tests")
	if step.ID != "detect" {
		t.Fatalf("detect step id = %q, want %q", step.ID, "detect")
	}

	// newRepo creates a base commit, then (if any changedFiles are given) a
	// second commit that adds each of them, and returns the repo dir plus
	// both commit SHAs for PR_BASE_SHA/PR_HEAD_SHA.
	newRepo := func(t *testing.T, changedFiles ...string) (dir, base, head string) {
		t.Helper()
		dir = t.TempDir()
		git := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"GIT_CONFIG_NOSYSTEM=1",
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.test",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.test",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		git("init", "-q", "-b", "main")
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "README.md")
		git("commit", "-q", "-m", "base")
		base = git("rev-parse", "HEAD")

		for _, f := range changedFiles {
			full := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git("add", f)
		}
		if len(changedFiles) > 0 {
			git("commit", "-q", "-m", "head")
		}
		head = git("rev-parse", "HEAD")
		return dir, base, head
	}

	writeEvent := func(t *testing.T, dir string, labels ...string) string {
		t.Helper()
		type label struct {
			Name string `json:"name"`
		}
		type prBody struct {
			Labels []label `json:"labels"`
		}
		type event struct {
			PullRequest prBody `json:"pull_request"`
		}
		e := event{PullRequest: prBody{Labels: []label{}}}
		for _, l := range labels {
			e.PullRequest.Labels = append(e.PullRequest.Labels, label{Name: l})
		}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "event.json")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	runDetect := func(t *testing.T, dir, eventPath, eventName, base, head string) (runRegression, reason string) {
		t.Helper()
		outFile := filepath.Join(t.TempDir(), "output")
		if err := os.WriteFile(outFile, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", step.Run)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"EVENT_NAME="+eventName,
			"PR_BASE_SHA="+base,
			"PR_HEAD_SHA="+head,
			"GITHUB_OUTPUT="+outFile,
			"GITHUB_EVENT_PATH="+eventPath,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("detect step: %v\noutput:\n%s", err, out)
		}
		outBytes, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(outBytes), "\n") {
			if v, ok := strings.CutPrefix(line, "run_regression="); ok {
				runRegression = v
			}
			if v, ok := strings.CutPrefix(line, "reason="); ok {
				reason = v
			}
		}
		return runRegression, reason
	}

	cases := []struct {
		name         string
		eventName    string
		labels       []string
		changedFiles []string
		want         string
	}{
		{"push always runs", "push", nil, nil, "true"},
		{"run-regression label forces true", "pull_request", []string{"run-regression"}, []string{"docs/unrelated.md"}, "true"},
		{"skip-regression label forces false", "pull_request", []string{"skip-regression"}, []string{"cmd/bd/x.go"}, "false"},
		{"cmd/bd non-test change runs", "pull_request", nil, []string{"cmd/bd/x.go"}, "true"},
		{"only a cmd/bd test file changed skips", "pull_request", nil, []string{"cmd/bd/x_test.go"}, "false"},
		{"docs-only change skips", "pull_request", nil, []string{"docs/a.md"}, "false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, base, head := newRepo(t, c.changedFiles...)
			eventPath := writeEvent(t, dir, c.labels...)
			got, reason := runDetect(t, dir, eventPath, c.eventName, base, head)
			if got != c.want {
				t.Errorf("run_regression = %q (reason %q), want %q", got, reason, c.want)
			}
		})
	}
}

// --- General sweep: no Blacksmith-reachable advisory job may save a cache --

// generalCacheSweepWorkflows is every F7c advisory workflow plus main.yml
// (the seeder). pr.yml, pr-risk.yml and bazel.yml also run jobs on
// Blacksmith, but their Go-cache topology is already pinned exhaustively,
// job-by-job and step-by-step, by TestGoCacheOwnershipTopology and
// TestBazelWorkflowCacheTopology; those jobs' runs-on expressions also
// depend on a prior job's `needs.rbe.outputs.enabled` output, which the
// minimal evalGHExpr engine below (deliberately scoped to github.*/runner.*
// context lookups) cannot resolve, so folding them into this sweep would
// either silently under-check them or require duplicating that existing
// machinery. This list is every workflow this F7c round can actually edit
// plus the one workflow (main.yml) whose seeder job this round added a new
// guard to.
var generalCacheSweepWorkflows = append(mapKeys(blacksmithSetupGoCacheConsumers), "main.yml")

// blacksmithTrustContexts are the two event shapes a same-repo Blacksmith
// `runs-on` ternary can route onto a `blacksmith-*` label for (F7c review fix
// S2): a trusted same-repo pull_request, and merge_group. runner.environment
// is pinned to "self-hosted" in both, matching what a job actually observes
// once it lands on a Blacksmith runner, so a step's own
// `runner.environment == 'self-hosted'`-gated logic evaluates the same way
// here as it would for real.
var blacksmithTrustContexts = map[string]map[string]string{
	"pull_request": {
		"github.event_name":                             "pull_request",
		"github.event.pull_request.head.repo.full_name": "gastownhall/beads",
		"github.repository":                             "gastownhall/beads",
		"github.actor":                                  "alice",
		"github.ref":                                    "refs/pull/1/merge",
		"runner.environment":                            "self-hosted",
	},
	"merge_group": {
		"github.event_name":  "merge_group",
		"github.repository":  "gastownhall/beads",
		"github.ref":         "refs/heads/gh-readonly-queue/main/pr-1",
		"runner.environment": "self-hosted",
	},
}

// ghExprTruthyDefaultTrue evaluates expr under ctx, treating both an empty
// expr and an evaluation error as truthy/reachable: this sweep's job is to
// catch a missing or wrong save-gate, so an `if:` this minimal evaluator
// cannot parse must fail closed (assume the step/job runs) rather than
// silently skip the jobs or steps that use it.
func ghExprTruthyDefaultTrue(expr string, ctx map[string]string) bool {
	if strings.TrimSpace(expr) == "" {
		return true
	}
	v, err := evalGHExpr(expr, ctx)
	if err != nil {
		return true
	}
	return ghTruthy(v)
}

// resolveRunsOnLabel returns the runner label a job's runs-on resolves to
// under ctx: the literal string itself if it is not a `${{ ... }}`
// expression, the evaluated result if it is and evaluates to a string, or ""
// (never a Blacksmith label) if it cannot be resolved at all.
func resolveRunsOnLabel(runsOn string, ctx map[string]string) string {
	if !strings.Contains(runsOn, "${{") {
		return strings.TrimSpace(runsOn)
	}
	v, err := evalGHExpr(runsOn, ctx)
	if err != nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// TestBlacksmithReachableAdvisoryJobsNeverSaveACache is the general sweep the
// F7c re-review required (S2): rather than a hand-picked table of the caches
// known about so far (B2, X1), evaluate EVERY job in EVERY generalCacheSweep-
// Workflows workflow against both blacksmithTrustContexts, and for every job
// whose runs-on actually resolves to a blacksmith-* label under one of them,
// forbid: a bare (monolithic) actions/cache step; an actions/cache/save step
// that is not gated off (would still run under) that same context; a
// setup-go step whose effective `cache` input is not disabled on a
// self-hosted runner; and a setup-node/setup-python step with any `cache`
// input set at all. This is the mechanism that would have caught N3 (docs
// setup-node cache: npm), N4 (a new bare actions/cache step in a Blacksmith
// job), and - once main.yml's seeder gained its S2 job-level guard - proves
// X1/N6 (main.yml growing a pull_request trigger) still cannot make the
// seeder's always-on save step run on a PR, without any of those needing
// their own bespoke test.
func TestBlacksmithReachableAdvisoryJobsNeverSaveACache(t *testing.T) {
	// Sorted so t.Run subtest names (and therefore failure ordering) are
	// stable regardless of Go's randomized map iteration.
	ctxNames := make([]string, 0, len(blacksmithTrustContexts))
	for name := range blacksmithTrustContexts {
		ctxNames = append(ctxNames, name)
	}
	sort.Strings(ctxNames)

	for _, file := range generalCacheSweepWorkflows {
		workflow := readCIWorkflow(t, file)
		for jobName, job := range workflow.Jobs {
			job := job
			for _, ctxName := range ctxNames {
				ctx := blacksmithTrustContexts[ctxName]
				t.Run(file+"/"+jobName+"/"+ctxName, func(t *testing.T) {
					if !ghExprTruthyDefaultTrue(job.If, ctx) {
						return // job cannot even run under this event
					}
					label := resolveRunsOnLabel(job.RunsOn, ctx)
					if !strings.HasPrefix(label, "blacksmith-") {
						return // doesn't land on Blacksmith under this context
					}

					// Every context under which the job is Blacksmith-reachable
					// must be checked independently (not just the first one
					// found): a save gate like `github.event_name !=
					// 'pull_request'` is false under pull_request but true
					// under merge_group, so a job reachable on Blacksmith
					// under both needs both verified.
					for _, step := range job.Steps {
						family := actionFamily(step.Uses)
						switch family {
						case cacheMonolithicActionFamily:
							t.Errorf("%s job %s step %q uses bare actions/cache on a Blacksmith-reachable job; it auto-saves on any key miss (B2/X1 forbid this - use actions/cache/restore + a non-PR-gated actions/cache/save)", file, jobName, step.Name)
						case cacheSaveActionFamily:
							if ghExprTruthyDefaultTrue(step.If, ctx) {
								t.Errorf("%s job %s step %q (actions/cache/save) has if=%q, which still runs under a same-repo Blacksmith %s event; it must be gated off", file, jobName, step.Name, step.If, ctxName)
							}
						case setupGoActionFamily:
							cacheVal, ok := step.With["cache"]
							wouldCache := true
							if ok {
								if !strings.Contains(cacheVal, "${{") {
									wouldCache = cacheVal == "true"
								} else if v, err := evalGHExpr(cacheVal, ctx); err == nil {
									wouldCache = ghTruthy(v)
								}
							}
							if wouldCache {
								t.Errorf("%s job %s setup-go step %q has cache=%q, which stays enabled on a self-hosted (Blacksmith) runner; it must disable its own implicit cache there", file, jobName, step.Name, cacheVal)
							}
						case setupNodeActionFamily, setupPythonActionFamily:
							if cacheVal, ok := step.With["cache"]; ok && cacheVal != "" {
								t.Errorf("%s job %s step %q sets cache=%q on a Blacksmith-reachable job; setup-node/setup-python's own cache uses a bare actions/cache internally", file, jobName, step.Name, cacheVal)
							}
						}
					}
				})
			}
		}
	}
}
