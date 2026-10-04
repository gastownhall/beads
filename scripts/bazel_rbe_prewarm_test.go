package scripts_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This file exists alongside ci_workflow_test.go's broader structural checks
// (TestBazelWorkflowJobsAndExecutionMode, TestBazelWorkflowSecretsAndFailureSurface,
// TestBazelWorkflowActionsArePinned, TestBazelRBEJobDecidesOnce,
// TestBazelGateSimulation, TestBazelCacheModeReachesTheRC) to pin, by name,
// the specific invariants engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md's "rbe-west
// Pre-warm" section promises a reader: the job never runs for an untrusted
// fork or on a mode rbe-worker-pool.yml cannot serve, its credentials are
// never readable outside its own steps, it is never required by pr.yml's
// gate or a `needs` of any lane, it cannot fail the run, and its dispatch is
// pinned to exactly gastownhall/gascity's rbe-worker-pool.yml on main with a
// token scoped to that one repository.
//
// Each test below was mutation-tested by hand while this job was written:
// reverting the fix it pins (restoring the old event-gated `if:`, widening
// the mint's `repositories:`, dropping the job-level continue-on-error,
// adding rbe-prewarm to bazel-gate.sh's skip/aggregate logic or to another
// job's `needs:`) reliably fails the corresponding test here. See this
// task's notes file for the transcript.

// TestRBEPrewarmIfOnlyRemoteAndForkRW runs the job's real, pinned `if:`
// expression (bazelRBEPrewarmIf, via the shared evalGHExpr from
// ci_blacksmith_runner_test.go, not a hand-written mirror) against every mode
// the rbe job can produce. Only remote and fork-rw must schedule the job:
// fork-ro targets rbe-west's separate, uncached "oss-fork" instance (never
// served by rbe-worker-pool.yml), and cache/local/skip mean nothing to
// pre-warm. fork-rw is deliberately included - a trusted fork author's PR,
// per rbe-fork-mint's tier decision, not just bazel-farm.yml's
// pull_request_target path - and a fork or Dependabot pull_request is exactly
// how that mode is reached, so this doubles as the fork/Dependabot
// reachability check the original spec asked for: such a run only matters
// here through needs.rbe.outputs.mode, never through re-inspecting
// github.actor or head.repo.fork in this job's own `if:` (that ban is
// TestBazelRBEJobDecidesOnce's job).
func TestRBEPrewarmIfOnlyRemoteAndForkRW(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	if job.If != bazelRBEPrewarmIf {
		t.Fatalf("rbe-prewarm if = %q, want the pinned %q", job.If, bazelRBEPrewarmIf)
	}
	for _, tc := range []struct {
		mode string
		want bool
	}{
		{"remote", true},
		{"fork-rw", true},
		{"fork-ro", false},
		{"cache", false},
		{"local", false},
		{"skip", false},
	} {
		v, err := evalGHExpr(job.If, map[string]string{"needs.rbe.outputs.mode": tc.mode})
		if err != nil {
			t.Fatalf("evalGHExpr(%q) mode=%s: %v", job.If, tc.mode, err)
		}
		if ghTruthy(v) != tc.want {
			t.Errorf("rbe-prewarm if, mode=%s: evaluated to %v, want %v", tc.mode, v, tc.want)
		}
	}
}

// TestRBEPrewarmNeverGatedOrNeeded: no lane depends on rbe-prewarm (a
// `needs:` cycle or ordering dependency would turn "advisory" into "gating"),
// and neither of pr.yml's ci-gate paths - the required-checks list it reads
// from .github/scripts/ci-gate.sh/pr-policy, nor bazel-gate.sh's own
// skip/aggregate vocabulary - ever mentions it by name.
func TestRBEPrewarmNeverGatedOrNeeded(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	for name, job := range workflow.Jobs {
		if name == bazelRBEPrewarmJobName {
			continue
		}
		for _, need := range job.Needs {
			if need == bazelRBEPrewarmJobName {
				t.Errorf("%s needs %s; rbe-prewarm is advisory and must never gate another lane", name, bazelRBEPrewarmJobName)
			}
		}
	}

	root := sourceRepoRoot(t)
	gate := readPolicyFile(t, root, filepath.Join(".github", "scripts", "bazel-gate.sh"))
	if strings.Contains(gate, bazelRBEPrewarmJobName) {
		t.Errorf("bazel-gate.sh mentions %s; it must stay outside the gate's skip/aggregate vocabulary", bazelRBEPrewarmJobName)
	}

	// pr.yml's ci-gate required-check id list: BAZEL* ids come from
	// bazel-gate.sh's own vocabulary (BAZEL, BAZEL_TEST, ...), never a
	// per-job id for rbe-prewarm.
	ciGate := readPolicyFile(t, root, filepath.Join(".github", "scripts", "ci-gate.sh"))
	if regexp.MustCompile(`(?i)rbe.prewarm`).MatchString(ciGate) {
		t.Errorf("ci-gate.sh mentions rbe-prewarm; it must never be a required check")
	}
}

// TestRBEPrewarmNeverFailsTheRun: the job carries the one deliberate
// continue-on-error in bazel.yml (every other occurrence is banned by
// TestBazelWorkflowSecretsAndFailureSurface), and the dispatch script it
// runs has no path that exits non-zero.
func TestRBEPrewarmNeverFailsTheRun(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	if !job.ContinueOnError {
		t.Errorf("rbe-prewarm job.continue-on-error = false; a real step failure would fail bazel.yml's own run and cascade into pr.yml's ci-gate")
	}
	for _, step := range job.Steps {
		if v, ok := step.ContinueOnError.(bool); ok && v {
			t.Errorf("rbe-prewarm step %q has its own continue-on-error; only the job level should (TestBazelWorkflowSecretsAndFailureSurface bans the key everywhere else)", step.Name)
		}
	}

	root := sourceRepoRoot(t)
	script := readPolicyFile(t, root, filepath.Join(".github", "scripts", "rbe-prewarm.sh"))
	if !strings.Contains(script, "set -u") || strings.Contains(script, "set -e") {
		t.Errorf("rbe-prewarm.sh must run under set -u only (no set -e/pipefail): every gh/curl failure path is handled explicitly and must fall through to the trailing exit 0")
	}
	exits := regexp.MustCompile(`(?m)^\s*exit\s+(\S+)`).FindAllStringSubmatch(script, -1)
	if len(exits) == 0 {
		t.Fatalf("rbe-prewarm.sh has no exit statement; expected only exit 0")
	}
	for _, m := range exits {
		if m[1] != "0" {
			t.Errorf("rbe-prewarm.sh has %q; every exit must be exit 0 (best-effort by design)", strings.TrimSpace(m[0]))
		}
	}
}

// TestRBEPrewarmSecretsOnlyInItsOwnJob walks the raw YAML of bazel.yml and
// confirms the two app-credential secrets are referenced only inside the
// rbe-prewarm job (its HAS_POOL_APP env check and the mint step's `with:`),
// never anywhere else in the file - not another job's env, not a workflow
// top-level env, not an `if:`.
func TestRBEPrewarmSecretsOnlyInItsOwnJob(t *testing.T) {
	secretRef := regexp.MustCompile(`\bsecrets\.RBE_POOL_APP_(ID|PRIVATE_KEY)\b`)
	jobPrefix := ".jobs." + bazelRBEPrewarmJobName + "."
	found := 0
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName)), "", func(path string, key bool, value string) {
		if key || !secretRef.MatchString(value) {
			return
		}
		found++
		if !strings.HasPrefix(path, jobPrefix) {
			t.Errorf("%s: %s references an rbe-prewarm app credential outside the job (prefix %q)", bazelWorkflowName, path, jobPrefix)
		}
	})
	if found == 0 {
		t.Fatalf("found no secrets.RBE_POOL_APP_* reference in %s; the test fixture or the job moved", bazelWorkflowName)
	}
}

// TestRBEPrewarmAppTokenScoped pins the mint step to the exact "bazel-
// allocator" installation-token shape the docs promise: the action pinned to
// a full commit SHA with its version comment (same SHA this repo already
// uses for the same action in update-flake-lock.yml), scoped to
// gastownhall/gascity alone (never, say, the whole gastownhall org or an
// unrelated repo), and narrowed to the Actions permission.
func TestRBEPrewarmAppTokenScoped(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	mint := job.step(t, "Mint gastownhall/gascity installation token")

	family, sha, found := strings.Cut(mint.Uses, "@")
	if family != "actions/create-github-app-token" {
		t.Fatalf("mint step uses %q, want actions/create-github-app-token", mint.Uses)
	}
	if !found || !actionPin.MatchString(sha) {
		t.Errorf("mint step action %q is not pinned to a 40-hex commit SHA", mint.Uses)
	}
	const wantSHA = "bcd2ba49218906704ab6c1aa796996da409d3eb1" // v3, same as update-flake-lock.yml
	if sha != wantSHA {
		t.Errorf("mint step action SHA = %q, want %q (update-flake-lock.yml's pin for the same action)", sha, wantSHA)
	}

	if got := mint.With["owner"]; got != "gastownhall" {
		t.Errorf("mint step owner = %q, want %q", got, "gastownhall")
	}
	if got := mint.With["repositories"]; got != "gascity" {
		t.Errorf("mint step repositories = %q, want exactly %q (not a list, not the whole org)", got, "gascity")
	}
	if got := mint.With["app-id"]; got != "${{ secrets.RBE_POOL_APP_ID }}" {
		t.Errorf("mint step app-id = %q, want the RBE_POOL_APP_ID secret", got)
	}
	if got := mint.With["private-key"]; got != "${{ secrets.RBE_POOL_APP_PRIVATE_KEY }}" {
		t.Errorf("mint step private-key = %q, want the RBE_POOL_APP_PRIVATE_KEY secret", got)
	}
	if got := mint.With["permission-actions"]; got != "write" {
		t.Errorf("mint step permission-actions = %q, want %q (narrow even if the app is ever granted more)", got, "write")
	}
	// Gated on the credential check, not unconditional: TestRBEPrewarmGatedOnAppSecret.
	if mint.If != "${{ steps.has-app.outputs.has-app == 'true' }}" {
		t.Errorf("mint step if = %q, want it gated on the has-app credential check", mint.If)
	}
}

// TestRBEPrewarmGatedOnAppSecret: the HAS_POOL_APP pattern (mirroring the rbe
// job's own HAS_EXECUTOR) - the mint step is skipped, not failed, whenever
// either app secret is unset, which is also the kill switch's main path
// (before the app is provisioned at all).
func TestRBEPrewarmGatedOnAppSecret(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	check := job.step(t, "Check for the pre-warm app credential")
	if check.Env["HAS_POOL_APP"] != "${{ secrets.RBE_POOL_APP_PRIVATE_KEY != '' }}" {
		t.Errorf("has-app step env HAS_POOL_APP = %q, want an emptiness test on RBE_POOL_APP_PRIVATE_KEY", check.Env["HAS_POOL_APP"])
	}
	if check.ID != "has-app" {
		t.Errorf("has-app step id = %q, want %q", check.ID, "has-app")
	}
}

// TestRBEPrewarmDispatchTargetPinned: the script's three target constants are
// literal (never a variable the caller or a repo var could redirect), and the
// only gh workflow/run subcommands in the script reference them, never a
// hardcoded alternative.
func TestRBEPrewarmDispatchTargetPinned(t *testing.T) {
	root := sourceRepoRoot(t)
	script := readPolicyFile(t, root, filepath.Join(".github", "scripts", "rbe-prewarm.sh"))

	for _, want := range []string{
		`POOL_REPO="gastownhall/gascity"`,
		`POOL_WORKFLOW="rbe-worker-pool.yml"`,
		`POOL_REF="main"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("rbe-prewarm.sh does not contain %q", want)
		}
	}

	ghCalls := regexp.MustCompile(`(?m)^\s*(gh |args=\()`).FindAllString(script, -1)
	if len(ghCalls) == 0 {
		t.Fatalf("rbe-prewarm.sh has no gh invocation; the dispatch logic moved")
	}
	// Every gh run-list/workflow-run invocation must use $POOL_REPO and
	// $POOL_WORKFLOW, never a literal repo or workflow file.
	ghLine := regexp.MustCompile(`(?m)^.*\bgh\b.*$`)
	for _, line := range ghLine.FindAllString(script, -1) {
		if strings.Contains(line, "run list") || strings.Contains(line, "workflow run") || strings.Contains(line, `args=(workflow`) {
			if !strings.Contains(line, "POOL_REPO") && !strings.Contains(line, `"${args[@]}"`) {
				t.Errorf("rbe-prewarm.sh gh invocation does not reference $POOL_REPO: %q", strings.TrimSpace(line))
			}
		}
	}
}

// TestRBEPrewarmTokenIsGHToken: the dispatch step hands the script the
// mint step's own output, never a long-lived or ambient credential
// (secrets.GITHUB_TOKEN, a PAT, or the mint step's app-id/private-key
// directly).
func TestRBEPrewarmTokenIsGHToken(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	dispatch := job.step(t, "Pre-warm rbe-west OSS worker pool (gastownhall/gascity)")
	if got := dispatch.Env["GH_TOKEN"]; got != "${{ steps.mint.outputs.token }}" {
		t.Errorf("dispatch step env GH_TOKEN = %q, want the mint step's own token output", got)
	}
	if strings.Contains(dispatch.Env["GH_TOKEN"], "secrets.") {
		t.Errorf("dispatch step GH_TOKEN reads a secret directly; it must use the minted installation token")
	}
}

// actionPin and readYAMLNode/walkYAML/evalGHExpr/ghTruthy/readCIWorkflow/
// readPolicyFile/sourceRepoRoot are shared helpers already defined in
// ci_workflow_test.go and ci_blacksmith_runner_test.go; this file adds no new
// infrastructure of its own.
