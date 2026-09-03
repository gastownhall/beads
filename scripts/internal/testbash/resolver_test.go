package testbash

import (
	"os"
	"strings"
	"testing"
)

func TestSanitizedEnvironmentRemovesBashAuthorityControls(t *testing.T) {
	controls := []string{
		"BASH_ENV",
		"ENV",
		"SHELLOPTS",
		"BASHOPTS",
		"GIT_EXEC_PATH",
		"BASH_FUNC_beads_testbash%%",
	}
	for _, key := range controls {
		t.Setenv(key, "inherited-poison")
	}

	overrides := make([]string, 0, len(controls)+1)
	for _, key := range controls {
		overrides = append(overrides, key+"=override-poison")
	}
	overrides = append(overrides, "BEADS_TEST_SAFE=override")

	environment := sanitizedEnvironment(os.Environ(), overrides...)
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if isBashAuthorityControl(environmentKey(key)) {
			t.Fatalf("sanitized environment retained Bash authority control %q", key)
		}
	}
	if !containsEnvironmentEntry(environment, "BEADS_TEST_SAFE=override") {
		t.Fatalf("sanitized environment did not retain benign override: %q", environment)
	}
}

func TestProbeRejectsSuccessWithoutSentinel(t *testing.T) {
	bash, err := Resolve()
	if err != nil {
		t.Skipf("bash is required to exercise probe execution: %v", err)
	}

	err = runProbe(bash, "early exit", "exit 0", sanitizedEnvironment(os.Environ()))
	if err == nil {
		t.Fatal("early successful exit unexpectedly passed without the execution sentinel")
	}
	if !strings.Contains(err.Error(), "without the exact execution sentinel") {
		t.Fatalf("early-exit error = %v", err)
	}
}

func TestProbePreservesFailure(t *testing.T) {
	bash, err := Resolve()
	if err != nil {
		t.Skipf("bash is required to exercise probe execution: %v", err)
	}

	err = Probe(bash, "failing capability", "exit 73", os.Environ())
	if err == nil {
		t.Fatal("failing capability unexpectedly passed")
	}
	if !strings.Contains(err.Error(), "exit status 73") {
		t.Fatalf("failing-capability error = %v", err)
	}
}

func containsEnvironmentEntry(environment []string, want string) bool {
	for _, entry := range environment {
		if entry == want {
			return true
		}
	}
	return false
}
