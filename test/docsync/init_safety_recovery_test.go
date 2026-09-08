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

// TestInitSafetyCLIReferenceCoversReCloneGotchas guards the same two
// re-clone gotchas (at CLI-help brevity) in the auto-generated CLI
// reference page. The page is generated from cmd/bd/init_safety_help.go's
// Long field via scripts/generate-cli-docs.sh — edit the Go source, not
// this generated file, then regenerate.
func TestInitSafetyCLIReferenceCoversReCloneGotchas(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "docs", "cli-reference", "init-safety.md")
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
			t.Errorf("docs/cli-reference/init-safety.md missing %s: expected to find %q (edit cmd/bd/init_safety_help.go and regenerate)", c.name, c.substr)
		}
	}
}
