package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsBlockedRepairIsDocumented pins the upgrade notes for #7037, the
// is_blocked repair of migrations 0075 and ignored 0032: the upgrading guide
// must tell apart the automatic repair, its continuation on an ordinary open,
// the manual remedy on older releases, the clone-local wisp limitation, the
// journal exemption, and what the printed count means; the changelog must
// carry a Fixed entry.
func TestIsBlockedRepairIsDocumented(t *testing.T) {
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	const heading = "\n### Stale `is_blocked` flags from migration 0059\n"
	upgrading := read("docs/getting-started/upgrading.md")
	start := strings.Index(upgrading, heading)
	if start < 0 {
		t.Fatalf("docs/getting-started/upgrading.md has no %q section", strings.TrimSpace(heading))
	}
	section := upgrading[start+len(heading):]
	if end := strings.Index(section, "\n### "); end >= 0 {
		section = section[:end]
	}
	// Whitespace-normalized, so reflowing a paragraph keeps the check.
	section = strings.Join(strings.Fields(section), " ")
	for _, want := range []string{
		"repair them automatically",              // the consented migration
		"including a read",                       // continuation on an ordinary open
		"without a prompt or `BD_ALLOW_MIGRATE`", // ...with no fresh consent
		"v1.3.4 or earlier",                      // the older-release remedy
		"bd recompute-blocked",
		"Wisps are clone-local", // the local-wisp limitation
		"mutation journal",      // the journal exemption
		"refresh their readiness snapshot",
		"not a count of rows 0059 broke", // fixpoint flips, not attribution
	} {
		if !strings.Contains(section, want) {
			t.Errorf("upgrading guide's is_blocked section does not contain %q", want)
		}
	}

	if !changelogFixedMentions(read("CHANGELOG.md"), "#7037") {
		t.Error("CHANGELOG.md has no ### Fixed entry for #7037 (is_blocked repair after 0059)")
	}
}
