package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/storage"
)

func initRoleFixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = gitenv.ScrubRouting(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newInitRoleFixture(t *testing.T) (target, decoy, home string) {
	t.Helper()
	// These tests change CWD/environment and must remain serial.
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	target, decoy, home = t.TempDir(), t.TempDir(), t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(key, home)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{target, decoy} {
		initRoleFixtureGit(t, repo, "init", "--quiet")
		initRoleFixtureGit(t, repo, "config", "--local", "core.hooksPath", ".git/hooks")
	}
	initRoleFixtureGit(t, decoy, "config", "--local", "beads.role", "decoy-role")
	t.Chdir(target)
	return target, decoy, home
}

func preserveInitRoleInputs(t *testing.T, paths ...string) {
	t.Helper()
	env := os.Environ()
	t.Cleanup(func() {
		if !reflect.DeepEqual(os.Environ(), env) {
			t.Error("init role operation changed inherited environment")
		}
	})
	for _, path := range paths {
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
				t.Errorf("init role operation changed %s: %v", path, err)
			}
		})
	}
}

func TestInitRoleIgnoresInheritedGitRouting(t *testing.T) {
	for _, tc := range []struct {
		name, local, global, want string
	}{
		{"local", "maintainer", "", "maintainer"},
		{"local_over_global", "contributor", "maintainer", "contributor"},
		{"literal", "future role = exact", "", "future role = exact"},
		{"default_global", "", "contributor", "contributor"},
		{"absent", "", "", ""},
		{"empty", " \t ", "", ""},
	} {
		for _, poison := range []string{"repository", "inline_config"} {
			t.Run(tc.name+"/"+poison, func(t *testing.T) {
				target, decoy, home := newInitRoleFixture(t)
				if tc.local != "" {
					initRoleFixtureGit(t, target, "config", "--local", "beads.role", tc.local)
				}
				if tc.global != "" {
					initRoleFixtureGit(t, target, "config", "--global", "beads.role", tc.global)
				}
				if poison == "repository" {
					t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
					t.Setenv("GIT_WORK_TREE", decoy)
				} else {
					t.Setenv("GIT_CONFIG_COUNT", "1")
					t.Setenv("GIT_CONFIG_KEY_0", "beads.role")
					t.Setenv("GIT_CONFIG_VALUE_0", "injected-role")
				}
				preserveInitRoleInputs(t, filepath.Join(decoy, ".git", "config"), filepath.Join(home, ".gitconfig"))
				if got, ok := getBeadsRole(); got != tc.want || ok != (tc.want != "") {
					t.Errorf("getBeadsRole = %q, %v; want %q, %v", got, ok, tc.want, tc.want != "")
				}
				const literal = "custom role = exact"
				if err := setBeadsRole(literal); err != nil {
					t.Fatal(err)
				}
				if got := initRoleFixtureGit(t, target, "config", "--local", "--get", "beads.role"); got != literal {
					t.Errorf("target role = %q, want %q", got, literal)
				}
				if got, ok := getBeadsRole(); got != literal || !ok {
					t.Errorf("live role after set = %q, %v", got, ok)
				}
			})
		}
	}
	t.Run("unusable_repository", func(t *testing.T) {
		_, decoy, home := newInitRoleFixture(t)
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, ".git"), []byte("invalid gitfile\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(outside)
		t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
		t.Setenv("GIT_WORK_TREE", decoy)
		preserveInitRoleInputs(t, filepath.Join(decoy, ".git", "config"), filepath.Join(home, ".gitconfig"))
		if err := setBeadsRole("contributor"); err == nil {
			t.Error("role write in an unusable repository must fail")
		}
		if got, ok := getBeadsRole(); got != "" || ok {
			t.Errorf("role read in an unusable repository = %q, %v", got, ok)
		}
	})
}

// Only configuration calls are implemented; any other storage method panics.
type initRoleConfigSpy struct {
	storage.DoltStorage
	values map[string]string
	writes [][2]string
}

func (s *initRoleConfigSpy) GetConfig(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *initRoleConfigSpy) SetConfig(_ context.Context, key, value string) error {
	s.values[key] = value
	s.writes = append(s.writes, [2]string{key, value})
	return nil
}

func TestAutoConfigureForkContributorIgnoresInheritedGitRouting(t *testing.T) {
	for _, name := range []string{"configure", "configured", "maintainer", "not_fork"} {
		t.Run(name, func(t *testing.T) {
			target, decoy, home := newInitRoleFixture(t)
			if name != "not_fork" {
				initRoleFixtureGit(t, target, "remote", "add", "upstream", "https://example.invalid/upstream/repo.git")
			}
			initRoleFixtureGit(t, target, "config", "--local", "beads.role", "maintainer")
			planning := filepath.Join(home, ".beads-planning")
			if err := os.Mkdir(planning, 0750); err != nil {
				t.Fatal(err)
			}
			// Pin absent YAML under this fixture; the auto path must not create a DB.
			t.Setenv("BEADS_DIR", filepath.Join(target, ".beads"))
			for _, key := range []string{"BD_ROUTING_CONTRIBUTOR", "BEADS_ROUTING_CONTRIBUTOR"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			initConfigForTest(t)
			t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
			t.Setenv("GIT_WORK_TREE", decoy)
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "beads.role")
			t.Setenv("GIT_CONFIG_VALUE_0", "injected-role")
			preserveInitRoleInputs(t, filepath.Join(decoy, ".git", "config"), filepath.Join(home, ".gitconfig"))
			spy := &initRoleConfigSpy{values: map[string]string{}}
			if name == "configured" {
				spy.values["routing.contributor"] = "existing-planning"
			}
			roleFlag := ""
			if name == "maintainer" {
				roleFlag = "maintainer"
			}
			var wantWrites [][2]string
			wantRole := "maintainer"
			if name == "configure" {
				wantRole = "contributor"
				wantWrites = [][2]string{{"routing.mode", "auto"}, {"routing.contributor", planning}, {"sync.remote", "upstream"}}
			}
			// Repeating the real call proves configured/idempotent and flag precedence.
			for range 2 {
				if err := autoConfigureForkContributor(t.Context(), spy, true, roleFlag); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(spy.writes, wantWrites) {
					t.Errorf("configuration writes = %v, want %v", spy.writes, wantWrites)
				}
				if got := initRoleFixtureGit(t, target, "config", "--local", "--get", "beads.role"); got != wantRole {
					t.Errorf("automatic target role = %q, want %q", got, wantRole)
				}
			}
			if entries, err := os.ReadDir(planning); err != nil || len(entries) != 0 {
				t.Errorf("precreated planning directory changed: %v, %v", entries, err)
			}
		})
	}
}

func TestCheckPushAccessIgnoresInheritedGitRouting(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		wantPush  bool
	}{
		{"ssh", "git@example.invalid:target/repo.git", true},
		{"https", "https://example.invalid/target/repo.git", false},
		{"file", "file:///target/repo.git", true},
		{"missing", "", false},
	} {
		for _, poison := range []string{"repository", "inline_config"} {
			t.Run(tc.name+"/"+poison, func(t *testing.T) {
				target, decoy, home := newInitRoleFixture(t)
				const injected = "git@example.invalid:decoy/repo.git"
				if tc.url != "" {
					initRoleFixtureGit(t, target, "remote", "add", "origin", tc.url)
				}
				initRoleFixtureGit(t, decoy, "remote", "add", "origin", injected)
				if poison == "repository" {
					t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
					t.Setenv("GIT_WORK_TREE", decoy)
				} else {
					key, value := "remote.origin.url", injected
					if tc.url != "" {
						key, value = "url."+injected+".insteadOf", tc.url
					}
					t.Setenv("GIT_CONFIG_COUNT", "1")
					t.Setenv("GIT_CONFIG_KEY_0", key)
					t.Setenv("GIT_CONFIG_VALUE_0", value)
				}
				preserveInitRoleInputs(t, filepath.Join(target, ".git", "config"), filepath.Join(decoy, ".git", "config"), filepath.Join(home, ".gitconfig"))
				if push, url := checkPushAccess(); push != tc.wantPush || url != tc.url {
					t.Errorf("checkPushAccess = %v, %q; want %v, %q", push, url, tc.wantPush, tc.url)
				}
			})
		}
	}
}

// Stop the actual wizard at its first persistence call, before changing config.
type initOriginStopStore struct{ storage.DoltStorage }

func (*initOriginStopStore) SetConfig(context.Context, string, string) error {
	return context.Canceled
}

func TestContributorWizardUsesTargetOrigin(t *testing.T) {
	target, decoy, home := newInitRoleFixture(t)
	const origin = "https://example.invalid/target/repo.git"
	initRoleFixtureGit(t, target, "remote", "add", "origin", origin)
	initRoleFixtureGit(t, target, "remote", "add", "upstream", "https://example.invalid/upstream/repo.git")
	initRoleFixtureGit(t, decoy, "remote", "add", "origin", "git@example.invalid:decoy/repo.git")
	// "n" cancels the decoy prompt or selects the target's existing planning directory.
	planning := filepath.Join(target, "n")
	if err := os.Mkdir(planning, 0750); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(home, "stdin")
	if err := os.WriteFile(inputPath, []byte("n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	t.Setenv("BEADS_DIR", "")
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	preserveInitRoleInputs(t, filepath.Join(target, ".git", "config"), filepath.Join(decoy, ".git", "config"), filepath.Join(home, ".gitconfig"))
	var wizardErr error
	out := captureStdout(t, func() error {
		oldStdin := os.Stdin
		os.Stdin = input
		defer func() { os.Stdin = oldStdin }()
		wizardErr = runContributorWizard(t.Context(), &initOriginStopStore{})
		return nil
	})
	if !errors.Is(wizardErr, context.Canceled) || !strings.Contains(wizardErr.Error(), "failed to set routing mode") {
		t.Errorf("wizard did not reach the owned persistence stop: %v", wizardErr)
	}
	for _, want := range []string{"Read-only access to origin (" + origin + ")", "Planning repo path [press Enter for default]:", "Using existing planning repository"} {
		if !strings.Contains(out, want) {
			t.Errorf("wizard output lacks %q: %s", want, out)
		}
	}
	if strings.Contains(out, "separate planning repo anyway?") || strings.Contains(out, "Setup canceled") {
		t.Errorf("wizard followed decoy push-access branch: %s", out)
	}
	if entries, err := os.ReadDir(planning); err != nil || len(entries) != 0 {
		t.Errorf("precreated planning directory changed: %v, %v", entries, err)
	}
}
