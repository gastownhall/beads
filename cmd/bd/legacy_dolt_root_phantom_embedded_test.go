//go:build cgo

package main

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

// TestUnownedDoltRootNeverOpensPhantomEmbeddedDatabase reproduces
// gastownhall/beads#6259: a .beads/dolt data root with no
// .beads/embeddeddolt repository and no server-mode selection. The
// current-era version witness admits it through the legacy-upgrade guard,
// and store open then creates a brand-new embedded database beside the real
// data root and answers from it. bd must refuse instead: nothing owns that
// root, and an empty answer is indistinguishable from real emptiness.
func TestUnownedDoltRootNeverOpensPhantomEmbeddedDatabase(t *testing.T) {
	bd := buildBDUnderTest(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "hq", ".dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"hq"}`))
	writeFile(t, filepath.Join(beadsDir, localVersionFile), []byte("1.3.0\n"))
	before := legacyworkspace.TreeDigest(t, beadsDir)

	run := runBDInWorkspace(t, bd, repoDir,
		[]string{"list", "--json", "--limit", "0", "--all"},
		[]string{"HOME=" + t.TempDir()}, "")
	assertNoPhantomEmbeddedDatabase(t, beadsDir)
	if run.err == nil {
		t.Fatalf("bd list exited 0 over a .beads/dolt root nothing owns:\n%s", run.output)
	}
	if !strings.Contains(run.output, unownedDoltRootRefusal) {
		t.Fatalf("bd list did not refuse with the unowned-root text %q:\n%s", unownedDoltRootRefusal, run.output)
	}
	if strings.Contains(run.stdout, "No issues found.") {
		t.Fatalf("bd list reported a false-empty result over an unowned .beads/dolt root:\n%s", run.output)
	}
	if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
		t.Fatalf("bd list changed the refused workspace: before %v, after %v", before, after)
	}
}
