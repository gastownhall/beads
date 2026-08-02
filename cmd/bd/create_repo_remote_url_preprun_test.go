package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateRemoteRepoSkipsLocalDatabaseGuard is a regression test for a gap
// left by gastownhall/beads#4615 (itself a reimplement of #3774). #4615
// taught PersistentPreRunE to resolve a *local* `--repo` path's
// workspace so `bd create --repo=<local path>` works from a directory with no
// .beads/ of its own. Remote --repo URLs were left unhandled: PreRunE still
// exited with "no beads database found" before create.go's remote-cache path
// (internal/remotecache) ever ran, even though that path opens its own store
// against the remote and needs no local database at all.
//
// This test does not exercise the real clone/pull (that needs network and a
// live dolt remote); it hides the `dolt` CLI from PATH so
// remotecache.Cache.Ensure fails fast and deterministically with "dolt CLI
// not found" instead of attempting a network call. That failure occurring at
// all is exactly what proves the earlier "no beads database found" guard got
// bypassed for a remote --repo value.
func TestCreateRemoteRepoSkipsLocalDatabaseGuard(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)

	originalReadonly := readonlyMode
	t.Cleanup(func() { readonlyMode = originalReadonly })
	readonlyMode = false

	repoFlag := createCmd.Flags().Lookup("repo")
	originalRepoValue := repoFlag.Value.String()
	originalRepoChanged := repoFlag.Changed
	t.Cleanup(func() {
		_ = repoFlag.Value.Set(originalRepoValue)
		repoFlag.Changed = originalRepoChanged
	})

	// Directory with no .beads/ workspace at all (and no .beads ancestor,
	// since it is an independent temp dir).
	noBeadsCwd := t.TempDir()
	if _, err := os.Stat(filepath.Join(noBeadsCwd, ".beads")); err == nil {
		t.Fatalf("test setup: %s unexpectedly has a .beads dir", noBeadsCwd)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(noBeadsCwd); err != nil {
		t.Fatalf("chdir(%q): %v", noBeadsCwd, err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	// Hide the `dolt` CLI so remotecache.Ensure fails fast and
	// deterministically in CGO-enabled builds, without attempting a real network
	// call. Pure-Go builds can fail earlier when the remote cache store tries to
	// use embedded Dolt, which is also downstream of the guard under test.
	emptyPathDir := t.TempDir()
	t.Setenv("PATH", emptyPathDir)

	store = nil
	dbPath = ""

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr
	t.Cleanup(func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	})

	rootCmd.SetArgs([]string{"create", "--repo", "https://doltremoteapi.dolthub.com/example/does-not-exist", "remote --repo preprun guard test"})
	execErr := rootCmd.Execute()
	rootCmd.SetArgs(nil)

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	var stdoutBuf, stderrBuf bytes.Buffer
	_, _ = stdoutBuf.ReadFrom(rOut)
	_, _ = stderrBuf.ReadFrom(rErr)
	combined := stdoutBuf.String() + stderrBuf.String()

	if execErr == nil {
		t.Fatalf("expected an error (dolt CLI is hidden from PATH), got success. Output:\n%s", combined)
	}

	if strings.Contains(combined, "no beads database found") {
		t.Fatalf("guard was not bypassed for remote --repo: got the pre-fix error.\nOutput:\n%s", combined)
	}

	if !strings.Contains(combined, "dolt CLI not found") &&
		!strings.Contains(combined, "embedded Dolt requires a CGO build") {
		t.Fatalf("expected downstream remote-cache/open failure, got:\n%s", combined)
	}
}
