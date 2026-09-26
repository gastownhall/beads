package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestConfigGetYamlOnlyKeyLocation pins GH#5049: `bd config get --json` for a
// yaml-only key must report the value's REAL provenance instead of a constant
// "config.yaml" for every key, including defaults and unset keys. Unlike
// TestViperSourceLabel (which only pins the pure label-mapping switch), this
// drives configGetCmd.RunE itself — the actual command path a caller
// exercises — so a regression at the config.go call site (e.g. hardcoding the
// location again) is caught even if the label-mapping helper stays correct.
func TestConfigGetYamlOnlyKeyLocation(t *testing.T) {
	const key = "dolt.auto-commit"

	tests := []struct {
		name       string
		writeYaml  bool
		envVal     string // "" = leave BD_DOLT_AUTO_COMMIT unset
		wantSource string
	}{
		{
			name:       "unset key reports default, not config.yaml",
			wantSource: "default",
		},
		{
			name:       "key written into config.yaml reports config.yaml",
			writeYaml:  true,
			wantSource: "config.yaml",
		},
		{
			name:       "env-set key reports env with the variable name",
			envVal:     "batch",
			wantSource: "env: BD_DOLT_AUTO_COMMIT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatalf("failed to create .beads dir: %v", err)
			}
			configPath := filepath.Join(beadsDir, "config.yaml")
			yamlContent := ""
			if tt.writeYaml {
				yamlContent = key + ": \"batch\"\n"
			}
			if err := os.WriteFile(configPath, []byte(yamlContent), 0600); err != nil {
				t.Fatalf("failed to write config.yaml: %v", err)
			}

			t.Chdir(tmpDir)
			t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
			if tt.envVal != "" {
				t.Setenv("BD_DOLT_AUTO_COMMIT", tt.envVal)
			} else {
				// t.Setenv registers the restore of any ambient value before
				// os.Unsetenv clears it for the test — os.Unsetenv alone
				// would leak the unset past this subtest.
				t.Setenv("BD_DOLT_AUTO_COMMIT", "")
				os.Unsetenv("BD_DOLT_AUTO_COMMIT")
			}

			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			savedJSONOutput := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = savedJSONOutput })

			out := captureStdout(t, func() error {
				return configGetCmd.RunE(configGetCmd, []string{key})
			})

			var result struct {
				Location string `json:"location"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("failed to parse `bd config get --json` output %q: %v", out, err)
			}
			if result.Location != tt.wantSource {
				t.Errorf("location = %q, want %q (output: %s)", result.Location, tt.wantSource, out)
			}
		})
	}
}
