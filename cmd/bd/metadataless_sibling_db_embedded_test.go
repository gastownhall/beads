//go:build cgo

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// metadatalessWorkspace is an embedded workspace whose metadata was removed
// after use: the GH#4381 shape, where a bd run resolves the .beads directory
// but finds neither metadata.json nor config.yaml.
type metadatalessWorkspace struct {
	bd, dir, beadsDir string
	metadata          []byte // the metadata.json bd init wrote
	jsonl             []byte // issues.jsonl as exported before the removal
	issueID           string // the one issue the workspace holds
}

// newMetadatalessWorkspace initializes an embedded workspace whose database is
// myproj, creates one issue, exports issues.jsonl when export is set, and
// then removes metadata.json, config.yaml and any legacy config.json.
func newMetadatalessWorkspace(t *testing.T, export bool) *metadatalessWorkspace {
	t.Helper()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "myproj")
	w := &metadatalessWorkspace{bd: bd, dir: dir, beadsDir: beadsDir}
	w.issueID = strings.TrimSpace(bdCreateSilent(t, bd, dir, "retained issue"))
	if export {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "export", "-o", filepath.Join(beadsDir, "issues.jsonl")); err != nil {
			t.Fatalf("bd export failed: %v\n%s", err, out)
		}
	}
	w.jsonl, _ = os.ReadFile(filepath.Join(beadsDir, "issues.jsonl"))
	var err error
	if w.metadata, err = os.ReadFile(filepath.Join(beadsDir, "metadata.json")); err != nil {
		t.Fatalf("setup: read metadata.json: %v", err)
	}
	for _, name := range []string{"metadata.json", "config.yaml", "config.json"} {
		if err := os.Remove(filepath.Join(beadsDir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	w.requireDatabases(t, "myproj")
	return w
}

// run runs bd in the workspace with extraEnv appended to the isolated test
// environment, returning stdout, stderr and the exit error.
func (w *metadatalessWorkspace) run(t *testing.T, extraEnv []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = append(bdEnv(w.dir), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// requireDatabases fails unless the embedded store holds exactly want.
func (w *metadatalessWorkspace) requireDatabases(t *testing.T, want ...string) {
	t.Helper()
	if got := configfile.EmbeddedDoltDatabases(w.beadsDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("embedded databases = %v, want %v", got, want)
	}
}

// requireRefused fails unless a bd run exited nonzero with the metadata-less
// refusal naming the workspace, the attempted default database, every
// database in found and the metadata remedy, and the store still holds found.
func (w *metadatalessWorkspace) requireRefused(t *testing.T, stdout, stderr string, err error, found ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("bd succeeded without metadata against a store holding %v; want the metadata-less refusal\nstdout:\n%s\nstderr:\n%s", found, stdout, stderr)
	}
	for _, want := range []string{
		w.beadsDir,
		"would open beads",
		"holds " + strings.Join(found, ", "),
		"no database was opened or created",
		filepath.Join(w.beadsDir, "metadata.json"),
		`"dolt_database"`,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not contain %q:\n%s", want, stderr)
		}
	}
	w.requireDatabases(t, found...)
}

// requireJSONLUnchanged fails unless issues.jsonl is byte-identical to the
// export taken before the metadata was removed.
func (w *metadatalessWorkspace) requireJSONLUnchanged(t *testing.T) {
	t.Helper()
	got, _ := os.ReadFile(filepath.Join(w.beadsDir, "issues.jsonl"))
	if !bytes.Equal(got, w.jsonl) {
		t.Fatalf("issues.jsonl changed:\nbefore:\n%s\nafter:\n%s", w.jsonl, got)
	}
}

// TestEmbeddedMetadatalessWorkspaceRefusesSiblingDatabase reproduces GH#4381:
// a bd run that resolves the .beads directory but no metadata must not open
// the default "beads" database beside the workspace's real one, where every
// read is false-empty and every write diverges from the configured database.
func TestEmbeddedMetadatalessWorkspaceRefusesSiblingDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	// AC-1: an ordinary read refuses before its open could create the
	// sibling, and an export cannot overwrite issues.jsonl with nothing.
	for _, read := range [][]string{
		{"list"},
		{"export", "-o", "issues.jsonl"},
	} {
		t.Run("read first "+read[0], func(t *testing.T) {
			w := newMetadatalessWorkspace(t, true)
			args := read
			if read[0] == "export" {
				args = []string{"export", "-o", filepath.Join(w.beadsDir, "issues.jsonl")}
			}
			stdout, stderr, err := w.run(t, nil, args...)
			w.requireRefused(t, stdout, stderr, err, "myproj")
			w.requireJSONLUnchanged(t)
		})
	}

	// AC-2: a mutating command refuses before it can create the sibling or
	// import issues.jsonl into it, whether or not the identity check is
	// waived, and restoring the metadata shows exactly the original issues.
	for _, tc := range []struct {
		name string
		env  []string
	}{
		{name: "write first"},
		{name: "write first identity check disabled", env: []string{"BEADS_SKIP_IDENTITY_CHECK=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newMetadatalessWorkspace(t, true)
			stdout, stderr, err := w.run(t, tc.env, "create", "--silent", "stray write")
			w.requireRefused(t, stdout, stderr, err, "myproj")
			w.requireJSONLUnchanged(t)
			w.requireOriginalIssuesAfterRestore(t)
		})
	}

	// AC-2: the base sequence that diverged writes: a read created the
	// sibling, and the next write imported issues.jsonl into it and wrote
	// there. Both now refuse. This fixture does not export over
	// issues.jsonl, so an import would have had something to hydrate.
	t.Run("read then write", func(t *testing.T) {
		w := newMetadatalessWorkspace(t, false)
		stdout, stderr, err := w.run(t, nil, "list")
		w.requireRefused(t, stdout, stderr, err, "myproj")
		stdout, stderr, err = w.run(t, []string{"BEADS_SKIP_IDENTITY_CHECK=1"}, "create", "--silent", "stray write")
		w.requireRefused(t, stdout, stderr, err, "myproj")
		w.requireJSONLUnchanged(t)
		w.requireOriginalIssuesAfterRestore(t)
	})
}

// requireOriginalIssuesAfterRestore puts the metadata back and fails unless
// the workspace lists exactly the one issue it held before the removal.
func (w *metadatalessWorkspace) requireOriginalIssuesAfterRestore(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w.beadsDir, "metadata.json"), w.metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, issue := range bdListJSON(t, w.bd, w.dir, "--all") {
		ids = append(ids, issue.ID)
	}
	if !reflect.DeepEqual(ids, []string{w.issueID}) {
		t.Fatalf("after restoring metadata.json the workspace lists %v, want only the original %s", ids, w.issueID)
	}
	w.requireDatabases(t, "myproj")
}
