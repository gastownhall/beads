package scripts_test

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The merge-group fast set (engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "Merge
// Queue"). A merge group runs only the fast build and unit set: every
// Windows, macOS and Nix job and bazel.yml's Dolt-tier lanes are skipped
// there by design. The pull request's own run before it runs everything,
// and main's push runs run the complement after it lands, so every job the
// required gates read runs on every push head on main: the merge queue lands
// a batch of entries as one push of its last entry, whose merge group ran
// the fast set on the same tree (main fast-forwards to it), and the push
// runs (pr.yml's and pr-risk.yml's, and bazel.yml's own, which runs every
// Bazel lane) run the rest on it. That tree contains every entry of the
// batch; an earlier entry gets the complement only as part of it.
//
// One script decides, .github/scripts/ci-scope.sh, run by pr.yml's and
// pr-risk.yml's scope jobs; it alone reads the event. Every other job reads
// the scope's outputs, and the two required gates excuse a skip only where
// the scope says so: ci-scope.sh's skips for the workflows' own job sets,
// bazel-gate.sh's for the Dolt-tier lanes, which also needs the Bazel call
// to report that it ran without them. These tests run the real script, every
// job's real if: through evalGHExpr, and both gates' real steps.

const (
	ciScopeScript = ".github/scripts/ci-scope.sh"
	ciScopeJob    = "scope"
	// bazel.yml's Dolt-tier switch: each Dolt-tier lane's if ends with it,
	// pr.yml passes the scope's dolt-tiers, and the call reports what it did
	// as its dolt-tiers output.
	bazelDoltTiersCond   = "inputs.dolt-tiers != 'off'"
	bazelDoltTiersOutput = "${{ inputs.dolt-tiers == 'off' && 'off' || 'on' }}"
)

// ciScopeRows: ci-scope.sh's decision by event (the two workflows'
// triggers). Any other event has no row and fails the scope job.
var ciScopeRows = map[string]map[string]string{
	// Everything, as before the fast set.
	"pull_request": {"platforms": "true", "dolt-tiers": "on", "linux": "true", "advisory": "true"},
	// The fast set: the Linux jobs and every Bazel lane but the Dolt tiers.
	"merge_group": {"platforms": "false", "dolt-tiers": "off", "linux": "true", "advisory": "false"},
	// The complement, after the merge: the Windows, macOS and Nix jobs.
	// bazel.yml's own push run is every Bazel lane, so pr.yml calls none.
	"push": {"platforms": "true", "dolt-tiers": "on", "linux": "false", "advisory": "false"},
}

// ciScopePlatformsIf: the if of a job on the platforms switch (the Windows,
// macOS and Nix jobs): it runs on every pull request and every push to main,
// and only a merge group skips it, which the gates excuse there alone
// (TestCIScopePRGate, TestCIScopeRiskGate).
const ciScopePlatformsIf = "${{ needs." + ciScopeJob + ".outputs.platforms == 'true' }}"

// ciScopeOutputs: the scope job's outputs, in the gate env's order.
var ciScopeOutputs = []string{"platforms", "dolt-tiers", "linux", "advisory"}

// ciScopeJobSwitch: the scope output each job of the two workflows runs on
// (if: needs.scope.outputs.<switch> == 'true'). The scope job decides and the
// gates always run; every other job is listed, so a new job cannot escape the
// decision (TestCIScopeJobsReadTheScope).
var ciScopeJobSwitch = map[string]map[string]string{
	"pr.yml": {
		"fast-checks":                   "linux",
		"check-doc-flags":               "linux",
		"bazel":                         "linux",
		"test-windows-liveness":         "platforms",
		"test-windows-small":            "platforms",
		"worktree-remove-windows":       "platforms",
		"check-doc-freshness-platforms": "platforms",
		"pr-preflight-platforms":        "platforms",
		"windows-make-shell":            "platforms",
		"advisory-reports":              "advisory",
	},
	prRiskWorkflowName: {"test-nix": "platforms"},
}

// ciScopeGateIDs: per workflow, the gate ids each scope switch turns off
// when it is false (the gate step's CI_SCOPE_<SWITCH>_IDS, which
// ci-scope.sh's skips prints): pr.yml's Windows and macOS jobs on a merge
// group, its Linux jobs and its whole Bazel call on a push to main (bazel.yml
// runs on that push by itself), pr-risk.yml's Nix job on a merge group. The
// Dolt-tier lanes' ids are bazel-gate.sh's (bazelDoltTierGateIDs).
var ciScopeGateIDs = map[string]map[string][]string{
	"pr.yml": {
		"platforms": {"TEST_WINDOWS_LIVENESS", "WORKTREE_REMOVE_WINDOWS", "CHECK_DOC_FRESHNESS_PLATFORMS", "PR_PREFLIGHT_PLATFORMS", "WINDOWS_MAKE_SHELL"},
		"linux": {"FAST_CHECKS", "CHECK_MIGRATION_HYGIENE", "CHECK_NO_BEADS_CHANGES", "CHECK_DOC_FLAGS", "PACKAGE_MCP", "PACKAGE_NPM",
			"BAZEL", "BAZEL_TEST", "BAZEL_PURE", "BAZEL_RELEASE_CROSS", "BAZEL_EMBEDDED", "BAZEL_INTEGRATION",
			"BAZEL_DOLTSERVER", "BAZEL_PROXIED", "BAZEL_SERVER_STORAGE", "BAZEL_CMD_DOLT", "BAZEL_REMOTE"},
	},
	prRiskWorkflowName: {"platforms": {"TEST_NIX"}},
}

// bazelDoltTierGateIDs: the gate ids of bazel.yml's Dolt-tier lanes, which a
// merge group skips. bazel-gate.sh excuses them only when the scope turned
// the tiers off and the call reports it ran without them.
var bazelDoltTierGateIDs = []string{"BAZEL_EMBEDDED", "BAZEL_DOLTSERVER", "BAZEL_PROXIED", "BAZEL_SERVER_STORAGE", "BAZEL_CMD_DOLT"}

// bazelDoltTierLanes: the bazel.yml jobs a dolt-tiers "off" call skips in
// every mode: the Dolt tiers' unions (mode remote) and their members (every
// other mode), and the embedded tier.
var bazelDoltTierLanes = []string{bazelEmbedJobName, bazelDoltJobName, bazelProxiedJobName, bazelDoltRaceJobName, bazelServerJobName, bazelCmdDoltJobName, bazelDoltIntJobName}

// withDoltTiers: a Dolt-tier lane's if, its own rule plus the caller's
// dolt-tiers switch.
func withDoltTiers(ifExpr string) string {
	return strings.TrimSuffix(ifExpr, " }}") + " && " + bazelDoltTiersCond + " }}"
}

// ciScopeEnvName: the gate env suffix of a scope output (dolt-tiers ->
// DOLT_TIERS).
func ciScopeEnvName(output string) string {
	return strings.ToUpper(strings.ReplaceAll(output, "-", "_"))
}

// ciScopeCtx: an evalGHExpr context holding the scope job's outputs.
func ciScopeCtx(scope map[string]string) map[string]string {
	ctx := map[string]string{}
	for k, v := range scope {
		ctx["needs."+ciScopeJob+".outputs."+k] = v
	}
	return ctx
}

// runCIScope runs ci-scope.sh with args and env, returning its stdout, its
// $GITHUB_OUTPUT and its error (with the output folded in).
func runCIScope(t *testing.T, env map[string]string, args ...string) (string, map[string]string, error) {
	t.Helper()
	requireHostTool(t, "bash")
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	cmd := exec.Command("bash", append([]string{filepath.Join(sourceRepoRoot(t), ciScopeScript)}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_OUTPUT=" + output, "GITHUB_STEP_SUMMARY=" + filepath.Join(dir, "summary")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("%v: %s%s", err, stdout.String(), stderr.String())
	}
	return strings.TrimSpace(stdout.String()), rrcOutputs(t, output), err
}

// ciScopeFor: the scope job's outputs for an event, from the real script.
func ciScopeFor(t *testing.T, event string) map[string]string {
	t.Helper()
	_, out, err := runCIScope(t, map[string]string{"GITHUB_EVENT_NAME": event}, "decide")
	if err != nil {
		t.Fatalf("%s decide on %s: %v", ciScopeScript, event, err)
	}
	return out
}

// prCallWith: pr.yml's bazel call inputs with the scope's outputs resolved.
// Only `${{ needs.scope.outputs.<x> }}` may be an expression there.
func prCallWith(t *testing.T, with, scope map[string]string) map[string]string {
	t.Helper()
	ref := regexp.MustCompile(`^\$\{\{ needs\.` + ciScopeJob + `\.outputs\.([a-z-]+) \}\}$`)
	out := map[string]string{}
	for k, v := range with {
		if strings.Contains(v, "${{") {
			m := ref.FindStringSubmatch(v)
			if m == nil {
				t.Fatalf("pr.yml bazel call with %s = %q: only the scope's outputs may be expressions there", k, v)
			}
			v = scope[m[1]]
		}
		out[k] = v
	}
	return out
}

// prCallWithFor: pr.yml's bazel call inputs on an event (ciScopeRows).
func prCallWithFor(t *testing.T, event string) map[string]string {
	t.Helper()
	return prCallWith(t, readCIWorkflow(t, "pr.yml").job(t, "bazel").With, ciScopeRows[event])
}

// bazelPRCallLanesFor: the modes each bazel.yml lane runs in under pr.yml's
// call on an event.
func bazelPRCallLanesFor(t *testing.T, event string) map[string]map[string]bool {
	t.Helper()
	return bazelPRCallLanes(t, prCallWithFor(t, event))
}

// TestCIScopeDecide: the decision table, from the real script. Any event the
// workflows do not trigger on fails the scope job, and so both gates. The
// script reads the event and nothing else.
func TestCIScopeDecide(t *testing.T) {
	for event, want := range ciScopeRows {
		if got := ciScopeFor(t, event); !reflect.DeepEqual(got, want) {
			t.Errorf("%s decide on %s = %v, want %v", ciScopeScript, event, got, want)
		}
	}
	for _, event := range []string{"", "workflow_dispatch", "schedule", "pull_request_target", "workflow_call", "workflow_run", "PUSH", "merge_group "} {
		if _, out, err := runCIScope(t, map[string]string{"GITHUB_EVENT_NAME": event}, "decide"); err == nil || len(out) != 0 {
			t.Errorf("%s decide on %q: outputs %v, err %v; want a failure and no outputs", ciScopeScript, event, out, err)
		}
	}
	if _, _, err := runCIScope(t, map[string]string{"GITHUB_EVENT_NAME": "pull_request"}, "bogus"); err == nil {
		t.Errorf("%s bogus succeeded; want a usage error", ciScopeScript)
	}
	vars := regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]*)`)
	for _, line := range strings.Split(readPolicyFile(t, sourceRepoRoot(t), ciScopeScript), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range vars.FindAllStringSubmatch(line, -1) {
			if strings.HasPrefix(m[1], "GITHUB_") && !slices.Contains([]string{"GITHUB_EVENT_NAME", "GITHUB_OUTPUT", "GITHUB_STEP_SUMMARY"}, m[1]) {
				t.Errorf("%s reads %s; the scope is a function of the event alone: %q", ciScopeScript, m[1], line)
			}
		}
		if strings.Contains(line, "${{") {
			t.Errorf("%s holds an expression: %q", ciScopeScript, line)
		}
	}
}

// TestCIScopeJobs: pr.yml's and pr-risk.yml's scope jobs are one job: no needs
// and no if (it always runs), the gates' Blacksmith runner, read-only, only
// the script checked out (sparse, no persisted credentials), the script's
// decide as its one step, and its four outputs.
func TestCIScopeJobs(t *testing.T) {
	wantOutputs := map[string]string{}
	for _, o := range ciScopeOutputs {
		wantOutputs[o] = "${{ steps.decide.outputs." + o + " }}"
	}
	wantCheckout := map[string]string{"persist-credentials": "false", "sparse-checkout": ciScopeScript, "sparse-checkout-cone-mode": "false"}
	var jobs []ciWorkflowJob
	for _, name := range []string{"pr.yml", prRiskWorkflowName} {
		job := readCIWorkflow(t, name).job(t, ciScopeJob)
		jobs = append(jobs, job)
		if job.If != "" || len(job.Needs) != 0 || job.RunsOn != sameRepoBlacksmith2vcpu || job.TimeoutMinutes != 5 || len(job.Env) != 0 || job.ContinueOnError || job.Uses != "" {
			t.Errorf("%s scope job: if %q, needs %v, runs-on %q, timeout %d, env %v, continue-on-error %v, uses %q; want an unconditional 5-minute job on %q",
				name, job.If, job.Needs, job.RunsOn, job.TimeoutMinutes, job.Env, job.ContinueOnError, job.Uses, sameRepoBlacksmith2vcpu)
		}
		if !reflect.DeepEqual(job.Permissions, map[string]any{"contents": "read"}) {
			t.Errorf("%s scope job permissions = %v, want contents: read", name, job.Permissions)
		}
		if !reflect.DeepEqual(job.Outputs, wantOutputs) {
			t.Errorf("%s scope job outputs = %v, want %v", name, job.Outputs, wantOutputs)
		}
		if len(job.Steps) != 2 {
			t.Fatalf("%s scope job has %d steps, want the script's checkout and decide", name, len(job.Steps))
		}
		checkout, decide := job.Steps[0], job.Steps[1]
		if checkout.Uses != "actions/checkout@"+checkoutSHA || !reflect.DeepEqual(checkout.With, wantCheckout) || checkout.If != "" || len(checkout.Env) != 0 {
			t.Errorf("%s scope checkout = %+v; want %s with %v", name, checkout, checkoutSHA, wantCheckout)
		}
		if decide.ID != "decide" || decide.Run != "bash "+ciScopeScript+" decide" || len(decide.Env) != 0 || decide.If != "" || decide.Uses != "" || decide.ContinueOnError != nil {
			t.Errorf("%s scope decide step = %+v; want id decide running %q with no env or if (the script reads GITHUB_EVENT_NAME alone)", name, decide, "bash "+ciScopeScript+" decide")
		}
	}
	if len(jobs) == 2 && !reflect.DeepEqual(jobs[0], jobs[1]) {
		t.Errorf("pr.yml and %s scope jobs differ; both gates must see the same decision:\n%+v\n%+v", prRiskWorkflowName, jobs[0], jobs[1])
	}
}

// TestCIScopeJobsReadTheScope: every job of the two workflows but the scope
// job and the gates needs exactly the scope job and runs exactly when its
// switch is "true"; the gates need the scope job too and always run. pr.yml's
// Bazel call passes the scope's Dolt-tier switch through.
func TestCIScopeJobsReadTheScope(t *testing.T) {
	for name, switches := range ciScopeJobSwitch {
		w := readCIWorkflow(t, name)
		for jobName, job := range w.Jobs {
			switch jobName {
			case ciScopeJob:
				continue
			case "ci-gate":
				if !contains(job.Needs, ciScopeJob) || job.If != "${{ always() }}" {
					t.Errorf("%s ci-gate: needs %v, if %q; want it to need %s and always run", name, job.Needs, job.If, ciScopeJob)
				}
				continue
			}
			sw, ok := switches[jobName]
			if !ok {
				t.Errorf("%s job %s reads no scope switch; give it one (ciScopeJobSwitch) and decide what a merge group and a push to main run", name, jobName)
				continue
			}
			if want := "${{ needs." + ciScopeJob + ".outputs." + sw + " == 'true' }}"; job.If != want {
				t.Errorf("%s job %s if = %q, want %q", name, jobName, job.If, want)
			}
			if !slices.Equal(job.Needs, []string{ciScopeJob}) {
				t.Errorf("%s job %s needs = %v, want [%s]", name, jobName, job.Needs, ciScopeJob)
			}
		}
		for jobName := range switches {
			if _, ok := w.Jobs[jobName]; !ok {
				t.Errorf("ciScopeJobSwitch lists %s job %s, which does not exist", name, jobName)
			}
		}
	}
	if got := readCIWorkflow(t, "pr.yml").job(t, "bazel").With["dolt-tiers"]; got != "${{ needs."+ciScopeJob+".outputs.dolt-tiers }}" {
		t.Errorf("pr.yml bazel call dolt-tiers = %q, want the scope's dolt-tiers output", got)
	}
}

// TestCIScopeGateEnv: both gates require the scope (SCOPE, which their run
// script sets from ci-scope.sh's result), read the scope job's result and
// outputs, and declare per switch exactly the ids it turns off, each one an
// id the gate requires.
func TestCIScopeGateEnv(t *testing.T) {
	for name, idsBySwitch := range ciScopeGateIDs {
		step := readCIWorkflow(t, name).job(t, "ci-gate").step(t, "Evaluate CI gate")
		required := strings.Fields(step.Env["CI_GATE_REQUIRED"])
		if !contains(required, "SCOPE") {
			t.Errorf("%s gate does not require SCOPE", name)
		}
		if _, ok := step.Env["SCOPE"]; ok {
			t.Errorf("%s gate env sets SCOPE; only its run script may (from %s result)", name, ciScopeScript)
		}
		want := map[string]string{"SCOPE_RESULT": "${{ needs." + ciScopeJob + ".result }}"}
		for _, o := range ciScopeOutputs {
			want["SCOPE_"+ciScopeEnvName(o)] = "${{ needs." + ciScopeJob + ".outputs." + o + " }}"
		}
		for k, v := range want {
			if step.Env[k] != v {
				t.Errorf("%s gate env %s = %q, want %q", name, k, step.Env[k], v)
			}
		}
		flagGated := map[string]bool{}
		for _, g := range bazelFlagGatedLanes {
			flagGated[g.id] = true
		}
		for _, o := range ciScopeOutputs {
			key := "CI_SCOPE_" + ciScopeEnvName(o) + "_IDS"
			got := strings.Fields(step.Env[key])
			if !reflect.DeepEqual(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(idsBySwitch[o]))) {
				t.Errorf("%s gate env %s = %v, want exactly %v", name, key, got, idsBySwitch[o])
			}
			for _, id := range got {
				if !contains(required, id) && !(name == "pr.yml" && flagGated[id]) {
					t.Errorf("%s gate env %s lists %s, which the gate does not require", name, key, id)
				}
			}
		}
		for _, frag := range []string{`SCOPE="$(bash ` + ciScopeScript + ` result)"`, "bash " + ciScopeScript + " skips"} {
			if strings.Count(step.Run, frag) != 1 {
				t.Errorf("%s gate run lacks exactly one %q:\n%s", name, frag, step.Run)
			}
		}
	}
}

// ciGateAllowedSkips: the ids a gate run accepted as skipped (ci-gate.sh's
// "ok: X=skipped (allowed)" lines).
func ciGateAllowedSkips(log string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^ok: ([A-Z0-9_]+)=skipped \(allowed\)$`).FindAllStringSubmatch(log, -1) {
		out = append(out, m[1])
	}
	slices.Sort(out)
	return out
}

// prScopeScenario: pr.yml's gate scenario for one run on event with this
// scope: every job the scope runs succeeded and every job it turned off was
// skipped; the Bazel call, when the scope runs it, took mode remote with
// every lane that runs there passing, and otherwise never started.
func prScopeScenario(t *testing.T, event string, scope map[string]string) (bazelGateScenario, map[string]map[string]bool) {
	t.Helper()
	pr := readCIWorkflow(t, "pr.yml")
	ctx := ciScopeCtx(scope)
	sc := bazelGateScenario{name: event, event: event, scope: scope, results: map[string]string{}, flags: map[string]string{}}
	for _, g := range bazelFlagGatedLanes {
		sc.flags[g.flag] = pr.Env[g.flag]
	}
	for jobName := range ciScopeJobSwitch["pr.yml"] {
		if jobName == "bazel" {
			continue
		}
		sc.results[jobName] = "skipped"
		if evalRRCIf(t, pr.job(t, jobName).If, ctx) {
			sc.results[jobName] = "success"
		}
	}
	lanes := map[string]map[string]bool{}
	if evalRRCIf(t, pr.job(t, "bazel").If, ctx) {
		sc.callWith = prCallWith(t, pr.job(t, "bazel").With, scope)
		lanes = bazelPRCallLanes(t, sc.callWith)
		sc.mode, sc.enabled, sc.call, sc.outputs = "remote", "true", "success", map[string]string{}
		for lane, modes := range lanes {
			if modes["remote"] {
				sc.outputs[lane] = "success"
			}
		}
	} else {
		sc.call, sc.callSkipped = "skipped", true
	}
	return sc, lanes
}

// prGateSetter: how a scenario sets the result one gate id reads. bazel:
// the id reads a Bazel call output (a lane's, or a package gate's).
type prGateSetter struct {
	bazel bool
	set   func(sc *bazelGateScenario, result string)
}

// prGateSetters: for every id pr.yml's gate reads from a job (not SCOPE,
// BAZEL or BAZEL_REMOTE, which its run script derives), how a scenario sets
// that job's result. A skipped Bazel lane or fast-checks step reports no
// output. A lane that runs in neither arm of its union in this mode "runs
// anyway" as the arm the mode would pick (the union in mode remote).
func prGateSetters(t *testing.T, step ciWorkflowStep, lanes map[string]map[string]bool) map[string]prGateSetter {
	t.Helper()
	re := regexp.MustCompile(`^\$\{\{ needs\.([A-Za-z0-9_-]+)\.(result|outputs\.([A-Za-z0-9_-]+))( \|\| 'skipped')? \}\}$`)
	setters := map[string]prGateSetter{}
	for key, value := range step.Env {
		m := re.FindStringSubmatch(value)
		if m == nil {
			continue
		}
		job, output := m[1], m[3]
		switch {
		case job == ciScopeJob, job == "bazel" && output == "", job == "bazel" && slices.Contains([]string{"rbe-mode", "rbe-enabled", "dolt-tiers"}, output):
			continue
		case job == "bazel":
			setters[key] = prGateSetter{bazel: true, set: func(sc *bazelGateScenario, r string) {
				reporter := bazelLaneReporter(lanes, output, sc.mode)
				if reporter == "" {
					reporter = output
					if union := bazelUnionOf(output); union != "" && sc.mode == "remote" {
						reporter = union
					}
				}
				if r == "skipped" {
					r = ""
				}
				sc.outputs = maps.Clone(sc.outputs)
				if sc.outputs == nil {
					sc.outputs = map[string]string{}
				}
				sc.outputs[reporter] = r
			}}
		case job == "fast-checks" && output != "":
			legacy := fastChecksLegacyJobNames[output]
			setters[key] = prGateSetter{set: func(sc *bazelGateScenario, r string) {
				sc.results = maps.Clone(sc.results)
				if r == "skipped" {
					r = ""
				} else if sc.results["fast-checks"] == "skipped" {
					sc.results["fast-checks"] = r // a step outcome needs a job that ran
				}
				sc.results[legacy] = r
			}}
		default:
			setters[key] = prGateSetter{set: func(sc *bazelGateScenario, r string) {
				sc.results = maps.Clone(sc.results)
				sc.results[job] = r
			}}
		}
	}
	return setters
}

// TestCIScopePRGate runs pr.yml's real gate step over each event with the
// real scope: green with exactly the skips the scope (and, for the Dolt
// tiers, the call's report) excuses: none on a pull request, the Windows and
// macOS jobs and the Dolt-tier lanes on a merge group, the Linux jobs and the
// whole Bazel call on a push to main. Every job that runs and fails, is
// cancelled or goes missing reds it, and so does a job the scope turned off
// that ran anyway and failed (a Dolt-tier lane in a merge group).
func TestCIScopePRGate(t *testing.T) {
	requireHostTool(t, "bash")
	step := readCIWorkflow(t, "pr.yml").job(t, "ci-gate").step(t, "Evaluate CI gate")
	required := strings.Fields(step.Env["CI_GATE_REQUIRED"])
	wantSkips := map[string][]string{
		"pull_request": nil,
		"merge_group":  slices.Concat(ciScopeGateIDs["pr.yml"]["platforms"], bazelDoltTierGateIDs),
		"push":         ciScopeGateIDs["pr.yml"]["linux"],
	}
	for _, event := range ciScopeEvents() {
		scope := ciScopeFor(t, event)
		base, lanes := prScopeScenario(t, event, scope)
		pass, log := runPRGateStep(t, step, base)
		if !pass {
			t.Fatalf("%s, every job the scope runs passed: gate red\n%s", event, log)
		}
		want := slices.Sorted(slices.Values(wantSkips[event]))
		if got := ciGateAllowedSkips(log); !slices.Equal(got, want) {
			t.Errorf("%s: gate accepted skips %v, want exactly %v\n%s", event, got, want, log)
		}
		skipped := map[string]bool{}
		for _, id := range ciGateAllowedSkips(log) {
			skipped[id] = true
		}
		ids := 0
		for id, setter := range prGateSetters(t, step, lanes) {
			if !contains(required, id) && !(id == bazelFlagGatedLanes[bazelCmdDoltJobName].id && base.flags[bazelFlagGatedLanes[bazelCmdDoltJobName].flag] == "true") {
				continue // advisory
			}
			if setter.bazel && base.callSkipped {
				continue // no lane can run in a call that never started
			}
			ids++
			results := []string{"failure", "cancelled"}
			if !skipped[id] {
				results = append(results, "skipped") // ran, and must not go missing
			}
			for _, r := range results {
				sc := base
				setter.set(&sc, r)
				if pass, log := runPRGateStep(t, step, sc); pass || !regexp.MustCompile(`::error::`+id+`\b`).MatchString(log) {
					t.Errorf("%s, %s %s (scope skipped it: %v): gate pass = %v, want red naming %s\n%s", event, id, r, skipped[id], pass, id, log)
				}
			}
		}
		// pull_request and merge_group: every job and lane id; push: the
		// Windows and macOS jobs and fast-checks' and check-doc-flags'.
		if min := map[string]int{"pull_request": 18, "merge_group": 18, "push": 9}[event]; ids < min {
			t.Errorf("%s: mutated %d gate ids, want at least %d; the setter scan is broken", event, ids, min)
		}
		// The scope job's own failure, or a broken output, reds the gate
		// however the rest went.
		for _, c := range []struct {
			name   string
			result string
			scope  map[string]string
		}{
			{"scope failed", "failure", nil},
			{"scope cancelled", "cancelled", nil},
			{"scope skipped", "skipped", nil},
			{"scope succeeded without outputs", "success", map[string]string{}},
			{"scope output upper-cased", "success", withScope(scope, "linux", strings.ToUpper(scope["linux"]))},
			{"scope output not a row", "success", withScope(scope, "platforms", map[string]string{"true": "false", "false": "true"}[scope["platforms"]])},
			{"scope output missing a switch", "success", withScope(scope, "advisory", "")},
		} {
			sc := base
			sc.results = maps.Clone(base.results)
			sc.results[ciScopeJob] = c.result
			sc.scope = c.scope
			if c.scope == nil {
				sc.scope = map[string]string{}
			}
			if pass, log := runPRGateStep(t, step, sc); pass || !regexp.MustCompile(`::error::SCOPE\b`).MatchString(log) {
				t.Errorf("%s, %s: gate pass = %v, want red naming SCOPE\n%s", event, c.name, pass, log)
			}
		}
	}

	// The Dolt-tier skip needs both halves: the scope turned the tiers off
	// and the call reports it ran without them.
	mg, _ := prScopeScenario(t, "merge_group", ciScopeFor(t, "merge_group"))
	mg.callWith = maps.Clone(mg.callWith)
	mg.callWith["dolt-tiers"] = "on" // a call that ran the Dolt tiers, yet they reported nothing
	if pass, log := runPRGateStep(t, step, mg); pass || !regexp.MustCompile(`::error::BAZEL_EMBEDDED\b`).MatchString(log) {
		t.Errorf("merge group, the call reports dolt-tiers on but the Dolt-tier lanes reported nothing: gate pass = %v, want red\n%s", pass, log)
	}
	prRun, _ := prScopeScenario(t, "pull_request", ciScopeFor(t, "pull_request"))
	prRun.callWith = maps.Clone(prRun.callWith)
	prRun.callWith["dolt-tiers"] = "off"
	for lane := range prRun.outputs {
		if slices.Contains(bazelDoltTierLanes, lane) {
			delete(prRun.outputs, lane)
		}
	}
	if pass, log := runPRGateStep(t, step, prRun); pass || !regexp.MustCompile(`::error::BAZEL_EMBEDDED\b`).MatchString(log) {
		t.Errorf("pull request, the call ran without the Dolt tiers: gate pass = %v, want red (the scope runs them on a PR)\n%s", pass, log)
	}
}

// withScope: scope with one output replaced ("" deletes it).
func withScope(scope map[string]string, key, value string) map[string]string {
	out := maps.Clone(scope)
	if value == "" {
		delete(out, key)
	} else {
		out[key] = value
	}
	return out
}

// ciScopeEvents: the events the two workflows run on, in a fixed order.
func ciScopeEvents() []string { return []string{"pull_request", "merge_group", "push"} }

// runRiskGateStep runs pr-risk.yml's real gate step with the jobs' results
// (default success) and the scope job's outputs.
func runRiskGateStep(t *testing.T, step ciWorkflowStep, event string, results, scope map[string]string) (bool, string) {
	t.Helper()
	re := regexp.MustCompile(`^\$\{\{ needs\.([A-Za-z0-9_-]+)\.(result|outputs\.([A-Za-z0-9_-]+)) \}\}$`)
	env := []string{"PATH=" + os.Getenv("PATH"), "GITHUB_EVENT_NAME=" + event}
	for key, value := range step.Env {
		if !strings.Contains(value, "${{") {
			env = append(env, key+"="+value)
			continue
		}
		m := re.FindStringSubmatch(value)
		if m == nil {
			t.Fatalf("pr-risk.yml gate env %s = %q: the simulation cannot evaluate it", key, value)
		}
		got := ""
		switch {
		case m[2] == "result":
			got = "success"
			if r, ok := results[m[1]]; ok {
				got = r
			}
		case m[1] == ciScopeJob:
			got = scope[m[3]]
		default:
			t.Fatalf("pr-risk.yml gate env %s = %q: the simulation cannot evaluate it", key, value)
		}
		env = append(env, key+"="+got)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", step.Run)
	cmd.Dir = sourceRepoRoot(t)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

// TestCIScopeRiskGate: pr-risk.yml's gate the same way: TEST_NIX may be
// skipped exactly where the scope turned the Nix job off (a merge group);
// it runs, and must pass, on a pull request and on a push to main.
func TestCIScopeRiskGate(t *testing.T) {
	requireHostTool(t, "bash")
	w := readCIWorkflow(t, prRiskWorkflowName)
	step := w.job(t, "ci-gate").step(t, "Evaluate CI gate")
	if got := strings.Fields(step.Env["CI_GATE_REQUIRED"]); !slices.Equal(got, []string{"SCOPE", "TEST_NIX"}) {
		t.Errorf("%s gate CI_GATE_REQUIRED = %v, want [SCOPE TEST_NIX]", prRiskWorkflowName, got)
	}
	for _, event := range ciScopeEvents() {
		scope := ciScopeFor(t, event)
		nix := "skipped"
		if evalRRCIf(t, w.job(t, "test-nix").If, ciScopeCtx(scope)) {
			nix = "success"
		}
		if want := map[bool]string{true: "success", false: "skipped"}[event != "merge_group"]; nix != want {
			t.Errorf("%s: test-nix %s, want %s", event, nix, want)
		}
		pass, log := runRiskGateStep(t, step, event, map[string]string{"test-nix": nix}, scope)
		if !pass {
			t.Errorf("%s, test-nix %s: gate red\n%s", event, nix, log)
		}
		want := []string(nil)
		if nix == "skipped" {
			want = []string{"TEST_NIX"}
		}
		if got := ciGateAllowedSkips(log); !slices.Equal(got, want) {
			t.Errorf("%s: gate accepted skips %v, want exactly %v", event, got, want)
		}
		for _, r := range []string{"failure", "cancelled", "skipped"} {
			if r == "skipped" && nix == "skipped" {
				continue
			}
			if pass, log := runRiskGateStep(t, step, event, map[string]string{"test-nix": r}, scope); pass || !strings.Contains(log, "::error::TEST_NIX") {
				t.Errorf("%s, test-nix %s: gate pass = %v, want red naming TEST_NIX\n%s", event, r, pass, log)
			}
		}
		for name, c := range map[string]struct {
			result string
			scope  map[string]string
		}{
			"scope failed":            {"failure", map[string]string{}},
			"scope without outputs":   {"success", map[string]string{}},
			"scope output not a row":  {"success", withScope(scope, "dolt-tiers", map[string]string{"on": "off", "off": "on"}[scope["dolt-tiers"]])},
			"scope output upper-case": {"success", withScope(scope, "platforms", strings.ToUpper(scope["platforms"]))},
		} {
			if pass, log := runRiskGateStep(t, step, event, map[string]string{ciScopeJob: c.result, "test-nix": nix}, c.scope); pass || !strings.Contains(log, "::error::SCOPE") {
				t.Errorf("%s, %s: gate pass = %v, want red naming SCOPE\n%s", event, name, pass, log)
			}
		}
	}
}

// TestCIScopeSkipsNeedARow: ci-scope.sh's result and skips accept only an
// exact row of the decision table: anything else is an invalid scope and
// excuses nothing.
func TestCIScopeSkipsNeedARow(t *testing.T) {
	ids := map[string]string{"CI_SCOPE_PLATFORMS_IDS": "P1 P2", "CI_SCOPE_LINUX_IDS": "L1"}
	env := func(result string, scope map[string]string) map[string]string {
		e := maps.Clone(ids)
		e["SCOPE_RESULT"] = result
		for _, o := range ciScopeOutputs {
			e["SCOPE_"+ciScopeEnvName(o)] = scope[o]
		}
		return e
	}
	wantSkips := map[string]string{"pull_request": "", "merge_group": "P1 P2", "push": "L1"}
	for _, event := range ciScopeEvents() {
		scope := ciScopeRows[event]
		if out, _, err := runCIScope(t, env("success", scope), "result"); err != nil || out != "success" {
			t.Errorf("%s row: result %q (%v), want success", event, out, err)
		}
		if out, _, err := runCIScope(t, env("success", scope), "skips"); err != nil || out != wantSkips[event] {
			t.Errorf("%s row: skips %q (%v), want %q", event, out, err, wantSkips[event])
		}
		for _, r := range []string{"failure", "cancelled", "skipped", ""} {
			if out, _, err := runCIScope(t, env(r, scope), "result"); err != nil || out != r {
				t.Errorf("%s row, scope job %q: result %q (%v), want the job's result", event, r, out, err)
			}
		}
	}
	for name, scope := range map[string]map[string]string{
		"empty":                        {},
		"platforms and linux both off": {"platforms": "false", "dolt-tiers": "off", "linux": "false", "advisory": "false"},
		"upper case":                   {"platforms": "FALSE", "dolt-tiers": "off", "linux": "true", "advisory": "false"},
		"padded":                       {"platforms": "false ", "dolt-tiers": "off", "linux": "true", "advisory": "false"},
		"injected":                     {"platforms": "false\n::error::x", "dolt-tiers": "off", "linux": "true", "advisory": "false"},
		"missing advisory":             {"platforms": "false", "dolt-tiers": "off", "linux": "true"},
	} {
		out, _, err := runCIScope(t, env("success", scope), "result")
		if err != nil || !regexp.MustCompile(`^invalid-scope:[A-Za-z0-9=,-]*$`).MatchString(out) {
			t.Errorf("%s: result %q (%v), want one invalid-scope:<sanitized> line", name, out, err)
		}
		if out, _, err := runCIScope(t, env("success", scope), "skips"); err != nil || out != "" {
			t.Errorf("%s: skips %q (%v), want none", name, out, err)
		}
	}
}

// TestBazelGateDoltTiersCrossCheck: bazel-gate.sh excuses the Dolt-tier
// lanes only when the scope turned the tiers off and the call reports it ran
// without them, in a valid mode; a call that never started (pr.yml's push run)
// reads skipped for its aggregate and its remote check, which only the
// scope's own skips may excuse.
func TestBazelGateDoltTiersCrossCheck(t *testing.T) {
	requireHostTool(t, "bash")
	run := func(arg string, env map[string]string) string {
		t.Helper()
		cmd := exec.Command("bash", bazelGateScript, arg)
		cmd.Dir = sourceRepoRoot(t)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %s %v: %v", bazelGateScript, arg, env, err)
		}
		return strings.TrimSpace(string(out))
	}
	for _, mode := range bazelRBEModes {
		base := run("skips", map[string]string{"BAZEL_RBE_MODE": mode, "BAZEL_RBE_ENABLED": bazelModeEnabled(mode)})
		for _, scopeDolt := range []string{"on", "off", ""} {
			for _, callDolt := range []string{"on", "off", ""} {
				got := strings.Fields(run("skips", map[string]string{"BAZEL_RBE_MODE": mode, "BAZEL_RBE_ENABLED": bazelModeEnabled(mode), "SCOPE_DOLT_TIERS": scopeDolt, "BAZEL_DOLT_TIERS": callDolt}))
				want := strings.Fields(base)
				if scopeDolt == "off" && callDolt == "off" {
					for _, id := range bazelDoltTierGateIDs {
						if !slices.Contains(want, id) {
							want = append(want, id)
						}
					}
				}
				slices.Sort(got)
				slices.Sort(want)
				if !slices.Equal(slices.Compact(got), want) {
					t.Errorf("mode %s, scope dolt-tiers %q, call dolt-tiers %q: skips %v, want %v", mode, scopeDolt, callDolt, got, want)
				}
			}
		}
	}
	if got := run("skips", map[string]string{"BAZEL_RBE_MODE": "bogus", "BAZEL_RBE_ENABLED": "true", "SCOPE_DOLT_TIERS": "off", "BAZEL_DOLT_TIERS": "off"}); got != "" {
		t.Errorf("invalid mode with the Dolt tiers off: skips %q, want none", got)
	}
	for _, c := range []struct {
		name, call, mode, enabled string
		aggregate, remote         string
	}{
		{"call never started", "skipped", "", "", "skipped", "skipped"},
		{"rbe job failed", "failure", "", "", "invalid-rbe-mode:/", "not-run-remotely"},
		{"call cancelled before the decision", "cancelled", "", "", "invalid-rbe-mode:/", "not-run-remotely"},
		{"remote", "success", "remote", "true", "success", "success"},
		{"fork-ro", "success", "fork-ro", "true", "success", "success"},
		{"cache", "success", "cache", "false", "success", "not-run-remotely"},
		{"skip", "success", "skip", "false", "success", "not-run-remotely"},
		{"inconsistent", "success", "remote", "false", "invalid-rbe-mode:remote/false", "not-run-remotely"},
		{"skipped with a mode", "skipped", "remote", "true", "skipped", "success"},
	} {
		env := map[string]string{"BAZEL_CALL": c.call, "BAZEL_RBE_MODE": c.mode, "BAZEL_RBE_ENABLED": c.enabled}
		if got := run("aggregate", env); got != c.aggregate {
			t.Errorf("%s: aggregate %q, want %q", c.name, got, c.aggregate)
		}
		if got := run("remote", env); got != c.remote {
			t.Errorf("%s: remote %q, want %q", c.name, got, c.remote)
		}
		if c.name == "call never started" {
			if got := run("skips", env); got != "" {
				t.Errorf("%s: skips %q, want none (the scope excuses what it turned off)", c.name, got)
			}
		}
	}
}

// TestBazelDoltTiersInput: bazel.yml's dolt-tiers input (a string, default
// "on": push, dispatch, nightly.yml and bazel-farm.yml keep every lane) turns
// off exactly the Dolt-tier lanes when it is "off", in every mode; nothing
// else reads it; the call reports what it did as its dolt-tiers output, which
// agrees with the lanes for every value; and only pr.yml passes it, from the
// scope.
func TestBazelDoltTiersInput(t *testing.T) {
	call := readBazelWorkflowCall(t)
	if in, ok := call.Inputs["dolt-tiers"]; !ok || in.Type != "string" || in.Default != "on" {
		t.Errorf("%s workflow_call input dolt-tiers = %+v (present %v), want type string, default on", bazelWorkflowName, in, ok)
	}
	if got := call.Outputs["dolt-tiers"].Value; got != bazelDoltTiersOutput {
		t.Errorf("%s workflow_call output dolt-tiers = %q, want %q", bazelWorkflowName, got, bazelDoltTiersOutput)
	}
	workflow := readCIWorkflow(t, bazelWorkflowName)
	suffix := " && " + bazelDoltTiersCond + " }}"
	for name, job := range workflow.Jobs {
		dolt := slices.Contains(bazelDoltTierLanes, name)
		if got := strings.HasSuffix(job.If, suffix); got != dolt {
			t.Errorf("%s if %q ends with the Dolt-tier switch: %v, want %v", name, job.If, got, dolt)
		}
		if strings.Count(job.If, "inputs.dolt-tiers") > btoi(dolt) {
			t.Errorf("%s if %q reads inputs.dolt-tiers outside the switch", name, job.If)
		}
		for _, s := range job.Steps {
			if strings.Contains(fmt.Sprint(s.If, s.Run, s.Env, s.With), "dolt-tiers") {
				t.Errorf("%s step %q reads dolt-tiers; only the lanes' if may", name, s.Name)
			}
		}
		if strings.Contains(fmt.Sprint(job.Env, job.RunsOn, job.Outputs), "dolt-tiers") {
			t.Errorf("%s reads dolt-tiers outside its if", name)
		}
	}
	for _, name := range bazelDoltTierLanes {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("bazelDoltTierLanes lists %s, which %s does not have", name, bazelWorkflowName)
		}
	}
	// The lanes' switch and the reported output agree on every value a
	// caller could pass (GitHub's == is case-insensitive).
	for _, v := range []any{nil, "", "on", "off", "OFF", "Off", "no", "false", false, true} {
		ctx := map[string]any{"inputs.dolt-tiers": v}
		runs, err := evalGHExprTyped("${{ "+bazelDoltTiersCond+" }}", ctx)
		if err != nil {
			t.Fatal(err)
		}
		out, err := evalGHExprTyped(bazelDoltTiersOutput, ctx)
		if err != nil {
			t.Fatal(err)
		}
		if (out == "on") != ghTruthy(runs) || (out != "on" && out != "off") {
			t.Errorf("dolt-tiers %#v: lanes run %v, the call reports %q", v, runs, out)
		}
	}
	// In every mode "off" stops exactly the Dolt-tier lanes that would run.
	for _, mode := range bazelRBEModes {
		for name, job := range workflow.Jobs {
			if name == bazelRBEJobName || isBazelRRCJob(name) {
				continue
			}
			for _, with := range []map[string]string{{}, {"integration": "on", "package-gates": "on"}} {
				on := bazelLaneRunModes(t, name, job.If, with)[mode]
				offWith := maps.Clone(with)
				offWith["dolt-tiers"] = "off"
				off := bazelLaneRunModes(t, name, job.If, offWith)[mode]
				if want := on && !slices.Contains(bazelDoltTierLanes, name); off != want {
					t.Errorf("mode %s, %s (with %v): runs with dolt-tiers off = %v, want %v", mode, name, with, off, want)
				}
			}
		}
	}
	// Only pr.yml passes it, and only the scope's switch.
	entries, err := os.ReadDir(filepath.Join(sourceRepoRoot(t), ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") || e.Name() == bazelWorkflowName {
			continue
		}
		for jobName, job := range readCIWorkflow(t, e.Name()).Jobs {
			v, ok := job.With["dolt-tiers"]
			if !ok || !strings.HasSuffix(job.Uses, "/"+bazelWorkflowName) {
				continue
			}
			if e.Name() != "pr.yml" || jobName != "bazel" || v != "${{ needs."+ciScopeJob+".outputs.dolt-tiers }}" {
				t.Errorf("%s job %s passes dolt-tiers %q to %s; only pr.yml's call may, from its scope", e.Name(), jobName, v, bazelWorkflowName)
			}
		}
	}
}

// TestCIScopeCoversEveryGatedJobOnMain is the invariant the fast set rests
// on: every job pr.yml's and pr-risk.yml's gates read on a pull request runs
// on every push head on main, in the merge group of the batch's last entry
// (main fast-forwards to that tested merge-group commit) or in main's push
// runs: pr.yml's and pr-risk.yml's (the Windows, macOS and Nix jobs) and
// bazel.yml's own push run (every Bazel lane: it has no inputs, so neither
// the Dolt tiers nor the integration build's //cmd/bd:bd_test are turned
// off). The merge group runs the fast set, the push runs the rest (and
// TestCIScopeExcusedIDsRedTheirPushRun: turn red when it fails). Jobs the
// gates do not read (advisory-reports) are not covered: pull requests only.
func TestCIScopeCoversEveryGatedJobOnMain(t *testing.T) {
	type where struct{ mergeGroup, push bool }
	runs := map[string]where{} // gate id -> where it runs after the PR run
	for name := range ciScopeJobSwitch {
		w := readCIWorkflow(t, name)
		step := w.job(t, "ci-gate").step(t, "Evaluate CI gate")
		for key, value := range step.Env {
			m := regexp.MustCompile(`^\$\{\{ needs\.([a-z0-9-]+)\.(result|outputs\.[a-z0-9-]+)`).FindStringSubmatch(value)
			if m == nil || m[1] == ciScopeJob || m[1] == "bazel" {
				continue
			}
			sw := ciScopeJobSwitch[name][m[1]]
			runs[key] = where{ciScopeRows["merge_group"][sw] == "true", ciScopeRows["push"][sw] == "true"}
		}
	}
	// The Bazel lanes, mode remote: pr.yml's merge-group call, and
	// bazel.yml's own push run (no inputs at all: every input reads null).
	mgLanes := bazelPRCallLanesFor(t, "merge_group")
	pushLanes := map[string]map[string]bool{}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if name != bazelRBEJobName && !isBazelRRCJob(name) {
			pushLanes[name] = bazelLaneRunModes(t, name, job.If, map[string]string{})
		}
	}
	bazelIDs := maps.Clone(bazelLaneGateIDs)
	for lane, g := range bazelFlagGatedLanes {
		bazelIDs[lane] = g.id
	}
	for lane, id := range bazelIDs {
		runs[id] = where{bazelLaneReporter(mgLanes, lane, "remote") != "", bazelLaneReporter(pushLanes, lane, "remote") != ""}
	}
	for id, w := range runs {
		if !w.mergeGroup && !w.push {
			t.Errorf("%s runs in neither the merge group nor main's push runs: a push head on main would never run it", id)
		}
	}
	// Where each set runs, spelled out.
	for _, id := range slices.Concat(ciScopeGateIDs["pr.yml"]["platforms"], ciScopeGateIDs[prRiskWorkflowName]["platforms"], bazelDoltTierGateIDs) {
		if w := runs[id]; w.mergeGroup || !w.push {
			t.Errorf("%s: merge group %v, push %v; want the push run only (the complement)", id, w.mergeGroup, w.push)
		}
	}
	for _, id := range []string{"FAST_CHECKS", "CHECK_MIGRATION_HYGIENE", "CHECK_NO_BEADS_CHANGES", "CHECK_DOC_FLAGS", "PACKAGE_MCP", "PACKAGE_NPM", "BAZEL_TEST", "BAZEL_PURE", "BAZEL_RELEASE_CROSS", "BAZEL_INTEGRATION"} {
		if w := runs[id]; !w.mergeGroup {
			t.Errorf("%s does not run in the merge group: it is the fast set", id)
		}
	}
	// The integration build's //cmd/bd:bd_test: pr.yml's call leaves it to
	// the cmd/bd Dolt-server tier (cmd-dolt-required), which a merge group
	// skips, so the merge group runs it nowhere; the push run runs it in both
	// (no cmd-dolt-required: bazel-integration keeps it, and the tier runs).
	exclude := readCIWorkflow(t, bazelWorkflowName).job(t, bazelIntegJobName).step(t, "bazel test //... --config=integration").Env["EXCLUDE_TARGETS"]
	for _, c := range []struct {
		name string
		ctx  map[string]any
		want string
	}{
		{"merge group call", map[string]any{"inputs.cmd-dolt-required": true}, "-//cmd/bd:bd_test"},
		{"push run", map[string]any{}, ""},
	} {
		if got, err := evalGHExprTyped(exclude, c.ctx); err != nil || got != c.want {
			t.Errorf("%s: bazel-integration EXCLUDE_TARGETS = %#v (%v), want %q", c.name, got, err, c.want)
		}
	}
	if with := prCallWithFor(t, "merge_group"); with["cmd-dolt-required"] != "true" || bazelLaneReporter(mgLanes, bazelCmdDoltJobName, "remote") != "" {
		t.Errorf("merge group call: cmd-dolt-required %q, cmd/bd tier reporter %q; want bd_test left to the push run", with["cmd-dolt-required"], bazelLaneReporter(mgLanes, bazelCmdDoltJobName, "remote"))
	}
	if bazelLaneReporter(pushLanes, bazelCmdDoltJobName, "remote") == "" {
		t.Error("bazel.yml's push run does not run the cmd/bd Dolt-server tier")
	}
	// pr.yml never calls bazel.yml on a push: bazel.yml's own push run is
	// every lane, and its rrc-seed mints only for a main-push bazel.yml token.
	if evalRRCIf(t, readCIWorkflow(t, "pr.yml").job(t, "bazel").If, ciScopeCtx(ciScopeRows["push"])) {
		t.Error("pr.yml calls bazel.yml on a push to main; bazel.yml's own push run covers the lanes")
	}
	var on struct {
		On map[string]yaml.Node `yaml:"on"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)), &on); err != nil {
		t.Fatal(err)
	}
	var push struct {
		Branches []string `yaml:"branches"`
	}
	if node, ok := on.On["push"]; !ok || node.Decode(&push) != nil || !slices.Equal(push.Branches, []string{"main"}) {
		t.Errorf("%s push trigger = %v; want push to main (the Bazel half of the complement)", bazelWorkflowName, push.Branches)
	}
}

// TestCIScopeExcusedIDsRedTheirPushRun: every gate id a merge group's gates
// excuse (read from the gates' own merge-group runs) turns main's push run red
// when its job fails there, which main-ci-alert.yml reports; running on the
// push is not enough.
//   - pr.yml's and pr-risk.yml's own jobs (the Windows, macOS and Nix jobs):
//     the job runs on the push, carries no job-level continue-on-error (its
//     failure fails the run), and the push run's real gate step goes red on
//     its failure.
//   - bazel.yml's Dolt-tier lanes, in its own push run (mode remote, no
//     inputs): the job reporting the id runs and carries no job-level
//     continue-on-error, and the tier's failure fails it. A lane's job.status
//     is its result; bazel-dolt-int's per-tier results fail it through the
//     classify step's --gate-tier (server-storage) or through a later step
//     that exits non-zero on the tier's failure outside pr.yml's call, and
//     only there: in pr.yml's call (pull_request, merge_group) the gate reads
//     the per-tier output, so a PR shows the failure exactly as before.
func TestCIScopeExcusedIDsRedTheirPushRun(t *testing.T) {
	requireHostTool(t, "bash")
	pr := readCIWorkflow(t, "pr.yml")
	prStep := pr.job(t, "ci-gate").step(t, "Evaluate CI gate")
	risk := readCIWorkflow(t, prRiskWorkflowName)
	riskStep := risk.job(t, "ci-gate").step(t, "Evaluate CI gate")
	mgScope, pushScope := ciScopeFor(t, "merge_group"), ciScopeFor(t, "push")

	excused := map[string]string{} // gate id -> the workflow whose gate excuses it
	mg, _ := prScopeScenario(t, "merge_group", mgScope)
	if pass, log := runPRGateStep(t, prStep, mg); !pass {
		t.Fatalf("pr.yml's merge-group gate is red:\n%s", log)
	} else {
		for _, id := range ciGateAllowedSkips(log) {
			excused[id] = "pr.yml"
		}
	}
	if pass, log := runRiskGateStep(t, riskStep, "merge_group", map[string]string{"test-nix": "skipped"}, mgScope); !pass {
		t.Fatalf("%s's merge-group gate is red:\n%s", prRiskWorkflowName, log)
	} else {
		for _, id := range ciGateAllowedSkips(log) {
			excused[id] = prRiskWorkflowName
		}
	}
	if len(excused) != len(ciScopeGateIDs["pr.yml"]["platforms"])+len(ciScopeGateIDs[prRiskWorkflowName]["platforms"])+len(bazelDoltTierGateIDs) {
		t.Fatalf("the merge group's gates excuse %v; want the Windows and macOS ids, TEST_NIX and the Dolt-tier ids", slices.Sorted(maps.Keys(excused)))
	}

	bazel := readCIWorkflow(t, bazelWorkflowName)
	pushLanes := map[string]map[string]bool{} // bazel.yml's own push run: no inputs
	for name, job := range bazel.Jobs {
		if name != bazelRBEJobName && !isBazelRRCJob(name) {
			pushLanes[name] = bazelLaneRunModes(t, name, job.If, map[string]string{})
		}
	}
	laneOf := map[string]string{} // gate id -> bazel.yml lane
	for lane, id := range bazelLaneGateIDs {
		laneOf[id] = lane
	}
	for lane, g := range bazelFlagGatedLanes {
		laneOf[g.id] = lane
	}
	needsResult := regexp.MustCompile(`^\$\{\{ needs\.([a-z0-9-]+)\.result \}\}$`)

	for id, wf := range excused {
		if lane, ok := laneOf[id]; ok && wf == "pr.yml" {
			reporter := bazelLaneReporter(pushLanes, lane, "remote")
			if reporter == "" {
				t.Errorf("%s: no job of bazel.yml's push run (mode remote) reports it", id)
				continue
			}
			job := bazel.job(t, reporter)
			if job.ContinueOnError {
				t.Errorf("%s: %s carries job-level continue-on-error; its failure would leave main's push run green", id, reporter)
			}
			key := "result"
			if bazelUnionOf(lane) == reporter && bazelUnionMemberOutputKey[lane] != "" {
				key = bazelUnionMemberOutputKey[lane]
			}
			if key == "result" {
				if job.Outputs["result"] != "${{ steps.result.outputs.result }}" {
					t.Errorf("%s: %s reports %v, not its job.status", id, reporter, job.Outputs)
				}
				continue // the job's own status: its failure is the run's
			}
			tier := strings.ReplaceAll(strings.TrimSuffix(key, "_result"), "_", "-")
			if strings.Contains(job.step(t, "Classify tier results").Run, "--gate-tier "+tier+" ") {
				continue // the classify step exits 1 on this tier's failure
			}
			failing := 0
			for _, step := range job.Steps {
				if !strings.Contains(step.If, "steps.classify.outputs."+key) {
					continue
				}
				// always() is true in a job that got this far.
				expr := strings.ReplaceAll(step.If, "always()", "true")
				runs := func(event, result string) bool {
					return evalRRCIf(t, expr, map[string]string{"github.event_name": event, "steps.classify.outputs." + key: result})
				}
				if !runs("push", "failure") {
					continue
				}
				failing++
				for _, event := range []string{"schedule", "workflow_dispatch", "pull_request_target"} {
					if !runs(event, "failure") {
						t.Errorf("%s: %s step %q does not fail a %s run on the %s tier's failure, where no gate reads %s", id, reporter, step.Name, event, tier, key)
					}
				}
				for _, event := range []string{"pull_request", "merge_group"} {
					if runs(event, "failure") {
						t.Errorf("%s: %s step %q fails pr.yml's %s call too, whose gate reads %s (a PR would show it twice)", id, reporter, step.Name, event, key)
					}
				}
				if runs("push", "success") || runs("push", "") {
					t.Errorf("%s: %s step %q runs without the %s tier's failure", id, reporter, step.Name, tier)
				}
				if step.ContinueOnError != nil && step.ContinueOnError != false {
					t.Errorf("%s: %s step %q carries continue-on-error", id, reporter, step.Name)
				}
				cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", step.Run)
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_EVENT_NAME=push"}
				if out, err := cmd.CombinedOutput(); err == nil {
					t.Errorf("%s: %s step %q exits 0 on the %s tier's failure:\n%s", id, reporter, step.Name, tier, out)
				}
			}
			if failing == 0 {
				t.Errorf("%s: a %s tier failure leaves %s green in main's push run (no --gate-tier %s, no step failing on %s); main-ci-alert.yml would never see it", id, tier, reporter, tier, key)
			}
			continue
		}
		// One of the workflow's own jobs.
		w, step := pr, prStep
		if wf == prRiskWorkflowName {
			w, step = risk, riskStep
		}
		m := needsResult.FindStringSubmatch(step.Env[id])
		if m == nil {
			t.Errorf("%s gate env %s = %q: want a job's result", wf, id, step.Env[id])
			continue
		}
		job := w.job(t, m[1])
		if !evalRRCIf(t, job.If, ciScopeCtx(pushScope)) || job.ContinueOnError {
			t.Errorf("%s: %s job %s on push: runs %v, continue-on-error %v; want it to run and to fail the run when it fails", id, wf, m[1], evalRRCIf(t, job.If, ciScopeCtx(pushScope)), job.ContinueOnError)
		}
		var pass bool
		var log string
		if wf == "pr.yml" {
			sc, _ := prScopeScenario(t, "push", pushScope)
			sc.results = maps.Clone(sc.results)
			sc.results[m[1]] = "failure"
			pass, log = runPRGateStep(t, step, sc)
		} else {
			pass, log = runRiskGateStep(t, step, "push", map[string]string{m[1]: "failure"}, pushScope)
		}
		if pass || !regexp.MustCompile(`::error::`+id+`\b`).MatchString(log) {
			t.Errorf("%s: %s failed on the push to main, %s's gate pass = %v; want red naming %s\n%s", id, m[1], wf, pass, id, log)
		}
	}
}

// TestCIScopeWorkflowsRunOnPushToMain: both workflows add exactly a push to
// main to their pull_request and merge_group triggers (the complement runs
// there), with no path filter.
func TestCIScopeWorkflowsRunOnPushToMain(t *testing.T) {
	for name := range ciScopeJobSwitch {
		var doc struct {
			On map[string]yaml.Node `yaml:"on"`
		}
		if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+name)), &doc); err != nil {
			t.Fatal(err)
		}
		if got := slices.Sorted(maps.Keys(doc.On)); !slices.Equal(got, []string{"merge_group", "pull_request", "push"}) {
			t.Errorf("%s triggers = %v, want merge_group, pull_request and push", name, got)
		}
		var push map[string]any
		if node, ok := doc.On["push"]; !ok || node.Decode(&push) != nil || !reflect.DeepEqual(push, map[string]any{"branches": []any{"main"}}) {
			t.Errorf("%s push trigger = %v, want exactly branches: [main]", name, push)
		}
	}
}

// TestCIScopePushRunsHandleNullPullRequestFields: a push run has no
// pull_request and no merge_group payload. Every job that now also runs on a
// push to main reads neither, except through the Blacksmith venue ternary
// (which falls back to ubuntu-latest there) and the concurrency group (one
// group per pushed commit, TestCIScopePushConcurrency).
func TestCIScopePushRunsHandleNullPullRequestFields(t *testing.T) {
	read := regexp.MustCompile(`github\.event\.(pull_request|merge_group)\.`)
	pushCtx := map[string]string{"github.event_name": "push", "github.repository": mergeQueueOwnRepo, "github.actor": "alice", "github.ref": "refs/heads/main", "github.sha": strings.Repeat("a", 40)}
	jobPath := regexp.MustCompile(`^\.jobs\.([^.]+)\.`)
	for name, switches := range ciScopeJobSwitch {
		onPush := map[string]bool{ciScopeJob: true, "ci-gate": true}
		for job, sw := range switches {
			onPush[job] = ciScopeRows["push"][sw] == "true"
		}
		seen := 0
		walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", name)), "", func(path string, key bool, value string) {
			if key || !read.MatchString(value) || path == ".concurrency.group" {
				return
			}
			m := jobPath.FindStringSubmatch(path)
			if m == nil {
				t.Errorf("%s %s reads %q outside a job", name, path, value)
				return
			}
			if !onPush[m[1]] {
				return
			}
			seen++
			if strings.HasSuffix(path, ".runs-on") {
				if got := mustEvalGHRunsOn(t, value, pushCtx); got != "ubuntu-latest" {
					t.Errorf("%s %s on push = %q, want ubuntu-latest", name, path, got)
				}
				return
			}
			t.Errorf("%s %s reads %q, which is null on a push to main; this job runs there now", name, path, value)
		})
		if seen == 0 {
			t.Errorf("%s: found no push-run job reading a pull_request field; the walk is broken (the gates' runs-on reads one)", name)
		}
	}
}

// TestCIScopePushConcurrency: on a push to main both workflows (and
// bazel.yml, the Bazel half of the complement) key their concurrency group on
// the pushed commit, so no push run cancels another or waits behind it
// (GitHub cancels a group's pending run when a newer one queues): every
// push's complement runs to completion. Pull requests and merge groups
// keep their groups (TestMergeQueueConcurrencyNeverCancelsQueueRuns).
func TestCIScopePushConcurrency(t *testing.T) {
	for _, name := range []string{"pr.yml", prRiskWorkflowName, bazelWorkflowName} {
		var doc struct {
			Concurrency struct {
				Group            string `yaml:"group"`
				CancelInProgress any    `yaml:"cancel-in-progress"`
			} `yaml:"concurrency"`
		}
		if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+name)), &doc); err != nil {
			t.Fatal(err)
		}
		push := func(sha string) map[string]string {
			return map[string]string{"github.workflow": strings.TrimSuffix(name, ".yml"), "github.event_name": "push", "github.ref": "refs/heads/main", "github.sha": sha}
		}
		a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
		ga, gb := interpolateGH(t, doc.Concurrency.Group, push(a)), interpolateGH(t, doc.Concurrency.Group, push(b))
		if ga == gb || !strings.Contains(ga, a) {
			t.Errorf("%s concurrency group on push: %q and %q; want one group per pushed commit", name, ga, gb)
		}
		for _, other := range []map[string]string{mergeGroupCtx(1, map[string]string{"github.sha": a}), pullRequestCtx(1, map[string]string{"github.sha": a})} {
			ctx := maps.Clone(other)
			ctx["github.workflow"] = strings.TrimSuffix(name, ".yml")
			if g := interpolateGH(t, doc.Concurrency.Group, ctx); g == ga || strings.Contains(g, a) {
				t.Errorf("%s concurrency group for %s = %q: keyed on the commit (or equal to the push group %q); want the PR number or the queue ref", name, other["github.event_name"], g, ga)
			}
		}
	}
}
