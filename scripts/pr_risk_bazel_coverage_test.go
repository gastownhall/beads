package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// D2: PR Risk's legacy tiers stand down on the PRs where pr.yml's gated
// Bazel lanes for them (bazel.yml, remote mode only) are the tier's run, and
// nowhere else. Step 1 retired the embedded-Dolt test jobs (Bazel
// bazel-embedded), step 2 the proxied-server and server-Dolt storage test
// jobs (bazel-proxied, bazel-server-storage) and, where both are retired,
// build-embedded, whose artifact only they consume. Both workflows run the
// same bazel-coverage job, which reads one committed flag per step
// (BAZEL_RETIRES_LEGACY_*), never the mutable RBE_WEST_WORKERS variable.
// PR Risk's gate accepts the legacy skips only when that job says covered;
// pr.yml's gate then requires the tier's Bazel lanes to have run remotely
// and passed, so no re-run of either workflow, with the variable flipped
// either way, can leave both gates green and a tier unrun.

const (
	prRiskWorkflowName     = "pr-risk.yml"
	prRiskCoverageJobName  = "bazel-coverage"
	prRiskPullRequestValue = "${{ github.event_name == 'pull_request' }}"
	// A merge group (merge queue) is covered like a same-repo PR: it runs
	// on this repository's gh-readonly-queue/* branch with the CI secrets
	// and its actor is github-merge-queue[bot].
	prRiskMergeGroupValue = "${{ github.event_name == 'merge_group' }}"
	// pr-risk.yml's and pr.yml's gate id for the decision job's result.
	prRiskCoverageGateID = "BAZEL_COVERAGE"
	// build-embedded's artifact (embedded-test-binaries) feeds exactly the
	// legacy test jobs of both retired tiers.
	prRiskBuildEmbeddedJob = "build-embedded"
	prRiskBuildEmbeddedID  = "BUILD_EMBEDDED"
)

// retiredTier: one D2 step's legacy tier, the committed flag that retires
// it, the decision job's output for it, and the Bazel lanes that replace it.
type retiredTier struct {
	workflow   string            // the workflow whose legacy jobs stand down
	output     string            // bazel-coverage's output
	flag       string            // the committed workflow env flag
	envKey     string            // the decision step's env key reading flag
	coversEnv  string            // both gates' env key reading output
	retiredID  string            // pr.yml's gate id: the lanes ran remotely and passed
	jobs       map[string]string // legacy job -> its workflow's gate id
	bazelLanes []string          // bazel.yml jobs
}

var retiredTiers = []retiredTier{
	{
		workflow: prRiskWorkflowName, output: "embedded", flag: "BAZEL_RETIRES_LEGACY_EMBEDDED", envKey: "RETIRED_EMBEDDED",
		coversEnv: "BAZEL_COVERS_EMBEDDED", retiredID: "BAZEL_EMBEDDED_RETIRED",
		jobs: map[string]string{
			"test-embedded-storage":     "TEST_EMBEDDED_STORAGE",
			"test-embedded-conformance": "TEST_EMBEDDED_CONFORMANCE",
			"test-embedded-cmd":         "TEST_EMBEDDED_CMD",
		},
		bazelLanes: []string{bazelEmbedJobName},
	},
	{
		workflow: prRiskWorkflowName, output: "dolt_server", flag: "BAZEL_RETIRES_LEGACY_DOLT_SERVER_TIERS", envKey: "RETIRED_DOLT_SERVER",
		coversEnv: "BAZEL_COVERS_DOLT_SERVER", retiredID: "BAZEL_DOLT_SERVER_RETIRED",
		jobs: map[string]string{
			"test-proxied-cmd":         "TEST_PROXIED_CMD",
			"test-server-storage":      "TEST_SERVER_STORAGE",
			"test-server-storage-full": "TEST_SERVER_STORAGE_FULL",
		},
		bazelLanes: []string{bazelProxiedJobName, bazelServerJobName},
	},
	// Step 3: pr.yml's own legacy jobs, whose Bazel lanes run in the same
	// pr.yml run (scripts/pr_lanes_bazel_coverage_test.go).
	{
		workflow: "pr.yml", output: "pr_lanes", flag: "BAZEL_RETIRES_LEGACY_PR_LANES", envKey: "RETIRED_PR_LANES",
		coversEnv: "BAZEL_COVERS_PR_LANES", retiredID: "BAZEL_PR_LANES_RETIRED",
		jobs: map[string]string{
			"build-artifacts":            "BUILD_ARTIFACTS",
			"pr-core-wrapper":            "PR_CORE_WRAPPER",
			"check-cmd-bd-puregeo-tests": "CHECK_CMD_BD_PUREGEO_TESTS",
			"test-domain-uow":            "TEST_DOMAIN_UOW",
			"contract-corpus":            "CONTRACT_CORPUS",
		},
		bazelLanes: []string{bazelJobName, bazelPureJobName, bazelDoltJobName},
	},
}

// riskTiers: the tiers whose legacy jobs are pr-risk.yml's.
func riskTiers() []retiredTier {
	var out []retiredTier
	for _, r := range retiredTiers {
		if r.workflow == prRiskWorkflowName {
			out = append(out, r)
		}
	}
	return out
}

func (r retiredTier) retiredValue() string { return "${{ env." + r.flag + " == 'true' }}" }

// The legacy test jobs' if: the existing risk tier, and not covered.
func (r retiredTier) legacyIf() string {
	return "needs.detect-ci-tier.outputs.full_embedded == 'true' && needs." + prRiskCoverageJobName + ".outputs." + r.output + " != 'true'"
}

// build-embedded's if: the risk tier, and some consumer not covered.
var prRiskBuildEmbeddedIf = "needs.detect-ci-tier.outputs.full_embedded == 'true' && (needs." + prRiskCoverageJobName + ".outputs.embedded != 'true' || needs." +
	prRiskCoverageJobName + ".outputs.dolt_server != 'true')"

// retiredJobs: every retired pr-risk.yml legacy test job -> its tier.
func retiredJobs() map[string]retiredTier {
	out := map[string]retiredTier{}
	for _, r := range riskTiers() {
		for job := range r.jobs {
			out[job] = r
		}
	}
	return out
}

// The bazel.yml rbe step env keys the decision copies verbatim.
var prRiskSharedDecisionEnv = []string{"FORK"}

// The decision's Dependabot test: Dependabot runs get no Actions secrets, so
// they are covered (like fork PRs) only while BAZEL_COVERS_FORKS is "true".
const prRiskDependabotValue = "${{ github.actor == 'dependabot[bot]' }}"

// The committed flag that covers fork and Dependabot PRs too (rbe-fork), and
// its expression in both decision steps.
const (
	prCoversForksFlag  = "BAZEL_COVERS_FORKS"
	prCoversForksValue = "${{ env." + prCoversForksFlag + " == 'true' }}"
)

// rbeFacts: what GitHub evaluates the decision steps' env expressions on.
type rbeFacts struct {
	event  string // github.event_name
	rbeVar string // vars.RBE_WEST_WORKERS ("" = unset)
	secret string // secrets.RBE_WEST_EXECUTOR ("" = unavailable: fork, Dependabot, unset)
	fork   bool   // github.event.pull_request.head.repo.fork
	// The committed env.BAZEL_RETIRES_LEGACY_* flags, in retiredTiers order.
	retired [3]string
	// github.actor is dependabot[bot] (fixed for a PR's runs and re-runs).
	dependabot bool
	// The committed env.BAZEL_COVERS_FORKS.
	coversForks string
	// What rbe-fork-mint's /v1/status answers this run (bazelTestMintEnv;
	// "" = unreachable). Only fork and Dependabot pull_request runs ask.
	mint string
}

func (f rbeFacts) String() string {
	return fmt.Sprintf("event=%s var=%q secret=%v fork=%v retired=%q dependabot=%v covers-forks=%q mint=%q",
		f.event, f.rbeVar, f.secret != "", f.fork, f.retired, f.dependabot, f.coversForks, f.mint)
}

// covers: whether a run with tier i's flag set is covered, the decision
// both workflows must take for tier i: every merge group (the queue's
// branch is this repository's, with the CI secrets; bazel.yml's rbe job
// never treats it as a fork); same-repo, non-Dependabot PRs always; fork
// and Dependabot PRs while BAZEL_COVERS_FORKS is "true". Never what the
// mint says: a covered fork run it does not serve is red.
func (f rbeFacts) covers(i int) bool {
	if !strings.EqualFold(f.retired[i], "true") {
		return false
	}
	if f.event == "merge_group" {
		return true
	}
	return f.event == "pull_request" &&
		(!f.fork && !f.dependabot || strings.EqualFold(f.coversForks, "true"))
}

// forkPR: a run that asks rbe-fork-mint (bazel.yml's rbe job).
func (f rbeFacts) forkPR() bool { return f.event == "pull_request" && (f.fork || f.dependabot) }

// rbeMintAnswers: the mint states every forkPR fact is tried with.
var rbeMintAnswers = []string{"", "closed", "ro", "rw"}

var (
	rbeEvents    = []string{"pull_request", "merge_group", "push", "workflow_dispatch", "pull_request_target"}
	rbeVarValues = []string{"", "true", "True", "TRUE", "false", "1", "yes"}
	rbeSecrets   = []string{"", "grpcs://rbe.example:443"}
	// Every flag on, each alone, all off, and the case-insensitive and
	// non-boolean spellings of each.
	retiredValues = [][3]string{
		{"true", "true", "true"}, {"true", "false", "false"}, {"false", "true", "false"}, {"false", "false", "true"},
		{"false", "false", "false"}, {"True", "", ""}, {"", "TRUE", ""}, {"", "yes", "True"},
	}
)

// rbeFactsMatrix: every combination of the facts the decisions read.
// BAZEL_COVERS_FORKS takes both values (and other spellings) where it can
// matter, fork and Dependabot facts, and "true" elsewhere only with every
// tier retired (to show it changes nothing there); the mint answers only
// matter to forkPR facts.
func rbeFactsMatrix() []rbeFacts {
	var out []rbeFacts
	allRetired := [3]string{"true", "true", "true"}
	for _, event := range rbeEvents {
		for _, v := range rbeVarValues {
			for _, secret := range rbeSecrets {
				for _, fork := range []bool{false, true} {
					for _, retired := range retiredValues {
						for _, dependabot := range []bool{false, true} {
							covers := []string{"false"}
							switch {
							case (fork || dependabot) && retired == allRetired:
								covers = []string{"false", "true", "True", "", "yes"}
							case fork || dependabot, retired == allRetired:
								covers = []string{"false", "true"}
							}
							for _, c := range covers {
								f := rbeFacts{event, v, secret, fork, retired, dependabot, c, ""}
								if !f.forkPR() {
									out = append(out, f)
									continue
								}
								for _, m := range rbeMintAnswers {
									f.mint = m
									out = append(out, f)
								}
							}
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
	if len(retiredTiers) != len(f.retired) {
		t.Fatalf("rbeFacts.retired has %d flags, retiredTiers %d", len(f.retired), len(retiredTiers))
	}
	for i, r := range retiredTiers {
		if expr == r.retiredValue() {
			return strconv.FormatBool(strings.EqualFold(f.retired[i], "true"))
		}
	}
	switch expr {
	case prCoversForksValue:
		return strconv.FormatBool(strings.EqualFold(f.coversForks, "true"))
	case "${{ github.event.pull_request.number }}":
		if f.event == "pull_request" || f.event == "pull_request_target" {
			return "7123"
		}
		return ""
	case prRiskPullRequestValue:
		return strconv.FormatBool(f.event == "pull_request")
	case prRiskMergeGroupValue:
		return strconv.FormatBool(f.event == "merge_group")
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
	env := map[string]string{bazelTestMintEnv: f.mint}
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

// The decision job reads the committed flags, the event, the actor and,
// through bazel.yml's rbe job's own expression, the fork flag: nothing a
// re-run can change, so never RBE_WEST_WORKERS, any other variable or any
// secret, and it runs no repository code. pr.yml runs the identical job, and
// both workflows commit the same flags. Nothing else in pr-risk.yml reads the
// facts, and nothing in it reads a secret.
func TestPRRiskBazelCoverageJob(t *testing.T) {
	risk := readCIWorkflow(t, prRiskWorkflowName)
	job := risk.job(t, prRiskCoverageJobName)
	// F3: this job moved to Blacksmith for same-repo PRs (and merge_group);
	// forks and Dependabot stay on ubuntu-latest (TestSameRepoBlacksmithRunners
	// pins the same literal for pr.yml's copy).
	if len(job.Needs) != 0 || job.If != "" || job.RunsOn != sameRepoBlacksmith2vcpu || len(job.Env) != 0 || job.ContinueOnError || job.TimeoutMinutes == 0 {
		t.Errorf("%s: needs %v, if %q, runs-on %q, env %v, continue-on-error %v, timeout %d; want no needs, if, env or continue-on-error, on %q, a timeout",
			prRiskCoverageJobName, job.Needs, job.If, job.RunsOn, job.Env, job.ContinueOnError, job.TimeoutMinutes, sameRepoBlacksmith2vcpu)
	}
	wantOutputs := map[string]string{}
	wantEnv := map[string]string{"PULL_REQUEST": prRiskPullRequestValue, "MERGE_GROUP": prRiskMergeGroupValue, "DEPENDABOT": prRiskDependabotValue, "COVERS_FORKS": prCoversForksValue}
	for _, r := range retiredTiers {
		wantOutputs[r.output] = "${{ steps.decide.outputs." + r.output + " }}"
		wantEnv[r.envKey] = r.retiredValue()
	}
	if !reflect.DeepEqual(job.Outputs, wantOutputs) {
		t.Errorf("%s outputs = %v, want %v", prRiskCoverageJobName, job.Outputs, wantOutputs)
	}
	if prJob := readCIWorkflow(t, "pr.yml").job(t, prRiskCoverageJobName); !reflect.DeepEqual(prJob, job) {
		t.Errorf("pr.yml's %s differs from pr-risk.yml's:\n%+v\n%+v", prRiskCoverageJobName, prJob, job)
	}
	riskEnv, prEnv := workflowEnv(t, prRiskWorkflowName), workflowEnv(t, "pr.yml")
	committed := []string{prCoversForksFlag}
	for _, r := range retiredTiers {
		committed = append(committed, r.flag)
	}
	for _, flag := range committed {
		if riskFlag, prFlag := riskEnv[flag], prEnv[flag]; riskFlag != prFlag || (riskFlag != "true" && riskFlag != "false") {
			t.Errorf("%s: pr-risk.yml %q, pr.yml %q; want the same literal \"true\" or \"false\" in both", flag, riskFlag, prFlag)
		}
	}
	step := prRiskCoverageStep(t)
	rbeStep := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName).Steps[0]
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
	// repository variable; only the workflow env sets a flag.
	stepEnv := ".jobs." + prRiskCoverageJobName + ".steps[0].env."
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	flags := map[string]bool{prCoversForksFlag: true}
	flagAlt := []string{prCoversForksFlag}
	for _, r := range retiredTiers {
		flags[r.flag] = true
		flagAlt = append(flagAlt, r.flag)
	}
	facts := regexp.MustCompile(`(?i)RBE_WEST_WORKERS|\bvars\s*(\.|\[)|head\.repo\.fork|RBE_WEST_EXECUTOR|github\.actor|dependabot|BAZEL_RETIRES_|` + strings.Join(flagAlt, "|"))
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", prRiskWorkflowName)), "", func(path string, key bool, value string) {
		if key {
			if flags[value] && path != ".env."+value {
				t.Errorf("%s: %s sets %s; only the workflow env may", prRiskWorkflowName, path, value)
			}
			return
		}
		if secretRef.MatchString(value) {
			t.Errorf("%s: %s reads secrets (%q); PR Risk needs none", prRiskWorkflowName, path, value)
		}
		// F3: detect-ci-tier/bazel-coverage/ci-gate's own runs-on picks a
		// runner venue (Blacksmith vs ubuntu-latest) for trusted same-repo
		// PRs; it shares some predicates (github.actor, dependabot,
		// head.repo.full_name) with the coverage decision but decides
		// something else entirely - not a re-derivation of the decision
		// (TestSameRepoBlacksmithRunners pins the literal).
		if strings.HasSuffix(path, ".runs-on") && (value == sameRepoBlacksmith2vcpu || value == sameRepoBlacksmith4vcpu || value == sameRepoBlacksmith8vcpu) {
			return
		}
		if facts.MatchString(value) && !strings.HasPrefix(path, stepEnv) && !strings.HasPrefix(path, ".jobs."+prRiskCoverageJobName+".steps[0].run") {
			t.Errorf("%s: %s re-derives the Bazel coverage decision (%q); read needs.%s.outputs", prRiskWorkflowName, path, value, prRiskCoverageJobName)
		}
	})
	// In pr.yml too, only the workflow env sets a flag and only the
	// decision step reads one (ci-gate names them in its messages).
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", "pr.yml")), "", func(path string, key bool, value string) {
		if key {
			if flags[value] && path != ".env."+value {
				t.Errorf("pr.yml: %s sets %s; only the workflow env may", path, value)
			}
			return
		}
		if regexp.MustCompile(`env\.BAZEL_(RETIRES_|COVERS_FORKS\b)`).MatchString(value) && !strings.HasPrefix(path, stepEnv) {
			t.Errorf("pr.yml: %s reads a BAZEL_RETIRES_* or BAZEL_COVERS_FORKS flag (%q); read needs.%s.outputs", path, value, prRiskCoverageJobName)
		}
	})
	// Every BAZEL_RETIRES_* flag either workflow commits is one of retiredTiers.
	for name, env := range map[string]map[string]string{prRiskWorkflowName: riskEnv, "pr.yml": prEnv} {
		for k := range env {
			if strings.HasPrefix(k, "BAZEL_RETIRES_") && !flags[k] {
				t.Errorf("%s commits %s, which no retiredTiers entry covers", name, k)
			}
		}
	}
}

// prGateFor: pr.yml's ci-gate scenario for one run whose Bazel call took
// this mode with every lane that runs in it passing, and whose bazel-coverage
// job reported these outputs.
func prGateFor(t *testing.T, lanes map[string]map[string]bool, event, mode string, covered map[string]string) bazelGateScenario {
	t.Helper()
	outputs := map[string]string{}
	for lane, modes := range lanes {
		if modes[mode] {
			outputs[lane] = "success"
		}
	}
	return bazelGateScenario{
		name: fmt.Sprintf("%s mode %s covered %v", event, mode, covered), event: event,
		mode: mode, enabled: bazelModeEnabled(mode), call: "success",
		outputs: outputs, covered: covered,
	}
}

// coveredAll: bazel-coverage outputs with every tier set to v.
func coveredAll(v string) map[string]string {
	out := map[string]string{}
	for _, r := range retiredTiers {
		out[r.output] = v
	}
	return out
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

// Never both gates green with neither a legacy tier nor its Bazel lanes
// having run it. covered (both workflows' actual decision scripts) is, per
// tier, the committed flag on a same-repo, non-Dependabot pull_request:
// nothing a re-run can change. Across two runs (PR Risk's and pr.yml's, or a
// re-run of either) that see RBE_WEST_WORKERS and the executor secret
// differently, every combination: if PR Risk skipped a legacy tier, pr.yml's
// actual gate step is green only if the lanes ran remotely. The happy path
// (flags, variable and secret on) is green with the legacy tiers skipped;
// the kill switch (variable off) or a missing secret with a flag still on is
// red, naming that tier's id.
func TestPRRiskDecisionMatchesBazelMode(t *testing.T) {
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

	// The Bazel lanes PR Risk defers to: remote-only, gated by pr.yml, and
	// not skippable in mode remote.
	gate := pr.job(t, "ci-gate")
	required := strings.Fields(gateStep.Env["CI_GATE_REQUIRED"])
	cmd := exec.Command("bash", filepath.Join(sourceRepoRoot(t), bazelGateScript), "skips")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "BAZEL_RBE_MODE=remote", "BAZEL_RBE_ENABLED=true"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	remoteSkips := strings.Fields(string(out))
	if !contains(required, prRiskCoverageGateID) {
		t.Errorf("pr.yml's ci-gate does not require %s", prRiskCoverageGateID)
	}
	if gateStep.Env[prRiskCoverageGateID] != "${{ needs."+prRiskCoverageJobName+".result }}" {
		t.Errorf("pr.yml's ci-gate %s = %q, want the decision job's result", prRiskCoverageGateID, gateStep.Env[prRiskCoverageGateID])
	}
	for _, r := range retiredTiers {
		if !contains(required, r.retiredID) {
			t.Errorf("pr.yml's ci-gate does not require %s", r.retiredID)
		}
		if gateStep.Env[r.coversEnv] != "${{ needs."+prRiskCoverageJobName+".outputs."+r.output+" }}" {
			t.Errorf("pr.yml's ci-gate %s = %q, want needs.%s.outputs.%s", r.coversEnv, gateStep.Env[r.coversEnv], prRiskCoverageJobName, r.output)
		}
		for _, lane := range r.bazelLanes {
			if !lanes[lane]["remote"] {
				t.Fatalf("%s does not run in mode remote", lane)
			}
			if !contains(required, bazelLaneGateIDs[lane]) {
				t.Errorf("pr.yml's ci-gate does not require %s", bazelLaneGateIDs[lane])
			}
			if contains(remoteSkips, bazelLaneGateIDs[lane]) {
				t.Fatalf("pr.yml's gate accepts a skipped %s in mode remote", bazelLaneGateIDs[lane])
			}
		}
	}
	if !contains(gate.Needs, "bazel") || !contains(gate.Needs, prRiskCoverageJobName) {
		t.Errorf("pr.yml's ci-gate needs %v, want bazel and %s", gate.Needs, prRiskCoverageJobName)
	}

	// Both workflows run for the same PRs, so a PR Risk run that defers
	// always has a pr.yml run that gates the lanes.
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

	// The decisions: bazel.yml's mode (which reads no flag) and both
	// workflows' bazel-coverage outputs.
	type decided struct {
		covered, prCovered map[string]string
		mode               string
	}
	modeMemo := map[rbeFacts]string{}
	coverMemo := map[rbeFacts][2]map[string]string{}
	decideMemo := map[rbeFacts]decided{}
	decide := func(t *testing.T, f rbeFacts) decided {
		t.Helper()
		if d, ok := decideMemo[f]; ok {
			return d
		}
		noFlags := f
		noFlags.retired = [3]string{}
		noFlags.coversForks = ""
		mode, ok := modeMemo[noFlags]
		if !ok {
			bazel, err := runDecisionStep(t, rbeStep, f, call.With)
			if err != nil {
				t.Fatalf("bazel.yml rbe step: %v", err)
			}
			mode = bazel["mode"]
			modeMemo[noFlags] = mode
		}
		// The coverage steps read no variable, secret or mint (their env is
		// pinned exactly by TestPRRiskBazelCoverageJob): one run per fact
		// without them.
		noMint := f
		noMint.rbeVar, noMint.secret, noMint.mint = "", "", ""
		cov, ok := coverMemo[noMint]
		if !ok {
			risk, err := runDecisionStep(t, riskStep, noMint, nil)
			if err != nil || len(risk) != len(retiredTiers) {
				t.Fatalf("%s decision step: %v %v", prRiskCoverageJobName, risk, err)
			}
			prd, err := runDecisionStep(t, prStep, noMint, nil)
			if err != nil {
				t.Fatalf("pr.yml %s step: %v", prRiskCoverageJobName, err)
			}
			cov = [2]map[string]string{risk, prd}
			coverMemo[noMint] = cov
		}
		d := decided{cov[0], cov[1], mode}
		decideMemo[f] = d
		return d
	}

	// One run's facts: the decision itself.
	saw := map[string]bool{}
	for _, f := range rbeFactsMatrix() {
		d := decide(t, f)
		for i, r := range retiredTiers {
			want := f.covers(i)
			if d.covered[r.output] != strconv.FormatBool(want) || d.prCovered[r.output] != d.covered[r.output] {
				t.Errorf("%v: %s = %q (pr.yml %q), want %v", f, r.output, d.covered[r.output], d.prCovered[r.output], want)
			}
			saw[r.output+"="+strconv.FormatBool(want)] = true
		}
	}
	for _, r := range retiredTiers {
		if !saw[r.output+"=true"] || !saw[r.output+"=false"] {
			t.Errorf("matrix never exercised both outcomes of %s: %v", r.output, saw)
		}
	}

	// Two runs (PR Risk's and pr.yml's, each possibly re-run) that agree on
	// everything committed or fixed by the PR and differ in what an admin can
	// change between them: the variable and the secret.
	gateMemo := map[string]bool{}
	prGatePasses := func(t *testing.T, event, mode string, covered map[string]string) bool {
		t.Helper()
		key := fmt.Sprint(event, "/", mode, "/", covered)
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
		mints := []string{f.mint}
		if f.forkPR() {
			mints = rbeMintAnswers // rbe-fork may open or close between the runs
		}
		for _, v := range rbeVarValues {
			for _, secret := range rbeSecrets {
				for _, m := range mints {
					g := f
					g.rbeVar, g.secret, g.mint = v, secret, m
					prRun := decide(t, g)
					bazelRan := bazelRemoteModes[prRun.mode] // and passed: every lane succeeds here
					for _, r := range retiredTiers {
						// PR Risk's tiers stand down in PR Risk's run; pr.yml's
						// own (step 3) in this same pr.yml run.
						legacyRan := risk.covered[r.output] != "true"
						if r.workflow == "pr.yml" {
							legacyRan = prRun.prCovered[r.output] != "true"
						}
						if !legacyRan && !bazelRan && prGatePasses(t, g.event, prRun.mode, prRun.prCovered) {
							t.Errorf("PR Risk run %v skipped the legacy %s tier and pr.yml run (var %q, secret %v, mint %q, mode %s, covered %v) is green without its Bazel lanes",
								f, r.output, v, secret != "", m, prRun.mode, prRun.prCovered)
						}
					}
				}
			}
		}
	}

	// Named cases, for the record. Forks and Dependabot run in mode cache
	// while rbe-fork is closed, which, like local, skips the remote-only
	// lanes, or remotely in fork-ro/fork-rw; while BAZEL_COVERS_FORKS is
	// "false" they keep the legacy tiers either way. While it is "true" they
	// are covered: green only with their lanes run remotely, red in mode
	// cache. reds: the retired ids a red gate must name.
	both, embOnly, dsOnly, prOnly, none := [3]string{"true", "true", "true"}, [3]string{"true", "false", "false"}, [3]string{"false", "true", "false"},
		[3]string{"false", "false", "true"}, [3]string{"false", "false", "false"}
	allRetired := []string{"BAZEL_EMBEDDED_RETIRED", "BAZEL_DOLT_SERVER_RETIRED", "BAZEL_PR_LANES_RETIRED"}
	for _, c := range []struct {
		name     string
		f        rbeFacts
		mode     string
		covered  map[string]string
		prPasses bool
		reds     []string
	}{
		{"same-repo PR, farm on", rbeFacts{"pull_request", "true", "x", false, both, false, "false", ""}, "remote", coveredAll("true"), true, nil},
		{"same-repo PR, kill switch (var unset)", rbeFacts{"pull_request", "", "x", false, both, false, "false", ""}, "skip", coveredAll("true"), false, allRetired},
		{"same-repo PR, executor secret missing", rbeFacts{"pull_request", "true", "", false, both, false, "false", ""}, "cache", coveredAll("true"), false, allRetired},
		{"same-repo PR, only embedded retired, var unset", rbeFacts{"pull_request", "", "x", false, embOnly, false, "false", ""}, "skip", map[string]string{"embedded": "true", "dolt_server": "false", "pr_lanes": "false"}, false, []string{"BAZEL_EMBEDDED_RETIRED"}},
		{"same-repo PR, only proxied/server retired, var unset", rbeFacts{"pull_request", "", "x", false, dsOnly, false, "false", ""}, "skip", map[string]string{"embedded": "false", "dolt_server": "true", "pr_lanes": "false"}, false, []string{"BAZEL_DOLT_SERVER_RETIRED"}},
		{"same-repo PR, only pr.yml's jobs retired, var unset", rbeFacts{"pull_request", "", "x", false, prOnly, false, "false", ""}, "skip", map[string]string{"embedded": "false", "dolt_server": "false", "pr_lanes": "true"}, false, []string{"BAZEL_PR_LANES_RETIRED"}},
		{"same-repo PR, only pr.yml's jobs retired, secret missing", rbeFacts{"pull_request", "true", "", false, prOnly, false, "false", ""}, "cache", map[string]string{"embedded": "false", "dolt_server": "false", "pr_lanes": "true"}, false, []string{"BAZEL_PR_LANES_RETIRED"}},
		{"same-repo PR, only pr.yml's jobs retired, farm on", rbeFacts{"pull_request", "true", "x", false, prOnly, false, "false", ""}, "remote", map[string]string{"embedded": "false", "dolt_server": "false", "pr_lanes": "true"}, true, nil},
		{"same-repo PR, flags reverted, var unset", rbeFacts{"pull_request", "", "x", false, none, false, "false", ""}, "skip", coveredAll("false"), true, nil},
		{"same-repo PR, flags reverted, secret missing", rbeFacts{"pull_request", "true", "", false, none, false, "false", ""}, "cache", coveredAll("false"), true, nil},
		{"fork PR", rbeFacts{"pull_request", "true", "", true, both, false, "false", ""}, "cache", coveredAll("false"), true, nil},
		{"fork PR, var unset", rbeFacts{"pull_request", "", "", true, both, false, "false", ""}, "cache", coveredAll("false"), true, nil},
		{"fork PR somehow with a secret", rbeFacts{"pull_request", "true", "x", true, both, false, "false", ""}, "cache", coveredAll("false"), true, nil},
		{"Dependabot PR (no Actions secrets)", rbeFacts{"pull_request", "true", "", false, both, true, "false", ""}, "cache", coveredAll("false"), true, nil},
		// rbe-fork decides for Dependabot like for forks (the mint, not the
		// farm switch): unreachable here, so cache.
		{"Dependabot PR, var unset", rbeFacts{"pull_request", "", "", false, both, true, "false", ""}, "cache", coveredAll("false"), true, nil},
		{"fork PR, rbe-fork ro", rbeFacts{"pull_request", "true", "", true, both, false, "false", "ro"}, "fork-ro", coveredAll("false"), true, nil},
		{"fork PR, rbe-fork rw", rbeFacts{"pull_request", "", "", true, both, false, "false", "rw"}, "fork-rw", coveredAll("false"), true, nil},
		{"fork PR, rbe-fork closed", rbeFacts{"pull_request", "true", "", true, both, false, "false", "closed"}, "cache", coveredAll("false"), true, nil},
		{"Dependabot PR, rbe-fork ro", rbeFacts{"pull_request", "true", "", false, both, true, "false", "ro"}, "fork-ro", coveredAll("false"), true, nil},
		{"covered fork PR, rbe-fork ro", rbeFacts{"pull_request", "true", "", true, both, false, "true", "ro"}, "fork-ro", coveredAll("true"), true, nil},
		{"covered fork PR, rbe-fork rw", rbeFacts{"pull_request", "", "", true, both, false, "true", "rw"}, "fork-rw", coveredAll("true"), true, nil},
		{"covered fork PR, rbe-fork closed", rbeFacts{"pull_request", "true", "", true, both, false, "true", "closed"}, "cache", coveredAll("true"), false, allRetired},
		{"covered fork PR, mint unreachable", rbeFacts{"pull_request", "true", "", true, both, false, "true", ""}, "cache", coveredAll("true"), false, allRetired},
		{"covered Dependabot PR, rbe-fork ro", rbeFacts{"pull_request", "true", "", false, both, true, "true", "ro"}, "fork-ro", coveredAll("true"), true, nil},
		{"covered Dependabot PR, rbe-fork closed", rbeFacts{"pull_request", "", "", false, both, true, "true", "closed"}, "cache", coveredAll("true"), false, allRetired},
		{"covered fork PR, only pr.yml's jobs retired, rbe-fork closed", rbeFacts{"pull_request", "true", "", true, prOnly, false, "true", "closed"}, "cache", map[string]string{"embedded": "false", "dolt_server": "false", "pr_lanes": "true"}, false, []string{"BAZEL_PR_LANES_RETIRED"}},
		// Merge queue: covered like a same-repo PR (the legacy tiers stay
		// retired), so the Bazel lanes must run remotely and pass.
		{"merge_group", rbeFacts{"merge_group", "true", "x", false, both, false, "false", ""}, "remote", coveredAll("true"), true, nil},
		{"merge_group, kill switch (var unset)", rbeFacts{"merge_group", "", "x", false, both, false, "false", ""}, "skip", coveredAll("true"), false, allRetired},
		{"merge_group, executor secret missing", rbeFacts{"merge_group", "true", "", false, both, false, "false", ""}, "cache", coveredAll("true"), false, allRetired},
		{"merge_group, flags reverted, var unset", rbeFacts{"merge_group", "", "x", false, none, false, "false", ""}, "skip", coveredAll("false"), true, nil},
		{"merge_group, only embedded retired", rbeFacts{"merge_group", "true", "x", false, embOnly, false, "false", ""}, "remote", map[string]string{"embedded": "true", "dolt_server": "false", "pr_lanes": "false"}, true, nil},
	} {
		d := decide(t, c.f)
		if !reflect.DeepEqual(d.covered, c.covered) || d.mode != c.mode {
			t.Errorf("%s: covered = %v, mode = %q; want %v, %s", c.name, d.covered, d.mode, c.covered, c.mode)
		}
		pass, out := runPRGateStep(t, gateStep, prGateFor(t, lanes, c.f.event, d.mode, d.prCovered))
		if pass != c.prPasses {
			t.Errorf("%s: pr.yml gate pass = %v, want %v\n%s", c.name, pass, c.prPasses, out)
		}
		for _, r := range retiredTiers {
			named := regexp.MustCompile(`::error::` + r.retiredID + `\b`).MatchString(out)
			if named != contains(c.reds, r.retiredID) {
				t.Errorf("%s: red gate names %s = %v, want %v:\n%s", c.name, r.retiredID, named, contains(c.reds, r.retiredID), out)
			}
		}
	}
	for _, r := range retiredTiers {
		// Covered, remote, but one of the tier's lanes failed, was cancelled
		// or reported nothing: red, naming the retirement too. The other
		// tier's lanes' results do not matter to this id.
		for _, lane := range r.bazelLanes {
			for _, res := range []string{"failure", "cancelled", ""} {
				sc := prGateFor(t, lanes, "pull_request", "remote", coveredAll("true"))
				sc.outputs[lane] = res
				if pass, out := runPRGateStep(t, gateStep, sc); pass || !strings.Contains(out, "::error::"+r.retiredID) {
					t.Errorf("covered, %s lane %s %q: gate pass = %v, want red naming %s\n%s", r.output, lane, res, pass, r.retiredID, out)
				}
				// Not covered: the lane's own id still reds the gate, but the
				// retirement check does not fire.
				sc.covered = coveredAll("false")
				if pass, out := runPRGateStep(t, gateStep, sc); pass || strings.Contains(out, "::error::"+r.retiredID) {
					t.Errorf("not covered, %s lane %s %q: gate pass = %v, want red without %s\n%s", r.output, lane, res, pass, r.retiredID, out)
				}
			}
		}
		// Covered, and lane results of success the mode cannot produce (the
		// lanes run only in mode remote): the mode alone still makes it red.
		for _, mode := range []string{"skip", "local", "cache"} {
			cov := coveredAll("false")
			cov[r.output] = "true"
			sc := prGateFor(t, lanes, "pull_request", mode, cov)
			for _, lane := range r.bazelLanes {
				sc.outputs[lane] = "success"
			}
			if pass, out := runPRGateStep(t, gateStep, sc); pass || !strings.Contains(out, "::error::"+r.retiredID) {
				t.Errorf("%s covered, mode %s, lanes reported success: gate pass = %v, want red naming %s\n%s", r.output, mode, pass, r.retiredID, out)
			}
		}
	}
	// pr.yml's coverage job failed: red even where nothing is retired.
	for _, res := range []string{"failure", "cancelled", "skipped"} {
		sc := prGateFor(t, lanes, "pull_request", "skip", coveredAll(""))
		sc.coverage = res
		if pass, out := runPRGateStep(t, gateStep, sc); pass || !strings.Contains(out, "::error::"+prRiskCoverageGateID) {
			t.Errorf("coverage job %s: gate pass = %v, want red naming %s\n%s", res, pass, prRiskCoverageGateID, out)
		}
	}
	// A value that is not a boolean fails the job rather than deciding.
	base := map[string]string{"PULL_REQUEST": "true", "FORK": "false", "DEPENDABOT": "false", "COVERS_FORKS": "false"}
	for _, r := range retiredTiers {
		base[r.envKey] = "true"
	}
	for k := range base {
		env := copyMap(base)
		env[k] = ""
		if out, err := runBazelRBEDecision(t, riskStep.Run, env); err == nil {
			t.Errorf("decision with %s='' succeeded with %v; want failure", k, out)
		}
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

// The legacy tiers' test jobs skip only when their tier is covered, and
// build-embedded (whose artifact only they consume) only when every tier is;
// PR Risk's gate accepts those skips only then: the jobs' needs and if, the
// gate's wiring, and the actual gate step over every risk tier x decision,
// including a missing, failed or non-true decision with the jobs skipped
// (red).
func TestPRRiskLegacyTiersDeferToBazelLanes(t *testing.T) {
	requireHostTool(t, "bash")
	risk := readCIWorkflow(t, prRiskWorkflowName)
	retired := retiredJobs()
	for name, r := range retired {
		job := risk.job(t, name)
		if job.If != r.legacyIf() {
			t.Errorf("%s if = %q, want %q", name, job.If, r.legacyIf())
		}
		if want := []string{"detect-ci-tier", prRiskCoverageJobName, prRiskBuildEmbeddedJob}; !reflect.DeepEqual([]string(job.Needs), want) {
			t.Errorf("%s needs = %v, want %v", name, job.Needs, want)
		}
	}
	build := risk.job(t, prRiskBuildEmbeddedJob)
	if build.If != prRiskBuildEmbeddedIf {
		t.Errorf("%s if = %q, want %q", prRiskBuildEmbeddedJob, build.If, prRiskBuildEmbeddedIf)
	}
	if want := []string{"detect-ci-tier", prRiskCoverageJobName}; !reflect.DeepEqual([]string(build.Needs), want) {
		t.Errorf("%s needs = %v, want %v", prRiskBuildEmbeddedJob, build.Needs, want)
	}
	// build-embedded's artifact has no consumer but the retired jobs (so
	// skipping it where they all skip loses nothing), and no other job
	// depends on the decision.
	var artifact string
	for _, step := range build.Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			if artifact != "" {
				t.Errorf("%s uploads more than one artifact", prRiskBuildEmbeddedJob)
			}
			artifact = step.With["name"]
		}
	}
	if artifact == "" {
		t.Fatalf("%s uploads no artifact", prRiskBuildEmbeddedJob)
	}
	for name, job := range risk.Jobs {
		consumes := false
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/download-artifact@") && (step.With["name"] == artifact || step.With["name"] == "" || strings.Contains(step.With["pattern"], "*")) {
				consumes = true
			}
		}
		_, isRetired := retired[name]
		if consumes != isRetired {
			t.Errorf("%s downloads %s = %v; want exactly the retired jobs %v to", name, artifact, consumes, retiredJobNames())
		}
		if contains(job.Needs, prRiskBuildEmbeddedJob) && !isRetired && name != "ci-gate" {
			t.Errorf("%s needs %s; only the retired jobs may (it skips where they all do)", name, prRiskBuildEmbeddedJob)
		}
		if isRetired || name == "ci-gate" || name == prRiskCoverageJobName || name == prRiskBuildEmbeddedJob {
			continue
		}
		if strings.Contains(job.If, prRiskCoverageJobName) || contains(job.Needs, prRiskCoverageJobName) {
			t.Errorf("%s depends on %s; only %v and %s may stand down", name, prRiskCoverageJobName, retiredJobNames(), prRiskBuildEmbeddedJob)
		}
	}

	gate := risk.job(t, "ci-gate")
	step := gate.step(t, "Evaluate CI gate")
	required := strings.Fields(step.Env["CI_GATE_REQUIRED"])
	if !contains(gate.Needs, prRiskCoverageJobName) || !contains(required, prRiskCoverageGateID) ||
		step.Env[prRiskCoverageGateID] != "${{ needs."+prRiskCoverageJobName+".result }}" {
		t.Errorf("pr-risk ci-gate does not require %s's result: needs %v, env %v", prRiskCoverageJobName, gate.Needs, step.Env)
	}
	for _, r := range retiredTiers {
		want := "${{ needs." + prRiskCoverageJobName + ".outputs." + r.output + " }}"
		if r.workflow != prRiskWorkflowName {
			want = "" // pr.yml's own jobs: nothing in PR Risk reads it
		}
		if step.Env[r.coversEnv] != want {
			t.Errorf("pr-risk ci-gate %s = %q, want %q", r.coversEnv, step.Env[r.coversEnv], want)
		}
	}
	for name, r := range retired {
		if !contains(required, r.jobs[name]) {
			t.Errorf("pr-risk ci-gate no longer requires %s (it runs wherever the Bazel lanes do not)", r.jobs[name])
		}
	}
	if !contains(required, prRiskBuildEmbeddedID) {
		t.Errorf("pr-risk ci-gate no longer requires %s", prRiskBuildEmbeddedID)
	}
	gateID := func(job string) string {
		if r, ok := retired[job]; ok {
			return r.jobs[job]
		}
		if job == prRiskBuildEmbeddedJob {
			return prRiskBuildEmbeddedID
		}
		return ""
	}

	// Each gated job's result for (tier, decision), as the jobs' if: and
	// needs produce it: needs.* outputs are strings, so only 'true' skips;
	// a failed decision job skips its dependents, and a skipped
	// build-embedded skips its consumers.
	results := func(full bool, coverageResult string, cov map[string]string) map[string]string {
		r := map[string]string{}
		for _, job := range gate.Needs {
			r[job] = "success"
		}
		r[prRiskCoverageJobName] = coverageResult
		decided := coverageResult == "success"
		buildRuns := full && decided && (cov["embedded"] != "true" || cov["dolt_server"] != "true")
		if !buildRuns {
			r[prRiskBuildEmbeddedJob] = "skipped"
		}
		for name := range risk.Jobs {
			if !contains(gate.Needs, name) || name == "detect-ci-tier" || name == prRiskCoverageJobName || name == "test-nix" || name == prRiskBuildEmbeddedJob {
				continue
			}
			runs := full
			if tier, ok := retired[name]; ok {
				runs = full && decided && buildRuns && cov[tier.output] != "true"
			}
			if !runs {
				r[name] = "skipped"
			}
		}
		return r
	}
	outputs := func(full bool, cov map[string]string) map[string]string {
		o := map[string]string{"detect-ci-tier.full_embedded": strconv.FormatBool(full)}
		for _, r := range retiredTiers {
			o[prRiskCoverageJobName+"."+r.output] = cov[r.output]
		}
		return o
	}

	var scenarios []prRiskGateScenario
	covs := []map[string]string{
		coveredAll("true"), coveredAll("false"),
		{"embedded": "true", "dolt_server": "false"}, {"embedded": "false", "dolt_server": "true"},
	}
	for _, full := range []bool{true, false} {
		for _, cov := range covs {
			name := fmt.Sprintf("full_embedded=%v covered=%v", full, cov)
			r := results(full, "success", cov)
			scenarios = append(scenarios, prRiskGateScenario{name: name + ", as designed", results: r, outputs: outputs(full, cov), wantPass: true})
			for _, id := range append(retiredJobNames(), prRiskBuildEmbeddedJob) {
				if r[id] == "skipped" {
					// Skipped by design; if it ran anyway and failed, red.
					bad := copyMap(r)
					bad[id] = "failure"
					scenarios = append(scenarios, prRiskGateScenario{name + ", " + id + " ran and failed", bad, outputs(full, cov), false, gateID(id)})
					continue
				}
				for _, res := range []string{"skipped", "failure", "cancelled"} {
					bad := copyMap(r)
					bad[id] = res
					scenarios = append(scenarios, prRiskGateScenario{name + ", " + id + " " + res, bad, outputs(full, cov), false, gateID(id)})
				}
			}
		}
	}
	// A failed, cancelled or missing decision, or one that is not exactly
	// 'true', excuses nothing, whatever the jobs did.
	for _, bad := range []struct{ result, covered string }{
		{"failure", ""}, {"cancelled", ""}, {"skipped", ""}, {"failure", "true"}, {"cancelled", "true"},
	} {
		r := results(true, "success", coveredAll("false"))
		r[prRiskCoverageJobName] = bad.result
		for _, id := range append(retiredJobNames(), prRiskBuildEmbeddedJob) {
			r[id] = "skipped"
		}
		scenarios = append(scenarios, prRiskGateScenario{
			name:    fmt.Sprintf("decision %s covered=%q, legacy skipped", bad.result, bad.covered),
			results: r, outputs: outputs(true, coveredAll(bad.covered)), wantPass: false, wantMention: prRiskCoverageGateID,
		})
	}
	// Review F3 (step 2): per tier, with the other tier's output a valid
	// "false" (so its jobs and build-embedded ran), a successful decision
	// whose output for this tier is not exactly 'true' excuses none of this
	// tier's skipped jobs: a gate testing != "false" (or similar) fails here.
	for _, tier := range riskTiers() {
		for _, covered := range []string{"", "TRUE ", "yes", "1", "True\n", "false "} {
			cov := coveredAll("false")
			cov[tier.output] = covered
			for job, id := range tier.jobs {
				r := results(true, "success", coveredAll("false"))
				r[job] = "skipped"
				scenarios = append(scenarios, prRiskGateScenario{
					name:    fmt.Sprintf("decision success %s=%q, %s skipped", tier.output, covered, job),
					results: r, outputs: outputs(true, cov), wantPass: false, wantMention: id,
				})
			}
		}
		// And build-embedded's skip is excused only when both are exactly true.
		cov := coveredAll("true")
		cov[tier.output] = "yes"
		r := results(true, "success", coveredAll("true"))
		for job := range tier.jobs {
			r[job] = "success" // isolate BUILD_EMBEDDED's own skip rule
		}
		scenarios = append(scenarios, prRiskGateScenario{
			name:    fmt.Sprintf("decision success %s=yes, other true, build-embedded skipped", tier.output),
			results: r, outputs: outputs(true, cov), wantPass: false, wantMention: prRiskBuildEmbeddedID,
		})
	}
	// The decision job itself must succeed even when nothing else needs it.
	for _, res := range []string{"failure", "cancelled", "skipped"} {
		r := results(false, "success", coveredAll("false"))
		r[prRiskCoverageJobName] = res
		scenarios = append(scenarios, prRiskGateScenario{"docs-only, decision " + res, r, outputs(false, coveredAll("")), false, prRiskCoverageGateID})
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

// retiredJobNames: the retired legacy test jobs, sorted.
func retiredJobNames() []string {
	var out []string
	for name := range retiredJobs() {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Review F5 (D2 step 1; step 2 for the proxied and server lanes): each
// retired tier's Bazel lane checks, after the run, that every Bazel shard of
// its manifest-sharded targets ran exactly the tests its PR Risk shard script
// lists, for the targets and shard counts of the legacy jobs, and that its
// unsharded targets did not only skip.
func TestBazelRetiredLanesCheckListedTestsRan(t *testing.T) {
	risk := readCIWorkflow(t, prRiskWorkflowName)
	type suite struct {
		job, step, label, script string
		// shardCount overrides the expected check_shard_coverage.py shard
		// count for this suite when the Bazel lane's own manifest block runs
		// a different number of shards than this PR Risk job's matrix. Zero
		// means "same as this suite's PR Risk job matrix size" (the common
		// case for a lane that is a drop-in retirement of the legacy job).
		// -1 means "read this suite's label's own shard_count live from its
		// BUILD.bazel rule", via the liveShardCount dispatch table below
		// (bazelProxiedShardCount, bazelEmbeddedCmdShardCount,
		// bazelEmbeddedStorageShardCount); only a label present in that
		// table may use -1 (enforced below), so copying this onto another
		// suite needs a reviewed change there.
		shardCount int
	}
	// liveShardCount dispatches a suite's -1 shardCount to the accessor that
	// reads its target's own shard_count from its BUILD.bazel rule — the
	// single source of truth for a Bazel-only lane's shard split, which no
	// longer has to equal the retired PR Risk job's matrix size (F2's
	// bd_proxied_test; F1's bd_embedded_test and
	// embeddeddolt_embedded_test). See bazelProxiedShardCount's doc comment
	// (scripts/ci_workflow_test.go) for the shared rationale.
	liveShardCount := map[string]func(*testing.T) int{
		"//cmd/bd:bd_proxied_test":                                   bazelProxiedShardCount,
		"//cmd/bd:bd_embedded_test":                                  bazelEmbeddedCmdShardCount,
		"//internal/storage/embeddeddolt:embeddeddolt_embedded_test": bazelEmbeddedStorageShardCount,
	}
	for _, c := range []struct {
		lane, config string
		suites       []suite
		whole        []string
	}{
		{bazelEmbedJobName, "embedded", []suite{
			// bazel-embedded runs its own duration-balanced manifest blocks
			// (scripts/ci/embedded_{cmd,storage}_test_durations.json), not
			// PR Risk's frozen 20- and 5-shard blocks (slice F1): neither is
			// a drop-in retirement of its legacy job's shard count, just its
			// tests. bazelEmbeddedCmdShardCount/bazelEmbeddedStorageShardCount
			// read the real counts from BUILD.bazel, making these suites'
			// `want` (below) genuine cross-file pins against bazel.yml's own
			// check_shard_coverage.py arguments, not independently
			// hard-coded literals that could drift from BUILD.bazel
			// unnoticed (S1).
			{"test-embedded-cmd", "Test", "//cmd/bd:bd_embedded_test", ".github/scripts/embedded-test-shard.sh", -1},
			{"test-embedded-storage", "Test", "//internal/storage/embeddeddolt:embeddeddolt_embedded_test", ".github/scripts/embedded-storage-test-shard.sh", -1},
		}, []string{"//internal/storage/embeddeddolt:embeddeddolt_conformance_core_test", "//internal/storage/embeddeddolt:embeddeddolt_conformance_audit_test"}},
		{bazelProxiedJobName, "doltserver-proxied", []suite{
			// bazel-proxied runs its own duration-balanced manifest block
			// (scripts/ci/proxied_test_durations.json), not PR Risk's frozen
			// 15-shard bd-init-cost-proxy block: it is not a drop-in
			// retirement of test-proxied-cmd's shard count, just its tests.
			// bazelProxiedShardCount reads the real count from
			// cmd/bd/BUILD.bazel, making this suite's `want` (below) a
			// genuine cross-file pin against bazel.yml's own
			// check_shard_coverage.py argument, not an independently
			// hard-coded literal that could drift from BUILD.bazel unnoticed
			// (S1).
			{"test-proxied-cmd", "Test proxied-server cmd shard", "//cmd/bd:bd_proxied_test", ".github/scripts/proxied-test-shard.sh", -1},
		}, nil},
		{bazelServerJobName, "doltserver-integration", []suite{
			{"test-server-storage-full", "Test", "//internal/storage/dolt:dolt_server_full_test", ".github/scripts/server-storage-test-shard.sh", 0},
		}, []string{"//internal/storage/dolt:dolt_server_conformance_test"}},
	} {
		job := readCIWorkflow(t, bazelWorkflowName).job(t, c.lane)
		step := job.step(t, "Every listed test ran in its shard")
		want := []string{"python3 tools/bazel/check_shard_coverage.py", `--bep "$RUNNER_TEMP/bazel-bep.json"`}
		for _, s := range c.suites {
			shards := s.shardCount
			switch {
			case shards < 0:
				fn, ok := liveShardCount[s.label]
				if !ok {
					t.Fatalf("%s: shardCount<0 (read live from BUILD.bazel) is not configured for %s", s.job, s.label)
				}
				shards = fn(t)
			case shards == 0:
				shards = len(risk.job(t, s.job).Strategy.Matrix.Shard)
			}
			if !strings.Contains(risk.job(t, s.job).step(t, s.step).Run, s.script) {
				t.Errorf("pr-risk.yml %s no longer runs %s; update this suite", s.job, s.script)
			}
			want = append(want, fmt.Sprintf("--suite %s %s %d", s.label, s.script, shards))
		}
		for _, w := range c.whole {
			want = append(want, "--whole "+w)
		}
		if got := strings.Join(strings.Fields(step.Run), " "); got != strings.Join(want, " ") {
			t.Errorf("%s coverage step runs %q, want %q", c.lane, got, strings.Join(want, " "))
		}
		if step.If != "${{ always() && steps.test.outcome != 'skipped' }}" || step.ContinueOnError != nil {
			t.Errorf("%s coverage step: if %q, continue-on-error %v; want always() after the test step and no continue-on-error", c.lane, step.If, step.ContinueOnError)
		}
		if !(job.stepIndex(t, "bazel test //... --config="+c.config) < job.stepIndex(t, step.Name) &&
			job.stepIndex(t, step.Name) < job.stepIndex(t, "Record job result")) {
			t.Errorf("%s coverage step must run after the test step and before the result recorder", c.lane)
		}
	}
	// The legacy conformance partitions the --whole targets stand for.
	for _, partition := range []string{"core", "audit"} {
		if risk.job(t, "test-embedded-conformance").step(t, "Test "+partition+" conformance").Run == "" {
			t.Errorf("pr-risk.yml test-embedded-conformance has no %s partition; update this check", partition)
		}
	}
	if !strings.Contains(risk.job(t, "test-server-storage").step(t, "Test").Run, "-test.run '^TestConformance$'") {
		t.Errorf("pr-risk.yml test-server-storage no longer runs ^TestConformance$; update this check")
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
// output (every retired lane's: the embedded, proxied and server suites). Every name they list, for every shard, must be a test go test
// runs (declared `func Name(t *testing.T)` in the package's _test.go files),
// or one check_shard_coverage.py drops as NOT_TESTS; and each NOT_TESTS name
// must really not be a test (TestMain takes *testing.M). Otherwise the
// checker reports a listed test that "did not run" on every real run.
func TestShardScriptsListOnlyRealTests(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold neither the shard scripts' sources nor tools/bazel")
	}
	// The CI shard scripts use bash 4 associative arrays (Linux runners only).
	requireAutofixBash(t)
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
		// D2 step 2: the proxied and server lanes' suites.
		{"test-proxied-cmd", ".github/scripts/proxied-test-shard.sh", "cmd/bd"},
		{"test-server-storage-full", ".github/scripts/server-storage-test-shard.sh", "internal/storage/dolt"},
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

		// B1: validate every total this script's committed manifest holds a
		// block for, plus this job's own PR Risk matrix size and (for
		// test-proxied-cmd) the Bazel lane's own shard_count — not just
		// whichever total happens to equal this PR Risk job's matrix. Before
		// F2 those always coincided; now the Bazel-only bazel-proxied lane
		// reads a manifest block (30) that no PR-Risk-matrix-only check ever
		// exercises, so a fork PR (which never runs bazel-proxied) could
		// corrupt that block and still merge green. Looping over every
		// distinct total in the manifest closes that gap for this script and
		// any other script that later grows a second block the same way.
		mm := shardManifestDefault.FindStringSubmatch(readPolicyFile(t, root, c.script))
		if mm == nil {
			t.Fatalf("%s has no ${BEADS_TEST_SHARD_MANIFEST:-...} default manifest", c.script)
		}
		totalsSet := map[int]bool{len(risk.job(t, c.job).Strategy.Matrix.Shard): true}
		for _, line := range strings.Split(readPolicyFile(t, root, mm[1]), "\n") {
			line, _, _ = strings.Cut(line, "#")
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if n, err := strconv.Atoi(fields[0]); err == nil {
				totalsSet[n] = true
			}
		}
		switch c.script {
		case ".github/scripts/proxied-test-shard.sh":
			totalsSet[bazelProxiedShardCount(t)] = true
		case ".github/scripts/embedded-test-shard.sh":
			// F1: the Bazel-only bazel-embedded lane reads a 50-shard cmd
			// block that no PR-Risk-matrix-only (20-shard) check exercises;
			// without this, "BUILD.bazel's shard_count and bazel.yml's
			// check_shard_coverage.py arg both drift to a new total with no
			// manifest block" passes every policy test (see review S3) --
			// the lane then silently goes 100% hash fallback and loses its
			// duration balancing.
			totalsSet[bazelEmbeddedCmdShardCount(t)] = true
		case ".github/scripts/embedded-storage-test-shard.sh":
			// F1: mirrors the cmd case above for the 15-shard storage block.
			totalsSet[bazelEmbeddedStorageShardCount(t)] = true
		}
		totals := make([]int, 0, len(totalsSet))
		for n := range totalsSet {
			totals = append(totals, n)
		}
		sort.Ints(totals)

		for _, shards := range totals {
			// The scripts' hash fallback forks per test (seconds per shard
			// for the server suite): list the shards concurrently.
			outs, errs := make([][]byte, shards+1), make([]error, shards+1)
			var wg sync.WaitGroup
			for k := 1; k <= shards; k++ {
				wg.Add(1)
				go func(k int) {
					defer wg.Done()
					cmd := exec.Command("bash", c.script, strconv.Itoa(k), strconv.Itoa(shards))
					cmd.Dir = root
					cmd.Env = append(os.Environ(), "BEADS_TEST_SHARD_LIST_ONLY=1")
					outs[k], errs[k] = cmd.Output()
				}(k)
			}
			wg.Wait()
			listed, manifestSum := 0, 0
			for k := 1; k <= shards; k++ {
				if errs[k] != nil {
					// The script itself exits 1 on a duplicate manifest entry
					// or a manifest entry that names no discovered test
					// (rename/typo/stale-after-delete), for the requested
					// total only: this is what catches a corrupted block
					// that a PR-Risk-matrix-only check at a different total
					// would never see.
					t.Fatalf("%s %d %d: %v\n%s", c.script, k, shards, errs[k], outs[k])
				}
				for _, line := range strings.Split(string(outs[k]), "\n") {
					if mc := regexp.MustCompile(`^  manifest: (\d+), fallback: \d+$`).FindStringSubmatch(line); mc != nil {
						n, _ := strconv.Atoi(mc[1])
						manifestSum += n
						continue
					}
					name, ok := strings.CutPrefix(line, "  ")
					if !ok || !strings.HasPrefix(name, "Test") || strings.ContainsAny(name, " :") {
						continue
					}
					listed++
					isTest := declared[name]
					switch {
					case notTests[name] && isTest:
						t.Errorf("%s shard %d/%d lists %s, which check_shard_coverage.py drops, but it is a real test", c.script, k, shards, name)
					case !notTests[name] && !isTest:
						t.Errorf("%s shard %d/%d lists %s, which is not a `func %s(t *testing.T)` test in %s: check_shard_coverage.py would report it missing on every run (add it to NOT_TESTS only if go test never runs it)",
							c.script, k, shards, name, name, c.pkg)
					}
				}
			}
			if listed < 50 {
				t.Errorf("%s at %d shards listed only %d tests; did the list-only output format change?", c.script, shards, listed)
			}
			// S1: a total that is supposed to have a committed manifest
			// block (every total this loop considers does: it is either a
			// live job's matrix size or a total this script's own manifest
			// already names) must not have silently gone 100% hash fallback,
			// which is what "the whole block was deleted" looks like from
			// here: check_shard_coverage.py would still pass (it rebuilds
			// its expectation from this same script), so nothing else
			// catches it.
			if manifestSum == 0 {
				t.Errorf("%s at %d shards: manifest entries for this total sum to 0 across all shards (100%% hash fallback); its committed block in %s may have been deleted", c.script, shards, mm[1])
			}
		}
	}
	for name := range notTests {
		if name != "TestMain" {
			t.Errorf("NOT_TESTS has %s; only TestMain is never a test", name)
		}
	}
}

// S3: the Bazel-only 30-shard block is not frozen like the legacy 15-shard
// block (TestShardScriptsListOnlyRealTests's B1 fix catches outright
// corruption, but not a committed block that has drifted from the currently
// discovered TestProxiedServer*/TestServerMode* test set, e.g. a test added,
// renamed, or removed without anyone running --write). gen_proxied_shard_
// manifest.py --check verifies only that the committed block names every
// discovered test exactly once -- not that its shard *assignments* match a
// fresh LPT pack -- and fails with the exact command to fix it when a name
// is missing, stale, or duplicated. It deliberately does NOT fail merely
// because proxied_test_durations.json's weights changed and the existing
// packing is now suboptimal: two PRs each adding one proxied test would
// otherwise force a full repack and conflict on unrelated shard lines (see
// --repack below for the explicit opt-in to that). Run --check here so a
// block with missing/stale/duplicate names fails go test ./scripts/...
// (and so scripts-go-checks, which runs on fork PRs) instead of only
// surfacing as a test silently never running in any shard. The legacy
// 15-shard block is deliberately excluded: its header documents that it is
// frozen and must not be regenerated (see
// .github/scripts/proxied-cmd-test-shards.txt and engdocs/TESTING.md), so a
// --check against it would always fail by design.
func TestProxiedShardManifestGeneratorNotStale(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold neither the generator's sources nor cmd/bd")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	root := sourceRepoRoot(t)
	cmd := exec.Command(python, "scripts/ci/gen_proxied_shard_manifest.py", "30", "--weights=duration", "--check")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("gen_proxied_shard_manifest.py 30 --weights=duration --check: %v\n%s", err, out)
	}
}

// Review G3: since D2 the retired tiers' Bazel lanes (embedded, proxied,
// server-storage) are those tiers' only pre-merge run on same-repo PRs, so
// nothing that reaches them may narrow them (select fewer tests, or turn
// them into skips) without a reviewed edit of this test.
// TestBazelEmbeddedJobMirrorsEmbeddedTier and TestBazelRetiredLanesArePinned
// pin the command lines and each lane config's lines; this covers
// everything else that applies to the lanes: every other .bazelrc line of a
// config a lane uses (the unconfigured ones, remote-exec and fork-cache,
// which setup-bazel's rc enables, and any config those pull in), rc files
// that would be try-imported, the tools/bazel scripts every test runs under
// or through, the whole setup-bazel action, and the lanes' targets' args and
// env. At run time, check_shard_coverage.py also fails a shard of only
// skips.
func TestBazelRetiredLanesCannotBeNarrowed(t *testing.T) {
	root := sourceRepoRoot(t)
	rcNarrow := regexp.MustCompile(`test_filter|test_arg|-test\.|_filters\b|test_env=(BEADS_TEST|GO_TEST|TESTBRIDGE)|--config=|cache_test_results|eviction_retries|run_under|flaky|runs_per_test|test_sharding_strategy|build_tests_only`)
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
		t.Errorf(".bazelrc imports %v, want exactly %v (an import can carry any flag into the lanes)", imports, wantImports)
	}
	// The configs the lanes use: their own (pinned), the configs
	// setup-bazel's generated rc enables for every command (remote-exec,
	// and fork-cache in mode cache, where the remote-only lanes are skipped
	// but the rc still applies), the unconfigured lines, and anything they
	// reference (which the check below then forbids anyway).
	laneConfigs := map[string][]string{"embedded": bazelEmbeddedRCLines}
	for config, want := range bazelDoltServerRCLines {
		laneConfigs[config] = want
	}
	// D2 step 3's lanes (PR-core, dolt-server, pure-Go/js-wasm, sole-run).
	for config, want := range bazelPRLaneRCLines {
		laneConfigs[config] = want
	}
	rcEnabled := map[string]bool{"remote-exec": true, "fork-cache": true}
	inUse := map[string]bool{"": true}
	for c := range laneConfigs {
		inUse[c] = true
	}
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
	for _, want := range laneConfigs {
		for _, l := range want {
			pinned[l] = true
		}
	}
	for _, l := range lines {
		if !inUse[l.config] || pinned[l.text] {
			continue
		}
		if _, lane := laneConfigs[l.config]; lane {
			t.Errorf(".bazelrc %q: not one of the pinned --config=%s lines", l.text, l.config)
			continue
		}
		if l.text == runUnder {
			sawRunUnder = true
			continue
		}
		if rcNarrow.MatchString(l.text) {
			t.Errorf(".bazelrc %q applies to the retired tiers' lanes (config %q) and selects, narrows or re-runs tests", l.text, l.config)
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

	// The scripts every lane's test runs under or through, and the whole
	// setup-bazel action (its generated rc applies to every command).
	if os.Getenv("TEST_SRCDIR") == "" {
		scriptNarrow := regexp.MustCompile(`-test\.(short|run|skip|list|bench)|BEADS_TEST_SKIP|BEADS_TEST_EMBEDDED_DOLT|BEADS_TEST_PROXIED_SERVER|BEADS_TEST_ENV_RUN_DOLT|BEADS_TEST_REQUIRE_DOLT_CONTAINER|BEADS_TEST_DOLT_SERVER\b|TESTBRIDGE_TEST_ONLY|test_filter|test_arg|_filters\b|cache_test_results|eviction_retries|flaky|runs_per_test|test_sharding_strategy`)
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
					t.Errorf("%s:%d %q can select, skip or re-run the retired tiers' lanes' tests", rel, i+1, code)
				}
				for _, m := range regexp.MustCompile(`--config=([A-Za-z0-9_-]+)`).FindAllStringSubmatch(code, -1) {
					if !rcEnabled[m[1]] {
						t.Errorf("%s:%d enables --config=%s for every command", rel, i+1, m[1])
					}
				}
			}
		}
	}

	// Review F2 (step 2): the shard scripts the sharded targets run (the
	// legacy jobs run the same scripts, so they are not changed here): the
	// command that runs the selected tests is pinned exactly, and nothing
	// else in them may select, skip, export a tier switch or run tests.
	if os.Getenv("TEST_SRCDIR") == "" {
		for _, c := range bazelShardScripts {
			for _, e := range shardScriptNarrowing(c, readPolicyFile(t, root, c.script)) {
				t.Error(e)
			}
		}
	}

	// The lanes' targets (tagged embedded, dolt-server-proxied or
	// dolt-server-integration): exactly these, with exactly these args and
	// env (the legacy jobs' flags; the shard scripts add the rest).
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
		"//cmd/bd:bd_proxied_test": {
			[]string{"$(rootpath //:.github/scripts/proxied-test-shard.sh)", "BEADS_TEST_CMD_BINARY", "$(rootpath :bd_test)"},
			map[string]string{
				"BEADS_TEST_BD_BINARY": "$(rlocationpath :bd_for_tests)", "BEADS_TEST_DOLT_SERVER": "local", "BEADS_TEST_GIT_IDENTITY": "1",
				"BEADS_TEST_GOFMT": "$(rlocationpath @go_sdk//:bin/gofmt)", "BEADS_TEST_PROXIED_SERVER": "1",
				"BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1", "BEADS_TEST_REQUIRE_SOCAT": "1", "GOMAXPROCS": "4",
			},
		},
		"//internal/storage/dolt:dolt_server_conformance_test": {
			[]string{"$(rootpath :dolt_race_off)", "-test.v", "-test.count=1", "-test.timeout=15m", "-test.run=^TestConformance$$"},
			map[string]string{"BEADS_TEST_DOLT_SERVER": "local", "BEADS_TEST_GIT_IDENTITY": "1", "BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1", "GOMAXPROCS": "4"},
		},
		"//internal/storage/dolt:dolt_server_full_test": {
			[]string{"$(rootpath //:.github/scripts/server-storage-test-shard.sh)", "BEADS_TEST_SERVER_TEST_BINARY", "$(rootpath :dolt_race_off)"},
			map[string]string{
				"BEADS_TEST_DOLT_SERVER": "local", "BEADS_TEST_ENV_RUN_DOLT": "1", "BEADS_TEST_GIT_IDENTITY": "1",
				"BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1", "BEADS_TEST_SUBPROCESS_BINARY": "$(rlocationpath :dolt_race_off)", "GOMAXPROCS": "4",
			},
		},
	}
	laneTags := regexp.MustCompile(`"(embedded|dolt-server-proxied|dolt-server-integration)"`)
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
			if tags == nil || !laneTags.MatchString(tags[1]) {
				continue
			}
			name := nameRe.FindStringSubmatch(rule)
			if name == nil {
				t.Errorf("%s: a retired lane's rule without a literal name:\n%s", pkg, rule)
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
		t.Errorf("the retired lanes' targets' args/env changed:\ngot  %v\nwant %v", got, want)
	}

	// D2 step 3: the PR-core, dolt-server and pure lanes run (nearly) every
	// other test target, too many to pin one by one: no rule outside the
	// pinned ones above, and no .bzl macro, may select, skip or switch off
	// tests through its args, env or anything else.
	ruleNarrow := regexp.MustCompile(`-test\.(short|run|skip|list|bench)|BEADS_TEST_SKIP|BEADS_TEST_EMBEDDED_DOLT|TESTBRIDGE_TEST_ONLY|test_filter|flaky\s*=\s*(True|1|[A-Za-z_])`)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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
		isBzl := strings.HasSuffix(d.Name(), ".bzl")
		if d.Type()&os.ModeSymlink != 0 || (d.Name() != "BUILD.bazel" && d.Name() != "BUILD" && !isBzl) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		pkg := filepath.ToSlash(filepath.Dir(rel))
		var units []string
		if isBzl {
			units = []string{string(data)}
		} else {
			units = bazelTopRules(string(data))
		}
		for _, unit := range units {
			if !isBzl {
				if name := nameRe.FindStringSubmatch(unit); name != nil {
					if _, pinned := want["//"+pkg+":"+name[1]]; pinned {
						continue
					}
				}
			}
			for _, line := range strings.Split(unit, "\n") {
				code, _, _ := strings.Cut(line, "#")
				if ruleNarrow.MatchString(code) && !strings.Contains(code, "flaky = False") {
					t.Errorf("%s: %q can select, skip or retry tests of the gated lanes", rel, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Review G4: each retired tier's lane asks Bazel which test targets are
// flaky (a grep of BUILD files misses `flaky = _VAR` or a macro default)
// before it runs the tier, and fails on any.
func TestBazelRetiredLanesQueryFlakyTargets(t *testing.T) {
	const want = `flaky="$(bazel query 'attr(flaky, 1, tests(//...))')"
if [ -n "$flaky" ]; then
  echo "::error::flaky = True on ${flaky//$'\n'/ }: the gated lanes run each test once"
  exit 1
fi`
	for lane, config := range bazelRetiredLaneConfigs {
		job := readCIWorkflow(t, bazelWorkflowName).job(t, lane)
		step := job.step(t, "No test target is marked flaky")
		if strings.TrimSpace(step.Run) != want || step.If != "" || step.ContinueOnError != nil {
			t.Errorf("%s flaky query step: if %q, continue-on-error %v, run:\n%s\nwant run:\n%s", lane, step.If, step.ContinueOnError, step.Run, want)
		}
		if !(job.stepIndex(t, "Set up Bazel") < job.stepIndex(t, step.Name) && job.stepIndex(t, step.Name) < job.stepIndex(t, "bazel test //... --config="+config)) {
			t.Errorf("%s flaky query step must run after setup-bazel and before the tier", lane)
		}
	}
}

// The retired tiers' Bazel lanes and the config each runs.
var bazelRetiredLaneConfigs = map[string]string{
	bazelEmbedJobName:   "embedded",
	bazelProxiedJobName: "doltserver-proxied",
	bazelServerJobName:  "doltserver-integration",
}

// bazelTierTestRun: a retired lane's test step, exactly (review F4).
func bazelTierTestRun(config string) string {
	return strings.ReplaceAll(bazelEmbeddedTestRun, "--config=embedded", "--config="+config)
}

// .bazelrc's --config=doltserver-proxied and --config=doltserver-integration,
// exactly and in order (D2 step 2, as review F4 for embedded): no
// --test_filter, no -test.short/-test.run/-test.skip, no retries (results
// are cached like every lane's; nightly's --config=fresh re-executes). Their targets' own args (the conformance target's -test.run, the
// legacy job's) are pinned by TestBazelRetiredLanesCannotBeNarrowed.
var bazelDoltServerRCLines = map[string][]string{
	"doltserver-proxied": {
		"test:doltserver-proxied --@rules_go//go/config:race",
		"test:doltserver-proxied --test_tag_filters=dolt-server-proxied",
		"test:doltserver-proxied --build_tests_only",
		"test:doltserver-proxied --keep_going",
		"test:doltserver-proxied --test_summary=terse",
		"test:doltserver-proxied --test_timeout=-1,-1,-1,1200",
		"test:doltserver-proxied --test_env=GO_TEST_WRAP_TESTV=1",
		"test:doltserver-proxied --test_arg=-test.parallel=4",
		"test:doltserver-proxied --local_test_jobs=4",
		"test:doltserver-proxied --remote_download_regex=.*/test\\.(log|xml)$",
		"test:doltserver-proxied --experimental_remote_cache_eviction_retries=0",
	},
	"doltserver-integration": {
		"build:doltserver-integration --@rules_go//go/config:tags=gms_pure_go,integration",
		"test:doltserver-integration --@rules_go//go/config:race",
		"test:doltserver-integration --test_tag_filters=dolt-server-integration",
		"test:doltserver-integration --build_tests_only",
		"test:doltserver-integration --keep_going",
		"test:doltserver-integration --test_summary=terse",
		"test:doltserver-integration --test_timeout=-1,-1,-1,1200",
		"test:doltserver-integration --test_env=GO_TEST_WRAP_TESTV=1",
		"test:doltserver-integration --test_arg=-test.parallel=4",
		"test:doltserver-integration --local_test_jobs=4",
		"test:doltserver-integration --remote_download_regex=.*/test\\.(log|xml)$",
		"test:doltserver-integration --experimental_remote_cache_eviction_retries=0",
	},
}

// D2 step 2, as review F2/F4 for embedded: the proxied and server lanes run
// exactly `bazel test //... --config=<config>` (plus nightly's BAZEL_FRESH)
// with a BEP and nothing else, their configs are exactly the pinned lines,
// and only test:docker and test:fresh set result caching.
func TestBazelRetiredLanesArePinned(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	for lane, config := range bazelRetiredLaneConfigs {
		test := workflow.job(t, lane).step(t, "bazel test //... --config="+config)
		if strings.TrimSpace(test.Run) != bazelTierTestRun(config) {
			t.Errorf("%s test step run changed; want exactly:\n%s\ngot:\n%s", lane, bazelTierTestRun(config), test.Run)
		}
	}
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	for config, want := range bazelDoltServerRCLines {
		var got []string
		for _, line := range strings.Split(rc, "\n") {
			line = strings.TrimSpace(line)
			head, _, _ := strings.Cut(line, " ")
			if strings.HasSuffix(head, ":"+config) {
				got = append(got, line)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf(".bazelrc --config=%s lines changed; want exactly:\n%s\ngot:\n%s", config, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
		// Results are cached like every lane's; only nightly's
		// --config=fresh re-executes them (ci_merge_queue_test.go).
	}
	// Review F1 (step 2): no whole-invocation retry after a remote cache
	// eviction in any retired lane (it would re-run, and could turn green,
	// a test that failed in the first attempt), and nothing else in
	// .bazelrc sets the retry count (a later value would win).
	evictionAllowed := map[string]bool{bazelSoleRunEvictionLine: true}
	for _, config := range bazelRetiredLaneConfigs {
		want := "test:" + config + " --experimental_remote_cache_eviction_retries=0"
		lines := bazelEmbeddedRCLines
		if config != "embedded" {
			lines = bazelDoltServerRCLines[config]
		}
		if !contains(lines, want) {
			t.Errorf("pinned --config=%s lacks %q", config, want)
		}
		evictionAllowed[want] = true
	}
	for _, line := range strings.Split(rc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && strings.Contains(line, "remote_cache_eviction_retries") && !evictionAllowed[line] {
			t.Errorf(".bazelrc %q: only the retired tiers' configs set the eviction retry count (to 0)", line)
		}
	}
	// A later --cache_test_results (any config the lanes use) would win.
	// Only the docker lane and nightly's --config=fresh (appended only when
	// the caller asks, ci_merge_queue_test.go) turn result caching off.
	allowed := map[string]bool{"test:docker --nocache_test_results": true, bazelFreshRCLine: true}
	for _, line := range strings.Split(rc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && strings.Contains(line, "cache_test_results") && !allowed[line] {
			t.Errorf(".bazelrc %q: only test:docker and test:fresh set test result caching", line)
		}
	}
}

// bazelShardScript: a manifest shard script a retired lane's sharded target
// runs, and how it runs the selected tests.
type bazelShardScript struct {
	script, binVar, timeout, goTags string
	race                            bool
	pkg, cd                         string // cd: the directory the prebuilt binary runs in ("" = none)
}

var bazelShardScripts = []bazelShardScript{
	{".github/scripts/embedded-test-shard.sh", "CMD_BINARY", "20m", "gms_pure_go", true, "./cmd/bd/", ""},
	{".github/scripts/embedded-storage-test-shard.sh", "STORAGE_BINARY", "20m", "gms_pure_go", true, "./internal/storage/embeddeddolt/", ""},
	{".github/scripts/proxied-test-shard.sh", "CMD_BINARY", "15m", "gms_pure_go", false, "./cmd/bd/", ""},
	{".github/scripts/server-storage-test-shard.sh", "STORAGE_BINARY", "15m", "integration,gms_pure_go", false, "./internal/storage/dolt/", "internal/storage/dolt"},
}

// tail: the script's code lines (comments and blank lines dropped) from the
// prebuilt-binary branch to the end of the file, exactly.
func (c bazelShardScript) tail() []string {
	race := ""
	if c.race {
		race = "-race "
	}
	out := []string{`if [ -x "$` + c.binVar + `" ]; then`}
	if c.cd != "" {
		out = append(out, "cd "+c.cd)
	}
	return append(out,
		`exec "$`+c.binVar+`" -test.v -test.count=1 -test.timeout=`+c.timeout+` \`,
		`-test.run "$RUN_REGEX" \`,
		`"$@"`,
		"else",
		`echo "Warning: pre-built test binary not found at $`+c.binVar+`, falling back to go test"`,
		`exec go test -tags=`+c.goTags+` -v `+race+`-count=1 -timeout `+c.timeout+` \`,
		`-run "$RUN_REGEX" \`,
		`"$@" \`,
		c.pkg,
		"fi",
	)
}

const bazelShardRunRegex = `RUN_REGEX="^($(IFS='|'; echo "${SHARD_TESTS[*]}"))$"`

// shardScriptNarrowing: why src does not run exactly c.tail() as its last code
// lines, builds RUN_REGEX from the selection exactly once, and outside the
// tail neither runs tests nor selects, skips or switches them.
func shardScriptNarrowing(c bazelShardScript, src string) []string {
	var errs []string
	var code []string
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			code = append(code, line)
		}
	}
	want := c.tail()
	if len(code) < len(want) || !reflect.DeepEqual(code[len(code)-len(want):], want) {
		got := code
		if len(code) > len(want) {
			got = code[len(code)-len(want):]
		}
		return append(errs, fmt.Sprintf("%s: the command that runs the shard changed; want exactly:\n%s\ngot:\n%s", c.script, strings.Join(want, "\n"), strings.Join(got, "\n")))
	}
	narrow := regexp.MustCompile(`-test\.|(^|\s)-(short|run|skip|count|list|bench)\b|\bexec\b|\bgo (test|run)\b|"\$@"|\$\*|\beval\b|BEADS_TEST_(SKIP|EMBEDDED_DOLT|PROXIED_SERVER|ENV_RUN_DOLT|REQUIRE_DOLT_CONTAINER|DOLT_SERVER\b)|GOFLAGS|TESTBRIDGE|RUN_REGEX`)
	runRegex := 0
	for _, line := range code[:len(code)-len(want)] {
		if line == bazelShardRunRegex {
			runRegex++
			continue
		}
		if narrow.MatchString(line) {
			errs = append(errs, fmt.Sprintf("%s: %q can run, select, skip or switch the shard's tests outside its pinned command", c.script, line))
		}
	}
	if runRegex != 1 {
		errs = append(errs, fmt.Sprintf("%s: want exactly one %s, got %d", c.script, bazelShardRunRegex, runRegex))
	}
	return errs
}

// The shard script check itself, on the real scripts and on narrowing edits
// of one (review F2 mutations m2 and m3).
func TestShardScriptNarrowingCheck(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold no .github/scripts")
	}
	root := sourceRepoRoot(t)
	scripts := map[string]bool{}
	for _, c := range bazelShardScripts {
		scripts[c.script] = true
	}
	// Every retired lane's sharded target runs one of these scripts.
	for _, c := range []string{"embedded-test-shard.sh", "embedded-storage-test-shard.sh", "proxied-test-shard.sh", "server-storage-test-shard.sh"} {
		if !scripts[".github/scripts/"+c] {
			t.Errorf("bazelShardScripts lacks %s", c)
		}
	}
	c := bazelShardScripts[3]
	src := readPolicyFile(t, root, c.script)
	for name, edit := range map[string][2]string{
		"exec -test.short":        {`-test.timeout=15m \`, `-test.timeout=15m -test.short \`},
		"export BEADS_TEST_SKIP":  {"set -euo pipefail\n", "set -euo pipefail\nexport BEADS_TEST_SKIP=dolt\n"},
		"GOFLAGS":                 {"set -euo pipefail\n", "set -euo pipefail\nexport GOFLAGS=-short\n"},
		"second exec":             {"set -euo pipefail\n", "set -euo pipefail\n[ -n \"${X:-}\" ] && exec true\n"},
		"narrowed regex":          {bazelShardRunRegex, `RUN_REGEX="^(${SHARD_TESTS[0]})$"`},
		"go test fallback -short": {`-v -count=1 -timeout 15m \`, `-v -short -count=1 -timeout 15m \`},
	} {
		if strings.Count(src, edit[0]) == 0 {
			t.Fatalf("%s: mutation %q no longer applies", c.script, name)
		}
		if len(shardScriptNarrowing(c, strings.Replace(src, edit[0], edit[1], 1))) == 0 {
			t.Errorf("%s: mutation %q passes the shard script check", c.script, name)
		}
	}
}
