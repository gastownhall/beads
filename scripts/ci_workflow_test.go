package scripts_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
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

func TestPRCoreRequiresExcludeReadPermissionCoverage(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-core-wrapper")
	if job.RunsOn != "ubuntu-latest" || job.If != "" || job.ContinueOnError {
		t.Error("exclude permission coverage must remain in the required Linux PR Core job")
	}
	step := job.Steps[job.stepIndex(t, "Run PR core wrapper")]
	if step.If != "" || (step.ContinueOnError != nil && step.ContinueOnError != false) || strings.TrimSpace(step.Run) != "make ci-pr-core" {
		t.Error("exclude permission coverage must run through the nonoptional PR Core wrapper")
	}
	if step.Env["BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION"] != "1" {
		t.Error("PR Core must require actual exclude read-permission coverage")
	}
	gate := workflow.job(t, "ci-gate")
	evaluate := gate.step(t, "Evaluate CI gate")
	if gate.If != "${{ always() }}" || gate.ContinueOnError || evaluate.If != "" || (evaluate.ContinueOnError != nil && evaluate.ContinueOnError != false) {
		t.Error("CI gate must propagate required PR Core failures")
	}
	if !contains(gate.Needs, "pr-core-wrapper") || evaluate.Env["PR_CORE_WRAPPER"] != "${{ needs.pr-core-wrapper.result }}" || !contains(strings.Fields(evaluate.Env["CI_GATE_REQUIRED"]), "PR_CORE_WRAPPER") {
		t.Error("CI gate must require the PR Core result")
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

func TestPRWorkflowExercisesNativeUserConfigDiagnostics(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	job := workflow.job(t, "pr-preflight-platforms")
	// The benchmark-environment test owns the shared job and matrix shape.
	wantHosts := map[string]string{"ubuntu-latest": "linux", "macos-latest": "darwin", "windows-latest": "windows"}
	gotHosts := make(map[string][]string)
	for _, tuple := range job.Strategy.Matrix.Include {
		gotHosts[tuple.OS] = append(gotHosts[tuple.OS], tuple.ExpectedGOOS)
	}
	for host, want := range wantHosts {
		if got := gotHosts[host]; len(got) != 1 || got[0] != want {
			t.Fatalf("native host %s = %v, want exactly one %s", host, got, want)
		}
	}
	step := job.step(t, "Check native user config diagnostics")
	if step.If != "" || step.Shell != "bash" || step.Env["CGO_ENABLED"] != "0" ||
		(step.ContinueOnError != nil && step.ContinueOnError != false) {
		t.Fatalf("native diagnostic step is conditional, optional, or uses the wrong host: %+v", step)
	}
	assertStepRunsExactly(t, job, step.Name, "bash scripts/ci/test-user-config-diagnostic.sh '${{ matrix.expected_goos }}'")
	assertStepsBefore(t, job, []string{"Set up Go", "Restore Go module cache"}, []string{step.Name})
	gate := workflow.job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	if !contains(gate.Needs, "pr-preflight-platforms") ||
		gateEnv["PR_PREFLIGHT_PLATFORMS"] != "${{ needs.pr-preflight-platforms.result }}" ||
		!contains(strings.Fields(gateEnv["CI_GATE_REQUIRED"]), "PR_PREFLIGHT_PLATFORMS") {
		t.Fatal("native diagnostic platform results must feed the required aggregate gate")
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

func TestPRWorkflowExercisesWindowsEnvironmentHelpers(t *testing.T) {
	workflow := readCIWorkflow(t, "pr.yml")
	// The benchmark test owns this shared job's matrix and CI Gate propagation.
	job := workflow.job(t, "pr-preflight-platforms")
	step := job.step(t, "Check shared environment key semantics")
	if step.If != "matrix.os == 'windows-latest'" || step.Shell != "bash" {
		t.Errorf("environment helpers need native Windows Bash: if=%q shell=%q", step.If, step.Shell)
	}
	if step.ContinueOnError != nil && step.ContinueOnError != false {
		t.Error("environment helper step may not continue on error")
	}
	if got := strings.TrimSpace(step.Run); got != "bash scripts/ci/test-windows-env-helpers.sh" {
		t.Errorf("environment helper entrypoint = %q", got)
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

	// pr.yml's job is the one lane with both the image and the pinned dolt
	// CLI, so it is where the container and local test servers are compared.
	job := readCIWorkflow(t, "pr.yml").job(t, "test-domain-uow")
	const fingerprintStep = "Test Dolt server fingerprint (container + local)"
	assertStepRunsExactly(t, job, fingerprintStep,
		"go test -tags gms_pure_go -count=1 -timeout 5m -v -run '^TestDoltServerFingerprint$' ./internal/testutil/")
	assertStepEnvValue(t, job, fingerprintStep, "BEADS_TEST_REQUIRE_DOLT_CONTAINER", "1")

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
		// -timeout=30m is pinned on both lanes because ./cmd/bd has outgrown
		// `go test`'s 10m per-package default (#6091, and wy-5b5fbl before it —
		// that default is what made these legs flaky). In main.yml it sits on
		// the invocation rather than in matrix.test-flags, so editing the
		// matrix cannot silently drop it, and so the macOS leg cannot drift
		// away from the ubuntu -race lanes' deadline.
		prTestCommand   = "go test -tags gms_pure_go -v -race -short -timeout=30m -skip '^TestEmbedded' ./..."
		mainTestCommand = "go test -tags gms_pure_go ${{ matrix.test-flags }} -timeout=30m -skip '^TestEmbedded' ./..."
		// The macOS leg is the only consumer of main.yml's matrix test-flags
		// (the ubuntu leg's coverage step hardcodes its own). The deadline is
		// deliberately NOT here — see mainTestCommand.
		mainMacOSTestFlags = "-v -race -short"
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
	Env             map[string]string    `yaml:"env"`
	Outputs         map[string]string    `yaml:"outputs"`
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

// TestPinnedDoltCLIMatchesContainerImage keeps the CLI pin, the sql-server
// container pin and the hermetic Bazel dolt (tools/bazel/dolt.bzl) on the same
// Dolt release. Server-mode tests run both at once
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

	bazelRule, err := os.ReadFile(filepath.Join(root, "tools", "bazel", "dolt.bzl"))
	if err != nil {
		t.Fatal(err)
	}
	bazelVersion := captureOne(t, `(?m)^DOLT_VERSION = "([0-9]+\.[0-9]+\.[0-9]+)"$`, string(bazelRule), "tools/bazel/dolt.bzl:DOLT_VERSION")

	if cliVersion != imageVersion || cliVersion != pullVersion || cliVersion != bazelVersion {
		t.Errorf("dolt pins disagree: CLI %s, DoltDockerImage %s, pull-dolt-image.sh %s, tools/bazel/dolt.bzl %s",
			cliVersion, imageVersion, pullVersion, bazelVersion)
	}
}

// TestProxiedLocalSmokeMatchesPinnedDoltVersion keeps the proxied-local-smoke
// lane's standalone Dolt CLI install on the same release as the rest of the
// suite. That lane downloads its own dolt binary straight from GitHub
// releases instead of going through scripts/ci/install-dolt.sh, so nothing
// else catches it drifting off the measured pin (see "Which Dolt version to
// install" in docs/architecture/dolt.md for why the pin is not just "latest").
func TestProxiedLocalSmokeMatchesPinnedDoltVersion(t *testing.T) {
	root := sourceRepoRoot(t)

	installer, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "install-dolt.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cliVersion := captureOne(t, `(?m)^readonly version="([0-9]+\.[0-9]+\.[0-9]+)"$`, string(installer), "scripts/ci/install-dolt.sh")

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "proxied-local-smoke.yml"))
	if err != nil {
		t.Fatal(err)
	}
	smokeVersion := captureOne(t, `(?m)^\s*DOLT_VERSION:\s*([0-9]+\.[0-9]+\.[0-9]+)\s*$`, string(workflow), "proxied-local-smoke.yml:DOLT_VERSION")

	if cliVersion != smokeVersion {
		t.Errorf("dolt pins disagree: CLI %s, proxied-local-smoke.yml DOLT_VERSION %s", cliVersion, smokeVersion)
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

// --- Bazel lane (.github/workflows/bazel.yml, advisory) ----------------------

const (
	bazelWorkflowName   = "bazel.yml"
	bazelJobName        = "bazel-test"
	bazelPureJobName    = "bazel-pure"
	bazelDoltJobName    = "bazel-doltserver"
	bazelEmbedJobName   = "bazel-embedded"
	bazelRBEJobName     = "rbe"
	bazelIntegJobName   = "bazel-integration"
	setupBazelActionDir = ".github/actions/setup-bazel"
	uploadArtifactSHA   = "043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"
	downloadArtifactSHA = "3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c"
	checkoutSHA         = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	bazelCacheKeyPrefix = "bazel-repo-v2-${{ runner.os }}-"
	bazelCacheKey       = bazelCacheKeyPrefix + "${{ hashFiles('.bazelversion', 'MODULE.bazel.lock') }}"
	bazelCachePath      = "${{ runner.temp }}/bazel-ci-cache"
	// Save only from a push to main that missed the exact key: the content is
	// fixed by the key, so re-saving every push only churns the quota.
	bazelCacheSaveIf = "${{ always() && github.event_name == 'push' && github.ref == 'refs/heads/main' && " +
		"steps.bazel.outcome == 'success' && steps.bazel.outputs.cache-hit != 'true' }}"
)

// bazel.yml's jobs: the rbe job that decides the execution mode, the
// --config=ci lane, and one job per CI job a Bazel config mirrors.
var bazelJobNames = []string{bazelDoltJobName, bazelEmbedJobName, bazelIntegJobName, bazelPureJobName, bazelJobName, bazelRBEJobName}

// The lanes that only run remotely (skipped unless the rbe job chose remote);
// every other lane also runs locally.
var bazelRemoteOnlyJobs = map[string]bool{bazelEmbedJobName: true, bazelIntegJobName: true}

// The rbe job's decision step reads exactly one secret, and only to test it
// for emptiness: its env value is a boolean, not the secret.
const (
	bazelRBESecretPath  = ".jobs." + bazelRBEJobName + ".steps[0].env.HAS_EXECUTOR"
	bazelRBESecretValue = "${{ secrets.RBE_WEST_EXECUTOR != '' }}"
)

// The only triggers bazel.yml may have. pull_request_target (and
// workflow_run) would run with secrets in the context of fork PRs.
var bazelWorkflowTriggers = []string{"merge_group", "pull_request", "push", "workflow_call", "workflow_dispatch"}

type ciCompositeAction struct {
	Runs struct {
		Using string           `yaml:"using"`
		Steps []ciWorkflowStep `yaml:"steps"`
	} `yaml:"runs"`
}

func readSetupBazelAction(t *testing.T) ciCompositeAction {
	t.Helper()
	path := filepath.Join(sourceRepoRoot(t), setupBazelActionDir, "action.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var action ciCompositeAction
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if action.Runs.Using != "composite" {
		t.Fatalf("%s runs.using = %q, want composite", path, action.Runs.Using)
	}
	return action
}

// The Bazel lane is advisory until the equivalence record and a human promote
// it: no ci-gate may need it or list it in CI_GATE_REQUIRED.
func TestBazelWorkflowIsAdvisory(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	var names []string
	for name, job := range workflow.Jobs {
		names = append(names, name)
		if job.ContinueOnError {
			t.Errorf("%s continue-on-error hides failures; the lane is advisory by staying out of ci-gate", name)
		}
		if job.TimeoutMinutes == 0 {
			t.Errorf("%s has no timeout-minutes", name)
		}
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, bazelJobNames) {
		t.Errorf("%s jobs = %v, want %v", bazelWorkflowName, names, bazelJobNames)
	}
	// Every lane needs the rbe job and takes its runner, skip rule and
	// setup-bazel env from that job's outputs alone. Remote runs use the
	// Blacksmith pool (the farm admits nothing else); local runs (forks,
	// rbe=off, secret-less Dependabot runs) the GitHub-hosted runner.
	const wantRunsOn = "${{ needs.rbe.outputs.enabled == 'true' && 'blacksmith-2vcpu-ubuntu-2404' || 'ubuntu-latest' }}"
	// Local-capable lanes skip only in mode skip (same-repo, RBE_WEST_WORKERS
	// unset); remote-only lanes (27 race processes and more, an hour or more
	// on a GitHub-hosted runner, for tests the Go jobs already run there)
	// skip unless remote.
	const wantIf = "${{ needs.rbe.outputs.mode != 'skip' }}"
	const wantRemoteOnlyIf = "${{ needs.rbe.outputs.enabled == 'true' }}"
	gate := "needs.rbe.outputs.enabled == 'true' && "
	wantSetupEnv := map[string]string{
		"BAZEL_REMOTE_EXECUTOR": "${{ " + gate + "secrets.RBE_WEST_EXECUTOR || '' }}",
		"RBE_TLS_CERT":          "${{ " + gate + "secrets.RBE_TLS_CERT || '' }}",
		"RBE_TLS_KEY":           "${{ " + gate + "secrets.RBE_TLS_KEY || '' }}",
		"RBE_TLS_CA":            "${{ " + gate + "secrets.RBE_TLS_CA || '' }}",
		"RBE_INSTANCE":          "${{ " + gate + "'oss' || '' }}",
	}
	for name, job := range workflow.Jobs {
		if name == bazelRBEJobName {
			continue
		}
		if !reflect.DeepEqual([]string(job.Needs), []string{bazelRBEJobName}) {
			t.Errorf("%s needs = %v, want [%s]", name, job.Needs, bazelRBEJobName)
		}
		if job.RunsOn != wantRunsOn {
			t.Errorf("%s runs-on = %q, want %q", name, job.RunsOn, wantRunsOn)
		}
		want := wantIf
		if bazelRemoteOnlyJobs[name] {
			want = wantRemoteOnlyIf
		}
		if job.If != want {
			t.Errorf("%s if = %q, want %q", name, job.If, want)
		}
		for _, step := range job.Steps {
			if step.Uses == "./"+setupBazelActionDir && !reflect.DeepEqual(step.Env, wantSetupEnv) {
				t.Errorf("%s setup-bazel env = %v, want %v", name, step.Env, wantSetupEnv)
			}
		}
	}
	for _, name := range []string{"pr.yml", "pr-risk.yml"} {
		for jobName, j := range readCIWorkflow(t, name).Jobs {
			for _, need := range j.Needs {
				if strings.Contains(strings.ToLower(need), "bazel") {
					t.Errorf("%s job %q needs %q; the Bazel lane is advisory", name, jobName, need)
				}
			}
			for _, step := range j.Steps {
				for key, value := range step.Env {
					if strings.Contains(strings.ToUpper(key), "BAZEL") ||
						(key == "CI_GATE_REQUIRED" && strings.Contains(strings.ToUpper(value), "BAZEL")) {
						t.Errorf("%s job %q step %q env %s=%q wires the Bazel lane into a gate", name, jobName, step.Name, key, value)
					}
				}
			}
		}
	}
}

// Every action in the Bazel lane is pinned to a full commit SHA, with the same
// released SHAs the Go lanes use, and the monolithic actions/cache is banned
// here too (restore everywhere, save only where the topology says).
func TestBazelWorkflowActionsArePinned(t *testing.T) {
	steps := map[string][]ciWorkflowStep{
		setupBazelActionDir + "/action.yml": readSetupBazelAction(t).Runs.Steps,
	}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		steps[bazelWorkflowName+" "+name] = job.Steps
	}
	want := map[string]string{
		setupNodeActionFamily:       setupNodeSHA,
		"actions/checkout":          checkoutSHA,
		setupGoActionFamily:         setupGoSHA,
		cacheRestoreActionFamily:    cacheSHA,
		cacheSaveActionFamily:       cacheSHA,
		"actions/upload-artifact":   uploadArtifactSHA,
		"actions/download-artifact": downloadArtifactSHA,
	}
	for file, list := range steps {
		for _, step := range list {
			if step.Uses == "" || strings.HasPrefix(step.Uses, "./") {
				continue
			}
			family, sha, found := strings.Cut(step.Uses, "@")
			if !found || !actionPin.MatchString(sha) {
				t.Errorf("%s step %q action %q is not pinned to a 40-hex commit SHA", file, step.Name, step.Uses)
				continue
			}
			if isForbiddenGoCacheActionFamily(family) {
				t.Errorf("%s step %q uses forbidden monolithic %q; use actions/cache/restore and /save", file, step.Name, family)
				continue
			}
			wantSHA, known := want[family]
			if !known {
				t.Errorf("%s step %q uses %q, which this policy does not know; add its released SHA here", file, step.Name, family)
			} else if sha != wantSHA {
				t.Errorf("%s step %q action %q has SHA %q, want %q", file, step.Name, family, sha, wantSHA)
			}
			if family == setupGoActionFamily && step.With["cache"] != "false" {
				t.Errorf("%s setup-go cache = %q, want false", file, step.With["cache"])
			}
			if family == cacheSaveActionFamily && file != bazelWorkflowName+" "+bazelJobName {
				t.Errorf("%s step %q saves a cache; only %s's push-to-main step may", file, step.Name, bazelJobName)
			}
		}
		for _, step := range list {
			if strings.Contains(step.Run, "apt-get") {
				t.Errorf("%s step %q runs apt-get; bound it (see ci_workflow_apt_get_timeout_test.go) or drop it", file, step.Name)
			}
		}
	}
}

// The runner cache holds only content fixed by .bazelversion and
// MODULE.bazel.lock (repository cache, Bazelisk), so it is keyed on exactly
// that and written only by a push to main that missed the key; it never holds
// the generated rc or credentials.
func TestBazelWorkflowCacheTopology(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	var saves []ciWorkflowStep
	for _, step := range job.Steps {
		if actionFamily(step.Uses) == cacheSaveActionFamily {
			saves = append(saves, step)
		}
	}
	if len(saves) != 1 {
		t.Fatalf("%s has %d cache save steps, want 1", bazelWorkflowName, len(saves))
	}
	save := saves[0]
	if save.If != bazelCacheSaveIf {
		t.Errorf("cache save if = %q, want exactly %q", save.If, bazelCacheSaveIf)
	}
	if save.With["key"] != bazelCacheKey || save.With["path"] != bazelCachePath {
		t.Errorf("cache save key/path = %q / %q, want %q / %q", save.With["key"], save.With["path"], bazelCacheKey, bazelCachePath)
	}

	var restore, writer ciWorkflowStep
	for _, step := range readSetupBazelAction(t).Runs.Steps {
		switch {
		case actionFamily(step.Uses) == cacheRestoreActionFamily:
			restore = step
		case step.Env["BAZEL_CI_SECRET_DIR"] != "":
			writer = step
		}
	}
	if restore.With["path"] != bazelCachePath || restore.With["key"] != bazelCacheKey ||
		strings.TrimSpace(restore.With["restore-keys"]) != bazelCacheKeyPrefix {
		t.Errorf("setup-bazel restore path/key/restore-keys = %q / %q / %q; want the save step's path and key with a %q prefix fallback",
			restore.With["path"], restore.With["key"], restore.With["restore-keys"], bazelCacheKeyPrefix)
	}
	if writer.Env["BAZEL_CI_CACHE_DIR"] != bazelCachePath {
		t.Errorf("rc writer BAZEL_CI_CACHE_DIR = %q, want %q", writer.Env["BAZEL_CI_CACHE_DIR"], bazelCachePath)
	}
	if secret := writer.Env["BAZEL_CI_SECRET_DIR"]; !strings.HasPrefix(secret, "${{ runner.temp }}/") || strings.HasPrefix(secret, bazelCachePath) {
		t.Errorf("rc writer BAZEL_CI_SECRET_DIR = %q; want a runner.temp directory outside the cached %q", secret, bazelCachePath)
	}
}

// The lane runs the committed ci config over //..., reports the critical path
// and the go test equivalence even when tests fail, catches BUILD drift, and
// hands secrets to nothing but setup-bazel.
func TestBazelWorkflowRunsCIConfigWithReports(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	if job.TimeoutMinutes == 0 {
		t.Errorf("%s has no timeout-minutes", bazelJobName)
	}
	testStep := job.step(t, "bazel test //... --config=ci")
	for _, required := range []string{"bazel test //... --config=ci", "--profile=", "--build_event_json_file=", "set -o pipefail"} {
		if !strings.Contains(testStep.Run, required) {
			t.Errorf("test step does not contain %q:\n%s", required, testStep.Run)
		}
	}
	if strings.Contains(testStep.Run, "--config=remote-exec") {
		t.Errorf("test step selects remote-exec itself; setup-bazel's rc does that only when secrets are present")
	}
	if i, j := strings.Index(testStep.Run, "bazel test"), strings.Index(testStep.Run, "--profile="); j < i {
		t.Errorf("--profile is a command option and must follow `bazel test`")
	}
	for name, script := range map[string]string{
		"Critical-path report": "tools/bazel/critpath.py",
		"Go test equivalence":  "tools/bazel/equivalence.py",
	} {
		step := job.step(t, name)
		if !strings.Contains(step.Run, script) || !strings.Contains(step.If, "always()") {
			t.Errorf("step %q must run %s under always(); run=%q if=%q", name, script, step.Run, step.If)
		}
		assertReportStepKeepsExitStatus(t, step, "python3 "+script)
	}
	assertTestStepKeepsExitStatus(t, testStep)
	sync := job.step(t, "BUILD files in sync (gazelle, go_srcs, MODULE.bazel)")
	for _, required := range []string{"make bazel-sync-check", "make bazel-sync\n", "git status --porcelain --untracked-files=all", "exit 1"} {
		if !strings.Contains(sync.Run, required) {
			t.Errorf("sync step does not contain %q:\n%s", required, sync.Run)
		}
	}
}

// bazel.yml publishes a Bazel-built bd under pr.yml's build-artifacts contract
// (artifact name, file names, checksum file, retention), so the jobs that
// download ci-build-artifacts can switch to it without other edits.
func TestBazelWorkflowPublishesBuildArtifacts(t *testing.T) {
	var prUpload ciWorkflowStep
	for _, step := range readCIWorkflow(t, "pr.yml").job(t, "build-artifacts").Steps {
		if actionFamily(step.Uses) == "actions/upload-artifact" {
			prUpload = step
		}
	}
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	pkg := job.step(t, "Package bd (ci-build-artifacts layout)")
	upload := job.step(t, "Upload build artifacts")
	if prUpload.With["name"] != "ci-build-artifacts" || upload.With["name"] != prUpload.With["name"] {
		t.Errorf("artifact name = %q, want pr.yml's %q (ci-build-artifacts)", upload.With["name"], prUpload.With["name"])
	}
	for _, key := range []string{"retention-days", "if-no-files-found"} {
		if upload.With[key] != prUpload.With[key] {
			t.Errorf("upload %s = %q, want pr.yml's %q", key, upload.With[key], prUpload.With[key])
		}
	}
	if pkg.ID == "" || upload.If != "${{ always() && steps."+pkg.ID+".outcome == 'success' }}" {
		t.Errorf("upload if = %q; want it gated on the package step's success", upload.If)
	}
	// The commands live in a script that sources .buildflags itself, so the
	// build-tag scan (scripts/check-build-tags.sh) still covers bazel.yml.
	const script = "scripts/ci/package-bazel-bd.sh"
	if pkg.Run != "./"+script+` "$RUNNER_TEMP/bd-artifacts"` {
		t.Errorf("package step = %q, want %s", pkg.Run, script)
	}
	if strings.Contains(readPolicyFile(t, bazelPolicyRoot(t), ".github/workflows/"+bazelWorkflowName), ".buildflags") {
		t.Errorf("%s mentions .buildflags; sourcing it would exempt the whole file from the build-tag scan", bazelWorkflowName)
	}
	body := readPolicyFile(t, bazelPolicyRoot(t), script)
	for _, required := range []string{
		"set -euo pipefail", "source ./.buildflags",
		"/bin/cmd/bd/bd_for_tests/bd", "bd-linux-gms-pure", "sha256sum bd-linux-gms-pure > SHA256SUMS",
		"build-manifest.txt", "commit=", "go_version=", "build_tags=", "artifact=bd-linux-gms-pure",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("%s does not contain %q", script, required)
		}
	}
	// test:ci must download the binary under --remote_download_minimal.
	if !strings.Contains(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "test:ci --remote_download_regex=.*/bin/cmd/bd/bd_for_tests/bd$") {
		t.Error(".bazelrc test:ci does not download //cmd/bd:bd_for_tests")
	}
}

// bazel-doltserver replaces pr.yml's container-backed jobs: --config=doltserver
// (hermetic dolt sql-servers, remote-executable) by default, and a dolt-server
// target in every package those jobs run. --config=docker stays reachable as
// the A/B control (dispatch dolt-lane=docker), with the jobs' image pull.
func TestBazelDoltJobMirrorsContainerJobs(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelDoltJobName)
	pull := job.step(t, "Pull Dolt sql-server image")
	for _, name := range []string{"test-domain-uow", "contract-corpus"} {
		if want := pr.job(t, name).step(t, "Pull Dolt sql-server image").Run; pull.Run != want {
			t.Errorf("%s pulls the dolt image differently from %s (%q)", bazelDoltJobName, name, want)
		}
	}
	if pull.If != "${{ env.BAZEL_DOLT_LANE == 'docker' }}" {
		t.Errorf("%s pulls the dolt image with if %q; only the docker lane needs it", bazelDoltJobName, pull.If)
	}
	if got := job.Env["BAZEL_DOLT_LANE"]; got != "${{ inputs.dolt-lane || 'doltserver' }}" {
		t.Errorf("%s BAZEL_DOLT_LANE = %q, want the doltserver lane unless dispatched otherwise", bazelDoltJobName, got)
	}
	test := job.step(t, "bazel test //... --config=doltserver")
	if !strings.Contains(test.Run, `bazel test //... "--config=$BAZEL_DOLT_LANE"`) || !strings.Contains(test.Run, "set -o pipefail") {
		t.Errorf("dolt lane step does not run the lane over //...:\n%s", test.Run)
	}
	if strings.Contains(test.Run, "--config=remote-exec") {
		t.Errorf("dolt lane step selects remote-exec itself; setup-bazel's rc does that only when secrets are present")
	}
	assertTestStepKeepsExitStatus(t, test)

	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	for _, want := range []string{"test:doltserver --test_tag_filters=dolt-server", "test:docker --test_tag_filters=requires-docker"} {
		if !strings.Contains(rc, want+"\n") {
			t.Errorf(".bazelrc lacks %q", want)
		}
	}

	// The packages those jobs run (test-domain-uow: domain/..., uow,
	// tracker/... and doctor/fix; contract-corpus: protocol) each need a
	// dolt-server target. Every such target, checked on its own, picks the
	// local backend and fails closed, so a broken backend fails rather than
	// skipping into a cached pass; the ones whose TestMain owns a server also
	// set the package's own REQUIRE switch. Only under go test: scripts_test's
	// runfiles hold no other package's BUILD.
	if os.Getenv("TEST_SRCDIR") != "" {
		return
	}
	root := sourceRepoRoot(t)
	for pkg, docker := range map[string]bool{
		"internal/storage/domain":      false,
		"internal/storage/domain/db":   true,
		"internal/storage/domain/fs":   false,
		"internal/storage/domain/git":  false,
		"internal/storage/uow":         true,
		"internal/tracker":             true,
		"internal/tracker/conformance": false,
		"cmd/bd/doctor/fix":            true,
		"cmd/bd/protocol":              true,
	} {
		build := readPolicyFile(t, root, pkg+"/BUILD.bazel")
		for _, err := range checkDoltServerRules(pkg, build) {
			t.Error(err)
		}
		if docker && !strings.Contains(build, `"requires-docker"`) {
			t.Errorf("%s/BUILD.bazel has no requires-docker variant for the docker A/B lane", pkg)
		}
	}
}

// doltServerRuleEnv is the rule env every dolt-server target must set, and
// doltServerExtraEnv what particular targets need on top: the TestMain
// switches that turn a server which cannot start into a failure.
var (
	doltServerRuleEnv  = []string{`"BEADS_TEST_DOLT_SERVER": "local"`, `"BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1"`}
	doltServerExtraEnv = map[string][]string{
		"fix_dolt_test":      {`"BEADS_FIX_REQUIRE_DOLT": "1"`},
		"protocol_dolt_test": {`"BEADS_PROTOCOL_REQUIRE_DOLT": "1"`},
	}
	bazelRuleNameRe = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
)

// bazelTopRules splits a BUILD file into its top-level calls: each starts at
// a line matching bazelTopRuleRe and ends at the next line that is just ")".
func bazelTopRules(build string) []string {
	var rules []string
	var cur []string
	in := false
	for _, line := range strings.Split(build, "\n") {
		if !in && bazelTopRuleRe.MatchString(line) {
			in, cur = true, nil
		}
		if in {
			cur = append(cur, line)
			if line == ")" || (len(cur) == 1 && strings.HasSuffix(strings.TrimSpace(line), ")")) {
				rules = append(rules, strings.Join(cur, "\n"))
				in = false
			}
		}
	}
	return rules
}

// checkDoltServerRules checks each rule tagged dolt-server in one package's
// BUILD file on its own (a file-wide search would accept another target's
// env), and that the package has at least one.
func checkDoltServerRules(pkg, build string) []error {
	var errs []error
	found := 0
	for _, rule := range bazelTopRules(stripStarlarkComments(build)) {
		if !strings.Contains(rule, `"dolt-server"`) {
			continue
		}
		found++
		name := "?"
		if m := bazelRuleNameRe.FindStringSubmatch(rule); m != nil {
			name = m[1]
		}
		where := "//" + pkg + ":" + name
		for _, env := range append(append([]string{}, doltServerRuleEnv...), doltServerExtraEnv[name]...) {
			if !strings.Contains(rule, env) {
				errs = append(errs, errors.New(where+" (dolt-server) lacks rule env "+env))
			}
		}
		if strings.Contains(rule, `"no-remote-exec"`) {
			errs = append(errs, errors.New(where+" (dolt-server) is tagged no-remote-exec; the lane runs on remote workers"))
		}
	}
	if found == 0 {
		errs = append(errs, errors.New(pkg+"/BUILD.bazel has no dolt-server target for the dolt-server lane"))
	}
	return errs
}

// A dolt-server rule is checked on its own: another target in the same file
// (the docker variant) carrying the env must not cover for it.
func TestCheckDoltServerRulesPerTarget(t *testing.T) {
	const docker = `sh_test(
    name = "uow_docker_test",
    env = {
        "BEADS_TEST_DOLT_SERVER": "container",
        "BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1",
    },
    tags = ["requires-docker", "no-remote-exec"],
)
`
	good := `load("@rules_shell//shell:sh_test.bzl", "sh_test")

` + docker + `
# Tags:
#   dolt-server: lane.
sh_test(
    name = "uow_dolt_test",
    env = {
        "BEADS_TEST_DOLT_SERVER": "local",
        "BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1",
    },
    tags = ["dolt-server"],
)
`
	if errs := checkDoltServerRules("p", good); len(errs) != 0 {
		t.Errorf("good BUILD: %v", errs)
	}
	for name, bad := range map[string]string{
		"no require":      strings.Replace(good, "        \"BEADS_TEST_REQUIRE_DOLT_CONTAINER\": \"1\",\n    },\n    tags = [\"dolt-server\"]", "    },\n    tags = [\"dolt-server\"]", 1),
		"container":       strings.Replace(good, `"local"`, `"container"`, 1),
		"no-remote-exec":  strings.Replace(good, `tags = ["dolt-server"]`, `tags = ["dolt-server", "no-remote-exec"]`, 1),
		"no target":       docker,
		"fix switch":      strings.Replace(good, `"uow_dolt_test"`, `"fix_dolt_test"`, 1),
		"protocol switch": strings.Replace(good, `"uow_dolt_test"`, `"protocol_dolt_test"`, 1),
	} {
		if bad == good {
			t.Fatalf("%s: mutation did not apply", name)
		}
		if errs := checkDoltServerRules("p", bad); len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The default shard manifest of a PR Risk shard script.
var shardManifestDefault = regexp.MustCompile(`\$\{BEADS_TEST_SHARD_MANIFEST:-([^}]+)\}`)

// bazelAttrBlock returns the text of a list attribute (`    name = [` up to
// its closing `    ],`) of a rule block from bazelRuleBlock, or "".
func bazelAttrBlock(rule, name string) string {
	i := strings.Index(rule, "\n    "+name+" = [")
	if i < 0 {
		return ""
	}
	end := strings.Index(rule[i:], "\n    ],\n")
	if end < 0 {
		return rule[i:]
	}
	return rule[i : i+end+7]
}

// bazelRuleBlock returns the text of the top-level rule named name in a
// BUILD file, or "" if there is none.
func bazelRuleBlock(build, name string) string {
	i := strings.Index(build, "\n    name = \""+name+"\",\n")
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(build[:i], "\n") + 1
	end := strings.Index(build[i:], "\n)\n")
	if end < 0 {
		return build[start:]
	}
	return build[start : i+end+3]
}

// bazel-integration mirrors main.yml's integration jobs with the unmodified
// --config=integration (whose .bazelrc filter and flags
// TestBazelrcIntegrationLane pins): no extra filter, selector or remote
// config on the command line, a BEP for the test-count check, and no
// equivalence step. ci and integration share bazel-testlogs (review F3), so
// neither the integration lane in bazel-test nor --config=ci here.
func TestBazelIntegrationJob(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelIntegJobName)
	if job.TimeoutMinutes > 45 {
		t.Errorf("%s timeout-minutes = %d; it runs remotely only, keep it near the step's 30", bazelIntegJobName, job.TimeoutMinutes)
	}
	test := job.step(t, "bazel test //... --config=integration")
	if test.TimeoutMinutes == 0 || test.TimeoutMinutes >= job.TimeoutMinutes {
		t.Errorf("%s test step timeout-minutes = %d, want set and below the job's %d", bazelIntegJobName, test.TimeoutMinutes, job.TimeoutMinutes)
	}
	cmd := regexp.MustCompile(`\s*\\\n\s*`).ReplaceAllString(test.Run, " ")
	const wantCmd = `bazel test //... --config=integration --build_event_json_file="$RUNNER_TEMP/bazel-bep.json" 2>&1 | tee "$RUNNER_TEMP/bazel-test.log" || rc=$?`
	if !strings.Contains(cmd, wantCmd) || !strings.Contains(test.Run, "set -o pipefail") {
		t.Errorf("%s test step does not run exactly %q:\n%s", bazelIntegJobName, wantCmd, test.Run)
	}
	if n := strings.Count(test.Run, "bazel test //"); n != 1 {
		t.Errorf("%s test step runs bazel test %d times, want 1", bazelIntegJobName, n)
	}
	assertTestStepKeepsExitStatus(t, test)
	count := job.step(t, "Every target and shard ran tests")
	const wantCount = `python3 tools/bazel/check_testcases.py --bep "$RUNNER_TEMP/bazel-bep.json" --not-go //tools/bazel:dolt_version_test`
	if strings.TrimSpace(count.Run) != wantCount ||
		count.If != "${{ always() && steps.test.outcome != 'skipped' }}" ||
		(count.ContinueOnError != nil && count.ContinueOnError != false) {
		t.Errorf("test-count step: if=%q continue-on-error=%v run=%q; want run %q", count.If, count.ContinueOnError, count.Run, wantCount)
	}
	logs := job.step(t, "Upload test logs")
	if logs.If != "${{ failure() && steps.test.outcome != 'skipped' }}" || logs.With["name"] != "bazel-integration-testlogs" {
		t.Errorf("test-log upload: if=%q name=%q", logs.If, logs.With["name"])
	}
	for name, j := range workflow.Jobs {
		for _, step := range j.Steps {
			integ := strings.Contains(step.Run, "--config=integration")
			if name == bazelIntegJobName && (strings.Contains(step.Run, "--config=ci") || strings.Contains(step.Run, "equivalence.py")) {
				t.Errorf("%s step %q runs the ci lane or equivalence.py; they would read each other's bazel-testlogs", name, step.Name)
			}
			if name != bazelIntegJobName && integ {
				t.Errorf("%s step %q runs --config=integration; only %s may (shared bazel-testlogs)", name, step.Name, bazelIntegJobName)
			}
		}
	}
	if err := checkBazelrcIntegrationLane(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")); err != nil {
		t.Error(err)
	}
}

// bazel-embedded replaces pr-risk.yml's embedded-Dolt tier: --config=embedded
// runs the embedded variants race like the jobs' binaries, with the jobs'
// parallelism; each shard variant runs its job's shard script with the job's
// shard total, and the conformance variants pass the jobs' exact selectors.
func TestBazelEmbeddedJobMirrorsEmbeddedTier(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelEmbedJobName)
	test := job.step(t, "bazel test //... --config=embedded")
	if !strings.Contains(test.Run, "bazel test //... --config=embedded") || !strings.Contains(test.Run, "set -o pipefail") {
		t.Errorf("embedded step does not run --config=embedded over //...:\n%s", test.Run)
	}
	if strings.Contains(test.Run, "--config=remote-exec") {
		t.Errorf("embedded step selects remote-exec itself; setup-bazel's rc does that only when secrets are present")
	}
	assertTestStepKeepsExitStatus(t, test)
	// A shard with no tests assigned, or a selector that matches nothing,
	// exits 0; the job fails on any target or shard whose test.xml lists none.
	if !strings.Contains(test.Run, `--build_event_json_file="$RUNNER_TEMP/bazel-bep.json"`) {
		t.Errorf("embedded step writes no BEP for the test-count check:\n%s", test.Run)
	}
	count := job.step(t, "Every target and shard ran tests")
	if strings.TrimSpace(count.Run) != `python3 tools/bazel/check_testcases.py --bep "$RUNNER_TEMP/bazel-bep.json"` ||
		count.If != "${{ always() && steps.test.outcome != 'skipped' }}" ||
		(count.ContinueOnError != nil && count.ContinueOnError != false) {
		t.Errorf("test-count step: if=%q continue-on-error=%v run=%q", count.If, count.ContinueOnError, count.Run)
	}

	rc := map[string]bool{}
	for _, line := range strings.Split(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "\n") {
		rc[strings.TrimSpace(line)] = true
	}
	for _, want := range []string{
		"test:embedded --@rules_go//go/config:race",
		"test:embedded --test_tag_filters=embedded",
		// The jobs pass no -parallel: GOMAXPROCS on 4-vCPU ubuntu-latest.
		"test:embedded --test_arg=-test.parallel=4",
	} {
		if !rc[want] {
			t.Errorf(".bazelrc lacks %q", want)
		}
	}
	for line := range rc {
		if strings.HasPrefix(line, "test:embedded ") && (strings.Contains(line, "-test.short") || strings.Contains(line, "BEADS_TEST_SKIP")) {
			t.Errorf(".bazelrc %q: the embedded jobs run without -short and BEADS_TEST_SKIP", line)
		}
	}

	risk := readCIWorkflow(t, "pr-risk.yml")
	build := risk.job(t, "build-embedded")
	for step, want := range map[string]string{
		"Build embedded bd binary":           "go build -tags gms_pure_go -race -o /tmp/bd-embedded-test ./cmd/bd/",
		"Build embedded storage test binary": "go test -tags gms_pure_go -race -c -o /tmp/embeddeddolt-test ./internal/storage/embeddeddolt/",
		"Build embedded cmd test binary":     "go test -tags gms_pure_go -race -c -o /tmp/bd-cmd-test ./cmd/bd/",
	} {
		if got := strings.TrimSpace(build.step(t, step).Run); got != want {
			t.Errorf("pr-risk.yml %q = %q, want %q (the race build --config=embedded mirrors)", step, got, want)
		}
	}

	sharded := []struct {
		job, script, pkg, target string
	}{
		{"test-embedded-cmd", ".github/scripts/embedded-test-shard.sh", "cmd/bd", "bd_embedded_test"},
		{"test-embedded-storage", ".github/scripts/embedded-storage-test-shard.sh", "internal/storage/embeddeddolt", "embeddeddolt_embedded_test"},
	}
	for _, c := range sharded {
		j := risk.job(t, c.job)
		shards := len(j.Strategy.Matrix.Shard)
		step := j.step(t, "Test")
		if want := "bash " + c.script + " ${{ matrix.shard }} " + strconv.Itoa(shards); strings.TrimSpace(step.Run) != want {
			t.Errorf("pr-risk.yml %s runs %q, want %q", c.job, step.Run, want)
		}
		if step.Env["BEADS_TEST_EMBEDDED_DOLT"] != "1" {
			t.Errorf("pr-risk.yml %s no longer sets BEADS_TEST_EMBEDDED_DOLT=1; update %s", c.job, c.target)
		}
		if os.Getenv("TEST_SRCDIR") != "" {
			continue // scripts_test's runfiles hold no other package's BUILD
		}
		root := sourceRepoRoot(t)
		rule := bazelRuleBlock(readPolicyFile(t, root, c.pkg+"/BUILD.bazel"), c.target)
		for _, want := range []string{
			`srcs = ["//tools/bazel:go_test_manifest_shard.sh"],`,
			`"$(rootpath //:` + c.script + `)",`,
			"shard_count = " + strconv.Itoa(shards) + ",",
			`"BEADS_TEST_EMBEDDED_DOLT": "1"`,
			`"embedded"`,
		} {
			if !strings.Contains(rule, want) {
				t.Errorf("%s:%s does not contain %q (pr-risk.yml %s):\n%s", c.pkg, c.target, want, c.job, rule)
			}
		}
		// The script and its manifest must be in the runfiles: without the
		// manifest the script silently falls back to hash assignment, so
		// every shard still passes but no longer runs its job's tests.
		m := shardManifestDefault.FindStringSubmatch(readPolicyFile(t, root, c.script))
		if m == nil {
			t.Fatalf("%s has no ${BEADS_TEST_SHARD_MANIFEST:-...} default manifest", c.script)
		}
		data := bazelAttrBlock(rule, "data")
		for _, file := range []string{c.script, m[1]} {
			if !strings.Contains(data, `"//:`+file+`",`) {
				t.Errorf("%s:%s data lacks //:%s:\n%s", c.pkg, c.target, file, data)
			}
		}
	}
	if os.Getenv("TEST_SRCDIR") == "" {
		// The cmd jobs' subprocess bd is the race build, as //cmd/bd:bd is
		// under --config=embedded (bd_for_tests never is).
		rule := bazelRuleBlock(readPolicyFile(t, sourceRepoRoot(t), "cmd/bd/BUILD.bazel"), "bd_embedded_test")
		if !strings.Contains(rule, `"BEADS_TEST_BD_BINARY": "$(rlocationpath :bd)"`) {
			t.Errorf("cmd/bd:bd_embedded_test must run the race //cmd/bd:bd as BEADS_TEST_BD_BINARY:\n%s", rule)
		}
		// The script's -test.timeout=20m equals Bazel's 1200s action limit,
		// which kills without a goroutine dump; 19m lets Go's fire first.
		if !strings.Contains(bazelAttrBlock(rule, "args"), `"-test.timeout=19m",`) {
			t.Errorf("cmd/bd:bd_embedded_test must pass -test.timeout=19m after the script's own:\n%s", rule)
		}
	}

	conformance := risk.job(t, "test-embedded-conformance")
	if conformance.Env["BEADS_TEST_EMBEDDED_DOLT"] != "1" {
		t.Error("pr-risk.yml test-embedded-conformance no longer sets BEADS_TEST_EMBEDDED_DOLT=1")
	}
	quoted := regexp.MustCompile(`(-test\.[a-z]+) '([^']*)'|(-test\.[a-z]+=\S+|-test\.v)`)
	for partition, target := range map[string]string{"core": "embeddeddolt_conformance_core_test", "audit": "embeddeddolt_conformance_audit_test"} {
		run := conformance.step(t, "Test "+partition+" conformance").Run
		fields := strings.Fields(run)
		if len(fields) == 0 || fields[0] != "/tmp/embeddeddolt-test" {
			t.Fatalf("pr-risk.yml %s conformance no longer runs /tmp/embeddeddolt-test: %q", partition, run)
		}
		var want []string
		for _, m := range quoted.FindAllStringSubmatch(run, -1) {
			switch {
			case m[1] != "":
				want = append(want, `"`+m[1]+"="+strings.ReplaceAll(m[2], "$", "$$")+`",`)
			case strings.HasPrefix(m[3], "-test.timeout="):
				// Documented deviation: Bazel kills at 1200s without a
				// goroutine dump, so the variant's Go timeout is 19m.
				if m[3] != "-test.timeout=30m" {
					t.Errorf("pr-risk.yml %s conformance timeout is now %q; revisit the variant's -test.timeout=19m", partition, m[3])
				}
				want = append(want, `"-test.timeout=19m",`)
			default:
				want = append(want, `"`+m[3]+`",`)
			}
		}
		if len(want) < 4 {
			t.Fatalf("parsed only %v from pr-risk.yml %s conformance %q", want, partition, run)
		}
		if os.Getenv("TEST_SRCDIR") != "" {
			continue
		}
		rule := bazelRuleBlock(readPolicyFile(t, sourceRepoRoot(t), "internal/storage/embeddeddolt/BUILD.bazel"), target)
		for _, w := range append(want, `"BEADS_TEST_EMBEDDED_DOLT": "1"`, `"embedded"`) {
			if !strings.Contains(rule, w) {
				t.Errorf("embeddeddolt:%s does not contain %q (pr-risk.yml %s conformance):\n%s", target, w, partition, rule)
			}
		}
	}
}

// bazel-pure replaces pr.yml's check-cmd-bd-puregeo-tests job: the same pure
// cmd/bd test selector, the same pure build set, and the js/wasm hook test
// under the same exact-count guard.
func TestBazelPureJobMirrorsPureGoJob(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml").job(t, "check-cmd-bd-puregeo-tests")
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelPureJobName)

	prSubset := pr.step(t, "Run pure-Go cmd/bd test subset (CGO_ENABLED=0)").Run
	m := regexp.MustCompile(`-run '([^']+)' \./cmd/bd`).FindStringSubmatch(prSubset)
	if m == nil {
		t.Fatalf("cannot find the -run selector in pr.yml's pure subset step:\n%s", prSubset)
	}
	if job.Env["PURE_CMD_BD_TESTS"] != m[1] || !strings.Contains(prSubset, "-short") {
		t.Errorf("%s PURE_CMD_BD_TESTS = %q, want pr.yml's -short -run selector %q", bazelPureJobName, job.Env["PURE_CMD_BD_TESTS"], m[1])
	}
	run := job.step(t, "Run pure-Go cmd/bd test subset (--config=pure)").Run
	for _, required := range []string{"bazel test --config=pure //cmd/bd:bd_test", `"--test_arg=-test.run=$PURE_CMD_BD_TESTS"`, "(( n > 0 ))"} {
		if !strings.Contains(run, required) {
			t.Errorf("pure subset step does not contain %q:\n%s", required, run)
		}
	}
	if !strings.Contains(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "test:pure --test_arg=-test.short") {
		t.Error(".bazelrc test:pure does not pass -test.short like the pr.yml subset")
	}

	build := job.step(t, "Build cmd/bd and pure-Go test binaries (--config=pure)").Run
	prBuild := pr.step(t, "Build cmd/bd (CGO_ENABLED=0, gms_pure_go)").Run + pr.step(t, "Compile pure-Go test binaries (CGO_ENABLED=0, gms_pure_go)").Run
	for pkg, target := range map[string]string{
		"go build -tags gms_pure_go -o /tmp/bd-puregeo ./cmd/bd":               "//cmd/bd:bd",
		"-o /tmp/bd-cmd-puregeo-test ./cmd/bd":                                 "//cmd/bd:bd_test",
		"-o /tmp/bd-embeddeddolt-puregeo-test ./internal/storage/embeddeddolt": "//internal/storage/embeddeddolt:embeddeddolt_test",
		"-o /tmp/bd-tracker-puregeo-test ./internal/tracker":                   "//internal/tracker:tracker_test",
	} {
		if !strings.Contains(prBuild, pkg) {
			t.Errorf("pr.yml pure build steps no longer contain %q; update %s to match", pkg, bazelPureJobName)
		}
		if !strings.Contains(build, target) || !strings.Contains(build, "bazel build --config=pure") {
			t.Errorf("pure build step does not build %s:\n%s", target, build)
		}
	}

	// `go build` rejects a pure binary that imports gozstd at compile time;
	// Bazel compiles gozstd's stubs, whose init panics, so the lane must start
	// every pure artifact (bd_test starts in the subset step above).
	start := job.step(t, "Start every pure-Go artifact (gozstd contamination check)").Run
	for _, required := range []string{
		"set -euo pipefail",
		"bazel run --config=pure //cmd/bd:bd -- version",
		"bazel test --config=pure",
		"//internal/storage/embeddeddolt:embeddeddolt_test",
		"//internal/tracker:tracker_test",
		"'--test_arg=-test.run=^$'",
	} {
		if !strings.Contains(start, required) {
			t.Errorf("pure artifact start step does not contain %q:\n%s", required, start)
		}
	}
	if os.Getenv("TEST_SRCDIR") == "" {
		patch := readPolicyFile(t, sourceRepoRoot(t), "third_party/patches/gozstd_nocgo.patch")
		if !strings.Contains(patch, "+func init() { panic(") {
			t.Error("gozstd_nocgo.patch stubs no longer panic in init; a contaminated pure binary would start")
		}
	}

	prWasm := pr.step(t, "Run js/wasm hook boundary").Run
	wasm := job.step(t, "Run js/wasm hook boundary").Run
	for _, required := range []string{"bazel build --config=js-wasm //internal/hooks:hooks_test", "go_js_wasm_exec"} {
		if !strings.Contains(wasm, required) {
			t.Errorf("js/wasm step does not contain %q:\n%s", required, wasm)
		}
	}
	// The selector and everything from the count guard on are pr.yml's.
	const selector = "run '^TestRunHookReportsUnsupportedExecution$'"
	if !strings.Contains(prWasm, " -"+selector) || !strings.Contains(wasm, " -test."+selector) {
		t.Errorf("js/wasm selector drifted from pr.yml's %q", selector)
	}
	guard := func(script string) string {
		i := strings.Index(script, "run_count=0")
		if i < 0 {
			return ""
		}
		return script[i:]
	}
	if guard(wasm) == "" || guard(wasm) != guard(prWasm) {
		t.Errorf("js/wasm count guard differs from pr.yml's:\n--- pr.yml\n%s\n--- %s\n%s", guard(prWasm), bazelPureJobName, guard(wasm))
	}
}

// swallowedExit matches shell that hides a failing command's status.
var swallowedExit = regexp.MustCompile(`\bexit\b|\|\|\s*(true|:)|;\s*(true|:)\s*$|\bset\s+\+e\b|\btrap\b`)

// shellCommands joins backslash continuations and drops blank and comment
// lines.
func shellCommands(run string) []string {
	var cmds []string
	cur := ""
	for _, line := range strings.Split(run, "\n") {
		trimmed := strings.TrimSpace(line)
		if cur == "" && (trimmed == "" || strings.HasPrefix(trimmed, "#")) {
			continue
		}
		if strings.HasSuffix(trimmed, "\\") {
			cur += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		cmds = append(cmds, strings.Join(strings.Fields(cur+trimmed), " "))
		cur = ""
	}
	if cur != "" {
		cmds = append(cmds, strings.Join(strings.Fields(cur), " "))
	}
	return cmds
}

// A report step's status is its script's: the script is the last command,
// nothing after it on that line, and nothing in the step can mask a failure.
func assertReportStepKeepsExitStatus(t *testing.T, step ciWorkflowStep, invocation string) {
	t.Helper()
	if step.Shell != "" {
		t.Errorf("step %q overrides shell %q; the default bash -e is part of the contract", step.Name, step.Shell)
	}
	cmds := shellCommands(step.Run)
	if len(cmds) == 0 {
		t.Errorf("step %q runs nothing", step.Name)
		return
	}
	last := cmds[len(cmds)-1]
	if !strings.HasPrefix(last, invocation+" ") || strings.ContainsAny(last, ";&|") {
		t.Errorf("step %q must end with a bare %q so its exit status is the step's; last command %q", step.Name, invocation, last)
	}
	for _, cmd := range cmds {
		if swallowedExit.MatchString(cmd) {
			t.Errorf("step %q command %q can hide the script's exit status", step.Name, cmd)
		}
	}
}

// The bazel test step captures bazel's status through tee and must exit
// with it.
func assertTestStepKeepsExitStatus(t *testing.T, step ciWorkflowStep) {
	t.Helper()
	if step.Shell != "" {
		t.Errorf("step %q overrides shell %q", step.Name, step.Shell)
	}
	cmds := shellCommands(step.Run)
	if len(cmds) == 0 || cmds[len(cmds)-1] != `exit "$rc"` {
		t.Errorf("step %q must end with exit \"$rc\"; commands %q", step.Name, cmds)
	}
	exits := 0
	for _, cmd := range cmds {
		if regexp.MustCompile(`(^|[;&|]\s*)exit\b`).MatchString(cmd) {
			exits++
		}
		if regexp.MustCompile(`\|\|\s*(true|:)|;\s*(true|:)\s*$|\bset\s+\+e\b|\btrap\b`).MatchString(cmd) {
			t.Errorf("step %q command %q can hide bazel's exit status", step.Name, cmd)
		}
	}
	if exits != 1 {
		t.Errorf("step %q has %d exit commands, want only the final exit \"$rc\"", step.Name, exits)
	}
}

func readYAMLNode(t *testing.T, rel string) *yaml.Node {
	t.Helper()
	path := filepath.Join(sourceRepoRoot(t), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("%s: want a YAML mapping document", path)
	}
	return doc.Content[0]
}

// walkYAML calls fn with the dotted path of every mapping key and scalar.
func walkYAML(node *yaml.Node, path string, fn func(path string, key bool, value string)) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			k := node.Content[i].Value
			fn(path+"."+k, true, k)
			walkYAML(node.Content[i+1], path+"."+k, fn)
		}
	case yaml.SequenceNode:
		for i, item := range node.Content {
			walkYAML(item, fmt.Sprintf("%s[%d]", path, i), fn)
		}
	case yaml.ScalarNode:
		fn(path, false, node.Value)
	case yaml.AliasNode:
		if node.Alias != nil {
			walkYAML(node.Alias, path, fn)
		}
	}
}

func yamlMapKeys(node *yaml.Node, key string) []string {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != key {
			continue
		}
		v := node.Content[i+1]
		switch v.Kind {
		case yaml.MappingNode:
			var keys []string
			for j := 0; j < len(v.Content); j += 2 {
				keys = append(keys, v.Content[j].Value)
			}
			return keys
		case yaml.SequenceNode:
			var keys []string
			for _, item := range v.Content {
				keys = append(keys, item.Value)
			}
			return keys
		default:
			return []string{v.Value}
		}
	}
	return nil
}

// bazel.yml hands the RBE credentials to PR-head code on same-repo PRs (an
// accepted, documented risk). Keep that exposure from growing: only the
// listed triggers (never pull_request_target), secrets referenced from the
// setup-bazel step's env and nowhere else (not workflow/job env, run, with or
// if), and no continue-on-error anywhere, so a red lane stays red.
func TestBazelWorkflowSecretsAndFailureSurface(t *testing.T) {
	root := readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName))
	triggers := yamlMapKeys(root, "on")
	sort.Strings(triggers)
	if !reflect.DeepEqual(triggers, bazelWorkflowTriggers) {
		t.Errorf("%s triggers = %v, want exactly %v", bazelWorkflowName, triggers, bazelWorkflowTriggers)
	}

	setupSteps := map[string]bool{}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if name == bazelRBEJobName {
			continue // no setup-bazel; its one secret read is pinned below
		}
		n := 0
		for i, step := range job.Steps {
			if step.Uses == "./"+setupBazelActionDir {
				setupSteps[fmt.Sprintf(".jobs.%s.steps[%d].env.", name, i)] = true
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s job %s has %d setup-bazel steps, want 1", bazelWorkflowName, name, n)
		}
	}
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	walkYAML(root, "", func(path string, key bool, value string) {
		if key && value == "continue-on-error" {
			t.Errorf("%s: %s hides failures; the lane is advisory by staying out of ci-gate", bazelWorkflowName, path)
		}
		if key && (value == "secrets" && strings.HasPrefix(path, ".jobs.")) {
			t.Errorf("%s: %s passes secrets to a called workflow or container", bazelWorkflowName, path)
		}
		if key || !secretRef.MatchString(value) {
			return
		}
		if path == bazelRBESecretPath && value == bazelRBESecretValue {
			return // the rbe job's emptiness test (TestBazelRBEJobDecidesOnce)
		}
		prefix := path[:strings.LastIndex(path, ".")+1]
		if !setupSteps[prefix] {
			t.Errorf("%s: %s reads secrets (%q); only the setup-bazel step's env may", bazelWorkflowName, path, value)
		}
	})

	action := readYAMLNode(t, filepath.Join(setupBazelActionDir, "action.yml"))
	walkYAML(action, "", func(path string, key bool, value string) {
		if key && value == "continue-on-error" {
			t.Errorf("setup-bazel: %s hides failures", path)
		}
		if !key && secretRef.MatchString(value) {
			t.Errorf("setup-bazel: %s reads secrets directly; the caller passes them in the step env", path)
		}
	})
}

// The execution mode (remote, local, skip) is decided once, by the rbe job,
// so a run cannot mix modes and no lane can drift from the others (review D1
// F1/F5/F6): Dependabot PRs are same-repo but get no secrets, so a condition
// on vars and fork alone sent them to the remote-only path with nothing to
// run it. The job's one step reads nothing but four booleans, runs no
// repository code, and its outputs are the workflow_call outputs.
func TestBazelRBEJobDecidesOnce(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelRBEJobName)
	if len(job.Needs) != 0 || job.If != "" || job.RunsOn != "ubuntu-latest" || len(job.Env) != 0 {
		t.Errorf("%s: needs %v, if %q, runs-on %q, env %v; want no needs, if or env, on ubuntu-latest",
			bazelRBEJobName, job.Needs, job.If, job.RunsOn, job.Env)
	}
	wantOutputs := map[string]string{
		"enabled": "${{ steps.decide.outputs.enabled }}",
		"mode":    "${{ steps.decide.outputs.mode }}",
	}
	if !reflect.DeepEqual(job.Outputs, wantOutputs) {
		t.Errorf("%s outputs = %v, want %v", bazelRBEJobName, job.Outputs, wantOutputs)
	}
	if len(job.Steps) != 1 {
		t.Fatalf("%s has %d steps, want exactly the decision step", bazelRBEJobName, len(job.Steps))
	}
	step := job.Steps[0]
	wantEnv := map[string]string{
		"RBE_VAR_ON":    "${{ vars.RBE_WEST_WORKERS == 'true' }}",
		"RBE_INPUT_OFF": "${{ inputs.rbe == 'off' }}",
		"FORK":          "${{ github.event.pull_request.head.repo.fork == true }}",
		"HAS_EXECUTOR":  bazelRBESecretValue,
	}
	if step.ID != "decide" || step.Uses != "" || step.Shell != "" || len(step.With) != 0 || !reflect.DeepEqual(step.Env, wantEnv) {
		t.Errorf("%s step: id %q, uses %q, shell %q, with %v, env %v; want id decide, a run step with env %v",
			bazelRBEJobName, step.ID, step.Uses, step.Shell, step.With, step.Env, wantEnv)
	}

	// The workflow_call outputs are the rbe job's, for a caller's gate.
	var doc struct {
		On struct {
			WorkflowCall struct {
				Outputs map[string]struct {
					Value string `yaml:"value"`
				} `yaml:"outputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)), &doc); err != nil {
		t.Fatal(err)
	}
	gotCallOutputs := map[string]string{}
	for k, v := range doc.On.WorkflowCall.Outputs {
		gotCallOutputs[k] = v.Value
	}
	wantCallOutputs := map[string]string{
		"rbe-enabled": "${{ jobs.rbe.outputs.enabled }}",
		"rbe-mode":    "${{ jobs.rbe.outputs.mode }}",
	}
	if !reflect.DeepEqual(gotCallOutputs, wantCallOutputs) {
		t.Errorf("workflow_call outputs = %v, want %v", gotCallOutputs, wantCallOutputs)
	}

	// Nothing outside the decision step re-derives the condition.
	rederive := regexp.MustCompile(`(?i)vars\.RBE_WEST_WORKERS|inputs\.rbe\b|head\.repo\.fork|github\.actor|dependabot`)
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName)), "", func(path string, key bool, value string) {
		if key || !rederive.MatchString(value) || strings.HasPrefix(path, ".jobs."+bazelRBEJobName+".steps[0].env.") {
			return
		}
		t.Errorf("%s: %s re-derives the execution mode (%q); read needs.rbe.outputs instead", bazelWorkflowName, path, value)
	})

	// The decision, for the facts GitHub evaluates the four env expressions
	// on (its == is case-insensitive; a missing secret reads as '').
	type facts struct {
		rbeVar, rbeInput, secret string
		fork                     bool
	}
	cases := []struct {
		name          string
		in            facts
		mode, enabled string
	}{
		{"same-repo PR with secrets", facts{"true", "", "grpcs://x", false}, "remote", "true"},
		{"push to main", facts{"true", "", "grpcs://x", false}, "remote", "true"},
		{"var in other case", facts{"True", "on", "grpcs://x", false}, "remote", "true"},
		{"Dependabot PR (no secrets)", facts{"true", "", "", false}, "local", "false"},
		{"fork PR", facts{"true", "", "", true}, "local", "false"},
		{"fork PR, var unset", facts{"", "", "", true}, "local", "false"},
		{"dispatch rbe=off", facts{"true", "off", "grpcs://x", false}, "local", "false"},
		{"call rbe=OFF", facts{"true", "OFF", "grpcs://x", false}, "local", "false"},
		{"same-repo, var unset", facts{"", "", "grpcs://x", false}, "skip", "false"},
		{"Dependabot, var unset", facts{"", "", "", false}, "skip", "false"},
		{"var false", facts{"false", "on", "grpcs://x", false}, "skip", "false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{
				"RBE_VAR_ON":    strconv.FormatBool(strings.EqualFold(c.in.rbeVar, "true")),
				"RBE_INPUT_OFF": strconv.FormatBool(strings.EqualFold(c.in.rbeInput, "off")),
				"FORK":          strconv.FormatBool(c.in.fork),
				"HAS_EXECUTOR":  strconv.FormatBool(c.in.secret != ""),
			}
			out, err := runBazelRBEDecision(t, step.Run, env)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"mode": c.mode, "enabled": c.enabled}
			if !reflect.DeepEqual(out, want) {
				t.Errorf("outputs = %v, want %v", out, want)
			}
		})
	}
	// A value that is not a boolean fails the job rather than picking a mode.
	if out, err := runBazelRBEDecision(t, step.Run, map[string]string{
		"RBE_VAR_ON": "true", "RBE_INPUT_OFF": "false", "FORK": "", "HAS_EXECUTOR": "true",
	}); err == nil {
		t.Errorf("decision with FORK='' succeeded with %v; want failure", out)
	}
}

// runBazelRBEDecision runs the rbe job's step script under bash -e (as
// GitHub's default shell) and returns its $GITHUB_OUTPUT.
func runBazelRBEDecision(t *testing.T, script string, env map[string]string) (map[string]string, error) {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_OUTPUT=" + output, "GITHUB_STEP_SUMMARY=" + filepath.Join(dir, "summary")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, b)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		out[k] = v
	}
	return out, nil
}

// write-bazelrc.sh: no secrets means a local-only rc (fork PRs), a partial set
// fails, and the full set enables remote-exec with the key material and rc
// outside the workspace and the runner cache.
func TestSetupBazelRCWriter(t *testing.T) {
	bash := requireHostTool(t, "bash")
	script := filepath.Join(sourceRepoRoot(t), setupBazelActionDir, "write-bazelrc.sh")
	pem := func(kind string) string {
		body := "-----BEGIN " + kind + "-----\nMIIBfake" + strings.ReplaceAll(kind, " ", "") + "\nline2\n-----END " + kind + "-----\n"
		return base64.StdEncoding.EncodeToString([]byte(body))
	}
	run := func(t *testing.T, extra ...string) (string, string, string, error) {
		t.Helper()
		dir := t.TempDir()
		workspace := filepath.Join(dir, "ws")
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "github_output")
		secret := filepath.Join(dir, "secret")
		cmd := exec.Command(bash, script)
		cmd.Dir = workspace
		cmd.Env = append([]string{
			"PATH=" + os.Getenv("PATH"),
			"GITHUB_WORKSPACE=" + workspace,
			"GITHUB_OUTPUT=" + out,
			"BAZEL_CI_CACHE_DIR=" + filepath.Join(dir, "cache"),
			"BAZEL_CI_SECRET_DIR=" + secret,
		}, extra...)
		logs, err := cmd.CombinedOutput()
		outputs, _ := os.ReadFile(out)
		rc, _ := os.ReadFile(filepath.Join(secret, "ci.bazelrc"))
		return string(outputs), string(rc), string(logs), err
	}
	executor := "BAZEL_REMOTE_EXECUTOR=grpcs://" + "farm.invalid:443"

	t.Run("no secrets stays local", func(t *testing.T) {
		outputs, rc, logs, err := run(t)
		if err != nil {
			t.Fatalf("err=%v\n%s", err, logs)
		}
		if !strings.Contains(outputs, "remote=false") || strings.Contains(rc, "remote") || strings.Contains(rc, "tls") {
			t.Errorf("outputs=%q rc=%q; want remote=false and no remote flags", outputs, rc)
		}
		if !strings.Contains(rc, "--repository_cache=") || strings.Contains(rc, "--disk_cache=") {
			t.Errorf("local rc = %q; want --repository_cache and no --disk_cache (local runs never save it)", rc)
		}
	})
	for name, env := range map[string][]string{
		"executor only":    {executor},
		"cert and key":     {"RBE_TLS_CERT=" + pem("CERTIFICATE"), "RBE_TLS_KEY=" + pem("PRIVATE KEY")},
		"executor no key":  {executor, "RBE_TLS_CERT=" + pem("CERTIFICATE")},
		"cert not pem b64": {executor, "RBE_TLS_CERT=bm90IGEgcGVt", "RBE_TLS_KEY=" + pem("PRIVATE KEY")},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, _, logs, err := run(t, env...); err == nil {
				t.Errorf("want failure, got success:\n%s", logs)
			}
		})
	}
	t.Run("full set enables remote-exec", func(t *testing.T) {
		outputs, rc, logs, err := run(t, executor, "RBE_TLS_CERT="+pem("CERTIFICATE"), "RBE_TLS_KEY="+pem("PRIVATE KEY"))
		if err != nil {
			t.Fatalf("err=%v\n%s", err, logs)
		}
		for _, want := range []string{
			"build:remote-exec --remote_executor=grpcs://farm.invalid:443", "--tls_client_certificate=", "--tls_client_key=",
			"build:remote-exec --noremote_upload_local_results", "build --config=remote-exec",
		} {
			if !strings.Contains(rc, want) {
				t.Errorf("rc lacks %q:\n%s", want, rc)
			}
		}
		if strings.Contains(rc, "--disk_cache") || strings.Contains(rc, "--remote_instance_name") {
			t.Errorf("rc sets --disk_cache or an instance nobody asked for:\n%s", rc)
		}
		if !strings.Contains(outputs, "remote=true") {
			t.Errorf("outputs = %q, want remote=true", outputs)
		}
		for _, want := range []string{"::add-mask::farm.invalid\n", "::add-mask::MIIBfakeCERTIFICATE\n", "::add-mask::MIIBfakePRIVATEKEY\n", "::add-mask::line2\n", "::warning title=RBE_INSTANCE not set::"} {
			if !strings.Contains(logs, want) {
				t.Errorf("log lacks %q:\n%s", want, logs)
			}
		}
		if strings.Contains(logs, "::add-mask::-----") {
			t.Errorf("PEM armor lines are masked (they would blank every PEM header in the log):\n%s", logs)
		}
	})
	t.Run("CA and instance", func(t *testing.T) {
		_, rc, logs, err := run(t, executor, "RBE_TLS_CERT="+pem("CERTIFICATE"), "RBE_TLS_KEY="+pem("PRIVATE KEY"),
			"RBE_TLS_CA="+pem("CA CERT"), "RBE_INSTANCE=beads")
		if err != nil {
			t.Fatalf("err=%v\n%s", err, logs)
		}
		for _, want := range []string{"build:remote-exec --tls_certificate=", "build:remote-exec --remote_instance_name=beads"} {
			if !strings.Contains(rc, want) {
				t.Errorf("rc lacks %q:\n%s", want, rc)
			}
		}
		if !strings.Contains(logs, "::add-mask::MIIBfakeCACERT\n") || strings.Contains(logs, "RBE_INSTANCE not set") {
			t.Errorf("want the CA masked and no instance warning:\n%s", logs)
		}
	})
	t.Run("secret dir inside workspace fails", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command(bash, script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_WORKSPACE=" + dir,
			"BAZEL_CI_CACHE_DIR=" + filepath.Join(t.TempDir(), "c"), "BAZEL_CI_SECRET_DIR=" + filepath.Join(dir, "s")}
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("want failure for a secret dir inside the workspace:\n%s", out)
		}
	})
}

const (
	bazelAutofixWorkflowName = "bazel-autofix.yml"
	bazelAutofixPushScript   = "scripts/bazel-autofix-push.sh"
	bazelSyncPatchScript     = "scripts/ci/bazel-sync-patch.sh"
	bazelSyncStepName        = "BUILD files in sync (gazelle, go_srcs, MODULE.bazel)"
)

// bazel.yml's sync step stays red on drift but leaves the allowlisted part of
// the fix as the bazel-sync-patch artifact, from PR-head code that sees no
// secrets; bazel-autofix.yml is the only consumer.
func TestBazelSyncStepPublishesPatch(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	sync := job.step(t, bazelSyncStepName)
	if sync.ID != "sync" {
		t.Errorf("sync step id = %q, want sync", sync.ID)
	}
	patchCmd := "./" + bazelSyncPatchScript + ` "$RUNNER_TEMP/bazel-sync-patch"`
	for _, required := range []string{"make bazel-sync-check || check=$?", patchCmd, "exit 1", `exit "$check"`} {
		if !strings.Contains(sync.Run, required) {
			t.Errorf("sync step does not contain %q:\n%s", required, sync.Run)
		}
	}
	if i, j := strings.Index(sync.Run, patchCmd), strings.Index(sync.Run, "exit 1"); j < i {
		t.Errorf("sync step must write the patch before failing")
	}
	for key, value := range sync.Env {
		if strings.Contains(value, "secrets") {
			t.Errorf("sync step env %s reads secrets; it runs PR-head code", key)
		}
	}
	upload := job.step(t, "Upload BUILD sync patch")
	if upload.If != "${{ always() && steps.sync.outcome == 'failure' }}" {
		t.Errorf("patch upload if = %q; want it gated on the sync step's failure", upload.If)
	}
	if upload.With["name"] != "bazel-sync-patch" || upload.With["path"] != "${{ runner.temp }}/bazel-sync-patch/" ||
		upload.With["if-no-files-found"] != "ignore" {
		t.Errorf("patch upload with = %v", upload.With)
	}
	if job.stepIndex(t, "Upload BUILD sync patch") != job.stepIndex(t, bazelSyncStepName)+1 {
		t.Errorf("patch upload must directly follow the sync step")
	}
}

// bazel-autofix.yml runs with write permissions via workflow_run, so it must
// never check out or execute PR code: the base branch's checkout, one trusted
// script, the artifact as data, no expressions in run bodies, and the push
// token visible only to the push step.
func TestBazelAutofixWorkflowSecurity(t *testing.T) {
	rel := filepath.Join(".github", "workflows", bazelAutofixWorkflowName)
	root := readYAMLNode(t, rel)

	if got := yamlMapKeys(root, "on"); !reflect.DeepEqual(got, []string{"workflow_run"}) {
		t.Errorf("triggers = %v, want exactly [workflow_run] (never pull_request_target)", got)
	}
	var doc struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
	}
	if err := root.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	// Only "Bazel" (bazel.yml on pull_request) uploads bazel-sync-patch. Any
	// other trigger would only ever be a no-op run (and, with a shared
	// concurrency group, could get in a real fix's way).
	if want := []string{"Bazel"}; !reflect.DeepEqual(doc.On.WorkflowRun.Workflows, want) {
		t.Errorf("workflow_run.workflows = %v, want %v", doc.On.WorkflowRun.Workflows, want)
	}
	requireWorkflowProducesArtifact(t, bazelWorkflowName, "Bazel", "bazel-sync-patch")
	if !reflect.DeepEqual(doc.On.WorkflowRun.Types, []string{"completed"}) {
		t.Errorf("workflow_run.types = %v, want [completed]", doc.On.WorkflowRun.Types)
	}
	wantPerms := map[string]string{"contents": "write", "pull-requests": "write", "actions": "read"}
	if !reflect.DeepEqual(doc.Permissions, wantPerms) {
		t.Errorf("permissions = %v, want exactly %v", doc.Permissions, wantPerms)
	}

	workflow := readCIWorkflow(t, bazelAutofixWorkflowName)
	if len(workflow.Jobs) != 1 {
		t.Fatalf("%s has %d jobs, want 1", bazelAutofixWorkflowName, len(workflow.Jobs))
	}
	job := workflow.job(t, "autofix")
	for _, cond := range []string{"github.event.workflow_run.event == 'pull_request'", "github.event.workflow_run.conclusion == 'failure'"} {
		if !strings.Contains(job.If, cond) {
			t.Errorf("job if = %q lacks %q", job.If, cond)
		}
	}
	if job.TimeoutMinutes == 0 {
		t.Error("autofix job has no timeout-minutes")
	}

	var checkouts int
	for _, step := range job.Steps {
		if step.Uses != "" {
			family, sha, _ := strings.Cut(step.Uses, "@")
			if family != "actions/checkout" || sha != checkoutSHA {
				t.Errorf("step %q uses %q; only actions/checkout@%s is allowed", step.Name, step.Uses, checkoutSHA)
			}
			if !reflect.DeepEqual(step.With, map[string]string{"persist-credentials": "false"}) {
				t.Errorf("checkout has with %v; want only persist-credentials: false (base default branch, never a PR ref, no token on disk)", step.With)
			}
			checkouts++
		}
		// Event fields (branch names, commit messages) are attacker text:
		// pass them through env, never interpolate them into a script.
		if strings.Contains(step.Run, "${{") {
			t.Errorf("step %q interpolates an expression into run", step.Name)
		}
		if regexp.MustCompile(`\bgit\s+(checkout|fetch|clone|worktree)\b|\bgh\s+pr\s+checkout\b|\bmake\b|\bgo\s+(run|build|test)\b`).MatchString(step.Run) {
			t.Errorf("step %q fetches or runs code in the workflow itself:\n%s", step.Name, step.Run)
		}
	}
	if checkouts != 1 {
		t.Errorf("want exactly one checkout step, got %d", checkouts)
	}
	push := job.step(t, "Push sync commit or leave apply recipe")
	if strings.TrimSpace(push.Run) != "./"+bazelAutofixPushScript {
		t.Errorf("push step run = %q, want ./%s", push.Run, bazelAutofixPushScript)
	}
	download := job.step(t, "Download BUILD sync patch (if any)")
	if !strings.Contains(download.Run, `select(.name == "bazel-sync-patch")`) ||
		!strings.Contains(download.Run, "bazel-sync.patch bazel-sync-meta.txt -d") {
		t.Errorf("download step must fetch bazel-sync-patch and extract only its two files:\n%s", download.Run)
	}
	for key, want := range map[string]string{
		"HEAD_REPO":          "${{ github.event.workflow_run.head_repository.full_name }}",
		"HEAD_BRANCH":        "${{ github.event.workflow_run.head_branch }}",
		"HEAD_SHA":           "${{ github.event.workflow_run.head_sha }}",
		"GH_TOKEN":           "${{ github.token }}",
		"PUSH_TOKEN":         "${{ secrets.DOCS_AUTOFIX_TOKEN || github.token }}",
		"AUTOFIX_TOKEN_KIND": "${{ secrets.DOCS_AUTOFIX_TOKEN && 'pat' || 'default' }}",
	} {
		if got := push.Env[key]; got != want {
			t.Errorf("push step env %s = %q, want %q", key, got, want)
		}
	}

	// Secrets: only the push step's PUSH_TOKEN / AUTOFIX_TOKEN_KIND, and only
	// the docs autofix token that this workflow shares.
	pushIndex := job.stepIndex(t, push.Name)
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	allowed := map[string]bool{
		fmt.Sprintf(".jobs.autofix.steps[%d].env.PUSH_TOKEN", pushIndex):         true,
		fmt.Sprintf(".jobs.autofix.steps[%d].env.AUTOFIX_TOKEN_KIND", pushIndex): true,
	}
	walkYAML(root, "", func(path string, key bool, value string) {
		if key || !secretRef.MatchString(value) {
			return
		}
		if !allowed[path] {
			t.Errorf("%s: %s reads secrets (%q)", bazelAutofixWorkflowName, path, value)
		}
		if refs := regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`).FindAllStringSubmatch(value, -1); len(refs) == 0 {
			t.Errorf("%s: %s uses a non-literal secrets reference", bazelAutofixWorkflowName, path)
		} else {
			for _, ref := range refs {
				if ref[1] != "DOCS_AUTOFIX_TOKEN" {
					t.Errorf("%s: %s reads secret %s; only DOCS_AUTOFIX_TOKEN is shared with this workflow", bazelAutofixWorkflowName, path, ref[1])
				}
			}
		}
	})
}

// requireWorkflowProducesArtifact: the workflow a workflow_run trigger names
// runs on pull_request and uploads the artifact the autofix job consumes.
func requireWorkflowProducesArtifact(t *testing.T, file, name, artifact string) {
	t.Helper()
	node := readYAMLNode(t, filepath.Join(".github", "workflows", file))
	if got := yamlScalar(node, "name"); got != name {
		t.Errorf("%s name = %q, want %q (the workflow_run trigger names it)", file, got, name)
	}
	if !slices.Contains(yamlMapKeys(node, "on"), "pull_request") {
		t.Errorf("%s has no pull_request trigger; the autofix job only acts on PR runs", file)
	}
	var uploads bool
	for _, job := range readCIWorkflow(t, file).Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && step.With["name"] == artifact {
				uploads = true
			}
		}
	}
	if !uploads {
		t.Errorf("%s never uploads %s; the workflow_run trigger on it would be dead", file, artifact)
	}
}

// Both autofix workflows push to the same PR branches. Job-level concurrency
// (skipped runs take no part) in ONE group per head branch, never cancelling:
// they queue instead of racing, and an unrelated completion cannot cancel a
// fix in flight.
func TestAutofixWorkflowsShareConcurrency(t *testing.T) {
	const group = "autofix-${{ github.event.workflow_run.head_repository.full_name }}-${{ github.event.workflow_run.head_branch }}"
	for _, file := range []string{bazelAutofixWorkflowName, "docs-autofix.yml"} {
		root := readYAMLNode(t, filepath.Join(".github", "workflows", file))
		if slices.ContainsFunc(root.Content, func(n *yaml.Node) bool { return n.Value == "concurrency" }) {
			t.Errorf("%s has workflow-level concurrency; it must be on the job so skipped runs take no part", file)
		}
		var doc struct {
			Jobs map[string]struct {
				Concurrency struct {
					Group            string `yaml:"group"`
					CancelInProgress *bool  `yaml:"cancel-in-progress"`
				} `yaml:"concurrency"`
				TimeoutMinutes int              `yaml:"timeout-minutes"`
				Steps          []ciWorkflowStep `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := root.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		job, ok := doc.Jobs["autofix"]
		if !ok || len(doc.Jobs) != 1 {
			t.Fatalf("%s: want exactly one job, autofix", file)
		}
		if job.Concurrency.Group != group {
			t.Errorf("%s job concurrency group = %q, want %q", file, job.Concurrency.Group, group)
		}
		if job.Concurrency.CancelInProgress == nil || *job.Concurrency.CancelInProgress {
			t.Errorf("%s job concurrency must set cancel-in-progress: false", file)
		}
		// A hung run holds the shared, non-cancelling group: bound it.
		if job.TimeoutMinutes != 10 {
			t.Errorf("%s autofix job timeout-minutes = %d, want 10", file, job.TimeoutMinutes)
		}
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["persist-credentials"] != "false" {
				t.Errorf("%s checkout keeps the workflow token in .git/config; set persist-credentials: false", file)
			}
		}
	}
	requireWorkflowProducesArtifact(t, "pr.yml", "PR", "cli-docs-freshness-patch")
}

// The two push scripts share their security-critical functions verbatim, and
// both apply the same hardening.
func TestAutofixScriptsShareGuards(t *testing.T) {
	root := sourceRepoRoot(t)
	bazel := readPolicyFile(t, root, bazelAutofixPushScript)
	docs := readPolicyFile(t, root, "scripts/docs-autofix-push.sh")
	for _, name := range []string{"git_", "validate_patch", "check_staged", "post_or_update_comment", "head_branch_protected"} {
		re := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(name) + `\(\) \{\n.*?^\}\n`)
		a, b := re.FindString(bazel), re.FindString(docs)
		if a == "" || a != b {
			t.Errorf("%s() differs between %s and docs-autofix-push.sh (or is missing)", name, bazelAutofixPushScript)
		}
	}
	validator := regexp.MustCompile(`(?ms)^validate_patch\(\) \{\n.*?^\}\n`).FindString(bazel)
	for _, want := range []string{
		// Any rename/copy header, including legacy "rename old/new".
		`rename |copy |`,
		// Every index line, not only hex ones.
		`grep -E '^index ' "$file" | grep -qvE '^index [0-9a-f]+\.\.[0-9a-f]+( 100644)?$'`,
		`git apply --summary "$file"`,
		`git apply --numstat -z "$file"`,
	} {
		if !strings.Contains(validator, want) {
			t.Errorf("validate_patch lacks %q", want)
		}
	}
	for script, body := range map[string]string{bazelAutofixPushScript: bazel, "scripts/docs-autofix-push.sh": docs} {
		for _, want := range []string{
			`select(.user.login == \"$COMMENT_AUTHOR\" and`,
			`COMMENT_AUTHOR="github-actions[bot]"`,
			`"--force-with-lease=refs/heads/$HEAD_BRANCH:$HEAD_SHA"`,
			`gh api "repos/$BASE_REPO/branches/$enc"`,
			`gh api "repos/$BASE_REPO/rules/branches/$enc"`,
			`(.base.repo.full_name // "") == $base`,
			"export GIT_LFS_SKIP_SMUDGE=1",
			"git -c core.hooksPath=/dev/null",
			// Only the branches the run needs, never a whole-repo clone.
			"git_ init --quiet --bare",
			"fetch --quiet --no-tags --filter=blob:none origin",
			`"+refs/heads/$HEAD_BRANCH:refs/autofix/head"`,
			// The circuit breaker reads the fetched commit and fails closed.
			`if ! HEAD_SUBJECT="$(git_ log -1 --format=%s "$HEAD_SHA")" || [[ "$HEAD_SUBJECT" == "$AUTOFIX_SUBJECT"* ]]; then`,
			`if length == 1 then .[0]`,
			`git_ read-tree "$HEAD_SHA"`,
			"apply --cached",
			`check_staged "$HEAD_SHA"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", script, want)
			}
		}
		// Heredocs hold the human recipe (git apply --index, git push), not
		// commands this script runs.
		code := regexp.MustCompile(`(?ms)<<'?EOF'?\n.*?^EOF\n`).ReplaceAllString(body, "")
		if regexp.MustCompile(`\bgit_?\s+(-c\s+\S+\s+)*(checkout|switch|worktree|restore|reset|stash)\b|apply --index`).MatchString(code) {
			t.Errorf("%s writes the PR tree to disk; apply to the index only", script)
		}
		if strings.Index(code, "if head_branch_protected;") > strings.Index(code, "push --quiet") {
			t.Errorf("%s must check branch protection before pushing", script)
		}
		if regexp.MustCompile(`(?m)^\s*[^#\s][^#\n]*(\bclone\b|/commits/)`).MatchString(code) {
			t.Errorf("%s clones the whole repository or reads commits through the API", script)
		}
		if strings.Contains(code, "fetch") && strings.Count(code, " fetch ") != 1 {
			t.Errorf("%s: want exactly one fetch (head, plus base for attribution)", script)
		}
		// Every git command goes through git_ (no hooks), except the
		// repository-free `git apply --summary/--numstat` of the validator.
		for _, m := range regexp.MustCompile(`(?m)(?:^\s*|[;&|!(]\s*|\$\(\s*)git\s+(\S+)`).FindAllStringSubmatch(code, -1) {
			if m[1] != "apply" && m[1] != "-c" {
				t.Errorf("%s: git %s runs without the git_ wrapper", script, m[1])
			}
		}
		if n, m := strings.Count(code, "git -c "), strings.Count(code, "git -c core.hooksPath=/dev/null "); n != 1 || m != 1 {
			t.Errorf("%s: want exactly one raw `git -c` call, git_'s core.hooksPath=/dev/null; got %d", script, n)
		}
	}
}

// The producer (PR code) and the consumer (base-branch code) must agree on
// the allowlist, or every patch would be refused; the consumer's copy is the
// one that is enforced.
func TestBazelAutofixAllowlistsMatch(t *testing.T) {
	re := regexp.MustCompile(`(?m)^BUILD_FILE_RE='([^']+)'$`)
	caseRe := regexp.MustCompile(`(?m)^\s+(MODULE\.bazel \| MODULE\.bazel\.lock\) return 0 ;;\n\s+third_party/\*\) return 1 ;;)$`)
	want := `^([A-Za-z0-9_+-][A-Za-z0-9_.+-]*/)*BUILD\.bazel$`
	for _, script := range []string{bazelAutofixPushScript, bazelSyncPatchScript} {
		body := readPolicyFile(t, sourceRepoRoot(t), script)
		m := re.FindStringSubmatch(body)
		if m == nil || m[1] != want {
			t.Errorf("%s BUILD_FILE_RE = %v, want %q", script, m, want)
		}
		if !caseRe.MatchString(body) {
			t.Errorf("%s lacks the MODULE.bazel / third_party cases of path_allowed", script)
		}
	}
	compiled := regexp.MustCompile(want)
	for path, ok := range map[string]bool{
		"BUILD.bazel":                       true,
		"cmd/bd/BUILD.bazel":                true,
		"internal/storage/dolt/BUILD.bazel": true,
		"a_b/c-d/e.f/BUILD.bazel":           true,
		".github/BUILD.bazel":               false,
		"a/../BUILD.bazel":                  false,
		"../BUILD.bazel":                    false,
		"/BUILD.bazel":                      false,
		"a//BUILD.bazel":                    false,
		"a/.hidden/BUILD.bazel":             false,
		"BUILD.bazel.go":                    false,
		"xBUILD.bazel":                      false,
		"a/BUILD":                           false,
		"a b/BUILD.bazel":                   false,
		`"a\tb/BUILD.bazel"`:                false,
	} {
		if compiled.MatchString(path) != ok {
			t.Errorf("BUILD_FILE_RE matches %q = %v, want %v", path, !ok, ok)
		}
	}
}

func yamlScalar(node *yaml.Node, key string) string {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1].Value
		}
	}
	return ""
}
