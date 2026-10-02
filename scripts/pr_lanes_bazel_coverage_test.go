package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// D2 step 3: pr.yml's own legacy jobs whose Bazel lanes run in the same
// pr.yml run stand down on the PRs bazel-coverage covers (pr_lanes):
// build-artifacts, PR Core, the pure-Go/js-wasm check, domain+uow and
// contract corpus, for Bazel / test, pure-Go and js/wasm and dolt-server
// lane. ci-gate then requires those lanes to have run remotely and passed
// (BAZEL_PR_LANES_RETIRED, simulated with the other tiers in
// TestPRRiskDecisionMatchesBazelMode). Everything the retired jobs did
// besides those tests keeps running on every PR: the package gates take the
// Bazel-built bd, the Dolt server fingerprint has its own job, and
// scripts-go-checks runs `go test ./scripts/...`, go test's vet checks and
// the Go tests the Bazel lane does not run or skips (equivalence allowlist).

const (
	// The legacy jobs' if: not covered.
	prLaneLegacyIf = "needs." + prRiskCoverageJobName + ".outputs.pr_lanes != 'true'"
	// The package gates: they run whenever detection and the decision did,
	// on build-artifacts' bd, or on Bazel's where build-artifacts stood down.
	prPackageGateIf = "${{ !cancelled() && needs.detect-package-gates.result == 'success' && needs." + prRiskCoverageJobName +
		".result == 'success' && (needs." + prRiskCoverageJobName + ".outputs.pr_lanes == 'true' || needs.build-artifacts.result == 'success') }}"
	prPackageGateArtifact = "${{ needs." + prRiskCoverageJobName + ".outputs.pr_lanes == 'true' && 'bazel-ci-build-artifacts' || 'ci-build-artifacts' }}"
	prFingerprintJob      = "test-dolt-server-fingerprint"
	prFingerprintID       = "TEST_DOLT_SERVER_FINGERPRINT"
	prAllowlistedStep     = "Run the Go tests the Bazel lane skips"
	prScriptsChecksJob    = "scripts-go-checks"
	prPackageBDCheck      = "Check the Bazel-built bd exists"
	prScriptsChecksID     = "SCRIPTS_GO_CHECKS"
	// bazel.yml's lanes for the step add --config=sole-run in mode remote.
	bazelSoleRunEnv          = "${{ needs.rbe.outputs.mode == 'remote' && '--config=sole-run' || '' }}"
	bazelSoleRunArg          = `${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"}`
	bazelSoleRunNoCacheLine  = "test:sole-run --nocache_test_results"
	bazelSoleRunEvictionLine = "test:sole-run --experimental_remote_cache_eviction_retries=0"
)

// The package gates (consumers of build-artifacts' ci-build-artifacts that
// keep running where it stands down).
var prPackageGates = map[string]string{"package-mcp": "PACKAGE_MCP", "package-npm": "PACKAGE_NPM"}

// Each retired job's needs, exactly.
var prLaneLegacyNeeds = map[string][]string{
	"build-artifacts":            {prRiskCoverageJobName},
	"check-cmd-bd-puregeo-tests": {prRiskCoverageJobName},
	"contract-corpus":            {prRiskCoverageJobName},
	"pr-core-wrapper":            {"build-artifacts", prRiskCoverageJobName},
	"test-domain-uow":            {"build-artifacts", prRiskCoverageJobName},
}

// .bazelrc's lines for the configs step 3's lanes run, exactly: the
// PR-core selection (test:ci), the dolt-server lane, the pure-Go and js/wasm
// lane, and sole-run (their only-run hardening).
var bazelPRLaneRCLines = map[string][]string{
	"prcore": {
		"test:prcore --@rules_go//go/config:race",
		"test:prcore --test_arg=-test.short",
		"test:prcore --test_arg=-test.parallel=4",
		"test:prcore --test_arg=-test.skip=^TestEmbedded",
		"test:prcore --test_env=BEADS_TEST_SKIP=dolt",
		"test:prcore --test_env=BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION=1",
		"test:prcore --test_tag_filters=-requires-docker,-dolt-server,-dolt-server-proxied,-dolt-server-integration,-embedded,-manual,-integration-only",
	},
	"ci": {
		"test:ci --config=prcore",
		"test:ci --keep_going --test_summary=terse",
		"test:ci --test_env=GO_TEST_WRAP_TESTV=1",
		"test:ci --remote_download_regex=.*/test\\.xml$",
		"test:ci --remote_download_regex=.*/bin/cmd/bd/bd_for_tests/bd$",
	},
	"doltserver": {
		"test:doltserver --@rules_go//go/config:race",
		"test:doltserver --test_tag_filters=dolt-server",
		"test:doltserver --build_tests_only",
		"test:doltserver --keep_going",
		"test:doltserver --test_env=GO_TEST_WRAP_TESTV=1",
		"test:doltserver --test_arg=-test.parallel=4",
		"test:doltserver --local_test_jobs=4",
		"test:doltserver --remote_download_regex=.*/test\\.(log|xml)$",
	},
	"pure": {
		"build:pure --@rules_go//go/config:pure",
		"test:pure --test_arg=-test.short",
		"test:pure --test_arg=-test.parallel=4",
		"test:pure --test_env=GO_TEST_WRAP_TESTV=1",
		"test:pure --remote_download_regex=.*/test\\.xml$",
	},
	"js-wasm": {
		"build:js-wasm --platforms=@rules_go//go/toolchain:js_wasm",
		"build:js-wasm --use_target_platform_for_tests",
		"build:js-wasm --remote_download_outputs=toplevel",
	},
	"sole-run": {bazelSoleRunNoCacheLine, bazelSoleRunEvictionLine},
}

// The steps of step 3's lanes that run Bazel, exactly (as review F4 of
// step 1 for the embedded lane): nothing may be appended that selects,
// skips or re-runs tests.
var bazelPRLaneSteps = map[string]map[string]string{
	bazelJobName: {
		"bazel test //... --config=ci": `set -o pipefail
start=$(date +%s)
rc=0
bazel test //... --config=ci ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} \
  --profile="$RUNNER_TEMP/bazel-profile.json" \
  --build_event_json_file="$RUNNER_TEMP/bazel-bep.json" \
  2>&1 | tee "$RUNNER_TEMP/bazel-test.log" || rc=$?
echo "bazel test: exit $rc, $(( $(date +%s) - start ))s wall" | tee -a "$GITHUB_STEP_SUMMARY"
echo >> "$GITHUB_STEP_SUMMARY"
exit "$rc"`,
	},
	bazelPureJobName: {
		"Start every pure-Go artifact (gozstd contamination check)": `set -euo pipefail
bazel run --config=pure //cmd/bd:bd -- version
bazel test --config=pure ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} \
  //internal/storage/embeddeddolt:embeddeddolt_test \
  //internal/tracker:tracker_test \
  --test_sharding_strategy=disabled \
  '--test_arg=-test.run=^$' \
  --test_env=BEADS_TEST_SKIP=dolt`,
		"Run pure-Go cmd/bd test subset (--config=pure)": `set -euo pipefail
bazel test --config=pure ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} //cmd/bd:bd_test \
  --test_sharding_strategy=disabled \
  "--test_arg=-test.run=$PURE_CMD_BD_TESTS"
n="$(grep -c '<testcase ' bazel-testlogs/cmd/bd/bd_test/test.xml || true)"
echo "pure cmd/bd subset: $n test cases"
(( n > 0 ))`,
	},
	bazelDoltJobName: {
		"bazel test //... --config=doltserver": `set -o pipefail
start=$(date +%s)
rc=0
bazel test //... "--config=$BAZEL_DOLT_LANE" ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} 2>&1 | tee "$RUNNER_TEMP/bazel-test.log" || rc=$?
echo "bazel test --config=$BAZEL_DOLT_LANE: exit $rc, $(( $(date +%s) - start ))s wall" | tee -a "$GITHUB_STEP_SUMMARY"
exit "$rc"`,
	},
}

// prLanesTier: retiredTiers' pr.yml entry.
func prLanesTier(t *testing.T) retiredTier {
	t.Helper()
	for _, r := range retiredTiers {
		if r.workflow == "pr.yml" {
			return r
		}
	}
	t.Fatal("retiredTiers has no pr.yml tier")
	return retiredTier{}
}

// prLegacyJobResult: a non-Bazel pr.yml need's result in a gate scenario,
// as the jobs' if: produce it (sc.results overrides): a retired job skips
// where its tier is covered (GitHub's == is case-insensitive) or the
// decision failed; a package gate skips where the decision failed.
func prLegacyJobResult(job string, sc bazelGateScenario) string {
	if r, ok := sc.results[job]; ok {
		return r
	}
	decided := sc.coverage == "" || sc.coverage == "success"
	for _, r := range retiredTiers {
		if _, ok := r.jobs[job]; ok && r.workflow == "pr.yml" && (!decided || strings.EqualFold(sc.covered[r.output], "true")) {
			return "skipped"
		}
	}
	if _, ok := prPackageGates[job]; ok && !decided {
		return "skipped"
	}
	return "success"
}

// The retired jobs' wiring, the artifact consumers, and pr.yml's actual
// gate step over the covered and uncovered cases.
func TestPRLegacyLanesDeferToBazelLanes(t *testing.T) {
	requireHostTool(t, "bash")
	pr := readCIWorkflow(t, "pr.yml")
	tier := prLanesTier(t)
	gate := pr.job(t, "ci-gate")
	gateStep := gate.step(t, "Evaluate CI gate")
	required := strings.Fields(gateStep.Env["CI_GATE_REQUIRED"])

	var names []string
	for name := range tier.jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, sortedKeys(prLaneLegacyNeeds)) {
		t.Fatalf("pr.yml tier jobs %v, want %v", names, sortedKeys(prLaneLegacyNeeds))
	}
	for name, id := range tier.jobs {
		job := pr.job(t, name)
		if job.If != prLaneLegacyIf || !reflect.DeepEqual([]string(job.Needs), prLaneLegacyNeeds[name]) || job.ContinueOnError {
			t.Errorf("%s: if %q, needs %v, continue-on-error %v; want if %q, needs %v", name, job.If, job.Needs, job.ContinueOnError, prLaneLegacyIf, prLaneLegacyNeeds[name])
		}
		// Still required: it runs wherever the Bazel lanes do not cover it.
		if !contains(required, id) || !contains(gate.Needs, name) || gateStep.Env[id] != "${{ needs."+name+".result }}" {
			t.Errorf("ci-gate does not require %s's result as %s", name, id)
		}
	}
	// The lanes that replace them run in this workflow's bazel call.
	if !reflect.DeepEqual(tier.bazelLanes, []string{bazelJobName, bazelPureJobName, bazelDoltJobName}) {
		t.Errorf("pr.yml tier lanes = %v", tier.bazelLanes)
	}

	// Only the retired jobs, the package gates and the gate read the
	// decision; only they and the gate need build-artifacts.
	for name, job := range pr.Jobs {
		_, retired := tier.jobs[name]
		_, pkg := prPackageGates[name]
		if retired || pkg || name == "ci-gate" || name == prRiskCoverageJobName {
			continue
		}
		if strings.Contains(job.If, prRiskCoverageJobName) || contains(job.Needs, prRiskCoverageJobName) {
			t.Errorf("%s depends on %s; only the retired jobs and the package gates may", name, prRiskCoverageJobName)
		}
		if contains(job.Needs, "build-artifacts") {
			t.Errorf("%s needs build-artifacts, which stands down on covered PRs; take the artifact like the package gates do", name)
		}
	}

	// Artifact consumers: the retired jobs read build-artifacts' artifact
	// (they only run where it does); the package gates read Bazel's where
	// it stood down. Nothing downloads by pattern or without a name.
	for name, job := range pr.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/download-artifact@") {
				continue
			}
			art := step.With["name"]
			if art == "" || step.With["pattern"] != "" {
				t.Errorf("%s downloads an artifact by pattern or without a name: %v", name, step.With)
				continue
			}
			if !strings.Contains(art, "ci-build-artifacts") {
				continue
			}
			_, retired := tier.jobs[name]
			_, pkg := prPackageGates[name]
			switch {
			case retired && art == "ci-build-artifacts":
			case pkg && art == prPackageGateArtifact:
			default:
				t.Errorf("%s downloads %q; only the retired jobs (ci-build-artifacts) and the package gates (%s) may", name, art, prPackageGateArtifact)
			}
			if step.With["path"] != "ci-build-artifacts" {
				t.Errorf("%s downloads into %q, want ci-build-artifacts (the layout its steps read)", name, step.With["path"])
			}
		}
	}
	for name, id := range prPackageGates {
		job := pr.job(t, name)
		want := []string{"detect-package-gates", "build-artifacts", prRiskCoverageJobName, "bazel"}
		if job.If != prPackageGateIf || !reflect.DeepEqual([]string(job.Needs), want) {
			t.Errorf("%s: if %q, needs %v; want if %q, needs %v", name, job.If, job.Needs, prPackageGateIf, want)
		}
		if dl := job.step(t, "Download build artifacts"); dl.With["name"] != prPackageGateArtifact {
			t.Errorf("%s downloads %q, want %q", name, dl.With["name"], prPackageGateArtifact)
		}
		if v := job.step(t, "Verify build artifacts").Run; !strings.Contains(v, "sha256sum -c SHA256SUMS") || !strings.Contains(v, "chmod +x bd-linux-gms-pure") {
			t.Errorf("%s no longer verifies the artifact:\n%s", name, v)
		}
		if !contains(required, id) {
			t.Errorf("ci-gate does not require %s", id)
		}
		// Review L2: with the farm off (mode skip) on a covered PR, say why
		// there is no bd before the download fails on a missing artifact.
		check := job.step(t, prPackageBDCheck)
		if check.If != "steps.applicability.outputs.run == 'true' && needs."+prRiskCoverageJobName+".outputs.pr_lanes == 'true'" ||
			!reflect.DeepEqual(check.Env, map[string]string{"BAZEL_RBE_MODE": "${{ needs.bazel.outputs.rbe-mode }}"}) || check.ContinueOnError != nil ||
			job.stepIndex(t, prPackageBDCheck) > job.stepIndex(t, "Download build artifacts") {
			t.Errorf("%s step %q: if %q, env %v; want it before the download, on covered applicable runs, reading the call's mode", name, prPackageBDCheck, check.If, check.Env)
		}
		for mode, wantOK := range map[string]bool{"remote": true, "skip": false, "cache": false, "local": false, "": false} {
			cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", check.Run)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "BAZEL_RBE_MODE=" + mode}
			out, err := cmd.CombinedOutput()
			if (err == nil) != wantOK || (!wantOK && !(strings.Contains(string(out), "::error::") && strings.Contains(string(out), "BAZEL_PR_LANES_RETIRED"))) {
				t.Errorf("%s bd check in mode %q: ok %v, want %v\n%s", name, mode, err == nil, wantOK, out)
			}
		}
	}
	// Bazel's artifact: bazel-test publishes it (always, after its tests)
	// under the name pr.yml passes, with build-artifacts' layout
	// (TestBazelWorkflowPublishesBuildArtifacts).
	if pr.job(t, "bazel").With["build-artifact-name"] != "bazel-ci-build-artifacts" {
		t.Errorf("pr.yml's bazel call no longer names its artifact bazel-ci-build-artifacts")
	}
	bazelTest := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	pkgStep := bazelTest.step(t, "Package bd (ci-build-artifacts layout)")
	if pkgStep.Run != `./scripts/ci/package-bazel-bd.sh "$RUNNER_TEMP/bd-artifacts"` || pkgStep.ContinueOnError != nil {
		t.Errorf("bazel-test packages bd with %q (continue-on-error %v)", pkgStep.Run, pkgStep.ContinueOnError)
	}

	// The gate. Other tiers not covered, so only this step's rules apply.
	cov := func(v string) map[string]string {
		c := coveredAll("false")
		c[tier.output] = v
		return c
	}
	lanes := bazelPRCallLanes(t, pr.job(t, "bazel").With)
	type scenario struct {
		name    string
		sc      bazelGateScenario
		pass    bool
		mention string
	}
	var scenarios []scenario
	add := func(name string, sc bazelGateScenario, pass bool, mention string) {
		scenarios = append(scenarios, scenario{name, sc, pass, mention})
	}
	add("covered, remote, lanes passed, legacy skipped", prGateFor(t, lanes, "pull_request", "remote", cov("true")), true, "")
	add("not covered, remote, legacy ran", prGateFor(t, lanes, "pull_request", "remote", cov("false")), true, "")
	add("merge_group, not covered, remote", prGateFor(t, lanes, "merge_group", "remote", cov("false")), true, "")
	for _, mode := range []string{"local", "cache"} {
		add("not covered, mode "+mode+", legacy ran", prGateFor(t, lanes, "pull_request", mode, cov("false")), true, "")
	}
	for job, id := range tier.jobs {
		// Covered, but the job ran anyway and failed: red.
		sc := prGateFor(t, lanes, "pull_request", "remote", cov("true"))
		sc.results = map[string]string{job: "failure"}
		add("covered, "+job+" ran and failed", sc, false, id)
		// Not exactly covered: its skip is not excused.
		for _, c := range []string{"false", "", "TRUE ", "yes", "1", "True\n"} {
			sc := prGateFor(t, lanes, "pull_request", "remote", cov(c))
			sc.results = map[string]string{job: "skipped"}
			add(fmt.Sprintf("covered=%q, %s skipped", c, job), sc, false, id)
		}
	}
	for _, lane := range tier.bazelLanes {
		for _, res := range []string{"failure", "cancelled", "skipped", ""} {
			sc := prGateFor(t, lanes, "pull_request", "remote", cov("true"))
			sc.outputs[lane] = res
			add(fmt.Sprintf("covered, %s %q", lane, res), sc, false, tier.retiredID)
		}
	}
	for _, mode := range []string{"skip", "local", "cache"} {
		sc := prGateFor(t, lanes, "pull_request", mode, cov("true"))
		for _, lane := range tier.bazelLanes {
			sc.outputs[lane] = "success"
		}
		add("covered, mode "+mode+", lanes reported success", sc, false, tier.retiredID)
	}
	for _, c := range []string{"true", "false"} {
		for _, res := range []string{"skipped", "failure", "cancelled"} {
			for _, job := range []string{prFingerprintJob, "package-mcp", "package-npm", "pr-preflight-platforms", prScriptsChecksJob} {
				sc := prGateFor(t, lanes, "pull_request", "remote", cov(c))
				sc.results = map[string]string{job: res}
				id := map[string]string{prFingerprintJob: prFingerprintID, "pr-preflight-platforms": "PR_PREFLIGHT_PLATFORMS", prScriptsChecksJob: prScriptsChecksID}[job]
				if id == "" {
					id = prPackageGates[job]
				}
				add(fmt.Sprintf("covered=%s, %s %s", c, job, res), sc, false, id)
			}
		}
	}
	// A failed decision: the retired jobs and the package gates skip
	// through needs, and the gate is red.
	for _, res := range []string{"failure", "cancelled", "skipped"} {
		sc := prGateFor(t, lanes, "pull_request", "remote", cov(""))
		sc.coverage = res
		add("decision "+res, sc, false, prRiskCoverageGateID)
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			pass, out := runPRGateStep(t, gateStep, s.sc)
			if pass != s.pass {
				t.Errorf("gate pass = %v, want %v\n%s", pass, s.pass, out)
			}
			if !s.pass && s.mention != "" && !regexp.MustCompile(`::error::`+regexp.QuoteMeta(s.mention)+`\b`).MatchString(out) {
				t.Errorf("red gate does not name %s:\n%s", s.mention, out)
			}
		})
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The Dolt server fingerprint (container image vs the pinned dolt CLI the
// Bazel dolt-server lanes start) runs on every PR in its own required job,
// whatever bazel-coverage says, and nowhere else in pr.yml.
func TestPRDoltServerFingerprintRunsOnEveryPR(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	job := pr.job(t, prFingerprintJob)
	if job.If != "" || len(job.Needs) != 0 || job.ContinueOnError || job.RunsOn != "ubuntu-latest" || job.TimeoutMinutes == 0 {
		t.Errorf("%s: if %q, needs %v, continue-on-error %v, runs-on %q, timeout %d; want an unconditional ubuntu-latest job with a timeout",
			prFingerprintJob, job.If, job.Needs, job.ContinueOnError, job.RunsOn, job.TimeoutMinutes)
	}
	var names []string
	for _, s := range job.Steps {
		if s.If != "" || s.ContinueOnError != nil {
			t.Errorf("%s step %q: if %q, continue-on-error %v", prFingerprintJob, s.Name, s.If, s.ContinueOnError)
		}
		names = append(names, s.Name)
	}
	want := []string{"", "Set up Go", "Install Dolt CLI", "Verify dolt on PATH", "Configure Git and Dolt identity", "Pull Dolt sql-server image", "Test Dolt server fingerprint (container + local)"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("%s steps %q, want %q", prFingerprintJob, names, want)
	}
	if got, want := job.step(t, "Pull Dolt sql-server image").Run, pr.job(t, "test-domain-uow").step(t, "Pull Dolt sql-server image").Run; got != want {
		t.Errorf("%s pulls the image with %q, test-domain-uow with %q", prFingerprintJob, got, want)
	}
	if got := job.step(t, "Install Dolt CLI").Run; got != "./scripts/ci/install-dolt.sh" {
		t.Errorf("%s installs dolt with %q", prFingerprintJob, got)
	}
	gate := pr.job(t, "ci-gate")
	env := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, prFingerprintJob) || env[prFingerprintID] != "${{ needs."+prFingerprintJob+".result }}" ||
		!contains(strings.Fields(env["CI_GATE_REQUIRED"]), prFingerprintID) {
		t.Errorf("ci-gate does not require %s", prFingerprintID)
	}
	for name, j := range pr.Jobs {
		for _, s := range j.Steps {
			if strings.Contains(s.Run, "TestDoltServerFingerprint") && name != prFingerprintJob {
				t.Errorf("%s step %q runs the fingerprint; only %s does", name, s.Name, prFingerprintJob)
			}
		}
	}
}

// PR Core's duties the Bazel lanes do not take over run on every PR, in
// pr.yml's required scripts-go-checks job (PR Core's environment): `go test
// ./scripts/...` with PR Core's flags (the policy tests, some of which check
// part or all of their rules under go test only), go test's vet checks over
// ./... (rules_go's go_test runs none), and the Go tests Bazel does not run
// or skips (tools/bazel/equivalence_allowlist.txt), each required to pass.
func TestPRRunsGoTestsBazelSkips(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	job := pr.job(t, prScriptsChecksJob)
	if job.If != "" || job.ContinueOnError || len(job.Needs) != 0 || job.RunsOn != "ubuntu-latest" || job.TimeoutMinutes == 0 {
		t.Errorf("%s: if %q, continue-on-error %v, needs %v, runs-on %q, timeout %d; want an unconditional ubuntu-latest job with a timeout",
			prScriptsChecksJob, job.If, job.ContinueOnError, job.Needs, job.RunsOn, job.TimeoutMinutes)
	}
	var names []string
	for _, st := range job.Steps {
		names = append(names, st.Name)
		if st.ContinueOnError != nil {
			t.Errorf("%s step %q has continue-on-error", prScriptsChecksJob, st.Name)
		}
	}
	wantNames := []string{"", "Set up Go", "Restore Go module cache", "Restore race Go build cache", "Install Dolt",
		"Configure Git and Dolt identity", "Go test the scripts packages", "Go vet with go test's checks", prAllowlistedStep}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("%s steps %q, want %q", prScriptsChecksJob, names, wantNames)
	}
	// The environment PR Core's job gives its go test.
	core := pr.job(t, "pr-core-wrapper")
	for _, name := range []string{"Install Dolt", "Configure Git and Dolt identity", "Restore race Go build cache"} {
		if got, want := job.step(t, name), core.step(t, name); got.Run != want.Run || !reflect.DeepEqual(got.With, want.With) || got.Uses != want.Uses {
			t.Errorf("%s step %q differs from pr-core-wrapper's", prScriptsChecksJob, name)
		}
	}
	untilDone := "${{ !cancelled() && steps.setup-go.outcome == 'success' }}"
	for name, want := range map[string]struct {
		run, ifc string
		env      map[string]string
	}{
		"Go test the scripts packages": {"bash scripts/ci/scripts-go-test.sh", "", map[string]string{
			"BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION": "1", "GOCACHE": "${{ runner.temp }}/go-cache/race"}},
		"Go vet with go test's checks": {"bash scripts/ci/go-test-vet.sh", untilDone, nil},
		prAllowlistedStep:              {"bash scripts/ci/allowlisted-go-tests.sh", untilDone, nil},
	} {
		st := job.step(t, name)
		if st.Run != want.run || st.If != want.ifc || st.Shell != "" || (len(st.Env) != 0 || len(want.env) != 0) && !reflect.DeepEqual(st.Env, want.env) {
			t.Errorf("%s step %q: run %q, if %q, shell %q, env %v; want run %q, if %q, env %v", prScriptsChecksJob, name, st.Run, st.If, st.Shell, st.Env, want.run, want.ifc, want.env)
		}
	}
	gate := pr.job(t, "ci-gate")
	env := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, prScriptsChecksJob) || env[prScriptsChecksID] != "${{ needs."+prScriptsChecksJob+".result }}" ||
		!contains(strings.Fields(env["CI_GATE_REQUIRED"]), prScriptsChecksID) {
		t.Errorf("ci-gate does not require %s", prScriptsChecksID)
	}
	// Nothing else in pr.yml runs these (one place to look).
	for name, j := range pr.Jobs {
		for _, st := range j.Steps {
			for _, script := range []string{"allowlisted-go-tests.sh", "scripts-go-test.sh", "go-test-vet.sh"} {
				if strings.Contains(st.Run, script) && name != prScriptsChecksJob {
					t.Errorf("%s step %q runs %s; only %s does", name, st.Name, script, prScriptsChecksJob)
				}
			}
		}
	}
	if os.Getenv("TEST_SRCDIR") != "" {
		return // scripts_test's runfiles hold none of the scripts (this part runs in that job itself)
	}
	root := sourceRepoRoot(t)
	prelude := []string{
		"set -euo pipefail",
		`SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"`,
		`REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"`,
		`source "$REPO_ROOT/.buildflags"`,
	}
	for script, rest := range map[string][]string{
		"scripts/ci/allowlisted-go-tests.sh": {
			`source "$REPO_ROOT/scripts/ci/lib/test-env.sh"`,
			`cd "$REPO_ROOT"`,
			"beads_test_env_enter",
			"export BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION=1",
			`python3 tools/bazel/run_allowlisted_go_tests.py "$@"`,
		},
		"scripts/ci/scripts-go-test.sh": {
			`source "$REPO_ROOT/scripts/ci/lib/timing.sh"`,
			`source "$REPO_ROOT/scripts/ci/lib/test-env.sh"`,
			`cd "$REPO_ROOT"`,
			"beads_test_env_enter",
			`ci_time "scripts go test" -- \`,
			`go test -p 4 -parallel 4 -race -short -timeout=30m -skip '^TestEmbedded' ./scripts/...`,
		},
		"scripts/ci/go-test-vet.sh": {
			`source "$REPO_ROOT/scripts/ci/lib/timing.sh"`,
			`cd "$REPO_ROOT"`,
			"GO_TEST_VET_FLAGS=(" + strings.Join(goTestDefaultVetFlags(t), " ") + ")",
			`ci_time "go vet (go test's checks)" -- \`,
			`go vet -tags gms_pure_go "${GO_TEST_VET_FLAGS[@]}" ./...`,
		},
	} {
		var code []string
		for _, line := range strings.Split(readPolicyFile(t, root, script), "\n") {
			if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
				code = append(code, l)
			}
		}
		if want := append(append([]string{}, prelude...), rest...); !reflect.DeepEqual(code, want) {
			t.Errorf("%s code changed:\n%s\nwant:\n%s", script, strings.Join(code, "\n"), strings.Join(want, "\n"))
		}
	}
	// PR Core's own test command: the scripts step must keep its flags.
	if core := readPolicyFile(t, root, "scripts/ci/pr-core.sh"); !strings.Contains(core,
		`go_test -p "$GO_TEST_PKG_PARALLEL" -parallel "$GO_TEST_PARALLEL" -race -short -timeout=30m -skip '^TestEmbedded' ./...`) {
		t.Errorf("scripts/ci/pr-core.sh's go test changed; keep scripts/ci/scripts-go-test.sh's flags equal to it")
	}

	// The real allowlist plans: every entry maps to a package go test runs.
	python := requireHostTool(t, "python3")
	runner := filepath.Join(root, "tools", "bazel", "run_allowlisted_go_tests.py")
	out, err := exec.Command(python, runner, "--dry-run").CombinedOutput()
	if err != nil {
		t.Fatalf("dry run of the real allowlist: %v\n%s", err, out)
	}
	for _, line := range strings.Split(readPolicyFile(t, root, "tools/bazel/equivalence_allowlist.txt"), "\n") {
		body, _, _ := strings.Cut(line, "#")
		f := strings.Fields(body)
		if len(f) == 0 {
			continue
		}
		if !strings.Contains(string(out), " ./"+f[0]+"\n") {
			t.Errorf("allowlist entry %q: no go test of ./%s planned:\n%s", line, f[0], out)
		}
		if f[1] != "*" && !strings.Contains(string(out), regexp.QuoteMeta(strings.TrimSuffix(f[1], "*"))) {
			t.Errorf("allowlist entry %q: not selected:\n%s", line, out)
		}
	}
}

// goTestDefaultVetFlags: the vet checks the Go toolchain running this test
// gives `go test` (cmd/go's defaultVetFlags), read from its source, so a Go
// upgrade that changes them fails here until go-test-vet.sh follows.
func goTestDefaultVetFlags(t *testing.T) []string {
	t.Helper()
	goroot, err := exec.Command(requireHostTool(t, "go"), "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(goroot)), "src", "cmd", "go", "internal", "test", "test.go"))
	if err != nil {
		t.Fatalf("reading cmd/go's test.go for defaultVetFlags: %v", err)
	}
	m := regexp.MustCompile(`(?s)var defaultVetFlags = \[\]string\{(.*?)\n\}`).FindSubmatch(src)
	if m == nil {
		t.Fatal("cmd/go's test.go has no defaultVetFlags")
	}
	var flags []string
	for _, line := range strings.Split(string(m[1]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//") || line == "" {
			continue
		}
		flags = append(flags, strings.Trim(strings.TrimSuffix(line, ","), `"`))
	}
	if len(flags) < 5 {
		t.Fatalf("defaultVetFlags parsed as %v", flags)
	}
	return flags
}

// run_allowlisted_go_tests.py on a synthetic allowlist and a fake go that
// reports given results: every entry must match a test that ran and passed.
func TestRunAllowlistedGoTestsScript(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold no tools/bazel Python")
	}
	requireHostTool(t, "bash")
	python := requireHostTool(t, "python3")
	runner := filepath.Join(sourceRepoRoot(t), "tools", "bazel", "run_allowlisted_go_tests.py")
	dir := t.TempDir()
	fakeGo := filepath.Join(dir, "go")
	// Emits one top-level event per FAKE_<pkg>="Test:action ..." pair and
	// records its arguments; exits FAKE_RC.
	if err := os.WriteFile(fakeGo, []byte(`#!/usr/bin/env bash
echo "$*" >> "$FAKE_LOG"
pkg="${!#}"; key="FAKE_$(printf '%s' "${pkg#./}" | tr -c 'A-Za-z0-9' '_')"
for pair in ${!key}; do
  printf '{"Action":"%s","Package":"x","Test":"%s"}\n' "${pair#*:}" "${pair%%:*}"
done
exit "${FAKE_RC:-0}"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	allow := filepath.Join(dir, "allow.txt")
	if err := os.WriteFile(allow, []byte(`# comment
cmd/bd TestZZStdioNotLeaked skip  # needs its baseline
scripts TestA skip  # why
scripts TestGlob* skip  # why
`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) (string, error) {
		log := filepath.Join(dir, "log")
		_ = os.Remove(log)
		cmd := exec.Command(python, runner, "--allowlist", allow, "--go", fakeGo)
		cmd.Env = append(os.Environ(), append([]string{"FAKE_LOG=" + log}, env...)...)
		out, err := cmd.CombinedOutput()
		args, _ := os.ReadFile(log)
		return string(out) + "\nARGS:\n" + string(args), err
	}
	good := []string{
		"FAKE_cmd_bd=TestAAAStdioBaseline:pass TestZZStdioNotLeaked:pass",
		"FAKE_scripts=TestA:pass TestGlobOne:pass TestGlobTwo:pass",
	}
	out, err := run(good...)
	if err != nil {
		t.Fatalf("all passed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"-run ^(TestAAAStdioBaseline|TestZZStdioNotLeaked)$ ./cmd/bd",
		"-run ^(TestA|TestGlob.*)$ ./scripts",
		"test -json -short -count=1 -timeout=30m -skip ^TestEmbedded -tags gms_pure_go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("go invocations lack %q:\n%s", want, out)
		}
	}
	for name, env := range map[string][]string{
		"entry skipped":       {good[0], "FAKE_scripts=TestA:skip TestGlobOne:pass"},
		"glob member skipped": {good[0], "FAKE_scripts=TestA:pass TestGlobOne:pass TestGlobTwo:skip"},
		"entry missing":       {good[0], "FAKE_scripts=TestGlobOne:pass"},
		"glob matched none":   {good[0], "FAKE_scripts=TestA:pass"},
		"companion only":      {"FAKE_cmd_bd=TestAAAStdioBaseline:pass", good[1]},
		"other test failed":   {good[0], good[1] + " TestOther:fail"},
		"go test failed":      append([]string{"FAKE_RC=1"}, good...),
		"package not run":     {good[1]},
	} {
		if out, err := run(env...); err == nil {
			t.Errorf("%s: runner passed:\n%s", name, out)
		}
	}
	// Entries it cannot run.
	for name, body := range map[string]string{
		"package glob": "cmd/* TestX skip  # why\n",
		"bad name":     "scripts Test(X) skip  # why\n",
		"no reason":    "scripts TestX skip\n",
	} {
		if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := run(good...); err == nil {
			t.Errorf("%s: runner accepted the allowlist:\n%s", name, out)
		}
	}
}

// bazel.yml's step-3 lanes: in mode remote (always, on covered PRs) every
// `bazel test` adds --config=sole-run, whose lines are exactly no cached
// test results and no whole-invocation retry after a remote cache eviction;
// their Bazel steps and configs are pinned exactly.
func TestBazelPRLanesArePinned(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	bazelCmd := regexp.MustCompile(`^\s*(?:\S+=\S+\s+)*(?:"?\$\(\s*)?bazel\s+(test|build|run|query|cquery|coverage)\b`)
	for lane, steps := range bazelPRLaneSteps {
		job := workflow.job(t, lane)
		if job.Env["BAZEL_SOLE_RUN"] != bazelSoleRunEnv {
			t.Errorf("%s env BAZEL_SOLE_RUN = %q, want %q", lane, job.Env["BAZEL_SOLE_RUN"], bazelSoleRunEnv)
		}
		for name, want := range steps {
			step := job.step(t, name)
			if strings.TrimSpace(step.Run) != want || step.ContinueOnError != nil {
				t.Errorf("%s step %q (continue-on-error %v) changed; want exactly:\n%s\ngot:\n%s", lane, name, step.ContinueOnError, want, step.Run)
			}
		}
		// Every bazel test in the lane takes sole-run; nothing else runs
		// Bazel but the pinned steps and pure's builds.
		for _, step := range job.Steps {
			for _, line := range strings.Split(step.Run, "\n") {
				m := bazelCmd.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				if m[1] == "test" && !strings.Contains(line, bazelSoleRunArg) {
					t.Errorf("%s step %q runs bazel test without %s: %q", lane, step.Name, bazelSoleRunArg, line)
				}
				if _, pinned := steps[step.Name]; !pinned && m[1] != "build" && m[1] != "cquery" {
					t.Errorf("%s step %q runs %q outside the pinned steps", lane, step.Name, line)
				}
			}
		}
	}
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	for config, want := range bazelPRLaneRCLines {
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
	}
	// sole-run is used by these lanes only, and only through the env above.
	for name, job := range workflow.Jobs {
		_, lane := bazelPRLaneSteps[name]
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "sole-run") || (!lane && strings.Contains(step.Run, "BAZEL_SOLE_RUN")) {
				t.Errorf("%s step %q names sole-run directly", name, step.Name)
			}
		}
	}
}
