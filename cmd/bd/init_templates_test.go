package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyRepoOrg is the pre-move GitHub org (GH#3059), split so that
// scripts/check-legacy-repo-refs.sh does not flag the absence check below.
const legacyRepoOrg = "steve" + "yegge"

// TestCreateReadmeLinksCurrentRepository: the .beads/README.md that bd init
// writes links gastownhall/beads, not the pre-move repository, and its
// install hint fails on an HTTP error instead of piping it into bash (GH#3059).
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
			t.Errorf("README.md lacks %q", want)
		}
	}
	if strings.Contains(readme, legacyRepoOrg) {
		t.Errorf("README.md still names the pre-move repository:\n%s", readme)
	}
}

// TestRenderInitConfigYAMLOmitsNoDB: the config.yaml bd init writes does not
// advertise no-db or a JSONL-only backend. Issues live in Dolt; JSONL is an
// export (GH#7387).
func TestRenderInitConfigYAMLOmitsNoDB(t *testing.T) {
	body := string(proxiedInitConfigYAMLBody(t.TempDir()))
	if body == "" {
		t.Fatal("no init config.yaml template rendered")
	}
	for _, unwanted := range []string{"no-db", "JSONL-only"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("init config.yaml template mentions %q:\n%s", unwanted, body)
		}
	}
}
