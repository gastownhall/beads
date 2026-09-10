package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Built via concatenation so this file's own source never contains the
// literal substring check-testing-short.sh scans for — otherwise this test
// file would flag itself when TestCheckTestingShortPassesOnCleanRepoTree
// runs the checker against the real repo tree.
const shortCall = "testing" + ".Short()"

func TestCheckTestingShortIgnoresCommentOnlyMention(t *testing.T) {
	out, err := runCheckTestingShort(t, map[string]string{
		"pkg_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {\n\t// this comment mentions " + shortCall + " and should not fail the gate\n}\n",
	})
	if err != nil {
		t.Fatalf("comment-only mention of %s should not fail the gate; err=%v output=%s", shortCall, err, out)
	}
}

func TestCheckTestingShortStillFlagsRealCallAndNamesFunc(t *testing.T) {
	out, err := runCheckTestingShort(t, map[string]string{
		"pkg_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestBar(t *testing.T) {\n\tif " + shortCall + " {\n\t\tt.Skip(\"slow\")\n\t}\n}\n",
	})
	if err == nil {
		t.Fatalf("real %s call should still fail the gate; output=%s", shortCall, out)
	}
	if !strings.Contains(out, "TestBar") {
		t.Errorf("expected offending function TestBar named in output, got: %s", out)
	}
}

func TestCheckTestingShortReportsUnknownAboveFirstFunc(t *testing.T) {
	out, err := runCheckTestingShort(t, map[string]string{
		"pkg_test.go": "package pkg\n\nimport \"testing\"\n\nvar shortMode = " + shortCall + "\n\nfunc TestBaz(t *testing.T) {}\n",
	})
	if err == nil {
		t.Fatalf("real %s call above any func should still fail the gate; output=%s", shortCall, out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected 'unknown' function name for a hit above the first func, got: %s", out)
	}
	if strings.Contains(out, "TestBaz") {
		t.Errorf("hit above the first func must not be attributed to a later func TestBaz: %s", out)
	}
}

func TestCheckTestingShortReportsUnknownBetweenFunctions(t *testing.T) {
	out, err := runCheckTestingShort(t, map[string]string{
		"pkg_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tt.Log(\"a\")\n}\n\nvar shortAgain = " + shortCall + "\n\nfunc TestB(t *testing.T) {}\n",
	})
	if err == nil {
		t.Fatalf("real %s call between two functions should still fail the gate; output=%s", shortCall, out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected 'unknown' function name for a hit between two functions (not truly inside either body), got: %s", out)
	}
	if strings.Contains(out, "TestA") {
		t.Errorf("hit between two functions must not be attributed to the preceding func TestA: %s", out)
	}
}

func TestCheckTestingShortPassesOnCleanRepoTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checker is a Bash boundary")
	}
	repo := sourceRepoRoot(t)
	cmd := exec.Command("bash", filepath.Join(repo, "scripts", "check-testing-short.sh"))
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check-testing-short.sh failed on clean repo tree (all 9 allowlist entries must still be recognized): %v\noutput: %s", err, out)
	}
}

func runCheckTestingShort(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("checker is a Bash boundary")
	}
	dir := t.TempDir()
	// A second, innocuous .go file keeps find+grep invoked with 2+ filenames,
	// matching real usage (repo root always has many .go files): grep only
	// prefixes matches with the filename when given more than one file, so a
	// single-fixture temp dir would silently corrupt the file:line parse in
	// check-testing-short.sh's read loop.
	if err := os.WriteFile(filepath.Join(dir, "companion.go"), []byte("package pkg\n"), 0600); err != nil {
		t.Fatalf("write companion.go: %v", err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	script := filepath.Join(sourceRepoRoot(t), "scripts", "check-testing-short.sh")
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
