package scripts_test

import (
	"strings"
	"testing"
)

func TestMigrationTestInstallDependenciesIsBoundedAndRetried(t *testing.T) {
	job := readCIWorkflow(t, "migration-test.yml").job(t, "historical-upgrades")
	step := job.step(t, "Install test dependencies")

	if step.TimeoutMinutes != 6 {
		t.Errorf("Install test dependencies timeout-minutes = %d, want 6", step.TimeoutMinutes)
	}

	for _, required := range []string{
		"sudo timeout 120 apt-get",
		"-o Acquire::Retries=3",
		"-o Acquire::http::Timeout=30",
		"-o Acquire::https::Timeout=30",
		"for attempt in 1 2 3",
		"sudo timeout 180 apt-get install -y jq libicu74",
	} {
		if !strings.Contains(step.Run, required) {
			t.Errorf("Install test dependencies command does not contain %q:\n%s", required, step.Run)
		}
	}
}

func TestReleaseInstallCrossCompilationToolchainsIsBoundedAndRetried(t *testing.T) {
	job := readCIWorkflow(t, "release.yml").job(t, "goreleaser")
	step := job.step(t, "Install cross-compilation toolchains and signing tools")

	if step.TimeoutMinutes != 8 {
		t.Errorf("Install cross-compilation toolchains timeout-minutes = %d, want 8", step.TimeoutMinutes)
	}

	for _, required := range []string{
		"sudo timeout 120 apt-get",
		"-o Acquire::Retries=3",
		"-o Acquire::http::Timeout=30",
		"-o Acquire::https::Timeout=30",
		"for attempt in 1 2 3",
		"sudo timeout 300 apt-get install -y gcc-mingw-w64-x86-64 gcc-aarch64-linux-gnu osslsigncode",
	} {
		if !strings.Contains(step.Run, required) {
			t.Errorf("Install cross-compilation toolchains command does not contain %q:\n%s", required, step.Run)
		}
	}
}
