//go:build cgo

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The tests in this file pin GH#4381: a workspace whose .beads holds an
// embedded Dolt database but no metadata.json must not silently fall back to
// the default database name "beads" — that fallback creates and hydrates a
// SIBLING of the workspace's real database (myproj here) and later writes
// diverge between the two. Every metadata-less open of such a workspace is
// refused with one error naming the workspace, the attempted database, the
// databases that already exist, and the metadata remedy.

// runBDMetadataless runs a bd command with extra environment entries on top of the
// hermetic bdEnv, retrying only on embedded flock contention. It returns the
// combined output and the process error, so a refusal can be asserted on.
func runBDMetadataless(t *testing.T, bd, dir string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	var out []byte
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = append(bdEnv(dir), extraEnv...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
		if err == nil {
			return string(stdout.Bytes()), nil
		}
		out = append(stdout.Bytes(), stderr.Bytes()...)
		if !isEmbeddedLockOutput(string(out)) {
			return string(out), err
		}
		t.Logf("bd %s: flock contention (attempt %d/10), retrying...", args[0], attempt+1)
		time.Sleep(time.Duration(500*(1<<min(attempt, 4))) * time.Millisecond)
	}
	return string(out), err
}

// metadatalessFixture is a workspace initialized with prefix myproj (so its
// real database is myproj, not the default beads), one issue, and an exported
// issues.jsonl — the exact shape a user loses metadata.json from. Tests call
// removeMetadata to enter the metadata-less state the guard refuses.
type metadatalessFixture struct {
	bd        string
	dir       string
	beadsDir  string
	storeDir  string
	jsonl     string
	jsonlData []byte
	metadata  []byte
}

func newMetadatalessFixture(t *testing.T) *metadatalessFixture {
	t.Helper()
	f := &metadatalessFixture{bd: buildEmbeddedBD(t)}
	f.dir, f.beadsDir, _ = bdInit(t, f.bd, "--prefix", "myproj")
	f.storeDir = filepath.Join(f.beadsDir, "embeddeddolt")
	bdCreateSilent(t, f.bd, f.dir, "existing fixture issue")
	f.jsonl = filepath.Join(f.beadsDir, "issues.jsonl")
	out, err := bdRunWithFlockRetry(t, f.bd, f.dir, "export", "-o", f.jsonl)
	if err != nil {
		t.Fatalf("bd export failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(f.jsonl)
	if err != nil || len(data) == 0 {
		t.Fatalf("fixture export unreadable (err=%v, %d bytes)", err, len(data))
	}
	f.jsonlData = data
	return f
}

// removeMetadata deletes the workspace's configuration files, capturing
// metadata.json so a test can restore it and prove the original database is
// intact. config.json and config.yaml go too: Load's legacy fallback and the
// yaml layer must not resurface another database name behind the test's back.
func (f *metadatalessFixture) removeMetadata(t *testing.T) {
	t.Helper()
	var err error
	f.metadata, err = os.ReadFile(filepath.Join(f.beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading fixture metadata.json: %v", err)
	}
	for _, name := range []string{"metadata.json", "config.json", "config.yaml"} {
		if err := os.Remove(filepath.Join(f.beadsDir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("removing %s: %v", name, err)
		}
	}
}

// restoreMetadata puts the captured metadata.json back.
func (f *metadatalessFixture) restoreMetadata(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.beadsDir, "metadata.json"), f.metadata, 0o600); err != nil {
		t.Fatalf("restoring metadata.json: %v", err)
	}
}

// databases lists the database directories in the embedded store, sorted.
func (f *metadatalessFixture) databases(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.storeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("listing %s: %v", f.storeDir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// requireDatabases asserts the embedded store holds exactly the named
// databases — the no-sibling guarantee: a refusal must not have created or
// renamed anything under embeddeddolt.
func (f *metadatalessFixture) requireDatabases(t *testing.T, want ...string) {
	t.Helper()
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	got := f.databases(t)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		t.Fatalf("embedded store holds %v, want exactly %v", got, sorted)
	}
}

// requireRefusal asserts out/err is the metadata-less refusal for the
// attempted database against the named existing ones: a nonzero exit, the
// workspace path, the attempted name, every existing database, the
// nothing-was-opened guarantee, and the metadata remedy.
func (f *metadatalessFixture) requireRefusal(t *testing.T, out string, err error, attempted string, existing ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("command unexpectedly succeeded against the metadata-less store; want the refusal:\n%s", out)
	}
	for _, want := range append([]string{f.beadsDir, attempted, "no storage database was opened or modified", "metadata.json"}, existing...) {
		if !strings.Contains(out, want) {
			t.Errorf("refusal output must name %q:\n%s", want, out)
		}
	}
}

// TestEmbeddedMetadatalessWorkspaceRefusesSiblingDatabase (GH#4381, AC-1):
// with metadata.json gone and only myproj in the store, an ordinary read
// falls back to the default database name beads and creates a SIBLING of
// myproj. The read must instead exit nonzero with the shared refusal, create
// no sibling, and leave the exported issues.jsonl untouched.
func TestEmbeddedMetadatalessWorkspaceRefusesSiblingDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	t.Run("list read refuses and creates no sibling", func(t *testing.T) {
		f := newMetadatalessFixture(t)
		f.removeMetadata(t)

		out, err := runBDMetadataless(t, f.bd, f.dir, nil, "list")
		f.requireRefusal(t, out, err, "beads", "myproj")
		f.requireDatabases(t, "myproj")
	})

	t.Run("export read refuses and preserves issues.jsonl", func(t *testing.T) {
		f := newMetadatalessFixture(t)
		f.removeMetadata(t)

		out, err := runBDMetadataless(t, f.bd, f.dir, nil, "export", "-o", f.jsonl)
		f.requireRefusal(t, out, err, "beads", "myproj")
		f.requireDatabases(t, "myproj")

		got, readErr := os.ReadFile(f.jsonl)
		if readErr != nil || !bytes.Equal(got, f.jsonlData) {
			t.Errorf("issues.jsonl changed under a refused export (err=%v): %d bytes, want the original %d", readErr, len(got), len(f.jsonlData))
		}
	})
}

// TestEmbeddedMetadatalessWriteRefusesSiblingDatabase (GH#4381, AC-2): a
// mutating command is the worse half of the bug — the write path hydrates the
// sibling from issues.jsonl, so the workspace's data forks. It must refuse
// with the same actionable error before creating or importing anything, with
// the identity check disabled too, and restoring metadata.json must reveal
// exactly the original issues.
func TestEmbeddedMetadatalessWriteRefusesSiblingDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	t.Run("create refuses with an actionable error", func(t *testing.T) {
		f := newMetadatalessFixture(t)
		f.removeMetadata(t)

		out, err := runBDMetadataless(t, f.bd, f.dir, nil, "create", "--silent", "stray write")
		f.requireRefusal(t, out, err, "beads", "myproj")
		f.requireDatabases(t, "myproj")
	})

	t.Run("create refuses with the identity check disabled", func(t *testing.T) {
		f := newMetadatalessFixture(t)
		f.removeMetadata(t)

		out, err := runBDMetadataless(t, f.bd, f.dir, []string{"BEADS_SKIP_IDENTITY_CHECK=1"}, "create", "--silent", "stray write")
		f.requireRefusal(t, out, err, "beads", "myproj")
		f.requireDatabases(t, "myproj")
	})

	t.Run("a read then a write both refuse", func(t *testing.T) {
		f := newMetadatalessFixture(t)
		f.removeMetadata(t)

		// The read must not create the sibling the write would import into.
		out, err := runBDMetadataless(t, f.bd, f.dir, nil, "list")
		f.requireRefusal(t, out, err, "beads", "myproj")
		out, err = runBDMetadataless(t, f.bd, f.dir, []string{"BEADS_SKIP_IDENTITY_CHECK=1"}, "create", "--silent", "stray write")
		f.requireRefusal(t, out, err, "beads", "myproj")
		f.requireDatabases(t, "myproj")
	})

	t.Run("restoring metadata reveals the original issues", func(t *testing.T) {
		f := newMetadatalessFixture(t)
		f.removeMetadata(t)

		if out, err := runBDMetadataless(t, f.bd, f.dir, []string{"BEADS_SKIP_IDENTITY_CHECK=1"}, "create", "--silent", "stray write"); err == nil {
			t.Fatalf("create succeeded against the metadata-less store:\n%s", out)
		}
		f.restoreMetadata(t)

		issues := bdListJSON(t, f.bd, f.dir)
		if len(issues) != 1 || !strings.Contains(issues[0].Title, "existing fixture issue") {
			var titles []string
			for _, issue := range issues {
				titles = append(titles, issue.Title)
			}
			t.Fatalf("after restoring metadata, list = %v (%d issues), want exactly the original 1", titles, len(issues))
		}
		f.requireDatabases(t, "myproj")
	})
}
