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

	"github.com/steveyegge/beads/internal/configfile"
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

// Reach the automatic read, then stop either flow before config persistence.
type initPlanningStopStore struct {
	storage.DoltStorage
	reads, writes []string
}

func (s *initPlanningStopStore) GetConfig(_ context.Context, key string) (string, error) {
	s.reads = append(s.reads, key)
	return "", nil
}

func (s *initPlanningStopStore) SetConfig(_ context.Context, key, value string) error {
	s.writes = append(s.writes, key+"="+value)
	return context.Canceled
}

func TestContributorPlanningGitIgnoresInheritedRouting(t *testing.T) {
	for _, flow := range []string{"wizard", "automatic"} {
		t.Run(flow, func(t *testing.T) {
			target, decoy, home := newInitRoleFixture(t)
			for _, key := range []string{"BEADS_DIR", "BD_ROUTING_CONTRIBUTOR", "BEADS_ROUTING_CONTRIBUTOR", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_DATABASE", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("DOLT_ROOT_PATH", t.TempDir())
			t.Setenv("BD_EVENTS_JOURNAL", "false")
			initConfigForTest(t)
			if err := os.Unsetenv("BEADS_DIR"); err != nil {
				t.Fatal(err)
			}
			hooks := filepath.Join(home, "empty-hooks")
			if err := os.Mkdir(hooks, 0750); err != nil {
				t.Fatal(err)
			}
			global := filepath.Join(home, ".gitconfig")
			for _, setting := range [][2]string{{"user.name", "Planning Fixture"}, {"user.email", "planning@example.invalid"}, {"commit.gpgSign", "false"}, {"core.hooksPath", hooks}, {"init.defaultBranch", "main"}} {
				initRoleFixtureGit(t, target, "config", "--file", global, setting[0], setting[1])
			}
			for _, repo := range []string{target, decoy} {
				initRoleFixtureGit(t, repo, "config", "core.hooksPath", hooks)
				initRoleFixtureGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
				if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("owned seed\n"), 0600); err != nil {
					t.Fatal(err)
				}
				initRoleFixtureGit(t, repo, "add", "seed.txt")
				initRoleFixtureGit(t, repo, "commit", "-m", "fixture seed")
			}
			initRoleFixtureGit(t, target, "remote", "add", "origin", "https://example.invalid/target/repo.git")
			initRoleFixtureGit(t, target, "remote", "add", "upstream", "https://example.invalid/upstream/repo.git")
			planning := filepath.Join(home, ".beads-planning")
			if flow == "wizard" {
				planning = filepath.Join(t.TempDir(), "planning with spaces")
			}
			if _, err := os.Stat(planning); !os.IsNotExist(err) {
				t.Fatalf("planning path must be absent: %v", err)
			}
			cfg, err := configfile.Load(filepath.Join(planning, ".beads"))
			if err != nil || cfg != nil {
				t.Fatalf("planning metadata must be absent: %v, %v", cfg, err)
			}
			cfg = normalizeLoadedConfig(cfg)
			if cfg.GetBackend() != configfile.BackendDolt || cfg.IsDoltServerMode() || cfg.IsDoltProxiedServerMode() || cfg.GetDoltDatabase() != configfile.DefaultDoltDatabase {
				t.Fatal("planning factory must select the owned embedded default")
			}
			inputPath := filepath.Join(home, "stdin")
			if err := os.WriteFile(inputPath, []byte(planning+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Commit identity is retained while repository routing is scrubbed.
			t.Setenv("GIT_AUTHOR_NAME", "Inherited Planning Author")
			t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
			t.Setenv("GIT_WORK_TREE", decoy)
			t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
			t.Setenv("GIT_CONFIG_COUNT", "2")
			t.Setenv("GIT_CONFIG_KEY_0", "user.name")
			t.Setenv("GIT_CONFIG_VALUE_0", "Decoy Author")
			t.Setenv("GIT_CONFIG_KEY_1", "user.email")
			t.Setenv("GIT_CONFIG_VALUE_1", "decoy@example.invalid")
			paths := []string{global, inputPath}
			for _, repo := range []string{target, decoy} {
				for _, name := range []string{"config", "index", "HEAD", "refs/heads/main"} {
					paths = append(paths, filepath.Join(repo, ".git", name))
				}
			}
			preserveInitRoleInputs(t, paths...)
			store := &initPlanningStopStore{}
			var flowErr error
			if flow == "wizard" {
				input, err := os.Open(inputPath)
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				out := captureStdout(t, func() error {
					old := os.Stdin
					os.Stdin = input
					defer func() { os.Stdin = old }()
					flowErr = runContributorWizard(t.Context(), store)
					return nil
				})
				if !strings.Contains(out, "Planning repository created") || len(store.reads) != 0 {
					t.Errorf("wizard creation branch = %q, reads %v", out, store.reads)
				}
			} else {
				// This really attempts optional embedded initialization with CGO;
				// without CGO its unsupported error is ignored by the same caller.
				flowErr = autoConfigureForkContributor(t.Context(), store, true, "")
				if !reflect.DeepEqual(store.reads, []string{"routing.contributor"}) {
					t.Errorf("automatic configuration reads = %v", store.reads)
				}
			}
			if !errors.Is(flowErr, context.Canceled) || !reflect.DeepEqual(store.writes, []string{"routing.mode=auto"}) {
				t.Fatalf("planning flow did not reach first persistence stop: %v, writes %v", flowErr, store.writes)
			}
			wantGit, err := os.Stat(filepath.Join(planning, ".git"))
			if err != nil || !wantGit.IsDir() {
				t.Fatalf("planning Git directory missing: %v", err)
			}
			actualGit := initRoleFixtureGit(t, planning, "rev-parse", "--absolute-git-dir")
			gotGit, err := os.Stat(actualGit)
			if err != nil || !os.SameFile(wantGit, gotGit) {
				t.Fatalf("planning Git directory = %q, %v", actualGit, err)
			}
			if beads, err := os.Stat(filepath.Join(planning, ".beads")); err != nil || !beads.IsDir() {
				t.Fatalf("planning .beads directory missing: %v", err)
			}
			if flow == "wizard" {
				if names := initRoleFixtureGit(t, planning, "ls-tree", "--name-only", "HEAD"); names != "README.md" {
					t.Errorf("planning commit tree = %q", names)
				}
				want := "Initial commit: beads planning repository\nInherited Planning Author\nplanning@example.invalid\nPlanning Fixture\nplanning@example.invalid"
				if got := initRoleFixtureGit(t, planning, "show", "-s", "--format=%s%n%an%n%ae%n%cn%n%ce", "HEAD"); got != want {
					t.Errorf("planning commit identity = %q, want %q", got, want)
				}
				readme, err := os.ReadFile(filepath.Join(planning, "README.md"))
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("git", "show", "HEAD:README.md")
				cmd.Dir, cmd.Env = planning, gitenv.ScrubRouting(os.Environ())
				if committed, err := cmd.Output(); err != nil || string(committed) != string(readme) {
					t.Errorf("planning README commit differs from created bytes: %v", err)
				}
			}
		})
	}
}

func TestInitRoleGitRepoUsesSelectedDirectory(t *testing.T) {
	for _, tc := range []struct {
		name            string
		want, inherited bool
	}{
		{"ordinary", true, true},
		{"canceled", false, true},
		{"invalid_routing", true, false},
		{"bare", true, true},
		{"nested", true, true},
		{"ceiling", true, false},
		{"nonrepo_decoy", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, decoy, home := newInitRoleFixture(t)
			switch tc.name {
			case "invalid_routing":
				t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing.git"))
			case "bare":
				bare := t.TempDir()
				initRoleFixtureGit(t, bare, "init", "--bare", "--quiet")
				t.Chdir(bare)
			case "nested", "ceiling":
				nested := filepath.Join(target, "nested")
				if err := os.Mkdir(nested, 0750); err != nil {
					t.Fatal(err)
				}
				t.Chdir(nested)
				if tc.name == "ceiling" {
					t.Setenv("GIT_CEILING_DIRECTORIES", target)
				}
			case "nonrepo_decoy":
				t.Chdir(t.TempDir())
				if isGitRepo() {
					t.Fatal("fixture must start outside a repository")
				}
				t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
			}
			preserveInitRoleInputs(t, filepath.Join(target, ".git", "config"), filepath.Join(decoy, ".git", "config"), filepath.Join(home, ".gitconfig"))
			if got := isGitRepo(); got != tc.inherited {
				t.Fatalf("inherited repository precondition = %v, want %v", got, tc.inherited)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.name == "canceled" {
				cancel()
			}
			if got := isInitRoleGitRepo(ctx); got != tc.want {
				t.Errorf("role repository probe = %v, want %v", got, tc.want)
			}
		})
	}
}
