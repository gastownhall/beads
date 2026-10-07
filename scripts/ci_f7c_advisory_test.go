package scripts_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// F7c (spec-f7.md §2.4, §4.3): advisory workflows moved onto a same-repo-PR
// Blacksmith runner. These tests pin the invariants that make that safe: no
// advisory job can read a secret just because it now names a Blacksmith
// label, and the Blacksmith-side setup-go seed in main.yml actually exists
// for the jobs that depend on it. The path-filtered upgrade suites F7c folded
// (Migration Test Harness, Cross-Version Smoke) and their filter and fold
// checks are gone: those suites run under Bazel (//tests/migration,
// //tests/upgrade_smoke).

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

// --- Security: no job gains a secret just by naming a Blacksmith label ----

// blacksmithAdvisoryWorkflows are every workflow file F7c moved a job onto a
// same-repo-PR (or push-only, for main.yml's seed) Blacksmith runner.
var blacksmithAdvisoryWorkflows = []string{
	"docs-mintlify.yml",
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
// the same-repo/merge_group condition is false.
var advisoryBlacksmithRunnerJobs = []struct {
	file            string
	job             string
	blacksmithLabel string
	fallback        string
}{
	{"docs-mintlify.yml", "broken-links", "blacksmith-2vcpu-ubuntu-2404", "ubuntu-latest"},
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
var blacksmithSetupGoCacheConsumers = map[string][]string{}

// blacksmithSetupGoCacheKeyNamespace is the self-defined cache key prefix
// (not setup-go's own implicit key) that the seeder and every consumer share,
// so the "no save in a consumer job" checks below can scope to exactly this
// cache without also flagging an unrelated, legitimately-caching step in
// another namespace.
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
// specifically (not "no actions/cache/save in this job at all"): a cache in
// another namespace is the general sweep's concern below
// (TestBlacksmithReachableAdvisoryJobsNeverSaveACache), not an exemption
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
