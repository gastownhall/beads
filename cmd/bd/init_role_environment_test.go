package main

import (
	"context"
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
	for _, name := range []string{"configure", "configured", "maintainer", "not_fork", "config_lock", "config_lock_quiet"} {
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
			if name == "configure" || strings.HasPrefix(name, "config_lock") {
				wantWrites = [][2]string{{"routing.mode", "auto"}, {"routing.contributor", planning}, {"sync.remote", "upstream"}}
				if name == "configure" {
					wantRole = "contributor"
				} else if err := os.WriteFile(filepath.Join(target, ".git", "config.lock"), []byte("owned lock"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Repeating the real call proves configured/idempotent and flag precedence.
			for call := range 2 {
				var callErr error
				stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
				if err != nil {
					t.Fatal(err)
				}
				defer stdoutFile.Close()
				stderr := captureStderr(t, func() {
					// captureStderr holds the shared stdio mutex; do not nest captureStdout.
					old := os.Stdout
					os.Stdout = stdoutFile
					defer func() { os.Stdout = old }()
					callErr = autoConfigureForkContributor(t.Context(), spy, strings.HasSuffix(name, "quiet"), roleFlag)
				})
				stdout, err := os.ReadFile(stdoutFile.Name())
				if err != nil {
					t.Fatal(err)
				}
				if callErr != nil {
					t.Fatal(callErr)
				}
				wantBanner := (name == "configure" || name == "config_lock") && call == 0
				if got := strings.Contains(string(stdout), "Fork detected — configuring contributor routing\n"); got != wantBanner {
					t.Errorf("call %d: stdout = %q, want setup banner %v", call+1, stdout, wantBanner)
				}
				wantWarning := name == "config_lock" && call == 0
				if got := strings.Contains(stderr, "Warning: failed to set beads.role=contributor:"); got != wantWarning {
					t.Errorf("call %d: role warning = %q, want warning %v", call+1, stderr, wantWarning)
				}
				if !wantWarning && stderr != "" {
					t.Errorf("call %d: unexpected stderr %q", call+1, stderr)
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
