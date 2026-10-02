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
// bazel-embedded, remote mode only) is the tier's run, and nowhere else.
// Both workflows run the same bazel-embedded-coverage job, which reads a
// committed flag (BAZEL_RETIRES_LEGACY_EMBEDDED), never the mutable
// RBE_WEST_WORKERS variable. PR Risk's gate accepts the legacy skips only
// when that job says covered; pr.yml's gate then requires the Bazel lane to
// have run remotely and passed, so no re-run of either workflow, with the
// variable flipped either way, can leave both gates green and the tier
// unrun.

const (
	prRiskWorkflowName     = "pr-risk.yml"
	prRiskCoverageJobName  = "bazel-embedded-coverage"
	prRiskCoverageCovered  = "${{ steps.decide.outputs.covered }}"
	prRiskPullRequestValue = "${{ github.event_name == 'pull_request' }}"
	prRiskRetiredFlag      = "BAZEL_RETIRES_LEGACY_EMBEDDED"
	prRiskRetiredValue     = "${{ env." + prRiskRetiredFlag + " == 'true' }}"
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

// The bazel.yml rbe step env keys the decision copies verbatim.
var prRiskSharedDecisionEnv = []string{"FORK"}

// The decision's Dependabot test: Dependabot runs get no Actions secrets, so
// bazel.yml runs them locally and they keep the legacy tier.
const prRiskDependabotValue = "${{ github.actor == 'dependabot[bot]' }}"

// rbeFacts: what GitHub evaluates the decision steps' env expressions on.
type rbeFacts struct {
	event   string // github.event_name
	rbeVar  string // vars.RBE_WEST_WORKERS ("" = unset)
	secret  string // secrets.RBE_WEST_EXECUTOR ("" = unavailable: fork, Dependabot, unset)
	fork    bool   // github.event.pull_request.head.repo.fork
	retired string // the committed env.BAZEL_RETIRES_LEGACY_EMBEDDED
	// github.actor is dependabot[bot] (fixed for a PR's runs and re-runs).
	dependabot bool
}

func (f rbeFacts) String() string {
	return fmt.Sprintf("event=%s var=%q secret=%v fork=%v retired=%q dependabot=%v", f.event, f.rbeVar, f.secret != "", f.fork, f.retired, f.dependabot)
}

var (
	rbeEvents     = []string{"pull_request", "merge_group", "push", "workflow_dispatch", "pull_request_target"}
	rbeVarValues  = []string{"", "true", "True", "TRUE", "false", "1", "yes"}
	rbeSecrets    = []string{"", "grpcs://rbe.example:443"}
	retiredValues = []string{"true", "True", "false", ""}
)

// rbeFactsMatrix: every combination of the facts the decisions read.
func rbeFactsMatrix() []rbeFacts {
	var out []rbeFacts
	for _, event := range rbeEvents {
		for _, v := range rbeVarValues {
			for _, secret := range rbeSecrets {
				for _, fork := range []bool{false, true} {
					for _, retired := range retiredValues {
						for _, dependabot := range []bool{false, true} {
							out = append(out, rbeFacts{event, v, secret, fork, retired, dependabot})
						}
					}
				}
			}
		}
	}
	return out
}

// evalRBEExpr evaluates the env expressions the decision steps may use, for
// a bazel.yml call with these inputs (with: the caller's `with:`; unset
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
	case prRiskRetiredValue:
		return strconv.FormatBool(strings.EqualFold(f.retired, "true"))
	case prRiskDependabotValue:
		return strconv.FormatBool(f.dependabot)
	case "${{ vars.RBE_WEST_WORKERS == 'true' }}":
		return strconv.FormatBool(strings.EqualFold(f.rbeVar, "true"))
	case "${{ inputs.rbe == 'off' }}":
		return strconv.FormatBool(strings.EqualFold(input("rbe", "on"), "off"))
	case "${{ inputs.rbe == 'cache' }}":
		return strconv.FormatBool(strings.EqualFold(input("rbe", "on"), "cache"))
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

func coverageStep(t *testing.T, workflow string) ciWorkflowStep {
	t.Helper()
	job := readCIWorkflow(t, workflow).job(t, prRiskCoverageJobName)
	if len(job.Steps) != 1 {
		t.Fatalf("%s %s has %d steps, want exactly the decision step", workflow, prRiskCoverageJobName, len(job.Steps))
	}
	return job.Steps[0]
}

func prRiskCoverageStep(t *testing.T) ciWorkflowStep { return coverageStep(t, prRiskWorkflowName) }

// workflowEnv: a workflow's top-level env.
func workflowEnv(t *testing.T, name string) map[string]string {
	t.Helper()
	var doc struct {
		Env map[string]string `yaml:"env"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+name)), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Env
}

// The decision job reads the committed flag, the event, the actor and,
// through bazel.yml's rbe job's own expression, the fork flag: nothing a
// re-run can change, so never RBE_WEST_WORKERS, any other variable or any
// secret, and it runs no repository code. pr.yml runs the identical job, and
// both workflows commit the same flag. Nothing else in pr-risk.yml reads the
// facts, and nothing in it reads a secret.
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
	if prJob := readCIWorkflow(t, "pr.yml").job(t, prRiskCoverageJobName); !reflect.DeepEqual(prJob, job) {
		t.Errorf("pr.yml's %s differs from pr-risk.yml's:\n%+v\n%+v", prRiskCoverageJobName, prJob, job)
	}
	riskFlag, prFlag := workflowEnv(t, prRiskWorkflowName)[prRiskRetiredFlag], workflowEnv(t, "pr.yml")[prRiskRetiredFlag]
	if riskFlag != prFlag || (riskFlag != "true" && riskFlag != "false") {
		t.Errorf("%s: pr-risk.yml %q, pr.yml %q; want the same literal \"true\" or \"false\" in both", prRiskRetiredFlag, riskFlag, prFlag)
	}
	step := prRiskCoverageStep(t)
	rbeStep := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName).Steps[0]
	wantEnv := map[string]string{"PULL_REQUEST": prRiskPullRequestValue, "RETIRED": prRiskRetiredValue, "DEPENDABOT": prRiskDependabotValue}
	for _, k := range prRiskSharedDecisionEnv {
		wantEnv[k] = rbeStep.Env[k]
	}
	if step.ID != "decide" || step.Uses != "" || step.Shell != "" || len(step.With) != 0 || step.If != "" || step.ContinueOnError != nil || !reflect.DeepEqual(step.Env, wantEnv) {
		t.Errorf("%s step: id %q, uses %q, shell %q, with %v, if %q, env %v; want id decide, a plain run step with env %v",
			prRiskCoverageJobName, step.ID, step.Uses, step.Shell, step.With, step.If, step.Env, wantEnv)
	}
	if strings.Contains(step.Run, "${{") || regexp.MustCompile(`\.github/|\./|source |\bbash\b|RBE_WEST_WORKERS|RBE_VAR|EXECUTOR`).MatchString(step.Run) {
		t.Errorf("%s step runs repository code, interpolates expressions or reads the RBE variable or secret:\n%s", prRiskCoverageJobName, step.Run)
	}
	for k, v := range step.Env {
		if regexp.MustCompile(`\b(secrets|vars)\s*(\.|\[)`).MatchString(v) {
			t.Errorf("%s step env %s = %q reads a secret or variable; a re-run could change it", prRiskCoverageJobName, k, v)
		}
	}

	// Only the decision step reads the facts; nothing reads a secret or a
	// repository variable.
	stepEnv := ".jobs." + prRiskCoverageJobName + ".steps[0].env."
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	facts := regexp.MustCompile(`(?i)RBE_WEST_WORKERS|\bvars\s*(\.|\[)|head\.repo\.fork|RBE_WEST_EXECUTOR|github\.actor|dependabot|` + prRiskRetiredFlag)
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", prRiskWorkflowName)), "", func(path string, key bool, value string) {
		if key {
			if value == prRiskRetiredFlag && path != ".env."+prRiskRetiredFlag {
				t.Errorf("%s: %s sets %s; only the workflow env may", prRiskWorkflowName, path, prRiskRetiredFlag)
			}
			return
		}
		if secretRef.MatchString(value) {
			t.Errorf("%s: %s reads secrets (%q); PR Risk needs none", prRiskWorkflowName, path, value)
		}
		if facts.MatchString(value) && !strings.HasPrefix(path, stepEnv) && !strings.HasPrefix(path, ".jobs."+prRiskCoverageJobName+".steps[0].run") {
			t.Errorf("%s: %s re-derives the Bazel coverage decision (%q); read needs.%s.outputs.covered", prRiskWorkflowName, path, value, prRiskCoverageJobName)
		}
	})
}

// prGateFor: pr.yml's ci-gate scenario for one run whose Bazel call took
// this mode with every lane that runs in it passing, and whose
// bazel-embedded-coverage job said covered.
func prGateFor(t *testing.T, lanes map[string]map[string]bool, event, mode, covered string) bazelGateScenario {
	t.Helper()
	outputs := map[string]string{}
	for lane, modes := range lanes {
		if modes[mode] {
			outputs[lane] = "success"
		}
	}
	return bazelGateScenario{
		name: fmt.Sprintf("%s mode %s covered %s", event, mode, covered), event: event,
		mode: mode, enabled: strconv.FormatBool(mode == "remote"), call: "success",
		outputs: outputs, covered: covered,
	}
}

// bazelPRCallLanes: the modes each bazel.yml lane runs in under pr.yml's call.
func bazelPRCallLanes(t *testing.T, with map[string]string) map[string]map[string]bool {
	t.Helper()
	lanes := map[string]map[string]bool{}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if name != bazelRBEJobName {
			lanes[name] = bazelLaneRunModes(t, name, job.If, with)
		}
	}
	return lanes
}

// Never both gates green with neither the legacy tier nor the Bazel lane
// having run it. covered (both workflows' actual decision scripts) is the
// committed flag on a same-repo, non-Dependabot pull_request: nothing a
// re-run can change. Across two runs (PR Risk's and pr.yml's, or a re-run of
// either) that see RBE_WEST_WORKERS and the executor secret differently,
// every combination: if PR Risk skipped the legacy tier, pr.yml's actual
// gate step is green only if the lane ran remotely. The happy path (flag,
// variable and secret on) is green with the legacy tier skipped; the kill
// switch (variable off) or a missing secret with the flag still on is red.
func TestPRRiskEmbeddedDecisionMatchesBazelMode(t *testing.T) {
	requireHostTool(t, "bash")
	pr := readCIWorkflow(t, "pr.yml")
	call := pr.job(t, "bazel")
	if call.Uses != "./.github/workflows/"+bazelWorkflowName {
		t.Fatalf("pr.yml bazel job uses %q, want the local %s", call.Uses, bazelWorkflowName)
	}
	riskStep, prStep := prRiskCoverageStep(t), coverageStep(t, "pr.yml")
	rbeStep := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName).Steps[0]
	gateStep := pr.job(t, "ci-gate").step(t, "Evaluate CI gate")
	lanes := bazelPRCallLanes(t, call.With)

	// The Bazel lane PR Risk defers to: remote-only, gated by pr.yml.
	if !lanes[bazelEmbedJobName]["remote"] {
		t.Fatalf("%s does not run in mode remote", bazelEmbedJobName)
	}
	gate := pr.job(t, "ci-gate")
	required := strings.Fields(gateStep.Env["CI_GATE_REQUIRED"])
	for _, id := range []string{bazelLaneGateIDs[bazelEmbedJobName], "BAZEL_EMBEDDED_COVERAGE", "BAZEL_EMBEDDED_RETIRED"} {
		if !contains(required, id) {
			t.Errorf("pr.yml's ci-gate does not require %s", id)
		}
	}
	if !contains(gate.Needs, "bazel") || !contains(gate.Needs, prRiskCoverageJobName) {
		t.Errorf("pr.yml's ci-gate needs %v, want bazel and %s", gate.Needs, prRiskCoverageJobName)
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

	type decided struct{ covered, prCovered, mode string }
	decideMemo := map[rbeFacts]decided{}
	decide := func(t *testing.T, f rbeFacts) decided {
		t.Helper()
		if d, ok := decideMemo[f]; ok {
			return d
		}
		bazel, err := runDecisionStep(t, rbeStep, f, call.With)
		if err != nil {
			t.Fatalf("bazel.yml rbe step: %v", err)
		}
		risk, err := runDecisionStep(t, riskStep, f, nil)
		if err != nil || len(risk) != 1 {
			t.Fatalf("%s decision step: %v %v", prRiskCoverageJobName, risk, err)
		}
		prd, err := runDecisionStep(t, prStep, f, nil)
		if err != nil {
			t.Fatalf("pr.yml %s step: %v", prRiskCoverageJobName, err)
		}
		d := decided{risk["covered"], prd["covered"], bazel["mode"]}
		decideMemo[f] = d
		return d
	}

	// One run's facts: the decision itself.
	sawCovered, sawLegacy := false, false
	for _, f := range rbeFactsMatrix() {
		d := decide(t, f)
		want := strings.EqualFold(f.retired, "true") && f.event == "pull_request" && !f.fork && !f.dependabot
		if d.covered != strconv.FormatBool(want) || d.prCovered != d.covered {
			t.Errorf("%v: covered = %q (pr.yml %q), want %v", f, d.covered, d.prCovered, want)
		}
		if want {
			sawCovered = true
		} else {
			sawLegacy = true
		}
	}
	if !sawCovered || !sawLegacy {
		t.Errorf("matrix never exercised both outcomes (covered %v, legacy %v)", sawCovered, sawLegacy)
	}

	// Two runs (PR Risk's and pr.yml's, each possibly re-run) that agree on
	// everything committed or fixed by the PR and differ in what an admin can
	// change between them: the variable and the secret.
	gateMemo := map[string]bool{}
	prGatePasses := func(t *testing.T, event, mode, covered string) bool {
		t.Helper()
		key := event + "/" + mode + "/" + covered
		if pass, ok := gateMemo[key]; ok {
			return pass
		}
		pass, _ := runPRGateStep(t, gateStep, prGateFor(t, lanes, event, mode, covered))
		gateMemo[key] = pass
		return pass
	}
	for _, f := range rbeFactsMatrix() {
		if f.event != "pull_request" && f.event != "merge_group" {
			continue // the events both workflows run on
		}
		risk := decide(t, f)
		for _, v := range rbeVarValues {
			for _, secret := range rbeSecrets {
				g := f
				g.rbeVar, g.secret = v, secret
				prRun := decide(t, g)
				legacyRan := risk.covered != "true"
				bazelRan := prRun.mode == "remote" // and passed: every lane succeeds here
				if !legacyRan && !bazelRan && prGatePasses(t, g.event, prRun.mode, prRun.prCovered) {
					t.Errorf("PR Risk run %v skipped the legacy tier and pr.yml run (var %q, secret %v, mode %s, covered %s) is green without the Bazel lane",
						f, v, secret != "", prRun.mode, prRun.prCovered)
				}
			}
		}
	}

	// Named cases, for the record (with the committed flag on). Forks and
	// Dependabot run in mode cache, which, like local, skips the remote-only
	// embedded lane, so they keep the legacy tier.
	for _, c := range []struct {
		name     string
		f        rbeFacts
		mode     string
		covered  string
		prPasses bool
	}{
		{"same-repo PR, farm on", rbeFacts{"pull_request", "true", "x", false, "true", false}, "remote", "true", true},
		{"same-repo PR, kill switch (var unset)", rbeFacts{"pull_request", "", "x", false, "true", false}, "skip", "true", false},
		{"same-repo PR, executor secret missing", rbeFacts{"pull_request", "true", "", false, "true", false}, "cache", "true", false},
		{"same-repo PR, flag reverted, var unset", rbeFacts{"pull_request", "", "x", false, "false", false}, "skip", "false", true},
		{"same-repo PR, flag reverted, secret missing", rbeFacts{"pull_request", "true", "", false, "false", false}, "cache", "false", true},
		{"fork PR", rbeFacts{"pull_request", "true", "", true, "true", false}, "cache", "false", true},
		{"fork PR, var unset", rbeFacts{"pull_request", "", "", true, "true", false}, "cache", "false", true},
		{"fork PR somehow with a secret", rbeFacts{"pull_request", "true", "x", true, "true", false}, "cache", "false", true},
		{"Dependabot PR (no Actions secrets)", rbeFacts{"pull_request", "true", "", false, "true", true}, "cache", "false", true},
		{"Dependabot PR, var unset", rbeFacts{"pull_request", "", "", false, "true", true}, "skip", "false", true},
		{"merge_group", rbeFacts{"merge_group", "true", "x", false, "true", false}, "remote", "false", true},
		{"merge_group, var unset", rbeFacts{"merge_group", "", "x", false, "true", false}, "skip", "false", true},
	} {
		d := decide(t, c.f)
		if d.covered != c.covered || d.mode != c.mode {
			t.Errorf("%s: covered = %q, mode = %q; want %s, %s", c.name, d.covered, d.mode, c.covered, c.mode)
		}
		if pass, out := runPRGateStep(t, gateStep, prGateFor(t, lanes, c.f.event, d.mode, d.prCovered)); pass != c.prPasses {
			t.Errorf("%s: pr.yml gate pass = %v, want %v\n%s", c.name, pass, c.prPasses, out)
		} else if !pass && !regexp.MustCompile(`::error::BAZEL_EMBEDDED_RETIRED\b`).MatchString(out) {
			t.Errorf("%s: red pr.yml gate does not name BAZEL_EMBEDDED_RETIRED:\n%s", c.name, out)
		}
	}
	// Covered, remote, but the lane failed, was cancelled or reported
	// nothing: red, naming the retirement too.
	for _, res := range []string{"failure", "cancelled", ""} {
		sc := prGateFor(t, lanes, "pull_request", "remote", "true")
		sc.outputs[bazelEmbedJobName] = res
		if pass, out := runPRGateStep(t, gateStep, sc); pass || !strings.Contains(out, "::error::BAZEL_EMBEDDED_RETIRED") {
			t.Errorf("covered, embedded lane %q: gate pass = %v, want red naming BAZEL_EMBEDDED_RETIRED\n%s", res, pass, out)
		}
	}
	// Covered, and an embedded result of success the mode cannot produce
	// (the lane runs only in mode remote): the mode alone still makes it red.
	for _, mode := range []string{"skip", "local", "cache"} {
		sc := prGateFor(t, lanes, "pull_request", mode, "true")
		sc.outputs[bazelEmbedJobName] = "success"
		if pass, out := runPRGateStep(t, gateStep, sc); pass || !strings.Contains(out, "::error::BAZEL_EMBEDDED_RETIRED") {
			t.Errorf("covered, mode %s, embedded reported success: gate pass = %v, want red naming BAZEL_EMBEDDED_RETIRED\n%s", mode, pass, out)
		}
	}
	// pr.yml's coverage job failed: red even where nothing is retired.
	for _, res := range []string{"failure", "cancelled", "skipped"} {
		sc := prGateFor(t, lanes, "pull_request", "skip", "")
		sc.coverage = res
		if pass, out := runPRGateStep(t, gateStep, sc); pass || !strings.Contains(out, "::error::BAZEL_EMBEDDED_COVERAGE") {
			t.Errorf("coverage job %s: gate pass = %v, want red naming BAZEL_EMBEDDED_COVERAGE\n%s", res, pass, out)
		}
	}
	// A value that is not a boolean fails the job rather than deciding.
	if out, err := runBazelRBEDecision(t, riskStep.Run, map[string]string{
		"RETIRED": "true", "PULL_REQUEST": "true", "FORK": "", "DEPENDABOT": "false",
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

// Review F5: bazel-embedded checks, after the run, that every Bazel shard of
// the manifest-sharded targets ran exactly the tests its PR Risk shard
// script lists, for the targets and shard counts of the legacy jobs.
func TestBazelEmbeddedChecksListedTestsRan(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelEmbedJobName)
	step := job.step(t, "Every listed test ran in its shard")
	risk := readCIWorkflow(t, prRiskWorkflowName)
	want := []string{"python3 tools/bazel/check_shard_coverage.py", `--bep "$RUNNER_TEMP/bazel-bep.json"`}
	for _, c := range []struct{ job, label, script string }{
		{"test-embedded-cmd", "//cmd/bd:bd_embedded_test", ".github/scripts/embedded-test-shard.sh"},
		{"test-embedded-storage", "//internal/storage/embeddeddolt:embeddeddolt_embedded_test", ".github/scripts/embedded-storage-test-shard.sh"},
	} {
		shards := len(risk.job(t, c.job).Strategy.Matrix.Shard)
		if !strings.Contains(risk.job(t, c.job).step(t, "Test").Run, c.script) {
			t.Errorf("pr-risk.yml %s no longer runs %s; update this suite", c.job, c.script)
		}
		want = append(want, fmt.Sprintf("--suite %s %s %d", c.label, c.script, shards))
	}
	// The conformance partitions (unsharded): not all skipped.
	for _, partition := range []string{"core", "audit"} {
		if risk.job(t, "test-embedded-conformance").step(t, "Test "+partition+" conformance").Run == "" {
			t.Errorf("pr-risk.yml test-embedded-conformance has no %s partition; update this check", partition)
		}
		want = append(want, "--whole //internal/storage/embeddeddolt:embeddeddolt_conformance_"+partition+"_test")
	}
	if got := strings.Join(strings.Fields(step.Run), " "); got != strings.Join(want, " ") {
		t.Errorf("%s coverage step runs %q, want %q", bazelEmbedJobName, got, strings.Join(want, " "))
	}
	if step.If != "${{ always() && steps.test.outcome != 'skipped' }}" || step.ContinueOnError != nil {
		t.Errorf("coverage step: if %q, continue-on-error %v; want always() after the test step and no continue-on-error", step.If, step.ContinueOnError)
	}
	if !(job.stepIndex(t, "bazel test //... --config=embedded") < job.stepIndex(t, step.Name) &&
		job.stepIndex(t, step.Name) < job.stepIndex(t, "Record job result")) {
		t.Errorf("coverage step must run after the test step and before the result recorder")
	}
}

// check_shard_coverage.py itself, on a synthetic BEP, test.xml files and
// shard script.
func TestCheckShardCoverageScript(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold no tools/bazel Python")
	}
	requireHostTool(t, "bash")
	python := requireHostTool(t, "python3")
	script := filepath.Join(sourceRepoRoot(t), "tools", "bazel", "check_shard_coverage.py")
	dir := t.TempDir()
	shard := filepath.Join(dir, "shard.sh")
	// Shard 1 lists TestA and TestB, shard 2 TestC; list-only mode only.
	if err := os.WriteFile(shard, []byte(`#!/usr/bin/env bash
set -euo pipefail
[ "${BEADS_TEST_SHARD_LIST_ONLY:-}" = 1 ] || { echo "not list-only" >&2; exit 3; }
[ "$2" = 2 ] || { echo "bad total $2" >&2; exit 1; }
echo "Shard $1/$2: running"
echo "  manifest: 1, fallback: 0"
case "$1" in
  1) printf '  %s\n' TestA TestB TestMain ;; # TestMain: never a testcase
  2) printf '  %s\n' TestC ;;
  *) exit 1 ;;
esac
[ -z "${FAKE_SHARD_FAIL:-}" ] || exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}
	const label = "//pkg:t"
	writeBEP := func(shards int) string {
		lines := []string{`{"id":{"targetConfigured":{"label":"` + label + `"}},"configured":{"targetKind":"sh_test rule"}}`}
		for k := 1; k <= shards; k++ {
			lines = append(lines, fmt.Sprintf(`{"id":{"testResult":{"label":"%s","shard":%d,"run":1,"attempt":1}}}`, label, k))
		}
		path := filepath.Join(dir, fmt.Sprintf("bep%d.json", shards))
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// A name ending in "~" is written as a skipped testcase.
	writeXML := func(logs string, k int, names ...string) {
		d := filepath.Join(logs, "pkg", "t", fmt.Sprintf("shard_%d_of_2", k))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		b.WriteString(`<testsuites><testsuite name="pkg">`)
		for _, n := range names {
			if skipped, ok := strings.CutSuffix(n, "~"); ok {
				fmt.Fprintf(&b, `<testcase classname="pkg" name="%s"><skipped message="skip"></skipped></testcase>`, skipped)
				continue
			}
			fmt.Fprintf(&b, `<testcase classname="pkg" name="%s"></testcase>`, n)
		}
		b.WriteString(`</testsuite></testsuites>`)
		if err := os.WriteFile(filepath.Join(d, "test.xml"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(bep, logs string) (bool, string) {
		cmd := exec.Command(python, script, "--bep", bep, "--testlogs", logs, "--suite", label, shard, "2")
		out, err := cmd.CombinedOutput()
		return err == nil, string(out)
	}
	cases := []struct {
		name     string
		bepShard int
		s1, s2   []string
		pass     bool
		mention  string
	}{
		{"exact", 2, []string{"TestA", "TestB", "TestA/sub"}, []string{"TestC"}, true, ""},
		{"listed test missing from the binary", 2, []string{"TestA"}, []string{"TestC"}, false, "TestB is listed"},
		{"test in the wrong shard", 2, []string{"TestA", "TestB", "TestC"}, []string{}, false, "TestC"},
		{"unlisted test ran", 2, []string{"TestA", "TestB", "TestZ"}, []string{"TestC"}, false, "TestZ ran"},
		{"shard count differs", 1, []string{"TestA", "TestB"}, []string{"TestC"}, false, "want 2"},
		{"missing test.xml", 2, []string{"TestA", "TestB"}, nil, false, "missing"},
		// Review G5: a shard whose test.xml has no top-level test at all
		// still reports every listed test missing (check_testcases.py also
		// rejects it, but this check must not depend on that).
		{"shard ran zero tests", 2, []string{}, []string{"TestC"}, false, "TestA is listed"},
		{"zero tests, only subtests", 2, []string{"TestA/sub"}, []string{"TestC"}, false, "TestB is listed"},
		// Review G3: some skips are fine; a shard of only skips is not.
		{"some skipped", 2, []string{"TestA~", "TestB"}, []string{"TestC"}, true, ""},
		{"shard all skipped", 2, []string{"TestA~", "TestB~"}, []string{"TestC"}, false, "shard 1/2: every top-level test (2) was skipped"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := filepath.Join(dir, fmt.Sprintf("logs%d", i))
			writeXML(logs, 1, c.s1...)
			if c.s2 != nil {
				writeXML(logs, 2, c.s2...)
			}
			pass, out := run(writeBEP(c.bepShard), logs)
			if pass != c.pass || (c.mention != "" && !strings.Contains(out, c.mention)) {
				t.Errorf("pass = %v, want %v (mention %q):\n%s", pass, c.pass, c.mention, out)
			}
		})
	}
	// --whole: an unsharded target must list a test and not only skips.
	for i, c := range []struct {
		names   []string
		pass    bool
		mention string
	}{
		{[]string{"TestConformance"}, true, ""},
		{[]string{"TestConformance~"}, false, "was skipped"},
		{[]string{}, false, "lists no tests"},
	} {
		logs := filepath.Join(dir, fmt.Sprintf("whole%d", i))
		d := filepath.Join(logs, "pkg", "w")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		xml := `<testsuites><testsuite name="pkg">`
		for _, n := range c.names {
			if skipped, ok := strings.CutSuffix(n, "~"); ok {
				xml += `<testcase name="` + skipped + `"><skipped></skipped></testcase>`
			} else {
				xml += `<testcase name="` + n + `"></testcase>`
			}
		}
		xml += `</testsuite></testsuites>`
		if err := os.WriteFile(filepath.Join(d, "test.xml"), []byte(xml), 0o644); err != nil {
			t.Fatal(err)
		}
		bep := filepath.Join(dir, fmt.Sprintf("whole%d.json", i))
		if err := os.WriteFile(bep, []byte(`{"id":{"targetConfigured":{"label":"//pkg:w"}},"configured":{"targetKind":"sh_test rule"}}
{"id":{"testResult":{"label":"//pkg:w","run":1,"attempt":1}}}
{"id":{"targetConfigured":{"label":"`+label+`"}},"configured":{"targetKind":"sh_test rule"}}
{"id":{"testResult":{"label":"`+label+`","shard":1}}}
{"id":{"testResult":{"label":"`+label+`","shard":2}}}
`), 0o644); err != nil {
			t.Fatal(err)
		}
		writeXML(logs, 1, "TestA", "TestB")
		writeXML(logs, 2, "TestC")
		out, err := exec.Command(python, script, "--bep", bep, "--testlogs", logs, "--suite", label, shard, "2", "--whole", "//pkg:w").CombinedOutput()
		if (err == nil) != c.pass || (c.mention != "" && !strings.Contains(string(out), c.mention)) {
			t.Errorf("--whole %v: pass = %v, want %v (mention %q):\n%s", c.names, err == nil, c.pass, c.mention, out)
		}
	}
	if out, err := exec.Command(python, script, "--bep", writeBEP(2), "--testlogs", filepath.Join(dir, "logs0"), "--suite", label, shard, "2", "--whole", "//pkg:absent").CombinedOutput(); err == nil {
		t.Errorf("--whole for a target the BEP lacks passed:\n%s", out)
	}

	// A failing shard script fails the check, even if what it listed
	// matches; so does a missing one.
	logs := filepath.Join(dir, "logs-bad")
	writeXML(logs, 1, "TestA", "TestB")
	writeXML(logs, 2, "TestC")
	for _, c := range []struct {
		script string
		env    []string
	}{{shard, []string{"FAKE_SHARD_FAIL=1"}}, {"/nonexistent/shard.sh", nil}} {
		cmd := exec.Command(python, script, "--bep", writeBEP(2), "--testlogs", logs, "--suite", label, c.script, "2")
		cmd.Env = append(os.Environ(), c.env...)
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "failed") {
			t.Errorf("shard script %s %v: check passed or did not say it failed:\n%s", c.script, c.env, out)
		}
	}
}

// Review G1: the checker's input is the real shard scripts' list-only
// output. Every name they list, for every shard, must be a test go test
// runs (declared `func Name(t *testing.T)` in the package's _test.go files),
// or one check_shard_coverage.py drops as NOT_TESTS; and each NOT_TESTS name
// must really not be a test (TestMain takes *testing.M). Otherwise the
// checker reports a listed test that "did not run" on every real run.
func TestEmbeddedShardScriptsListOnlyRealTests(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold neither the shard scripts' sources nor tools/bazel")
	}
	requireHostTool(t, "bash")
	root := sourceRepoRoot(t)
	m := regexp.MustCompile(`(?m)^NOT_TESTS = frozenset\(\{([^}]*)\}\)`).FindStringSubmatch(readPolicyFile(t, root, "tools/bazel/check_shard_coverage.py"))
	if m == nil {
		t.Fatal("tools/bazel/check_shard_coverage.py has no NOT_TESTS = frozenset({...})")
	}
	notTests := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		notTests[q[1]] = true
	}
	risk := readCIWorkflow(t, prRiskWorkflowName)
	for _, c := range []struct{ job, script, pkg string }{
		{"test-embedded-cmd", ".github/scripts/embedded-test-shard.sh", "cmd/bd"},
		{"test-embedded-storage", ".github/scripts/embedded-storage-test-shard.sh", "internal/storage/embeddeddolt"},
	} {
		var src strings.Builder
		files, err := filepath.Glob(filepath.Join(root, c.pkg, "*_test.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no _test.go files (%v)", c.pkg, err)
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src.Write(data)
			src.WriteString("\n")
		}
		declared := map[string]bool{}
		for _, d := range regexp.MustCompile(`(?m)^func (Test\w*)\(\w+ \*testing\.T\) \{`).FindAllStringSubmatch(src.String(), -1) {
			declared[d[1]] = true
		}
		shards := len(risk.job(t, c.job).Strategy.Matrix.Shard)
		listed := 0
		for k := 1; k <= shards; k++ {
			cmd := exec.Command("bash", c.script, strconv.Itoa(k), strconv.Itoa(shards))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "BEADS_TEST_SHARD_LIST_ONLY=1")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s %d %d: %v", c.script, k, shards, err)
			}
			for _, line := range strings.Split(string(out), "\n") {
				name, ok := strings.CutPrefix(line, "  ")
				if !ok || !strings.HasPrefix(name, "Test") || strings.ContainsAny(name, " :") {
					continue
				}
				listed++
				isTest := declared[name]
				switch {
				case notTests[name] && isTest:
					t.Errorf("%s shard %d lists %s, which check_shard_coverage.py drops, but it is a real test", c.script, k, name)
				case !notTests[name] && !isTest:
					t.Errorf("%s shard %d lists %s, which is not a `func %s(t *testing.T)` test in %s: check_shard_coverage.py would report it missing on every run (add it to NOT_TESTS only if go test never runs it)",
						c.script, k, name, name, c.pkg)
				}
			}
		}
		if listed < 50 {
			t.Errorf("%s listed only %d tests over %d shards; did the list-only output format change?", c.script, listed, shards)
		}
	}
	for name := range notTests {
		if name != "TestMain" {
			t.Errorf("NOT_TESTS has %s; only TestMain is never a test", name)
		}
	}
}

// Review G3: since D2 step 1 the Bazel embedded lane is the tier's only
// pre-merge run on same-repo PRs, so nothing that reaches it may narrow it
// (select fewer tests, or turn them into skips) without a reviewed edit of
// this test. TestBazelEmbeddedJobMirrorsEmbeddedTier pins the command line
// and the --config=embedded lines; this covers everything else that applies
// to the lane: every other .bazelrc line of a config the lane uses (the
// unconfigured ones, remote-exec and fork-cache, which setup-bazel's rc
// enables, and any config those pull in), rc files that would be try-imported, the
// tools/bazel scripts every test runs under or through, the whole
// setup-bazel action, and the embedded-tagged targets' args and env. At run
// time, check_shard_coverage.py also fails a shard of only skips.
func TestBazelEmbeddedLaneCannotBeNarrowed(t *testing.T) {
	root := sourceRepoRoot(t)
	rcNarrow := regexp.MustCompile(`test_filter|test_arg|-test\.|_filters\b|test_env=(BEADS_TEST|GO_TEST|TESTBRIDGE)|--config=|cache_test_results|run_under|flaky|runs_per_test|test_sharding_strategy|build_tests_only`)
	const runUnder = "test --run_under=//tools/bazel:test_env"
	wantImports := []string{"try-import %workspace%/.bazelrc.local", "try-import %workspace%/user.bazelrc"}

	type rcLine struct{ cmd, config, text string }
	var lines []rcLine
	var imports []string
	sawRunUnder := false
	for _, raw := range strings.Split(readPolicyFile(t, root, ".bazelrc"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, _, _ := strings.Cut(line, " ")
		if head == "import" || head == "try-import" {
			imports = append(imports, line)
			continue
		}
		cmd, config, _ := strings.Cut(head, ":")
		lines = append(lines, rcLine{cmd, config, line})
	}
	if !reflect.DeepEqual(imports, wantImports) {
		t.Errorf(".bazelrc imports %v, want exactly %v (an import can carry any flag into the lane)", imports, wantImports)
	}
	// The configs the lane uses: --config=embedded (pinned), the configs
	// setup-bazel's generated rc enables for every command (remote-exec,
	// and fork-cache in mode cache, where the remote-only lane is skipped
	// but the rc still applies), the unconfigured lines, and anything they
	// reference (which the check below then forbids anyway).
	rcEnabled := map[string]bool{"remote-exec": true, "fork-cache": true}
	inUse := map[string]bool{"": true, "embedded": true}
	for c := range rcEnabled {
		inUse[c] = true
	}
	for changed := true; changed; {
		changed = false
		for _, l := range lines {
			if !inUse[l.config] {
				continue
			}
			for _, m := range regexp.MustCompile(`--config=([A-Za-z0-9_-]+)`).FindAllStringSubmatch(l.text, -1) {
				if !inUse[m[1]] {
					inUse[m[1]], changed = true, true
				}
			}
		}
	}
	pinned := map[string]bool{}
	for _, l := range bazelEmbeddedRCLines {
		pinned[l] = true
	}
	for _, l := range lines {
		if !inUse[l.config] || pinned[l.text] {
			continue
		}
		if l.config == "embedded" {
			t.Errorf(".bazelrc %q: not one of the pinned --config=embedded lines", l.text)
			continue
		}
		if l.text == runUnder {
			sawRunUnder = true
			continue
		}
		if rcNarrow.MatchString(l.text) {
			t.Errorf(".bazelrc %q applies to the embedded lane (config %q) and selects, narrows or re-runs tests", l.text, l.config)
		}
	}
	if !sawRunUnder {
		t.Errorf(".bazelrc lacks %q (the wrapper the narrowing checks below cover)", runUnder)
	}

	// Committed rc files: only .bazelrc. .bazelrc.local and user.bazelrc are
	// developer-local (gitignored) and would be try-imported into CI runs.
	if os.Getenv("TEST_SRCDIR") == "" {
		if git, err := exec.LookPath("git"); err == nil {
			out, err := exec.Command(git, "-C", root, "ls-files").Output()
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				base := filepath.Base(f)
				if strings.Contains(base, "bazelrc") && f != ".bazelrc" && f != setupBazelActionDir+"/write-bazelrc.sh" {
					t.Errorf("committed rc file %s: .bazelrc's try-import would load it into every CI run", f)
				}
			}
		}
	}

	// The scripts every embedded test runs under or through, and the whole
	// setup-bazel action (its generated rc applies to every command).
	if os.Getenv("TEST_SRCDIR") == "" {
		scriptNarrow := regexp.MustCompile(`-test\.(short|run|skip|list|bench)|BEADS_TEST_SKIP|BEADS_TEST_EMBEDDED_DOLT|TESTBRIDGE_TEST_ONLY|test_filter|test_arg|_filters\b|cache_test_results|flaky|runs_per_test|test_sharding_strategy`)
		files, _ := filepath.Glob(filepath.Join(root, "tools", "bazel", "*.sh"))
		action, _ := filepath.Glob(filepath.Join(root, setupBazelActionDir, "*"))
		files = append(files, action...)
		if len(files) < 5 {
			t.Fatalf("found only %v", files)
		}
		for _, f := range files {
			rel, _ := filepath.Rel(root, f)
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(data), "\n") {
				code := strings.TrimSpace(line)
				if strings.HasPrefix(code, "#") {
					continue
				}
				if scriptNarrow.MatchString(code) {
					t.Errorf("%s:%d %q can select, skip or re-run the embedded lane's tests", rel, i+1, code)
				}
				for _, m := range regexp.MustCompile(`--config=([A-Za-z0-9_-]+)`).FindAllStringSubmatch(code, -1) {
					if !rcEnabled[m[1]] {
						t.Errorf("%s:%d enables --config=%s for every command", rel, i+1, m[1])
					}
				}
			}
		}
	}

	// The embedded-tagged targets: exactly these, with exactly these args
	// and env (the legacy jobs' flags; the shard scripts add the rest).
	if os.Getenv("TEST_SRCDIR") != "" {
		return // scripts_test's runfiles hold no other package's BUILD
	}
	type target struct {
		args []string
		env  map[string]string
	}
	want := map[string]target{
		"//cmd/bd:bd_embedded_test": {
			[]string{"$(rootpath //:.github/scripts/embedded-test-shard.sh)", "BEADS_TEST_CMD_BINARY", "$(rootpath :bd_test)", "-test.timeout=19m"},
			map[string]string{"BEADS_TEST_BD_BINARY": "$(rlocationpath :bd)", "BEADS_TEST_EMBEDDED_DOLT": "1", "BEADS_TEST_GOFMT": "$(rlocationpath @go_sdk//:bin/gofmt)"},
		},
		"//internal/storage/embeddeddolt:embeddeddolt_embedded_test": {
			[]string{"$(rootpath //:.github/scripts/embedded-storage-test-shard.sh)", "BEADS_TEST_EMBEDDED_TEST_BINARY", "$(rootpath :embeddeddolt_test)", "-test.timeout=19m"},
			map[string]string{"BEADS_TEST_EMBEDDED_DOLT": "1"},
		},
		"//internal/storage/embeddeddolt:embeddeddolt_conformance_core_test": {
			[]string{"$(rootpath :embeddeddolt_test)", "-test.v", "-test.count=1", "-test.timeout=19m", "-test.run=^TestConformance$$", "-test.skip=^TestConformance$$/^Audit$$"},
			map[string]string{"BEADS_TEST_EMBEDDED_DOLT": "1"},
		},
		"//internal/storage/embeddeddolt:embeddeddolt_conformance_audit_test": {
			[]string{"$(rootpath :embeddeddolt_test)", "-test.v", "-test.count=1", "-test.timeout=19m", "-test.run=^TestConformance$$/^Audit$$"},
			map[string]string{"BEADS_TEST_EMBEDDED_DOLT": "1"},
		},
	}
	quoted := regexp.MustCompile(`"([^"]*)"`)
	envPair := regexp.MustCompile(`"([^"]*)":\s*"([^"]*)"`)
	nameRe := regexp.MustCompile(`(?m)^    name = "([^"]+)",$`)
	tagsRe := regexp.MustCompile(`(?ms)^    tags = \[(.*?)\],$`)
	envRe := regexp.MustCompile(`(?ms)^    env = \{(.*?)\},$`)
	got := map[string]target{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".beads":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || (d.Name() != "BUILD.bazel" && d.Name() != "BUILD") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pkg, _ := filepath.Rel(root, filepath.Dir(path))
		for _, rule := range bazelTopRules(string(data)) {
			tags := tagsRe.FindStringSubmatch(rule)
			if tags == nil || !strings.Contains(tags[1], `"embedded"`) {
				continue
			}
			name := nameRe.FindStringSubmatch(rule)
			if name == nil {
				t.Errorf("%s: an embedded-tagged rule without a literal name:\n%s", pkg, rule)
				continue
			}
			var tg target
			for _, q := range quoted.FindAllStringSubmatch(bazelAttrBlock(rule, "args"), -1) {
				tg.args = append(tg.args, q[1])
			}
			tg.env = map[string]string{}
			if e := envRe.FindStringSubmatch(rule); e != nil {
				for _, p := range envPair.FindAllStringSubmatch(e[1], -1) {
					tg.env[p[1]] = p[2]
				}
			}
			if strings.Contains(rule, "args = select") || strings.Contains(rule, "env = select") || strings.Contains(rule, "env_inherit") {
				t.Errorf("//%s:%s sets args/env indirectly; keep them literal so this check sees them", pkg, name[1])
			}
			got["//"+filepath.ToSlash(pkg)+":"+name[1]] = tg
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("embedded-tagged targets' args/env changed:\ngot  %v\nwant %v", got, want)
	}
}

// Review G4: bazel-embedded asks Bazel which test targets are flaky (a
// grep of BUILD files misses `flaky = _VAR` or a macro default) before it
// runs the tier, and fails on any.
func TestBazelEmbeddedQueriesFlakyTargets(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelEmbedJobName)
	step := job.step(t, "No test target is marked flaky")
	const want = `flaky="$(bazel query 'attr(flaky, 1, tests(//...))')"
if [ -n "$flaky" ]; then
  echo "::error::flaky = True on ${flaky//$'\n'/ }: the gated lanes run each test once"
  exit 1
fi`
	if strings.TrimSpace(step.Run) != want || step.If != "" || step.ContinueOnError != nil {
		t.Errorf("flaky query step: if %q, continue-on-error %v, run:\n%s\nwant run:\n%s", step.If, step.ContinueOnError, step.Run, want)
	}
	if !(job.stepIndex(t, "Set up Bazel") < job.stepIndex(t, step.Name) && job.stepIndex(t, step.Name) < job.stepIndex(t, "bazel test //... --config=embedded")) {
		t.Errorf("flaky query step must run after setup-bazel and before the tier")
	}
}
