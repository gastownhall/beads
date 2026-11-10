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

// metadatalessShowStatus reports the status bd show prints for id, parsed
// from its --json payload (a JSON array, like bd list --json).
func metadatalessShowStatus(t *testing.T, bd, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	stdout, stderr, err := metadatalessRun(t, bd, dir, extraEnv, append([]string{"show"}, args...)...)
	if err != nil {
		t.Fatalf("bd show %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("no JSON array in bd show %v stdout:\n%s\nstderr:\n%s", args, stdout, stderr)
	}
	var shown []struct {
		Status string `json:"status"`
	}
	if jsonErr := json.Unmarshal([]byte(stdout[start:]), &shown); jsonErr != nil || len(shown) != 1 {
		t.Fatalf("parse bd show %v: %v\nstdout:\n%s\nstderr:\n%s", args, jsonErr, stdout, stderr)
	}
	return shown[0].Status
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

// TestEmbeddedMetadatalessWorkspaceCloseFirstCommandPersistsToSoleDatabase
// pins GH#4135 AC-2: with metadata.json removed, `bd close <id>` as the very
// first command must write to the sole existing database. At base the default
// open creates an empty "beads" database and the close fails there (or, with
// an export fixture, lands in that phantom), so restoring metadata shows the
// issue still open. The JSONL export fixture subtest pins the
// acknowledged-write-to-phantom variant: list first, then close, then restore
// metadata — the close must be in the real database, not in a phantom the
// list created.
func TestEmbeddedMetadatalessWorkspaceCloseFirstCommandPersistsToSoleDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir := metadatalessInit(t, bd, "gh4135")
	id := metadatalessCreate(t, bd, dir, "close as first command")
	saved := metadatalessRemoveMetadata(t, beadsDir)

	stdout, stderr, err := metadatalessRun(t, bd, dir, nil, "close", id, "-r", "metadata-less close")
	if err != nil {
		t.Fatalf("bd close as the first command after metadata removal failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	metadatalessRequireDatabases(t, beadsDir, "gh4135")

	// The write must be in the original database: metadata names it, so a
	// fresh process reading through the restored metadata reports closed.
	metadatalessRestoreMetadata(t, beadsDir, saved)
	if status := metadatalessShowStatus(t, bd, dir, nil, id, "--json"); status != "closed" {
		t.Fatalf("issue %s status = %q after a metadata-less close and metadata restore, want closed (the close went to a different database)", id, status)
	}
}

// TestEmbeddedMetadatalessWorkspaceCloseAfterExportFixturePersistsToSoleDatabase
// is the list-then-close variant of GH#4135 AC-2: with an explicit JSONL
// export in place, the read must adopt the sole database, and the close that
// follows must persist there — never into a phantom the read created, where
// the acknowledged close would be lost on metadata restore.
func TestEmbeddedMetadatalessWorkspaceCloseAfterExportFixturePersistsToSoleDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir := metadatalessInit(t, bd, "gh4135")
	id := metadatalessCreate(t, bd, dir, "export fixture issue")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if stdout, stderr, err := metadatalessRun(t, bd, dir, nil, "export", "-o", jsonlPath); err != nil {
		t.Fatalf("bd export fixture failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	saved := metadatalessRemoveMetadata(t, beadsDir)

	// List first: at base this creates the empty default database and reports
	// no issues; at head it adopts the sole database and reports the issue.
	stdout, stderr, err := metadatalessRun(t, bd, dir, nil, "list", "--json")
	if err != nil {
		t.Fatalf("bd list --json after metadata removal failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	ids := metadatalessListIDs(t, stdout, stderr)
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("bd list --json reported %v, want the sole database's issue %s", ids, id)
	}
	if stdout, stderr, err = metadatalessRun(t, bd, dir, nil, "close", id, "-r", "list then close"); err != nil {
		t.Fatalf("bd close after metadata removal failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	metadatalessRequireDatabases(t, beadsDir, "gh4135")

	metadatalessRestoreMetadata(t, beadsDir, saved)
	if status := metadatalessShowStatus(t, bd, dir, nil, id, "--json"); status != "closed" {
		t.Fatalf("issue %s status = %q after list-then-close on a metadata-less workspace, want closed (the acknowledged close went to a phantom database)", id, status)
	}
}
