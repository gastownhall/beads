//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedDoctorRunsFullPipeline(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt doctor tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dr")
	env := embeddedDoctorEnv(t, bd, dir)

	textOut, _ := runEmbeddedDoctor(t, bd, dir, env, "doctor")
	assertNoEmbeddedDoctorStub(t, textOut)
	if !strings.Contains(textOut, "bd doctor v") {
		t.Fatalf("doctor text output did not run full diagnostics:\n%s", textOut)
	}
	if !strings.Contains(textOut, "passed") {
		t.Fatalf("doctor text output missing diagnostic summary:\n%s", textOut)
	}

	jsonOut, _ := runEmbeddedDoctor(t, bd, dir, env, "doctor", "--json")
	assertNoEmbeddedDoctorStub(t, jsonOut)
	var result doctorResult
	if err := json.Unmarshal([]byte(jsonOut), &result); err != nil {
		t.Fatalf("doctor --json did not emit parseable doctorResult JSON: %v\n%s", err, jsonOut)
	}
	if len(result.Checks) == 0 {
		t.Fatalf("doctor --json emitted no checks:\n%s", jsonOut)
	}
	if result.CLIVersion == "" {
		t.Fatalf("doctor --json missing cli_version:\n%s", jsonOut)
	}

	agentOut, _ := runEmbeddedDoctor(t, bd, dir, env, "doctor", "--agent", "--json")
	assertNoEmbeddedDoctorStub(t, agentOut)
	var agentResult agentDoctorResult
	if err := json.Unmarshal([]byte(agentOut), &agentResult); err != nil {
		t.Fatalf("doctor --agent --json did not emit parseable agent JSON: %v\n%s", err, agentOut)
	}
	if agentResult.CLIVersion == "" {
		t.Fatalf("doctor --agent --json missing cli_version:\n%s", agentOut)
	}
	if agentResult.Summary == "" {
		t.Fatalf("doctor --agent --json missing summary:\n%s", agentOut)
	}
	if agentResult.PassedCount == 0 && len(agentResult.Diagnostics) == 0 {
		t.Fatalf("doctor --agent --json emitted neither passed checks nor diagnostics:\n%s", agentOut)
	}
}

func embeddedDoctorEnv(t *testing.T, bd, dir string) []string {
	t.Helper()
	env := bdEnv(dir)
	bdDir := filepath.Dir(bd)
	replacedPath := false
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[i] = "PATH=" + bdDir + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
			replacedPath = true
			break
		}
	}
	if !replacedPath {
		env = append(env, "PATH="+bdDir)
	}
	return env
}

func runEmbeddedDoctor(t *testing.T, bd, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func assertNoEmbeddedDoctorStub(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "not yet supported in embedded mode") {
		t.Fatalf("doctor emitted old embedded-mode stub:\n%s", out)
	}
	if strings.Contains(out, "Reinitialize if needed") || strings.Contains(out, "Switch to server mode") {
		t.Fatalf("doctor emitted old destructive embedded-mode hints:\n%s", out)
	}
}
