package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCIWorkflowArtifactOwnership(t *testing.T) {
	for _, workflowName := range []string{"pr.yml", "main.yml"} {
		t.Run(workflowName, func(t *testing.T) {
			workflow := readCIWorkflow(t, workflowName)

			for _, forbidden := range []string{
				"golangci-lint",
				"make ci-pr-policy",
				"make ci-pr-lint",
			} {
				for _, step := range workflow.job(t, "build-artifacts").Steps {
					if strings.Contains(step.Run, forbidden) {
						t.Errorf("build-artifacts runs %q in step %q", forbidden, step.Name)
					}
				}
			}

			assertJobRunsExactly(t, workflow.job(t, "pr-policy-wrapper"), "make ci-pr-policy")
			assertJobRunsExactly(t, workflow.job(t, "pr-lint-wrapper"), "make ci-pr-lint")
		})
	}
}

func TestPRCIGateRequiresPolicyAndLintWrappers(t *testing.T) {
	gate := readCIWorkflow(t, "pr.yml").job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env

	for _, job := range []string{"pr-policy-wrapper", "pr-lint-wrapper"} {
		if !contains(gate.Needs, job) {
			t.Errorf("ci-gate needs %q: %v", job, gate.Needs)
		}
	}

	for key, want := range map[string]string{
		"PR_POLICY_WRAPPER": "${{ needs.pr-policy-wrapper.result }}",
		"PR_LINT_WRAPPER":   "${{ needs.pr-lint-wrapper.result }}",
	} {
		if got := gateEnv[key]; got != want {
			t.Errorf("ci-gate env %s = %q, want %q", key, got, want)
		}
	}

	for _, required := range []string{"PR_POLICY_WRAPPER", "PR_LINT_WRAPPER"} {
		if !strings.Contains(gateEnv["CI_GATE_REQUIRED"], required) {
			t.Errorf("ci-gate CI_GATE_REQUIRED does not include %q", required)
		}
	}
}

func TestPRWorkflowExercisesWindowsBenchmarkEnvScrubbing(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-preflight-platforms")

	if job.RunsOn != "${{ matrix.os }}" {
		t.Errorf("pr-preflight-platforms runs-on = %q, want matrix.os", job.RunsOn)
	}
	if got := job.Strategy.Matrix.OS; !equalStrings(got, []string{"ubuntu-latest", "macos-latest", "windows-latest"}) {
		t.Errorf("pr-preflight-platforms matrix os = %v, want required three-host matrix", got)
	}
	if job.If != "" {
		t.Errorf("pr-preflight-platforms job is conditional: %q", job.If)
	}
	if job.ContinueOnError {
		t.Error("pr-preflight-platforms job may not continue on error")
	}

	step := job.step(t, "Check benchmark environment scrubbing")
	if step.If != "matrix.os == 'windows-latest'" {
		t.Errorf("benchmark environment scrubbing selector = %q, want native Windows only", step.If)
	}
	if step.ContinueOnError != nil && step.ContinueOnError != false {
		t.Error("benchmark environment scrubbing step may not continue on error")
	}
	const command = "go test -tags gms_pure_go -count=1 -run '^(TestCleanEnvUsesHostKeySemantics|TestBenchmarkCommandBuildersStripDoltEnvOverrides)$' ./scripts/repro-dolt-prod-timeouts"
	if got := strings.TrimSpace(step.Run); got != command {
		t.Errorf("benchmark environment scrubbing command = %q, want %q", got, command)
	}

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if gate.If != "${{ always() }}" {
		t.Errorf("ci-gate condition = %q, want always() aggregation", gate.If)
	}
	if gate.ContinueOnError {
		t.Error("ci-gate may not continue on error")
	}
	if !contains(gate.Needs, "pr-preflight-platforms") {
		t.Errorf("ci-gate does not require pr-preflight-platforms: %v", gate.Needs)
	}
	if got := gateEnv["PR_PREFLIGHT_PLATFORMS"]; got != "${{ needs.pr-preflight-platforms.result }}" {
		t.Errorf("ci-gate pr-preflight-platforms result = %q", got)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "PR_PREFLIGHT_PLATFORMS") {
		t.Error("ci-gate required set omits pr-preflight-platforms")
	}
}

func TestPRCIGateRequiresJSWasmHookExecution(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "check-cmd-bd-puregeo-tests")
	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("js/wasm hook job runs-on = %q, want ubuntu-latest", job.RunsOn)
	}
	if job.If != "" {
		t.Errorf("js/wasm hook job is conditional: %q", job.If)
	}

	setupGo := job.step(t, "Set up Go")
	if setupGo.Uses != setupGoActionFamily+"@"+setupGoSHA {
		t.Errorf("setup-go action = %q", setupGo.Uses)
	}
	if setupGo.With["go-version-file"] != "go.mod" || setupGo.With["cache"] != "false" {
		t.Errorf("setup-go inputs = %v", setupGo.With)
	}

	setupNode := job.step(t, "Set up Node.js")
	if setupNode.Uses != setupNodeActionFamily+"@"+setupNodeSHA {
		t.Errorf("setup-node action = %q", setupNode.Uses)
	}
	if setupNode.With["node-version"] != "24" {
		t.Errorf("setup-node version = %q, want 24", setupNode.With["node-version"])
	}

	execute := job.step(t, "Run js/wasm hook boundary")
	if execute.If != "" {
		t.Errorf("js/wasm hook step is conditional: %q", execute.If)
	}
	for key, want := range map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      "wasm",
		"GOOS":        "js",
	} {
		got, ok := execute.Env[key]
		if !ok || got != want {
			t.Errorf("js/wasm hook env %s = %q (present=%v), want %q", key, got, ok, want)
		}
	}
	for _, required := range []string{
		`go test -tags gms_pure_go -count=1 -timeout=2m`,
		`-exec="$(go env GOROOT)/lib/wasm/go_js_wasm_exec"`,
		`-run '^TestRunHookReportsUnsupportedExecution$'`,
		`-v ./internal/hooks`,
		`|| test_status=$?`,
		`=== RUN   TestRunHookReportsUnsupportedExecution`,
		`^--- PASS: TestRunHookReportsUnsupportedExecution`,
		`nonpass_pattern='^[[:space:]]*--- (FAIL|SKIP): '`,
		`[[ "$line" =~ $nonpass_pattern ]]`,
		`run_count != 1 || pass_count != 1 || nonpass_count != 0`,
	} {
		if !strings.Contains(execute.Run, required) {
			t.Errorf("js/wasm hook command does not contain %q", required)
		}
	}
	if regexp.MustCompile(`\bgo1\.[0-9]`).MatchString(execute.Run) {
		t.Errorf("js/wasm hook command duplicates the Go version owned by go.mod")
	}

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "check-cmd-bd-puregeo-tests") {
		t.Errorf("ci-gate does not require js/wasm hook job: %v", gate.Needs)
	}
	if got := gateEnv["CHECK_CMD_BD_PUREGEO_TESTS"]; got != "${{ needs.check-cmd-bd-puregeo-tests.result }}" {
		t.Errorf("ci-gate js/wasm hook result = %q", got)
	}
	if !strings.Contains(gateEnv["CI_GATE_REQUIRED"], "CHECK_CMD_BD_PUREGEO_TESTS") {
		t.Errorf("ci-gate required set omits js/wasm hook job")
	}
}

func TestPRCIGateRequiresGeneratedHookTimeoutProcessBoundary(t *testing.T) {
	const (
		jobName     = "pr-preflight-platforms"
		stepName    = "Exercise generated Git hook timeout process boundary"
		stepCommand = "go test '-tags=gms_pure_go' -count=1 -run '^TestGeneratedHookTimeoutProcessBoundary$' ./cmd/bd"
		gateKey     = "PR_PREFLIGHT_PLATFORMS"
	)

	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, jobName)
	if job.RunsOn != "${{ matrix.os }}" || !equalStrings(job.Strategy.Matrix.OS, []string{"ubuntu-latest", "macos-latest", "windows-latest"}) {
		t.Errorf("generated-hook process job is not the required three-host matrix: runs-on=%q os=%v", job.RunsOn, job.Strategy.Matrix.OS)
	}
	if job.TimeoutMinutes != 20 {
		t.Errorf("generated-hook process job timeout = %d minutes, want 20", job.TimeoutMinutes)
	}
	step := job.step(t, stepName)
	if step.If != "" || (step.ContinueOnError != nil && step.ContinueOnError != false) || step.Shell != "bash" || step.Run != stepCommand {
		t.Errorf("generated-hook process step is not required exact Bash execution: if=%q continue-on-error=%v shell=%q run=%q",
			step.If, step.ContinueOnError, step.Shell, step.Run)
	}
	assertStepsBefore(t, job, []string{"Restore Go module cache"}, []string{stepName})

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, jobName) || gateEnv[gateKey] != "${{ needs.pr-preflight-platforms.result }}" ||
		!contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), gateKey) {
		t.Errorf("ci-gate does not require the three-host generated-hook lane: needs=%v %s=%q required=%q",
			gate.Needs, gateKey, gateEnv[gateKey], gateEnv["CI_GATE_REQUIRED"])
	}
}

func TestStorageDomainUOWJobsUseNestedTimeoutBudgets(t *testing.T) {
	const (
		storageTimeoutMinutes     = 15
		doctorTimeoutMinutes      = 10
		setupTeardownSlackMinutes = 5
		jobTimeoutMinutes         = storageTimeoutMinutes + doctorTimeoutMinutes + setupTeardownSlackMinutes
	)
	storageCommand := fmt.Sprintf(
		"go test -tags gms_pure_go -race -count=1 -timeout %dm -v ./internal/storage/domain/... ./internal/storage/uow/... ./internal/tracker/...",
		storageTimeoutMinutes)
	doctorCommand := fmt.Sprintf(
		"go test -tags gms_pure_go -race -count=1 -timeout %dm -v ./cmd/bd/doctor/fix/",
		doctorTimeoutMinutes)

	for _, workflowName := range []string{"pr.yml", "main.yml"} {
		t.Run(workflowName, func(t *testing.T) {
			job := readCIWorkflow(t, workflowName).job(t, "test-domain-uow")
			if job.TimeoutMinutes != jobTimeoutMinutes {
				t.Errorf("test-domain-uow timeout = %d minutes, want %d", job.TimeoutMinutes, jobTimeoutMinutes)
			}
			// Go's timeout applies per package test binary, so this is a
			// maintenance tripwire for the declared sequential tier budgets,
			// not a mathematical upper bound for the multi-package first step.
			if job.TimeoutMinutes <= storageTimeoutMinutes+doctorTimeoutMinutes {
				t.Errorf(
					"test-domain-uow timeout = %d minutes, want more than %d minutes of declared tier budgets",
					job.TimeoutMinutes,
					storageTimeoutMinutes+doctorTimeoutMinutes)
			}
			assertStepRunsExactly(t, job, "Test domain + uow + tracker", storageCommand)
			assertStepRunsExactly(t, job, "Test doctor/fix (Dolt-backed, hard-require container)", doctorCommand)
		})
	}

	gate := readCIWorkflow(t, "pr.yml").job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "test-domain-uow") {
		t.Errorf("ci-gate needs test-domain-uow: %v", gate.Needs)
	}
	if got, want := gateEnv["TEST_DOMAIN_UOW"], "${{ needs.test-domain-uow.result }}"; got != want {
		t.Errorf("ci-gate TEST_DOMAIN_UOW = %q, want %q", got, want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "TEST_DOMAIN_UOW") {
		t.Errorf("ci-gate CI_GATE_REQUIRED does not include TEST_DOMAIN_UOW: %q", gateEnv["CI_GATE_REQUIRED"])
	}
}

func TestMacOSTestJobsReuseWorkspaceBDBinary(t *testing.T) {
	const (
		workspaceBDBinary = "${{ github.workspace }}/bd"
		buildCommand      = "go build -v -tags gms_pure_go ./cmd/bd"
		prTestCommand     = "go test -tags gms_pure_go -v -race -short -skip '^TestEmbedded' ./..."
		mainTestCommand   = "go test -tags gms_pure_go ${{ matrix.test-flags }} -skip '^TestEmbedded' ./..."
	)

	workflows := map[string]ciWorkflow{
		"main.yml": readCIWorkflow(t, "main.yml"),
		"pr.yml":   readCIWorkflow(t, "pr.yml"),
	}

	prMacOS := workflows["pr.yml"].job(t, "test-macos")
	if prMacOS.RunsOn != macOSRunner {
		t.Errorf("pr macOS test runner = %q, want %q", prMacOS.RunsOn, macOSRunner)
	}
	assertStepRunsExactly(t, prMacOS, "Build", buildCommand)
	assertStepRunsExactly(t, prMacOS, "Test", prTestCommand)
	assertStepsBefore(t, prMacOS, []string{"Build"}, []string{"Test"})
	assertStepEnvValue(t, prMacOS, "Test", "BEADS_TEST_BD_BINARY", workspaceBDBinary)

	mainTest := workflows["main.yml"].job(t, "test")
	assertStepRunsExactly(t, mainTest, "Build", buildCommand)
	assertStepRunsExactly(t, mainTest, "Test", mainTestCommand)
	assertStepsBefore(t, mainTest, []string{"Build"}, []string{"Test"})
	if got := mainTest.step(t, "Build").If; got != "matrix.os != 'ubuntu-latest'" {
		t.Errorf("main build condition = %q, want macOS-only condition", got)
	}
	if got := mainTest.step(t, "Test").If; got != "${{ !matrix.coverage }}" {
		t.Errorf("main test condition = %q, want non-coverage condition", got)
	}
	if got := mainTest.Strategy.Matrix.OS; !equalStrings(got, []string{"ubuntu-latest", macOSRunner}) {
		t.Errorf("main test matrix os = %v, want [ubuntu-latest %s]", got, macOSRunner)
	}
	if got := mainTest.Strategy.Matrix.Include; len(got) != 2 ||
		got[0].OS != "ubuntu-latest" || !got[0].Coverage ||
		got[1].OS != macOSRunner || got[1].Coverage || got[1].TestFlags != "-v -race -short" {
		t.Errorf("main test matrix include = %+v, want macOS non-coverage entry with -v -race -short", got)
	}
	assertStepEnvValue(t, mainTest, "Test", "BEADS_TEST_BD_BINARY", workspaceBDBinary)

	for workflowName, workflow := range workflows {
		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if step.Env["BEADS_TEST_BD_BINARY"] == workspaceBDBinary &&
					!(workflowName == "pr.yml" && jobName == "test-macos" && step.Name == "Test") &&
					!(workflowName == "main.yml" && jobName == "test" && step.Name == "Test") {
					t.Errorf("%s job %q step %q has unexpected workspace bd binary override", workflowName, jobName, step.Name)
				}
			}
		}
	}
}

func TestPRPreflightPlatformsRunsTestScriptPrebuiltBinaryContract(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-preflight-platforms")
	if job.RunsOn != "${{ matrix.os }}" {
		t.Errorf("pr-preflight-platforms runs-on = %q, want matrix.os", job.RunsOn)
	}
	if job.If != "" || job.TimeoutMinutes != 20 {
		t.Errorf("pr-preflight-platforms condition/timeout = %q/%d, want unconditional/20",
			job.If, job.TimeoutMinutes)
	}
	if got := job.Strategy.Matrix.OS; !equalStrings(got, []string{"ubuntu-latest", "macos-latest", "windows-latest"}) {
		t.Errorf("pr-preflight-platforms matrix os = %v, want all three hosted platforms", got)
	}

	const stepName = "Exercise test.sh prebuilt binary path"
	assertStepRunsExactly(t, job, stepName,
		"go test '-tags=gms_pure_go' -count=1 -run '^TestTestScriptPrebuiltBinaryContract$' ./scripts")
	step := job.step(t, stepName)
	if step.Shell != "bash" || step.If != "" || (step.ContinueOnError != nil && step.ContinueOnError != false) {
		t.Errorf("%s shell/condition/continue-on-error = %q/%q/%v, want unconditional required bash",
			stepName, step.Shell, step.If, step.ContinueOnError)
	}

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "pr-preflight-platforms") {
		t.Errorf("ci-gate does not need pr-preflight-platforms: %v", gate.Needs)
	}
	if got, want := gateEnv["PR_PREFLIGHT_PLATFORMS"], "${{ needs.pr-preflight-platforms.result }}"; got != want {
		t.Errorf("ci-gate PR_PREFLIGHT_PLATFORMS = %q, want %q", got, want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "PR_PREFLIGHT_PLATFORMS") {
		t.Errorf("ci-gate required set omits PR_PREFLIGHT_PLATFORMS")
	}
}

func TestRepositoryTextEOLPolicyWorkflow(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "check-doc-freshness-platforms")

	if want := "${{ matrix.os }}"; job.RunsOn != want {
		t.Errorf("check-doc-freshness-platforms runs-on = %q, want %q", job.RunsOn, want)
	}
	wantMatrix := map[string]string{
		"ubuntu-latest":  "linux",
		"macos-latest":   "darwin",
		"windows-latest": "windows",
	}
	if len(job.Strategy.Matrix.OS) != 0 {
		t.Errorf("check-doc-freshness-platforms retains an unbound os-list matrix: %v", job.Strategy.Matrix.OS)
	}
	if got, want := len(job.Strategy.Matrix.Include), len(wantMatrix); got != want {
		t.Fatalf("check-doc-freshness-platforms include tuple count = %d, want %d", got, want)
	}
	seen := make(map[string]bool, len(wantMatrix))
	for _, tuple := range job.Strategy.Matrix.Include {
		wantGOOS, ok := wantMatrix[tuple.OS]
		if !ok {
			t.Errorf("unexpected check-doc-freshness-platforms runner tuple: %+v", tuple)
			continue
		}
		if seen[tuple.OS] {
			t.Errorf("duplicate check-doc-freshness-platforms runner tuple for %q", tuple.OS)
		}
		seen[tuple.OS] = true
		if tuple.ExpectedGOOS != wantGOOS {
			t.Errorf("runner %q expected_goos = %q, want %q", tuple.OS, tuple.ExpectedGOOS, wantGOOS)
		}
		if tuple.Coverage || tuple.TestFlags != "" {
			t.Errorf(
				"runner %q has unexpected shared matrix fields: coverage=%t test-flags=%q",
				tuple.OS,
				tuple.Coverage,
				tuple.TestFlags,
			)
		}
		if len(tuple.Extra) != 0 {
			t.Errorf("runner %q has unexpected matrix fields: %v", tuple.OS, tuple.Extra)
		}
	}

	docStep := job.step(t, "Exercise native date and Bash process boundary")
	const wantDocCommand = "go test '-tags=integration,gms_pure_go' -count=1 -run '^TestDocFreshness' ./scripts"
	if docStep.Run != wantDocCommand {
		t.Errorf("doc-freshness command = %q, want exact original %q", docStep.Run, wantDocCommand)
	}

	eolStep := job.step(t, "Exercise repository text EOL policy boundary")
	const wantEOLCommand = "go test '-tags=integration,gms_pure_go' -count=1 ./scripts/gitattributespolicy -args -required-host -expected-goos '${{ matrix.expected_goos }}'"
	if eolStep.Run != wantEOLCommand {
		t.Errorf("repository EOL command = %q, want %q", eolStep.Run, wantEOLCommand)
	}
	if eolStep.If != "" {
		t.Errorf("repository EOL step has conditional if = %q", eolStep.If)
	}
	if strings.Contains(eolStep.Run, "-run") {
		t.Errorf("repository EOL step may not filter the narrow package: %q", eolStep.Run)
	}
	if job.stepIndex(t, "Exercise native date and Bash process boundary") >=
		job.stepIndex(t, "Exercise repository text EOL policy boundary") {
		t.Error("repository EOL step must remain separate and follow doc freshness")
	}

	gate := workflow.job(t, "ci-gate")
	if !contains(gate.Needs, "check-doc-freshness-platforms") {
		t.Errorf("ci-gate does not need check-doc-freshness-platforms: %v", gate.Needs)
	}
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	const gateKey = "CHECK_DOC_FRESHNESS_PLATFORMS"
	if want := "${{ needs.check-doc-freshness-platforms.result }}"; gateEnv[gateKey] != want {
		t.Errorf("ci-gate env %s = %q, want %q", gateKey, gateEnv[gateKey], want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), gateKey) {
		t.Errorf("ci-gate CI_GATE_REQUIRED does not include %q", gateKey)
	}
}

func TestGoCacheOwnershipTopology(t *testing.T) {
	workflows := map[string]ciWorkflow{
		"main.yml":    readCIWorkflow(t, "main.yml"),
		"pr.yml":      readCIWorkflow(t, "pr.yml"),
		"pr-risk.yml": readCIWorkflow(t, "pr-risk.yml"),
	}

	for workflowName, workflow := range workflows {
		assertPinnedGoCacheActions(t, workflowName, workflow)
	}
	t.Run("monolithic cache action is forbidden", func(t *testing.T) {
		if !isGoCacheActionFamily(cacheMonolithicActionFamily) || !isForbiddenGoCacheActionFamily(cacheMonolithicActionFamily) {
			t.Fatal("actions/cache must be recognized as a forbidden cache action family")
		}
	})

	assertGoCacheInventory(t, workflows["main.yml"].job(t, "build-artifacts"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCache("non-race"), saveModuleCache(), saveBuildCache("non-race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "build-embedded"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCache("race"), saveBuildCache("race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "pr-core-wrapper"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCache("race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "test"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCacheIf("non-race", macOSMatrixCondition), mainRestoreBuildCache("race"),
		saveModuleCacheAfterFailureOnMacOS(), saveBuildCacheAfterFailureOnMacOS("non-race"), saveBuildCacheAfterFailureOnMacOS("race"),
	})
	assertGoCacheInventory(t, workflows["main.yml"].job(t, "test-windows"), []goCacheStep{
		mainRestoreModuleCache(), mainRestoreBuildCache("non-race"), saveModuleCache(), saveBuildCache("non-race"),
	})
	assertConditionalCacheWritersHaveMatrixMember(t, workflows["main.yml"].job(t, "test"), macOSRunner)

	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "build-artifacts"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("non-race"),
	})
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "pr-core-wrapper"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("race"),
	})
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "test-macos"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("non-race"), restoreBuildCache("race"),
	})
	assertGoCacheInventory(t, workflows["pr.yml"].job(t, "worktree-remove-windows"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("non-race"),
	})
	for _, jobName := range []string{"check-doc-freshness-platforms", "pr-preflight-platforms", "build-examples"} {
		assertGoCacheInventory(t, workflows["pr.yml"].job(t, jobName), []goCacheStep{restoreModuleCache()})
	}
	assertGoCacheInventory(t, workflows["pr-risk.yml"].job(t, "build-embedded"), []goCacheStep{
		restoreModuleCache(), restoreBuildCache("race"), restoreBuildCache("non-race"),
	})
	assertNoUnmanagedGoCacheSteps(t, workflows, map[string]map[string]bool{
		"main.yml": {
			"build-artifacts": true, "build-embedded": true, "pr-core-wrapper": true, "test": true, "test-windows": true,
		},
		"pr.yml": {
			"build-artifacts": true, "pr-core-wrapper": true, "test-macos": true, "worktree-remove-windows": true,
			"check-doc-freshness-platforms": true, "pr-preflight-platforms": true, "build-examples": true,
		},
		"pr-risk.yml": {"build-embedded": true},
	})

	mainArtifacts := workflows["main.yml"].job(t, "build-artifacts")
	assertStepsBefore(t, mainArtifacts, []string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Build reusable Linux artifacts"})
	assertStepsBefore(t, mainArtifacts, []string{"Build reusable Linux artifacts", "Upload build artifacts"}, []string{"Save Go module cache", "Save non-race Go build cache"})

	mainEmbedded := workflows["main.yml"].job(t, "build-embedded")
	embeddedRaceBuilds := []string{"Build embedded bd binary", "Build embedded storage test binary", "Build embedded cmd test binary"}
	assertStepsBefore(t, mainEmbedded, []string{"Restore Go module cache", "Restore race Go build cache"}, embeddedRaceBuilds)
	assertStepsBefore(t, mainEmbedded, append(embeddedRaceBuilds, "Upload binaries"), []string{"Save race Go build cache"})

	assertStepsBefore(t, workflows["main.yml"].job(t, "pr-core-wrapper"),
		[]string{"Restore Go module cache", "Restore race Go build cache"}, []string{"Run PR core wrapper"})
	mainTest := workflows["main.yml"].job(t, "test")
	assertStepsBefore(t, mainTest, []string{"Restore Go module cache"}, []string{"Install gotestsum", "Build", "Test (with coverage + JUnit XML)", "Test"})
	assertStepsBefore(t, mainTest, []string{"Restore non-race Go build cache"}, []string{"Build"})
	assertStepsBefore(t, mainTest, []string{"Restore race Go build cache"}, []string{"Test (with coverage + JUnit XML)", "Test"})
	assertStepsBefore(t, mainTest, []string{"Build", "Test"}, []string{"Save Go module cache"})
	assertStepsBefore(t, mainTest, []string{"Build"}, []string{"Save non-race Go build cache"})
	assertStepsBefore(t, mainTest, []string{"Test"}, []string{"Save race Go build cache"})

	mainWindows := workflows["main.yml"].job(t, "test-windows")
	assertStepsBefore(t, mainWindows, []string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Build (pure Go regex)"})
	assertStepsBefore(t, mainWindows, []string{"Build (pure Go regex)", "Smoke test - version", "Smoke test - help"}, []string{"Save Go module cache", "Save non-race Go build cache"})

	assertStepsBefore(t, workflows["pr.yml"].job(t, "build-artifacts"),
		[]string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Build reusable Linux artifacts"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "pr-core-wrapper"),
		[]string{"Restore Go module cache", "Restore race Go build cache"}, []string{"Run PR core wrapper"})
	prMacOS := workflows["pr.yml"].job(t, "test-macos")
	assertStepsBefore(t, prMacOS, []string{"Restore Go module cache"}, []string{"Build", "Test"})
	assertStepsBefore(t, prMacOS, []string{"Restore non-race Go build cache"}, []string{"Build"})
	assertStepsBefore(t, prMacOS, []string{"Restore race Go build cache"}, []string{"Test"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "worktree-remove-windows"),
		[]string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Run native Windows worktree removal boundary tests"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "check-doc-freshness-platforms"),
		[]string{"Restore Go module cache"}, []string{"Exercise native date and Bash process boundary"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "pr-preflight-platforms"),
		[]string{"Restore Go module cache"}, []string{"Exercise the real Bash process boundary", "Exercise test.sh prebuilt binary path"})
	assertStepsBefore(t, workflows["pr.yml"].job(t, "build-examples"),
		[]string{"Restore Go module cache"}, []string{"Type-check every module under examples/"})

	prRiskEmbedded := workflows["pr-risk.yml"].job(t, "build-embedded")
	prRiskNonRaceBuilds := []string{"Build proxied bd subprocess binary", "Build server Dolt conformance test binary"}
	assertStepsBefore(t, prRiskEmbedded, []string{"Restore Go module cache"}, append(append([]string{}, embeddedRaceBuilds...), prRiskNonRaceBuilds...))
	assertStepsBefore(t, prRiskEmbedded, []string{"Restore race Go build cache"}, embeddedRaceBuilds)
	assertStepsBefore(t, prRiskEmbedded, []string{"Restore non-race Go build cache"}, prRiskNonRaceBuilds)

	assertGoCacheEnv(t, workflows["main.yml"].job(t, "build-artifacts"), "Build reusable Linux artifacts", "non-race")
	for _, stepName := range []string{"Build embedded bd binary", "Build embedded storage test binary", "Build embedded cmd test binary"} {
		assertGoCacheEnv(t, workflows["main.yml"].job(t, "build-embedded"), stepName, "race")
	}
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "test"), "Build", "non-race")
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "test"), "Test (with coverage + JUnit XML)", "race")
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "test"), "Test", "race")
	assertGoCacheEnv(t, workflows["main.yml"].job(t, "test-windows"), "Build (pure Go regex)", "non-race")
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "build-artifacts"), "Build reusable Linux artifacts", "non-race")
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "pr-core-wrapper"), "Run PR core wrapper", "race")
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "test-macos"), "Build", "non-race")
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "test-macos"), "Test", "race")
	assertGoCacheEnv(t, workflows["pr.yml"].job(t, "worktree-remove-windows"), "Run native Windows worktree removal boundary tests", "non-race")
	for _, stepName := range []string{"Build embedded bd binary", "Build embedded storage test binary", "Build embedded cmd test binary"} {
		assertGoCacheEnv(t, workflows["pr-risk.yml"].job(t, "build-embedded"), stepName, "race")
	}
	for _, stepName := range []string{"Build proxied bd subprocess binary", "Build server Dolt conformance test binary"} {
		assertGoCacheEnv(t, workflows["pr-risk.yml"].job(t, "build-embedded"), stepName, "non-race")
	}
	for _, workflowName := range []string{"pr.yml", "pr-risk.yml"} {
		for jobName, job := range workflows[workflowName].Jobs {
			for _, step := range job.Steps {
				family := actionFamily(step.Uses)
				if family == cacheSaveActionFamily || isForbiddenGoCacheActionFamily(family) {
					t.Errorf("%s job %q may not save cache in PR workflow", workflowName, jobName)
				}
			}
		}
	}

	assertGoCacheWriter(t, workflows["main.yml"], "build-artifacts", "ubuntu-latest", "Save Go module cache", cacheMissCondition(goModuleCacheRestoreID))
	assertGoCacheWriter(t, workflows["main.yml"], "build-artifacts", "ubuntu-latest", "Save non-race Go build cache", cacheMissCondition(goBuildCacheRestoreID("non-race")))
	assertGoCacheWriter(t, workflows["main.yml"], "build-embedded", "ubuntu-latest", "Save race Go build cache", cacheMissCondition(goBuildCacheRestoreID("race")))
	assertGoCacheWriter(t, workflows["main.yml"], "test", "${{ matrix.os }}", "Save Go module cache", failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goModuleCacheRestoreID)))
	assertGoCacheWriter(t, workflows["main.yml"], "test", "${{ matrix.os }}", "Save non-race Go build cache", failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goBuildCacheRestoreID("non-race"))))
	assertGoCacheWriter(t, workflows["main.yml"], "test", "${{ matrix.os }}", "Save race Go build cache", failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goBuildCacheRestoreID("race"))))
	assertGoCacheWriter(t, workflows["main.yml"], "test-windows", "windows-latest", "Save Go module cache", cacheMissCondition(goModuleCacheRestoreID))
	assertGoCacheWriter(t, workflows["main.yml"], "test-windows", "windows-latest", "Save non-race Go build cache", cacheMissCondition(goBuildCacheRestoreID("non-race")))
	for _, target := range []struct{ workflow, job string }{
		{"main.yml", "build-artifacts"},
		{"main.yml", "build-embedded"},
		{"main.yml", "pr-core-wrapper"},
		{"main.yml", "test"},
		{"main.yml", "test-windows"},
		{"pr.yml", "build-artifacts"},
		{"pr.yml", "pr-core-wrapper"},
		{"pr.yml", "test-macos"},
		{"pr.yml", "worktree-remove-windows"},
		{"pr.yml", "check-doc-freshness-platforms"},
		{"pr.yml", "pr-preflight-platforms"},
		{"pr.yml", "build-examples"},
		{"pr-risk.yml", "build-embedded"},
	} {
		if got := workflows[target.workflow].job(t, target.job).step(t, "Set up Go").ID; got != "setup-go" {
			t.Errorf("%s job %q setup-go id = %q, want setup-go", target.workflow, target.job, got)
		}
	}
}

func assertNoUnmanagedGoCacheSteps(t *testing.T, workflows map[string]ciWorkflow, managed map[string]map[string]bool) {
	t.Helper()

	for workflowName, workflow := range workflows {
		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				family := actionFamily(step.Uses)
				if isForbiddenGoCacheActionFamily(family) {
					t.Errorf("%s job %q has forbidden monolithic cache step %q", workflowName, jobName, step.Name)
					continue
				}
				if isGoCacheActionFamily(family) && !managed[workflowName][jobName] {
					t.Errorf("%s job %q has unmanaged cache step %q", workflowName, jobName, step.Name)
				}
			}
		}
	}
}

const (
	setupGoActionFamily         = "actions/setup-go"
	setupNodeActionFamily       = "actions/setup-node"
	cacheMonolithicActionFamily = "actions/cache"
	cacheRestoreActionFamily    = "actions/cache/restore"
	cacheSaveActionFamily       = "actions/cache/save"
	setupGoSHA                  = "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	setupNodeSHA                = "820762786026740c76f36085b0efc47a31fe5020"
	cacheSHA                    = "55cc8345863c7cc4c66a329aec7e433d2d1c52a9"
	goCacheSchema               = "v2"
	goBaseTag                   = "gms_pure_go"
	goModuleCachePath           = "~/go/pkg/mod"
	goModuleCacheRestoreID      = "restore-go-module-cache"
	macOSRunner                 = "macos-latest"
	macOSMatrixCondition        = "matrix.os == '" + macOSRunner + "'"
)

type goCacheStep struct {
	name        string
	id          string
	family      string
	key         string
	restoreKeys string
	path        string
	ifCondition string
}

func goModuleCacheKey() string {
	return "beads-go-mod-" + goCacheSchema + "-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-${{ hashFiles('go.mod', 'go.sum') }}"
}

func goModuleCacheRestoreKeys() string {
	return "beads-go-mod-" + goCacheSchema + "-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-"
}

func goBuildCachePrefix(profile string) string {
	// The base tag identifies the cache topology. Go's content-addressed
	// cache includes compiler options, so extra tags can safely share it.
	return "beads-go-build-" + goCacheSchema + "-${{ runner.os }}-${{ runner.arch }}-go-${{ steps.setup-go.outputs.go-version }}-base-" + goBaseTag + "-" + profile + "-"
}

func goBuildCacheKey(profile string) string {
	return goBuildCachePrefix(profile) + "${{ github.sha }}"
}

func goBuildCachePath(profile string) string { return "${{ runner.temp }}/go-cache/" + profile }

func goBuildCacheRestoreID(profile string) string {
	return "restore-" + profile + "-go-build-cache"
}

func cacheMissCondition(restoreID string) string {
	return "steps." + restoreID + ".outputs.cache-hit != 'true'"
}

func combineConditions(conditions ...string) string {
	var nonEmpty []string
	for _, condition := range conditions {
		if condition != "" {
			nonEmpty = append(nonEmpty, condition)
		}
	}
	return strings.Join(nonEmpty, " && ")
}

func failureSurvivingCacheSaveCondition(conditions ...string) string {
	return "${{ !cancelled() && " + combineConditions(conditions...) + " }}"
}

func restoreModuleCache() goCacheStep {
	return goCacheStep{name: "Restore Go module cache", family: cacheRestoreActionFamily, key: goModuleCacheKey(), restoreKeys: goModuleCacheRestoreKeys(), path: goModuleCachePath}
}

func mainRestoreModuleCache() goCacheStep {
	step := restoreModuleCache()
	step.id = goModuleCacheRestoreID
	return step
}

func saveModuleCache() goCacheStep { return saveModuleCacheIf("") }

func saveModuleCacheIf(condition string) goCacheStep {
	return goCacheStep{name: "Save Go module cache", family: cacheSaveActionFamily, key: goModuleCacheKey(), path: goModuleCachePath, ifCondition: combineConditions(condition, cacheMissCondition(goModuleCacheRestoreID))}
}

func saveModuleCacheAfterFailureOnMacOS() goCacheStep {
	step := saveModuleCache()
	step.ifCondition = failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goModuleCacheRestoreID))
	return step
}

func restoreBuildCache(profile string) goCacheStep {
	return restoreBuildCacheIf(profile, "")
}

func restoreBuildCacheIf(profile, condition string) goCacheStep {
	return goCacheStep{name: "Restore " + profile + " Go build cache", family: cacheRestoreActionFamily, key: goBuildCacheKey(profile), restoreKeys: goBuildCachePrefix(profile), path: goBuildCachePath(profile), ifCondition: condition}
}

func mainRestoreBuildCache(profile string) goCacheStep {
	return mainRestoreBuildCacheIf(profile, "")
}

func mainRestoreBuildCacheIf(profile, condition string) goCacheStep {
	step := restoreBuildCacheIf(profile, condition)
	step.id = goBuildCacheRestoreID(profile)
	return step
}

func saveBuildCache(profile string) goCacheStep { return saveBuildCacheIf(profile, "") }

func saveBuildCacheIf(profile, condition string) goCacheStep {
	return goCacheStep{name: "Save " + profile + " Go build cache", family: cacheSaveActionFamily, key: goBuildCacheKey(profile), path: goBuildCachePath(profile), ifCondition: combineConditions(condition, cacheMissCondition(goBuildCacheRestoreID(profile)))}
}

func saveBuildCacheAfterFailureOnMacOS(profile string) goCacheStep {
	step := saveBuildCache(profile)
	step.ifCondition = failureSurvivingCacheSaveCondition(macOSMatrixCondition, cacheMissCondition(goBuildCacheRestoreID(profile)))
	return step
}

func assertGoCacheInventory(t *testing.T, job ciWorkflowJob, want []goCacheStep) {
	t.Helper()

	var got []ciWorkflowStep
	for _, step := range job.Steps {
		if isGoCacheActionFamily(actionFamily(step.Uses)) {
			got = append(got, step)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("cache steps = %d, want %d; got %+v", len(got), len(want), got)
	}
	for i, expected := range want {
		step := got[i]
		if step.Name != expected.name || step.ID != expected.id || actionFamily(step.Uses) != expected.family || step.With["key"] != expected.key || step.With["restore-keys"] != expected.restoreKeys || step.With["path"] != expected.path || step.If != expected.ifCondition {
			t.Errorf("cache step %d = {name:%q id:%q family:%q key:%q restore-keys:%q path:%q if:%q}, want {name:%q id:%q family:%q key:%q restore-keys:%q path:%q if:%q}", i, step.Name, step.ID, actionFamily(step.Uses), step.With["key"], step.With["restore-keys"], step.With["path"], step.If, expected.name, expected.id, expected.family, expected.key, expected.restoreKeys, expected.path, expected.ifCondition)
		}
	}
}

func assertConditionalCacheWritersHaveMatrixMember(t *testing.T, job ciWorkflowJob, wantMember string) {
	t.Helper()

	memberCount := 0
	for _, member := range job.Strategy.Matrix.OS {
		if member == wantMember {
			memberCount++
		}
	}
	if memberCount != 1 {
		t.Errorf("strategy matrix os has %d concrete %q members, want exactly 1: %v", memberCount, wantMember, job.Strategy.Matrix.OS)
	}

	conditionalWriters := 0
	wantCondition := "matrix.os == '" + wantMember + "'"
	for _, step := range job.Steps {
		if actionFamily(step.Uses) != cacheSaveActionFamily {
			continue
		}
		if !strings.Contains(step.If, wantCondition) {
			t.Errorf("cache writer %q condition %q does not target matrix member %q", step.Name, step.If, wantMember)
			continue
		}
		conditionalWriters++
	}
	if conditionalWriters == 0 {
		t.Fatal("job has no conditional matrix cache writers")
	}
}

func assertStepsBefore(t *testing.T, job ciWorkflowJob, before, after []string) {
	t.Helper()

	for _, beforeName := range before {
		beforeIndex := job.stepIndex(t, beforeName)
		for _, afterName := range after {
			afterIndex := job.stepIndex(t, afterName)
			if beforeIndex >= afterIndex {
				t.Errorf("step %q index %d must precede %q index %d", beforeName, beforeIndex, afterName, afterIndex)
			}
		}
	}
}

func assertGoCacheEnv(t *testing.T, job ciWorkflowJob, stepName, profile string) {
	t.Helper()
	if got := job.step(t, stepName).Env["GOCACHE"]; got != goBuildCachePath(profile) {
		t.Errorf("step %q GOCACHE = %q, want %q", stepName, got, goBuildCachePath(profile))
	}
}

func assertStepRunsExactly(t *testing.T, job ciWorkflowJob, stepName, want string) {
	t.Helper()
	if got := job.step(t, stepName).Run; got != want {
		t.Errorf("step %q run = %q, want %q", stepName, got, want)
	}
}

func assertStepEnvValue(t *testing.T, job ciWorkflowJob, stepName, key, want string) {
	t.Helper()
	if got := job.step(t, stepName).Env[key]; got != want {
		t.Errorf("step %q env %s = %q, want %q", stepName, key, got, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func assertGoCacheWriter(t *testing.T, workflow ciWorkflow, wantJob, wantRunner, wantStep, wantCondition string) {
	t.Helper()

	var writers []string
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if job.RunsOn == wantRunner && actionFamily(step.Uses) == cacheSaveActionFamily && step.Name == wantStep && step.If == wantCondition {
				writers = append(writers, jobName+"/"+step.Name)
			}
		}
	}
	want := wantJob + "/" + wantStep
	if len(writers) != 1 || writers[0] != want {
		t.Errorf("cache writer %q with if %q = %v, want exactly [%s]", wantStep, wantCondition, writers, want)
	}
}

var actionPin = regexp.MustCompile(`^[0-9a-f]{40}$`)

func actionFamily(uses string) string {
	family, _, found := strings.Cut(uses, "@")
	if !found {
		return uses
	}
	return family
}

func isGoCacheActionFamily(family string) bool {
	return family == cacheMonolithicActionFamily || family == cacheRestoreActionFamily || family == cacheSaveActionFamily
}

func isForbiddenGoCacheActionFamily(family string) bool {
	return family == cacheMonolithicActionFamily
}

func assertPinnedGoCacheActions(t *testing.T, workflowName string, workflow ciWorkflow) {
	t.Helper()

	allowed := map[string]string{
		setupGoActionFamily:      setupGoSHA,
		cacheRestoreActionFamily: cacheSHA,
		cacheSaveActionFamily:    cacheSHA,
	}
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			family := actionFamily(step.Uses)
			wantSHA, managed := allowed[family]
			if isForbiddenGoCacheActionFamily(family) {
				managed = true
			}
			if !managed {
				continue
			}
			_, sha, found := strings.Cut(step.Uses, "@")
			if !found || !actionPin.MatchString(sha) {
				t.Errorf("%s job %q step %q action %q is not pinned to exactly 40 lowercase hex characters", workflowName, jobName, step.Name, step.Uses)
			}
			if isForbiddenGoCacheActionFamily(family) {
				t.Errorf("%s job %q step %q uses forbidden monolithic action %q", workflowName, jobName, step.Name, family)
				continue
			}
			if !found || !actionPin.MatchString(sha) {
				continue
			}
			if sha != wantSHA {
				t.Errorf("%s job %q step %q action %q has SHA %q, want released SHA %q", workflowName, jobName, step.Name, family, sha, wantSHA)
			}
			if family == setupGoActionFamily && step.With["cache"] != "false" {
				t.Errorf("%s job %q setup-go cache = %q, want false", workflowName, jobName, step.With["cache"])
			}
		}
	}
}

type ciWorkflow struct {
	Jobs map[string]ciWorkflowJob `yaml:"jobs"`
}

type ciWorkflowJob struct {
	Needs           ciWorkflowStringList `yaml:"needs"`
	Steps           []ciWorkflowStep     `yaml:"steps"`
	RunsOn          string               `yaml:"runs-on"`
	If              string               `yaml:"if"`
	ContinueOnError bool                 `yaml:"continue-on-error"`
	TimeoutMinutes  int                  `yaml:"timeout-minutes"`
	Strategy        ciWorkflowStrategy   `yaml:"strategy"`
}

type ciWorkflowStrategy struct {
	Matrix ciWorkflowMatrix `yaml:"matrix"`
}

type ciWorkflowMatrix struct {
	OS      []string                  `yaml:"os"`
	Include []ciWorkflowMatrixInclude `yaml:"include"`
}

type ciWorkflowMatrixInclude struct {
	OS           string         `yaml:"os"`
	ExpectedGOOS string         `yaml:"expected_goos"`
	Coverage     bool           `yaml:"coverage"`
	TestFlags    string         `yaml:"test-flags"`
	Extra        map[string]any `yaml:",inline"`
}

type ciWorkflowStep struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	If              string            `yaml:"if"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	Shell           string            `yaml:"shell"`
	ContinueOnError any               `yaml:"continue-on-error"`
	Env             map[string]string `yaml:"env"`
	With            map[string]string `yaml:"with"`
}

type ciWorkflowStringList []string

func (items *ciWorkflowStringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*items = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("needs item must be scalar, got YAML kind %d", item.Kind)
			}
			values = append(values, item.Value)
		}
		*items = values
		return nil
	default:
		return fmt.Errorf("needs must be scalar or sequence, got YAML kind %d", node.Kind)
	}
}

func readCIWorkflow(t *testing.T, name string) ciWorkflow {
	t.Helper()

	path := filepath.Join(sourceRepoRoot(t), ".github", "workflows", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var workflow ciWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return workflow
}

func (workflow ciWorkflow) job(t *testing.T, name string) ciWorkflowJob {
	t.Helper()

	job, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("workflow has no %q job", name)
	}
	return job
}

func (job ciWorkflowJob) step(t *testing.T, name string) ciWorkflowStep {
	t.Helper()

	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("job has no %q step", name)
	return ciWorkflowStep{}
}

func (job ciWorkflowJob) stepIndex(t *testing.T, name string) int {
	t.Helper()

	index := -1
	for i, step := range job.Steps {
		if step.Name != name {
			continue
		}
		if index >= 0 {
			t.Fatalf("job has more than one %q step", name)
		}
		index = i
	}
	if index < 0 {
		t.Fatalf("job has no %q step", name)
	}
	return index
}

func assertJobRunsExactly(t *testing.T, job ciWorkflowJob, want string) {
	t.Helper()

	for _, step := range job.Steps {
		if strings.TrimSpace(step.Run) == want {
			return
		}
	}
	t.Errorf("job has no step that runs exactly %q", want)
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
