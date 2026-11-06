package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/gitenv"
)

// TestContextOmitsRoleAndWarnsOnReadFailure pins bd-a2qnf4 (GH#6765): when
// the beads.role read fails, `bd context` warns on stderr, omits the role
// line, and still prints the workspace report — this command exists to
// describe degraded states, so one unreadable config value must not hide
// the rest of the identity.
func TestContextOmitsRoleAndWarnsOnReadFailure(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	isolateBeadsDirForTest(t)

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[beads\n\trole = maintainer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	resetRepoCachesForTest(t)
	pinJSONOutput(t, false)
	contextRoleReadFailureWarned = false

	// captureStdout and captureStderr serialize on stdioMutex, so the two
	// captures need two runs rather than one nested capture.
	var runErr error
	stdout := captureStdout(t, func() error {
		runErr = contextCmd.RunE(contextCmd, nil)
		return nil
	})
	if runErr != nil {
		t.Fatalf("bd context must not fail solely for the role read: %v", runErr)
	}
	if !strings.Contains(stdout, "beads dir:") {
		t.Errorf("stdout %q must still report the workspace", stdout)
	}
	if strings.Contains(stdout, "  role:") {
		t.Errorf("stdout %q must omit the role line over the failed read", stdout)
	}
	// The warning is once-per-invocation and the first run above spent it,
	// so arm it again for the run whose stderr is captured.
	contextRoleReadFailureWarned = false
	stderr := captureStderr(t, func() {
		runErr = contextCmd.RunE(contextCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("second run failed: %v", runErr)
	}
	if !strings.Contains(stderr, "could not read beads.role") {
		t.Errorf("stderr %q lacks the read-failure warning", stderr)
	}
	if !strings.Contains(stderr, "bad config") {
		t.Errorf("stderr %q lacks git's diagnostic for the unreadable config", stderr)
	}
}
