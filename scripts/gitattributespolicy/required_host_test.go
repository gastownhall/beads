package gitattributespolicy_test

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
)

var (
	requiredHost = flag.Bool(
		"required-host",
		false,
		"require exact hosted GOOS and one successful EOL boundary test")
	expectedGOOS = flag.String(
		"expected-goos",
		"",
		"protected GOOS expected by the required hosted job")
	eolTestRuns    atomic.Int32
	eolTestPasses  atomic.Int32
	eolTestSkipped atomic.Int32
)

func TestMain(m *testing.M) {
	if !flag.Parsed() {
		flag.Parse()
	}
	if err := validateRequiredHost(*requiredHost, *expectedGOOS, runtime.GOOS); err != nil {
		fmt.Fprintf(os.Stderr, "repository EOL required-host contract failed: %v\n", err)
		os.Exit(2)
	}

	exitCode := m.Run()
	if *requiredHost {
		if err := validateRequiredEOLExecution(
			int(eolTestRuns.Load()),
			int(eolTestPasses.Load()),
			int(eolTestSkipped.Load())); err != nil {
			fmt.Fprintf(os.Stderr, "repository EOL required-test contract failed: %v\n", err)
			if exitCode == 0 {
				exitCode = 2
			}
		}
	}
	os.Exit(exitCode)
}

func TestRequiredHostContractRejectsMissingOrWrongGOOS(t *testing.T) {
	for _, test := range []struct {
		name     string
		required bool
		expected string
		actual   string
		wantErr  bool
	}{
		{name: "ordinary local run", required: false, wantErr: false},
		{name: "missing expected GOOS", required: true, actual: "linux", wantErr: true},
		{name: "wrong runtime GOOS", required: true, expected: "linux", actual: "windows", wantErr: true},
		{name: "unsupported expected GOOS", required: true, expected: "plan9", actual: "plan9", wantErr: true},
		{name: "matching required GOOS", required: true, expected: "linux", actual: "linux", wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRequiredHost(test.required, test.expected, test.actual)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateRequiredHost(%t, %q, %q) error = %v, wantErr %t",
					test.required, test.expected, test.actual, err, test.wantErr)
			}
		})
	}
}

func TestRequiredEOLExecutionContractRejectsMissingOrSkippedTest(t *testing.T) {
	for _, test := range []struct {
		name                string
		runs, passes, skips int
		wantErr             bool
	}{
		{name: "exact successful run", runs: 1, passes: 1, wantErr: false},
		{name: "zero selected tests", wantErr: true},
		{name: "skipped test", runs: 1, skips: 1, wantErr: true},
		{name: "failed test", runs: 1, wantErr: true},
		{name: "duplicate test", runs: 2, passes: 2, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRequiredEOLExecution(test.runs, test.passes, test.skips)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateRequiredEOLExecution(%d, %d, %d) error = %v, wantErr %t",
					test.runs, test.passes, test.skips, err, test.wantErr)
			}
		})
	}
}

func validateRequiredHost(required bool, expected, actual string) error {
	if !required {
		return nil
	}
	if expected == "" {
		return fmt.Errorf("-expected-goos is required when -required-host is set")
	}
	switch expected {
	case "linux", "darwin", "windows":
	default:
		return fmt.Errorf("unsupported expected GOOS %q", expected)
	}
	if actual != expected {
		return fmt.Errorf("runtime GOOS %q does not match protected expected GOOS %q", actual, expected)
	}
	return nil
}

func validateRequiredEOLExecution(runs, passes, skips int) error {
	if runs != 1 || passes != 1 || skips != 0 {
		return fmt.Errorf(
			"EOL boundary counts runs=%d passes=%d skips=%d, want exactly runs=1 passes=1 skips=0",
			runs,
			passes,
			skips)
	}
	return nil
}
