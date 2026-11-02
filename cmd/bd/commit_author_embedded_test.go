//go:build cgo

package main

import (
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
// actor bd resolved (--actor > BEADS_ACTOR > config actor > git user.name);
// GIT_AUTHOR_EMAIL, else git user.email. Name and email resolve independently.
func TestEmbeddedCommitAuthorUsesResolvedActor(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt commit author tests")
	}
	bd := buildEmbeddedBD(t)
	// initGitRepoAt sets user.name "Test" and user.email "test@test.com".
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ca")

	steps := []struct {
		name       string
		configured string // config.yaml actor; "" leaves none
		env        []string
		args       []string
		wantName   string
		wantEmail  string
	}{
		{name: "git config", wantName: "Test", wantEmail: "test@test.com"},
		{
			name: "BEADS_ACTOR over git", env: []string{"BEADS_ACTOR=Env Actor"},
			wantName: "Env Actor", wantEmail: "test@test.com",
		},
		{
			name: "--actor over BEADS_ACTOR", env: []string{"BEADS_ACTOR=Env Actor"},
			args: []string{"--actor", "Flag Actor"}, wantName: "Flag Actor", wantEmail: "test@test.com",
		},
		{
			name: "config actor over git", configured: "Cfg Actor",
			wantName: "Cfg Actor", wantEmail: "test@test.com",
		},
		{
			name: "BEADS_ACTOR over config actor", configured: "Cfg Actor", env: []string{"BEADS_ACTOR=Env Actor"},
			wantName: "Env Actor", wantEmail: "test@test.com",
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
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			writeConfiguredActor(t, beadsDir, step.configured)
			args := append(append([]string{}, step.args...), "create", "--json", "commit author: "+step.name)
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = append(commitAuthorEnv(dir), step.env...)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			if err != nil {
				t.Fatalf("bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
			}
			name, email, message := embeddedHeadAuthor(t, beadsDir)
			if !strings.HasPrefix(message, "bd: create") {
				t.Fatalf("HEAD message = %q, want the create commit", message)
			}
			if name != step.wantName || email != step.wantEmail {
				t.Errorf("HEAD author = %q <%s>, want %q <%s>", name, email, step.wantName, step.wantEmail)
			}
		})
	}
}

// commitAuthorEnv is bdEnv with no inherited author identity, git config from
// the workspace repo only, and no user-level bd config.
func commitAuthorEnv(dir string) []string {
	env := bdEnv(dir)
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_CONFIG_GLOBAL", "XDG_CONFIG_HOME"} {
		env = envWithout(env, name)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "XDG_CONFIG_HOME="+filepath.Join(dir, ".xdg"))
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

// embeddedHeadAuthor reads the author and message of the workspace's HEAD commit.
func embeddedHeadAuthor(t *testing.T, beadsDir string) (name, email, message string) {
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
	if err := db.QueryRowContext(t.Context(), "SELECT author, author_email, message FROM dolt_log LIMIT 1").Scan(&name, &email, &message); err != nil {
		t.Fatalf("query dolt_log: %v", err)
	}
	return name, email, message
}
