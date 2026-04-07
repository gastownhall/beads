package main

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestShowConfigYAMLOverrides_EnvVarDetection(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	_ = config.Initialize()

	// Set an env var override using BD_ prefix
	t.Setenv("BD_TEST_KEY", "env-value")

	dbConfig := map[string]string{
		"test-key": "db-value",
	}

	// Redirect stdout to avoid test noise
	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = old }()

	// Should not panic
	showConfigYAMLOverrides(dbConfig)
}

func TestShowConfigYAMLOverrides_LegacyEnvVar(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	_ = config.Initialize()

	// Set a legacy BEADS_ prefix env var
	t.Setenv("BEADS_TEST_KEY", "legacy-value")

	dbConfig := map[string]string{
		"test-key": "db-value",
	}

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = old }()

	showConfigYAMLOverrides(dbConfig)
}

func TestShowConfigYAMLOverrides_EmptyDB(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	_ = config.Initialize()

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = old }()

	// Empty DB config — should still check yaml keys and not panic
	showConfigYAMLOverrides(map[string]string{})
}

func TestShowConfigYAMLOverrides_NoOverrides(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	_ = config.Initialize()

	dbConfig := map[string]string{
		"some-key": "some-value",
	}

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = old }()

	// No env vars set, should complete without issues
	showConfigYAMLOverrides(dbConfig)
}
