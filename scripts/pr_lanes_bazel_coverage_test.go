package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// pr.yml's PR Core, build-artifacts and pure-Go/js-wasm jobs are retired
// (ga-96smfk.22): Bazel / test, pure-Go and js/wasm and the dolt-server lane
// are their only run, and pr.yml's gate requires them to have run remotely
// and passed (TestPRGateRequiresRemoteBazelLanes). Everything the retired
// jobs did besides those tests keeps running on every PR: the package gates
// take the Bazel-built bd, the Dolt server fingerprint runs on the
// dolt-server lane, and nogo (//tools/nogo) runs go test's vet checks beside
// every compile of the Bazel lanes.

const (
	// bazel.yml's lanes for the step add --config=sole-run whenever they
	// execute remotely (enabled: mode remote, and the rbe-fork modes
	// fork-ro/fork-rw, a fork's or Dependabot PR's only run).
	bazelSoleRunEnv          = "${{ needs.rbe.outputs.enabled == 'true' && '--config=sole-run' || '' }}"
	bazelSoleRunArg          = `${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"}`
	bazelSoleRunEvictionLine = "test:sole-run --experimental_remote_cache_eviction_retries=0"
)

// F3: the package gates moved into bazel.yml (package-mcp, package-npm);
// they depend only on the caller's package-gates input (TestPackageGateJobs). This
// map is only their CI_GATE_REQUIRED ids, for the gate-simulation scenarios
// below (pr.yml no longer has jobs by these names).
var prPackageGateIDs = map[string]string{"package-mcp": "PACKAGE_MCP", "package-npm": "PACKAGE_NPM"}

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
		"test:prcore --test_tag_filters=-dolt-server,-dolt-server-proxied,-dolt-server-integration,-dolt-server-cmd,-embedded,-manual,-integration-only",
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
	"sole-run": {bazelSoleRunEvictionLine},
}

// The steps of step 3's lanes that run Bazel, exactly (as review F4 of
// step 1 for the embedded lane): nothing may be appended that selects,
// skips or re-runs tests.
var bazelPRLaneSteps = map[string]map[string]string{
	bazelJobName: {
		"bazel test //... --config=ci": `set -o pipefail
start=$(date +%s)
rc=0
bazel test //... --config=ci ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} ${BAZEL_FRESH:+"$BAZEL_FRESH"} \
  --profile="$RUNNER_TEMP/bazel-profile.json" \
  --build_event_json_file="$RUNNER_TEMP/bazel-bep.json" \
  --execution_log_compact_file="$RUNNER_TEMP/bazel-exec.log.zst" \
  --experimental_build_event_upload_strategy=local \
  2>&1 | tee "$RUNNER_TEMP/bazel-test.log" || rc=$?
echo "bazel test: exit $rc, $(( $(date +%s) - start ))s wall" | tee -a "$GITHUB_STEP_SUMMARY"
echo >> "$GITHUB_STEP_SUMMARY"
exit "$rc"`,
	},
	bazelPureJobName: {
		"Start every pure-Go artifact (gozstd contamination check)": `set -euo pipefail
# Not instrumented: no BEP/exec-log/profile is captured for this
# one-off smoke invocation (design's bazel-pure invocation list
# names build-wasm, build-pure, test-pure and test-bd only).
bazel run --config=pure //cmd/bd:bd -- version
bazel test --config=pure ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} ${BAZEL_FRESH:+"$BAZEL_FRESH"} \
  //internal/storage/embeddeddolt:embeddeddolt_test \
  //internal/tracker:tracker_test \
  --test_sharding_strategy=disabled \
  '--test_arg=-test.run=^$' \
  --test_env=BEADS_TEST_SKIP=dolt \
  --profile="$RUNNER_TEMP/bazel-profile-test-pure.json" \
  --build_event_json_file="$RUNNER_TEMP/bazel-bep-test-pure.json" \
  --execution_log_compact_file="$RUNNER_TEMP/bazel-exec-test-pure.log.zst" \
  --experimental_build_event_upload_strategy=local`,
		"Run pure-Go cmd/bd test subset (--config=pure)": `set -euo pipefail
bazel test --config=pure ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} ${BAZEL_FRESH:+"$BAZEL_FRESH"} //cmd/bd:bd_test \
  --test_sharding_strategy=disabled \
  "--test_arg=-test.run=$PURE_CMD_BD_TESTS" \
  --profile="$RUNNER_TEMP/bazel-profile-test-bd.json" \
  --build_event_json_file="$RUNNER_TEMP/bazel-bep-test-bd.json" \
  --execution_log_compact_file="$RUNNER_TEMP/bazel-exec-test-bd.log.zst" \
  --experimental_build_event_upload_strategy=local
n="$(grep -c '<testcase ' bazel-testlogs/cmd/bd/bd_test/test.xml || true)"
echo "pure cmd/bd subset: $n test cases"
(( n > 0 ))`,
	},
	bazelDoltJobName: {
		"bazel test //... --config=doltserver": `set -o pipefail
start=$(date +%s)
rc=0
bazel test //... --config=doltserver ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} ${BAZEL_FRESH:+"$BAZEL_FRESH"} \
  --build_event_json_file="$RUNNER_TEMP/bazel-bep.json" \
  --execution_log_compact_file="$RUNNER_TEMP/bazel-exec.log.zst" \
  --profile="$RUNNER_TEMP/bazel-profile.json" \
  --experimental_build_event_upload_strategy=local \
  2>&1 | tee "$RUNNER_TEMP/bazel-test.log" || rc=$?
echo "bazel test --config=doltserver: exit $rc, $(( $(date +%s) - start ))s wall" | tee -a "$GITHUB_STEP_SUMMARY"
exit "$rc"`,
	},
}

// prLegacyJobResult: a non-Bazel pr.yml need's result in a gate scenario:
// success unless sc.results overrides it. (package-mcp/package-npm are
// bazel.yml call outputs, simulated through sc.outputs.)
func prLegacyJobResult(job string, sc bazelGateScenario) string {
	if r, ok := sc.results[job]; ok {
		return r
	}
	return "success"
}

// pr.yml's non-Bazel jobs and artifacts around the lanes: nothing needs or
// downloads the retired build-artifacts job's artifact, the package gates
// are pinned, Bazel publishes bd under the name pr.yml passes, and a failed
// package gate or preflight leg reds pr.yml's actual gate step.
func TestPRGateNonBazelNeeds(t *testing.T) {
	requireHostTool(t, "bash")
	pr := readCIWorkflow(t, "pr.yml")
	gateStep := pr.job(t, "ci-gate").step(t, "Evaluate CI gate")
	required := strings.Fields(gateStep.Env["CI_GATE_REQUIRED"])

	for name, job := range pr.Jobs {
		if contains(job.Needs, "build-artifacts") {
			t.Errorf("%s needs the retired build-artifacts job", name)
		}
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/download-artifact@") {
				continue
			}
			art := step.With["name"]
			if art == "" || step.With["pattern"] != "" {
				t.Errorf("%s downloads an artifact by pattern or without a name: %v", name, step.With)
			}
			if strings.Contains(art, "ci-build-artifacts") {
				t.Errorf("%s downloads %q; nothing in pr.yml consumes the bd artifact", name, art)
			}
		}
	}
	testPackageGateJobs(t, required)
	// Bazel's artifact: bazel-test publishes it (always, after its tests)
	// under the name pr.yml passes (TestBazelWorkflowPublishesBuildArtifacts).
	if pr.job(t, "bazel").With["build-artifact-name"] != "bazel-ci-build-artifacts" {
		t.Errorf("pr.yml's bazel call no longer names its artifact bazel-ci-build-artifacts")
	}
	bazelTest := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	pkgStep := bazelTest.step(t, "Package bd (ci-build-artifacts layout)")
	if pkgStep.Run != `./scripts/ci/package-bazel-bd.sh "$RUNNER_TEMP/bd-artifacts"` || pkgStep.ContinueOnError != nil {
		t.Errorf("bazel-test packages bd with %q (continue-on-error %v)", pkgStep.Run, pkgStep.ContinueOnError)
	}

	lanes := bazelPRCallLanes(t, pr.job(t, "bazel").With)
	if pass, out := runPRGateStep(t, gateStep, prGateFor(t, lanes, "pull_request", "remote")); !pass {
		t.Errorf("remote, every lane and job passed: gate red\n%s", out)
	}
	for _, res := range []string{"skipped", "failure", "cancelled"} {
		for _, job := range []string{"package-mcp", "package-npm", "pr-preflight-platforms"} {
			sc := prGateFor(t, lanes, "pull_request", "remote")
			// F3: package-mcp/package-npm are bazel.yml call outputs
			// (needs.bazel.outputs.package-mcp), not pr.yml job results.
			id := prPackageGateIDs[job]
			if id != "" {
				sc.outputs[job] = res
			} else {
				id = "PR_PREFLIGHT_PLATFORMS"
				sc.results = map[string]string{job: res}
			}
			pass, out := runPRGateStep(t, gateStep, sc)
			if pass || !regexp.MustCompile(`::error::`+regexp.QuoteMeta(id)+`\b`).MatchString(out) {
				t.Errorf("%s %s: gate pass = %v, want red naming %s\n%s", job, res, pass, id, out)
			}
		}
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

// F3: the package gates (bazel.yml's package-mcp, package-npm). They depend
// only on the caller turning package-gates on, build bd for themselves - via
// the Bazel lane's binary when the farm is up, a go build fallback
// otherwise (package-mcp.sh/package-npm.sh's prepare_bd_binary) - and are
// structurally identical apart from their language setup and `make` target.
// Their runs-on/if/needs/result-recorder are already pinned for every
// bazel.yml job by TestBazelWorkflowJobsAndExecutionMode and
// TestBazelLaneIsGated; this test pins the step-by-step
// detail those generic checks do not.
func testPackageGateJobs(t *testing.T, prGateRequired []string) {
	t.Helper()
	workflow := readCIWorkflow(t, bazelWorkflowName)
	// release.yml's release-package-mcp/release-package-npm jobs (the tag
	// build's package gates) run the same language setup; bazel.yml's copy
	// must not drift from it.
	mainWorkflow := readCIWorkflow(t, "release.yml")
	type pkgLane struct {
		job, detectOutput, langStepName, makeTarget string
	}
	lanes := []pkgLane{
		{bazelPackageMCPJobName, "mcp_package", "Set up Python", "make ci-package-mcp"},
		{bazelPackageNPMJobName, "npm_package", "Set up Node.js", "make ci-package-npm"},
	}
	for _, lane := range lanes {
		id := bazelLaneGateIDs[lane.job]
		if !contains(prGateRequired, id) {
			t.Errorf("ci-gate does not require %s (%s)", lane.job, id)
		}
		job, ok := workflow.Jobs[lane.job]
		if !ok {
			t.Fatalf("%s has no %s job", bazelWorkflowName, lane.job)
		}
		if !reflect.DeepEqual([]string(job.Needs), []string{bazelRBEJobName}) || job.If != bazelPackageGatesIf || job.RunsOn != bazelPackageRunsOn {
			t.Errorf("%s: needs %v, if %q, runs-on %q; want needs [%s], if %q, runs-on %q",
				lane.job, job.Needs, job.If, job.RunsOn, bazelRBEJobName, bazelPackageGatesIf, bazelPackageRunsOn)
		}

		detectCond := "steps.detect.outputs." + lane.detectOutput + " == 'true'"
		detectIf := "${{ " + detectCond + " }}"
		// Mode remote, not enabled: the package gates take no rbe-fork
		// certificate, so fork modes keep the go build fallback.
		bazelPathIf := "${{ " + detectCond + " && needs." + bazelRBEJobName + ".outputs.mode == 'remote' }}"
		fallbackIf := "${{ " + detectCond + " && needs." + bazelRBEJobName + ".outputs.mode != 'remote' }}"
		wantNames := []string{
			"", // checkout (no name)
			"Decide applicability",
			"Set up Bazel",
			"bazel build //cmd/bd:bd_for_tests",
			"Package the Bazel-built bd",
			"Set up Go",
			"Restore Go module cache",
			"Verify Go modules",
			"Restore non-race Go build cache",
			lane.langStepName,
		}
		if lane.job == bazelPackageMCPJobName {
			wantNames = append(wantNames, "Install uv")
		}
		wantNames = append(wantNames, "Run "+map[string]string{bazelPackageMCPJobName: "MCP", bazelPackageNPMJobName: "npm"}[lane.job]+" package gate", "Record job result")

		var gotNames []string
		for _, s := range job.Steps {
			gotNames = append(gotNames, s.Name)
		}
		if !reflect.DeepEqual(gotNames, wantNames) {
			t.Errorf("%s step names = %q, want %q", lane.job, gotNames, wantNames)
		}

		checkout := job.Steps[0]
		if checkout.Uses != "actions/checkout@"+checkoutSHA || checkout.With["fetch-depth"] != "0" {
			t.Errorf("%s checkout = %+v; want fetch-depth 0 (detect-package-gates.sh diffs full history)", lane.job, checkout)
		}

		detect := job.step(t, "Decide applicability")
		wantDetectEnv := map[string]string{
			// The merge queue's bounds where there is no pull_request.
			"PR_BASE_SHA":     "${{ github.event.pull_request.base.sha || github.event.merge_group.base_sha }}",
			"PR_HEAD_SHA":     "${{ github.event.pull_request.head.sha || github.event.merge_group.head_sha }}",
			"PUSH_BEFORE_SHA": "${{ github.event.before }}",
			"PUSH_AFTER_SHA":  "${{ github.sha }}",
		}
		if detect.ID != "detect" || detect.Run != "./scripts/ci/detect-package-gates.sh" || !reflect.DeepEqual(detect.Env, wantDetectEnv) {
			t.Errorf("%s detect step: id %q, run %q, env %v; want id detect, run ./scripts/ci/detect-package-gates.sh, env %v",
				lane.job, detect.ID, detect.Run, detect.Env, wantDetectEnv)
		}

		// The Bazel-built-bd path: gated on the farm being up, in addition
		// to detection. No --config=ci (only test:ci exists). The explicit
		// --@rules_go//go/config:race matches test:ci's top-level build
		// setting, so bd_for_tests' race="off" transition lands in the same
		// output directory and action-cache key `bazel test --config=ci`
		// already populated (verified with cquery against origin/main
		// e2b78f7d7a: both configs cquery to the identical
		// bazel-out/k8-fastbuild-ST-.../bin/cmd/bd/bd_for_tests/bd path).
		buildStep := job.step(t, "bazel build //cmd/bd:bd_for_tests")
		if buildStep.If != bazelPathIf || buildStep.Run != `bazel build --@rules_go//go/config:race //cmd/bd:bd_for_tests --remote_download_regex='.*/bin/cmd/bd/bd_for_tests/bd$'` {
			t.Errorf("%s bazel build step: if %q, run %q; want if %q", lane.job, buildStep.If, buildStep.Run, bazelPathIf)
		}
		pkgBD := job.step(t, "Package the Bazel-built bd")
		if pkgBD.If != bazelPathIf || !strings.Contains(pkgBD.Run, `./scripts/ci/package-bazel-bd.sh "$RUNNER_TEMP/bd-artifacts"`) ||
			!strings.Contains(pkgBD.Run, "BEADS_TEST_BD_BINARY=$RUNNER_TEMP/bd-artifacts/bd-linux-gms-pure") {
			t.Errorf("%s package-the-Bazel-built-bd step: if %q, run %q", lane.job, pkgBD.If, pkgBD.Run)
		}
		// F3 review NIT-5: nothing past this step needs the Bazel server or
		// its RBE client key (uv sync/pytest/npm install run PR-controlled
		// code), so both are torn down here, defense in depth.
		if !strings.Contains(pkgBD.Run, "bazel shutdown") || !strings.Contains(pkgBD.Run, `rm -rf "$RUNNER_TEMP/bazel-ci-secret"`) {
			t.Errorf("%s package-the-Bazel-built-bd step does not shut down Bazel and remove the RBE client key: run %q", lane.job, pkgBD.Run)
		}
		setupBazel := job.step(t, "Set up Bazel")
		if setupBazel.If != bazelPathIf || setupBazel.Uses != "./"+setupBazelActionDir {
			t.Errorf("%s Set up Bazel: if %q, uses %q; want if %q", lane.job, setupBazel.If, setupBazel.Uses, bazelPathIf)
		}

		// The go-build-fallback path: gated on detection and the farm being
		// down; it restores caches but never sets BEADS_TEST_BD_BINARY, so
		// prepare_bd_binary() falls back to `go build`.
		for _, name := range []string{"Set up Go", "Restore Go module cache", "Verify Go modules", "Restore non-race Go build cache"} {
			if st := job.step(t, name); st.If != fallbackIf {
				t.Errorf("%s step %q: if %q, want %q", lane.job, name, st.If, fallbackIf)
			}
		}

		// The language setup and the gate command run whenever detection
		// did, whichever bd path provided the binary.
		if st := job.step(t, lane.langStepName); st.If != detectIf {
			t.Errorf("%s step %q: if %q, want %q", lane.job, lane.langStepName, st.If, detectIf)
		}

		// The language setup itself must not drift from release.yml's copy
		// of the same gate (python-version / Node version / the uv install
		// command): the tag build is the one other place this exact setup
		// is pinned.
		mainJob := mainWorkflow.job(t, "release-"+lane.job)
		langStep := job.step(t, lane.langStepName)
		mainLangStep := mainJob.step(t, lane.langStepName)
		if langStep.Uses != mainLangStep.Uses || !reflect.DeepEqual(langStep.With, mainLangStep.With) {
			t.Errorf("%s %q: uses %q, with %v; release.yml's release-%s has uses %q, with %v",
				lane.job, lane.langStepName, langStep.Uses, langStep.With, lane.job, mainLangStep.Uses, mainLangStep.With)
		}
		if lane.job == bazelPackageMCPJobName {
			installUv := job.step(t, "Install uv")
			mainInstallUv := mainJob.step(t, "Install uv")
			if installUv.Run != mainInstallUv.Run {
				t.Errorf("%s Install uv: run %q; release.yml's has run %q", lane.job, installUv.Run, mainInstallUv.Run)
			}
		}
		gateName := "Run " + map[string]string{bazelPackageMCPJobName: "MCP", bazelPackageNPMJobName: "npm"}[lane.job] + " package gate"
		gate := job.step(t, gateName)
		if gate.If != detectIf || gate.Run != lane.makeTarget {
			t.Errorf("%s gate step: if %q, run %q; want if %q, run %q", lane.job, gate.If, gate.Run, detectIf, lane.makeTarget)
		}
		if job.stepIndex(t, gateName) != len(job.Steps)-2 {
			t.Errorf("%s gate step is not the last step before the result recorder", lane.job)
		}
	}
}

// The Dolt server fingerprint runs in CI on the local backend only: the
// pinned dolt CLI every Bazel dolt-server lane starts, checked by
// //internal/testutil:testutil_dolt_test on the dolt-server lane whatever the
// mode. No CI job runs the Dolt-backed suites against the container backend
// any more, so none runs its container half (plain `go test` does, wherever
// docker and the image are present), and no pr.yml job runs the fingerprint
// or reports a separate fingerprint result.
func TestDoltServerFingerprintRunsOnTheDoltServerLane(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	for name, j := range pr.Jobs {
		for _, s := range j.Steps {
			if strings.Contains(s.Run, "TestDoltServerFingerprint") {
				t.Errorf("%s step %q runs the fingerprint; //internal/testutil:testutil_dolt_test does", name, s.Name)
			}
		}
	}
	gate := pr.job(t, "ci-gate")
	env := gate.step(t, "Evaluate CI gate").Env
	if _, ok := env["TEST_DOLT_SERVER_FINGERPRINT"]; ok || strings.Contains(env["CI_GATE_REQUIRED"], "FINGERPRINT") {
		t.Errorf("ci-gate still evaluates a separate fingerprint result: %q", env["CI_GATE_REQUIRED"])
	}

	rule := bazelRuleBlock(readPolicyFile(t, sourceRepoRoot(t), "internal/testutil/BUILD.bazel"), "testutil_dolt_test")
	for _, w := range []string{`"$(rootpath :testutil_test)"`, `"BEADS_TEST_DOLT_SERVER": "local"`, `"BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1"`, `tags = ["dolt-server"]`} {
		if !strings.Contains(rule, w) {
			t.Errorf("//internal/testutil:testutil_dolt_test lacks %s (the local half runs on the dolt-server lane):\n%s", w, rule)
		}
	}
}

// PR Core's one duty the Bazel lanes did not take over, go test's vet checks
// over ./... (rules_go's go_test runs none), is nogo's: //tools/nogo runs
// cmd/go's defaultVetFlags analyzers beside every compile of every lane, so
// bazel.yml's required test lane (`bazel test //... --config=ci`) fails on a
// finding. No workflow runs `go vet` any more. The repository policy tests
// and the Go tests that walk the checkout run under Bazel
// (//scripts:scripts_test and //test/docsync over //:repo_files), so no
// workflow runs `go test` over ./scripts/... or the equivalence allowlist
// either.
func TestPRRunsGoTestsBazelSkips(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	root := sourceRepoRoot(t)
	// tools/nogo/analyzers.bzl's VET_PASSES are go test's vet checks (cmd/vet
	// keeps -bool and -buildtags as aliases of the bools and buildtag
	// analyzers).
	m := regexp.MustCompile(`(?ms)^VET_PASSES = \[(.*?)^\]`).FindStringSubmatch(readPolicyFile(t, root, "tools/nogo/analyzers.bzl"))
	if m == nil {
		t.Fatal("tools/nogo/analyzers.bzl has no VET_PASSES list")
	}
	var passes []string
	for _, q := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(m[1], -1) {
		passes = append(passes, q[1])
	}
	var want []string
	for _, flag := range goTestDefaultVetFlags(t) {
		flag = strings.TrimPrefix(flag, "-")
		if alias, ok := map[string]string{"bool": "bools", "buildtags": "buildtag"}[flag]; ok {
			flag = alias
		}
		want = append(want, flag)
	}
	sort.Strings(passes)
	sort.Strings(want)
	if !reflect.DeepEqual(passes, want) {
		t.Errorf("tools/nogo/analyzers.bzl VET_PASSES = %v, want go test's vet checks %v", passes, want)
	}
	if module := readPolicyFile(t, root, "MODULE.bazel"); !strings.Contains(module, "go_sdk.nogo(\n    includes = [\"//:__subpackages__\"],\n    nogo = \"//tools/nogo\",\n)") {
		t.Error("MODULE.bazel does not register //tools/nogo for every first-party package (go_sdk.nogo)")
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "ci", "go-test-vet.sh")); err == nil {
		t.Error("scripts/ci/go-test-vet.sh is back; go test's vet checks run as nogo")
	}
	goVet := regexp.MustCompile(`\bgo vet\b|go-test-vet\.sh`)
	for _, wf := range []string{"pr.yml", "main.yml", "nightly.yml", "pr-risk.yml", bazelWorkflowName} {
		for name, j := range readCIWorkflow(t, wf).Jobs {
			for _, st := range j.Steps {
				if goVet.MatchString(st.Run) {
					t.Errorf("%s %s step %q runs go vet; nogo runs its checks in the Bazel lanes", wf, name, st.Name)
				}
			}
		}
	}
	retired := regexp.MustCompile(`allowlisted-go-tests\.sh|scripts-go-test\.sh|run_allowlisted_go_tests\.py|go test[^\n]* \./scripts/\.\.\.`)
	for _, wf := range []string{"pr.yml", "main.yml", "nightly.yml", "pr-risk.yml", bazelWorkflowName} {
		for name, j := range readCIWorkflow(t, wf).Jobs {
			for _, st := range j.Steps {
				// A compile-only cache warm (-run '^$') runs no test.
				if m := retired.FindString(st.Run); m != "" && !strings.Contains(m, "-run '^$'") {
					t.Errorf("%s %s step %q runs %q; the scripts packages and the allowlisted tests run under Bazel", wf, name, st.Name, m)
				}
			}
		}
	}
	for _, gone := range []string{"scripts/ci/allowlisted-go-tests.sh", "scripts/ci/scripts-go-test.sh", "tools/bazel/run_allowlisted_go_tests.py"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(gone))); err == nil {
			t.Errorf("%s is back; the tests it ran under go test run under Bazel", gone)
		}
	}
	// The allowlist's remaining skip entries run in pr-preflight-platforms
	// (every OS), each named in its go test selector.
	fallback := pr.job(t, "pr-preflight-platforms").step(t, "Exercise generated Git hook timeout process boundary and go test's bd build fallback").Run
	for _, line := range strings.Split(readPolicyFile(t, root, "tools/bazel/equivalence_allowlist.txt"), "\n") {
		body, _, _ := strings.Cut(line, "#")
		f := strings.Fields(body)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 || f[0] != "cmd/bd" || f[2] != "skip" || !strings.Contains(fallback, f[1]) || !strings.HasSuffix(fallback, " ./cmd/bd") {
			t.Errorf("allowlist entry %q is not a cmd/bd skip run by pr-preflight-platforms' bd build fallback step (%q)", line, fallback)
		}
	}
}

// goTestDefaultVetFlags: the vet checks go.mod's Go toolchain gives `go test`
// (cmd/go's defaultVetFlags), read from its source, so a Go upgrade that
// changes them fails here until tools/nogo/analyzers.bzl follows. Under Bazel the
// toolchain is the registered SDK (testGo).
func goTestDefaultVetFlags(t *testing.T) []string {
	t.Helper()
	goroot, err := exec.Command(testGo(t), "env", "GOROOT").Output()
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
