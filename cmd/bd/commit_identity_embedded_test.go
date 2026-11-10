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

// TestEmbeddedCommitIdentityHonorsGitAuthor pins embedded-mode commit
// authorship to the resolved identity (GIT_AUTHOR_*), as server mode does,
// rather than the hardcoded `beads <beads@local>` (#7628).
func TestEmbeddedCommitIdentityHonorsGitAuthor(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ci")

	cmd := exec.Command(bd, "create", "Identity probe", "--type", "task")
	cmd.Dir = dir
	cmd.Env = append(envWithout(envWithout(bdEnv(dir), "GIT_AUTHOR_NAME"), "GIT_AUTHOR_EMAIL"),
		"GIT_AUTHOR_NAME=Repro Author",
		"GIT_AUTHOR_EMAIL=repro@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}

	database := ""
	if cfg, _ := configfile.Load(beadsDir); cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()

	// Pin the create commit by its message and read its author fields: the
	// committer columns alone would pass with a session identity set and the
	// --author still hardcoded.
	var name, email string
	if err := db.QueryRowContext(t.Context(),
		"SELECT author, author_email FROM dolt_log WHERE message LIKE 'bd: create %' ORDER BY commit_order DESC LIMIT 1").
		Scan(&name, &email); err != nil {
		t.Fatalf("query dolt_log: %v", err)
	}
	if name != "Repro Author" || email != "repro@example.com" {
		t.Errorf("embedded create commit authored %q <%s>, want %q <%s>",
			name, email, "Repro Author", "repro@example.com")
	}
}
