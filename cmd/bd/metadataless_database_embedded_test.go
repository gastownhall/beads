//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
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

// listedIDs decodes run's `bd list --json` stdout, which must be exactly one
// JSON array, and returns the issue ids it lists.
func listedIDs(t *testing.T, run metadatalessRun) []string {
	t.Helper()
	var issues []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &issues); err != nil {
		t.Fatalf("bd list --json stdout is not one JSON array: %v\nstdout:\n%s\nstderr:\n%s", err, run.stdout, run.stderr)
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
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
		if ids := listedIDs(t, run); !slices.Equal(ids, []string{issue.ID}) {
			t.Fatalf("bd list --json listed %v, want only %s from the workspace's database\nstderr:\n%s", ids, issue.ID, run.stderr)
		}
		if !strings.Contains(run.stderr, `"gh4135"`) {
			t.Fatalf("stderr does not name the selected database \"gh4135\":\n%s", run.stderr)
		}
		assertEmbeddedDatabases(t, beadsDir, "gh4135")
		assertMetadataAbsent(t, beadsDir)
	})

	// The close is the first command after metadata.json goes: the identity
	// preview and the real open must both reach the workspace's database, so
	// restoring metadata.json shows the close it acknowledged.
	t.Run("close first", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gh4135")
		issue := bdCreate(t, bd, dir, "Closed while metadata was lost")
		saved := removeMetadata(t, beadsDir)

		run := runBDMetadataless(t, bd, dir, nil, "close", issue.ID)
		if run.err != nil {
			t.Fatalf("bd close %s failed: %v\nstdout:\n%s\nstderr:\n%s", issue.ID, run.err, run.stdout, run.stderr)
		}
		assertEmbeddedDatabases(t, beadsDir, "gh4135")
		restoreMetadata(t, beadsDir, saved)
		if got := bdShow(t, bd, dir, issue.ID); got.Status != "closed" {
			t.Fatalf("%s status = %q in the original database after the acknowledged close, want closed", issue.ID, got.Status)
		}
	})

	// The report's sequence: a read creates the phantom, the git-tracked
	// export then seeds it on the next write, and the close lands there
	// instead of in the database metadata.json names again once restored.
	t.Run("list then close with a JSONL export", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gh4135")
		issue := bdCreate(t, bd, dir, "Exported before metadata was lost")
		if out, err := bdRunWithFlockRetry(t, bd, dir, "export", "-o", filepath.Join(beadsDir, "issues.jsonl")); err != nil {
			t.Fatalf("bd export failed: %v\n%s", err, out)
		}
		saved := removeMetadata(t, beadsDir)

		if run := runBDMetadataless(t, bd, dir, nil, "list", "--json"); run.err != nil {
			t.Fatalf("bd list --json failed: %v\nstdout:\n%s\nstderr:\n%s", run.err, run.stdout, run.stderr)
		}
		run := runBDMetadataless(t, bd, dir, nil, "close", issue.ID)
		if run.err != nil {
			t.Fatalf("bd close %s failed: %v\nstdout:\n%s\nstderr:\n%s", issue.ID, run.err, run.stdout, run.stderr)
		}
		restoreMetadata(t, beadsDir, saved)
		if got := bdShow(t, bd, dir, issue.ID); got.Status != "closed" {
			t.Fatalf("%s status = %q in the original database after the acknowledged close, want closed", issue.ID, got.Status)
		}
		assertEmbeddedDatabases(t, beadsDir, "gh4135")
	})
}

// severalDatabasesFixture builds an embedded workspace whose own database,
// gh4135, holds one open issue, adds a second database named second (copied
// out of a separate workspace, with an open issue of its own), and removes
// metadata.json. It returns the workspace, its .beads directory, the removed
// metadata, and the issue in each database.
func severalDatabasesFixture(t *testing.T, bd, second string) (dir, beadsDir string, saved []byte, ownID, secondID string) {
	t.Helper()
	dir, beadsDir, _ = bdInit(t, bd, "--prefix", "gh4135")
	ownID = bdCreate(t, bd, dir, "In the workspace's own database").ID
	otherDir, otherBeadsDir, _ := bdInit(t, bd, "--prefix", second)
	secondID = bdCreate(t, bd, otherDir, "In the second database").ID
	if err := copyTree(filepath.Join(otherBeadsDir, "embeddeddolt", second), filepath.Join(beadsDir, "embeddeddolt", second)); err != nil {
		t.Fatal(err)
	}
	saved = removeMetadata(t, beadsDir)
	want := []string{"gh4135", second}
	slices.Sort(want)
	assertEmbeddedDatabases(t, beadsDir, want...)
	return dir, beadsDir, saved, ownID, secondID
}

// assertIssueStatusIn reads id's status out of the named database of the
// workspace in dir, which has its metadata.json.
func assertIssueStatusIn(t *testing.T, bd, dir, database, id, want string) {
	t.Helper()
	run := runBDMetadataless(t, bd, dir, []string{"BEADS_DOLT_SERVER_DATABASE=" + database}, "show", id, "--json")
	if run.err != nil {
		t.Fatalf("bd show %s in %s failed: %v\nstdout:\n%s\nstderr:\n%s", id, database, run.err, run.stdout, run.stderr)
	}
	if got := parseIssueJSON(t, []byte(run.stdout)); got.Status != types.Status(want) {
		t.Fatalf("%s status in %s = %q, want %q", id, database, got.Status, want)
	}
}

// TestEmbeddedMetadatalessWorkspaceRefusesSeveralDatabases: a workspace that
// lost metadata.json and holds several databases names no database a command
// could safely pick, the default "beads" included, so reads and writes refuse
// before opening one and say how to select it.
func TestEmbeddedMetadatalessWorkspaceRefusesSeveralDatabases(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)

	for _, second := range []string{"second", "beads"} {
		for _, tc := range []struct {
			name string
			args func(ownID string) []string
		}{
			{name: "list", args: func(string) []string { return []string{"list", "--json"} }},
			{name: "readonly list", args: func(string) []string { return []string{"list", "--json", "--readonly"} }},
			{name: "close", args: func(ownID string) []string { return []string{"close", ownID, "--json"} }},
		} {
			t.Run(second+"/"+tc.name, func(t *testing.T) {
				dir, beadsDir, saved, ownID, secondID := severalDatabasesFixture(t, bd, second)
				args := tc.args(ownID)

				run := runBDMetadataless(t, bd, dir, nil, args...)
				if run.err == nil {
					t.Fatalf("bd %s succeeded in a metadata-less workspace holding several databases:\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), run.stdout, run.stderr)
				}
				if strings.TrimSpace(run.stdout) != "" {
					t.Fatalf("refused bd %s printed a payload:\n%s", strings.Join(args, " "), run.stdout)
				}
				for _, want := range []string{"gh4135", second, "metadata.json", "BEADS_DOLT_SERVER_DATABASE"} {
					if !strings.Contains(run.stderr, want) {
						t.Fatalf("refusal does not mention %q:\n%s", want, run.stderr)
					}
				}
				want := []string{"gh4135", second}
				slices.Sort(want)
				assertEmbeddedDatabases(t, beadsDir, want...)
				assertMetadataAbsent(t, beadsDir)

				restoreMetadata(t, beadsDir, saved)
				assertIssueStatusIn(t, bd, dir, "gh4135", ownID, "open")
				assertIssueStatusIn(t, bd, dir, second, secondID, "open")
			})
		}
	}
}

// TestEmbeddedMetadatalessWorkspaceEnvVarSelectsDatabase: in a workspace that
// lost metadata.json, BEADS_DOLT_SERVER_DATABASE selects one of the databases
// it holds, for the identity preview and the real open alike, and one it does
// not hold is refused rather than created.
func TestEmbeddedMetadatalessWorkspaceEnvVarSelectsDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)

	t.Run("matching", func(t *testing.T) {
		dir, beadsDir, saved, ownID, secondID := severalDatabasesFixture(t, bd, "second")
		env := []string{"BEADS_DOLT_SERVER_DATABASE=second"}

		run := runBDMetadataless(t, bd, dir, env, "list", "--json")
		if run.err != nil {
			t.Fatalf("bd list --json failed: %v\nstdout:\n%s\nstderr:\n%s", run.err, run.stdout, run.stderr)
		}
		if !strings.Contains(run.stdout, `"`+secondID+`"`) || strings.Contains(run.stdout, `"`+ownID+`"`) {
			t.Fatalf("bd list --json did not read the selected database \"second\":\n%s", run.stdout)
		}
		if !strings.Contains(run.stderr, `"second"`) || !strings.Contains(run.stderr, "BEADS_DOLT_SERVER_DATABASE") {
			t.Fatalf("stderr does not name the selected database and its source:\n%s", run.stderr)
		}

		run = runBDMetadataless(t, bd, dir, env, "close", secondID)
		if run.err != nil {
			t.Fatalf("bd close %s failed: %v\nstdout:\n%s\nstderr:\n%s", secondID, run.err, run.stdout, run.stderr)
		}
		assertEmbeddedDatabases(t, beadsDir, "gh4135", "second")
		assertMetadataAbsent(t, beadsDir)

		restoreMetadata(t, beadsDir, saved)
		assertIssueStatusIn(t, bd, dir, "second", secondID, "closed")
		assertIssueStatusIn(t, bd, dir, "gh4135", ownID, "open")
	})

	t.Run("nonmatching", func(t *testing.T) {
		dir, beadsDir, saved, ownID, secondID := severalDatabasesFixture(t, bd, "second")
		env := []string{"BEADS_DOLT_SERVER_DATABASE=missing"}

		for _, args := range [][]string{{"list", "--json"}, {"close", ownID, "--json"}} {
			run := runBDMetadataless(t, bd, dir, env, args...)
			if run.err == nil {
				t.Fatalf("bd %s succeeded with an override naming no database:\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), run.stdout, run.stderr)
			}
			if strings.TrimSpace(run.stdout) != "" {
				t.Fatalf("refused bd %s printed a payload:\n%s", strings.Join(args, " "), run.stdout)
			}
			for _, want := range []string{"BEADS_DOLT_SERVER_DATABASE=missing", "gh4135", "second"} {
				if !strings.Contains(run.stderr, want) {
					t.Fatalf("refusal of bd %s does not mention %q:\n%s", strings.Join(args, " "), want, run.stderr)
				}
			}
			assertEmbeddedDatabases(t, beadsDir, "gh4135", "second")
		}

		restoreMetadata(t, beadsDir, saved)
		assertIssueStatusIn(t, bd, dir, "gh4135", ownID, "open")
		assertIssueStatusIn(t, bd, dir, "second", secondID, "open")
	})
}
