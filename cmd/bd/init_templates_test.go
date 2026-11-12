package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateReadmeLinksCurrentRepository: the .beads/README.md that bd init
// writes links gastownhall/beads, not the pre-move steveyegge/beads, and its
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
	if strings.Contains(readme, "steveyegge") {
		t.Errorf("README.md still names the pre-move steveyegge repository:\n%s", readme)
	}
}
