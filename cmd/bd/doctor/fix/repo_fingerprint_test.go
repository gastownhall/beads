package fix

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// TestRepoFingerprint_ReinitializeChoicePreservesFiles pins the GH#5990
// removal of the destructive menu option: a user answering the old "2"
// (reinitialize) input — even confirming with "y" — must not delete the
// database directory, and the menu must no longer offer reinitialization.
func TestRepoFingerprint_ReinitializeChoicePreservesFiles(t *testing.T) {
	dir := setupTestWorkspace(t)
	doltDir := filepath.Join(dir, ".beads", "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads/dolt: %v", err)
	}
	sentinel := filepath.Join(doltDir, "sentinel.json")
	if err := os.WriteFile(sentinel, []byte("{}"), 0o644); err != nil {
		t.Fatalf("failed to write sentinel: %v", err)
	}

	answers := []string{"2", "y"}
	oldReadLine := repoFingerprintReadLine
	repoFingerprintReadLine = func() (string, error) {
		if len(answers) == 0 {
			return "", nil
		}
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	runErr := RepoFingerprint(dir, false)
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured output: %v", err)
	}
	os.Stdout = oldStdout

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("answering the old reinitialize choice destroyed the database directory: %v", err)
	}
	if runErr != nil {
		t.Fatalf("RepoFingerprint(old choice 2 + confirm) returned error: %v", runErr)
	}
	if strings.Contains(string(out), "Reinitialize") {
		t.Fatalf("output must not offer reinitialization:\n%s", string(out))
	}
}
