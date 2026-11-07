//go:build cgo && integration && unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// writeDoltVersionWrapper writes an executable `dolt` at path that reports
// version for `dolt version` and runs realDolt for everything else, so the
// server it starts is a real dolt sql-server whose command line names dolt.
// note varies the file's size between versions, as a real upgrade does.
func writeDoltVersionWrapper(t *testing.T, path, realDolt, version, note string) {
	t.Helper()
	script := "#!/bin/sh\n# " + note + "\n" +
		"if [ \"$1\" = version ]; then echo \"dolt version " + version + "\"; exit 0; fi\n" +
		"exec '" + realDolt + "' \"$@\"\n"
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(script), 0o700); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	// Replace, as an installer does: the running server keeps the old file.
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// runBDStdoutStderr runs bd and returns its stdout and stderr separately.
func runBDStdoutStderr(t *testing.T, bdBinary, dir string, env []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bdBinary, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func readServerPIDFile(t *testing.T, beadsDir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.pid")) //nolint:gosec // G304: test-owned workspace
	if err != nil {
		t.Fatalf("read dolt-server.pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("parse dolt-server.pid %q: %v", b, err)
	}
	return pid
}

func readServerPortFile(t *testing.T, beadsDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port")) //nolint:gosec // G304: test-owned workspace
	if err != nil {
		t.Fatalf("read dolt-server.port: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// TestE2E_AutoStartedServerRestartsAfterDoltBinaryUpgrade is GH#6064's CLI
// regression, with the same bd throughout: the server bd started for the
// workspace keeps running the dolt it was started from, so after dolt is
// upgraded the next command replaces it, on the same port and with the same
// data, before its first query, and says so once. The replacement then
// serves the following commands.
func TestE2E_AutoStartedServerRestartsAfterDoltBinaryUpgrade(t *testing.T) {
	testutil.RequireDoltBinary(t)
	realDolt, err := exec.LookPath("dolt")
	if err != nil {
		t.Skipf("dolt not on PATH: %v", err)
	}
	if realDolt, err = filepath.Abs(realDolt); err != nil {
		t.Fatal(err)
	}

	bdBinary := buildLifecycleTestBinary(t)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	stopDoltServerCleanup(t, beadsDir)
	if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test User")

	binDir := t.TempDir()
	wrapper := filepath.Join(binDir, "dolt")
	writeDoltVersionWrapper(t, wrapper, realDolt, "2.6.0", "first install")

	env := append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BEADS_TEST_MODE=",
		"GT_ROOT=",
		"BEADS_DOLT_AUTO_START=",
		"BEADS_DOLT_SERVER_MODE=",
		"BEADS_DOLT_SERVER_HOST=",
		"BEADS_DOLT_SERVER_SOCKET=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
		"BEADS_DOLT_SHARED_SERVER=",
		"BD_MIGRATION_FREEZE_FILE=",
		"GIT_TERMINAL_PROMPT=0",
		"SSH_ASKPASS=",
		"GIT_ASKPASS=",
	)

	if out, errOut, err := runBDStdoutStderr(t, bdBinary, tmpDir, env, "init", "--backend", "dolt", "--server", "--prefix", "test", "--quiet"); err != nil {
		t.Fatalf("bd init --server failed: %v\n%s%s", err, out, errOut)
	}
	createOut, createErr, err := runBDStdoutStderr(t, bdBinary, tmpDir, env, "create", "survives the dolt upgrade", "--json")
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s%s", err, createOut, createErr)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut[strings.Index(createOut, "{"):]), &created); err != nil || created.ID == "" {
		t.Fatalf("parse create json (%v):\n%s", err, createOut)
	}
	if strings.Contains(createErr, "Restarted the dolt sql-server") {
		t.Fatalf("restart before any upgrade:\n%s", createErr)
	}
	oldPID := readServerPIDFile(t, beadsDir)
	port := readServerPortFile(t, beadsDir)
	if !processAlive(oldPID) {
		t.Fatalf("server PID %d not running after create", oldPID)
	}

	// Upgrade dolt: the executable on PATH is replaced by one reporting a
	// new release. bd itself is unchanged.
	writeDoltVersionWrapper(t, wrapper, realDolt, "2.7.0", "upgraded install, a different size")

	listOut, listErr, err := runBDStdoutStderr(t, bdBinary, tmpDir, env, "list", "--json")
	if err != nil {
		t.Fatalf("bd list after the upgrade failed: %v\nstdout:\n%s\nstderr:\n%s", err, listOut, listErr)
	}
	var listed []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(listOut[strings.Index(listOut, "["):]), &listed); err != nil {
		t.Fatalf("parse list json (%v):\n%s", err, listOut)
	}
	found := false
	for _, is := range listed {
		found = found || is.ID == created.ID
	}
	if !found {
		t.Errorf("bd list after the restart does not include %s:\n%s", created.ID, listOut)
	}
	if n := strings.Count(listErr, "Restarted the dolt sql-server"); n != 1 {
		t.Fatalf("bd list printed %d restart notices, want one:\n%s", n, listErr)
	}
	for _, want := range []string{"port " + port, "dolt 2.6.0", "now 2.7.0"} {
		if !strings.Contains(listErr, want) {
			t.Errorf("restart notice does not mention %q:\n%s", want, listErr)
		}
	}
	if strings.Contains(listOut, "Restarted") {
		t.Errorf("restart notice on stdout breaks --json:\n%s", listOut)
	}
	newPID := readServerPIDFile(t, beadsDir)
	if newPID == oldPID {
		t.Fatalf("server PID unchanged (%d) after the upgrade", oldPID)
	}
	if got := readServerPortFile(t, beadsDir); got != port {
		t.Errorf("port moved from %s to %s", port, got)
	}
	if processAlive(oldPID) {
		t.Errorf("old server PID %d still running", oldPID)
	}
	if !processAlive(newPID) {
		t.Fatalf("replacement server PID %d not running after bd list exited", newPID)
	}

	// The replacement is current: the next command uses it, silently.
	showOut, showErr, err := runBDStdoutStderr(t, bdBinary, tmpDir, env, "show", created.ID, "--json")
	if err != nil {
		t.Fatalf("bd show after the restart failed: %v\n%s%s", err, showOut, showErr)
	}
	if !strings.Contains(showOut, created.ID) {
		t.Errorf("bd show does not return %s:\n%s", created.ID, showOut)
	}
	if strings.Contains(showErr, "Restarted the dolt sql-server") || strings.Contains(showErr, "does not restart it") {
		t.Errorf("second command after the upgrade reported the server again:\n%s", showErr)
	}
	if got := readServerPIDFile(t, beadsDir); got != newPID || !processAlive(newPID) {
		t.Errorf("server PID after the next command = %d (alive %v), want %d kept", got, processAlive(newPID), newPID)
	}
	// bd init committed .beads/ without the server's state files, the launch
	// record among them, so restarting and stopping the server leave it
	// clean.
	assertBeadsDirClean(t, tmpDir, "after the restart")

	if out, errOut, err := runBDStdoutStderr(t, bdBinary, tmpDir, env, "dolt", "stop"); err != nil {
		t.Fatalf("bd dolt stop cleanup failed: %v\n%s%s", err, out, errOut)
	}
	assertBeadsDirClean(t, tmpDir, "after bd dolt stop")
}

// assertBeadsDirClean fails the test when git status shows any change under
// repo's .beads/, untracked files included.
func assertBeadsDirClean(t *testing.T, repo, when string) {
	t.Helper()
	status, err := runCommandInDirCombinedOutput(repo, "git", "status", "--porcelain", "--untracked-files=all", "--", ".beads")
	if err != nil {
		t.Fatalf("git status %s failed: %v\n%s", when, err, status)
	}
	if status != "" {
		t.Errorf("git status shows .beads/ changes %s:\n%s", when, status)
	}
}
