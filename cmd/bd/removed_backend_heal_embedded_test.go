//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// setLegacyBackend rewrites metadata.json's "backend" field the way a stale
// pre-rollback workspace carries it, preserving every other field.
func setLegacyBackend(t *testing.T, beadsDir, backend string) {
	t.Helper()
	path := filepath.Join(beadsDir, configfile.ConfigFileName)
	raw, err := os.ReadFile(path) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("parse metadata.json: %v", err)
	}
	fields["backend"] = backend
	updated, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatalf("encode metadata.json: %v", err)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
}

// snapshotTree records the relative path and size of every file beneath root, so
// a test can prove a rejected command neither opened nor grew the Dolt database.
func snapshotTree(t *testing.T, root string) map[string]int64 {
	t.Helper()
	tree := map[string]int64{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			tree[rel+"/"] = 0
			return nil
		}
		tree[rel] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return tree
}

// beadsDirEntries lists the names directly under .beads, skipping the advisory
// workspace-gate lock files bd creates before it reaches the backend check.
// Those are not storage, and they are the only entries a rejected run may add.
func beadsDirEntries(t *testing.T, beadsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		t.Fatalf("read %s: %v", beadsDir, err)
	}
	var names []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".gate.lock") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// runBDExpectingRejection runs bd in dir and requires it to exit 1 (the
// documented removed-backend exit code), returning the combined output.
func runBDExpectingRejection(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd %s unexpectedly succeeded:\n%s", strings.Join(args, " "), out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("bd %s failed without an exit status: %v\n%s", strings.Join(args, " "), err, out)
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Fatalf("bd %s exit code = %d, want 1\n%s", strings.Join(args, " "), code, out)
	}
	return string(out)
}

// TestRemovedBackendHealRoundTrip is the headline D-8 test: a workspace whose
// metadata.json picked up a stale legacy "backend" value still holds its Dolt
// database, so the rejection must name the exact edit that heals it — and
// following that edit verbatim must make the workspace open again with its
// issues intact. Before D-8 the message sent this user to export the data with
// an old bd release and reinitialize, destroying a live database.
func TestRemovedBackendHealRoundTrip(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "heal")
	created := bdCreate(t, bd, dir, "issue that must survive the rejection", "--type", "task")

	doltDir := filepath.Join(beadsDir, "embeddeddolt")
	setLegacyBackend(t, beadsDir, configfile.BackendSQLite)

	metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
	metadataBefore, err := os.ReadFile(metadataPath) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	doltBefore := snapshotTree(t, doltDir)
	entriesBefore := beadsDirEntries(t, beadsDir)

	out := runBDExpectingRejection(t, bd, dir, "list", "--json")
	for _, want := range []string{
		"no longer supported",
		metadataPath,
		`"backend": "sqlite"`,
		`"backend": "dolt"`,
		doltDir,
		configfile.BackendNotOpenedGuarantee,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rejection missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(strings.ToLower(out), "export") {
		t.Errorf("rejection offered the destructive export path for a workspace with live Dolt data:\n%s", out)
	}

	// Fail-closed means exactly that: the refusal read the workspace and wrote
	// nothing — not the metadata that named the wrong backend, not the database.
	metadataAfter, err := os.ReadFile(metadataPath) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatalf("read metadata.json after rejection: %v", err)
	}
	if !bytes.Equal(metadataAfter, metadataBefore) {
		t.Errorf("rejection rewrote metadata.json:\nbefore:\n%s\nafter:\n%s", metadataBefore, metadataAfter)
	}
	if diff := treeDiff(doltBefore, snapshotTree(t, doltDir)); diff != "" {
		t.Errorf("rejection touched the Dolt database:\n%s", diff)
	}
	if got := beadsDirEntries(t, beadsDir); !equalStrings(got, entriesBefore) {
		t.Errorf("rejection added workspace entries: before %v, after %v", entriesBefore, got)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "beads.db")); !os.IsNotExist(statErr) {
		t.Errorf("rejection provisioned a SQLite database (stat error: %v)", statErr)
	}

	// Now apply exactly the edit the message prescribes.
	setLegacyBackend(t, beadsDir, configfile.BackendDolt)
	issues := bdListJSON(t, bd, dir)
	if !containsIssue(issues, created.ID) {
		t.Fatalf("after the prescribed heal, bd list does not show %s: %+v", created.ID, issues)
	}

	// The message's stated alternative — dropping the field entirely — must work
	// just as well, since Dolt is the default.
	removeBackendField(t, beadsDir)
	issues = bdListJSON(t, bd, dir)
	if !containsIssue(issues, created.ID) {
		t.Fatalf("after deleting the backend field, bd list does not show %s: %+v", created.ID, issues)
	}
}

// TestRemovedBackendHealRejectionPerBackend proves every removed-backend
// tombstone — not just sqlite — reaches the heal message when Dolt data is
// present. Only sqlite runs the full round-trip above; here we pin the rejection.
func TestRemovedBackendHealRejectionPerBackend(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	for _, backend := range []string{configfile.BackendPostgres, configfile.BackendMySQL} {
		t.Run(backend, func(t *testing.T) {
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "hl"+backend[:2])
			setLegacyBackend(t, beadsDir, backend)

			out := runBDExpectingRejection(t, bd, dir, "list", "--json")
			for _, want := range []string{
				"no longer supported",
				filepath.Join(beadsDir, configfile.ConfigFileName),
				fmt.Sprintf(`"backend": %q`, backend),
				`"backend": "dolt"`,
				configfile.BackendNotOpenedGuarantee,
			} {
				if !strings.Contains(out, want) {
					t.Errorf("%s rejection missing %q:\n%s", backend, want, out)
				}
			}
			if strings.Contains(strings.ToLower(out), "export") {
				t.Errorf("%s rejection offered the destructive export path despite live Dolt data:\n%s", backend, out)
			}
		})
	}
}

// removeBackendField deletes the "backend" key from metadata.json, the
// alternative the heal message offers.
func removeBackendField(t *testing.T, beadsDir string) {
	t.Helper()
	path := filepath.Join(beadsDir, configfile.ConfigFileName)
	raw, err := os.ReadFile(path) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("parse metadata.json: %v", err)
	}
	delete(fields, "backend")
	updated, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatalf("encode metadata.json: %v", err)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
}

func containsIssue(issues []*types.IssueWithCounts, id string) bool {
	for _, issue := range issues {
		if issue != nil && issue.Issue != nil && issue.Issue.ID == id {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// treeDiff describes how two snapshots differ, or "" when they match.
func treeDiff(before, after map[string]int64) string {
	var lines []string
	for path, size := range after {
		if prev, ok := before[path]; !ok {
			lines = append(lines, "added: "+path)
		} else if prev != size {
			lines = append(lines, fmt.Sprintf("resized: %s (%d -> %d)", path, prev, size))
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			lines = append(lines, "removed: "+path)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
