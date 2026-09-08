package docsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitSafetyRecoveryDocCoversReCloneGotchas guards two gotchas discovered
// during live recovery incident ga-vrq5pu:
//
//  1. A damaged/set-aside Dolt database directory left INSIDE the
//     sql-server's data_dir crash-loops the server, because every
//     subdirectory of data_dir is treated as its own database. Symptom:
//     "root hash doesn't exist: <hash>".
//  2. A fresh clone lacks dolt-ignored clone-local tables (leases, wisps,
//     events, local_metadata, etc.) until `bd migrate schema` has run once.
//     Symptom: "table not found: leases". The fix's "Schema already at v64"
//     output is the expected, reassuring result — not an error.
func TestInitSafetyRecoveryDocCoversReCloneGotchas(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "docs", "recovery", "init-safety.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	lower := strings.ToLower(string(data))

	cases := []struct {
		name   string
		substr string
	}{
		{"damaged-store crash-loop symptom", "root hash doesn't exist"},
		{"fresh-clone missing-table symptom", "table not found: leases"},
		{"fresh-clone fix command", "bd migrate schema"},
		{"fresh-clone reassuring success message", "schema already at v64"},
	}
	for _, c := range cases {
		if !strings.Contains(lower, strings.ToLower(c.substr)) {
			t.Errorf("docs/recovery/init-safety.md missing %s: expected to find %q", c.name, c.substr)
		}
	}

	if !strings.Contains(lower, "outside") || !strings.Contains(lower, "data_dir") {
		t.Errorf("docs/recovery/init-safety.md must say damaged/set-aside stores go OUTSIDE data_dir (the sql-server treats every data_dir subdirectory as a database)")
	}
}

// TestInitSafetyCLIHelpCoversReCloneGotchas guards the same two re-clone
// gotchas (at CLI-help brevity) in cmd/bd/init_safety_help.go's Long field,
// the generator source for docs/cli-reference/init-safety.md.
//
// The generated page itself is intentionally NOT checked here: per
// docs/cli-docs.pin, docs/cli-reference/ is regenerated from a pinned
// *released* bd tag (built in a detached worktree), not from this checkout,
// so a Long-field edit at HEAD does not flow into the committed generated
// page until a maintainer bumps the pin as part of a release (see
// docs/cli-docs.pin's own header comment and scripts/resolve-docs-bd.sh).
// CI's generated-docs drift gate (scripts/check-cli-docs-drift.sh, driven by
// scripts/check-doc-flags.sh) is blame-scoped for exactly this reason: it
// does not fail a PR whose regenerated CLI surface is unchanged from the
// merge-base. Testing the Long field directly checks the content a PR can
// actually change and that will ship in the doc at the next pin bump.
func TestInitSafetyCLIHelpCoversReCloneGotchas(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "cmd", "bd", "init_safety_help.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	lower := strings.ToLower(string(data))

	cases := []struct {
		name   string
		substr string
	}{
		{"damaged-store crash-loop symptom", "root hash doesn't exist"},
		{"fresh-clone missing-table symptom", "table not found: leases"},
		{"fresh-clone fix command", "bd migrate schema"},
	}
	for _, c := range cases {
		if !strings.Contains(lower, strings.ToLower(c.substr)) {
			t.Errorf("cmd/bd/init_safety_help.go missing %s: expected to find %q in the Long help text", c.name, c.substr)
		}
	}
}
