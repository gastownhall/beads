package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// D2 step 1: PR Risk's legacy embedded-Dolt test jobs stand down on the PRs
// where pr.yml's gated Bazel `embedded Dolt tier` lane (bazel.yml's
// bazel-embedded, remote mode only) runs the same tier, and nowhere else.
// pr-risk.yml's bazel-embedded-coverage job makes that call; its gate accepts
// the skips only when that job says so.

const (
	prRiskWorkflowName     = "pr-risk.yml"
	prRiskCoverageJobName  = "bazel-embedded-coverage"
	prRiskCoverageCovered  = "${{ steps.decide.outputs.covered }}"
	prRiskPullRequestValue = "${{ github.event_name == 'pull_request' }}"
	// The legacy jobs' if: the existing risk tier, and not covered.
	prRiskBazelCoveredIf = "needs.detect-ci-tier.outputs.full_embedded == 'true' && needs." + prRiskCoverageJobName + ".outputs.covered != 'true'"
)

// The retired legacy jobs and their gate ids. build-embedded is not one: its
// artifact also feeds the proxied and server Dolt jobs.
var prRiskBazelCoveredJobs = []string{"test-embedded-storage", "test-embedded-conformance", "test-embedded-cmd"}

var prRiskBazelCoveredIDs = map[string]string{
	"test-embedded-storage":     "TEST_EMBEDDED_STORAGE",
	"test-embedded-conformance": "TEST_EMBEDDED_CONFORMANCE",
	"test-embedded-cmd":         "TEST_EMBEDDED_CMD",
}

// The bazel.yml rbe step env keys pr-risk.yml's decision copies verbatim.
var prRiskSharedDecisionEnv = []string{"RBE_VAR_ON", "FORK", "HAS_EXECUTOR"}

// rbeFacts: what GitHub evaluates both decision steps' env expressions on.
type rbeFacts struct {
	event  string // github.event_name
	rbeVar string // vars.RBE_WEST_WORKERS ("" = unset)
	secret string // secrets.RBE_WEST_EXECUTOR ("" = unavailable: fork, Dependabot, unset)
	fork   bool   // github.event.pull_request.head.repo.fork
}

func (f rbeFacts) String() string {
	return fmt.Sprintf("event=%s var=%q secret=%v fork=%v", f.event, f.rbeVar, f.secret != "", f.fork)
}

// rbeFactsMatrix: every combination of the facts either decision reads.
func rbeFactsMatrix() []rbeFacts {
	var out []rbeFacts
	for _, event := range []string{"pull_request", "merge_group", "push", "workflow_dispatch", "pull_request_target"} {
		for _, v := range []string{"", "true", "True", "TRUE", "false", "1", "yes"} {
			for _, secret := range []string{"", "grpcs://rbe.example:443"} {
				for _, fork := range []bool{false, true} {
					out = append(out, rbeFacts{event, v, secret, fork})
				}
			}
		}
	}
	return out
}

// evalRBEExpr evaluates the env expressions the two decision steps may use,
// for a bazel.yml call with these inputs (with: the caller's `with:`; unset
// inputs take their defaults). GitHub's == on strings is case-insensitive.
// Any other expression fails the test, so the simulation cannot silently
// drift from the workflows.
func evalRBEExpr(t *testing.T, expr string, f rbeFacts, with map[string]string) string {
	t.Helper()
	input := func(name, def string) string {
		if v, ok := with[name]; ok {
			return v
		}
		return def
	}
	switch expr {
	case prRiskPullRequestValue:
		return strconv.FormatBool(f.event == "pull_request")
	case "${{ vars.RBE_WEST_WORKERS == 'true' }}":
		return strconv.FormatBool(strings.EqualFold(f.rbeVar, "true"))
	case "${{ inputs.rbe == 'off' }}":
		return strconv.FormatBool(strings.EqualFold(input("rbe", "on"), "off"))
	case "${{ github.event.pull_request.head.repo.fork == true }}":
		return strconv.FormatBool(f.fork)
	case bazelForkFarmValue:
		return strconv.FormatBool(strings.EqualFold(input("fork-farm", "off"), "authorized") &&
			f.event == "pull_request_target" && input("checkout-sha", "") != "")
	case bazelRBESecretValue:
		return strconv.FormatBool(f.secret != "")
	}
	t.Fatalf("decision env expression %q: teach evalRBEExpr how GitHub evaluates it", expr)
	return ""
}

// runDecisionStep runs a decision step's script under GitHub's bash flags
// with its env evaluated for f, and returns its $GITHUB_OUTPUT.
func runDecisionStep(t *testing.T, step ciWorkflowStep, f rbeFacts, with map[string]string) (map[string]string, error) {
	t.Helper()
	env := map[string]string{}
	for k, v := range step.Env {
		env[k] = evalRBEExpr(t, v, f, with)
	}
	return runBazelRBEDecision(t, step.Run, env)
}

func prRiskCoverageStep(t *testing.T) ciWorkflowStep {
	t.Helper()
	job := readCIWorkflow(t, prRiskWorkflowName).job(t, prRiskCoverageJobName)
	if len(job.Steps) != 1 {
		t.Fatalf("%s has %d steps, want exactly the decision step", prRiskCoverageJobName, len(job.Steps))
	}
	return job.Steps[0]
}

// The decision job reads the same facts as bazel.yml's rbe job, through the
// same expressions, reads the executor secret only as an emptiness test, and
// runs no repository code. Nothing else in pr-risk.yml reads the facts or
// any secret.
func TestPRRiskBazelEmbeddedCoverageJob(t *testing.T) {
	risk := readCIWorkflow(t, prRiskWorkflowName)
	job := risk.job(t, prRiskCoverageJobName)
	if len(job.Needs) != 0 || job.If != "" || job.RunsOn != "ubuntu-latest" || len(job.Env) != 0 || job.ContinueOnError || job.TimeoutMinutes == 0 {
		t.Errorf("%s: needs %v, if %q, runs-on %q, env %v, continue-on-error %v, timeout %d; want no needs, if, env or continue-on-error, ubuntu-latest, a timeout",
			prRiskCoverageJobName, job.Needs, job.If, job.RunsOn, job.Env, job.ContinueOnError, job.TimeoutMinutes)
	}
	if want := map[string]string{"covered": prRiskCoverageCovered}; !reflect.DeepEqual(job.Outputs, want) {
		t.Errorf("%s outputs = %v, want %v", prRiskCoverageJobName, job.Outputs, want)
	}
	step := prRiskCoverageStep(t)
	rbeStep := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName).Steps[0]
	wantEnv := map[string]string{"PULL_REQUEST": prRiskPullRequestValue}
	for _, k := range prRiskSharedDecisionEnv {
		wantEnv[k] = rbeStep.Env[k]
	}
	if step.ID != "decide" || step.Uses != "" || step.Shell != "" || len(step.With) != 0 || step.If != "" || step.ContinueOnError != nil || !reflect.DeepEqual(step.Env, wantEnv) {
		t.Errorf("%s step: id %q, uses %q, shell %q, with %v, if %q, env %v; want id decide, a plain run step with env %v (bazel.yml's rbe step expressions)",
			prRiskCoverageJobName, step.ID, step.Uses, step.Shell, step.With, step.If, step.Env, wantEnv)
	}
	if strings.Contains(step.Run, "${{") || regexp.MustCompile(`\.github/|\./|source |\bbash\b`).MatchString(step.Run) {
		t.Errorf("%s step runs repository code or interpolates expressions:\n%s", prRiskCoverageJobName, step.Run)
	}

	// Only the decision step reads the facts; only its emptiness test reads
	// a secret.
	stepEnv := ".jobs." + prRiskCoverageJobName + ".steps[0].env."
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	facts := regexp.MustCompile(`(?i)RBE_WEST_WORKERS|head\.repo\.fork|RBE_WEST_EXECUTOR|github\.actor|dependabot`)
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", prRiskWorkflowName)), "", func(path string, key bool, value string) {
		if key {
			return
		}
		if secretRef.MatchString(value) && !(path == stepEnv+"HAS_EXECUTOR" && value == bazelRBESecretValue) {
			t.Errorf("%s: %s reads secrets (%q); only %s's emptiness test may", prRiskWorkflowName, path, value, prRiskCoverageJobName)
		}
		if facts.MatchString(value) && !strings.HasPrefix(path, stepEnv) && !strings.HasPrefix(path, ".jobs."+prRiskCoverageJobName+".steps[0].run") {
			t.Errorf("%s: %s re-derives the Bazel coverage decision (%q); read needs.%s.outputs.covered", prRiskWorkflowName, path, value, prRiskCoverageJobName)
		}
	})
}

// The decision is true exactly when pr.yml's Bazel call takes execution mode
// remote (so its embedded lane runs and pr.yml's gate requires it) on a
// pull_request: both workflows' actual decision scripts, over every
// combination of the facts they read. Forks, Dependabot and other
// secret-less runs, the farm switch unset or off, and every other event keep
// the legacy tier.
func TestPRRiskEmbeddedDecisionMatchesBazelMode(t *testing.T) {
	requireHostTool(t, "bash")
	pr := readCIWorkflow(t, "pr.yml")
	call := pr.job(t, "bazel")
	if call.Uses != "./.github/workflows/"+bazelWorkflowName {
		t.Fatalf("pr.yml bazel job uses %q, want the local %s", call.Uses, bazelWorkflowName)
	}
	riskStep := prRiskCoverageStep(t)
	rbeStep := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName).Steps[0]

	// The Bazel lane PR Risk defers to: remote-only, gated by pr.yml.
	embedded := readCIWorkflow(t, bazelWorkflowName).job(t, bazelEmbedJobName)
	if !bazelLaneRunModes(t, bazelEmbedJobName, embedded.If, call.With)["remote"] {
		t.Fatalf("%s does not run in mode remote (if %q)", bazelEmbedJobName, embedded.If)
	}
	gate := pr.job(t, "ci-gate")
	if !contains(strings.Fields(gate.step(t, "Evaluate CI gate").Env["CI_GATE_REQUIRED"]), bazelLaneGateIDs[bazelEmbedJobName]) || !contains(gate.Needs, "bazel") {
		t.Fatalf("pr.yml's ci-gate no longer requires %s; PR Risk cannot defer to it", bazelLaneGateIDs[bazelEmbedJobName])
	}
	cmd := exec.Command("bash", filepath.Join(sourceRepoRoot(t), bazelGateScript), "skips")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "BAZEL_RBE_MODE=remote", "BAZEL_RBE_ENABLED=true"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if contains(strings.Fields(string(out)), bazelLaneGateIDs[bazelEmbedJobName]) {
		t.Fatalf("pr.yml's gate accepts a skipped %s in mode remote", bazelLaneGateIDs[bazelEmbedJobName])
	}

	// Both workflows run for the same PRs, so a PR Risk run that defers
	// always has a pr.yml run that gates the lane.
	type triggers struct {
		On struct {
			PullRequest struct {
				Branches []string `yaml:"branches"`
				Paths    []string `yaml:"paths"`
				Ignore   []string `yaml:"paths-ignore"`
				Types    []string `yaml:"types"`
			} `yaml:"pull_request"`
		} `yaml:"on"`
	}
	var prOn, riskOn triggers
	for name, dst := range map[string]*triggers{"pr.yml": &prOn, prRiskWorkflowName: &riskOn} {
		if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+name)), dst); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(prOn, riskOn) || len(prOn.On.PullRequest.Paths)+len(prOn.On.PullRequest.Ignore) != 0 {
		t.Fatalf("pull_request triggers differ or are path-filtered: pr.yml %+v, %s %+v", prOn, prRiskWorkflowName, riskOn)
	}

	sawCovered, sawLegacy := false, false
	for _, f := range rbeFactsMatrix() {
		t.Run(f.String(), func(t *testing.T) {
			bazel, err := runDecisionStep(t, rbeStep, f, call.With)
			if err != nil {
				t.Fatalf("bazel.yml rbe step: %v", err)
			}
			risk, err := runDecisionStep(t, riskStep, f, nil)
			if err != nil {
				t.Fatalf("%s decision step: %v", prRiskCoverageJobName, err)
			}
			want := f.event == "pull_request" && bazel["mode"] == "remote" && bazel["enabled"] == "true"
			if got := risk["covered"]; got != strconv.FormatBool(want) || len(risk) != 1 {
				t.Errorf("covered = %q (outputs %v), want %v: pr.yml's Bazel call takes mode %q", got, risk, want, bazel["mode"])
			}
			if want {
				sawCovered = true
			} else {
				sawLegacy = true
			}
		})
	}
	if !sawCovered || !sawLegacy {
		t.Errorf("matrix never exercised both outcomes (covered %v, legacy %v)", sawCovered, sawLegacy)
	}

	// Named cases, for the record.
	for _, c := range []struct {
		name string
		f    rbeFacts
		want string
	}{
		{"same-repo PR, farm on", rbeFacts{"pull_request", "true", "x", false}, "true"},
		{"fork PR", rbeFacts{"pull_request", "true", "", true}, "false"},
		{"fork PR somehow with a secret", rbeFacts{"pull_request", "true", "x", true}, "false"},
		{"Dependabot PR (no Actions secrets)", rbeFacts{"pull_request", "true", "", false}, "false"},
		{"farm switch unset", rbeFacts{"pull_request", "", "x", false}, "false"},
		{"farm switch false", rbeFacts{"pull_request", "false", "x", false}, "false"},
		{"merge_group", rbeFacts{"merge_group", "true", "x", false}, "false"},
	} {
		got, err := runDecisionStep(t, riskStep, c.f, nil)
		if err != nil || got["covered"] != c.want {
			t.Errorf("%s: covered = %v (%v), want %s", c.name, got, err, c.want)
		}
	}
	// A value that is not a boolean fails the job rather than deciding.
	if out, err := runBazelRBEDecision(t, riskStep.Run, map[string]string{
		"PULL_REQUEST": "true", "RBE_VAR_ON": "true", "FORK": "", "HAS_EXECUTOR": "true",
	}); err == nil {
		t.Errorf("decision with FORK='' succeeded with %v; want failure", out)
	}
}

// prRiskGateScenario: what pr-risk.yml's ci-gate sees.
type prRiskGateScenario struct {
	name        string
	results     map[string]string // needs.<job>.result
	outputs     map[string]string // needs.<job>.outputs.<name>, keyed "job.name"
	wantPass    bool
	wantMention string
}

// runPRRiskGateStep runs pr-risk.yml's actual "Evaluate CI gate" step with its
// env evaluated for the scenario. An env expression of any other form fails
// the test.
func runPRRiskGateStep(t *testing.T, step ciWorkflowStep, sc prRiskGateScenario) (bool, string) {
	t.Helper()
	expr := regexp.MustCompile(`^\$\{\{ needs\.([A-Za-z0-9_-]+)\.(result|outputs\.([A-Za-z0-9_-]+)) \}\}$`)
	env := []string{"PATH=" + os.Getenv("PATH"), "GITHUB_EVENT_NAME=pull_request"}
	for key, value := range step.Env {
		if !strings.Contains(value, "${{") {
			env = append(env, key+"="+value)
			continue
		}
		m := expr.FindStringSubmatch(value)
		if m == nil {
			t.Fatalf("pr-risk ci-gate env %s = %q: the gate simulation cannot evaluate it", key, value)
		}
		var got string
		var ok bool
		if m[2] == "result" {
			got, ok = sc.results[m[1]]
		} else {
			got, ok = sc.outputs[m[1]+"."+m[3]], true
		}
		if !ok {
			t.Fatalf("scenario %q has no result for needs.%s", sc.name, m[1])
		}
		env = append(env, key+"="+got)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", step.Run)
	cmd.Dir = sourceRepoRoot(t)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

// The legacy embedded test jobs skip only when covered, and PR Risk's gate
// accepts their skip only then: the jobs' needs and if, the gate's wiring,
// and the actual gate step over every tier x decision, including a missing,
// failed or non-true decision with the jobs skipped (red).
func TestPRRiskLegacyEmbeddedTierDefersToBazelLane(t *testing.T) {
	requireHostTool(t, "bash")
	risk := readCIWorkflow(t, prRiskWorkflowName)
	for _, name := range prRiskBazelCoveredJobs {
		job := risk.job(t, name)
		if job.If != prRiskBazelCoveredIf {
			t.Errorf("%s if = %q, want %q", name, job.If, prRiskBazelCoveredIf)
		}
		if want := []string{"detect-ci-tier", prRiskCoverageJobName, "build-embedded"}; !reflect.DeepEqual([]string(job.Needs), want) {
			t.Errorf("%s needs = %v, want %v", name, job.Needs, want)
		}
	}
	// Every other job ignores the decision (build-embedded's artifact feeds
	// the proxied and server Dolt jobs, which Bazel does not replace here).
	covered := map[string]bool{}
	for _, name := range prRiskBazelCoveredJobs {
		covered[name] = true
	}
	for name, job := range risk.Jobs {
		if covered[name] || name == "ci-gate" || name == prRiskCoverageJobName {
			continue
		}
		if strings.Contains(job.If, prRiskCoverageJobName) || contains(job.Needs, prRiskCoverageJobName) {
			t.Errorf("%s depends on %s; only %v may stand down", name, prRiskCoverageJobName, prRiskBazelCoveredJobs)
		}
	}

	gate := risk.job(t, "ci-gate")
	step := gate.step(t, "Evaluate CI gate")
	required := strings.Fields(step.Env["CI_GATE_REQUIRED"])
	if !contains(gate.Needs, prRiskCoverageJobName) || !contains(required, "BAZEL_EMBEDDED_COVERAGE") ||
		step.Env["BAZEL_EMBEDDED_COVERAGE"] != "${{ needs."+prRiskCoverageJobName+".result }}" ||
		step.Env["BAZEL_COVERS_EMBEDDED"] != "${{ needs."+prRiskCoverageJobName+".outputs.covered }}" {
		t.Errorf("pr-risk ci-gate does not require %s's result and read its covered output: needs %v, env %v", prRiskCoverageJobName, gate.Needs, step.Env)
	}
	for _, id := range prRiskBazelCoveredIDs {
		if !contains(required, id) {
			t.Errorf("pr-risk ci-gate no longer requires %s (it runs wherever the Bazel lane does not)", id)
		}
	}

	// Each gated job's result for (tier, decision), as the jobs' if: produce
	// it: needs.* outputs are strings, so only covered == 'true' skips.
	results := func(full bool, coverageResult, coveredOut string) map[string]string {
		r := map[string]string{}
		for _, job := range gate.Needs {
			r[job] = "success"
		}
		r[prRiskCoverageJobName] = coverageResult
		for name := range risk.Jobs {
			if !contains(gate.Needs, name) || name == "detect-ci-tier" || name == prRiskCoverageJobName || name == "test-nix" {
				continue
			}
			runs := full
			if covered[name] {
				// A failed decision job skips its dependents.
				runs = full && coverageResult == "success" && coveredOut != "true"
			}
			if !runs {
				r[name] = "skipped"
			}
		}
		return r
	}
	outputs := func(full bool, coveredOut string) map[string]string {
		return map[string]string{
			"detect-ci-tier.full_embedded":     strconv.FormatBool(full),
			prRiskCoverageJobName + ".covered": coveredOut,
		}
	}

	var scenarios []prRiskGateScenario
	for _, full := range []bool{true, false} {
		for _, coveredOut := range []string{"true", "false"} {
			name := fmt.Sprintf("full_embedded=%v covered=%s", full, coveredOut)
			r := results(full, "success", coveredOut)
			scenarios = append(scenarios, prRiskGateScenario{name: name + ", as designed", results: r, outputs: outputs(full, coveredOut), wantPass: true})
			for _, id := range prRiskBazelCoveredJobs {
				if r[id] == "skipped" {
					// Skipped by design; if it ran anyway and failed, red.
					bad := copyMap(r)
					bad[id] = "failure"
					scenarios = append(scenarios, prRiskGateScenario{name + ", " + id + " ran and failed", bad, outputs(full, coveredOut), false, prRiskBazelCoveredIDs[id]})
					continue
				}
				for _, res := range []string{"skipped", "failure", "cancelled"} {
					bad := copyMap(r)
					bad[id] = res
					scenarios = append(scenarios, prRiskGateScenario{name + ", " + id + " " + res, bad, outputs(full, coveredOut), false, prRiskBazelCoveredIDs[id]})
				}
			}
			if full {
				// The decision never excuses the jobs it does not retire.
				for _, id := range []string{"build-embedded", "test-proxied-cmd", "test-server-storage", "test-server-storage-full"} {
					bad := copyMap(r)
					bad[id] = "skipped"
					scenarios = append(scenarios, prRiskGateScenario{name + ", " + id + " skipped", bad, outputs(full, coveredOut), false, ""})
				}
			}
		}
	}
	// A failed, cancelled or missing decision, or one that is not exactly
	// 'true', excuses nothing, whatever the jobs did.
	for _, bad := range []struct{ result, covered string }{
		{"failure", ""}, {"cancelled", ""}, {"skipped", ""}, {"success", ""}, {"success", "TRUE "}, {"success", "yes"}, {"success", "1"},
	} {
		r := results(true, "success", "false")
		r[prRiskCoverageJobName] = bad.result
		for _, id := range prRiskBazelCoveredJobs {
			r[id] = "skipped"
		}
		scenarios = append(scenarios, prRiskGateScenario{
			name:    fmt.Sprintf("decision %s covered=%q, legacy skipped", bad.result, bad.covered),
			results: r, outputs: outputs(true, bad.covered), wantPass: false, wantMention: "TEST_EMBEDDED_STORAGE",
		})
	}
	// The decision job itself must succeed even when nothing else needs it.
	for _, res := range []string{"failure", "cancelled", "skipped"} {
		r := results(false, "success", "false")
		r[prRiskCoverageJobName] = res
		scenarios = append(scenarios, prRiskGateScenario{"docs-only, decision " + res, r, outputs(false, ""), false, "BAZEL_EMBEDDED_COVERAGE"})
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			pass, out := runPRRiskGateStep(t, step, sc)
			if pass != sc.wantPass {
				t.Errorf("gate pass = %v, want %v (results %v, outputs %v)\n%s", pass, sc.wantPass, sc.results, sc.outputs, out)
			}
			if !sc.wantPass && sc.wantMention != "" && !regexp.MustCompile(`::error::`+sc.wantMention+`\b`).MatchString(out) {
				t.Errorf("red gate does not name %s:\n%s", sc.wantMention, out)
			}
		})
	}
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
