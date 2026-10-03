package scripts_test

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

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
	},
	"migration-test.yml": {
		".github/workflows/migration-test.yml",
		"scripts/migration-test/**",
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

// TestMigrationHarnessLoopsOverEveryShardVersion pins that the "Verify
// explicit historical upgrades" step actually iterates every version in the
// shard (not just the first) and keeps going after a failure, since the
// underlying historical-dolt-upgrade-test.sh uses `set -euo pipefail` and
// aborts at its first failure within one process.
func TestMigrationHarnessLoopsOverEveryShardVersion(t *testing.T) {
	job := readCIWorkflow(t, "migration-test.yml").job(t, "historical-upgrades")
	step := job.step(t, "Verify explicit historical upgrades")

	for _, want := range []string{
		"jq -r '.[]'",
		"for v in ",
		"./scripts/migration-test/run.sh --version \"$v\"",
		"if ! ",
		"failed=1",
	} {
		if !strings.Contains(step.Run, want) {
			t.Errorf("migration-test.yml's historical-upgrades Verify step does not contain %q:\n%s", want, step.Run)
		}
	}
	// A bare `./scripts/migration-test/run.sh --version "$HISTORICAL_VERSION"`
	// (the pre-fold single-version invocation) must be gone: if it came back,
	// the shard would silently only test one version again.
	if strings.Contains(step.Run, "$HISTORICAL_VERSION") {
		t.Errorf("migration-test.yml's Verify step still references the old single-version $HISTORICAL_VERSION env var")
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

// TestCrossVersionSmokeChunksEveryResolvedVersion pins the chunking
// machinery itself: `versions` must chunk its full resolved list into groups
// of 5 (not drop a remainder group), and `smoke` must build once per chunk
// and feed every version in the chunk to upgrade-smoke-test.sh's own
// SMOKE_VERSIONS loop (which already self-reinvokes per version and
// collects failures, so a bad version in a chunk cannot hide the rest).
func TestCrossVersionSmokeChunksEveryResolvedVersion(t *testing.T) {
	workflow := readCIWorkflow(t, "cross-version-smoke.yml")

	versionsJob := workflow.job(t, "versions")
	resolve := versionsJob.step(t, "Resolve release versions")
	if !strings.Contains(resolve.Run, "range(0; length; 5)") {
		t.Errorf("cross-version-smoke.yml's versions job does not chunk into groups of 5:\n%s", resolve.Run)
	}
	if !strings.Contains(resolve.Run, "chunks=") {
		t.Errorf("cross-version-smoke.yml's versions job does not emit a chunks output:\n%s", resolve.Run)
	}
	if versionsJob.Outputs["chunks"] == "" {
		t.Errorf("cross-version-smoke.yml's versions job has no chunks output")
	}

	smokeJob := workflow.job(t, "smoke")
	if !contains(smokeJob.Needs, "versions") {
		t.Errorf("cross-version-smoke.yml's smoke job does not need versions")
	}

	chunkKeyStep := smokeJob.step(t, "Compute chunk cache key")
	if !strings.Contains(chunkKeyStep.Run, "join(\" \")") {
		t.Errorf("cross-version-smoke.yml's chunk-key step does not space-join the chunk's versions for SMOKE_VERSIONS:\n%s", chunkKeyStep.Run)
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
	// A bare version positional arg (the pre-fold single-version invocation)
	// must be gone: if it came back, only one version per chunk would run.
	if strings.Contains(run.Run, "matrix.prev_version") {
		t.Errorf("cross-version-smoke.yml's smoke job still references the old per-version matrix.prev_version")
	}

	cacheStep := smokeJob.step(t, "Cache previous release binaries")
	if !strings.Contains(cacheStep.With["key"], "steps.chunk.outputs.hash") {
		t.Errorf("cross-version-smoke.yml's cache key = %q, want it keyed on the chunk hash, not a single version", cacheStep.With["key"])
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

// TestSameRepoBlacksmithExpressionSemantics is a literal (deliberately
// un-clever) mirror of the sameRepoBlacksmith2vcpu/4vcpu expression's boolean
// logic, run against a truth table covering every event shape the F7c
// advisory workflows see: merge_group, a same-repo PR, a fork PR, a
// Dependabot PR, and push/workflow_dispatch/schedule. It exists so a change
// to the real formula's semantics has to be made in both places before this
// test goes green again, and it is the "fork runner selection" coverage
// spec-f7.md §4.3 asks for.
func TestSameRepoBlacksmithExpressionSemantics(t *testing.T) {
	const label = "blacksmith-4vcpu-ubuntu-2404"
	eval := func(eventName, actor string, headRepoEqualsBase bool) string {
		sameRepoPR := eventName == "pull_request" && headRepoEqualsBase && actor != "dependabot[bot]"
		if eventName == "merge_group" || sameRepoPR {
			return label
		}
		return "ubuntu-latest"
	}

	cases := []struct {
		name               string
		eventName          string
		actor              string
		headRepoEqualsBase bool
		want               string
	}{
		{"merge_group always Blacksmith", "merge_group", "someone", false, label},
		{"same-repo PR, human actor", "pull_request", "alice", true, label},
		{"same-repo PR, dependabot actor stays ubuntu-latest", "pull_request", "dependabot[bot]", true, "ubuntu-latest"},
		{"fork PR, human actor stays ubuntu-latest", "pull_request", "alice", false, "ubuntu-latest"},
		{"fork PR, dependabot actor stays ubuntu-latest", "pull_request", "dependabot[bot]", false, "ubuntu-latest"},
		{"push stays ubuntu-latest", "push", "alice", true, "ubuntu-latest"},
		{"workflow_dispatch stays ubuntu-latest", "workflow_dispatch", "alice", true, "ubuntu-latest"},
		{"schedule stays ubuntu-latest", "schedule", "alice", true, "ubuntu-latest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eval(c.eventName, c.actor, c.headRepoEqualsBase); got != c.want {
				t.Errorf("eval(%q, %q, %v) = %q, want %q", c.eventName, c.actor, c.headRepoEqualsBase, got, c.want)
			}
		})
	}

	// Pin that sameRepoBlacksmith4vcpu actually contains this exact boolean
	// structure (not just a same-shaped-but-differently-wired formula).
	for _, want := range []string{
		"github.event_name == 'merge_group'",
		"github.event_name == 'pull_request'",
		"github.event.pull_request.head.repo.full_name == github.repository",
		"github.actor != 'dependabot[bot]'",
		"'" + label + "'",
		"'ubuntu-latest'",
	} {
		if !strings.Contains(sameRepoBlacksmith4vcpu, want) {
			t.Errorf("sameRepoBlacksmith4vcpu does not contain %q", want)
		}
	}
}

// --- Blacksmith cache-visibility precondition (spec-f7.md §2.2 Group B) ---

// TestBlacksmithSetupGoSeedExistsForAdvisoryConsumers pins that main.yml's
// blacksmith-setup-go-cache job exists and actually warms the setup-go
// implicit cache that Conformance, Regression (PR), Migration Test Harness,
// Cross-Version Smoke and Docs docsync now depend on when they run on
// Blacksmith for a same-repo PR. Blacksmith cannot see GitHub-saved caches,
// so without this push-only seed those jobs' first Blacksmith run (and every
// run after a go.sum change) would silently lose setup-go's module/build
// cache rather than restore it.
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
		if step.Uses != "" && strings.HasPrefix(step.Uses, "actions/setup-go@") {
			setupGoIndex = i
			if step.ID != "setup-go" {
				t.Errorf("main.yml's blacksmith-setup-go-cache setup-go step has id %q, want \"setup-go\"", step.ID)
			}
			if step.With["cache"] == "false" {
				t.Error("main.yml's blacksmith-setup-go-cache disables setup-go's cache; it exists to warm that exact cache")
			}
			if step.With["go-version-file"] != "go.mod" {
				t.Errorf("main.yml's blacksmith-setup-go-cache setup-go go-version-file = %q, want go.mod", step.With["go-version-file"])
			}
		}
	}
	if setupGoIndex < 0 {
		t.Fatal("main.yml's blacksmith-setup-go-cache has no actions/setup-go step")
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
			if step.If != "steps.setup-go.outputs.cache-hit != 'true'" {
				t.Errorf("main.yml's blacksmith-setup-go-cache step %q (index %d) has if=%q, want it gated on a cache miss",
					want, setupGoIndex+1+i, step.If)
			}
		}
		if !found {
			t.Errorf("main.yml's blacksmith-setup-go-cache has no step that runs exactly %q", want)
		}
	}
}

// TestAdvisoryBlacksmithConsumersKeepImplicitSetupGoCache pins the other half
// of the cache-visibility precondition: none of the advisory jobs that moved
// onto Blacksmith and rely on the main.yml seed above may turn off setup-go's
// implicit cache (which would silently make the seed a no-op for them).
func TestAdvisoryBlacksmithConsumersKeepImplicitSetupGoCache(t *testing.T) {
	consumers := map[string][]string{
		"conformance.yml":         {"conformance"},
		"regression.yml":          {"regression"},
		"migration-test.yml":      {"historical-upgrades"},
		"cross-version-smoke.yml": {"smoke"},
		"docs-mintlify.yml":       {"docsync"},
		"proxied-local-smoke.yml": {"managed-local-smoke"},
	}
	for file, jobNames := range consumers {
		workflow := readCIWorkflow(t, file)
		for _, jobName := range jobNames {
			job := workflow.job(t, jobName)
			for _, step := range job.Steps {
				if step.Uses == "" || !strings.HasPrefix(step.Uses, "actions/setup-go@") {
					continue
				}
				if step.With["cache"] == "false" {
					t.Errorf("%s job %s disables setup-go's implicit cache; it is the only consumer the Blacksmith seed exists for", file, jobName)
				}
			}
		}
	}
}
