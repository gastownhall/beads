package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file is T2.8 (BEADS-JOURNAL-PLAN.md §4.3, PR A2): a main-plane
// migration with DDL on bd_events_* fails check-migration-hygiene.sh. The
// plan names the rule "hygiene check E" and the test TestHygieneCheckE, but
// this repo's own check E already means something else (no PREPARE'd DML in
// new main-plane migrations — see that check's doc comment in
// check-migration-hygiene.sh). The rule is implemented here as a new check F
// instead, and these tests are named for what they check rather than for the
// plan's letter, to avoid misdescribing either check.

// runCheckMigrationHygiene runs check-migration-hygiene.sh in a throwaway git
// repository: baseFiles are committed on main (standing in for the PR's
// base); newFiles are then added to the working tree uncommitted (standing in
// for the PR's diff — checks C, D, E and F all diff the working tree against
// this base commit).
func runCheckMigrationHygiene(t *testing.T, baseFiles, newFiles map[string]string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("checker is a Bash boundary")
	}
	requireHostTool(t, "git")
	requireHostTool(t, "bash")
	script, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "check-migration-hygiene.sh"))
	if err != nil {
		t.Fatalf("read check-migration-hygiene.sh: %v", err)
	}
	dir := t.TempDir()
	write := func(files map[string]string) {
		for rel, content := range files {
			full := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
			}
			if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
		}
	}
	all := map[string]string{"scripts/check-migration-hygiene.sh": string(script)}
	for rel, content := range baseFiles {
		all[rel] = content
	}
	write(all)
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write(newFiles)
	cmd := exec.Command("bash", filepath.Join(dir, "scripts", "check-migration-hygiene.sh"))
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestHygieneCheckFRejectsMainPlaneJournalDDL is T2.8: a new main-plane
// migration altering bd_events_journal fails the script even though it ships
// an ignored-series twin in the same change — proving this is check F (no
// twin exemption, ever) and not merely check D (twin required) passing
// through by accident. Kills: removing check F, or weakening it to accept a
// twin the way check D does.
func TestHygieneCheckFRejectsMainPlaneJournalDDL(t *testing.T) {
	out, err := runCheckMigrationHygiene(t, nil, map[string]string{
		"internal/storage/schema/migrations/0100_add_journal_widget.up.sql":         "ALTER TABLE bd_events_journal ADD COLUMN widget VARCHAR(32);\n",
		"internal/storage/schema/migrations/ignored/0100_add_journal_widget.up.sql": "-- twin: not what check F looks at; its presence must not save the main-plane file.\nSELECT 1;\n",
	})
	if err == nil {
		t.Fatalf("main-plane DDL on bd_events_journal was accepted even with an ignored twin:\n%s", out)
	}
	if !strings.Contains(out, "FAIL (journal plane)") {
		t.Errorf("expected a \"FAIL (journal plane)\" report, got:\n%s", out)
	}
	if !strings.Contains(out, "0100_add_journal_widget.up.sql") {
		t.Errorf("expected the offending file named in output, got:\n%s", out)
	}
}

// TestHygieneCheckFRejectsMainPlaneEventsSeqDDL pins that the rule covers
// bd_events_seq too, not only bd_events_journal.
func TestHygieneCheckFRejectsMainPlaneEventsSeqDDL(t *testing.T) {
	out, err := runCheckMigrationHygiene(t, nil, map[string]string{
		"internal/storage/schema/migrations/0100_rename_events_seq.up.sql": "ALTER TABLE bd_events_seq ADD COLUMN note VARCHAR(8);\n",
	})
	if err == nil {
		t.Fatalf("main-plane DDL on bd_events_seq was accepted:\n%s", out)
	}
	if !strings.Contains(out, "FAIL (journal plane)") {
		t.Errorf("expected a \"FAIL (journal plane)\" report, got:\n%s", out)
	}
}

// TestHygieneCheckFAllowsMainPlaneDDLOnNonJournalTables is the negative
// control: a main-plane migration that never mentions a journal table passes
// cleanly, so check F is not a blanket rejection of main-plane DDL.
func TestHygieneCheckFAllowsMainPlaneDDLOnNonJournalTables(t *testing.T) {
	out, err := runCheckMigrationHygiene(t, nil, map[string]string{
		"internal/storage/schema/migrations/0100_add_issues_widget.up.sql": "ALTER TABLE issues ADD COLUMN widget VARCHAR(32);\n",
	})
	if err != nil {
		t.Fatalf("main-plane DDL on a non-journal table was rejected: %v\n%s", err, out)
	}
	if strings.Contains(out, "FAIL (journal plane)") {
		t.Errorf("did not expect a journal-plane failure, got:\n%s", out)
	}
}

// TestHygieneCheckFIgnoresIgnoredPlaneJournalDDL confirms the rule is
// main-plane-only: the same DDL shipped as an ignored-series migration (the
// only place journal tables are allowed to change, PR A2) passes.
func TestHygieneCheckFIgnoresIgnoredPlaneJournalDDL(t *testing.T) {
	out, err := runCheckMigrationHygiene(t, nil, map[string]string{
		"internal/storage/schema/migrations/ignored/0100_add_journal_widget.up.sql": "ALTER TABLE bd_events_journal ADD COLUMN widget VARCHAR(32);\n",
	})
	if err != nil {
		t.Fatalf("ignored-plane DDL on bd_events_journal was rejected: %v\n%s", err, out)
	}
	if strings.Contains(out, "FAIL (journal plane)") {
		t.Errorf("did not expect a journal-plane failure for an ignored-series migration, got:\n%s", out)
	}
}
