package scripts_test

import (
	"os"
	"os/exec"
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
// B1 (security review of bdef342d5, this task's second pass): the job used
// to check out `.github/scripts/rbe-prewarm.sh` at the PR's own head SHA
// (with allow-unsafe-pr-checkout) and run it while the mint step's token was
// live. Editing a script file does not trip the org's workflow-file approval
// policy the way editing this YAML does, so any same-repo branch push - not
// just a fork - could have altered that script to exfiltrate the shared
// gascity scaler credential. The fix: no checkout step at all, and the
// dispatch logic lives inline in the step's own `run:`, which
// pull_request_target always loads from the trusted base branch. Several
// tests below were rewritten to read that inline string (job.step(...).Run)
// instead of a file that no longer exists; new tests pin the no-checkout and
// nothing-after-the-mint-touches-a-repo-path invariants the old suite did
// not check (the mutation that added an extra step running
// `bash .github/scripts/evil.sh` after the mint step passed undetected).
//
// Each test below was mutation-tested by hand while this job was written:
// reverting the fix it pins reliably fails the corresponding test here. See
// this task's notes file for the transcript.

// TestRBEPrewarmIfOnlyRemote runs the job's real, pinned `if:` expression
// (bazelRBEPrewarmIf, via the shared evalGHExpr from
// ci_blacksmith_runner_test.go, not a hand-written mirror) against every mode
// the rbe job can produce. Only remote may schedule the job (B1 dropped
// fork-rw: a fork or Dependabot pull_request run never carries a
// workflow_call secret regardless of this if, and bazel-farm.yml - the other
// path to a privileged fork tier - no longer forwards the app secrets
// either, so fork-rw never had a credential to use).
func TestRBEPrewarmIfOnlyRemote(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	if job.If != bazelRBEPrewarmIf {
		t.Fatalf("rbe-prewarm if = %q, want the pinned %q", job.If, bazelRBEPrewarmIf)
	}
	for _, tc := range []struct {
		mode string
		want bool
	}{
		{"remote", true},
		{"fork-rw", false},
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

// TestRBEPrewarmNoCheckoutStep: B1's core fix. No step in this job may check
// out the repository at all - the dispatch logic lives entirely in the
// trusted workflow YAML's own `run:` block, never a file read from a
// checked-out tree.
func TestRBEPrewarmNoCheckoutStep(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	for _, step := range job.Steps {
		if actionFamily(step.Uses) == "actions/checkout" {
			t.Errorf("rbe-prewarm step %q checks out the repository; B1 (security review of bdef342d5) requires this job have no checkout at all", step.Name)
		}
	}
}

// TestRBEPrewarmNothingAfterMintTouchesARepoPath: even with no checkout step,
// a future edit could add a step after the mint step that runs a local
// composite action or a script path from the working directory while the
// token (or, worse, the raw private key) is still in scope. This is exactly
// the review's M2 mutation (an extra step running
// `bash .github/scripts/evil.sh` with the token in env), which the pre-B1
// suite did not catch. Pin: no step at or after the mint step's index may
// use a local ("./...") action, and no such step's `run:` may reference a
// repository-relative path.
func TestRBEPrewarmNothingAfterMintTouchesARepoPath(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	mintIndex := -1
	for i, step := range job.Steps {
		if step.Name == "Mint gastownhall/gascity installation token" {
			mintIndex = i
			break
		}
	}
	if mintIndex < 0 {
		t.Fatalf("rbe-prewarm has no mint step named %q", "Mint gastownhall/gascity installation token")
	}
	repoPath := regexp.MustCompile(`(^|[^$.])\.(/|github/)|\bbash\s+\.`)
	checked := 0
	for i, step := range job.Steps {
		if i < mintIndex {
			continue
		}
		checked++
		if strings.HasPrefix(step.Uses, "./") {
			t.Errorf("rbe-prewarm step %q (index %d, at/after the mint step) uses a local action %q; nothing after the mint step may run repository code", step.Name, i, step.Uses)
		}
		if repoPath.MatchString(step.Run) {
			t.Errorf("rbe-prewarm step %q (index %d, at/after the mint step) run: references a repository-relative path; nothing after the mint step may run repository code:\n%s", step.Name, i, step.Run)
		}
	}
	if checked < 2 {
		t.Fatalf("only %d step(s) at/after the mint step; expected at least the dispatch and record-result steps", checked)
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
// TestBazelWorkflowSecretsAndFailureSurface), and the dispatch step's own
// inline script (job.step(...).Run: no separate file since B1) has no path
// that exits non-zero.
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

	script := job.step(t, "Pre-warm rbe-west OSS worker pool (gastownhall/gascity)").Run
	if !strings.Contains(script, "set -u") || strings.Contains(script, "set -e") {
		t.Errorf("the dispatch step must run under set -u only (no set -e/pipefail): every gh failure path is handled explicitly and must fall through to the trailing exit 0")
	}
	exits := regexp.MustCompile(`(?m)^\s*exit\s+(\S+)`).FindAllStringSubmatch(script, -1)
	if len(exits) == 0 {
		t.Fatalf("the dispatch step has no exit statement; expected only exit 0")
	}
	for _, m := range exits {
		if m[1] != "0" {
			t.Errorf("the dispatch step has %q; every exit must be exit 0 (best-effort by design)", strings.TrimSpace(m[0]))
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

// TestRBEPrewarmAppSecretsOnlyExpectedCallers: B1 (security review of
// bdef342d5) requires the two bazel-allocator app secrets reach only
// bazel.yml's rbe-prewarm job and the pass-through `secrets:` blocks of its
// two same-repo/trusted callers (pr.yml, nightly.yml) - never
// bazel-farm.yml, whose pull_request_target run executes an allowlisted
// fork author's own PR code, and never any other workflow file.
func TestRBEPrewarmAppSecretsOnlyExpectedCallers(t *testing.T) {
	secretRef := regexp.MustCompile(`RBE_POOL_APP_(ID|PRIVATE_KEY)`)
	root := sourceRepoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		name := entry.Name()
		walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", name)), "", func(path string, key bool, value string) {
			if key || !secretRef.MatchString(value) {
				return
			}
			found[name]++
			switch name {
			case bazelWorkflowName:
				if !strings.HasPrefix(path, ".jobs."+bazelRBEPrewarmJobName+".") {
					t.Errorf("%s: %s references an rbe-prewarm app credential outside the job", name, path)
				}
			case "pr.yml", "nightly.yml":
				if !strings.HasPrefix(path, ".jobs.bazel.secrets.") {
					t.Errorf("%s: %s references an rbe-prewarm app credential outside the bazel.yml call's secrets pass-through", name, path)
				}
			default:
				t.Errorf("%s: %s references an rbe-prewarm app credential; only bazel.yml, pr.yml and nightly.yml may (B1: bazel-farm.yml, and every other workflow, must not)", name, path)
			}
		})
	}
	if found[bazelWorkflowName] == 0 {
		t.Fatalf("found no RBE_POOL_APP_* reference in %s; the job moved", bazelWorkflowName)
	}
	for _, caller := range []string{"pr.yml", "nightly.yml"} {
		if found[caller] == 0 {
			t.Errorf("found no RBE_POOL_APP_* reference in %s; expected its pass-through secrets block", caller)
		}
	}
	if found[bazelFarmWorkflowName] != 0 {
		t.Errorf("%s references an rbe-prewarm app credential %d time(s); B1 requires zero", bazelFarmWorkflowName, found[bazelFarmWorkflowName])
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

// TestRBEPrewarmDispatchTargetPinned: the dispatch step's inline script
// (job.step(...).Run - no separate file since B1) pins its three target
// constants literally (never a variable the caller or a repo var could
// redirect), and every gh workflow/run subcommand invocation is the exact,
// literal command line this job is meant to issue - not just any line
// mentioning $POOL_REPO (the pre-B1 version of this test accepted any line
// containing "${args[@]}", which a mutation could redirect freely).
func TestRBEPrewarmDispatchTargetPinned(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	script := job.step(t, "Pre-warm rbe-west OSS worker pool (gastownhall/gascity)").Run

	for _, want := range []string{
		`POOL_REPO="gastownhall/gascity"`,
		`POOL_WORKFLOW="rbe-worker-pool.yml"`,
		`POOL_REF="main"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("dispatch step run: does not contain %q", want)
		}
	}

	const wantRunListCmd = `gh run list -R "$POOL_REPO" --workflow "$POOL_WORKFLOW" --limit 50 \`
	if !strings.Contains(script, wantRunListCmd) {
		t.Errorf("dispatch step run: does not contain the exact run-list invocation %q", wantRunListCmd)
	}
	const wantDispatchCmd = `gh workflow run "$POOL_WORKFLOW" -R "$POOL_REPO" --ref "$POOL_REF"`
	if !strings.Contains(script, wantDispatchCmd) {
		t.Errorf("dispatch step run: does not contain the exact dispatch invocation %q", wantDispatchCmd)
	}

	// Every gh invocation line must use the three variables, never a literal
	// repo, workflow file or ref.
	ghLine := regexp.MustCompile(`(?m)^.*\bgh\b.*$`)
	lines := ghLine.FindAllString(script, -1)
	if len(lines) == 0 {
		t.Fatalf("dispatch step run: has no gh invocation; the dispatch logic moved")
	}
	for _, line := range lines {
		if strings.Contains(line, "gastownhall/gascity") || strings.Contains(line, "rbe-worker-pool.yml") {
			t.Errorf("dispatch step run: gh invocation hardcodes the target instead of using $POOL_REPO/$POOL_WORKFLOW: %q", strings.TrimSpace(line))
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

// TestRBEPrewarmTokenOnlyInDispatchStep: steps.mint.outputs.token must
// appear exactly once in the whole file, and only as the dispatch step's own
// GH_TOKEN - never copied into another step's env, with:, or run:, which
// would widen the token's exposure beyond the one step B1 requires.
func TestRBEPrewarmTokenOnlyInDispatchStep(t *testing.T) {
	const tokenRef = "steps.mint.outputs.token"
	wantPath := ".jobs." + bazelRBEPrewarmJobName + ".steps[2].env.GH_TOKEN"
	// Confirm the expected path actually names the dispatch step before
	// using it as the sole allowed match, so a step-index change doesn't
	// silently make this test vacuous.
	if job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName); len(job.Steps) < 3 || job.Steps[2].Name != "Pre-warm rbe-west OSS worker pool (gastownhall/gascity)" {
		t.Fatalf("rbe-prewarm steps[2] is not the dispatch step; update wantPath in this test")
	}
	found := 0
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName)), "", func(path string, key bool, value string) {
		if key || !strings.Contains(value, tokenRef) {
			return
		}
		found++
		if path != wantPath || value != "${{ "+tokenRef+" }}" {
			t.Errorf("%s: %s = %q references the minted token; only %s may, as exactly %q", bazelWorkflowName, path, value, wantPath, "${{ "+tokenRef+" }}")
		}
	})
	if found != 1 {
		t.Errorf("%s: found %d references to %s, want exactly 1 (the dispatch step's GH_TOKEN)", bazelWorkflowName, found, tokenRef)
	}
}

// TestRBEPrewarmDispatchScriptBehavior extracts the dispatch step's real,
// pinned inline `run:` string from the parsed YAML (not a hand-maintained
// copy) and executes it against a fake `gh` on PATH, covering the same
// scenarios this job's script was hand-verified against before it was
// inlined (see this task's notes file): an empty token, the
// RBE_PREWARM_WORKERS=0 kill switch, the pool already at the desired size,
// `gh run list` failing, and `gh workflow run` failing or succeeding. This
// keeps the inline script under continuous test even though it is no longer
// a standalone file shellcheck or a Go test can read directly by path.
func TestRBEPrewarmDispatchScriptBehavior(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEPrewarmJobName)
	script := job.step(t, "Pre-warm rbe-west OSS worker pool (gastownhall/gascity)").Run
	if strings.TrimSpace(script) == "" {
		t.Fatal("dispatch step has an empty run: body")
	}

	scriptPath := filepath.Join(t.TempDir(), "prewarm.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	fakeGHDir := t.TempDir()
	const fakeGH = `#!/usr/bin/env bash
set -u
if [ "$1" = "run" ] && [ "$2" = "list" ]; then
  printf '%s' "${FAKE_GH_RUN_LIST_OUTPUT:-}"
  exit "${FAKE_GH_RUN_LIST_EXIT:-0}"
fi
if [ "$1" = "workflow" ] && [ "$2" = "run" ]; then
  echo "$*" >> "${FAKE_GH_CALL_LOG:?}"
  exit "${FAKE_GH_WORKFLOW_RUN_EXIT:-0}"
fi
echo "fake gh: unexpected invocation: $*" >&2
exit 127
`
	if err := os.WriteFile(filepath.Join(fakeGHDir, "gh"), []byte(fakeGH), 0o700); err != nil {
		t.Fatal(err)
	}

	type result struct {
		stdout string
		calls  string
		code   int
	}
	run := func(t *testing.T, env map[string]string) result {
		t.Helper()
		callLog := filepath.Join(t.TempDir(), "calls.log")
		if err := os.WriteFile(callLog, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", scriptPath)
		// fakeGHDir first so it shadows any real gh on PATH; the rest of the
		// inherited PATH stays so the fake gh's own "#!/usr/bin/env bash"
		// shebang can still find env and bash.
		cmd.Env = []string{"PATH=" + fakeGHDir + ":" + os.Getenv("PATH"), "FAKE_GH_CALL_LOG=" + callLog}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("run dispatch script: %v", err)
			}
		}
		calls, readErr := os.ReadFile(callLog)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return result{stdout: string(out), calls: string(calls), code: code}
	}

	t.Run("empty token warns and does not call gh", func(t *testing.T) {
		r := run(t, map[string]string{"GH_TOKEN": ""})
		if r.code != 0 {
			t.Errorf("exit code = %d, want 0 (best-effort)", r.code)
		}
		if !strings.Contains(r.stdout, "::warning title=rbe-west pre-warm::no dispatch token") {
			t.Errorf("stdout = %q, want the no-dispatch-token warning", r.stdout)
		}
		if r.calls != "" {
			t.Errorf("calls = %q, want no gh workflow run calls", r.calls)
		}
	})

	t.Run("RBE_PREWARM_WORKERS=0 is the kill switch", func(t *testing.T) {
		r := run(t, map[string]string{"GH_TOKEN": "tok", "RBE_PREWARM_WORKERS": "0"})
		if r.code != 0 {
			t.Errorf("exit code = %d, want 0", r.code)
		}
		if !strings.Contains(r.stdout, "RBE_PREWARM_WORKERS=0: rbe-west pre-warm disabled (kill switch)") {
			t.Errorf("stdout = %q, want the kill-switch message", r.stdout)
		}
		if r.calls != "" {
			t.Errorf("calls = %q, want no gh workflow run calls", r.calls)
		}
	})

	t.Run("already at desired count dispatches nothing", func(t *testing.T) {
		r := run(t, map[string]string{
			"GH_TOKEN":                "tok",
			"FAKE_GH_RUN_LIST_OUTPUT": "1",
		})
		if r.code != 0 {
			t.Errorf("exit code = %d, want 0", r.code)
		}
		if !strings.Contains(r.stdout, "already active") {
			t.Errorf("stdout = %q, want an already-active message", r.stdout)
		}
		if r.calls != "" {
			t.Errorf("calls = %q, want no gh workflow run calls", r.calls)
		}
	})

	t.Run("gh run list failure skips the dispatch", func(t *testing.T) {
		r := run(t, map[string]string{
			"GH_TOKEN":                "tok",
			"FAKE_GH_RUN_LIST_EXIT":   "1",
			"FAKE_GH_RUN_LIST_OUTPUT": "",
		})
		if r.code != 0 {
			t.Errorf("exit code = %d, want 0", r.code)
		}
		if !strings.Contains(r.stdout, "::warning title=rbe-west pre-warm::could not read") {
			t.Errorf("stdout = %q, want the could-not-read warning", r.stdout)
		}
		if r.calls != "" {
			t.Errorf("calls = %q, want no gh workflow run calls", r.calls)
		}
	})

	t.Run("gh workflow run failure warns but still exits 0", func(t *testing.T) {
		r := run(t, map[string]string{
			"GH_TOKEN":                  "tok",
			"RBE_PREWARM_WORKERS":       "1",
			"FAKE_GH_RUN_LIST_OUTPUT":   "0",
			"FAKE_GH_WORKFLOW_RUN_EXIT": "1",
		})
		if r.code != 0 {
			t.Errorf("exit code = %d, want 0", r.code)
		}
		if !strings.Contains(r.stdout, "::warning title=rbe-west pre-warm::gh workflow run rbe-worker-pool.yml -R gastownhall/gascity failed (attempt 1/1)") {
			t.Errorf("stdout = %q, want the dispatch-failed warning", r.stdout)
		}
		if !strings.Contains(r.stdout, "dispatched 0/1") {
			t.Errorf("stdout = %q, want a dispatched 0/1 summary", r.stdout)
		}
		if strings.Count(r.calls, "\n") != 1 {
			t.Errorf("calls = %q, want exactly one attempted gh workflow run invocation", r.calls)
		}
	})

	t.Run("dispatches the missing count and reports it", func(t *testing.T) {
		r := run(t, map[string]string{
			"GH_TOKEN":                "tok",
			"RBE_PREWARM_WORKERS":     "2",
			"FAKE_GH_RUN_LIST_OUTPUT": "0",
		})
		if r.code != 0 {
			t.Errorf("exit code = %d, want 0", r.code)
		}
		if !strings.Contains(r.stdout, "dispatched 2/2 gastownhall/gascity/rbe-worker-pool.yml worker(s) (active before: 0, want: 2)") {
			t.Errorf("stdout = %q, want the dispatched-2/2 summary", r.stdout)
		}
		if strings.Count(r.calls, "\n") != 2 {
			t.Errorf("calls = %q, want exactly two gh workflow run invocations", r.calls)
		}
		for _, line := range strings.Split(strings.TrimRight(r.calls, "\n"), "\n") {
			if strings.TrimSpace(line) != `workflow run rbe-worker-pool.yml -R gastownhall/gascity --ref main` {
				t.Errorf("call line = %q, want the exact pinned dispatch invocation", line)
			}
		}
	})
}

// actionPin, actionFamily and readYAMLNode/walkYAML/evalGHExpr/ghTruthy/
// readCIWorkflow/readPolicyFile/sourceRepoRoot are shared helpers already
// defined in ci_workflow_test.go and ci_blacksmith_runner_test.go; this file
// adds no new infrastructure of its own.
