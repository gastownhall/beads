package fix

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestRepoFingerprint_AutoYesSkipsPrompt(t *testing.T) {
	dir := setupTestWorkspace(t)

	oldReadLine := repoFingerprintReadLine
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	readCalled := false
	repoFingerprintReadLine = func() (string, error) {
		readCalled = true
		return "", nil
	}

	// autoYes=true should not call readLine for prompts.
	// It will fail to open the Dolt database (test environment),
	// but it should NOT have called readLine.
	_ = RepoFingerprint(dir, true)

	if readCalled {
		t.Fatal("expected autoYes path to skip interactive stdin read")
	}
}

func TestRepoFingerprint_SkipChoiceDoesNothing(t *testing.T) {
	dir := setupTestWorkspace(t)

	oldReadLine := repoFingerprintReadLine
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	repoFingerprintReadLine = func() (string, error) { return "s", nil }

	// Choice "s" should skip without error
	if err := RepoFingerprint(dir, false); err != nil {
		t.Fatalf("RepoFingerprint(autoYes=false, choice=s) returned error: %v", err)
	}
}

func TestRepoFingerprint_UnrecognizedInputSkips(t *testing.T) {
	dir := setupTestWorkspace(t)

	oldReadLine := repoFingerprintReadLine
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	repoFingerprintReadLine = func() (string, error) { return "x", nil }

	// Unrecognized input should skip without error
	if err := RepoFingerprint(dir, false); err != nil {
		t.Fatalf("RepoFingerprint(autoYes=false, choice=x) returned error: %v", err)
	}
}

// captureRepoFingerprintStdout runs fn with os.Stdout redirected and returns
// what it printed. Tests using it must not run in parallel.
func captureRepoFingerprintStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	defer func() { os.Stdout = oldStdout }()
	fn()
	_ = w.Close()
	os.Stdout = oldStdout
	return <-done
}

// GH#5990: a fingerprint mismatch is no evidence the database is
// disposable, and in a shared-ledger deployment it is not. The former
// reinitialize choice [2] must skip like any unrecognized input, and the
// menu must offer only update and skip.
func TestRepoFingerprint_ReinitializeChoicePreservesFiles(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	dbPath := configfile.DefaultConfig().DatabasePath(beadsDir)
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dbPath, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldReadLine := repoFingerprintReadLine
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()
	answers := []string{"2", "yes"}
	reads := 0
	repoFingerprintReadLine = func() (string, error) {
		answer := answers[reads]
		reads++
		return answer, nil
	}

	var runErr error
	out := captureRepoFingerprintStdout(t, func() {
		runErr = RepoFingerprint(dir, false)
	})
	if runErr != nil {
		t.Fatalf("RepoFingerprint(autoYes=false, choice=2) returned error: %v", runErr)
	}
	if reads != 1 {
		t.Fatalf("read %d answers, want only the menu choice (no destructive confirmation)\n%s", reads, out)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("database sentinel = %q, %v; want it preserved\n%s", data, err, out)
	}
	for _, bad := range []string{"[2]", "Reinitializ", "DELETE", "Removing"} {
		if strings.Contains(out, bad) {
			t.Fatalf("output offers or runs reinitialization (%q):\n%s", bad, out)
		}
	}
	for _, want := range []string{"[1] Update repo ID", "[s] Skip", "Choice [1/s]", "every clone", "skipping"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
