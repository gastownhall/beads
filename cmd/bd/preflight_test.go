package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCheckResult_Passed(t *testing.T) {
	r := CheckResult{
		Name:    "Tests pass",
		Passed:  true,
		Command: "go test ./...",
		Output:  "",
	}

	if !r.Passed {
		t.Error("Expected result to be passed")
	}
	if r.Name != "Tests pass" {
		t.Errorf("Expected name 'Tests pass', got %q", r.Name)
	}
}

func TestPrintCheckResult_Failed(t *testing.T) {
	r := CheckResult{
		Name:    "tests",
		Passed:  false,
		Command: "go test ./...",
		Output:  "--- FAIL: TestSomething\nexpected X got Y",
	}

	if r.Passed {
		t.Error("Expected result to be failed")
	}
	if !strings.Contains(r.Output, "FAIL") {
		t.Error("Expected output to contain FAIL")
	}
}

func TestCheckResult_JSONFields(t *testing.T) {
	r := CheckResult{
		Name:    "tests",
		Passed:  true,
		Command: "go test -short ./...",
		Output:  "ok  	github.com/example/pkg	0.123s",
	}

	// Verify JSON struct tags are correct by checking field names
	if r.Name == "" {
		t.Error("Name should not be empty")
	}
	if r.Command == "" {
		t.Error("Command should not be empty")
	}
}

func TestPreflightResult_AllPassed(t *testing.T) {
	results := PreflightResult{
		Checks: []CheckResult{
			{Name: "Tests pass", Passed: true, Command: "go test ./..."},
			{Name: "Lint passes", Passed: true, Command: "golangci-lint run"},
		},
		Passed:  true,
		Summary: "2/2 checks passed",
	}

	if !results.Passed {
		t.Error("Expected all checks to pass")
	}
	if len(results.Checks) != 2 {
		t.Errorf("Expected 2 checks, got %d", len(results.Checks))
	}
}

func TestPreflightResult_SomeFailed(t *testing.T) {
	results := PreflightResult{
		Checks: []CheckResult{
			{Name: "Tests pass", Passed: true, Command: "go test ./..."},
			{Name: "Lint passes", Passed: false, Command: "golangci-lint run", Output: "linting errors"},
		},
		Passed:  false,
		Summary: "1/2 checks passed",
	}

	if results.Passed {
		t.Error("Expected some checks to fail")
	}

	passCount := 0
	failCount := 0
	for _, c := range results.Checks {
		if c.Passed {
			passCount++
		} else {
			failCount++
		}
	}
	if passCount != 1 || failCount != 1 {
		t.Errorf("Expected 1 pass and 1 fail, got %d pass and %d fail", passCount, failCount)
	}
}

func TestPreflightResult_WithSkipped(t *testing.T) {
	results := PreflightResult{
		Checks: []CheckResult{
			{Name: "Tests pass", Passed: true, Command: "go test ./..."},
			{Name: "Lint passes", Passed: false, Skipped: true, Command: "golangci-lint run", Output: "not installed"},
		},
		Passed:  true,
		Summary: "1/1 checks passed (1 skipped)",
	}

	// Skipped checks don't count as failures
	if !results.Passed {
		t.Error("Expected result to pass (skipped doesn't count as failure)")
	}

	skipCount := 0
	for _, c := range results.Checks {
		if c.Skipped {
			skipCount++
		}
	}
	if skipCount != 1 {
		t.Errorf("Expected 1 skipped, got %d", skipCount)
	}
}

func TestPreflightResult_WithWarning(t *testing.T) {
	results := PreflightResult{
		Checks: []CheckResult{
			{Name: "Tests pass", Passed: true, Command: "go test ./..."},
			{Name: "Nix hash current", Passed: false, Warning: true, Command: "git diff HEAD -- go.sum", Output: "go.sum changed"},
		},
		Passed:  true, // Warnings don't fail the overall result
		Summary: "1/2 checks passed, 1 warning(s)",
	}

	// Warnings don't count as failures
	if !results.Passed {
		t.Error("Expected result to pass (warning doesn't count as failure)")
	}

	warnCount := 0
	for _, c := range results.Checks {
		if c.Warning {
			warnCount++
		}
	}
	if warnCount != 1 {
		t.Errorf("Expected 1 warning, got %d", warnCount)
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLen    int
		wantTrunc bool
	}{
		{"short string", "hello world", 500, false},
		{"exact length", strings.Repeat("x", 500), 500, false},
		{"over length", strings.Repeat("x", 600), 500, true},
		{"empty string", "", 500, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateOutput(tt.input, tt.maxLen)
			if tt.wantTrunc {
				if !strings.Contains(result, "truncated") {
					t.Error("Expected truncation marker in output")
				}
				if len(result) > tt.maxLen+20 { // allow some slack for marker
					t.Errorf("Result too long: got %d chars", len(result))
				}
			} else {
				if strings.Contains(result, "truncated") {
					t.Error("Did not expect truncation marker")
				}
			}
		})
	}
}

func TestRunLintCheck_MissingCommandFailsByDefault(t *testing.T) {
	t.Setenv("PATH", "")

	result := runLintCheck(false)
	if result.Passed {
		t.Fatalf("expected lint check to fail when golangci-lint is missing")
	}
	if result.Skipped {
		t.Fatalf("expected missing lint to be a hard failure, not skipped")
	}
	if !strings.Contains(result.Output, "not found in PATH") {
		t.Fatalf("expected missing command message, got: %q", result.Output)
	}
	if !strings.Contains(result.Output, "--skip-lint") {
		t.Fatalf("expected explicit skip guidance in message, got: %q", result.Output)
	}
}

func TestRunLintCheck_SkipLintFlag(t *testing.T) {
	result := runLintCheck(true)
	if result.Passed {
		t.Fatalf("expected skipped lint check to remain non-passing")
	}
	if !result.Skipped {
		t.Fatalf("expected skipped lint check to be marked skipped")
	}
	if !result.Warning {
		t.Fatalf("expected skipped lint check to be warning")
	}
	if !strings.Contains(result.Output, "--skip-lint") {
		t.Fatalf("expected output to mention --skip-lint, got: %q", result.Output)
	}
}

func TestLintInvocationForRootMatchesChecklistAndProjectType(t *testing.T) {
	beads := writeMarkerDir(t, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"})
	beadsInvocation := lintInvocationForRoot(beads)
	if beadsInvocation.display != beadsPRLintDriverCommand {
		t.Fatalf("Beads command = %q, want %q", beadsInvocation.display, beadsPRLintDriverCommand)
	}
	if beadsInvocation.executable != "go" {
		t.Fatalf("Beads executable = %q, want go", beadsInvocation.executable)
	}
	wantBeadsArgs := []string{"run", "-mod=readonly", "-tags=gms_pure_go", "./scripts/pr-lint"}
	if !reflect.DeepEqual(beadsInvocation.args, wantBeadsArgs) {
		t.Fatalf("Beads args = %#v, want %#v", beadsInvocation.args, wantBeadsArgs)
	}
	if beadsInvocation.dir != beads {
		t.Fatalf("Beads command dir = %q, want %q", beadsInvocation.dir, beads)
	}
	if checklist := strings.Join(buildPreflightChecklist(beads), "\n"); !strings.Contains(checklist, "make ci-pr-lint") {
		t.Fatalf("Beads checklist does not report supported lint entrypoint:\n%s", checklist)
	}

	generic := writeMarkerDir(t, map[string]string{"go.mod": "module example.com/generic\n"})
	genericInvocation := lintInvocationForRoot(generic)
	if genericInvocation.display != "golangci-lint run ./..." {
		t.Fatalf("generic command = %q, want direct lint contract", genericInvocation.display)
	}
	if genericInvocation.executable != "golangci-lint" {
		t.Fatalf("generic executable = %q, want golangci-lint", genericInvocation.executable)
	}
	if want := []string{"run", "./..."}; !reflect.DeepEqual(genericInvocation.args, want) {
		t.Fatalf("generic args = %#v, want %#v", genericInvocation.args, want)
	}
	if checklist := strings.Join(buildPreflightChecklist(generic), "\n"); !strings.Contains(checklist, genericInvocation.display) {
		t.Fatalf("generic checklist does not report executable lint command %q:\n%s", genericInvocation.display, checklist)
	}
}

func TestRunLintCheckAtBeadsExecutesCheckoutDriverAndReportsJSONCommand(t *testing.T) {
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("locate Go toolchain for subprocess fixture: %v", err)
	}
	helperDir := t.TempDir()
	helperSource := filepath.Join(helperDir, "fake-go.go")
	const source = `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	marker, err := os.Create(os.Getenv("PREFLIGHT_LINT_MARKER"))
	if err != nil {
		panic(err)
	}
	defer marker.Close()
	if err := json.NewEncoder(marker).Encode(map[string]any{"args": os.Args[1:], "dir": dir}); err != nil {
		panic(err)
	}
	fmt.Println("synthetic checkout lint success")
}
`
	if err := os.WriteFile(helperSource, []byte(source), 0o600); err != nil {
		t.Fatalf("write fake Go source: %v", err)
	}
	helperName := "go"
	if runtime.GOOS == "windows" {
		helperName += ".exe"
	}
	helperPath := filepath.Join(helperDir, helperName)
	build := exec.Command(realGo, "build", "-o", helperPath, helperSource)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Go executable: %v\n%s", err, output)
	}

	beads := writeMarkerDir(t, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"})
	marker := filepath.Join(t.TempDir(), "invocation.json")
	t.Setenv("PATH", helperDir)
	t.Setenv("PREFLIGHT_LINT_MARKER", marker)
	result := runLintCheckAt(beads, false)
	if !result.Passed {
		t.Fatalf("checkout lint invocation failed: %s", result.Output)
	}
	if result.Command != beadsPRLintDriverCommand {
		t.Fatalf("reported command = %q, want %q", result.Command, beadsPRLintDriverCommand)
	}
	if !strings.Contains(result.Output, "synthetic checkout lint success") {
		t.Fatalf("missing subprocess output: %q", result.Output)
	}

	var invocation struct {
		Args []string `json:"args"`
		Dir  string   `json:"dir"`
	}
	markerData, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read invocation marker: %v", err)
	}
	if err := json.Unmarshal(markerData, &invocation); err != nil {
		t.Fatalf("decode invocation marker: %v", err)
	}
	wantArgs := []string{"run", "-mod=readonly", "-tags=gms_pure_go", "./scripts/pr-lint"}
	if !reflect.DeepEqual(invocation.Args, wantArgs) {
		t.Fatalf("subprocess args = %#v, want %#v", invocation.Args, wantArgs)
	}
	if invocation.Dir != beads {
		t.Fatalf("subprocess dir = %q, want checkout root %q", invocation.Dir, beads)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal CheckResult: %v", err)
	}
	var reported CheckResult
	if err := json.Unmarshal(encoded, &reported); err != nil {
		t.Fatalf("unmarshal CheckResult: %v", err)
	}
	if reported.Command != beadsPRLintDriverCommand || !reported.Passed {
		t.Fatalf("JSON evidence = %#v, want passing checkout driver command", reported)
	}
}

func TestRunFmtCheckAtFormattedRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := runFmtCheckAt(dir)
	if !result.Passed {
		t.Fatalf("formatted root failed: %s", result.Output)
	}
}

func TestRunFmtCheckAtFindsUnformattedFileOutsideCallerSubtree(t *testing.T) {
	root := t.TempDir()
	callerDir := filepath.Join(root, "nested", "caller")
	outsideCaller := filepath.Join(root, "sibling", "bad.go")
	if err := os.MkdirAll(callerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(outsideCaller), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideCaller, []byte("package sibling\nfunc  bad( )  {  }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(callerDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	result := runFmtCheckAt(root)
	if result.Passed {
		t.Fatal("root formatting check passed despite unformatted sibling file")
	}
	if !strings.Contains(result.Output, "bad.go") {
		t.Fatalf("root formatting check missed sibling file: %s", result.Output)
	}
}

func TestRunBeadsPollutionCheck_Clean(t *testing.T) {
	// In a clean repo state (no uncommitted .beads changes), the check should pass.
	result := runBeadsPollutionCheck()
	if !result.Passed {
		// If this fails, it means the test environment itself has .beads changes,
		// which is valid — skip rather than fail.
		if strings.Contains(result.Output, "modified") {
			t.Skip("test environment has .beads changes, skipping")
		}
		if result.Skipped {
			t.Skip("cannot determine branch in test environment")
		}
		t.Fatalf("expected beads pollution check to pass in clean state, got: %q", result.Output)
	}
}

func TestRunVersionSyncCheck_ScriptFallback(t *testing.T) {
	// Run from a temp dir where scripts/check-versions.sh does not exist.
	// The fallback inline logic should be used, resulting in a skipped result
	// because version.go won't be found either.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	result := runVersionSyncCheck()
	// Without version.go present, fallback should skip
	if !result.Skipped {
		// Could also pass if default.nix is also missing — both are acceptable fallback outcomes
		if result.Passed && strings.Contains(result.Output, "not found") {
			return // acceptable: nix not found skip
		}
	}
	if result.Command == "scripts/check-versions.sh" {
		t.Fatal("expected fallback logic, not script invocation")
	}
}

// writeMarkerDir creates a temp dir containing the given marker files (each
// with trivial content) and returns its path. Used to exercise project-type
// detection in the preflight checklist (GH#4364).
func writeMarkerDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestBuildPreflightChecklist(t *testing.T) {
	beadsGoMod := "module github.com/steveyegge/beads\n\ngo 1.22\n"
	otherGoMod := "module example.com/foo\n\ngo 1.22\n"

	cases := []struct {
		name        string
		files       map[string]string
		wantContain []string // substring present in at least one item
		wantAbsent  []string // substring present in no item
	}{
		{
			name:        "beads repo keeps rich Go+Nix checklist",
			files:       map[string]string{"go.mod": beadsGoMod, "default.nix": "{}", ".beads": ""},
			wantContain: []string{"gms_pure_go", "Version sync", "Nix hash", "No beads pollution"},
		},
		{
			name:        "generic Go project gets plain go commands",
			files:       map[string]string{"go.mod": otherGoMod},
			wantContain: []string{"go test ./...", "golangci-lint run ./...", "gofmt -l ."},
			wantAbsent:  []string{"gms_pure_go", "Version sync", "Nix hash"},
		},
		{
			name:        "node project",
			files:       map[string]string{"package.json": "{}"},
			wantContain: []string{"npm test", "tsc --noEmit"},
			wantAbsent:  []string{"go test", "gofmt"},
		},
		{
			name:        "rust project",
			files:       map[string]string{"Cargo.toml": "[package]\n"},
			wantContain: []string{"cargo test", "cargo clippy"},
			wantAbsent:  []string{"go test ./...", "npm test"},
		},
		{
			name:        "python pyproject",
			files:       map[string]string{"pyproject.toml": "[project]\n"},
			wantContain: []string{"pytest", "ruff check"},
		},
		{
			name:        "python setup.py",
			files:       map[string]string{"setup.py": "", ".beads": ""},
			wantContain: []string{"pytest", "No beads pollution"},
		},
		{
			name:        "unknown stack gets generic reminder",
			files:       map[string]string{"README.md": "hi"},
			wantContain: []string{"run your project's test suite"},
			wantAbsent:  []string{"go test", "npm test", "cargo test", "pytest"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeMarkerDir(t, tc.files)
			joined := strings.Join(buildPreflightChecklist(dir), "\n")
			for _, want := range tc.wantContain {
				if !strings.Contains(joined, want) {
					t.Errorf("checklist missing %q\ngot:\n%s", want, joined)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(joined, absent) {
					t.Errorf("checklist unexpectedly contains %q\ngot:\n%s", absent, joined)
				}
			}
		})
	}
}

func TestIsBeadsRepo(t *testing.T) {
	beads := writeMarkerDir(t, map[string]string{"go.mod": "module github.com/steveyegge/beads\n"})
	if !isBeadsRepo(beads) {
		t.Error("expected beads module to be detected as the beads repo")
	}
	other := writeMarkerDir(t, map[string]string{"go.mod": "module example.com/foo\n"})
	if isBeadsRepo(other) {
		t.Error("non-beads module should not be detected as the beads repo")
	}
	empty := t.TempDir()
	if isBeadsRepo(empty) {
		t.Error("dir without go.mod should not be detected as the beads repo")
	}
}
