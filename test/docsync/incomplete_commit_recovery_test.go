package docsync

import "testing"

// TestIncompleteCommitRecoveryGuidance guards the runbook for a commit that
// leaves tracked changes in the working set or cannot verify that it did not
// (GH#5433): both typed errors and their fields, the two messages, what the
// check cannot see, and a nondestructive procedure, runnable in embedded
// mode, that never replays the mutations or treats an export as a full
// snapshot. Each assertion is scoped to the section a reader lands in.
func TestIncompleteCommitRecoveryGuidance(t *testing.T) {
	runbook := readRepoDoc(t, "docs", "recovery", "incomplete-commits.md")
	section := func(heading string) string {
		t.Helper()
		body, ok := docSubtree(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/incomplete-commits.md missing the %q section", heading)
		}
		return body
	}

	requirePhrases(t, "incomplete-commits.md", runbook,
		"whether anything is missing from HEAD is unknown",
		"does not prove that any data is absent from HEAD",
		"Neither error proves the original write failed",
	)
	requirePhrases(t, "incomplete-commits.md ## Symptoms", section("## Symptoms"),
		"commit incomplete:",
		"a commit was created, but tracked changes remain in",
		"commit verification failed:",
		"the commit may have landed, but completeness could not be verified",
		"nothing to commit; working-set cleanliness could not be verified",
		"stops before contacting the remote and prints no success line",
	)
	carry := section("## What the errors carry")
	requirePhrases(t, "incomplete-commits.md ## What the errors carry", carry,
		"*beads.UncommittedChangesError", "Tables", "IgnorePatterns", "Committed",
		"*beads.CommitVerificationError", "beads.ErrCommitVerificationFailed",
		"Outcome", "beads.CommitOutcome", "commit_succeeded", "nothing_to_commit", "Operation", "Cause",
		"neither error means the write failed",
	)
	// Library callers cannot import internal/storage; the public beads
	// package re-exports these names.
	forbidPhrases(t, "incomplete-commits.md ## What the errors carry", carry, "storage.")
	diagnosis := section("## Diagnosis")
	requirePhrases(t, "incomplete-commits.md ## Diagnosis", diagnosis,
		"only rows dolt_status shows count",
		"a new table an ignore pattern hides never appears there",
		"a hint, not proof",
		"plus `events` once the database has applied migration 0062",
		"server-mode storage does not run this check",
	)
	// Only embedded mode runs the check, and bd sql refuses there
	// (cmd/bd/sql.go), so every query and correction runs through the Dolt
	// CLI in the database directory while no bd command is running.
	requirePhrases(t, "incomplete-commits.md ## Diagnosis", diagnosis,
		"`bd sql` does not run in embedded mode",
		"only while no `bd` command is running",
		"`dolt_database` value in `.beads/metadata.json`",
		"cd .beads/embeddeddolt/<db>",
		`dolt sql -q "SELECT table_name, staged, status FROM dolt_status"`,
	)
	// bd vc status prints only the branch and HEAD commit, never the dirty
	// tables. Diagnosis may list it for those two; the recovery steps below
	// read what was left behind from dolt_status and the history from
	// dolt_log, so they never use it as a check.
	forbidPhrases(t, "incomplete-commits.md ## Diagnosis", diagnosis,
		"HEAD, and uncommitted changes",
	)
	solution := section("## Solution")
	requirePhrases(t, "incomplete-commits.md ## Solution", solution,
		"back up the whole .beads directory",
		"confirm the active database and branch",
		"compare the working-set data with HEAD",
		"do not change anything you have not confirmed",
		"cd .beads/embeddeddolt/<db>",
		`dolt sql -q "UPDATE dolt_ignore`,
		"bd dolt commit",
		"verify the history",
		`dolt sql -q "SELECT table_name, staged, status FROM dolt_status"`,
		"do not replay the mutations",
		"do not treat an export as a complete rescue snapshot",
	)
	forbidPhrases(t, "incomplete-commits.md ## Solution", solution,
		"rm -rf .beads", "bd init --force", "bd import ", "bd vc status", "bd sql",
	)
	verificationFailed := section("## If the error is commit verification failed")
	requirePhrases(t, "incomplete-commits.md ## If the error is commit verification failed", verificationFailed,
		"Operation", "Cause", "Outcome",
		"dolt_status still lists tracked tables",
		"the working set is clean",
		"do not replay the mutations",
	)
	forbidPhrases(t, "incomplete-commits.md ## If the error is commit verification failed", verificationFailed,
		"bd vc status", "bd sql",
	)
	// Canonical membership says nothing about who added a pattern. A bd sql
	// invocation anywhere on the page is one a reader cannot run.
	forbidPhrases(t, "incomplete-commits.md", runbook,
		"not set by bd", "bd did not set", "bd sql '", `bd sql "`,
	)
}
