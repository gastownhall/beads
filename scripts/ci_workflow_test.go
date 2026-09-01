package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestPRComplexityReportIsAdvisoryAndBestEffort(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "complexity-report")
	if job.RunsOn != "ubuntu-latest" || job.TimeoutMinutes != 0 || job.ContinueOnError {
		t.Errorf("complexity job must have no job timeout/continue-on-error: runs-on=%q timeout=%d continue=%v", job.RunsOn, job.TimeoutMinutes, job.ContinueOnError)
	}
	if contains(job.Needs, "ci-gate") {
		t.Errorf("complexity report unexpectedly depends on ci-gate: %v", job.Needs)
	}
	for _, name := range []string{"Set up Go", "Install gocyclo", "Generate complexity report", "Annotate unavailable complexity report", "Upload complexity report"} {
		step := job.step(t, name)
		if step.TimeoutMinutes <= 0 || step.ContinueOnError != true {
			t.Errorf("complexity step %q is not bounded/best-effort: timeout=%d continue=%v", name, step.TimeoutMinutes, step.ContinueOnError)
		}
	}
	checkout := job.Steps[0]
	if checkout.TimeoutMinutes <= 0 || checkout.ContinueOnError != true {
		t.Errorf("complexity checkout is not bounded/best-effort: timeout=%d continue=%v", checkout.TimeoutMinutes, checkout.ContinueOnError)
	}
	report := job.step(t, "Generate complexity report")
	if report.ID != "generate-complexity" || report.If != "always()" || !strings.Contains(report.Run, "complexity.sh diff") || !strings.Contains(report.Run, "COMPLEXITY_BASE_REF=origin/main") {
		t.Errorf("complexity report step missing diff/always contract: id=%q if=%q run=%q", report.ID, report.If, report.Run)
	}
	annotate := job.step(t, "Annotate unavailable complexity report")
	if annotate.If != "always()" || !strings.Contains(annotate.Run, "::warning") {
		t.Errorf("complexity annotation step missing always/warning contract: if=%q run=%q", annotate.If, annotate.Run)
	}
	gate := workflow.job(t, "ci-gate")
	if contains(gate.Needs, "complexity-report") {
		t.Errorf("ci-gate must not require advisory complexity report: %v", gate.Needs)
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
		// The macOS leg is the only consumer of main.yml's matrix test-flags (the
		// ubuntu leg's coverage step hardcodes its own), and it carries an explicit
		// per-package -timeout because go test's 10m default is what made the ubuntu
		// leg flaky (wy-5b5fbl). Keep the two legs' deadlines in step when either moves.
		mainMacOSTestFlags = "-v -race -short -timeout=25m"
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
		got[1].OS != macOSRunner || got[1].Coverage || got[1].TestFlags != mainMacOSTestFlags {
		t.Errorf("main test matrix include = %+v, want macOS non-coverage entry with %s", got, mainMacOSTestFlags)
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

// TestPRRiskGateReachesFullServerDoltStorageSuite is the regression test for
// be-aiy5: test-server-storage already keeps a live Dolt server up (via
// build-embedded's /tmp/dolt-conformance-test binary, which compiles every
// top-level test in package internal/storage/dolt, not just conformance),
// but restricts execution to -test.run '^TestConformance$'. Every other
// server-gated test in that same binary -- TestCreateGuard_*,
// TestFederationPeerCredentialLifecycleLazyKeyInit, and diff-owned tests
// such as TestBenchDBPurgeDoesNotLeak on PR #5792 -- is reached by no
// PR-triggered lane, so it silently SKIPs instead of producing a real
// PASS/FAIL. A sibling job must run everything else in the same binary
// against the same live server, without a new build step, and must be wired
// into ci-gate as required so a SKIP there can no longer hide behind green.
func TestPRRiskGateReachesFullServerDoltStorageSuite(t *testing.T) {
	const jobName = "test-server-storage-full"

	workflow := readCIWorkflow(t, "pr-risk.yml")
	job := workflow.job(t, jobName)

	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("%s runs-on = %q, want ubuntu-latest", jobName, job.RunsOn)
	}
	if job.TimeoutMinutes != 20 {
		t.Errorf("%s timeout = %d minutes, want 20 (matches test-server-storage)", jobName, job.TimeoutMinutes)
	}
	if !contains(job.Needs, "detect-ci-tier") || !contains(job.Needs, "build-embedded") {
		t.Errorf("%s needs = %v, want detect-ci-tier and build-embedded (reuse the existing artifact, no new build)", jobName, job.Needs)
	}
	if job.If != "needs.detect-ci-tier.outputs.full_embedded == 'true'" {
		t.Errorf("%s if = %q, want the same tier gate as test-server-storage", jobName, job.If)
	}

	download := job.step(t, "Download binaries")
	if download.With["name"] != "embedded-test-binaries" {
		t.Errorf("%s does not download the existing embedded-test-binaries artifact (would require a new build step): %v", jobName, download.With)
	}

	// Pro-rata against the embedded lane (75 shard-minutes / 324 tests):
	// 1126 tests at the same per-test cost is ~260 shard-minutes; 16 shards
	// x 15m = 240 shard-minutes, with the ~9.5-minute TestCloudAuthCLIRouting
	// outlier isolated on its own shard via the shard manifest.
	const totalShards = 16
	wantShards := make([]int, totalShards)
	for i := range wantShards {
		wantShards[i] = i + 1
	}
	if !reflect.DeepEqual(job.Strategy.Matrix.Shard, wantShards) {
		t.Errorf("%s strategy.matrix.shard = %v, want %v", jobName, job.Strategy.Matrix.Shard, wantShards)
	}
	if job.Strategy.FailFast {
		t.Errorf("%s strategy.fail-fast = true, want false (one slow/flaky shard should not cancel the others)", jobName)
	}

	wantTestCommand := fmt.Sprintf("bash .github/scripts/server-storage-test-shard.sh ${{ matrix.shard }} %d", totalShards)
	assertStepRunsExactly(t, job, "Test", wantTestCommand)
	// federation_test.go Fatals instead of silently skipping when this is set
	// and the server it expects isn't reachable -- this job's whole point is
	// a real PASS/FAIL, not a hidden self-skip if its own setup regresses.
	assertStepEnvValue(t, job, "Test", "BEADS_TEST_ENV_RUN_DOLT", "1")

	// test-server-storage itself is untouched: conformance keeps its own
	// dedicated job and timeout budget; this is an additive sibling, not a
	// widened filter on the existing job.
	existing := workflow.job(t, "test-server-storage")
	assertStepRunsExactly(t, existing, "Test", `/tmp/dolt-conformance-test -test.v -test.count=1 -test.timeout=15m -test.run '^TestConformance$'`)

	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	const gateKey = "TEST_SERVER_STORAGE_FULL"
	if !contains(gate.Needs, jobName) {
		t.Errorf("ci-gate does not need %q: %v", jobName, gate.Needs)
	}
	if got, want := gateEnv[gateKey], fmt.Sprintf("${{ needs.%s.result }}", jobName); got != want {
		t.Errorf("ci-gate env %s = %q, want %q", gateKey, got, want)
	}
	if !contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), gateKey) {
		t.Errorf("ci-gate CI_GATE_REQUIRED does not include %q", gateKey)
	}
}

func TestServerStorageShardScriptRunsPrebuiltBinaryFromPackageDir(t *testing.T) {
	// pr4107_corruption_test.go and journal_scope_completeness_test.go use
	// paths relative to internal/storage/dolt (e.g. ../schema/migrations,
	// ../issueops). `go test` runs a package's tests with cwd = the package
	// dir, so those resolve; a prebuilt test binary inherits the invoking
	// shell's cwd instead. server-storage-test-shard.sh is invoked from the
	// repo root (see TestPRRiskGateReachesFullServerDoltStorageSuite above),
	// so the prebuilt-binary branch must cd into the package dir itself,
	// immediately before exec -- any earlier and it breaks the repo-root-
	// relative manifest/discovery above it; any later, or absent, and the 6
	// relative-path tests fail with "open ../issueops: no such file or
	// directory".
	path := filepath.Join(sourceRepoRoot(t), ".github", "scripts", "server-storage-test-shard.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")

	indexOfContains := func(want string) int {
		for i, line := range lines {
			if strings.Contains(line, want) {
				return i
			}
		}
		return -1
	}
	indexOfExact := func(want string) int {
		for i, line := range lines {
			if strings.TrimSpace(line) == want {
				return i
			}
		}
		return -1
	}

	discoveryIndex := indexOfContains(`grep -rh '^func Test' internal/storage/dolt/*_test.go`)
	if discoveryIndex < 0 {
		t.Fatal("could not find repo-root-relative test-discovery grep line")
	}
	prebuiltBranchIndex := indexOfContains(`if [ -x "$STORAGE_BINARY" ]; then`)
	if prebuiltBranchIndex < 0 {
		t.Fatal("could not find prebuilt-binary branch")
	}
	execIndex := indexOfContains(`exec "$STORAGE_BINARY"`)
	if execIndex < 0 {
		t.Fatal("could not find prebuilt-binary exec line")
	}
	fallbackExecIndex := indexOfContains(`exec go test`)
	if fallbackExecIndex < 0 {
		t.Fatal("could not find go-test fallback exec line")
	}
	if fallbackExecIndex < execIndex {
		t.Fatal("go-test fallback exec line appears before the prebuilt-binary exec line -- branch order assumption violated")
	}

	cdIndex := indexOfExact("cd internal/storage/dolt")
	if cdIndex < 0 {
		t.Fatal(`script does not "cd internal/storage/dolt" before running the prebuilt binary -- ` +
			`relative-path tests (../schema/migrations, ../issueops) will fail when this script ` +
			`is invoked from the repo root, as pr-risk.yml's test-server-storage-full job does`)
	}
	if cdIndex <= discoveryIndex {
		t.Fatalf("cd internal/storage/dolt at line %d is at or before the repo-root-relative test "+
			"discovery grep at line %d -- that discovery must still run from the repo root",
			cdIndex+1, discoveryIndex+1)
	}
	if cdIndex <= prebuiltBranchIndex || cdIndex >= execIndex {
		t.Fatalf("cd internal/storage/dolt at line %d must sit strictly between the prebuilt-binary "+
			"branch at line %d and its exec at line %d", cdIndex+1, prebuiltBranchIndex+1, execIndex+1)
	}

	// The go-test fallback already scopes via the ./internal/storage/dolt/
	// argument (cwd = repo root is fine for `go test`); it must not also cd.
	for i := execIndex + 1; i <= fallbackExecIndex; i++ {
		if strings.TrimSpace(lines[i]) == "cd internal/storage/dolt" {
			t.Fatalf("unexpected cd internal/storage/dolt at line %d in the go-test fallback branch -- "+
				"it already scopes via the ./internal/storage/dolt/ argument", i+1)
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
	FailFast bool             `yaml:"fail-fast"`
	Matrix   ciWorkflowMatrix `yaml:"matrix"`
}

type ciWorkflowMatrix struct {
	OS      []string                  `yaml:"os"`
	Shard   []int                     `yaml:"shard"`
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
	TimeoutMinutes  int               `yaml:"timeout-minutes"`
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

// TestWorkflowsInstallPinnedDolt keeps the Dolt CLI under test pinned. Every
// workflow used to install it by piping dolthub/dolt's releases/latest
// install.sh, so the binary under test changed whenever upstream published —
// including backports, which can move "latest" backwards. When Dolt 2.3.0
// landed it regressed CALL DOLT_RESET('--hard'): roughly one freshly created
// database in twenty comes up with the procedure permanently broken
// ("Error 1105 (HY000): context canceled"), which made
// TestFreshBootstrapHealIncarnation fail on a coin flip. See
// scripts/ci/install-dolt.sh for the per-version measurements. The CLI is now
// pinned to the same release as the container image, so the two halves of
// every server-mode test (the per-test sql-server doltserver.Start launches,
// and the shared container) can never drift apart.
func TestWorkflowsInstallPinnedDolt(t *testing.T) {
	workflowDir := filepath.Join(sourceRepoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatal(err)
	}

	installers := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}
		path := filepath.Join(workflowDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		if strings.Contains(body, "dolt/releases/latest") {
			t.Errorf("%s installs dolt from releases/latest; use ./scripts/ci/install-dolt.sh so the "+
				"binary under test is pinned", entry.Name())
		}
		installers += strings.Count(body, "scripts/ci/install-dolt.sh")
	}
	if installers == 0 {
		t.Fatal("no workflow installs dolt via scripts/ci/install-dolt.sh — the pin is not wired up")
	}
}

// TestPinnedDoltCLIMatchesContainerImage keeps the CLI pin and the sql-server
// container pin on the same Dolt release. Server-mode tests run both at once
// against the same databases; a drifting pair tests a combination no release
// ever shipped.
func TestPinnedDoltCLIMatchesContainerImage(t *testing.T) {
	root := sourceRepoRoot(t)

	installer, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "install-dolt.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cliVersion := captureOne(t, `(?m)^readonly version="([0-9]+\.[0-9]+\.[0-9]+)"$`, string(installer), "scripts/ci/install-dolt.sh")

	common, err := os.ReadFile(filepath.Join(root, "internal", "testutil", "testdoltcommon.go"))
	if err != nil {
		t.Fatal(err)
	}
	imageVersion := captureOne(t, `dolthub/dolt-sql-server:([0-9]+\.[0-9]+\.[0-9]+)`, string(common), "testdoltcommon.go:DoltDockerImage")

	pullScript, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "pull-dolt-image.sh"))
	if err != nil {
		t.Fatal(err)
	}
	pullVersion := captureOne(t, `dolthub/dolt-sql-server:([0-9]+\.[0-9]+\.[0-9]+)`, string(pullScript), "scripts/ci/pull-dolt-image.sh")

	if cliVersion != imageVersion || cliVersion != pullVersion {
		t.Errorf("dolt pins disagree: CLI %s, DoltDockerImage %s, pull-dolt-image.sh %s",
			cliVersion, imageVersion, pullVersion)
	}
}

func captureOne(t *testing.T, pattern, body, source string) string {
	t.Helper()

	matches := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if matches == nil {
		t.Fatalf("%s does not match %s", source, pattern)
	}
	return matches[1]
}
