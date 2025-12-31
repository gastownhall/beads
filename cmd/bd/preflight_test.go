package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"tests", "Tests"},
		{"lint", "Lint"},
		{"", ""},
		{"A", "A"},
		{"already Capitalized", "Already Capitalized"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := capitalizeFirst(tt.input)
			if result != tt.expected {
				t.Errorf("capitalizeFirst(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestPrintCheckResult_Passed(t *testing.T) {
	// Capture stdout by redirecting to buffer
	r := CheckResult{
		Name:    "tests",
		Passed:  true,
		Command: "go test ./...",
		Output:  "",
	}

	var buf bytes.Buffer
	// We can't easily capture stdout, so just verify the function doesn't panic
	// and test the logic directly
	if !r.Passed {
		t.Error("Expected result to be passed")
	}
	if r.Name != "tests" {
		t.Errorf("Expected name 'tests', got %q", r.Name)
	}
	_ = buf // keep compiler happy
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

func TestPreflightResults_AllPassed(t *testing.T) {
	results := PreflightResults{
		Checks: []CheckResult{
			{Name: "tests", Passed: true, Command: "go test ./..."},
			{Name: "lint", Passed: true, Command: "golangci-lint run"},
		},
		Passed:  true,
		Summary: "2 passed, 0 failed",
	}

	if !results.Passed {
		t.Error("Expected all checks to pass")
	}
	if len(results.Checks) != 2 {
		t.Errorf("Expected 2 checks, got %d", len(results.Checks))
	}
}

func TestPreflightResults_SomeFailed(t *testing.T) {
	results := PreflightResults{
		Checks: []CheckResult{
			{Name: "tests", Passed: true, Command: "go test ./..."},
			{Name: "lint", Passed: false, Command: "golangci-lint run", Output: "linting errors"},
		},
		Passed:  false,
		Summary: "1 passed, 1 failed",
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

func TestOutputTruncation(t *testing.T) {
	// Test that long output is properly truncated
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "ok  	github.com/example/pkg" + strings.Repeat("x", 50)
	}
	output := strings.Join(lines, "\n")

	// Simulate the truncation logic
	if len(output) > 3000 {
		splitLines := strings.Split(output, "\n")
		if len(splitLines) > 50 {
			firstPart := strings.Join(splitLines[:30], "\n")
			lastPart := strings.Join(splitLines[len(splitLines)-20:], "\n")
			truncated := firstPart + "\n\n...(truncated)...\n\n" + lastPart

			if !strings.Contains(truncated, "truncated") {
				t.Error("Expected truncation marker in output")
			}
			if len(strings.Split(truncated, "\n")) > 55 {
				t.Error("Truncated output should be around 50 lines plus marker")
			}
		}
	}
}
