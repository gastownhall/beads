package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func stubCopilotVersion(t *testing.T, lookPath func(string) (string, error), version func() ([]byte, error)) {
	t.Helper()
	origLookPath := copilotLookPath
	origVersion := copilotVersion
	copilotLookPath = lookPath
	copilotVersion = version
	t.Cleanup(func() {
		copilotLookPath = origLookPath
		copilotVersion = origVersion
	})
}

func TestCheckCopilotNotConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	stubCopilotVersion(t,
		func(string) (string, error) { return "", errors.New("not found") },
		func() ([]byte, error) { return nil, errors.New("should not run") },
	)

	check := CheckCopilot(tmpDir)
	if check.Status != StatusOK {
		t.Fatalf("expected ok, got %s", check.Status)
	}
	if check.Name != "Copilot CLI Integration" {
		t.Fatalf("unexpected check name: %s", check.Name)
	}
}

func TestCheckCopilotConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	stubCopilotVersion(t,
		func(string) (string, error) { return "/usr/local/bin/copilot", nil },
		func() ([]byte, error) { return []byte("copilot 1.0.5\n"), nil },
	)

	if err := os.MkdirAll(filepath.Join(tmpDir, ".github", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, copilotInstructionsFile), []byte("<!-- BEGIN BEADS INTEGRATION profile:minimal hash:abc -->\n<!-- END BEADS INTEGRATION -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, copilotHooksFile), []byte(`{"version":1,"hooks":{"sessionStart":[{"type":"command","bash":"bd prime"}],"preCompact":[{"type":"command","bash":"bd prime"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckCopilot(tmpDir)
	if check.Status != StatusOK {
		t.Fatalf("expected ok, got %s: %s", check.Status, check.Message)
	}
}

func TestCheckCopilotPartialInstall(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, copilotInstructionsFile), []byte("<!-- BEGIN BEADS INTEGRATION -->\n<!-- END BEADS INTEGRATION -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckCopilot(tmpDir)
	if check.Status != StatusWarning {
		t.Fatalf("expected warning, got %s", check.Status)
	}
}

func TestCheckCopilotOldVersion(t *testing.T) {
	tmpDir := t.TempDir()
	stubCopilotVersion(t,
		func(string) (string, error) { return "/usr/local/bin/copilot", nil },
		func() ([]byte, error) { return []byte("copilot 1.0.4\n"), nil },
	)

	if err := os.MkdirAll(filepath.Join(tmpDir, ".github", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, copilotInstructionsFile), []byte("<!-- BEGIN BEADS INTEGRATION -->\n<!-- END BEADS INTEGRATION -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, copilotHooksFile), []byte(`{"version":1,"hooks":{"sessionStart":[{"type":"command","bash":"bd prime"}],"preCompact":[{"type":"command","bash":"bd prime"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckCopilot(tmpDir)
	if check.Status != StatusWarning {
		t.Fatalf("expected warning, got %s", check.Status)
	}
}

func TestCheckCopilotHooksHealth(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".github", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, copilotHooksFile), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckCopilotHooksHealth(tmpDir)
	if check.Status != StatusError {
		t.Fatalf("expected error, got %s", check.Status)
	}
}

func TestCheckCopilotHookCompleteness(t *testing.T) {
	t.Run("both hooks present", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmpDir, ".github", "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, copilotHooksFile), []byte(`{"version":1,"hooks":{"sessionStart":[{"type":"command","bash":"bd prime"}],"preCompact":[{"type":"command","bash":"bd prime --stealth"}]}}`), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckCopilotHookCompleteness(tmpDir)
		if check.Status != StatusOK {
			t.Fatalf("expected ok, got %s", check.Status)
		}
	})

	t.Run("missing preCompact", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmpDir, ".github", "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, copilotHooksFile), []byte(`{"version":1,"hooks":{"sessionStart":[{"type":"command","bash":"bd prime"}]}}`), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckCopilotHookCompleteness(tmpDir)
		if check.Status != StatusWarning {
			t.Fatalf("expected warning, got %s", check.Status)
		}
	})
}

func TestCheckDocumentationBdPrimeReferenceIncludesCopilotInstructions(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, copilotInstructionsFile), []byte("Run bd prime for workflow context."), 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckDocumentationBdPrimeReference(tmpDir)
	if check.Status != StatusOK && check.Status != StatusWarning {
		t.Fatalf("expected ok or warning, got %s", check.Status)
	}
}
