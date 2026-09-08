//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMetricsOffRejectsUnsafeUserRootsInNativeProcess exercises the real bd
// executable from a sentinel checkout. It models the profile environment a
// Git-for-Windows hook can inherit and proves an explicit user-global write
// fails before creating a cwd-relative "~" or APPDATA tree.
func TestMetricsOffRejectsUnsafeUserRootsInNativeProcess(t *testing.T) {
	sentinel := t.TempDir()
	bdBinary := buildBDForInitTests(t)
	validHome := filepath.Join(sentinel, "bootstrap-home")
	validAppData := filepath.Join(validHome, "AppData", "Roaming")
	if err := os.MkdirAll(validAppData, 0o755); err != nil {
		t.Fatalf("create isolated bootstrap profile: %v", err)
	}

	baseEnv := filteredWindowsUserConfigEnv(os.Environ())
	initEnv := append(baseEnv,
		"HOME="+validHome,
		"USERPROFILE="+validHome,
		"APPDATA="+validAppData,
		"XDG_CONFIG_HOME="+filepath.Join(validHome, ".config"),
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BEADS_TEST_MODE=1",
	)

	gitInit := exec.Command("git", "init", "--quiet")
	gitInit.Dir = sentinel
	gitInit.Env = initEnv
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	gitHooks := exec.Command("git", "config", "core.hooksPath", ".git/hooks")
	gitHooks.Dir = sentinel
	gitHooks.Env = initEnv
	if out, err := gitHooks.CombinedOutput(); err != nil {
		t.Fatalf("isolate git hooks: %v\n%s", err, out)
	}

	tests := []struct {
		name       string
		profileEnv []string
	}{
		{
			name:       "literal tilde profile",
			profileEnv: []string{"HOME=~", "USERPROFILE=~", "APPDATA=relative-appdata"},
		},
		{
			name:       "missing native profile",
			profileEnv: []string{"HOME=/c/Users/hook-user", "APPDATA=relative-appdata"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := append([]string{}, baseEnv...)
			env = append(env, tt.profileEnv...)
			env = append(env,
				"BD_DISABLE_METRICS=1",
				"BD_DISABLE_EVENT_FLUSH=1",
				"BEADS_TEST_MODE=1",
			)

			cmd := exec.Command(bdBinary, "metrics", "off")
			cmd.Dir = sentinel
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("metrics off unexpectedly succeeded:\n%s", out)
			}
			if !strings.Contains(string(out), "not an absolute native path") &&
				!strings.Contains(string(out), "cannot find home directory") {
				t.Fatalf("metrics off did not report path resolution failure: %v\n%s", err, out)
			}

			for _, relativeRoot := range []string{"~", "relative-appdata"} {
				if _, statErr := os.Stat(filepath.Join(sentinel, relativeRoot)); !os.IsNotExist(statErr) {
					t.Errorf("unsafe relative root %q was materialized: %v", relativeRoot, statErr)
				}
			}
		})
	}

	t.Run("hook skips implicit bootstrap when profile is missing", func(t *testing.T) {
		env := append([]string{}, baseEnv...)
		env = append(env,
			"HOME=/c/Users/hook-user",
			"APPDATA=relative-appdata",
			"BD_DISABLE_EVENT_FLUSH=1",
			"BEADS_TEST_MODE=1",
		)

		cmd := exec.Command(bdBinary, "hooks", "run", "pre-commit")
		cmd.Dir = sentinel
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hooks run pre-commit failed after user config resolution was skipped: %v\n%s", err, out)
		}

		for _, relativeRoot := range []string{"~", "relative-appdata"} {
			if _, statErr := os.Stat(filepath.Join(sentinel, relativeRoot)); !os.IsNotExist(statErr) {
				t.Errorf("unsafe relative root %q was materialized by hook process: %v", relativeRoot, statErr)
			}
		}
	})
}

func filteredWindowsUserConfigEnv(env []string) []string {
	blocked := map[string]bool{
		"HOME":                          true,
		"USERPROFILE":                   true,
		"HOMEDRIVE":                     true,
		"HOMEPATH":                      true,
		"APPDATA":                       true,
		"LOCALAPPDATA":                  true,
		"XDG_CONFIG_HOME":               true,
		"BEADS_DIR":                     true,
		"BEADS_DB":                      true,
		"BD_DB":                         true,
		"BD_DISABLE_METRICS":            true,
		"BD_DISABLE_EVENT_FLUSH":        true,
		"DO_NOT_TRACK":                  true,
		"BEADS_TEST_MODE":               true,
		"BEADS_TEST_IGNORE_REPO_CONFIG": true,
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if blocked[strings.ToUpper(key)] {
			continue
		}
		out = append(out, entry)
	}
	return out
}
