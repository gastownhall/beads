//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// GH#7628 AC-4: the CLI installs its resolved identity (actor chain, owner
// email) on every embedded store it opens, so embedded Dolt commits carry
// who ran the command instead of the anonymous beads <beads@local> pair.
// Each scenario runs a real `bd create` in a fresh initialized workspace
// under exactly the identity sources the scenario means to test, then reads
// the commit that create minted.

// newestCreateCommitAuthor reads the newest "bd: create" commit's author pair
// from an initialized embedded workspace. commit_order, not date, orders the
// rows; the message filter picks the create commit itself, so a later
// autocommit net cannot mask it.
func newestCreateCommitAuthor(t *testing.T, beadsDir string) (author, email string) {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	if err := db.QueryRowContext(t.Context(),
		"SELECT author, author_email FROM dolt_log WHERE message LIKE 'bd: create %' ORDER BY commit_order DESC LIMIT 1").
		Scan(&author, &email); err != nil {
		t.Fatalf("query dolt_log: %v", err)
	}
	return author, email
}

// bdCreateEnv runs `bd create --json` in dir with bdEnv plus the scenario's
// environment overrides, and fatals unless the create succeeded. bdEnv's
// strip is what makes BEADS_ACTOR/BD_ACTOR scenarios hermetic; the identity
// variables are removed here so the subprocess cannot inherit the test
// process's own git-author environment.
func bdCreateEnv(t *testing.T, bd, dir string, env map[string]string, args ...string) {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"create", "--json"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "USER"} {
		cmd.Env = envWithout(cmd.Env, key)
	}
	for key, value := range env {
		cmd.Env = envWithout(cmd.Env, key)
		if value != "" {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}
}

// createScenarioWithActor initializes a fresh workspace, runs one
// `bd create` under the scenario's env and args, and returns the author pair
// of the commit that create minted. unsetGitIdentity drops the repo-local
// git user.name/email that initGitRepoAt seeds, for the no-source scenarios.
func createScenarioWithAuthor(t *testing.T, bd string, env map[string]string, unsetGitIdentity bool, args ...string) (author, email string) {
	t.Helper()
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cia")
	if unsetGitIdentity {
		for _, key := range []string{"user.name", "user.email"} {
			cmd := exec.Command("git", "config", "--local", "--unset", key)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git config --unset %s: %v\n%s", key, err, out)
			}
		}
	}
	bdCreateEnv(t, bd, dir, env, args...)
	return newestCreateCommitAuthor(t, beadsDir)
}

func TestEmbeddedCommitAuthorUsesResolvedActor(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)

	assertAuthor := func(t *testing.T, gotAuthor, gotEmail, wantAuthor, wantEmail string) {
		t.Helper()
		if gotAuthor != wantAuthor || gotEmail != wantEmail {
			t.Fatalf("create commit author = %q <%q>, want %q <%q>", gotAuthor, gotEmail, wantAuthor, wantEmail)
		}
	}

	t.Run("git_default", func(t *testing.T) {
		author, email := createScenarioWithAuthor(t, bd, nil, false, "git default identity", "--type", "task")
		// initGitRepoAt seeds git user.name "Test" / user.email
		// "test@test.com"; with no other source, that pair attributes
		// the commit.
		assertAuthor(t, author, email, "Test", "test@test.com")
	})

	t.Run("actor_flag", func(t *testing.T) {
		author, email := createScenarioWithAuthor(t, bd, nil, false, "flag identity", "--type", "task", "--actor", "Flag Actor")
		assertAuthor(t, author, email, "Flag Actor", "test@test.com")
	})

	t.Run("beads_actor_env", func(t *testing.T) {
		author, email := createScenarioWithAuthor(t, bd, map[string]string{"BEADS_ACTOR": "Env Actor"}, false, "env identity", "--type", "task")
		assertAuthor(t, author, email, "Env Actor", "test@test.com")
	})

	t.Run("bd_actor_env_deprecated_alias", func(t *testing.T) {
		author, email := createScenarioWithAuthor(t, bd, map[string]string{"BD_ACTOR": "Deprecated Actor"}, false, "deprecated env identity", "--type", "task")
		assertAuthor(t, author, email, "Deprecated Actor", "test@test.com")
	})

	t.Run("config_actor", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cia")
		cfgPath := filepath.Join(beadsDir, "config.yaml")
		f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatalf("open config.yaml: %v", err)
		}
		if _, err := f.WriteString("\nactor: Config Actor\n"); err != nil {
			t.Fatalf("append actor key: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close config.yaml: %v", err)
		}
		bdCreateEnv(t, bd, dir, nil, "config identity", "--type", "task")
		author, email := newestCreateCommitAuthor(t, beadsDir)
		assertAuthor(t, author, email, "Config Actor", "test@test.com")
	})

	t.Run("explicit_unknown_actor_is_preserved", func(t *testing.T) {
		author, email := createScenarioWithAuthor(t, bd, nil, false, "explicit unknown", "--type", "task", "--actor", "unknown")
		// A deliberately configured literal "unknown" is an identity;
		// only the chain's terminal sentinel is dropped.
		assertAuthor(t, author, email, "unknown", "test@test.com")
	})

	t.Run("no_sources_fall_back_to_beads", func(t *testing.T) {
		author, email := createScenarioWithAuthor(t, bd, nil, true, "no identity anywhere", "--type", "task")
		// No actor source at all: the chain's terminal "unknown" is a
		// sentinel, not an identity, and the store keeps its anonymous
		// beads <beads@local> pair.
		assertAuthor(t, author, email, "beads", "beads@local")
	})

	t.Run("git_author_env_outranks_resolved_actor", func(t *testing.T) {
		author, email := createScenarioWithAuthor(t, bd,
			map[string]string{"GIT_AUTHOR_NAME": "Env Author", "GIT_AUTHOR_EMAIL": "env@example.com"},
			false, "env wins", "--type", "task", "--actor", "Flag Actor")
		assertAuthor(t, author, email, "Env Author", "env@example.com")
	})

	t.Run("email_resolves_independently", func(t *testing.T) {
		author, email := createScenarioWithAuthor(t, bd,
			map[string]string{"GIT_AUTHOR_EMAIL": "only@example.com"},
			false, "email only", "--type", "task")
		// The actor chain names the author; the author environment names
		// the email, and neither blocks the other.
		assertAuthor(t, author, email, "Test", "only@example.com")
	})
}

// TestActorExplicitlyUnknown pins the sentinel mapping directly, without an
// engine: only a source that literally names "unknown" keeps it, and every
// other terminal resolution is reported as not explicit.
func TestActorExplicitlyUnknown(t *testing.T) {
	// The test process's own actor sources are irrelevant here: the rungs
	// are read from their sources, and the empty workspace means --actor is
	// unchanged, env vars are what the scenario sets, and config.yaml is
	// whatever the test process loaded.
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("BD_ACTOR", "")
	t.Setenv("USER", "")
	if actorExplicitlyUnknown() {
		t.Fatal("empty sources: actorExplicitlyUnknown() = true, want false")
	}
	t.Setenv("BEADS_ACTOR", "unknown")
	if !actorExplicitlyUnknown() {
		t.Fatal("BEADS_ACTOR=unknown: actorExplicitlyUnknown() = false, want true")
	}
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("BD_ACTOR", "unknown")
	if !actorExplicitlyUnknown() {
		t.Fatal("BD_ACTOR=unknown: actorExplicitlyUnknown() = false, want true")
	}
	t.Setenv("BD_ACTOR", "")
	// config.GetString("actor"), the --actor flag and the git rung are
	// covered by the subprocess scenarios above; here the env rungs carry
	// the mapping's weight.
}