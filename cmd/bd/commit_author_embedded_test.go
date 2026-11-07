//go:build cgo

package main

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedCommitAuthorUsesResolvedActor pins the author of the Dolt commit
// a bd write records in embedded mode (GH#7628): GIT_AUTHOR_NAME, else the
// actor bd resolved (--actor > BEADS_ACTOR > BD_ACTOR > config actor > git
// user.name > $USER), else beads; GIT_AUTHOR_EMAIL, else git user.email, else
// beads@local. Name and email resolve independently. Every step requires a new
// commit of the write it ran and reads that commit's author.
func TestEmbeddedCommitAuthorUsesResolvedActor(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt commit author tests")
	}
	bd := buildEmbeddedBD(t)

	type step struct {
		name       string
		configured string // config.yaml actor; "" leaves none
		env        []string
		args       []string
		wantName   string
		wantEmail  string
	}
	runCreateSteps := func(t *testing.T, dir, beadsDir string, steps []step) {
		t.Helper()
		for _, step := range steps {
			t.Run(step.name, func(t *testing.T) {
				writeConfiguredActor(t, beadsDir, step.configured)
				before, _ := newestCommitLike(t, beadsDir, "bd: create %")
				args := append(append([]string{}, step.args...), "create", "--json", "commit author: "+step.name)
				runCommitAuthorBD(t, bd, dir, step.env, args...)
				requireNewCommitAuthor(t, beadsDir, "bd: create %", before, step.wantName, step.wantEmail)
			})
		}
	}

	t.Run("workspace with git identity", func(t *testing.T) {
		// initGitRepoAt sets user.name "Test" and user.email "test@test.com".
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ca")
		runCreateSteps(t, dir, beadsDir, []step{
			{name: "git config", wantName: "Test", wantEmail: "test@test.com"},
			{
				name: "BEADS_ACTOR over git", env: []string{"BEADS_ACTOR=Env Actor"},
				wantName: "Env Actor", wantEmail: "test@test.com",
			},
			{
				name: "BD_ACTOR alias over git", env: []string{"BD_ACTOR=Alias Actor"},
				wantName: "Alias Actor", wantEmail: "test@test.com",
			},
			{
				name: "BEADS_ACTOR over BD_ACTOR", env: []string{"BEADS_ACTOR=Env Actor", "BD_ACTOR=Alias Actor"},
				wantName: "Env Actor", wantEmail: "test@test.com",
			},
			{
				name: "--actor over BEADS_ACTOR", env: []string{"BEADS_ACTOR=Env Actor"},
				args: []string{"--actor", "Flag Actor"}, wantName: "Flag Actor", wantEmail: "test@test.com",
			},
			{
				name: "explicit --actor unknown is kept",
				args: []string{"--actor", "unknown"}, wantName: "unknown", wantEmail: "test@test.com",
			},
			{
				name: "config actor over git", configured: "Cfg Actor",
				wantName: "Cfg Actor", wantEmail: "test@test.com",
			},
			{
				name: "BD_ACTOR over config actor", configured: "Cfg Actor", env: []string{"BD_ACTOR=Alias Actor"},
				wantName: "Alias Actor", wantEmail: "test@test.com",
			},
			{
				name: "GIT_AUTHOR_NAME over --actor, email from git", configured: "Cfg Actor",
				env:  []string{"GIT_AUTHOR_NAME=Alice Author"},
				args: []string{"--actor", "Flag Actor"}, wantName: "Alice Author", wantEmail: "test@test.com",
			},
			{
				name: "GIT_AUTHOR_EMAIL with the actor's name", configured: "Cfg Actor",
				env:      []string{"GIT_AUTHOR_EMAIL=alice@example.com"},
				wantName: "Cfg Actor", wantEmail: "alice@example.com",
			},
			{
				name: "explicit beads pair opts out", env: []string{"GIT_AUTHOR_NAME=beads", "GIT_AUTHOR_EMAIL=beads@local"},
				args: []string{"--actor", "Flag Actor"}, wantName: "beads", wantEmail: "beads@local",
			},
		})
	})

	t.Run("workspace without git identity", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cn")
		unsetLocalGitIdentity(t, dir)
		runCreateSteps(t, dir, beadsDir, []step{
			{name: "no source at all", wantName: "beads", wantEmail: "beads@local"},
			{
				name: "USER when git names no one", env: []string{"USER=sysuser"},
				wantName: "sysuser", wantEmail: "beads@local",
			},
			{
				name: "config actor with no email source", configured: "Cfg Actor", env: []string{"USER=sysuser"},
				wantName: "Cfg Actor", wantEmail: "beads@local",
			},
			{
				name: "BEADS_ACTOR=unknown is kept", env: []string{"BEADS_ACTOR=unknown"},
				wantName: "unknown", wantEmail: "beads@local",
			},
			{
				name: "GIT_AUTHOR_NAME empty once cleaned falls through", env: []string{"GIT_AUTHOR_NAME=<>()", "USER=sysuser"},
				wantName: "sysuser", wantEmail: "beads@local",
			},
		})
	})

	t.Run("bd init commit", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		runCommitAuthorBD(t, bd, dir, []string{"BEADS_ACTOR=Init Actor"}, "init", "--quiet", "--prefix", "ci")
		requireNewCommitAuthor(t, filepath.Join(dir, ".beads"), "bd init", commitAuthorRow{}, "Init Actor", "test@test.com")
	})

	t.Run("routed write", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "crs")
		targetDir := filepath.Join(dir, "target-repo")
		if err := os.MkdirAll(targetDir, 0o750); err != nil {
			t.Fatal(err)
		}
		initGitRepoAt(t, targetDir)
		runBDInit(t, bd, targetDir, "--prefix", "crt")
		issue := bdCreate(t, bd, targetDir, "routed target issue")
		route := `{"prefix":"crt-","path":"target-repo"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, ".beads", "routes.jsonl"), []byte(route), 0o644); err != nil {
			t.Fatalf("write routes.jsonl: %v", err)
		}

		targetBeadsDir := filepath.Join(targetDir, ".beads")
		before, _ := newestCommitLike(t, targetBeadsDir, "bd: update %")
		runCommitAuthorBD(t, bd, dir, []string{"BEADS_ACTOR=Routed Actor"}, "update", issue.ID, "--title", "updated through route")
		requireNewCommitAuthor(t, targetBeadsDir, "bd: update %", before, "Routed Actor", "test@test.com")
	})
}

// runCommitAuthorBD runs bd in dir under commitAuthorEnv plus env, and fatals
// unless it succeeds.
func runCommitAuthorBD(t *testing.T, bd, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(commitAuthorEnv(dir), env...)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
}

// commitAuthorEnv is bdEnv with no inherited author identity or $USER, git
// config from the workspace repo only, and no user-level bd config. bdEnv
// already drops BEADS_* and BD_* and points HOME at dir.
func commitAuthorEnv(dir string) []string {
	env := bdEnv(dir)
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_CONFIG_GLOBAL", "XDG_CONFIG_HOME", "USER"} {
		env = envWithout(env, name)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "XDG_CONFIG_HOME="+filepath.Join(dir, ".xdg"))
}

// unsetLocalGitIdentity removes the user.name / user.email initGitRepoAt
// seeds, so no git config names anyone.
func unsetLocalGitIdentity(t *testing.T, dir string) {
	t.Helper()
	for _, key := range []string{"user.name", "user.email"} {
		cmd := exec.Command("git", "config", "--local", "--unset", key)
		cmd.Dir = dir
		cmd.Env = commitAuthorEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config --unset %s: %v\n%s", key, err, out)
		}
	}
}

// writeConfiguredActor sets (or, for "", removes) config.yaml's actor line.
func writeConfiguredActor(t *testing.T, beadsDir, actor string) {
	t.Helper()
	path := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "actor:") {
			lines = append(lines, line)
		}
	}
	if actor != "" {
		lines = append(lines, "actor: "+actor+"\n")
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

type commitAuthorRow struct {
	hash, name, email, message string
}

// newestCommitLike reads the newest commit whose message matches the SQL LIKE
// pattern, ordered by commit_order, so a later commit (an auto-commit net, a
// recompute) cannot mask the write under test. ok is false when none matches.
func newestCommitLike(t *testing.T, beadsDir, like string) (row commitAuthorRow, ok bool) {
	t.Helper()
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load %s config: %v", beadsDir, err)
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), cfg.GetDoltDatabase(), "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	err = db.QueryRowContext(t.Context(),
		"SELECT commit_hash, author, author_email, message FROM dolt_log WHERE message LIKE ? ORDER BY commit_order DESC LIMIT 1", like).
		Scan(&row.hash, &row.name, &row.email, &row.message)
	if errors.Is(err, sql.ErrNoRows) {
		return commitAuthorRow{}, false
	}
	if err != nil {
		t.Fatalf("query dolt_log: %v", err)
	}
	return row, true
}

// requireNewCommitAuthor asserts a commit matching like newer than before
// exists and records name <email>.
func requireNewCommitAuthor(t *testing.T, beadsDir, like string, before commitAuthorRow, name, email string) {
	t.Helper()
	got, ok := newestCommitLike(t, beadsDir, like)
	if !ok || got.hash == before.hash {
		t.Fatalf("no new commit matching %q (newest before: %q)", like, before.message)
	}
	if got.name != name || got.email != email {
		t.Errorf("commit %q author = %q <%s>, want %q <%s>", got.message, got.name, got.email, name, email)
	}
}
