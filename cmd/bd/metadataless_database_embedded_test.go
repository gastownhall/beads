//go:build cgo

package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// metadatalessRun is one bd invocation in a metadata-less workspace.
type metadatalessRun struct {
	stdout, stderr string
	err            error
}

// runBDMetadataless runs bd in dir with the hermetic embedded environment plus
// extraEnv, keeping stdout and stderr apart.
func runBDMetadataless(t *testing.T, bd, dir string, extraEnv []string, args ...string) metadatalessRun {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), extraEnv...)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	return metadatalessRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// removeMetadata deletes beadsDir's metadata.json and returns its bytes so a
// test can restore it.
func removeMetadata(t *testing.T, beadsDir string) []byte {
	t.Helper()
	path := configfile.MetadataFilePath(beadsDir)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return saved
}

func restoreMetadata(t *testing.T, beadsDir string, saved []byte) {
	t.Helper()
	if err := os.WriteFile(configfile.MetadataFilePath(beadsDir), saved, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertMetadataAbsent(t *testing.T, beadsDir string) {
	t.Helper()
	if _, err := os.Stat(configfile.MetadataFilePath(beadsDir)); !os.IsNotExist(err) {
		t.Fatalf("metadata.json was recreated (stat err %v)", err)
	}
}

func assertEmbeddedDatabases(t *testing.T, beadsDir string, want ...string) {
	t.Helper()
	if got := embeddeddolt.Databases(beadsDir); !slices.Equal(got, want) {
		t.Fatalf("embedded databases = %v, want %v", got, want)
	}
}

// TestEmbeddedMetadatalessWorkspaceAdoptsSoleDatabase pins GH#4135's
// split-brain: an embedded workspace that lost metadata.json opened the
// literal default database "beads", creating it beside the workspace's own,
// and answered reads empty out of it.
func TestEmbeddedMetadatalessWorkspaceAdoptsSoleDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)

	t.Run("list", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gh4135")
		issue := bdCreate(t, bd, dir, "Tracked before metadata was lost")
		removeMetadata(t, beadsDir)

		run := runBDMetadataless(t, bd, dir, nil, "list", "--json")
		if run.err != nil {
			t.Fatalf("bd list --json failed: %v\nstdout:\n%s\nstderr:\n%s", run.err, run.stdout, run.stderr)
		}
		if !strings.Contains(run.stdout, `"`+issue.ID+`"`) {
			t.Fatalf("bd list --json did not return %s from the workspace's database:\nstdout:\n%s\nstderr:\n%s", issue.ID, run.stdout, run.stderr)
		}
		if !strings.Contains(run.stderr, `"gh4135"`) {
			t.Fatalf("stderr does not name the selected database \"gh4135\":\n%s", run.stderr)
		}
		assertEmbeddedDatabases(t, beadsDir, "gh4135")
		assertMetadataAbsent(t, beadsDir)
	})
}
