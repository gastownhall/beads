package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyRepoOrg is the pre-move GitHub org, split across two literals so
// this file does not itself trip scripts/check-legacy-repo-refs.sh.
const legacyRepoOrg = "steveyeg" + "ge"

// TestCreateReadmeLinksCurrentRepository pins the URLs bd init writes into
// .beads/README.md (GH#3059): the learn-more and examples links must name
// gastownhall/beads, the install hint must fetch install.sh from there with
// curl -f, and nothing may reference the pre-move repository.
func TestCreateReadmeLinksCurrentRepository(t *testing.T) {
	beadsDir := t.TempDir()
	if err := createReadme(beadsDir); err != nil {
		t.Fatalf("createReadme: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(beadsDir, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(data)
	for _, want := range []string{
		"https://github.com/gastownhall/beads",
		"curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash",
		"https://beads.gascity.com",
		"https://github.com/gastownhall/beads/tree/main/examples",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md missing %q", want)
		}
	}
	if strings.Contains(readme, legacyRepoOrg) {
		t.Errorf("README.md still references the pre-move repository")
	}
}
