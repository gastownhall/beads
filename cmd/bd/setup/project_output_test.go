package setup

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// bd init --quiet hands the project installers QuietProjectInstallOutput:
// none of their progress may reach the writers the env provider set up (the
// process's stdout in production), and their error stream stays stderr.
func TestProjectInstallersReportToTheGivenOutput(t *testing.T) {
	q := QuietProjectInstallOutput()
	if q.Stdout != io.Discard || q.stderr() == io.Discard {
		t.Fatalf("QuietProjectInstallOutput = %+v; want progress discarded, errors kept", q)
	}

	env, envOut, envErr := newClaudeTestEnv(t)
	stubClaudeEnvProvider(t, env, nil)
	var out, errOut bytes.Buffer
	if err := InstallClaudeProjectTo(false, ProjectInstallOutput{Stdout: &out, Stderr: &errOut}); err != nil {
		t.Fatalf("InstallClaudeProjectTo: %v", err)
	}
	if envOut.Len() != 0 || envErr.Len() != 0 {
		t.Errorf("Claude installer wrote to the env's own streams: stdout %q stderr %q", envOut, envErr)
	}
	if !strings.Contains(out.String(), "Installing Claude hooks for this project") {
		t.Errorf("Claude installer progress not on the given stdout: %q", out.String())
	}

	codex, codexOut, codexErr := newCodexTestEnv(t)
	origCodex := codexEnvProvider
	codexEnvProvider = func() (codexEnv, error) { return codex, nil }
	t.Cleanup(func() { codexEnvProvider = origCodex })
	out.Reset()
	if err := InstallCodexProjectTo(ProjectInstallOutput{Stdout: &out, Stderr: &errOut}); err != nil {
		t.Fatalf("InstallCodexProjectTo: %v", err)
	}
	if codexOut.Len() != 0 || codexErr.Len() != 0 {
		t.Errorf("Codex installer wrote to the env's own streams: stdout %q stderr %q", codexOut, codexErr)
	}
	if out.Len() == 0 {
		t.Error("Codex installer progress not on the given stdout")
	}

	inTempDir(t)
	out.Reset()
	if err := InstallCursorProjectTo(ProjectInstallOutput{Stdout: &out, Stderr: &errOut}); err != nil {
		t.Fatalf("InstallCursorProjectTo: %v", err)
	}
	if !strings.Contains(out.String(), "Cursor integration installed") || !strings.Contains(out.String(), "Beads agent skill installed") {
		t.Errorf("Cursor installer progress not on the given stdout: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("successful installs wrote errors: %q", errOut.String())
	}
}
