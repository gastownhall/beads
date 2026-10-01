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
var prRiskSharedDecisionEnv = []string{"FORK", "HAS_EXECUTOR"}

// rbeFacts: what GitHub evaluates the decision steps' env expressions on.
type rbeFacts struct {
	event   string // github.event_name
	rbeVar  string // vars.RBE_WEST_WORKERS ("" = unset)
	secret  string // secrets.RBE_WEST_EXECUTOR ("" = unavailable: fork, Dependabot, unset)
	fork    bool   // github.event.pull_request.head.repo.fork
	retired string // the committed env.BAZEL_RETIRES_LEGACY_EMBEDDED
}

func (f rbeFacts) String() string {
	return fmt.Sprintf("event=%s var=%q secret=%v fork=%v retired=%q", f.event, f.rbeVar, f.secret != "", f.fork, f.retired)
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
						out = append(out, rbeFacts{event, v, secret, fork, retired})
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

// The decision job reads the committed flag and, through bazel.yml's rbe
// job's own expressions, the fork flag and the executor secret (only as an
// emptiness test); never RBE_WEST_WORKERS or any other variable, and it runs
// no repository code. pr.yml runs the identical job, and both workflows
// commit the same flag. Nothing else in pr-risk.yml reads the facts or any
// secret.
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
	wantEnv := map[string]string{"PULL_REQUEST": prRiskPullRequestValue, "RETIRED": prRiskRetiredValue}
	for _, k := range prRiskSharedDecisionEnv {
		wantEnv[k] = rbeStep.Env[k]
	}
	if step.ID != "decide" || step.Uses != "" || step.Shell != "" || len(step.With) != 0 || step.If != "" || step.ContinueOnError != nil || !reflect.DeepEqual(step.Env, wantEnv) {
		t.Errorf("%s step: id %q, uses %q, shell %q, with %v, if %q, env %v; want id decide, a plain run step with env %v",
			prRiskCoverageJobName, step.ID, step.Uses, step.Shell, step.With, step.If, step.Env, wantEnv)
	}
	if strings.Contains(step.Run, "${{") || regexp.MustCompile(`\.github/|\./|source |\bbash\b|RBE_WEST_WORKERS|RBE_VAR`).MatchString(step.Run) {
		t.Errorf("%s step runs repository code, interpolates expressions or reads the RBE variable:\n%s", prRiskCoverageJobName, step.Run)
	}

	// Only the decision step reads the facts; only its emptiness test reads
	// a secret; nothing reads a repository variable.
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
		if secretRef.MatchString(value) && !(path == stepEnv+"HAS_EXECUTOR" && value == bazelRBESecretValue) {
			t.Errorf("%s: %s reads secrets (%q); only %s's emptiness test may", prRiskWorkflowName, path, value, prRiskCoverageJobName)
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
// committed flag on a same-repo pull_request with the executor secret, and
// is then never in a run where bazel.yml takes mode local. Across two runs
// (PR Risk's and pr.yml's, or a re-run of either) that see RBE_WEST_WORKERS
// differently, every combination: if PR Risk skipped the legacy tier, pr.yml's
// actual gate step is green only if the lane ran remotely. The happy path
// (flag and variable on) is green with the legacy tier skipped; the kill
// switch alone (variable off, flag still on) is red.
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
		want := strings.EqualFold(f.retired, "true") && f.event == "pull_request" && !f.fork && f.secret != ""
		if d.covered != strconv.FormatBool(want) || d.prCovered != d.covered {
			t.Errorf("%v: covered = %q (pr.yml %q), want %v", f, d.covered, d.prCovered, want)
		}
		if want && d.mode == "local" {
			t.Errorf("%v: covered, but bazel.yml takes mode local (the embedded lane never runs there)", f)
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
	// everything committed or fixed by the event and differ only in the
	// mutable variable.
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
			g := f
			g.rbeVar = v
			prRun := decide(t, g)
			legacyRan := risk.covered != "true"
			bazelRan := prRun.mode == "remote" // and passed: every lane succeeds here
			if !legacyRan && !bazelRan && prGatePasses(t, g.event, prRun.mode, prRun.prCovered) {
				t.Errorf("PR Risk run %v skipped the legacy tier and pr.yml run (var %q, mode %s, covered %s) is green without the Bazel lane", f, v, prRun.mode, prRun.prCovered)
			}
		}
	}

	// Named cases, for the record (with the committed flag on).
	for _, c := range []struct {
		name     string
		f        rbeFacts
		covered  string
		prPasses bool
	}{
		{"same-repo PR, farm on", rbeFacts{"pull_request", "true", "x", false, "true"}, "true", true},
		{"same-repo PR, kill switch (var unset)", rbeFacts{"pull_request", "", "x", false, "true"}, "true", false},
		{"same-repo PR, flag reverted, var unset", rbeFacts{"pull_request", "", "x", false, "false"}, "false", true},
		{"fork PR", rbeFacts{"pull_request", "true", "", true, "true"}, "false", true},
		{"fork PR somehow with a secret", rbeFacts{"pull_request", "true", "x", true, "true"}, "false", true},
		{"Dependabot PR (no Actions secrets)", rbeFacts{"pull_request", "true", "", false, "true"}, "false", true},
		{"Dependabot PR, var unset", rbeFacts{"pull_request", "", "", false, "true"}, "false", true},
		{"merge_group", rbeFacts{"merge_group", "true", "x", false, "true"}, "false", true},
		{"merge_group, var unset", rbeFacts{"merge_group", "", "x", false, "true"}, "false", true},
	} {
		d := decide(t, c.f)
		if d.covered != c.covered {
			t.Errorf("%s: covered = %q, want %s", c.name, d.covered, c.covered)
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
	for _, mode := range []string{"skip", "local"} {
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
		"RETIRED": "true", "PULL_REQUEST": "true", "FORK": "", "HAS_EXECUTOR": "true",
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
