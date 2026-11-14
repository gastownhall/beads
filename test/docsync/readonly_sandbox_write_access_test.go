package docsync

import "testing"

// readOnlySandboxHeading is the troubleshooting section a user in a read-only
// sandbox lands on (GH#7445).
const readOnlySandboxHeading = "### Read-only sandboxes and read-only files"

// TestReadOnlySandboxWriteAccessGuidance guards the troubleshooting guidance
// for GH#7445: embedded stores need filesystem write access to open even for
// `bd --readonly`, the refusal is permanent until access changes (so retrying
// cannot help), the diagnostic bd prints for it applies only to builds with
// the fix, and a sandbox also needs Dolt's global configuration under HOME or
// DOLT_ROOT_PATH (without it the open fails as "database not found") and the
// workspace gate files. Phrases are checked inside the section a reader lands
// on, so moving the advice elsewhere fails the test.
func TestReadOnlySandboxWriteAccessGuidance(t *testing.T) {
	guide := readRepoDoc(t, "docs", "reference", "troubleshooting.md")
	section, ok := docSubtree(guide, readOnlySandboxHeading)
	if !ok {
		t.Fatalf("docs/reference/troubleshooting.md missing the %q section", readOnlySandboxHeading)
	}
	requirePhrases(t, "troubleshooting.md "+readOnlySandboxHeading, section,
		// --readonly is application-level, not a filesystem mode.
		"blocks commands that would change issues",
		"does not let bd open the database without filesystem write access",
		".beads/embeddeddolt/",
		// Retrying cannot help until access changes.
		"permanent until access changes",
		"retrying the same command under the same permissions fails the same way",
		// The diagnostic, as the store prints it, and the builds that print it.
		"store requires write access to open",
		"embedded stores require write access even for --readonly; retrying will not help until access changes",
		"builds that include the fix for",
		"Earlier builds print only the underlying error",
		// The remedy.
		"run bd where the process can read and write",
		// The other paths a sandbox needs writable.
		"DOLT_ROOT_PATH",
		"Error 1049: database not found",
		"workspace gate acquisition failed, continuing ungated",
	)
}
