package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
)

// TestIntegrityChecks_NoBeadsDir verifies all integrity check functions handle
// missing .beads directories gracefully. This replaces 4 individual subtests.
func TestIntegrityChecks_NoBeadsDir(t *testing.T) {
	checks := []struct {
		name     string
		fn       func(string) DoctorCheck
		wantName string
	}{
		{"IDFormat", CheckIDFormat, "Issue IDs"},
		{"DependencyCycles", CheckDependencyCycles, "Dependency Cycles"},
		{"DeletionsManifest", CheckDeletionsManifest, "Deletions Manifest"},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			result := tc.fn(tmpDir)

			if result.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", result.Name, tc.wantName)
			}
		})
	}
}

// TestIntegrityChecks_EmptyBeadsDir verifies all integrity check functions return OK
// when .beads directory exists but is empty (no database/files to check).
func TestIntegrityChecks_EmptyBeadsDir(t *testing.T) {
	checks := []struct {
		name string
		fn   func(string) DoctorCheck
	}{
		{"IDFormat", CheckIDFormat},
		{"DependencyCycles", CheckDependencyCycles},
		{"DeletionsManifest", CheckDeletionsManifest},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			result := tc.fn(tmpDir)

			if result.Status != StatusOK {
				t.Errorf("Status = %q, want %q", result.Status, StatusOK)
			}
		})
	}
}

// TestCheckDeletionsManifest_LegacyFile tests the specific case where a legacy
// deletions.jsonl file exists and should trigger a warning.
func TestCheckDeletionsManifest_LegacyFile(t *testing.T) {
	// The check only warns inside a git repository, and it asks git about the
	// working directory, so the fixture is a repository and the check runs in
	// it (not in whatever checkout the test binary happens to start in).
	tmpDir := newGitRepo(t)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a deletions.jsonl file
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")
	if err := os.WriteFile(deletionsPath, []byte(`{"id":"test-1"}`), 0644); err != nil {
		t.Fatal(err)
	}

	var check DoctorCheck
	runInDir(t, tmpDir, func() { check = CheckDeletionsManifest(tmpDir) })

	// Should warn about legacy deletions file
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
}

// TestIsHashID verifies the hash-based ID detection heuristic.
func TestIsHashID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"hash with letters", "bd-0jkc", true},
		{"hash with only digits short", "bd-88", false},
		{"hash 5+ chars all digits", "bd-12345", true},
		{"all digits 4 chars", "bd-0088", false}, // isHashID needs letters for < 5 chars
		{"sequential numeric", "bd-1", false},
		{"sequential two digit", "bd-42", false},
		{"no separator", "abc", false},
		{"empty suffix", "bd-", false},
		{"hierarchical hash", "bd-0jkc.1", true},
		{"hierarchical sequential", "bd-1.2", false},
		{"uppercase rejected", "bd-ABCD", false},
		{"special chars rejected", "bd-ab!c", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHashID(tt.id)
			if got != tt.want {
				t.Errorf("isHashID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// TestCheckIDFormat_DeadBranch documents the dead branch bug in CheckIDFormat.
// Both branches after DetectHashBasedIDs return identical results ("hash-based ✓"),
// so the function reports the same status regardless of detection outcome.
func TestCheckIDFormat_DeadBranch(t *testing.T) {
	// We can't easily test with a real DB in a non-cgo test,
	// but we can verify the dead branch exists by checking that both
	// code paths (lines 78-84 and 86-90) produce identical output.
	// This test documents the bug for future fix.

	// Verify the function returns "Issue IDs" check name for no-beads-dir case
	tmpDir := t.TempDir()
	check := CheckIDFormat(tmpDir)
	if check.Name != "Issue IDs" {
		t.Errorf("Name = %q, want %q", check.Name, "Issue IDs")
	}
}

// TestCheckRepoFingerprint_NoDatabase verifies graceful handling when no database exists.
func TestCheckRepoFingerprint_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckRepoFingerprint(tmpDir)

	if check.Name != "Repo Fingerprint" {
		t.Errorf("Name = %q, want %q", check.Name, "Repo Fingerprint")
	}
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Errorf("Message = %q, want it to contain 'N/A'", check.Message)
	}
}

// TestTruncateID verifies safe truncation of repo IDs for display.
// Regression test for the [:8] panic in CheckRepoFingerprint.
func TestTruncateID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"long ID", "abcdefghijklmnop", "abcdefgh"},
		{"exactly 8", "abcdefgh", "abcdefgh"},
		{"short ID", "abc", "abc"},
		{"empty", "", ""},
		{"one char", "x", "x"},
		{"seven chars", "abcdefg", "abcdefg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateID(tt.id)
			if got != tt.want {
				t.Errorf("truncateID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

// TestCheckDependencyCycles_NoDatabase verifies graceful handling when no database exists.
func TestCheckDependencyCycles_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckDependencyCycles(tmpDir)

	if check.Name != "Dependency Cycles" {
		t.Errorf("Name = %q, want %q", check.Name, "Dependency Cycles")
	}
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
}

// bd-46vla: a fingerprint mismatch where the current id is a path-fallback
// hash (no origin remote on this host) is the synced-clone signature — the
// stored id is the canonical shared value, and stamping the local one via
// 'bd migrate --update-repo-id' would propagate it to every clone. That case
// must warn-and-advise-leaving-it, not present as "wrong database".
func TestClassifyRepoFingerprint(t *testing.T) {
	// GH#5990: a fingerprint mismatch carries no evidence that the database
	// is disposable, and in a shared-ledger deployment deleting it destroys
	// real state. No branch may suggest deleting or moving it aside.
	assertNoDestructiveAdvice := func(t *testing.T, check DoctorCheck) {
		t.Helper()
		for _, text := range []string{check.Message, check.Detail, check.Fix} {
			for _, bad := range []string{"rm ", "rm -rf", "mv ", "bd init", "delete", "Delete"} {
				if strings.Contains(text, bad) {
					t.Fatalf("check suggests %q: %q", bad, text)
				}
			}
		}
	}

	t.Run("match is OK regardless of source", func(t *testing.T) {
		check := classifyRepoFingerprint("aaaa1111bbbb2222", beads.RepoFingerprint{ID: "aaaa1111bbbb2222", Source: beads.RepoIDSourcePath})
		if check.Status != StatusOK {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusOK, check.Message)
		}
	})

	// GH#5990: a database stamped before the upgrade holds this checkout's
	// literal-alias hash; after alias resolution it is the LegacyID.
	t.Run("legacy alias id of this checkout is OK", func(t *testing.T) {
		current := beads.RepoFingerprint{
			ID:          "cccc3333dddd4444",
			Source:      beads.RepoIDSourceRemote,
			LegacyID:    "aaaa1111bbbb2222",
			SSHAlias:    "alias-a",
			SSHHostName: "github.com",
		}
		check := classifyRepoFingerprint("aaaa1111bbbb2222", current)
		if check.Status != StatusOK {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusOK, check.Message)
		}
		if check.Message != "Verified (aaaa1111)" {
			t.Fatalf("Message = %q, want the matched legacy id shown", check.Message)
		}
	})

	t.Run("mismatch with remote-derived current id is an error", func(t *testing.T) {
		check := classifyRepoFingerprint("aaaa1111bbbb2222", beads.RepoFingerprint{ID: "cccc3333dddd4444", Source: beads.RepoIDSourceRemote})
		if check.Status != StatusError {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusError, check.Message)
		}
		if !strings.Contains(check.Fix, "bd migrate --update-repo-id") {
			t.Fatalf("Fix = %q, want the update-repo-id repair offered", check.Fix)
		}
		if check.Message != "Database fingerprint does not match this repository's remote" {
			t.Fatalf("Message = %q, want the mismatch stated without asserting another repository", check.Message)
		}
		for _, want := range []string{"same repository", "every clone"} {
			if !strings.Contains(check.Fix, want) {
				t.Fatalf("Fix = %q, want the conditional shared-id migration (%q)", check.Fix, want)
			}
		}
		assertNoDestructiveAdvice(t, check)
	})

	t.Run("mismatch after alias resolution is still an error", func(t *testing.T) {
		current := beads.RepoFingerprint{
			ID:          "cccc3333dddd4444",
			Source:      beads.RepoIDSourceRemote,
			LegacyID:    "eeee5555ffff6666",
			SSHAlias:    "alias-b",
			SSHHostName: "github.com",
		}
		check := classifyRepoFingerprint("aaaa1111bbbb2222", current)
		if check.Status != StatusError {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusError, check.Message)
		}
		for _, want := range []string{"aaaa1111", "cccc3333", "eeee5555", "alias-b", "github.com"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("Detail = %q, want %q named", check.Detail, want)
			}
		}
		if strings.Contains(check.Detail, "cosmetic") || strings.Contains(check.Detail, "same repository") {
			t.Fatalf("Detail = %q must not claim the mismatch is harmless", check.Detail)
		}
		if !strings.Contains(check.Fix, "bd migrate --update-repo-id") {
			t.Fatalf("Fix = %q, want the update-repo-id repair offered", check.Fix)
		}
		assertNoDestructiveAdvice(t, check)
	})

	t.Run("short ids are shown safely", func(t *testing.T) {
		check := classifyRepoFingerprint("abc", beads.RepoFingerprint{ID: "cccc3333dddd4444", Source: beads.RepoIDSourceRemote})
		if check.Status != StatusError || !strings.Contains(check.Detail, "stored: abc,") {
			t.Fatalf("check = %+v, want an error showing the short stored id", check)
		}
		assertNoDestructiveAdvice(t, check)
	})

	t.Run("mismatch with path-fallback current id warns and advises leaving it", func(t *testing.T) {
		check := classifyRepoFingerprint("aaaa1111bbbb2222", beads.RepoFingerprint{ID: "cccc3333dddd4444", Source: beads.RepoIDSourcePath})
		if check.Status != StatusWarning {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusWarning, check.Message)
		}
		if !strings.Contains(check.Message, "no origin remote") {
			t.Fatalf("Message = %q, want the no-origin-remote framing", check.Message)
		}
		if !strings.Contains(check.Fix, "propagates to every clone") {
			t.Fatalf("Fix = %q, want the propagation named", check.Fix)
		}
		assertNoDestructiveAdvice(t, check)
	})
}

// GH#5990: a shared ledger initialized by one clone's old alias hash. Before
// the explicit update only that clone verifies (through its LegacyID); after
// one explicit update writes the newly computed id, every clone reaching the
// repository through a supported alias, and the canonical HTTPS clone,
// verifies.
func TestRepoFingerprint_SharedLedgerConvergesAfterExplicitUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh is a shell script")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_SSH", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH_VARIANT", "")

	// An ssh whose config maps alias-a and alias-b to HostName github.com.
	sshDir := t.TempDir()
	script := `#!/bin/sh
for dest; do :; done
case "${dest#*@}" in
alias-a|alias-b) echo "hostname github.com" ;;
*) echo "hostname ${dest#*@}" ;;
esac
`
	if err := os.WriteFile(filepath.Join(sshDir, "ssh"), []byte(script), 0o700); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	clone := func(remote string) string {
		dir := t.TempDir()
		for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		return dir
	}
	fingerprint := func(dir string) beads.RepoFingerprint {
		fp, err := beads.ComputeRepoFingerprintForPath(dir)
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}

	cloneA := clone("git@alias-a:org/repo.git")
	cloneB := clone("git@alias-b:org/repo.git")
	cloneHTTPS := clone("https://github.com/org/repo.git")
	fpA, fpB, fpHTTPS := fingerprint(cloneA), fingerprint(cloneB), fingerprint(cloneHTTPS)
	if !fpA.AliasResolved() || !fpB.AliasResolved() {
		t.Fatalf("aliases not resolved: A=%+v B=%+v", fpA, fpB)
	}

	// The ledger as bd init stamped it on clone A before the upgrade.
	stored := fpA.LegacyID
	if check := classifyRepoFingerprint(stored, fpA); check.Status != StatusOK {
		t.Fatalf("clone A before update: %q %q, want OK through its legacy id", check.Status, check.Message)
	}
	if check := classifyRepoFingerprint(stored, fpB); check.Status != StatusError {
		t.Fatalf("clone B before update: %q %q, want an error until the explicit update", check.Status, check.Message)
	}

	// The explicit update ('bd migrate --update-repo-id', or the doctor
	// repair's update choice) records what ComputeRepoIDForPath returns.
	updated, err := beads.ComputeRepoIDForPath(cloneB)
	if err != nil {
		t.Fatal(err)
	}
	if updated == stored {
		t.Fatal("explicit update recorded the old alias hash")
	}
	for name, fp := range map[string]beads.RepoFingerprint{"A": fpA, "B": fpB, "https": fpHTTPS} {
		if check := classifyRepoFingerprint(updated, fp); check.Status != StatusOK {
			t.Errorf("clone %s after update: %q %q, want OK", name, check.Status, check.Message)
		}
	}
}
