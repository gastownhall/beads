//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// metadatalessRun runs the embedded bd binary in dir with extraEnv entries
// added to the hermetic test environment, and returns stdout and stderr
// separately. A --json command carries errors as a JSON envelope on stdout
// (the bd JSON contract) and prints warnings on stderr, so each test asserts
// on the stream its outcome belongs to.
func metadatalessRun(t *testing.T, bd, dir string, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	env := doctorTestEnv(dir)
	env = append(env, extraEnv...)
	cmd.Env = env
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// metadatalessInit initializes an embedded workspace whose database is named
// after prefix (a nondefault name, so the default "beads" can never satisfy
// the selection by accident) and returns its directory and .beads directory.
func metadatalessInit(t *testing.T, bd, prefix string) (dir, beadsDir string) {
	t.Helper()
	dir, beadsDir, _ = bdInit(t, bd, "--prefix", prefix)
	return dir, beadsDir
}

// metadatalessCreate creates one issue titled title and returns its id.
func metadatalessCreate(t *testing.T, bd, dir, title string) string {
	t.Helper()
	stdout, stderr, err := metadatalessRun(t, bd, dir, nil, "create", title, "--json")
	if err != nil {
		t.Fatalf("bd create %s failed: %v\nstdout:\n%s\nstderr:\n%s", title, err, stdout, stderr)
	}
	var created struct {
		ID string `json:"id"`
	}
	if i := strings.Index(stdout, "{"); i < 0 || json.Unmarshal([]byte(stdout[i:]), &created) != nil || created.ID == "" {
		t.Fatalf("parse bd create %s:\nstdout:\n%s\nstderr:\n%s", title, stdout, stderr)
	}
	return created.ID
}

// metadatalessRemoveMetadata saves the workspace's metadata.json bytes,
// removes the file, and returns the saved bytes for restoration. The
// metadata-less selection must never write the file, so the tested commands
// really do run against a workspace with no metadata.
func metadatalessRemoveMetadata(t *testing.T, beadsDir string) []byte {
	t.Helper()
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	saved, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read %s: %v", metadataPath, err)
	}
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("remove %s: %v", metadataPath, err)
	}
	return saved
}

// metadatalessRestoreMetadata restores bytes saved by
// metadatalessRemoveMetadata.
func metadatalessRestoreMetadata(t *testing.T, beadsDir string, saved []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), saved, 0600); err != nil {
		t.Fatalf("restore metadata.json: %v", err)
	}
}

// metadatalessRequireDatabases fails unless beadsDir holds exactly the named
// embedded databases: the metadata-less selection must neither create another
// database nor remove one.
func metadatalessRequireDatabases(t *testing.T, beadsDir string, want ...string) {
	t.Helper()
	got := embeddeddolt.Databases(beadsDir)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("embedded databases = %v, want %v", got, want)
	}
}

// metadatalessListIDs parses `bd list --json` stdout and returns the issue
// ids it reported.
func metadatalessListIDs(t *testing.T, stdout, stderr string) []string {
	t.Helper()
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("no JSON array in bd list --json stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	var issues []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout[start:]), &issues); err != nil {
		t.Fatalf("parse bd list --json stdout: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

// TestEmbeddedMetadatalessWorkspaceAdoptsSoleDatabase pins the selection rule
// for an embedded workspace whose metadata.json is missing (GH#4135, AC-1 of
// the bd-j1p0v0 design): a read adopts the sole existing database instead of
// opening — and thereby creating — the default one, names the selected
// database on stderr, and creates neither another database nor metadata.json.
func TestEmbeddedMetadatalessWorkspaceAdoptsSoleDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir := metadatalessInit(t, bd, "gh4135")
	id := metadatalessCreate(t, bd, dir, "sole database issue")
	metadatalessRemoveMetadata(t, beadsDir)

	stdout, stderr, err := metadatalessRun(t, bd, dir, nil, "list", "--json")
	if err != nil {
		t.Fatalf("bd list --json on a metadata-less workspace failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	ids := metadatalessListIDs(t, stdout, stderr)
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("bd list --json reported %v, want the sole database's issue %s", ids, id)
	}
	if !strings.Contains(stderr, "gh4135") {
		t.Errorf("bd list --json did not name the selected database on stderr:\n%s", stderr)
	}
	metadatalessRequireDatabases(t, beadsDir, "gh4135")
	if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(err) {
		t.Errorf("bd list --json created or restored metadata.json; the metadata-less selection must not write it")
	}
}
