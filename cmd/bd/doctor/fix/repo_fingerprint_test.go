package fix

import (
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

func TestRepoFingerprint_Choice2RefusesEscapingAndSymlinkedDoltDataDir(t *testing.T) {
	// An inherited BEADS_DOLT_DATA_DIR would select, and exempt, a real store.
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(victim, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(beadsDir, victim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_data_dir":"`+filepath.ToSlash(rel)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	oldReadLine := repoFingerprintReadLine
	defer func() { repoFingerprintReadLine = oldReadLine }()
	// The refusal comes before the DELETE confirmation; answering "n" there
	// keeps a regression from deleting anything.
	inputs := []string{"2", "n"}
	reads := 0
	repoFingerprintReadLine = func() (string, error) {
		reads++
		return inputs[min(reads, len(inputs))-1], nil
	}

	err = RepoFingerprint(dir, false)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected outside error, got %v", err)
	}
	if reads != 1 {
		t.Fatalf("expected the refusal before the DELETE confirmation (1 read), got %d reads", reads)
	}
	if data, rerr := os.ReadFile(sentinel); rerr != nil || string(data) != "keep" {
		t.Fatalf("sentinel removed: %q, %v", data, rerr)
	}
}
