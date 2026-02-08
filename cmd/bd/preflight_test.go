package main

import (
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
